// Package effect owns typed native WhatsApp effects. It exposes no string
// command, provider address, or provider DTO to the rest of the application.
package effect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

const MaxEmojiBytes = 64

type Kind uint8

const (
	KindReact Kind = iota + 1
	KindDeleteMessage
	KindMarkRead
	KindSetChatPresence
)

type PresenceState string

const (
	PresenceComposing PresenceState = "composing"
	PresencePaused    PresenceState = "paused"
)

type Effect interface {
	isEffect()
	Validate() error
	Kind() Kind
	Capability() policy.Capability
	Durable() bool
}

type React struct {
	TargetMessageID identity.MessageID
	Emoji           string
}

func (React) isEffect()                     {}
func (effect React) Kind() Kind             { return KindReact }
func (React) Capability() policy.Capability { return policy.CapabilityMessageReact }
func (React) Durable() bool                 { return true }
func (effect React) Validate() error {
	if effect.TargetMessageID.IsZero() || strings.TrimSpace(effect.Emoji) == "" || !utf8.ValidString(effect.Emoji) || len(effect.Emoji) > MaxEmojiBytes {
		return agent.NewError(agent.ErrorInvalidArgument, "validate reaction effect", fmt.Errorf("target and bounded emoji are required"))
	}
	return nil
}

type DeleteMessage struct{ TargetMessageID identity.MessageID }

func (DeleteMessage) isEffect()                     {}
func (DeleteMessage) Kind() Kind                    { return KindDeleteMessage }
func (DeleteMessage) Capability() policy.Capability { return policy.CapabilityMessageDelete }
func (DeleteMessage) Durable() bool                 { return true }
func (effect DeleteMessage) Validate() error {
	if effect.TargetMessageID.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "validate delete effect", fmt.Errorf("target message is required"))
	}
	return nil
}

type MarkRead struct{ TargetMessageID identity.MessageID }

func (MarkRead) isEffect()                     {}
func (MarkRead) Kind() Kind                    { return KindMarkRead }
func (MarkRead) Capability() policy.Capability { return policy.CapabilityMessageMarkRead }
func (MarkRead) Durable() bool                 { return false }
func (effect MarkRead) Validate() error {
	if effect.TargetMessageID.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "validate mark-read effect", fmt.Errorf("target message is required"))
	}
	return nil
}

type SetChatPresence struct{ State PresenceState }

func (SetChatPresence) isEffect()                     {}
func (SetChatPresence) Kind() Kind                    { return KindSetChatPresence }
func (SetChatPresence) Capability() policy.Capability { return policy.CapabilityChatPresence }
func (SetChatPresence) Durable() bool                 { return false }
func (effect SetChatPresence) Validate() error {
	if effect.State != PresenceComposing && effect.State != PresencePaused {
		return agent.NewError(agent.ErrorInvalidArgument, "validate presence effect", fmt.Errorf("presence state is invalid"))
	}
	return nil
}

type Ref struct {
	Key      agent.Key
	EffectID identity.EffectID
}

func (ref Ref) Validate() error {
	if err := ref.Key.Validate(); err != nil || ref.EffectID.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "validate effect reference", fmt.Errorf("agent key and effect ID are required"))
	}
	return nil
}

type PlanRequest struct {
	Ref          Ref
	InvocationID identity.InvocationID
	Principal    policy.Principal
	Effect       Effect
}

// DigestPlan makes an idempotency collision detectable without persisting an
// untyped payload blob. It includes provenance and scope as well as the typed
// effect fields.
func DigestPlan(request PlanRequest) ([32]byte, error) {
	if err := request.Validate(); err != nil {
		return [32]byte{}, err
	}
	var canonical bytes.Buffer
	canonical.WriteString("wazzapagent.typed-effect.v1")
	writeDigestField(&canonical, request.Ref.Key.TenantID.String())
	writeDigestField(&canonical, request.Ref.Key.AccountID.String())
	writeDigestField(&canonical, request.Ref.Key.ChatID.String())
	writeDigestField(&canonical, request.Ref.EffectID.String())
	writeDigestField(&canonical, request.InvocationID.String())
	canonical.WriteByte(byte(request.Principal.Kind))
	writeDigestField(&canonical, request.Principal.ParticipantID.String())
	writeDigestField(&canonical, request.Principal.LID.String())
	writeDigestField(&canonical, request.Principal.InvocationID.String())
	canonical.WriteByte(byte(request.Effect.Kind()))
	switch typed := request.Effect.(type) {
	case React:
		writeDigestField(&canonical, typed.TargetMessageID.String())
		writeDigestField(&canonical, typed.Emoji)
	case DeleteMessage:
		writeDigestField(&canonical, typed.TargetMessageID.String())
	case MarkRead:
		writeDigestField(&canonical, typed.TargetMessageID.String())
	case SetChatPresence:
		writeDigestField(&canonical, string(typed.State))
	default:
		return [32]byte{}, agent.NewError(agent.ErrorInvalidArgument, "digest effect plan", fmt.Errorf("effect type is not supported"))
	}
	return sha256.Sum256(canonical.Bytes()), nil
}

