package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestReconcileAccountDefaultsPreservesChatOverridesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	key := testKey(t)
	initial := testDefaults(t)
	initialSnapshot, err := store.Configs().LoadOrCreate(ctx, key, initial)
	if err != nil {
		t.Fatalf("create chat config: %v", err)
	}

	values := initialSnapshot.Values()
	override := agent.PromptOverride{Mode: agent.PromptAppend, Text: "keep this chat instruction"}
	values.PromptOverride = &override
	values.Permission.ModerationLevel = agent.ModerationDeleteMute
	values.Triggers = agent.TriggerConfig{Mention: false, Name: true, Reply: true}
	custom, err := store.Configs().CompareAndSwap(ctx, key, initialSnapshot.Version, values)
	if err != nil {
		t.Fatalf("set chat-specific config: %v", err)
	}

	providerID, err := identity.ParseProviderID("second-provider")
	if err != nil {
		t.Fatal(err)
	}
	policyID, err := identity.ParsePolicyID("second-policy")
	if err != nil {
		t.Fatal(err)
	}
	defaults := agent.ConfigValues{
		Model:      agent.ModelConfig{ProviderID: providerID, Model: "new-model", MaxOutputTokens: 1024},
		Prompt:     "new global prompt",
		Permission: agent.PermissionConfig{PolicyID: policyID, Revision: 3},
		Triggers:   agent.DefaultTriggerConfig(),
	}
	changed, err := store.Configs().ReconcileAccountDefaults(ctx, key.TenantID, key.AccountID, defaults)
	if err != nil || changed != 1 {
		t.Fatalf("reconcile defaults changed=%d err=%v, want one row changed", changed, err)
	}
	updated, err := store.Configs().Load(ctx, key)
	if err != nil {
		t.Fatalf("load reconciled config: %v", err)
	}
	if updated.Version != custom.Version+1 || updated.Model != defaults.Model || updated.Prompt != defaults.Prompt || updated.Permission.PolicyID != policyID || updated.Permission.Revision != 3 {
		t.Fatalf("global projection was not applied: %#v", updated)
	}
	if updated.PromptOverride == nil || *updated.PromptOverride != override {
		t.Fatalf("chat prompt override changed: %#v", updated.PromptOverride)
	}
	if updated.Permission.ModerationLevel != agent.ModerationDeleteMute || updated.Triggers != values.Triggers {
		t.Fatalf("chat-specific moderation or triggers changed: permission=%v triggers=%#v", updated.Permission.ModerationLevel, updated.Triggers)
	}

	changed, err = store.Configs().ReconcileAccountDefaults(ctx, key.TenantID, key.AccountID, defaults)
	if err != nil || changed != 0 {
		t.Fatalf("repeat reconciliation changed=%d err=%v, want no rows changed", changed, err)
	}
	repeated, err := store.Configs().Load(ctx, key)
	if err != nil || repeated.Version != updated.Version {
		t.Fatalf("repeat reconciliation changed version=%d err=%v, want %d", repeated.Version, err, updated.Version)
	}
}

func TestChangedChatDefaultsOnlyInitializeNewChats(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	key := testKey(t)
	old := testDefaults(t)
	first, err := store.Configs().LoadOrCreate(ctx, key, old)
	if err != nil {
		t.Fatal(err)
	}
	newDefaults := old
	newDefaults.Permission.ModerationLevel = agent.ModerationDeleteMute
	newDefaults.Triggers = agent.TriggerConfig{Name: true}
	newDefaults.PromptOverride = &agent.PromptOverride{Mode: agent.PromptReplace, Text: "new default"}
	global := newDefaults
	global.Permission.ModerationLevel = agent.ModerationNone
	global.PromptOverride = nil
	if _, err := store.Configs().ReconcileAccountDefaults(ctx, key.TenantID, key.AccountID, global); err != nil {
		t.Fatal(err)
	}
	kept, err := store.Configs().LoadOrCreate(ctx, key, newDefaults)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Version != first.Version || kept.Triggers != old.Triggers || kept.PromptOverride != nil || kept.Permission.ModerationLevel != old.Permission.ModerationLevel {
		t.Fatalf("existing chat changed: %#v", kept)
	}
	other := testKey(t)
	other.TenantID, other.AccountID = key.TenantID, key.AccountID
	created, err := store.Configs().LoadOrCreate(ctx, other, newDefaults)
	if err != nil {
		t.Fatal(err)
	}
	if created.Triggers != newDefaults.Triggers || created.Permission.ModerationLevel != newDefaults.Permission.ModerationLevel || created.PromptOverride == nil || *created.PromptOverride != *newDefaults.PromptOverride {
		t.Fatalf("new chat missed defaults: %#v", created)
	}
}

func TestChatTriggerSettingsPersistAcrossStoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trigger-settings.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	key := testKey(t)
	initial, err := store.Configs().LoadOrCreate(ctx, key, testDefaults(t))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	values := initial.Values()
	values.Triggers = agent.TriggerConfig{Mention: false, Name: true, Reply: false, NameRegex: true, NamePattern: `(?i)\bvivy\b`}
	updated, err := store.Configs().CompareAndSwap(ctx, key, initial.Version, values)
	if err != nil {
		t.Fatalf("save triggers: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	loaded, err := reopened.Configs().Load(ctx, key)
	if err != nil || loaded.Version != updated.Version || loaded.Triggers != values.Triggers {
		t.Fatalf("persisted triggers = %#v version=%d, err=%v", loaded.Triggers, loaded.Version, err)
	}
}
