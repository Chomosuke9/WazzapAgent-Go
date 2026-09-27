// Package effect owns typed native WhatsApp effects. It exposes no string
// command, provider address, or provider DTO to the rest of the application.
package effect

import (
	"context"
	"errors"
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
	// Kinds 3 and 4 were mark-read and presence hints that nothing planned.
	KindRunCommand Kind = 5
)

type Effect interface {
	isEffect()
	Validate() error
	Kind() Kind
	Capability() policy.Capability
}

type React struct {
	TargetMessageID identity.MessageID
	Emoji           string
}

func (React) isEffect()                     {}
func (effect React) Kind() Kind             { return KindReact }
func (React) Capability() policy.Capability { return policy.CapabilityMessageReact }
func (effect React) Validate() error {
	if effect.TargetMessageID.IsZero() || strings.TrimSpace(effect.Emoji) == "" || !utf8.ValidString(effect.Emoji) || len(effect.Emoji) > MaxEmojiBytes {
		return agent.NewError(agent.ErrorInvalidArgument, "validate reaction effect", errors.New("target and bounded emoji are required"))
	}
	return nil
}

type DeleteMessage struct{ TargetMessageID identity.MessageID }

func (DeleteMessage) isEffect()                     {}
func (DeleteMessage) Kind() Kind                    { return KindDeleteMessage }
func (DeleteMessage) Capability() policy.Capability { return policy.CapabilityMessageDelete }
func (effect DeleteMessage) Validate() error {
	if effect.TargetMessageID.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "validate delete effect", errors.New("target message is required"))
	}
	return nil
}

type RunCommand struct {
	Command         string
	TargetMessageID identity.MessageID
}

func (RunCommand) isEffect()                     {}
func (RunCommand) Kind() Kind                    { return KindRunCommand }
func (RunCommand) Capability() policy.Capability { return policy.CapabilityCommandExecute }
func (command RunCommand) Validate() error {
	if strings.TrimSpace(command.Command) != command.Command || !strings.HasPrefix(command.Command, "/") ||
		len(command.Command) == 0 || len(command.Command) > agent.MaxInputBytes || !utf8.ValidString(command.Command) {
		return agent.NewError(agent.ErrorInvalidArgument, "validate command", errors.New("registered command is malformed"))
	}
	return nil
}

type Ref struct {
	Key      agent.Key
	EffectID identity.EffectID
}

func (ref Ref) Validate() error {
	if err := ref.Key.Validate(); err != nil || ref.EffectID.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "validate effect reference", errors.New("agent key and effect ID are required"))
	}
	return nil
}

type PlanRequest struct {
	Ref          Ref
	InvocationID identity.InvocationID
	Principal    policy.Principal
	Effect       Effect
}

func (request PlanRequest) Validate() error {
	if err := request.Ref.Validate(); err != nil || request.InvocationID.IsZero() || request.Effect == nil {
		return agent.NewError(agent.ErrorInvalidArgument, "validate effect plan", errors.New("reference, invocation, and effect are required"))
	}
	if err := request.Principal.Validate(); err != nil {
		return err
	}
	if request.Ref.Key != request.Principal.Key() {
		return agent.NewError(agent.ErrorInvalidArgument, "validate effect plan", errors.New("principal scope does not match effect scope"))
	}
	if (request.Principal.Kind == policy.PrincipalModel || request.Principal.Kind == policy.PrincipalRecovery) && request.Principal.InvocationID != request.InvocationID {
		return agent.NewError(agent.ErrorInvalidArgument, "validate effect plan", errors.New("principal invocation does not match effect invocation"))
	}
	return request.Effect.Validate()
}

type State uint8

// State values are stored in typed_effects.state. 2 was the claimed lease
// state; migration 021 turned those rows back into pending.
const (
	StatePending        State = 1
	StateExecuting      State = 3
	StateSucceeded      State = 4
	StateFailedTerminal State = 5
	StateUnknownOutcome State = 6
)

type Stored struct {
	Request         PlanRequest
	State           State
	ProviderReceipt string
	CompletedAt     *time.Time
}

// Store is the effect outbox. Load reports a model effect as pending until
// the reply it follows has been sent. Start is the only claim: a conditional
// update from pending to executing.
type Store interface {
	Plan(context.Context, PlanRequest, time.Time) (Stored, error)
	Load(context.Context, Ref) (Stored, error)
	Start(context.Context, Ref, time.Time) error
	Complete(context.Context, Ref, string, time.Time) error
	FailTerminal(context.Context, Ref, agent.ErrorCode, time.Time) error
	MarkUnknown(context.Context, Ref, agent.ErrorCode, time.Time) error
}

type Authorizer interface {
	AuthorizeEffect(context.Context, policy.EffectAuthorization) error
}

type Sender interface {
	Ready() bool
	ExecuteEffect(context.Context, Stored) (providerReceipt string, err error)
}

// CommandExecutor runs a model-requested command. It authorizes nothing
// itself beyond what the command registry's permission expression decides.
type CommandExecutor interface {
	ExecuteCommandEffect(context.Context, Stored, RunCommand) (string, error)
}

