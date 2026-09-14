package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type Dependencies struct {
	Defaults      ConfigValues
	ConfigStore   ConfigStore
	HistoryStore  HistoryStore
	Turns         TurnStore
	Context       ContextBuilder
	HistoryWindow uint32
	Model         ModelInvoker
	Responses     ResponseDispatcher
	Effects       EffectDispatcher
	Events        ConfigEventSink
	Clock         Clock
}

type Agent struct {
	key           Key
	config        *Config
	history       *History
	turns         TurnStore
	model         ModelInvoker
	context       ContextBuilder
	historyWindow uint32
	responses     ResponseDispatcher
	effects       EffectDispatcher
	clock         Clock
	gate          *operationGate
}

func New(ctx context.Context, key Key, dependencies Dependencies) (*Agent, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if dependencies.ConfigStore == nil || dependencies.HistoryStore == nil || dependencies.Turns == nil ||
		dependencies.Context == nil || dependencies.Model == nil ||
		dependencies.Responses == nil || dependencies.Events == nil || dependencies.Clock == nil {
		return nil, NewError(ErrorInvalidArgument, "create agent", fmt.Errorf("all Part 2 dependencies are required"))
	}
	if dependencies.HistoryWindow == 0 || dependencies.HistoryWindow > MaxHistoryPageSize {
		return nil, NewError(ErrorInvalidArgument, "create agent", fmt.Errorf("history window must be between 1 and %d", MaxHistoryPageSize))
	}
	if dependencies.Effects == nil {
		dependencies.Effects = rejectedEffectDispatcher{}
	}
	config, err := newConfig(
		ctx,
		key,
		dependencies.Defaults,
		dependencies.ConfigStore,
		dependencies.Events,
		dependencies.Clock,
	)
	if err != nil {
		return nil, err
	}
	gate := newOperationGate()
	history, err := newHistory(key, dependencies.HistoryStore, gate, dependencies.Clock)
	if err != nil {
		return nil, err
	}
	agent := &Agent{
		key:           key,
		config:        config,
		history:       history,
		turns:         dependencies.Turns,
		model:         dependencies.Model,
		context:       dependencies.Context,
		historyWindow: dependencies.HistoryWindow,
		responses:     dependencies.Responses,
		effects:       dependencies.Effects,
		clock:         dependencies.Clock,
		gate:          gate,
	}
	return agent, nil
}

func (agent *Agent) Key() Key          { return agent.key }
func (agent *Agent) Config() *Config   { return agent.config }
func (agent *Agent) History() *History { return agent.history }

// BuildInput returns the same bounded message list produced by the Agent's
// context builder, without invoking the model. Authorization for exposing this
// potentially sensitive data remains outside Agent.
func (agent *Agent) BuildInput(ctx context.Context, version ConfigVersion, currentInvocationID identity.InvocationID) ([]ModelMessage, error) {
	if version == 0 || currentInvocationID.IsZero() {
		return nil, NewError(ErrorInvalidArgument, "build agent input", fmt.Errorf("config version and current invocation are required"))
	}
	if err := agent.gate.acquire(ctx); err != nil {
		return nil, err
	}
	defer agent.gate.release()
	snapshot, err := agent.config.Refresh(ctx)
	if err != nil {
		return nil, NewError(ErrorIntegrityFailure, "build agent input", err)
	}
	if snapshot.Version != version {
		return nil, NewError(ErrorConflict, "build agent input", fmt.Errorf("authorized config version changed"))
	}
	page, err := agent.history.List(ctx, version, HistoryQuery{
		Limit: agent.historyWindow, ThroughInvocationID: currentInvocationID,
	})
	if err != nil {
		return nil, NewError(ErrorIntegrityFailure, "build agent input", err)
	}
	messages, err := agent.context.Build(ContextBuildRequest{
		Config: snapshot, History: page.Entries, CurrentInvocationID: currentInvocationID,
	})
	if err != nil {
		return nil, NewError(ErrorIntegrityFailure, "build agent input", err)
	}
	return messages, nil
}

