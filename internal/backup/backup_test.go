package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	appsqlite "github.com/Chomosuke9/WazzapAgent-Go/internal/adapters/sqlite"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/backup"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestBackupVerifyAndRestorePreserveDurableHistory(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	source := filepath.Join(base, "data")
	backupParent := filepath.Join(base, "backups")
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	chatID, _ := identity.NewChatID()
	key := agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
	tenantDir := filepath.Join(source, "tenants", tenantID.String())
	if err := os.MkdirAll(tenantDir, 0o700); err != nil {
		t.Fatalf("create source: %v", err)
	}
	store, err := appsqlite.Open(ctx, filepath.Join(tenantDir, "app.db"))
	if err != nil {
		t.Fatalf("open source store: %v", err)
	}
	providerID, _ := identity.ParseProviderID("openai-compatible")
	policyID, _ := identity.ParsePolicyID("part2-chat-gate.v1")
	snapshot, err := store.Configs().LoadOrCreate(ctx, key, agent.ConfigValues{
		Model:  agent.ModelConfig{ProviderID: providerID, Model: "model", MaxOutputTokens: 128},
		Prompt: "prompt", Permission: agent.PermissionConfig{PolicyID: policyID, Revision: 1},
	})
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	messageID, _ := identity.NewMessageID()
	invocationID, _ := identity.NewInvocationID()
	causationID, _ := identity.NewCausationID()
	participantID, _ := identity.NewParticipantID()
	senderRef, _ := identity.NewSenderRef()
	entry := agent.HistoryEntry{
		MessageID: messageID, InvocationID: invocationID,
		Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: causationID},
		Role:      agent.HistoryUser,
		Sender:    &agent.SenderContext{ParticipantID: participantID, Ref: senderRef, DisplayName: "Tester"},
		Content:   []agent.ContentPart{agent.TextPart{Text: "survives restore"}},
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	if err := store.History().Append(ctx, key, entry); err != nil {
		t.Fatalf("append source history: %v", err)
	}
	if err := store.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint source: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "runtime-identity.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write identity fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tenantDir, "whatsapp.db"), []byte("device-session-fixture"), 0o600); err != nil {
		t.Fatalf("write device fixture: %v", err)
	}
	backupPath, err := backup.Create(ctx, source, backupParent, time.Unix(1_700_000_100, 0).UTC())
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	manifest, err := backup.Verify(ctx, backupPath)
	if err != nil || len(manifest.Files) < 3 {
		t.Fatalf("verify backup = %#v, err=%v", manifest, err)
	}
	restored := filepath.Join(base, "restored")
	if err := backup.Restore(ctx, backupPath, restored); err != nil {
		t.Fatalf("restore backup: %v", err)
	}
	restoredStore, err := appsqlite.Open(ctx, filepath.Join(restored, "tenants", tenantID.String(), "app.db"))
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	defer restoredStore.Close()
	page, err := restoredStore.History().ListIfConfigVersion(ctx, key, snapshot.Version, agent.HistoryQuery{Limit: 10})
	if err != nil || len(page.Entries) != 1 ||
		page.Entries[0].Content[0].(agent.TextPart).Text != "survives restore" {
		t.Fatalf("restored history = %#v, err=%v", page, err)
	}
	if err := os.WriteFile(filepath.Join(backupPath, "runtime-identity.json"), []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper backup: %v", err)
	}
	if _, err := backup.Verify(ctx, backupPath); err == nil {
		t.Fatal("tampered backup passed verification")
	}
}

func TestBackupRejectsDestinationSymlinkedInsideSource(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	source := filepath.Join(base, "data")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "runtime-identity.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	alias := filepath.Join(base, "source-alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := backup.Create(ctx, source, alias, time.Now().UTC()); err == nil {
		t.Fatal("backup accepted a destination symlinked inside its source")
	}
}

func TestBackupRejectsEmptySourceAndReservedManifestPath(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	empty := filepath.Join(base, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatalf("create empty source: %v", err)
	}
	if _, err := backup.Create(ctx, empty, filepath.Join(base, "backups-empty"), time.Now().UTC()); err == nil {
		t.Fatal("backup accepted an empty source")
	}
	reserved := filepath.Join(base, "reserved")
	if err := os.Mkdir(reserved, 0o700); err != nil {
		t.Fatalf("create reserved source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reserved, "backup-manifest.json"), []byte("source data"), 0o600); err != nil {
		t.Fatalf("write reserved path: %v", err)
	}
	if _, err := backup.Create(ctx, reserved, filepath.Join(base, "backups-reserved"), time.Now().UTC()); err == nil {
		t.Fatal("backup accepted a source containing its reserved manifest path")
	}
}

func TestNestedBackupAndRestoreDestinationsAreRejectedWithoutMutation(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	source := filepath.Join(base, "data")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "runtime-identity.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	nestedBackupParent := filepath.Join(source, "must-not-exist")
	if _, err := backup.Create(ctx, source, nestedBackupParent, time.Now().UTC()); err == nil {
		t.Fatal("backup accepted a destination nested in its source")
	}
	if _, err := os.Stat(nestedBackupParent); !os.IsNotExist(err) {
		t.Fatalf("rejected backup created its destination: %v", err)
	}
	backupPath, err := backup.Create(ctx, source, filepath.Join(base, "backups"), time.Now().UTC())
	if err != nil {
		t.Fatalf("create valid backup: %v", err)
	}
	nestedRestore := filepath.Join(backupPath, "must-not-exist", "restored")
	if err := backup.Restore(ctx, backupPath, nestedRestore); err == nil {
		t.Fatal("restore accepted a destination nested in its backup")
	}
	if _, err := os.Stat(filepath.Dir(nestedRestore)); !os.IsNotExist(err) {
		t.Fatalf("rejected restore mutated its backup: %v", err)
	}
	if _, err := backup.Verify(ctx, backupPath); err != nil {
		t.Fatalf("rejected restore invalidated its source backup: %v", err)
	}
}