func writeDigestField(buffer *bytes.Buffer, value string) {
	_ = binary.Write(buffer, binary.BigEndian, uint32(len(value)))
	buffer.WriteString(value)
}

func (request PlanRequest) Validate() error {
	if err := request.Ref.Validate(); err != nil || request.InvocationID.IsZero() || request.Effect == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "validate effect plan", fmt.Errorf("reference, invocation, and effect are required"))
	}
	if err := request.Principal.Validate(); err != nil {
		return err
	}
	if request.Ref.Key != request.Principal.Key() {
		return agent.NewError(agent.ErrorInvalidArgument, "validate effect plan", fmt.Errorf("principal scope does not match effect scope"))
	}
	if (request.Principal.Kind == policy.PrincipalModel || request.Principal.Kind == policy.PrincipalRecovery) && request.Principal.InvocationID != request.InvocationID {
		return agent.NewError(agent.ErrorInvalidArgument, "validate effect plan", fmt.Errorf("principal invocation does not match effect invocation"))
	}
	return request.Effect.Validate()
}

type State uint8

const (
	StatePending State = iota + 1
	StateClaimed
	StateExecuting
	StateSucceeded
	StateFailedTerminal
	StateUnknownOutcome
	StateSkipped
)

type Lease string

type Stored struct {
	Request         PlanRequest
	State           State
	Lease           Lease
	ProviderReceipt string
	CompletedAt     *time.Time
}

type Store interface {
	Plan(context.Context, PlanRequest, time.Time) (Stored, error)
	Claim(context.Context, Ref, time.Time) (Stored, error)
	Requeue(context.Context, Ref, Lease, time.Time) error
	MarkExecuting(context.Context, Ref, Lease, time.Time) error
	Complete(context.Context, Ref, Lease, string, time.Time) error
	FailTerminal(context.Context, Ref, Lease, agent.ErrorCode, time.Time) error
	MarkUnknown(context.Context, Ref, Lease, agent.ErrorCode, time.Time) error
	Skip(context.Context, Ref, Lease, agent.ErrorCode, time.Time) error
}

type Authorizer interface {
	AuthorizeEffect(context.Context, policy.EffectAuthorization) error
}

type Sender interface {
	Ready() bool
	ExecuteEffect(context.Context, Stored) (providerReceipt string, err error)
}

// Dispatcher is an outbox state machine for durable effects. Ephemeral
// mark-read and presence hints are persisted for auditing/planning but are
// never retried: an interrupted or failed attempt becomes skipped.
type Dispatcher struct {
	store      Store
	authorizer Authorizer
	sender     Sender
	clock      agent.Clock
}

func NewDispatcher(store Store, authorizer Authorizer, sender Sender, clock agent.Clock) (*Dispatcher, error) {
	if store == nil || authorizer == nil || sender == nil || clock == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create effect dispatcher", fmt.Errorf("store, authorizer, sender, and clock are required"))
	}
	return &Dispatcher{store: store, authorizer: authorizer, sender: sender, clock: clock}, nil
}

func (dispatcher *Dispatcher) Plan(ctx context.Context, request PlanRequest) (Stored, error) {
	if err := request.Validate(); err != nil {
		return Stored{}, err
	}
	return dispatcher.store.Plan(ctx, request, dispatcher.clock.Now())
}

