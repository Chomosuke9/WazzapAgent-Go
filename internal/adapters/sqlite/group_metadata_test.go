package sqlite

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestGroupMetadataPersistsEventsAndInvalidation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "app.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	const group = "120363000000000001@g.us"
	cache := store.Inbound()
	if _, _, err := cache.LoadGroupMetadata(ctx, tenantID, accountID, group); !agent.IsCode(err, agent.ErrorNotReady) {
		t.Fatalf("uninitialized cache = %v, want not ready", err)
	}
	if err := cache.ReplaceGroupMetadata(ctx, tenantID, accountID, map[string][]byte{group: []byte(`{"name":"First"}`)}, 10); err != nil {
		t.Fatal(err)
	}
	if err := cache.UpsertGroupMetadata(ctx, tenantID, accountID, group, []byte(`{"name":"Renamed"}`), 11); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cache = store.Inbound()
	payload, observedAt, err := cache.LoadGroupMetadata(ctx, tenantID, accountID, group)
	if err != nil || !bytes.Equal(payload, []byte(`{"name":"Renamed"}`)) || observedAt != 11 {
		t.Fatalf("reopened group = %s at %d, err = %v", payload, observedAt, err)
	}
	if err := cache.InvalidateGroupMetadata(ctx, tenantID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.LoadGroupMetadata(ctx, tenantID, accountID, group); !agent.IsCode(err, agent.ErrorNotReady) {
		t.Fatalf("invalidated cache = %v, want not ready", err)
	}
	if err := cache.ReplaceGroupMetadata(ctx, tenantID, accountID, map[string][]byte{}, 12); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.LoadGroupMetadata(ctx, tenantID, accountID, group); !agent.IsCode(err, agent.ErrorNotFound) {
		t.Fatalf("removed group = %v, want not found", err)
	}
}
