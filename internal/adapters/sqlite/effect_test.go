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
	claimed, err := store.Effects().Claim(context.Background(), request.Ref, time.Now().UTC())
	if err != nil || claimed.State != effect.StateClaimed || claimed.Lease == "" {
		t.Fatalf("claim delete = %#v, %v", claimed, err)
	}
	if err := store.Effects().MarkExecuting(context.Background(), request.Ref, claimed.Lease, time.Now().UTC()); err != nil {
		t.Fatalf("start delete: %v", err)
	}
	if err := store.Effects().MarkUnknown(context.Background(), request.Ref, claimed.Lease, agent.ErrorTimeout, time.Now().UTC()); err != nil {
		t.Fatalf("mark delete unknown: %v", err)
	}
	observed, err := store.Effects().Claim(context.Background(), request.Ref, time.Now().UTC())
	if err != nil || observed.State != effect.StateUnknownOutcome {
		t.Fatalf("unknown delete was replayable: %#v, %v", observed, err)
	}
}

func TestTypedEffectRecoveryListsOnlyPendingOrExpiredLeases(t *testing.T) {
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
		t.Fatalf("plan recoverable effect: %v", err)
	}
	refs, err := store.Effects().ListRecoverableEffects(context.Background(), key.TenantID, now, 10)
	if err != nil || len(refs) != 1 || refs[0] != request.Ref {
		t.Fatalf("pending recoverable refs = %#v, %v", refs, err)
	}
	claimed, err := store.Effects().Claim(context.Background(), request.Ref, now)
	if err != nil || claimed.State != effect.StateClaimed {
		t.Fatalf("claim effect: %#v, %v", claimed, err)
	}
	refs, err = store.Effects().ListRecoverableEffects(context.Background(), key.TenantID, now, 10)
	if err != nil || len(refs) != 0 {
		t.Fatalf("active claim was recoverable: %#v, %v", refs, err)
	}
	refs, err = store.Effects().ListRecoverableEffects(context.Background(), key.TenantID, now.Add(time.Hour), 10)
	if err != nil || len(refs) != 1 || refs[0] != request.Ref {
		t.Fatalf("expired claim recoverable refs = %#v, %v", refs, err)
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
