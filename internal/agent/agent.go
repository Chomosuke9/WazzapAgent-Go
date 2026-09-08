package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type Dependencies struct {
	Defaults    ConfigValues
	ConfigStore ConfigStore
	Turns       TurnStore
	Model       ModelInvoker
	Responses   ResponseDispatcher
	Events      ConfigEventSink
	Clock       Clock
}

type Agent struct {
	key       Key
	config    *Config
	turns     TurnStore
	model     ModelInvoker
	responses ResponseDispatcher
	clock     Clock
	gate      chan struct{}
	inFlight  atomic.Int64
}

func New(ctx context.Context, key Key, dependencies Dependencies) (*Agent, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	if dependencies.ConfigStore == nil || dependencies.Turns == nil || dependencies.Model == nil ||
		dependencies.Responses == nil || dependencies.Events == nil || dependencies.Clock == nil {
		return nil, NewError(ErrorInvalidArgument, "create agent", fmt.Errorf("all Part 1 dependencies are required"))
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
	agent := &Agent{
		key:       key,
		config:    config,
		turns:     dependencies.Turns,
		model:     dependencies.Model,
		responses: dependencies.Responses,
		clock:     dependencies.Clock,
		gate:      make(chan struct{}, 1),
	}
	agent.gate <- struct{}{}
	return agent, nil
}

func (agent *Agent) Key() Key        { return agent.key }
func (agent *Agent) Config() *Config { return agent.config }

func (agent *Agent) Invoke(ctx context.Context, invocation Invocation) (InvokeResult, error) {
	if err := agent.acquire(ctx); err != nil {
		return InvokeResult{}, err
	}
	defer agent.release()

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
		return agent.dispatchPlan(ctx, *claim.Plan)
	}
	if claim.State != TurnGenerating || claim.Lease == "" {
		return InvokeResult{}, NewError(ErrorIntegrityFailure, "invoke agent", fmt.Errorf("turn store returned invalid generation claim"))
	}

	request := ModelRequest{
		Key:           agent.key,
		InvocationID:  invocation.ID,
		ConfigVersion: snapshot.Version,
		Model:         snapshot.Model,
		Prompt:        snapshot.Prompt,
		Override:      clonePromptOverride(snapshot.PromptOverride),
		Sender:        cloneSender(invocation.Sender),
		Input:         cloneContent(invocation.Input),
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

func (agent *Agent) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return contextError("wait for agent invocation gate", ctx.Err())
	case <-agent.gate:
		agent.inFlight.Add(1)
		return nil
	}
}

func (agent *Agent) release() {
	agent.inFlight.Add(-1)
	agent.gate <- struct{}{}
}

func (agent *Agent) isInFlight() bool { return agent.inFlight.Load() > 0 }

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
