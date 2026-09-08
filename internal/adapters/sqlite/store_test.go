package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/maintenance"
)

func TestOpenAppliesAndVerifiesEmbeddedMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	assertSQLitePragma(t, store.db, "foreign_keys", "1")
	assertSQLitePragma(t, store.db, "journal_mode", "wal")
	assertSQLitePragma(t, store.db, "integrity_check", "ok")
	var migrations int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrations != 1 {
		t.Fatalf("migration count = %d, want 1", migrations)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
}

func TestConfigCASAndTenantChatIsolation(t *testing.T) {
	store := openTestStore(t)
	defaults := testDefaults(t)
	keyA := testKey(t)
	keyB := testKey(t)
	first, err := store.Configs().LoadOrCreate(context.Background(), keyA, defaults)
	if err != nil {
		t.Fatalf("create config A: %v", err)
	}
	if first.Version != agent.InitialConfigVersion {
		t.Fatalf("initial version = %d, want 1", first.Version)
	}
	second, err := store.Configs().LoadOrCreate(context.Background(), keyA, changedDefaults(defaults))
	if err != nil {
		t.Fatalf("reload config A: %v", err)
	}
	if second.Prompt != defaults.Prompt {
		t.Fatal("LoadOrCreate overwrote existing defaults")
	}
	if _, err := store.Configs().LoadOrCreate(context.Background(), keyB, defaults); err != nil {
		t.Fatalf("create config B: %v", err)
	}
	values := first.Values()
	values.PromptOverride = &agent.PromptOverride{Mode: agent.PromptAppend, Text: "chat A only"}
	updated, err := store.Configs().CompareAndSwap(context.Background(), keyA, first.Version, values)
	if err != nil {
		t.Fatalf("update config A: %v", err)
	}
	if updated.Version != first.Version+1 || updated.PromptOverride == nil {
		t.Fatalf("updated snapshot = %#v", updated)
	}
	if _, err := store.Configs().CompareAndSwap(context.Background(), keyA, first.Version, values); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("stale update error = %v, want conflict", err)
	}
	isolated, err := store.Configs().Load(context.Background(), keyB)
	if err != nil {
		t.Fatalf("load config B: %v", err)
	}
	if isolated.PromptOverride != nil || isolated.Version != agent.InitialConfigVersion {
		t.Fatalf("config B was changed through config A: %#v", isolated)
	}
}

