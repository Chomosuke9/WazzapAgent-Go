package effect_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/effect"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/policy"
)

func TestDispatcherNeverReplaysAmbiguousDurableEffect(t *testing.T) {
	store := &memoryStore{}
	authorizer := &recordingAuthorizer{}
	sender := &recordingEffectSender{ready: true, err: agent.NewError(agent.ErrorTimeout, "send fake", context.DeadlineExceeded)}
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

func TestNotReadyEffectReturnsToPendingBeforeNativeExecution(t *testing.T) {
	store := &memoryStore{}
	sender := &recordingEffectSender{ready: false}
	dispatcher, err := effect.NewDispatcher(store, &recordingAuthorizer{}, sender, agent.SystemClock{})
	if err != nil {
		t.Fatalf("create dispatcher: %v", err)
	}
	request := durablePlan(t)
	if _, err := dispatcher.Plan(context.Background(), request); err != nil {
		t.Fatalf("plan effect: %v", err)
	}
	if err := dispatcher.Dispatch(context.Background(), request.Ref); !agent.IsCode(err, agent.ErrorNotReady) {
		t.Fatalf("not-ready dispatch = %v, want not_ready", err)
	}
	if store.stored.State != effect.StatePending || sender.calls != 0 {
		t.Fatalf("not-ready state/native calls = %#v/%d", store.stored.State, sender.calls)
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

func (store *memoryStore) Load(_ context.Context, ref effect.Ref) (effect.Stored, error) {
	if store.stored.Request.Ref != ref {
		return effect.Stored{}, fmt.Errorf("effect not found")
	}
	return store.stored, nil
}

func (store *memoryStore) Start(_ context.Context, ref effect.Ref, _ time.Time) error {
	if store.stored.Request.Ref != ref || store.stored.State != effect.StatePending {
		return agent.NewError(agent.ErrorConflict, "start fake effect", fmt.Errorf("effect is not pending"))
	}
	store.stored.State = effect.StateExecuting
	return nil
}

func (store *memoryStore) Complete(_ context.Context, ref effect.Ref, _ string, _ time.Time) error {
	return store.finish(ref, effect.StateSucceeded)
}

func (store *memoryStore) FailTerminal(_ context.Context, ref effect.Ref, _ agent.ErrorCode, _ time.Time) error {
	return store.finish(ref, effect.StateFailedTerminal)
}

func (store *memoryStore) MarkUnknown(_ context.Context, ref effect.Ref, _ agent.ErrorCode, _ time.Time) error {
	return store.finish(ref, effect.StateUnknownOutcome)
}

func (store *memoryStore) finish(ref effect.Ref, state effect.State) error {
	if store.stored.Request.Ref != ref {
		return fmt.Errorf("effect not found")
	}
	store.stored.State = state
	return nil
}

type recordingAuthorizer struct{ calls int }

func (authorizer *recordingAuthorizer) AuthorizeEffect(_ context.Context, _ policy.EffectAuthorization) error {
	authorizer.calls++
	return nil
}

type recordingEffectSender struct {
	calls int
	err   error
	ready bool
}

func (sender *recordingEffectSender) Ready() bool { return sender.ready }
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

func testKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}
