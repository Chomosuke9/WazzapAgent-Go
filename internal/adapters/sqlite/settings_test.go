package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

func TestSettingsSaveReopenAndCAS(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.db")
	store, err := OpenSettings(ctx, path)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	initial, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("load initial settings: %v", err)
	}
	if initial.Revision != settingsRevision || string(initial.Values) != "{}" {
		t.Fatalf("initial snapshot = %#v, want revision %d and empty object", initial, settingsRevision)
	}
	saved, err := store.Save(ctx, initial.Revision, struct {
		AssistantName string `json:"assistantName"`
		APIKey        string `json:"apiKey"`
	}{"bot", "secret-value"})
	if err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if saved.Revision != initial.Revision+1 || string(saved.Values) != `{"assistantName":"bot","apiKey":"secret-value"}` {
		t.Fatalf("saved snapshot = %#v", saved)
	}
	if _, err := store.Save(ctx, initial.Revision, map[string]string{"assistantName": "stale"}); !agent.IsCode(err, agent.ErrorConflict) || !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale save error = %v, want conflict", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close settings: %v", err)
	}
	reopened, err := OpenSettings(ctx, path)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	defer reopened.Close()
	var values struct {
		AssistantName string `json:"assistantName"`
		APIKey        string `json:"apiKey"`
	}
	snapshot, err := reopened.LoadInto(ctx, &values)
	if err != nil {
		t.Fatalf("load typed settings: %v", err)
	}
	if snapshot.Revision != saved.Revision || values.AssistantName != "bot" || values.APIKey != "secret-value" {
		t.Fatalf("reopened values = %#v revision=%d", values, snapshot.Revision)
	}
}

func TestSettingsMigrationLedgerAndPayloadValidation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.db")
	store, err := OpenSettings(ctx, path)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	initial, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if _, err := store.Save(ctx, initial.Revision, nil); err == nil {
		t.Fatal("nil settings unexpectedly accepted")
	}
	if _, err := store.Save(ctx, initial.Revision, []string{"wrong-shape"}); err == nil {
		t.Fatal("array settings unexpectedly accepted")
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE schema_migrations SET checksum = 'tampered' WHERE version = 1"); err != nil {
		t.Fatalf("tamper migration ledger: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close settings: %v", err)
	}
	if _, err := OpenSettings(ctx, path); !agent.IsCode(err, agent.ErrorIntegrityFailure) {
		t.Fatalf("reopen tampered settings error = %v, want integrity failure", err)
	}
}

func TestSettingsMigrationIsSeparateFromConversationSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.db")
	store, err := OpenSettings(ctx, path)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count settings migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("settings migration count = %d, want 1", count)
	}
	if _, err := store.db.ExecContext(ctx, "SELECT 1 FROM application_settings WHERE id = 1"); err != nil {
		t.Fatalf("settings row missing: %v", err)
	}
	var missing int
	err = store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'chats'").Scan(&missing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("inspect conversation schema: %v", err)
	}
	if missing != 0 {
		t.Fatal("conversation tables unexpectedly created in settings database")
	}
}
