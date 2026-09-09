package action

import (
	"context"
	"fmt"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type State uint8

const (
	StatePending State = iota + 1
	StateClaimed
	StateExecuting
	StateSucceeded
	StateFailedRetryable
	StateFailedTerminal
	StateUnknownOutcome
)

type Lease string

type StoredAction struct {
	Ref             agent.DispatchRef
	InvocationID    identity.InvocationID
	ResponseID      identity.MessageID
	Text            string
	State           State
	Lease           Lease
	CompletedAt     *time.Time
	ProviderReceipt string
}

type Store interface {
	Claim(context.Context, agent.DispatchRef, time.Time) (StoredAction, error)
	MarkExecuting(context.Context, agent.DispatchRef, Lease, time.Time) error
	Complete(context.Context, agent.DispatchRef, Lease, string, time.Time) error
	Release(context.Context, agent.DispatchRef, Lease, agent.ErrorCode, bool, time.Time) error
	MarkUnknown(context.Context, agent.DispatchRef, Lease, agent.ErrorCode, time.Time) error
}

type SendAuthorizer interface {
	AuthorizeSend(context.Context, agent.Key) error
}

type SendTextRequest struct {
	Key      agent.Key
	ActionID identity.ActionID
	Text     string
}

type SendTextResult struct {
	ProviderReceipt string
}

type TextSender interface {
	Ready() bool
	SendText(context.Context, SendTextRequest) (SendTextResult, error)
}

type Observer interface {
	ObserveDelivery(agent.DeliveryStatus, agent.ErrorCode)
}

type DiscardObserver struct{}

func (DiscardObserver) ObserveDelivery(agent.DeliveryStatus, agent.ErrorCode) {}

type Dispatcher struct {
	store      Store
	policy     SendAuthorizer
	sender     TextSender
	clock      agent.Clock
	retryDelay time.Duration
	observer   Observer
}

func NewDispatcher(store Store, policy SendAuthorizer, sender TextSender, clock agent.Clock, observer Observer) (*Dispatcher, error) {
	if store == nil || policy == nil || sender == nil || clock == nil || observer == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create response dispatcher", fmt.Errorf("store, policy, sender, clock, and observer are required"))
	}
	return &Dispatcher{store: store, policy: policy, sender: sender, clock: clock, retryDelay: time.Second, observer: observer}, nil
}

func (dispatcher *Dispatcher) Dispatch(ctx context.Context, ref agent.DispatchRef) (result agent.DeliveryResult, resultErr error) {
	defer func() { dispatcher.observer.ObserveDelivery(result.Status, agent.CodeOf(resultErr)) }()
	if err := ref.Key.Validate(); err != nil || ref.ActionID.IsZero() {
		return agent.DeliveryResult{}, agent.NewError(agent.ErrorInvalidArgument, "dispatch response", fmt.Errorf("valid dispatch reference is required"))
	}
	action, err := dispatcher.store.Claim(ctx, ref, dispatcher.clock.Now())
	if err != nil {
		return agent.DeliveryResult{}, err
	}
	switch action.State {
	case StateSucceeded:
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliverySucceeded, CompletedAt: cloneTime(action.CompletedAt)}, nil
	case StateFailedTerminal:
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryFailedTerminal, CompletedAt: cloneTime(action.CompletedAt)}, agent.NewError(agent.ErrorProviderFailure, "dispatch response", fmt.Errorf("delivery previously failed terminally"))
	case StateUnknownOutcome:
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryUnknownOutcome, CompletedAt: cloneTime(action.CompletedAt)}, agent.NewError(agent.ErrorUnknownOutcome, "dispatch response", fmt.Errorf("delivery outcome is unknown"))
	case StateExecuting, StatePending, StateFailedRetryable:
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryPending}, agent.NewError(agent.ErrorDeliveryPending, "dispatch response", fmt.Errorf("delivery is owned by another worker"))
	case StateClaimed:
		// Continue below.
	default:
		return agent.DeliveryResult{}, agent.NewError(agent.ErrorIntegrityFailure, "dispatch response", fmt.Errorf("invalid action state"))
	}

	if err := dispatcher.policy.AuthorizeSend(ctx, ref.Key); err != nil {
		_ = dispatcher.release(action, agent.CodeOf(err), false)
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryFailedTerminal}, err
	}
	if !dispatcher.sender.Ready() {
		_ = dispatcher.release(action, agent.ErrorNotReady, true)
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryPending}, agent.NewError(agent.ErrorNotReady, "dispatch response", fmt.Errorf("text sender is not ready"))
	}
	now := dispatcher.clock.Now()
	if err := dispatcher.store.MarkExecuting(ctx, ref, action.Lease, now); err != nil {
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryPending}, err
	}
	sent, sendErr := dispatcher.sender.SendText(ctx, SendTextRequest{Key: ref.Key, ActionID: ref.ActionID, Text: action.Text})
	if sendErr != nil {
		code := agent.CodeOf(sendErr)
		if code == agent.ErrorCancelled || code == agent.ErrorTimeout || code == agent.ErrorUnavailable || code == agent.ErrorUnknownOutcome {
			_ = dispatcher.markUnknown(action, code)
			return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryUnknownOutcome}, agent.NewError(agent.ErrorUnknownOutcome, "dispatch response", sendErr)
		}
		// Once native send has begun, an unclassified provider failure is also
		// ambiguous. The conversation core never guesses by sending it again.
		_ = dispatcher.markUnknown(action, code)
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryUnknownOutcome}, agent.NewError(agent.ErrorUnknownOutcome, "dispatch response", sendErr)
	}
	completedAt := dispatcher.clock.Now()
	if err := dispatcher.store.Complete(ctx, ref, action.Lease, sent.ProviderReceipt, completedAt); err != nil {
		// Provider accepted the send but the receipt did not commit. This is an
		// unknown outcome and must not become another send on retry.
		_ = dispatcher.markUnknown(action, agent.ErrorStorageFailure)
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryUnknownOutcome}, agent.NewError(agent.ErrorUnknownOutcome, "record delivery receipt", err)
	}
	return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliverySucceeded, CompletedAt: &completedAt}, nil
}

func (dispatcher *Dispatcher) release(action StoredAction, code agent.ErrorCode, retryable bool) error {
	retryAt := dispatcher.clock.Now()
	if retryable {
		retryAt = retryAt.Add(dispatcher.retryDelay)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return dispatcher.store.Release(ctx, action.Ref, action.Lease, code, retryable, retryAt)
}

func (dispatcher *Dispatcher) markUnknown(action StoredAction, code agent.ErrorCode) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return dispatcher.store.MarkUnknown(ctx, action.Ref, action.Lease, code, dispatcher.clock.Now())
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
