package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestTypedEffectPlanBindsTargetAndIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	key := testKey(t)
	target := seedEffectTarget(t, store, key)
	invocationID, _ := identity.NewInvocationID()
	effectID, _ := identity.NewEffectID()
	principal, err := policy.SystemPrincipal(key)
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	request := effect.PlanRequest{
		Ref: effect.Ref{Key: key, EffectID: effectID}, InvocationID: invocationID, Principal: principal,
		Effect: effect.React{TargetMessageID: target, Emoji: "✅"},
	}
	stored, err := store.Effects().Plan(context.Background(), request, time.Now().UTC())
	if err != nil || stored.State != effect.StatePending {
		t.Fatalf("plan typed effect = %#v, %v", stored, err)
	}
	if _, err := store.Effects().Plan(context.Background(), request, time.Now().UTC()); err != nil {
		t.Fatalf("idempotent typed effect plan: %v", err)
	}
	request.Effect = effect.React{TargetMessageID: target, Emoji: "❌"}
	if _, err := store.Effects().Plan(context.Background(), request, time.Now().UTC()); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("effect ID payload collision = %v, want conflict", err)
	}
}

func TestTypedEffectRejectsTargetFromAnotherChatAndDoesNotReplayUnknown(t *testing.T) {
	store := openTestStore(t)
	keyA := testKey(t)
	keyB := testKey(t)
	targetA := seedEffectTarget(t, store, keyA)
	_ = seedEffectTarget(t, store, keyB)
	principal, _ := policy.SystemPrincipal(keyB)
	invocationID, _ := identity.NewInvocationID()
	effectID, _ := identity.NewEffectID()
	request := effect.PlanRequest{
		Ref: effect.Ref{Key: keyB, EffectID: effectID}, InvocationID: invocationID, Principal: principal,
		Effect: effect.DeleteMessage{TargetMessageID: targetA},
	}
	if _, err := store.Effects().Plan(context.Background(), request, time.Now().UTC()); !agent.IsCode(err, agent.ErrorNotFound) {
		t.Fatalf("cross-chat target plan = %v, want not found", err)
	}

	principalA, _ := policy.SystemPrincipal(keyA)
	request.Ref.Key = keyA
	request.Ref.EffectID, _ = identity.NewEffectID()
	request.Principal = principalA
	request.Effect = effect.DeleteMessage{TargetMessageID: targetA}
	if _, err := store.Effects().Plan(context.Background(), request, time.Now().UTC()); err != nil {
		t.Fatalf("plan delete: %v", err)
	}
	if err := store.Effects().Start(context.Background(), request.Ref, time.Now().UTC()); err != nil {
		t.Fatalf("start delete: %v", err)
	}
	if err := store.Effects().Start(context.Background(), request.Ref, time.Now().UTC()); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("second start = %v, want conflict", err)
	}
	if err := store.Effects().MarkUnknown(context.Background(), request.Ref, agent.ErrorTimeout, time.Now().UTC()); err != nil {
		t.Fatalf("mark delete unknown: %v", err)
	}
	observed, err := store.Effects().Load(context.Background(), request.Ref)
	if err != nil || observed.State != effect.StateUnknownOutcome {
		t.Fatalf("unknown delete was replayable: %#v, %v", observed, err)
	}
}

func TestTypedEffectOutboxListsPendingAndResolvesInterrupted(t *testing.T) {
	store := openTestStore(t)
	key := testKey(t)
	target := seedEffectTarget(t, store, key)
	principal, _ := policy.SystemPrincipal(key)
	invocationID, _ := identity.NewInvocationID()
	effectID, _ := identity.NewEffectID()
	now := time.Now().UTC()
	request := effect.PlanRequest{
		Ref: effect.Ref{Key: key, EffectID: effectID}, InvocationID: invocationID, Principal: principal,
		Effect: effect.React{TargetMessageID: target, Emoji: "✅"},
	}
	if _, err := store.Effects().Plan(context.Background(), request, now); err != nil {
		t.Fatalf("plan effect: %v", err)
	}
	refs, err := store.Effects().ListPending(context.Background(), key.TenantID)
	if err != nil || len(refs) != 1 || refs[0] != request.Ref {
		t.Fatalf("pending refs = %#v, %v", refs, err)
	}
	if err := store.Effects().Start(context.Background(), request.Ref, now); err != nil {
		t.Fatalf("start effect: %v", err)
	}
	refs, err = store.Effects().ListPending(context.Background(), key.TenantID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("executing effect was listed as pending: %#v, %v", refs, err)
	}
	// A restart finds the effect still executing: it may have run, so it is
	// never sent again.
	if err := store.ResolveInterrupted(context.Background(), key.TenantID); err != nil {
		t.Fatalf("resolve interrupted: %v", err)
	}
	observed, err := store.Effects().Load(context.Background(), request.Ref)
	if err != nil || observed.State != effect.StateUnknownOutcome {
		t.Fatalf("interrupted effect = %#v, %v", observed, err)
	}
}

func seedEffectTarget(t *testing.T, store *Store, key agent.Key) identity.MessageID {
	t.Helper()
	messageID, _ := identity.NewMessageID()
	invocationID, _ := identity.NewInvocationID()
	causationID, _ := identity.NewCausationID()
	entry := agent.HistoryEntry{
		MessageID: messageID, InvocationID: invocationID,
		Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: causationID},
		Role:      agent.HistoryAssistant, Content: []agent.ContentPart{agent.TextPart{Text: "target"}},
		Delivery: agent.DeliverySucceeded, CreatedAt: time.Now().UTC(),
	}
	if err := store.History().Append(context.Background(), key, entry); err != nil {
		t.Fatalf("seed effect target: %v", err)
	}
	return messageID
}