func TestInboundClaimSenderRefAndDuplicateAreDurable(t *testing.T) {
	store := openTestStore(t)
	candidate := testCandidate(t, "provider-message-1", "15550000001@s.whatsapp.net")
	claimed, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim inbound: %v", err)
	}
	if claimed.Duplicate || claimed.Message.SenderRef.IsZero() || claimed.Message.ChatID.IsZero() {
		t.Fatalf("first claim = %#v", claimed)
	}
	duplicate, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim duplicate: %v", err)
	}
	if !duplicate.Duplicate || duplicate.Message.ID != claimed.Message.ID || duplicate.Message.SenderRef != claimed.Message.SenderRef {
		t.Fatalf("duplicate did not preserve identity: first=%#v duplicate=%#v", claimed, duplicate)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	allowed, err := store.Inbound().IsChatAllowlisted(context.Background(), key)
	if err != nil || !allowed {
		t.Fatalf("chat allowlist = %v, err=%v", allowed, err)
	}
	address, err := store.Inbound().ResolveChatAddress(context.Background(), key)
	if err != nil || address != candidate.ProviderChatAddress {
		t.Fatalf("resolved address = %q, err=%v", address, err)
	}
	if err := store.Inbound().MarkIgnored(context.Background(), claimed.Message, inbound.IgnorePolicyDenied); err != nil {
		t.Fatalf("mark ignored: %v", err)
	}
	var ignoredReason string
	if err := store.db.QueryRowContext(context.Background(), `SELECT ignored_reason FROM inbound_events
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), claimed.Message.InvocationID.String(),
	).Scan(&ignoredReason); err != nil || ignoredReason != string(inbound.IgnorePolicyDenied) {
		t.Fatalf("ignored reason = %q, err=%v", ignoredReason, err)
	}
	handled, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil || !handled.Handled {
		t.Fatalf("ignored duplicate = %#v, err=%v", handled, err)
	}
}

func TestSenderRefAndAgentConfigSurviveStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	candidate := testCandidate(t, "provider-reopen-1", "15550000011@s.whatsapp.net")
	claimed, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim inbound: %v", err)
	}
	key := agent.Key{TenantID: candidate.TenantID, AccountID: candidate.AccountID, ChatID: claimed.Message.ChatID}
	snapshot, err := store.Configs().LoadOrCreate(context.Background(), key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	values := snapshot.Values()
	values.PromptOverride = &agent.PromptOverride{Mode: agent.PromptAppend, Text: "durable override"}
	updated, err := store.Configs().CompareAndSwap(context.Background(), key, snapshot.Version, values)
	if err != nil {
		t.Fatalf("persist config: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	duplicate, err := reopened.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("replay inbound after reopen: %v", err)
	}
	if !duplicate.Duplicate || duplicate.Message.SenderRef != claimed.Message.SenderRef || duplicate.Message.ChatID != claimed.Message.ChatID {
		t.Fatalf("reopened identity changed: first=%#v duplicate=%#v", claimed.Message, duplicate.Message)
	}
	reloaded, err := reopened.Configs().Load(context.Background(), key)
	if err != nil {
		t.Fatalf("load config after reopen: %v", err)
	}
	if reloaded.Version != updated.Version || reloaded.PromptOverride == nil || reloaded.PromptOverride.Text != "durable override" {
		t.Fatalf("reopened config = %#v", reloaded)
	}
}

func TestPromptMutationsAreJournaledInChatOrder(t *testing.T) {
	store := openTestStore(t)
	firstCandidate := testCandidate(t, "provider-prompt-1", "15550000031@s.whatsapp.net")
	first, err := store.Inbound().ClaimAndResolveSender(context.Background(), firstCandidate)
	if err != nil {
		t.Fatalf("claim first prompt command: %v", err)
	}
	secondCandidate := firstCandidate
	secondCandidate.ProviderMessageID = "provider-prompt-2"
	secondCandidate.Text = "/prompt clear"
	secondCandidate.OccurredAt = secondCandidate.OccurredAt.Add(time.Second)
	secondCandidate.ReceivedAt = secondCandidate.ReceivedAt.Add(time.Second)
	second, err := store.Inbound().ClaimAndResolveSender(context.Background(), secondCandidate)
	if err != nil {
		t.Fatalf("claim second prompt command: %v", err)
	}
	set := inbound.PromptCommand{Kind: inbound.PromptSet, Text: "first override"}
	if _, err := store.Inbound().BeginPromptMutation(context.Background(), first.Message, set, 1); err != nil {
		t.Fatalf("journal first prompt mutation: %v", err)
	}
	clear := inbound.PromptCommand{Kind: inbound.PromptClear}
	if _, err := store.Inbound().BeginPromptMutation(context.Background(), second.Message, clear, 2); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("second mutation error = %v, want conflict while first is unapplied", err)
	}
	if err := store.Inbound().MarkPromptMutationApplied(context.Background(), first.Message, 1, 2); err != nil {
		t.Fatalf("mark first prompt mutation applied: %v", err)
	}
	journal, err := store.Inbound().BeginPromptMutation(context.Background(), second.Message, clear, 2)
	if err != nil {
		t.Fatalf("journal second prompt mutation: %v", err)
	}
	if journal.ExpectedVersion != 2 || journal.AppliedVersion != 0 {
		t.Fatalf("second prompt journal = %#v", journal)
	}
}

func TestAccountPolicyReconciliationFailsClosedAcrossRestart(t *testing.T) {
	store := openTestStore(t)
	candidate := testCandidate(t, "provider-policy-1", "15550000021@s.whatsapp.net")
	candidate.ProviderSenderAddress = "15550000020@s.whatsapp.net"
	candidate.Owner = true
	claimed, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim policy fixture: %v", err)
	}
	key := agent.Key{TenantID: candidate.TenantID, AccountID: candidate.AccountID, ChatID: claimed.Message.ChatID}

	if err := store.Inbound().ReconcileAccountPolicy(
		context.Background(), candidate.TenantID, candidate.AccountID,
		"15550000999@s.whatsapp.net", []string{"15550000888@s.whatsapp.net"},
	); err != nil {
		t.Fatalf("remove prior policy: %v", err)
	}
	allowed, err := store.Inbound().IsChatAllowlisted(context.Background(), key)
	if err != nil || allowed {
		t.Fatalf("stale chat allowlist survived reconciliation: allowed=%v err=%v", allowed, err)
	}
	var owner int
	if err := store.db.QueryRowContext(context.Background(), `SELECT owner FROM participants
      WHERE tenant_id = ? AND account_id = ? AND id = ?`,
		candidate.TenantID.String(), candidate.AccountID.String(), claimed.Message.SenderID.String(),
	).Scan(&owner); err != nil {
		t.Fatalf("read reconciled owner: %v", err)
	}
	if owner != 0 {
		t.Fatal("stale owner authorization survived reconciliation")
	}

	if err := store.Inbound().ReconcileAccountPolicy(
		context.Background(), candidate.TenantID, candidate.AccountID,
		candidate.ProviderSenderAddress, []string{candidate.ProviderChatAddress},
	); err != nil {
		t.Fatalf("restore current policy: %v", err)
	}
	allowed, err = store.Inbound().IsChatAllowlisted(context.Background(), key)
	if err != nil || !allowed {
		t.Fatalf("current chat allowlist was not restored: allowed=%v err=%v", allowed, err)
	}
	if err := store.db.QueryRowContext(context.Background(), `SELECT owner FROM participants
      WHERE tenant_id = ? AND account_id = ? AND id = ?`,
		candidate.TenantID.String(), candidate.AccountID.String(), claimed.Message.SenderID.String(),
	).Scan(&owner); err != nil || owner != 1 {
		t.Fatalf("current owner was not restored: owner=%d err=%v", owner, err)
	}
}

func TestSenderRefCollisionRetriesWithoutChangingExistingReference(t *testing.T) {
	refs := []string{"u_AAAAAAAA", "u_AAAAAAAA", "u_BBBBBBBB"}
	var calls atomic.Int32
	factory := func() (identity.SenderRef, error) {
		index := int(calls.Add(1)) - 1
		if index >= len(refs) {
			return identity.SenderRef{}, context.DeadlineExceeded
		}
		return identity.ParseSenderRef(refs[index])
	}
	store, err := OpenWithOptions(context.Background(), filepath.Join(t.TempDir(), "app.db"), Options{SenderRefFactory: factory})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first := testCandidate(t, "sender-ref-1", "15550000031@s.whatsapp.net")
	first.ProviderSenderAddress = "15550000032@s.whatsapp.net"
	firstClaim, err := store.Inbound().ClaimAndResolveSender(context.Background(), first)
	if err != nil {
		t.Fatalf("claim first sender: %v", err)
	}
	second := first
	second.ProviderMessageID = "sender-ref-2"
	second.ProviderSenderAddress = "15550000033@s.whatsapp.net"
	secondClaim, err := store.Inbound().ClaimAndResolveSender(context.Background(), second)
	if err != nil {
		t.Fatalf("claim colliding sender: %v", err)
	}
	if firstClaim.Message.SenderRef.String() != refs[0] || secondClaim.Message.SenderRef.String() != refs[2] || calls.Load() != 3 {
		t.Fatalf("collision refs/calls = %s/%s/%d", firstClaim.Message.SenderRef, secondClaim.Message.SenderRef, calls.Load())
	}
	firstAgain, err := store.Inbound().ClaimAndResolveSender(context.Background(), first)
	if err != nil || firstAgain.Message.SenderRef != firstClaim.Message.SenderRef {
		t.Fatalf("existing ref changed after collision: first=%s again=%s err=%v", firstClaim.Message.SenderRef, firstAgain.Message.SenderRef, err)
	}
}

func TestTurnPlanAndActionReceiptAreAtomicAndReplayable(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)}
	store := openTestStoreWithClock(t, clock)
	candidate := testCandidate(t, "provider-message-2", "15550000002@s.whatsapp.net")
	candidate.ReceivedAt = clock.now
	candidate.OccurredAt = clock.now.Add(-time.Second)
	claimed, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim inbound: %v", err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	configSnapshot, err := store.Configs().LoadOrCreate(context.Background(), key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	capabilities, _ := agent.NewCapabilitySet()
	invocation := agent.Invocation{
		ID:            claimed.Message.InvocationID,
		Causation:     agent.CausationRef{Kind: agent.CausationMessage, ID: claimed.Message.CausationID},
		Cause:         agent.CauseInboundMessage,
		Sender:        &agent.SenderContext{ParticipantID: claimed.Message.SenderID, Ref: claimed.Message.SenderRef, DisplayName: claimed.Message.SenderName},
		Input:         []agent.ContentPart{agent.TextPart{Text: claimed.Message.Text}},
		Capabilities:  capabilities,
		PolicyVersion: configSnapshot.Version,
		RequestedAt:   claimed.Message.OccurredAt,
	}
	digest, err := agent.DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest invocation: %v", err)
	}
	turns := store.Turns()
	claim, err := turns.Claim(context.Background(), agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: clock.now})
	if err != nil {
		t.Fatalf("claim turn: %v", err)
	}
	if claim.State != agent.TurnGenerating || claim.Lease == "" {
		t.Fatalf("turn claim = %#v", claim)
	}
	if _, err := turns.Claim(context.Background(), agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: clock.now}); !agent.IsCode(err, agent.ErrorInProgress) {
		t.Fatalf("active lease error = %v, want in_progress", err)
	}
	plan, err := turns.CommitPlan(context.Background(), agent.CommitPlanRequest{
		Key: key, InvocationID: invocation.ID, Lease: claim.Lease, ConfigVersion: configSnapshot.Version, ResponseText: "hello back",
	})
	if err != nil {
		t.Fatalf("commit plan: %v", err)
	}
	plannedRecord, err := turns.Load(context.Background(), key, invocation.ID)
	if err != nil || plannedRecord.State != agent.TurnResponsePlanned || plannedRecord.Delivery != agent.DeliveryPending {
		t.Fatalf("planned turn = %#v, err=%v", plannedRecord, err)
	}
	replay, err := turns.Claim(context.Background(), agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: clock.now})
	if err != nil {
		t.Fatalf("replay claim: %v", err)
	}
	if replay.Plan == nil || replay.Plan.ActionID != plan.ActionID || replay.Plan.ResponseID != plan.ResponseID {
		t.Fatalf("replay plan = %#v, original = %#v", replay.Plan, plan)
	}
	changed := invocation
	changed.Input = []agent.ContentPart{agent.TextPart{Text: "different"}}
	changedDigest, _ := agent.DigestInvocation(key, changed)
	if _, err := turns.Claim(context.Background(), agent.ClaimTurnRequest{Key: key, Invocation: changed, Digest: changedDigest, Now: clock.now}); !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("changed digest error = %v, want conflict", err)
	}
	actions := store.Actions()
	clock.now = clock.now.Add(3 * time.Second)
	recoverable, err := actions.ListRecoverable(context.Background(), key.TenantID, clock.now, 10)
	if err != nil {
		t.Fatalf("list recoverable action: %v", err)
	}
	if len(recoverable) != 1 || recoverable[0] != plan.Dispatch {
		t.Fatalf("recoverable actions = %#v, want plan dispatch", recoverable)
	}
	storedAction, err := actions.Claim(context.Background(), plan.Dispatch, clock.now)
	if err != nil {
		t.Fatalf("claim action: %v", err)
	}
	if storedAction.State != action.StateClaimed || storedAction.Text != "hello back" {
		t.Fatalf("action claim = %#v", storedAction)
	}
	pendingRecord, err := turns.Load(context.Background(), key, invocation.ID)
	if err != nil || pendingRecord.State != agent.TurnDeliveryPending {
		t.Fatalf("delivery-pending turn = %#v, err=%v", pendingRecord, err)
	}
	if err := actions.MarkExecuting(context.Background(), plan.Dispatch, storedAction.Lease, clock.now); err != nil {
		t.Fatalf("mark executing: %v", err)
	}
	clock.now = clock.now.Add(time.Second)
	if err := actions.Complete(context.Background(), plan.Dispatch, storedAction.Lease, "provider-receipt", clock.now); err != nil {
		t.Fatalf("complete action: %v", err)
	}
	record, err := turns.Load(context.Background(), key, invocation.ID)
	if err != nil {
		t.Fatalf("load completed turn: %v", err)
	}
	if record.State != agent.TurnSucceeded || record.Delivery != agent.DeliverySucceeded || record.Plan.ActionID != plan.ActionID {
		t.Fatalf("completed turn = %#v", record)
	}
	observed, err := actions.Claim(context.Background(), plan.Dispatch, clock.now)
	if err != nil || observed.State != action.StateSucceeded {
		t.Fatalf("observe completed action = %#v, err=%v", observed, err)
	}
	clock.now = clock.now.Add(25 * time.Hour)
	maintained, err := store.Maintain(context.Background(), maintenance.Request{
		TenantID: key.TenantID, Now: clock.now, ScrubBefore: clock.now.Add(-24 * time.Hour),
		DeleteBefore: clock.now.Add(-30 * 24 * time.Hour), BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("scrub terminal content: %v", err)
	}
	if maintained.InboundScrubbed != 1 || maintained.ActionsScrubbed != 1 || maintained.TurnsDeleted != 0 {
		t.Fatalf("scrub result = %#v", maintained)
	}
	observed, err = actions.Claim(context.Background(), plan.Dispatch, clock.now)
	if err != nil || observed.State != action.StateSucceeded || observed.Text != "" {
		t.Fatalf("observe scrubbed terminal action = %#v, err=%v", observed, err)
	}
	clock.now = clock.now.Add(30 * 24 * time.Hour)
	maintained, err = store.Maintain(context.Background(), maintenance.Request{
		TenantID: key.TenantID, Now: clock.now, ScrubBefore: clock.now.Add(-24 * time.Hour),
		DeleteBefore: clock.now.Add(-30 * 24 * time.Hour), BatchSize: 10,
	})
	if err != nil || maintained.TurnsDeleted != 1 {
		t.Fatalf("delete expired terminal turn = %#v, err=%v", maintained, err)
	}
	var remaining int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM outbound_actions
      WHERE tenant_id = ? AND action_id = ?`, key.TenantID.String(), plan.ActionID.String()).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("cascaded terminal action count = %d, err=%v", remaining, err)
	}
}

