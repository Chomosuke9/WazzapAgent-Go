package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/maintenance"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func TestOpenAppliesAndVerifiesEmbeddedMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	assertSQLitePragma(t, store.db, "foreign_keys", "1")
	assertSQLitePragma(t, store.db, "journal_mode", "wal")
	assertSQLitePragma(t, store.db, "integrity_check", "ok")
	var migrations int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrations != 15 {
		t.Fatalf("migration count = %d, want 15", migrations)
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

func TestPart2MigrationUpgradesAnExistingPart1Database(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	db, err := sql.Open("sqlite", databaseDSN(path, defaultBusyTimeoutMS))
	if err != nil {
		t.Fatalf("open raw Part 1 database: %v", err)
	}
	part1, err := migrationFiles.ReadFile("migrations/001_part1.sql")
	if err != nil {
		t.Fatalf("read Part 1 migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
        version INTEGER PRIMARY KEY,
        name TEXT NOT NULL UNIQUE,
        checksum TEXT NOT NULL,
        applied_at_ms INTEGER NOT NULL
    ) STRICT`); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(part1)); err != nil {
		t.Fatalf("apply Part 1 schema: %v", err)
	}
	digest := sha256.Sum256(part1)
	if _, err := db.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, checksum, applied_at_ms) VALUES (1, ?, ?, ?)",
		"001_part1.sql", hex.EncodeToString(digest[:]), time.Now().UTC().UnixMilli(),
	); err != nil {
		t.Fatalf("record Part 1 migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw Part 1 database: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade database: %v", err)
	}
	defer store.Close()
	var migrations int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatalf("count upgraded migrations: %v", err)
	}
	if migrations != 15 {
		t.Fatalf("upgraded migration count = %d, want 15", migrations)
	}
	if _, err := store.db.ExecContext(ctx, "SELECT quoted_message_id, quoted_sequence, batch_ready_at_ms FROM inbound_events LIMIT 0"); err != nil {
		t.Fatalf("Part 2 inbound columns are unavailable: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "SELECT sequence FROM history_entries LIMIT 0"); err != nil {
		t.Fatalf("Part 2 history table is unavailable: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "SELECT effect_id FROM typed_effects LIMIT 0"); err != nil {
		t.Fatalf("typed effects table is unavailable: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "SELECT message_id, token FROM message_mentions LIMIT 0"); err != nil {
		t.Fatalf("message mention table is unavailable: %v", err)
	}
}

func TestResolveMessageTargetKeepsProviderFieldsAtAdapterEdge(t *testing.T) {
	store := openTestStore(t)
	candidate := testCandidate(t, "provider-effect-target", "15550000042@s.whatsapp.net")
	claimed, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim message target: %v", err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	chat, providerMessage, sender, occurredAt, err := store.Inbound().ResolveMessageTarget(context.Background(), key, claimed.Message.ID)
	if err != nil {
		t.Fatalf("resolve message target: %v", err)
	}
	if chat != candidate.ProviderChatAddress || providerMessage != candidate.ProviderMessageID || sender != candidate.SenderLID.String() || occurredAt.IsZero() {
		t.Fatalf("resolved provider target = %q/%q/%q/%v", chat, providerMessage, sender, occurredAt)
	}
	otherKey := testKey(t)
	if _, _, _, _, err := store.Inbound().ResolveMessageTarget(context.Background(), otherKey, claimed.Message.ID); !agent.IsCode(err, agent.ErrorNotFound) {
		t.Fatalf("cross-chat provider target = %v, want not found", err)
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

func TestConfigPersistsModerationLevelAndAlwaysDerivesReactionTool(t *testing.T) {
	store := openTestStore(t)
	key := testKey(t)
	snapshot, err := store.Configs().LoadOrCreate(context.Background(), key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	values := snapshot.Values()
	values.Permission.ModerationLevel = agent.ModerationDeleteMute
	updated, err := store.Configs().CompareAndSwap(context.Background(), key, snapshot.Version, values)
	if err != nil {
		t.Fatalf("persist moderation level: %v", err)
	}
	reloaded, err := store.Configs().Load(context.Background(), key)
	if err != nil || reloaded.Permission.ModerationLevel != agent.ModerationDeleteMute ||
		!reloaded.Permission.ModelToolCapabilities().Has("message.react") ||
		!reloaded.Permission.ModelToolCapabilities().Has("group.delete") ||
		!reloaded.Permission.ModelToolCapabilities().Has("group.mute") ||
		reloaded.Permission.ModelToolCapabilities().Has("group.kick") || reloaded.Version != updated.Version {
		t.Fatalf("reloaded permission = %#v, %v", reloaded.Permission, err)
	}
	values = updated.Values()
	values.Permission.ModerationLevel = 4
	if _, err := store.Configs().CompareAndSwap(context.Background(), key, updated.Version, values); err == nil {
		t.Fatal("invalid moderation level was accepted into durable config")
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
	candidate.ProviderSenderPhone = "15550000020@s.whatsapp.net"
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
		candidate.ProviderSenderPhone, []string{candidate.ProviderChatAddress},
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

func TestAccountPolicyReconciliationAppliesChatAllowlistWildcards(t *testing.T) {
	store := openTestStore(t)
	direct := testCandidate(t, "wildcard-direct", "10000000001@lid")
	group := direct
	group.ProviderMessageID = "wildcard-group"
	group.ProviderChatAddress = "120363000000000001@g.us"
	group.ChatKind = conversation.ChatGroup
	status := direct
	status.ProviderMessageID = "wildcard-status"
	status.ProviderChatAddress = "status@broadcast"
	status.ChatKind = conversation.ChatStatus

	candidates := []conversation.IncomingCandidate{direct, group, status}
	keys := make([]agent.Key, 0, len(candidates))
	for _, candidate := range candidates {
		claimed, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
		if err != nil {
			t.Fatalf("claim %s: %v", candidate.ProviderMessageID, err)
		}
		keys = append(keys, agent.Key{TenantID: candidate.TenantID, AccountID: candidate.AccountID, ChatID: claimed.Message.ChatID})
	}

	assertAllowed := func(pattern string, want ...bool) {
		t.Helper()
		if err := store.Inbound().ReconcileAccountPolicy(
			context.Background(), direct.TenantID, direct.AccountID, direct.ProviderSenderPhone, []string{pattern},
		); err != nil {
			t.Fatalf("reconcile wildcard %q: %v", pattern, err)
		}
		for index, key := range keys {
			got, err := store.Inbound().IsChatAllowlisted(context.Background(), key)
			if err != nil || got != want[index] {
				t.Fatalf("wildcard %q chat %d = %v, %v; want %v", pattern, index, got, err, want[index])
			}
		}
	}

	assertAllowed(policy.ChatAllowlistDirect, true, false, false)
	assertAllowed(policy.ChatAllowlistGroup, false, true, false)
	assertAllowed(policy.ChatAllowlistAll, true, true, false)
}

func TestSenderRefCollisionRetriesWithoutChangingExistingReference(t *testing.T) {
	refs := []string{"aaaaaa", "aaaaaa", "bbbbbb"}
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
	first.ProviderSenderPhone = "15550000032@s.whatsapp.net"
	firstClaim, err := store.Inbound().ClaimAndResolveSender(context.Background(), first)
	if err != nil {
		t.Fatalf("claim first sender: %v", err)
	}
	second := first
	second.ProviderMessageID = "sender-ref-2"
	second.ProviderSenderPhone = "15550000033@s.whatsapp.net"
	second.SenderLID, _ = identity.ParseLID("10000000033@lid")
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

func TestSenderRefAndLIDResolveBothWays(t *testing.T) {
	store := openTestStore(t)
	candidate := testCandidate(t, "lid-round-trip", "15550000041@s.whatsapp.net")
	claimed, err := store.Inbound().ClaimAndResolveSender(context.Background(), candidate)
	if err != nil {
		t.Fatalf("claim sender: %v", err)
	}
	key := agent.Key{TenantID: candidate.TenantID, AccountID: candidate.AccountID, ChatID: claimed.Message.ChatID}
	lid, err := store.Inbound().ResolveLID(context.Background(), key, claimed.Message.SenderRef)
	if err != nil || lid != candidate.SenderLID {
		t.Fatalf("senderRef -> LID = %s, %v", lid, err)
	}
	ref, err := store.Inbound().ResolveSenderRef(context.Background(), key, candidate.SenderLID)
	if err != nil || ref != claimed.Message.SenderRef {
		t.Fatalf("LID -> senderRef = %s, %v", ref, err)
	}
	aliasChanged := candidate
	aliasChanged.ProviderMessageID = "lid-round-trip-new-phone"
	aliasChanged.ProviderSenderPhone = "15550000042@s.whatsapp.net"
	aliasChanged.ReceivedAt = aliasChanged.ReceivedAt.Add(time.Second)
	aliasChanged.OccurredAt = aliasChanged.OccurredAt.Add(time.Second)
	changed, err := store.Inbound().ClaimAndResolveSender(context.Background(), aliasChanged)
	if err != nil || changed.Message.SenderID != claimed.Message.SenderID || changed.Message.SenderRef != claimed.Message.SenderRef {
		t.Fatalf("phone alias changed canonical identity: %#v, %v", changed.Message, err)
	}
	conflict := aliasChanged
	conflict.ProviderMessageID = "lid-conflicting-phone"
	conflict.SenderLID, _ = identity.ParseLID("10000000042@lid")
	conflict.ReceivedAt = conflict.ReceivedAt.Add(time.Second)
	conflict.OccurredAt = conflict.OccurredAt.Add(time.Second)
	if _, err := store.Inbound().ClaimAndResolveSender(context.Background(), conflict); !agent.IsCode(err, agent.ErrorIntegrityFailure) {
		t.Fatalf("conflicting LID/phone binding error = %v", err)
	}
}

func TestInboundMentionsKeepRawTextAndSurviveAsHistorySnapshots(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	firstCandidate := testCandidate(t, "mention-source", "15550000061@s.whatsapp.net")
	targetLID, _ := identity.ParseLID("10000000077@lid")
	firstCandidate.Text = "halo @10000000077 dan @999999"
	firstCandidate.Mentions = []conversation.IncomingMention{
		{Token: "@10000000077", TargetLID: targetLID, DisplayName: "Budi"},
		{Token: "@999999", Bot: true},
	}
	first, err := store.Inbound().ClaimAndResolveSender(ctx, firstCandidate)
	if err != nil {
		t.Fatalf("claim mentioned message: %v", err)
	}
	if first.Message.Text != firstCandidate.Text || len(first.Message.Mentions) != 2 || first.Message.Mentions[0].SenderRef.IsZero() {
		t.Fatalf("claimed mention message = %#v", first.Message)
	}
	var storedText, storedName string
	var storedRef string
	if err := store.db.QueryRowContext(ctx, `SELECT e.input_text, m.sender_ref, r.display_name
      FROM inbound_events e
		JOIN message_mentions m ON m.tenant_id = e.tenant_id AND m.account_id = e.account_id
        AND m.chat_id = e.chat_id AND m.message_id = e.message_id AND m.ordinal = 0
      JOIN sender_refs r ON r.tenant_id = m.tenant_id AND r.account_id = m.account_id
        AND r.chat_id = m.chat_id AND r.sender_ref = m.sender_ref
      WHERE e.tenant_id = ? AND e.account_id = ? AND e.chat_id = ? AND e.message_id = ?`,
		first.Message.TenantID.String(), first.Message.AccountID.String(), first.Message.ChatID.String(), first.Message.ID.String(),
	).Scan(&storedText, &storedRef, &storedName); err != nil {
		t.Fatalf("read raw mention storage: %v", err)
	}
	if storedText != firstCandidate.Text || storedRef != first.Message.Mentions[0].SenderRef.String() || storedName != "Budi" {
		t.Fatalf("raw mention storage = %q/%q/%q", storedText, storedRef, storedName)
	}

	secondCandidate := firstCandidate
	secondCandidate.ProviderMessageID = "mention-quote"
	secondCandidate.ProviderQuotedMessageID = firstCandidate.ProviderMessageID
	secondCandidate.Text = "lanjut"
	secondCandidate.Mentions = nil
	secondCandidate.OccurredAt = secondCandidate.OccurredAt.Add(time.Second)
	secondCandidate.ReceivedAt = secondCandidate.ReceivedAt.Add(time.Second)
	second, err := store.Inbound().ClaimAndResolveSender(ctx, secondCandidate)
	if err != nil {
		t.Fatalf("claim quoted mention message: %v", err)
	}
	if second.Message.Quote == nil || second.Message.Quote.Text != firstCandidate.Text || len(second.Message.Quote.Mentions) != 2 {
		t.Fatalf("resolved quote = %#v", second.Message.Quote)
	}

	key := agent.Key{TenantID: first.Message.TenantID, AccountID: first.Message.AccountID, ChatID: first.Message.ChatID}
	snapshot, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if err := store.Inbound().MarkIgnored(ctx, first.Message, inbound.IgnorePolicyDenied); err != nil {
		t.Fatalf("mark first ignored: %v", err)
	}
	if err := store.Inbound().MarkIgnored(ctx, second.Message, inbound.IgnorePolicyDenied); err != nil {
		t.Fatalf("mark second ignored: %v", err)
	}
	now := time.Now().UTC().Add(72 * time.Hour)
	maintained, err := store.Maintain(ctx, maintenance.Request{
		TenantID: key.TenantID, Now: now, ScrubBefore: now.Add(-24 * time.Hour),
		DeleteBefore: now.Add(-48 * time.Hour), BatchSize: 100,
	})
	if err != nil || maintained.TurnsDeleted != 2 {
		t.Fatalf("delete inbound sources = %#v, err=%v", maintained, err)
	}

	page, err := store.History().ListIfConfigVersion(ctx, key, snapshot.Version, agent.HistoryQuery{Limit: 10})
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("load retained mention history = %#v, err=%v", page, err)
	}
	if len(page.Entries[0].Mentions) != 2 || page.Entries[0].Mentions[0].DisplayName != "Budi" ||
		page.Entries[1].Quote == nil || len(page.Entries[1].Quote.Mentions) != 2 {
		t.Fatalf("retained mention snapshots = %#v", page.Entries)
	}
	var inboundCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbound_events
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(),
	).Scan(&inboundCount); err != nil || inboundCount != 0 {
		t.Fatalf("remaining inbound rows = %d, err=%v", inboundCount, err)
	}
}

func TestClaimedBatchSurvivesReopenAndRecoversThroughItsAnchor(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "app.db")
	clock := &testClock{now: time.Unix(1_700_000_000, 0).UTC()}
	store, err := OpenWithOptions(ctx, path, Options{Clock: clock})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	firstCandidate := testCandidate(t, "batch-restart-1", "15550000044@s.whatsapp.net")
	firstCandidate.OccurredAt = clock.now.Add(-time.Second)
	firstCandidate.ReceivedAt = clock.now
	secondCandidate := testCandidate(t, "batch-restart-2", "15550000044@s.whatsapp.net")
	secondCandidate.TenantID = firstCandidate.TenantID
	secondCandidate.AccountID = firstCandidate.AccountID
	secondCandidate.OccurredAt = firstCandidate.OccurredAt
	secondCandidate.ReceivedAt = firstCandidate.ReceivedAt
	first, err := store.Inbound().ClaimAndResolveSender(ctx, firstCandidate)
	if err != nil {
		t.Fatalf("claim first: %v", err)
	}
	second, err := store.Inbound().ClaimAndResolveSender(ctx, secondCandidate)
	if err != nil {
		t.Fatalf("claim second: %v", err)
	}
	// Make lexical InvocationID order the opposite of durable intake order.
	// Batching must use the SQLite intake sequence, not a random UUID tie-break.
	firstID, _ := identity.ParseInvocationID("ffffffff-ffff-7fff-bfff-ffffffffffff")
	secondID, _ := identity.ParseInvocationID("00000000-0000-7000-8000-000000000001")
	for _, replacement := range []struct {
		providerID string
		value      identity.InvocationID
	}{
		{providerID: firstCandidate.ProviderMessageID, value: firstID},
		{providerID: secondCandidate.ProviderMessageID, value: secondID},
	} {
		if _, err := store.db.ExecContext(ctx, `UPDATE inbound_events SET invocation_id = ?
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND provider_message_id = ?`,
			replacement.value.String(), first.Message.TenantID.String(), first.Message.AccountID.String(),
			first.Message.ChatID.String(), replacement.providerID,
		); err != nil {
			t.Fatalf("replace invocation ID: %v", err)
		}
	}
	first.Message.InvocationID = firstID
	second.Message.InvocationID = secondID
	readyAt := clock.now.Add(time.Second)
	firstStage, err := store.Inbound().StageBatch(ctx, first.Message, readyAt)
	if err != nil {
		t.Fatalf("stage first: %v", err)
	}
	secondStage, err := store.Inbound().StageBatch(ctx, second.Message, readyAt)
	if err != nil {
		t.Fatalf("stage second: %v", err)
	}
	if !firstStage.Wait || secondStage.Wait {
		t.Fatalf("batch waiter election = first:%v second:%v", firstStage.Wait, secondStage.Wait)
	}
	clock.now = readyAt
	batch, err := store.Inbound().ClaimBatch(ctx, first.Message, clock.now, 8)
	if err != nil || len(batch.Messages) != 2 ||
		batch.Messages[1].InvocationID != second.Message.InvocationID {
		t.Fatalf("claim batch = %#v, err=%v", batch, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	reopened, err := OpenWithOptions(ctx, path, Options{Clock: clock})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	clock.now = clock.now.Add(10 * time.Second)
	recoverable, err := reopened.Inbound().ListRecoverableInbound(
		ctx, first.Message.TenantID, clock.now, clock.now.Add(-time.Second), 10,
	)
	if err != nil || len(recoverable) != 1 ||
		recoverable[0].InvocationID != second.Message.InvocationID {
		t.Fatalf("recoverable anchors = %#v, err=%v", recoverable, err)
	}
	recoveredBatch, err := reopened.Inbound().ClaimBatch(ctx, recoverable[0], clock.now, 8)
	if err != nil || len(recoveredBatch.Messages) != 2 {
		t.Fatalf("recovered batch = %#v, err=%v", recoveredBatch, err)
	}
}

func TestRetryableBatchAnchorCanBeRecoveredAndBlocksNewerMessages(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}
	store := openTestStoreWithClock(t, clock)
	firstCandidate := testCandidate(t, "batch-retry-1", "15550000046@s.whatsapp.net")
	firstCandidate.ReceivedAt = clock.now
	firstCandidate.OccurredAt = clock.now.Add(-time.Second)
	first, err := store.Inbound().ClaimAndResolveSender(ctx, firstCandidate)
	if err != nil {
		t.Fatalf("claim first: %v", err)
	}
	key := agent.Key{TenantID: first.Message.TenantID, AccountID: first.Message.AccountID, ChatID: first.Message.ChatID}
	snapshot, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	stage, err := store.Inbound().StageBatch(ctx, first.Message, clock.now)
	if err != nil || !stage.Wait {
		t.Fatalf("stage first = %#v, err=%v", stage, err)
	}
	batch, err := store.Inbound().ClaimBatch(ctx, first.Message, clock.now, 8)
	if err != nil || len(batch.Messages) != 1 {
		t.Fatalf("claim first batch = %#v, err=%v", batch, err)
	}
	capabilities, _ := agent.NewCapabilitySet()
	invocation := agent.Invocation{
		ID: first.Message.InvocationID, Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: first.Message.CausationID},
		Cause:  agent.CauseInboundMessage,
		Sender: &agent.SenderContext{ParticipantID: first.Message.SenderID, Ref: first.Message.SenderRef, DisplayName: first.Message.SenderName},
		Input:  []agent.ContentPart{agent.TextPart{Text: first.Message.Text}}, Capabilities: capabilities,
		PolicyVersion: snapshot.Version, RequestedAt: first.Message.OccurredAt,
	}
	digest, err := agent.DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest first: %v", err)
	}
	turnClaim, err := store.Turns().Claim(ctx, agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: clock.now})
	if err != nil {
		t.Fatalf("claim first turn: %v", err)
	}
	if err := store.Turns().FailGeneration(ctx, agent.FailGenerationRequest{
		Key: key, InvocationID: invocation.ID, Lease: turnClaim.Lease, Code: agent.ErrorUnavailable,
		Retryable: true, RetryAfter: clock.now,
	}); err != nil {
		t.Fatalf("mark first retryable: %v", err)
	}

	clock.now = clock.now.Add(time.Millisecond)
	secondCandidate := firstCandidate
	secondCandidate.ProviderMessageID = "batch-retry-2"
	secondCandidate.OccurredAt = clock.now
	secondCandidate.ReceivedAt = clock.now
	second, err := store.Inbound().ClaimAndResolveSender(ctx, secondCandidate)
	if err != nil {
		t.Fatalf("claim second: %v", err)
	}
	secondStage, err := store.Inbound().StageBatch(ctx, second.Message, clock.now)
	if err != nil {
		t.Fatalf("stage second: %v", err)
	}
	if secondStage.Wait || secondStage.Handled {
		t.Fatalf("newer message bypassed retryable anchor: %#v", secondStage)
	}

	recoveryStage, err := store.Inbound().StageBatch(ctx, first.Message, clock.now)
	if err != nil || !recoveryStage.Wait || recoveryStage.Handled {
		t.Fatalf("stage retry recovery = %#v, err=%v", recoveryStage, err)
	}
	recovered, err := store.Inbound().ClaimBatch(ctx, first.Message, clock.now, 8)
	if err != nil || len(recovered.Messages) != 1 || recovered.Messages[0].InvocationID != first.Message.InvocationID {
		t.Fatalf("recover retryable batch = %#v, err=%v", recovered, err)
	}
}

func TestPreBatchRetryableTurnRecoversAsSingleton(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 9, 8, 0, 30, 0, 0, time.UTC)}
	store := openTestStoreWithClock(t, clock)
	candidate := testCandidate(t, "pre-batch-retry", "15550000047@s.whatsapp.net")
	candidate.ReceivedAt = clock.now
	candidate.OccurredAt = clock.now.Add(-time.Second)
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, candidate)
	if err != nil {
		t.Fatalf("claim inbound: %v", err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	snapshot, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t))
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
	digest, err := agent.DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest invocation: %v", err)
	}
	turnClaim, err := store.Turns().Claim(ctx, agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: clock.now})
	if err != nil {
		t.Fatalf("claim turn: %v", err)
	}
	if err := store.Turns().FailGeneration(ctx, agent.FailGenerationRequest{
		Key: key, InvocationID: invocation.ID, Lease: turnClaim.Lease, Code: agent.ErrorUnavailable,
		Retryable: true, RetryAfter: clock.now,
	}); err != nil {
		t.Fatalf("mark turn retryable: %v", err)
	}
	stage, err := store.Inbound().StageBatch(ctx, claimed.Message, clock.now)
	if err != nil || !stage.Wait || stage.Handled {
		t.Fatalf("stage pre-batch recovery = %#v, err=%v", stage, err)
	}
	batch, err := store.Inbound().ClaimBatch(ctx, claimed.Message, clock.now, 8)
	if err != nil || len(batch.Messages) != 1 || batch.Messages[0].InvocationID != invocation.ID {
		t.Fatalf("pre-batch recovery = %#v, err=%v", batch, err)
	}
}

func TestMessageCannotEnterBatchAfterAConcurrentHistoryReset(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(1_700_000_000, 0).UTC()}
	store := openTestStoreWithClock(t, clock)
	candidate := testCandidate(t, "batch-reset-race", "15550000045@s.whatsapp.net")
	candidate.OccurredAt = clock.now.Add(-time.Second)
	candidate.ReceivedAt = clock.now
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, candidate)
	if err != nil {
		t.Fatalf("claim message before reset: %v", err)
	}
	key := agent.Key{
		TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID,
	}
	snapshot, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	clock.now = clock.now.Add(time.Second)
	if err := store.History().ResetIfConfigVersion(ctx, key, snapshot.Version, clock.now); err != nil {
		t.Fatalf("reset history: %v", err)
	}
	stage, err := store.Inbound().StageBatch(ctx, claimed.Message, clock.now.Add(time.Second))
	if err != nil {
		t.Fatalf("stage pre-reset message: %v", err)
	}
	if !stage.Handled {
		t.Fatal("pre-reset message entered a post-reset batch")
	}
	batch, err := store.Inbound().ClaimBatch(ctx, claimed.Message, clock.now, 8)
	if err != nil {
		t.Fatalf("observe discarded message: %v", err)
	}
	if !batch.Handled || len(batch.Messages) != 0 {
		t.Fatalf("discarded batch = %#v", batch)
	}
}

func TestLateClaimOfPreResetInboundDoesNotReintroduceHistory(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Unix(1_700_000_000, 0).UTC()}
	store := openTestStoreWithClock(t, clock)
	first := testCandidate(t, "reset-seed", "15550000046@s.whatsapp.net")
	first.ReceivedAt = clock.now
	first.OccurredAt = clock.now
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, first)
	if err != nil {
		t.Fatalf("claim seed: %v", err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	snapshot, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	resetAt := clock.now.Add(time.Second)
	if err := store.History().ResetIfConfigVersion(ctx, key, snapshot.Version, resetAt); err != nil {
		t.Fatalf("reset history: %v", err)
	}
	late := first
	late.ProviderMessageID = "reset-preexisting-late-claim"
	late.Text = "must stay hidden"
	late.OccurredAt = resetAt.Add(-time.Second)
	late.ReceivedAt = resetAt.Add(-time.Millisecond)
	lateClaim, err := store.Inbound().ClaimAndResolveSender(ctx, late)
	if err != nil {
		t.Fatalf("claim pre-reset event after reset: %v", err)
	}
	if !lateClaim.Handled {
		t.Fatal("pre-reset late claim was left eligible for processing")
	}
	page, err := store.History().ListIfConfigVersion(ctx, key, snapshot.Version, agent.HistoryQuery{Limit: 10})
	if err != nil {
		t.Fatalf("list post-reset history: %v", err)
	}
	if len(page.Entries) != 0 {
		t.Fatalf("pre-reset late claim resurfaced: %#v", page.Entries)
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
	capabilities, _ := agent.NewCapabilitySet("message.react")
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
	if err := store.History().Append(context.Background(), key, agent.HistoryEntry{
		MessageID: claim.MessageID, InvocationID: invocation.ID, Causation: invocation.Causation,
		Role: agent.HistoryUser, Sender: invocation.Sender, Content: invocation.Input,
		Delivery: agent.DeliveryNotStarted, CreatedAt: invocation.RequestedAt,
	}); err != nil {
		t.Fatalf("append current history target: %v", err)
	}
	if _, err := turns.Claim(context.Background(), agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: clock.now}); !agent.IsCode(err, agent.ErrorInProgress) {
		t.Fatalf("active lease error = %v, want in_progress", err)
	}
	plan, err := turns.CommitPlan(context.Background(), agent.CommitPlanRequest{
		Key: key, InvocationID: invocation.ID, CurrentMessageID: claim.MessageID, Lease: claim.Lease, ConfigVersion: configSnapshot.Version, ResponseText: "hello back", ReplyToMessageID: claim.MessageID,
		Capabilities: capabilities,
		Effects: []agent.ModelEffect{{CallID: "call_react_1", Intent: agent.EffectIntent{
			Kind: agent.EffectReact, TargetMessageID: claim.MessageID, Emoji: "✅",
		}}},
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
	if replay.Plan == nil || replay.Plan.ActionID != plan.ActionID || replay.Plan.ResponseID != plan.ResponseID || replay.Plan.ReplyToMessageID != claim.MessageID {
		t.Fatalf("replay plan = %#v, original = %#v", replay.Plan, plan)
	}
	if len(plan.Effects) != 1 || len(replay.Plan.Effects) != 1 || replay.Plan.Effects[0] != plan.Effects[0] {
		t.Fatalf("atomic model effect refs = %#v / %#v", plan.Effects, replay.Plan.Effects)
	}
	blockedEffect, err := store.Effects().Claim(context.Background(), effect.Ref{Key: key, EffectID: plan.Effects[0].EffectID}, clock.now)
	if err != nil || blockedEffect.State != effect.StatePending {
		t.Fatalf("model effect ran before response delivery = %#v, %v", blockedEffect, err)
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
	if storedAction.State != action.StateClaimed || storedAction.Text != "hello back" || storedAction.ReplyToMessageID != claim.MessageID {
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
	releasedEffect, err := store.Effects().Claim(context.Background(), effect.Ref{Key: key, EffectID: plan.Effects[0].EffectID}, clock.now)
	if err != nil || releasedEffect.State != effect.StateClaimed {
		t.Fatalf("model effect was not released after response delivery = %#v, %v", releasedEffect, err)
	}
	historyPage, err := store.History().ListIfConfigVersion(
		context.Background(), key, configSnapshot.Version, agent.HistoryQuery{Limit: 10},
	)
	var completedAssistant *agent.HistoryEntry
	for index := range historyPage.Entries {
		if historyPage.Entries[index].Role == agent.HistoryAssistant {
			completedAssistant = &historyPage.Entries[index]
		}
	}
	if err != nil || completedAssistant == nil || completedAssistant.Delivery != agent.DeliverySucceeded {
		t.Fatalf("completed assistant history = %#v, err=%v", historyPage, err)
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

func TestPlanReplayUsesTheExactAssistantHistoryTimestamp(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{now: time.Date(2026, 9, 8, 1, 30, 0, 999_500_000, time.UTC)}
	store := openTestStoreWithClock(t, clock)
	candidate := testCandidate(t, "history-timestamp-replay", "15550000048@s.whatsapp.net")
	candidate.ReceivedAt = clock.now
	candidate.OccurredAt = clock.now.Add(-time.Second)
	claimed, err := store.Inbound().ClaimAndResolveSender(ctx, candidate)
	if err != nil {
		t.Fatalf("claim inbound: %v", err)
	}
	key := agent.Key{TenantID: claimed.Message.TenantID, AccountID: claimed.Message.AccountID, ChatID: claimed.Message.ChatID}
	snapshot, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t))
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
	digest, err := agent.DigestInvocation(key, invocation)
	if err != nil {
		t.Fatalf("digest invocation: %v", err)
	}
	claim, err := store.Turns().Claim(ctx, agent.ClaimTurnRequest{Key: key, Invocation: invocation, Digest: digest, Now: clock.now})
	if err != nil {
		t.Fatalf("claim turn: %v", err)
	}
	clock.step = time.Millisecond
	plan, err := store.Turns().CommitPlan(ctx, agent.CommitPlanRequest{
		Key: key, InvocationID: invocation.ID, Lease: claim.Lease,
		ConfigVersion: snapshot.Version, ResponseText: "same timestamp",
	})
	if err != nil {
		t.Fatalf("commit plan: %v", err)
	}
	replayed, err := store.Turns().Load(ctx, key, invocation.ID)
	if err != nil || replayed.Plan == nil || !replayed.Plan.CreatedAt.Equal(plan.CreatedAt) {
		t.Fatalf("replayed plan = %#v, original = %#v, err=%v", replayed.Plan, plan, err)
	}
	if err := store.History().Append(ctx, key, agent.HistoryEntry{
		MessageID: plan.ResponseID, InvocationID: invocation.ID, Causation: invocation.Causation,
		Role: agent.HistoryAssistant, Content: []agent.ContentPart{agent.TextPart{Text: plan.Text}},
		Delivery: agent.DeliveryPending, CreatedAt: replayed.Plan.CreatedAt,
	}); err != nil {
		t.Fatalf("idempotent assistant history replay: %v", err)
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
	history, err := store.History().ListIfConfigVersion(
		context.Background(), key, snapshot.Version, agent.HistoryQuery{Limit: 10},
	)
	var unknownAssistant *agent.HistoryEntry
	for index := range history.Entries {
		if history.Entries[index].Role == agent.HistoryAssistant {
			unknownAssistant = &history.Entries[index]
		}
	}
	if err != nil || unknownAssistant == nil || unknownAssistant.Delivery != agent.DeliveryUnknownOutcome {
		t.Fatalf("unknown assistant history = %#v, err=%v", history, err)
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
		Triggers:   agent.DefaultTriggerConfig(),
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
	lid, _ := identity.ParseLID("10000000009@lid")
	now := time.Now().UTC()
	return conversation.IncomingCandidate{
		TenantID:            tenantID,
		AccountID:           accountID,
		ProviderMessageID:   providerMessageID,
		ProviderChatAddress: chat,
		SenderLID:           lid,
		ProviderSenderPhone: "15550000009@s.whatsapp.net",
		SenderName:          "Tester",
		ChatKind:            conversation.ChatDirect,
		Text:                "hello",
		Owner:               true,
		Allowlisted:         true,
		OccurredAt:          now.Add(-time.Second),
		ReceivedAt:          now,
	}
}

type testClock struct {
	now  time.Time
	step time.Duration
}

func (clock *testClock) Now() time.Time {
	value := clock.now
	clock.now = clock.now.Add(clock.step)
	return value
}

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