func (agent *Agent) Invoke(ctx context.Context, invocation Invocation) (InvokeResult, error) {
	if err := agent.gate.acquire(ctx); err != nil {
		return InvokeResult{}, err
	}
	defer agent.gate.release()

	digest, err := DigestInvocation(agent.key, invocation)
	if err != nil {
		return InvokeResult{}, NewError(ErrorIntegrityFailure, "invoke agent", err)
	}

	// Looking up an existing plan before validating the current policy version
	// is intentional: replay keeps the original immutable plan. Dispatch still
	// performs a fresh external policy check immediately before the side effect.
	record, loadErr := agent.turns.Load(ctx, agent.key, invocation.ID)
	switch {
	case loadErr == nil:
		if record.Digest != digest {
			return InvokeResult{}, NewError(ErrorConflict, "invoke agent", fmt.Errorf("invocation ID is bound to different input"))
		}
		if record.Plan != nil {
			if err := agent.ensureInvocationHistory(ctx, record.MessageID, invocation, record.Plan, record.Delivery); err != nil {
				return InvokeResult{}, err
			}
			return agent.dispatchPlan(ctx, *record.Plan)
		}
		if record.State == TurnFailedTerminal {
			return InvokeResult{}, NewError(ErrorProviderFailure, "invoke agent", fmt.Errorf("generation previously failed terminally"))
		}
	case IsCode(loadErr, ErrorNotFound):
		// Continue with a new durable claim.
	default:
		return InvokeResult{}, loadErr
	}

	snapshot, err := agent.config.Refresh(ctx)
	if err != nil {
		return InvokeResult{}, NewError(ErrorIntegrityFailure, "invoke agent", err)
	}
	if snapshot.Version != invocation.PolicyVersion {
		return InvokeResult{}, NewError(ErrorConflict, "invoke agent", fmt.Errorf("policy decision used a stale config version"))
	}

	claim, err := agent.turns.Claim(ctx, ClaimTurnRequest{
		Key:        agent.key,
		Invocation: cloneInvocation(invocation),
		Digest:     digest,
		Now:        agent.clock.Now(),
	})
	if err != nil {
		return InvokeResult{}, NewError(ErrorStorageFailure, "invoke agent", err)
	}
	if claim.Plan != nil {
		if err := agent.ensureInvocationHistory(ctx, claim.MessageID, invocation, claim.Plan, deliveryForTurnState(claim.State)); err != nil {
			return InvokeResult{}, err
		}
		return agent.dispatchPlan(ctx, *claim.Plan)
	}
	if claim.State != TurnGenerating || claim.Lease == "" {
		return InvokeResult{}, NewError(ErrorIntegrityFailure, "invoke agent", fmt.Errorf("turn store returned invalid generation claim"))
	}
	if claim.MessageID.IsZero() {
		return InvokeResult{}, NewError(ErrorIntegrityFailure, "invoke agent", fmt.Errorf("turn store returned an empty message ID"))
	}
	if err := agent.history.appendWithinGate(ctx, userHistoryEntry(claim.MessageID, invocation)); err != nil {
		agent.failGeneration(invocation.ID, claim.Lease, err)
		return InvokeResult{}, err
	}
	// The transcript may continue to receive passive group messages while this
	// turn is waiting for its debounce window. Context is therefore bounded at
	// this invocation rather than at the newest row in the whole chat.
	page, err := agent.history.List(ctx, snapshot.Version, HistoryQuery{
		Limit: agent.historyWindow, ThroughInvocationID: invocation.ID,
	})
	if err != nil {
		wrappedErr := NewError(ErrorIntegrityFailure, "invoke agent", err)
		agent.failGeneration(invocation.ID, claim.Lease, wrappedErr)
		return InvokeResult{}, wrappedErr
	}
	messages, err := agent.context.Build(ContextBuildRequest{
		Config: snapshot, History: page.Entries, CurrentInvocationID: invocation.ID,
	})
	if err != nil {
		wrappedErr := NewError(ErrorIntegrityFailure, "invoke agent", err)
		agent.failGeneration(invocation.ID, claim.Lease, wrappedErr)
		return InvokeResult{}, wrappedErr
	}

	request := ModelRequest{
		Key:              agent.key,
		InvocationID:     invocation.ID,
		CurrentMessageID: claim.MessageID,
		ConfigVersion:    snapshot.Version,
		Model:            snapshot.Model,
		Messages:         messages,
		Capabilities:     CapabilitySet{values: invocation.Capabilities.Values()},
		ContextMessages:  contextMessageMap(page.Entries),
	}
	generated, err := agent.model.Generate(ctx, request)
	if err != nil {
		wrappedErr := NewError(ErrorProviderFailure, "invoke agent", err)
		agent.failGeneration(invocation.ID, claim.Lease, wrappedErr)
		return InvokeResult{}, wrappedErr
	}
	if err := validateModelResult(generated, request.Capabilities, request.ContextMessages); err != nil {
		wrappedErr := NewError(ErrorProviderFailure, "invoke agent", err)
		agent.failGeneration(invocation.ID, claim.Lease, wrappedErr)
		return InvokeResult{}, wrappedErr
	}

	plan, err := agent.turns.CommitPlan(ctx, CommitPlanRequest{
		Key:              agent.key,
		InvocationID:     invocation.ID,
		CurrentMessageID: claim.MessageID,
		Lease:            claim.Lease,
		ConfigVersion:    snapshot.Version,
		ResponseText:     generated.Text,
		Capabilities:     request.Capabilities,
		Effects:          cloneModelEffects(generated.Effects),
	})
	if err != nil {
		return InvokeResult{}, err
	}
	return agent.dispatchPlan(ctx, plan)
}