func TestPreclaimedInboundCannotBeReboundToDifferentInvocationContent(t *testing.T) {
	store := openTestStore(t)
	candidate := testCandidate(t, "provider-message-integrity", "15550000007@s.whatsapp.net")
	claimed, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim inbound: %v", err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	snapshot, err := store.Configs().LoadOrCreate(context.Background(), key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	capabilities, _ := agent.NewCapabilitySet()
	invocation := agent.Invocation{
		ID: claimed.Message.InvocationID, Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: claimed.Message.CausationID},
		Cause:  agent.CauseInboundMessage,
		Sender: &agent.SenderContext{ParticipantID: claimed.Message.SenderID, Ref: claimed.Message.SenderRef, DisplayName: claimed.Message.SenderName},
		Input:  []agent.ContentPart{agent.TextPart{Text: "tampered text"}}, Capabilities: capabilities,
		PolicyVersion: snapshot.Version, RequestedAt: claimed.Message.OccurredAt,
	}
	digest, _ := agent.DigestInvocation(key, invocation)
	_, err = store.Turns().Claim(context.Background(), agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: time.Now().UTC()})
	if !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("tampered preclaim error = %v, want conflict", err)
	}
}

func TestExpiredGenerationLeaseCannotPublishOrFailTurn(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)}
	store, err := OpenWithOptions(context.Background(), filepath.Join(t.TempDir(), "app.db"), Options{
		GenerationLeaseTTL: time.Second,
		ActionLeaseTTL:     time.Minute,
		Clock:              clock,
	})
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	key := testKey(t)
	invocationID, _ := identity.NewInvocationID()
	causationID, _ := identity.NewCausationID()
	capabilities, _ := agent.NewCapabilitySet()
	invocation := agent.Invocation{
		ID:            invocationID,
		Causation:     agent.CausationRef{Kind: agent.CausationRequest, ID: causationID},
		Cause:         agent.CauseDirectRequest,
		Input:         []agent.ContentPart{agent.TextPart{Text: "lease test"}},
		Capabilities:  capabilities,
		PolicyVersion: agent.InitialConfigVersion,
		RequestedAt:   clock.now,
	}
	digest, err := agent.DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest invocation: %v", err)
	}
	claim, err := store.Turns().Claim(context.Background(), agent.ClaimTurnRequest{
		Key: key, Invocation: invocation, Digest: digest, Now: clock.now,
	})
	if err != nil {
		t.Fatalf("claim turn: %v", err)
	}
	clock.now = clock.now.Add(time.Second)

	_, err = store.Turns().CommitPlan(context.Background(), agent.CommitPlanRequest{
		Key: key, InvocationID: invocationID, Lease: claim.Lease,
		ConfigVersion: agent.InitialConfigVersion, ResponseText: "too late",
	})
	if !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("expired commit error = %v, want conflict", err)
	}
	err = store.Turns().FailGeneration(context.Background(), agent.FailGenerationRequest{
		Key: key, InvocationID: invocationID, Lease: claim.Lease, Code: agent.ErrorProviderFailure,
	})
	if !agent.IsCode(err, agent.ErrorConflict) {
		t.Fatalf("expired failure error = %v, want conflict", err)
	}

	second, err := store.Turns().Claim(context.Background(), agent.ClaimTurnRequest{
		Key: key, Invocation: invocation, Digest: digest, Now: clock.now,
	})
	if err != nil {
		t.Fatalf("reclaim expired turn: %v", err)
	}
	if second.Lease == "" || second.Lease == claim.Lease {
		t.Fatalf("replacement lease = %q, original = %q", second.Lease, claim.Lease)
	}
}

