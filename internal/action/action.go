package action

import (
	"context"
	"fmt"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

type State uint8

// State values are stored in outbound_actions.state. 2 (claimed) and 5
// (retryable) were lease states; migration 021 turned them back into pending.
const (
	StatePending        State = 1
	StateExecuting      State = 3
	StateSucceeded      State = 4
	StateFailedTerminal State = 6
	StateUnknownOutcome State = 7
)

type StoredAction struct {
	Ref              agent.DispatchRef
	InvocationID     identity.InvocationID
	ResponseID       identity.MessageID
	ReplyToMessageID identity.MessageID
	Text             string
	State            State
	CompletedAt      *time.Time
	ProviderReceipt  string
}

// Store is the reply outbox. Start is the only claim: it moves a pending row
// to executing with a conditional update, so two callers can never both send
// it. One process owns the database, so there is no lease to expire; an
// executing row found at startup was cut off mid-send and becomes unknown.
type Store interface {
	Load(context.Context, agent.DispatchRef) (StoredAction, error)
	Start(context.Context, agent.DispatchRef, time.Time) error
	Complete(context.Context, agent.DispatchRef, string, time.Time) error
	FailTerminal(context.Context, agent.DispatchRef, agent.ErrorCode, time.Time) error
	MarkUnknown(context.Context, agent.DispatchRef, agent.ErrorCode, time.Time) error
}

type SendAuthorizer interface {
	AuthorizeSend(context.Context, agent.Key) error
}

type SendTextRequest struct {
	Key             agent.Key
	ActionID        identity.ActionID
	Text            string
	QuotedMessageID identity.MessageID
	// Choices, when set, are sent as quick-reply buttons under Text (a quiz).
	// A tap comes back as the tapped choice's text.
	Choices []string
}

// SendCopyCodeRequest sends Code behind a single "copy" button.
type SendCopyCodeRequest struct {
	Key  agent.Key
	Code string
}

// Button is one quick-reply button. Tapping it sends ID back as the user's
// message, so a slash-command ID ("/trigger mention off") re-enters that command.
type Button struct {
	ID    string
	Label string
}

// Menu is a list button: tapping it opens Rows, and picking a row sends the
// row's ID back like a quick-reply button.
type Menu struct {
	Title string
	Rows  []MenuRow
}

// MenuRow is one choice in a Menu; Description is an optional second line.
type MenuRow struct {
	ID          string
	Title       string
	Description string
}

// SendButtonsRequest sends Text, and an optional Footer line, with up to
// MaxButtons quick-reply buttons and menus in all.
type SendButtonsRequest struct {
	Key      agent.Key
	ActionID identity.ActionID
	Text     string
	Footer   string
	Buttons  []Button
	Menus    []Menu
}

const (
	MaxButtons  = 10
	MaxMenuRows = 10
)

type SendTextResult struct {
	ProviderReceipt string
}

type TextSender interface {
	Ready() bool
	SendText(context.Context, SendTextRequest) (SendTextResult, error)
	// SendCopyCode is a best-effort follow-up; its error never changes the
	// outcome of the reply it follows.
	SendCopyCode(context.Context, SendCopyCodeRequest) error
}

type Observer interface {
	ObserveDelivery(agent.DeliveryStatus, agent.ErrorCode)
}

type DiscardObserver struct{}

func (DiscardObserver) ObserveDelivery(agent.DeliveryStatus, agent.ErrorCode) {}

type Dispatcher struct {
	store    Store
	policy   SendAuthorizer
	sender   TextSender
	clock    agent.Clock
	observer Observer
}

func NewDispatcher(store Store, policy SendAuthorizer, sender TextSender, clock agent.Clock, observer Observer) (*Dispatcher, error) {
	if store == nil || policy == nil || sender == nil || clock == nil || observer == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create response dispatcher", fmt.Errorf("store, policy, sender, clock, and observer are required"))
	}
	return &Dispatcher{store: store, policy: policy, sender: sender, clock: clock, observer: observer}, nil
}