func (dispatcher *Dispatcher) Dispatch(ctx context.Context, ref Ref) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	stored, err := dispatcher.store.Claim(ctx, ref, dispatcher.clock.Now())
	if err != nil {
		return err
	}
	switch stored.State {
	case StateSucceeded, StateFailedTerminal, StateUnknownOutcome, StateSkipped:
		return terminalStateError(stored.State)
	case StateClaimed:
		// Continue below: this dispatcher owns the returned lease.
	case StatePending, StateExecuting:
		return agent.NewError(agent.ErrorDeliveryPending, "dispatch effect", fmt.Errorf("effect is owned by another worker"))
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "dispatch effect", fmt.Errorf("effect state is invalid"))
	}
	// No provider call has happened while the row is merely claimed. A
	// disconnected account therefore returns the row to pending rather than
	// converting safe recovery into a terminal denial.
	if !dispatcher.sender.Ready() {
		return dispatcher.requeuePreExecution(stored, agent.NewError(agent.ErrorNotReady, "dispatch effect", fmt.Errorf("effect sender is not ready")))
	}
	if err := dispatcher.authorizer.AuthorizeEffect(ctx, policy.EffectAuthorization{
		Key: stored.Request.Ref.Key, Principal: stored.Request.Principal, Capability: stored.Request.Effect.Capability(),
	}); err != nil {
		if retryablePreExecutionError(err) {
			return dispatcher.requeuePreExecution(stored, err)
		}
		return dispatcher.finalizePreExecution(stored, agent.CodeOf(err), err)
	}
	if !dispatcher.sender.Ready() {
		return dispatcher.requeuePreExecution(stored, agent.NewError(agent.ErrorNotReady, "dispatch effect", fmt.Errorf("effect sender is not ready")))
	}
	if err := dispatcher.store.MarkExecuting(ctx, ref, stored.Lease, dispatcher.clock.Now()); err != nil {
		return err
	}
	receipt, executeErr := dispatcher.sender.ExecuteEffect(ctx, stored)
	if executeErr != nil {
		if stored.Request.Effect.Durable() {
			_ = dispatcher.store.MarkUnknown(context.Background(), ref, stored.Lease, agent.CodeOf(executeErr), dispatcher.clock.Now())
			return agent.NewError(agent.ErrorUnknownOutcome, "dispatch effect", executeErr)
		}
		_ = dispatcher.store.Skip(context.Background(), ref, stored.Lease, agent.CodeOf(executeErr), dispatcher.clock.Now())
		return executeErr
	}
	if err := dispatcher.store.Complete(ctx, ref, stored.Lease, receipt, dispatcher.clock.Now()); err != nil {
		if stored.Request.Effect.Durable() {
			_ = dispatcher.store.MarkUnknown(context.Background(), ref, stored.Lease, agent.ErrorStorageFailure, dispatcher.clock.Now())
			return agent.NewError(agent.ErrorUnknownOutcome, "record effect receipt", err)
		}
		_ = dispatcher.store.Skip(context.Background(), ref, stored.Lease, agent.ErrorStorageFailure, dispatcher.clock.Now())
		return err
	}
	return nil
}

// DispatchEffect adapts the Agent-owned replay reference without exposing the
// effect outbox's richer type to Agent. Both references contain only durable
// internal IDs, never a provider address or raw payload.
func (dispatcher *Dispatcher) DispatchEffect(ctx context.Context, ref agent.EffectDispatchRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	return dispatcher.Dispatch(ctx, Ref{Key: ref.Key, EffectID: ref.EffectID})
}

func (dispatcher *Dispatcher) finalizePreExecution(stored Stored, code agent.ErrorCode, resultErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if stored.Request.Effect.Durable() {
		_ = dispatcher.store.FailTerminal(ctx, stored.Request.Ref, stored.Lease, code, dispatcher.clock.Now())
	} else {
		_ = dispatcher.store.Skip(ctx, stored.Request.Ref, stored.Lease, code, dispatcher.clock.Now())
	}
	return resultErr
}

func (dispatcher *Dispatcher) requeuePreExecution(stored Stored, resultErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := dispatcher.store.Requeue(ctx, stored.Request.Ref, stored.Lease, dispatcher.clock.Now()); err != nil {
		return err
	}
	return resultErr
}

func retryablePreExecutionError(err error) bool {
	switch agent.CodeOf(err) {
	case agent.ErrorNotReady, agent.ErrorTimeout, agent.ErrorUnavailable, agent.ErrorProviderFailure, agent.ErrorInternal:
		return true
	default:
		return false
	}
}

func terminalStateError(state State) error {
	switch state {
	case StateSucceeded:
		return nil
	case StateUnknownOutcome:
		return agent.NewError(agent.ErrorUnknownOutcome, "dispatch effect", fmt.Errorf("effect outcome is unknown"))
	case StateSkipped:
		return agent.NewError(agent.ErrorUnsupported, "dispatch effect", fmt.Errorf("ephemeral effect was skipped"))
	default:
		return agent.NewError(agent.ErrorProviderFailure, "dispatch effect", fmt.Errorf("effect previously failed terminally"))
	}
}