func TestGenerationRetriesAreBoundedAndBecomeTerminal(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)}
	store := openTestStoreWithClock(t, clock)
	key := testKey(t)
	invocationID, _ := identity.NewInvocationID()
	causationID, _ := identity.NewCausationID()
	capabilities, _ := agent.NewCapabilitySet()
	invocation := agent.Invocation{
		ID: invocationID, Causation: agent.CausationRef{Kind: agent.CausationRequest, ID: causationID},
		Cause: agent.CauseDirectRequest, Input: []agent.ContentPart{agent.TextPart{Text: "retry test"}},
		Capabilities: capabilities, PolicyVersion: agent.InitialConfigVersion, RequestedAt: clock.now,
	}
	digest, err := agent.DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest invocation: %v", err)
	}
	for attempt := 1; attempt <= maxGenerationAttempts; attempt++ {
		claim, err := store.Turns().Claim(context.Background(), agent.ClaimTurnRequest{
			Key: key, Invocation: invocation, Digest: digest, Now: clock.now,
		})
		if err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
		if err := store.Turns().FailGeneration(context.Background(), agent.FailGenerationRequest{
			Key: key, InvocationID: invocationID, Lease: claim.Lease, Code: agent.ErrorUnavailable,
			Retryable: true, RetryAfter: clock.now,
		}); err != nil {
			t.Fatalf("fail attempt %d: %v", attempt, err)
		}
	}
	_, err = store.Turns().Claim(context.Background(), agent.ClaimTurnRequest{
		Key: key, Invocation: invocation, Digest: digest, Now: clock.now,
	})
	if !agent.IsCode(err, agent.ErrorProviderFailure) {
		t.Fatalf("exhausted claim error = %v, want provider_failure", err)
	}
	record, err := store.Turns().Load(context.Background(), key, invocationID)
	if err != nil {
		t.Fatalf("load exhausted turn: %v", err)
	}
	if record.State != agent.TurnFailedTerminal || record.Plan != nil {
		t.Fatalf("exhausted turn = %#v", record)
	}
}

