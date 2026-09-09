package effect_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestDispatcherNeverReplaysAmbiguousDurableEffect(t *testing.T) {
	store := &memoryStore{}
	authorizer := &recordingAuthorizer{}
	sender := &recordingEffectSender{err: agent.NewError(agent.ErrorTimeout, "send fake", context.DeadlineExceeded)}
	dispatcher, err := effect.NewDispatcher(store, authorizer, sender, agent.SystemClock{})
	if err != nil {
		t.Fatalf("create dispatcher: %v", err)
	}
	request := durablePlan(t)
	if _, err := dispatcher.Plan(context.Background(), request); err != nil {
		t.Fatalf("plan durable effect: %v", err)
	}
	if err := dispatcher.Dispatch(context.Background(), request.Ref); !agent.IsCode(err, agent.ErrorUnknownOutcome) {
		t.Fatalf("dispatch result = %v, want unknown outcome", err)
	}
	if err := dispatcher.Dispatch(context.Background(), request.Ref); !agent.IsCode(err, agent.ErrorUnknownOutcome) {
		t.Fatalf("replay result = %v, want unknown outcome", err)
	}
	if sender.calls != 1 || store.stored.State != effect.StateUnknownOutcome || authorizer.calls != 1 {
		t.Fatalf("durable effect state/calls = %#v/%d/%d", store.stored, sender.calls, authorizer.calls)
	}
}

func TestDispatcherSkipsFailedEphemeralEffect(t *testing.T) {
	store := &memoryStore{}
	sender := &recordingEffectSender{err: agent.NewError(agent.ErrorUnavailable, "send fake", fmt.Errorf("offline"))}
	dispatcher, err := effect.NewDispatcher(store, &recordingAuthorizer{}, sender, agent.SystemClock{})
	if err != nil {
		t.Fatalf("create dispatcher: %v", err)
	}
	request := presencePlan(t)
	if _, err := dispatcher.Plan(context.Background(), request); err != nil {
		t.Fatalf("plan ephemeral effect: %v", err)
	}
	if err := dispatcher.Dispatch(context.Background(), request.Ref); err == nil {
		t.Fatal("failed ephemeral effect was reported as success")
	}
	if store.stored.State != effect.StateSkipped || sender.calls != 1 {
		t.Fatalf("ephemeral effect state/calls = %#v/%d", store.stored, sender.calls)
	}
	if err := dispatcher.Dispatch(context.Background(), request.Ref); !agent.IsCode(err, agent.ErrorUnsupported) {
		t.Fatalf("skipped effect replay = %v, want unsupported", err)
	}
}

type memoryStore struct{ stored effect.Stored }

func (store *memoryStore) Plan(_ context.Context, request effect.PlanRequest, _ time.Time) (effect.Stored, error) {
	if !store.stored.Request.Ref.EffectID.IsZero() {
		if store.stored.Request.Ref != request.Ref {
			return effect.Stored{}, fmt.Errorf("unexpected effect reference")
		}
		return store.stored, nil
	}
	store.stored = effect.Stored{Request: request, State: effect.StatePending}
	return store.stored, nil
}

func (store *memoryStore) Claim(_ context.Context, ref effect.Ref, _ time.Time) (effect.Stored, error) {
	if store.stored.Request.Ref != ref {
		return effect.Stored{}, fmt.Errorf("effect not found")
	}
	if store.stored.State == effect.StatePending {
		store.stored.State = effect.StateClaimed
		store.stored.Lease = "lease"
	}
	return store.stored, nil
}

func (store *memoryStore) MarkExecuting(_ context.Context, ref effect.Ref, lease effect.Lease, _ time.Time) error {
	if store.stored.Request.Ref != ref || store.stored.State != effect.StateClaimed || store.stored.Lease != lease {
		return fmt.Errorf("invalid execution claim")
	}
	store.stored.State = effect.StateExecuting
	return nil
}

func (store *memoryStore) Complete(_ context.Context, ref effect.Ref, lease effect.Lease, _ string, _ time.Time) error {
	return store.finish(ref, lease, effect.StateSucceeded)
}

func (store *memoryStore) FailTerminal(_ context.Context, ref effect.Ref, lease effect.Lease, _ agent.ErrorCode, _ time.Time) error {
	return store.finish(ref, lease, effect.StateFailedTerminal)
}

func (store *memoryStore) MarkUnknown(_ context.Context, ref effect.Ref, lease effect.Lease, _ agent.ErrorCode, _ time.Time) error {
	return store.finish(ref, lease, effect.StateUnknownOutcome)
}

func (store *memoryStore) Skip(_ context.Context, ref effect.Ref, lease effect.Lease, _ agent.ErrorCode, _ time.Time) error {
	return store.finish(ref, lease, effect.StateSkipped)
}

func (store *memoryStore) finish(ref effect.Ref, lease effect.Lease, state effect.State) error {
	if store.stored.Request.Ref != ref || store.stored.State != effect.StateExecuting || store.stored.Lease != lease {
		return fmt.Errorf("invalid completion claim")
	}
	store.stored.State = state
	store.stored.Lease = ""
	return nil
}

type recordingAuthorizer struct{ calls int }

func (authorizer *recordingAuthorizer) AuthorizeEffect(_ context.Context, _ effect.Stored) error {
	authorizer.calls++
	return nil
}

type recordingEffectSender struct {
	calls int
	err   error
}

func (*recordingEffectSender) Ready() bool { return true }
func (sender *recordingEffectSender) ExecuteEffect(_ context.Context, _ effect.Stored) (string, error) {
	sender.calls++
	return "provider-receipt", sender.err
}

func durablePlan(t *testing.T) effect.PlanRequest {
	t.Helper()
	key := testKey(t)
	principal, _ := policy.SystemPrincipal(key)
	effectID, _ := identity.NewEffectID()
	invocationID, _ := identity.NewInvocationID()
	messageID, _ := identity.NewMessageID()
	return effect.PlanRequest{
		Ref: effect.Ref{Key: key, EffectID: effectID}, InvocationID: invocationID, Principal: principal,
		Effect: effect.React{TargetMessageID: messageID, Emoji: "✅"},
	}
}

func presencePlan(t *testing.T) effect.PlanRequest {
	t.Helper()
	key := testKey(t)
	principal, _ := policy.SystemPrincipal(key)
	effectID, _ := identity.NewEffectID()
	invocationID, _ := identity.NewInvocationID()
	return effect.PlanRequest{
		Ref: effect.Ref{Key: key, EffectID: effectID}, InvocationID: invocationID, Principal: principal,
		Effect: effect.SetChatPresence{State: effect.PresenceComposing},
	}
}

func testKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}