func contextMessageMap(entries []HistoryEntry) map[string]identity.MessageID {
	result := make(map[string]identity.MessageID, len(entries))
	for _, entry := range entries {
		if entry.Sequence > 0 && !entry.MessageID.IsZero() {
			// Keep lookup IDs byte-for-byte aligned with the compact transcript.
			// Sequence numbers are durable and may cross the six-digit display
			// boundary; the model only sees the modulo representation.
			result[formatCompactContextID(entry.Sequence)] = entry.MessageID
		}
	}
	return result
}

func userHistoryEntry(messageID identity.MessageID, invocation Invocation) HistoryEntry {
	return HistoryEntry{
		MessageID: messageID, InvocationID: invocation.ID, Causation: invocation.Causation,
		Role: HistoryUser, Sender: cloneSender(invocation.Sender), Quote: cloneQuote(invocation.Quote),
		Content: cloneContent(invocation.Input), Delivery: DeliveryNotStarted, CreatedAt: invocation.RequestedAt,
	}
}

func (agent *Agent) ensureInvocationHistory(
	ctx context.Context,
	messageID identity.MessageID,
	invocation Invocation,
	plan *StoredPlan,
	delivery DeliveryStatus,
) error {
	if messageID.IsZero() {
		return Errorf(ErrorIntegrityFailure, "restore invocation history", "turn message ID is missing")
	}
	if err := agent.history.appendWithinGate(ctx, userHistoryEntry(messageID, invocation)); err != nil {
		return NewError(ErrorStorageFailure, "restore invocation history", err)
	}
	if plan == nil {
		return nil
	}
	if strings.TrimSpace(plan.Text) == "" {
		// Terminal plan content may already have been scrubbed while its
		// independently retained history/receipt tombstones remain valid.
		return nil
	}
	err := agent.history.appendWithinGate(ctx, HistoryEntry{
		MessageID: plan.ResponseID, InvocationID: invocation.ID, Causation: invocation.Causation,
		Role: HistoryAssistant, Content: []ContentPart{TextPart{Text: plan.Text}},
		Delivery: delivery, CreatedAt: plan.CreatedAt,
	})
	if err != nil {
		return NewError(ErrorStorageFailure, "restore invocation history", err)
	}
	return nil
}

func deliveryForTurnState(state TurnState) DeliveryStatus {
	switch state {
	case TurnSucceeded:
		return DeliverySucceeded
	case TurnFailedTerminal:
		return DeliveryFailedTerminal
	case TurnUnknownOutcome:
		return DeliveryUnknownOutcome
	default:
		return DeliveryPending
	}
}