func TestExpiredExecutingActionBecomesUnknownAndCannotBeReclaimed(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)}
	store, err := OpenWithOptions(context.Background(), filepath.Join(t.TempDir(), "app.db"), Options{
		GenerationLeaseTTL: time.Minute, ActionLeaseTTL: time.Second, Clock: clock,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	candidate := testCandidate(t, "unknown-expired-action", "15550000041@s.whatsapp.net")
	candidate.ReceivedAt = clock.now
	candidate.OccurredAt = clock.now.Add(-time.Second)
	claimed, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim inbound: %v", err)
	}
	key := agent.Key{TenantID: candidate.TenantID, AccountID: candidate.AccountID, ChatID: claimed.Message.ChatID}
	snapshot, err := store.Configs().LoadOrCreate(context.Background(), key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	capabilities, _ := agent.NewCapabilitySet()
	invocation := agent.Invocation{
		ID: claimed.Message.InvocationID, Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: claimed.Message.CausationID},
		Cause:  agent.CauseInboundMessage,
		Sender: &agent.SenderContext{ParticipantID: claimed.Message.SenderID, Ref: claimed.Message.SenderRef, DisplayName: claimed.Message.SenderName},
		Input:  []agent.ContentPart{agent.TextPart{Text: claimed.Message.Text}}, Capabilities: capabilities,
		PolicyVersion: snapshot.Version, RequestedAt: claimed.Message.OccurredAt,
	}
	digest, _ := agent.DigestInvocation(key, invocation)
	turnClaim, err := store.Turns().Claim(context.Background(), agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: clock.now})
	if err != nil {
		t.Fatalf("claim turn: %v", err)
	}
	plan, err := store.Turns().CommitPlan(context.Background(), agent.CommitPlanRequest{
		Key: key, InvocationID: invocation.ID, Lease: turnClaim.Lease, ConfigVersion: snapshot.Version, ResponseText: "ambiguous send",
	})
	if err != nil {
		t.Fatalf("commit plan: %v", err)
	}
	stored, err := store.Actions().Claim(context.Background(), plan.Dispatch, clock.now)
	if err != nil {
		t.Fatalf("claim action: %v", err)
	}
	if err := store.Actions().MarkExecuting(context.Background(), plan.Dispatch, stored.Lease, clock.now); err != nil {
		t.Fatalf("mark action executing: %v", err)
	}
	clock.now = clock.now.Add(time.Second)
	recoverable, err := store.Actions().ListRecoverable(context.Background(), key.TenantID, clock.now, 10)
	if err != nil || len(recoverable) != 1 || recoverable[0] != plan.Dispatch {
		t.Fatalf("expired executing recovery list = %#v, err=%v", recoverable, err)
	}
	observed, err := store.Actions().Claim(context.Background(), plan.Dispatch, clock.now)
	if err != nil {
		t.Fatalf("observe expired action: %v", err)
	}
	if observed.State != action.StateUnknownOutcome {
		t.Fatalf("expired action state = %v, want unknown", observed.State)
	}
	again, err := store.Actions().Claim(context.Background(), plan.Dispatch, clock.now.Add(time.Minute))
	if err != nil || again.State != action.StateUnknownOutcome {
		t.Fatalf("unknown action was reclaimable: state=%v err=%v", again.State, err)
	}
	record, err := store.Turns().Load(context.Background(), key, invocation.ID)
	if err != nil || record.State != agent.TurnUnknownOutcome || record.Delivery != agent.DeliveryUnknownOutcome {
		t.Fatalf("unknown turn receipt = %#v, err=%v", record, err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	return openTestStoreWithClock(t, &testClock{now: time.Now().UTC()})
}

func openTestStoreWithClock(t *testing.T, clock *testClock) *Store {
	t.Helper()
	store, err := OpenWithOptions(context.Background(), filepath.Join(t.TempDir(), "app.db"), Options{
		GenerationLeaseTTL: time.Minute,
		ActionLeaseTTL:     time.Minute,
		Clock:              clock,
	})
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testKey(t *testing.T) agent.Key {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	return agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
}

func testDefaults(t *testing.T) agent.ConfigValues {
	t.Helper()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	policyID, _ := identity.ParsePolicyID("part1-chat-gate.v1")
	return agent.ConfigValues{
		Model:      agent.ModelConfig{ProviderID: providerID, Model: "test-model", MaxOutputTokens: 256},
		Prompt:     "base prompt",
		Permission: agent.PermissionConfig{PolicyID: policyID, Revision: 1},
	}
}

func changedDefaults(defaults agent.ConfigValues) agent.ConfigValues {
	defaults.Prompt = "must not replace"
	return defaults
}

func testCandidate(t *testing.T, providerMessageID, chat string) conversation.IncomingCandidate {
	t.Helper()
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	now := time.Now().UTC()
	return conversation.IncomingCandidate{
		TenantID:              tenantID,
		AccountID:             accountID,
		ProviderMessageID:     providerMessageID,
		ProviderChatAddress:   chat,
		ProviderSenderAddress: "15550000009@s.whatsapp.net",
		SenderName:            "Tester",
		ChatKind:              conversation.ChatDirect,
		Text:                  "hello",
		Owner:                 true,
		Allowlisted:           true,
		OccurredAt:            now.Add(-time.Second),
		ReceivedAt:            now,
	}
}

type testClock struct{ now time.Time }

func (clock *testClock) Now() time.Time { return clock.now }

func assertSQLitePragma(t *testing.T, db *sql.DB, name, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow("PRAGMA " + name).Scan(&got); err != nil {
		t.Fatalf("read PRAGMA %s: %v", name, err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %q, want %q", name, got, want)
	}
}
