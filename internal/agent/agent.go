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
		clock:         dependencies.Clock,
		gate:          gate,
	}
	return agent, nil
}

func (agent *Agent) Key() Key          { return agent.key }
func (agent *Agent) Config() *Config   { return agent.config }
func (agent *Agent) History() *History { return agent.history }

func (agent *Agent) Invoke(ctx context.Context, invocation Invocation) (InvokeResult, error) {
	if err := agent.gate.acquire(ctx); err != nil {
		return InvokeResult{}, err
	}
	defer agent.gate.release()

	digest, err := DigestInvocation(agent.key, invocation)
	if err != nil {
		return InvokeResult{}, err
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
		return InvokeResult{}, err
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
		return InvokeResult{}, err
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
	page, err := agent.history.List(ctx, snapshot.Version, HistoryQuery{Limit: agent.historyWindow})
	if err != nil {
		agent.failGeneration(invocation.ID, claim.Lease, err)
		return InvokeResult{}, err
	}
	messages, err := agent.context.Build(ContextBuildRequest{
		Config: snapshot, History: page.Entries, CurrentInvocationID: invocation.ID,
	})
	if err != nil {
		agent.failGeneration(invocation.ID, claim.Lease, err)
		return InvokeResult{}, err
	}

	request := ModelRequest{
		Key:           agent.key,
		InvocationID:  invocation.ID,
		ConfigVersion: snapshot.Version,
		Model:         snapshot.Model,
		Messages:      messages,
		Capabilities:  CapabilitySet{values: invocation.Capabilities.Values()},
	}
	generated, err := agent.model.Generate(ctx, request)
	if err != nil {
		agent.failGeneration(invocation.ID, claim.Lease, err)
		return InvokeResult{}, err
	}
	if err := validateModelResult(generated); err != nil {
		agent.failGeneration(invocation.ID, claim.Lease, err)
		return InvokeResult{}, err
	}

	plan, err := agent.turns.CommitPlan(ctx, CommitPlanRequest{
		Key:           agent.key,
		InvocationID:  invocation.ID,
		Lease:         claim.Lease,
		ConfigVersion: snapshot.Version,
		ResponseText:  generated.Text,
	})
	if err != nil {
		return InvokeResult{}, err
	}
	return agent.dispatchPlan(ctx, plan)
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
		return NewError(ErrorIntegrityFailure, "restore invocation history", fmt.Errorf("turn message ID is missing"))
	}
	if err := agent.history.appendWithinGate(ctx, userHistoryEntry(messageID, invocation)); err != nil {
		return err
	}
	if plan == nil {
		return nil
	}
	if strings.TrimSpace(plan.Text) == "" {
		// Terminal plan content may already have been scrubbed while its
		// independently retained history/receipt tombstones remain valid.
		return nil
	}
	return agent.history.appendWithinGate(ctx, HistoryEntry{
		MessageID: plan.ResponseID, InvocationID: invocation.ID, Causation: invocation.Causation,
		Role: HistoryAssistant, Content: []ContentPart{TextPart{Text: plan.Text}},
		Delivery: delivery, CreatedAt: plan.CreatedAt,
	})
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
	return result, err
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

func validateModelResult(result ModelResult) error {
	if strings.TrimSpace(result.Text) == "" || !utf8.ValidString(result.Text) {
		return NewError(ErrorProviderFailure, "validate model result", fmt.Errorf("provider returned empty or invalid UTF-8 text"))
	}
	if len(result.Text) > MaxResponseBytes {
		return NewError(ErrorProviderFailure, "validate model result", fmt.Errorf("provider response exceeds %d bytes", MaxResponseBytes))
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

func contextError(operation string, err error) error {
	if err == context.DeadlineExceeded {
		return NewError(ErrorTimeout, operation, err)
	}
	return NewError(ErrorCancelled, operation, err)
}