// Dispatcher runs effects from the outbox.
type Dispatcher struct {
	store      Store
	authorizer Authorizer
	sender     Sender
	clock      agent.Clock
	commands   CommandExecutor
}

func (dispatcher *Dispatcher) BindCommandExecutor(executor CommandExecutor) error {
	if executor == nil || dispatcher.commands != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "bind command executor", errors.New("one command executor is required"))
	}
	dispatcher.commands = executor
	return nil
}

func NewDispatcher(store Store, authorizer Authorizer, sender Sender, clock agent.Clock) (*Dispatcher, error) {
	if store == nil || authorizer == nil || sender == nil || clock == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create effect dispatcher", errors.New("store, authorizer, sender, and clock are required"))
	}
	return &Dispatcher{store: store, authorizer: authorizer, sender: sender, clock: clock}, nil
}

func (dispatcher *Dispatcher) Plan(ctx context.Context, request PlanRequest) (Stored, error) {
	if err := request.Validate(); err != nil {
		return Stored{}, err
	}
	return dispatcher.store.Plan(ctx, request, dispatcher.clock.Now())
}

// Dispatch runs a planned effect at most once. An effect that cannot run yet
// (the account is offline, or its reply is unsent) stays pending; the
// runtime dispatches pending effects again whenever the account reconnects.
func (dispatcher *Dispatcher) Dispatch(ctx context.Context, ref Ref) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	stored, err := dispatcher.store.Load(ctx, ref)
	if err != nil {
		return err
	}
	switch stored.State {
	case StateSucceeded, StateFailedTerminal, StateUnknownOutcome:
		return terminalStateError(stored.State)
	case StateExecuting:
		return agent.NewError(agent.ErrorDeliveryPending, "dispatch effect", errors.New("effect is already running"))
	case StatePending:
		// Continue below.
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "dispatch effect", errors.New("effect state is invalid"))
	}
	if !dispatcher.sender.Ready() {
		return agent.NewError(agent.ErrorNotReady, "dispatch effect", errors.New("effect sender is not ready"))
	}
	// A command is checked only by its registry permission expression when it
	// runs; every other effect is checked against the model's capabilities.
	_, isCommand := stored.Request.Effect.(RunCommand)
	var authorizeErr error
	if isCommand {
		if dispatcher.commands == nil {
			authorizeErr = agent.NewError(agent.ErrorNotReady, "dispatch command effect", errors.New("command executor is not bound"))
		}
	} else {
		authorizeErr = dispatcher.authorizer.AuthorizeEffect(ctx, policy.EffectAuthorization{
			Key: stored.Request.Ref.Key, Principal: stored.Request.Principal, Capability: stored.Request.Effect.Capability(),
		})
	}
	if authorizeErr != nil {
		if retryablePreExecutionError(authorizeErr) {
			// Nothing ran; the row stays pending for the next reconnect.
			return authorizeErr
		}
		record(func(ctx context.Context) error {
			return dispatcher.store.FailTerminal(ctx, ref, agent.CodeOf(authorizeErr), dispatcher.clock.Now())
		})
		return authorizeErr
	}
	if err := dispatcher.store.Start(ctx, ref, dispatcher.clock.Now()); err != nil {
		if agent.IsCode(err, agent.ErrorConflict) {
			return agent.NewError(agent.ErrorDeliveryPending, "dispatch effect", errors.New("effect is already running"))
		}
		return err
	}
	var receipt string
	var executeErr error
	if isCommand {
		receipt, executeErr = dispatcher.commands.ExecuteCommandEffect(ctx, stored, stored.Request.Effect.(RunCommand))
	} else {
		receipt, executeErr = dispatcher.sender.ExecuteEffect(ctx, stored)
	}
	if executeErr != nil {
		state := StateUnknownOutcome
		if isCommand && agent.CodeOf(executeErr) == agent.ErrorPermissionDenied {
			// The registry refused the command before it ran, so nothing happened.
			state = StateFailedTerminal
		}
		record(func(ctx context.Context) error {
			if state == StateFailedTerminal {
				return dispatcher.store.FailTerminal(ctx, ref, agent.ErrorPermissionDenied, dispatcher.clock.Now())
			}
			return dispatcher.store.MarkUnknown(ctx, ref, agent.CodeOf(executeErr), dispatcher.clock.Now())
		})
		if state == StateFailedTerminal {
			return executeErr
		}
		return agent.NewError(agent.ErrorUnknownOutcome, "dispatch effect", executeErr)
	}
	if err := dispatcher.store.Complete(ctx, ref, receipt, dispatcher.clock.Now()); err != nil {
		record(func(ctx context.Context) error {
			return dispatcher.store.MarkUnknown(ctx, ref, agent.ErrorStorageFailure, dispatcher.clock.Now())
		})
		return agent.NewError(agent.ErrorUnknownOutcome, "record effect receipt", err)
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

// record saves an outcome even when the caller's context is already done.
func record(write func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = write(ctx)
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
		return agent.NewError(agent.ErrorUnknownOutcome, "dispatch effect", errors.New("effect outcome is unknown"))
	default:
		return agent.NewError(agent.ErrorProviderFailure, "dispatch effect", errors.New("effect previously failed terminally"))
	}
}
