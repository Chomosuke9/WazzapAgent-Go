package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
)

func TestControlSettingsRepositoryPersistsTypedValues(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.db")
	store, err := OpenSettings(ctx, path)
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	repository, err := NewControlSettingsRepository(store)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := control.NewController(repository)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := controller.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Revision != 1 || initial.Values.DataDir == "" {
		t.Fatalf("initial view = %#v", initial)
	}
	draft := config.DefaultSettings()
	draft.WhatsAppEnabled = false
	draft.AgentEnabled = false
	draft.AssistantName = "persisted"
	result, err := controller.Save(ctx, initial.Revision, control.SettingsPatch{Draft: draft})
	if err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if result.Revision != 2 || result.View.Values.AssistantName != "persisted" {
		t.Fatalf("saved view = %#v", result)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close settings before reopen: %v", err)
	}
	reopened, err := OpenSettings(ctx, path)
	if err != nil {
		t.Fatalf("reopen settings: %v", err)
	}
	defer reopened.Close()
	reopenedRepository, err := NewControlSettingsRepository(reopened)
	if err != nil {
		t.Fatal(err)
	}
	reopenedController, err := control.NewController(reopenedRepository)
	if err != nil {
		t.Fatal(err)
	}
	view, err := reopenedController.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != 2 || view.Values.AssistantName != "persisted" {
		t.Fatalf("reopened view = %#v", view)
	}
}