// Dispatch sends a planned reply at most once. A reply that cannot be sent
// yet (the account is offline) stays pending; the runtime dispatches pending
// replies again whenever the account reconnects.
func (dispatcher *Dispatcher) Dispatch(ctx context.Context, ref agent.DispatchRef) (result agent.DeliveryResult, resultErr error) {
	defer func() { dispatcher.observer.ObserveDelivery(result.Status, agent.CodeOf(resultErr)) }()
	if err := ref.Key.Validate(); err != nil || ref.ActionID.IsZero() {
		return agent.DeliveryResult{}, agent.NewError(agent.ErrorInvalidArgument, "dispatch response", fmt.Errorf("valid dispatch reference is required"))
	}
	action, err := dispatcher.store.Load(ctx, ref)
	if err != nil {
		return agent.DeliveryResult{}, err
	}
	pending := agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryPending}
	switch action.State {
	case StateSucceeded:
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliverySucceeded, CompletedAt: action.CompletedAt}, nil
	case StateFailedTerminal:
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryFailedTerminal, CompletedAt: action.CompletedAt}, agent.NewError(agent.ErrorProviderFailure, "dispatch response", fmt.Errorf("delivery previously failed terminally"))
	case StateUnknownOutcome:
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryUnknownOutcome, CompletedAt: action.CompletedAt}, agent.NewError(agent.ErrorUnknownOutcome, "dispatch response", fmt.Errorf("delivery outcome is unknown"))
	case StateExecuting:
		return pending, agent.NewError(agent.ErrorDeliveryPending, "dispatch response", fmt.Errorf("delivery is already in progress"))
	case StatePending:
		// Continue below.
	default:
		return agent.DeliveryResult{}, agent.NewError(agent.ErrorIntegrityFailure, "dispatch response", fmt.Errorf("invalid action state"))
	}

	if err := dispatcher.policy.AuthorizeSend(ctx, ref.Key); err != nil {
		record(func(ctx context.Context) error {
			return dispatcher.store.FailTerminal(ctx, ref, agent.CodeOf(err), dispatcher.clock.Now())
		})
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryFailedTerminal}, err
	}
	if !dispatcher.sender.Ready() {
		return pending, agent.NewError(agent.ErrorNotReady, "dispatch response", fmt.Errorf("text sender is not ready"))
	}
	if err := dispatcher.store.Start(ctx, ref, dispatcher.clock.Now()); err != nil {
		if agent.IsCode(err, agent.ErrorConflict) {
			return pending, agent.NewError(agent.ErrorDeliveryPending, "dispatch response", fmt.Errorf("delivery is already in progress"))
		}
		return pending, err
	}
	text, choices := SplitChoices(action.Text)
	sent, sendErr := dispatcher.sender.SendText(ctx, SendTextRequest{Key: ref.Key, ActionID: ref.ActionID, Text: text, QuotedMessageID: action.ReplyToMessageID, Choices: choices})
	if sendErr != nil {
		// The request may have reached WhatsApp. Never send it a second time.
		code := agent.CodeOf(sendErr)
		record(func(ctx context.Context) error {
			return dispatcher.store.MarkUnknown(ctx, ref, code, dispatcher.clock.Now())
		})
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryUnknownOutcome}, agent.NewError(agent.ErrorUnknownOutcome, "dispatch response", fmt.Errorf("send text unknown outcome: %w", sendErr))
	}
	completedAt := dispatcher.clock.Now()
	if err := dispatcher.store.Complete(ctx, ref, sent.ProviderReceipt, completedAt); err != nil {
		record(func(ctx context.Context) error {
			return dispatcher.store.MarkUnknown(ctx, ref, agent.ErrorStorageFailure, dispatcher.clock.Now())
		})
		return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliveryUnknownOutcome}, agent.NewError(agent.ErrorUnknownOutcome, "record delivery receipt", fmt.Errorf("complete storage failed: %w", err))
	}
	if code := FirstCodeBlock(text); code != "" {
		_ = dispatcher.sender.SendCopyCode(ctx, SendCopyCodeRequest{Key: ref.Key, Code: code})
	}
	return agent.DeliveryResult{ActionID: ref.ActionID, Status: agent.DeliverySucceeded, CompletedAt: &completedAt}, nil
}

// record saves an outcome even when the caller's context is already done.
// A failed write leaves the row executing, which the next startup turns into
// unknown: the reply is still never sent twice.
func record(write func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = write(ctx)
}