func (agent *Agent) dispatchPlan(ctx context.Context, plan StoredPlan) (InvokeResult, error) {
	result := InvokeResult{
		InvocationID:  plan.InvocationID,
		ConfigVersion: plan.ConfigVersion,
		ResponseID:    plan.ResponseID,
		ActionID:      plan.ActionID,
		Text:          plan.Text,
		Delivery:      DeliveryPending,
	}
	delivery, err := agent.responses.Dispatch(ctx, plan.Dispatch)
	if delivery.ActionID.IsZero() && err != nil {
		return result, err
	}
	if delivery.ActionID != plan.ActionID {
		return result, NewError(ErrorIntegrityFailure, "dispatch response", fmt.Errorf("dispatcher returned a different action ID"))
	}
	switch delivery.Status {
	case DeliveryPending, DeliverySucceeded, DeliveryFailedTerminal, DeliveryUnknownOutcome:
		result.Delivery = delivery.Status
	default:
		return result, NewError(ErrorIntegrityFailure, "dispatch response", fmt.Errorf("dispatcher returned an invalid delivery status"))
	}
	if err != nil {
		return result, err
	}
	for _, ref := range plan.Effects {
		if err := agent.effects.DispatchEffect(ctx, ref); err != nil {
			return result, err
		}
	}
	return result, err
}

type rejectedEffectDispatcher struct{}

func (rejectedEffectDispatcher) DispatchEffect(context.Context, EffectDispatchRef) error {
	return NewError(ErrorPermissionDenied, "dispatch effect", fmt.Errorf("no effect executor is configured"))
}

func (agent *Agent) failGeneration(invocationID identity.InvocationID, lease TurnLease, generationErr error) {
	code := CodeOf(generationErr)
	retryable := code == ErrorRateLimited || code == ErrorTimeout || code == ErrorCancelled ||
		code == ErrorUnavailable || code == ErrorProviderFailure || code == ErrorInternal
	request := FailGenerationRequest{
		Key:          agent.key,
		InvocationID: invocationID,
		Lease:        lease,
		Code:         code,
		Retryable:    retryable,
		RetryAfter:   agent.clock.Now().Add(time.Second),
	}
	// The generation error remains the primary result. A failed cleanup leaves
	// the bounded lease to expire, which is safer than hiding the original cause.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = agent.turns.FailGeneration(cleanupCtx, request)
}

func (agent *Agent) isInFlight() bool { return agent.gate.isInFlight() }

func validateModelResult(result ModelResult, capabilities CapabilitySet, contextMessages map[string]identity.MessageID) error {
	if strings.TrimSpace(result.Text) == "" || !utf8.ValidString(result.Text) {
		return Errorf(ErrorProviderFailure, "validate model result", "provider returned empty or invalid UTF-8 text")
	}
	if len(result.Text) > MaxResponseBytes {
		return Errorf(ErrorProviderFailure, "validate model result", "provider response exceeds %d bytes", MaxResponseBytes)
	}
	if len(result.Effects) > MaxModelEffects {
		return Errorf(ErrorProviderFailure, "validate model result", "provider returned too many effects")
	}
	callIDs := make(map[string]struct{}, len(result.Effects))
	allowedTargets := make(map[identity.MessageID]struct{}, len(contextMessages))
	for _, messageID := range contextMessages {
		allowedTargets[messageID] = struct{}{}
	}
	for _, planned := range result.Effects {
		if err := planned.Validate(capabilities); err != nil {
			return NewError(ErrorProviderFailure, "validate model result", err)
		}
		if !planned.Intent.TargetMessageID.IsZero() {
			if _, ok := allowedTargets[planned.Intent.TargetMessageID]; !ok {
				return Errorf(ErrorProviderFailure, "validate model result", "model effect target is outside supplied history")
			}
		}
		if _, exists := callIDs[planned.CallID]; exists {
			return Errorf(ErrorProviderFailure, "validate model result", "provider duplicated a tool call ID")
		}
		callIDs[planned.CallID] = struct{}{}
	}
	return nil
}

func cloneInvocation(invocation Invocation) Invocation {
	invocation.Sender = cloneSender(invocation.Sender)
	invocation.Quote = cloneQuote(invocation.Quote)
	invocation.Input = cloneContent(invocation.Input)
	invocation.Capabilities = CapabilitySet{values: invocation.Capabilities.Values()}
	return invocation
}

func cloneModelEffects(effects []ModelEffect) []ModelEffect {
	return append([]ModelEffect(nil), effects...)
}

func contextError(operation string, err error) error {
	if err == context.DeadlineExceeded {
		return NewError(ErrorTimeout, operation, err)
	}
	return NewError(ErrorCancelled, operation, err)
}
