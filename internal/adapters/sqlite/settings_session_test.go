package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestSessionBindingTransitionsPersistWithoutChangingSettingsRevision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.db")
	store, err := OpenSettings(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewSessionBindingRepository(store)
	if err != nil {
		t.Fatal(err)
	}
	settingsBefore, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := repository.LoadSessionBinding(ctx)
	if err != nil || initial.State != control.SessionUnpaired {
		t.Fatalf("initial binding = %+v, error = %v", initial, err)
	}
	tenantID, err := identity.NewTenantID()
	if err != nil {
		t.Fatal(err)
	}
	accountID, err := identity.NewAccountID()
	if err != nil {
		t.Fatal(err)
	}
	scope := control.SessionScope{TenantID: tenantID, AccountID: accountID}
	if err := repository.BeginSessionPairing(ctx, scope); err != nil {
		t.Fatal(err)
	}
	pending, err := repository.LoadSessionBinding(ctx)
	if err != nil || !pending.HasPendingScope || pending.PendingScope != scope {
		t.Fatalf("pending binding = %+v, error = %v", pending, err)
	}
	if err := repository.MarkSessionPaired(ctx, scope, "123456789@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	paired, err := repository.LoadSessionBinding(ctx)
	if err != nil || paired.State != control.SessionPaired || paired.ActiveScope != scope || paired.HasPendingScope {
		t.Fatalf("paired binding = %+v, error = %v", paired, err)
	}
	if err := repository.MarkSessionRevoked(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSettings(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedRepository, err := NewSessionBindingRepository(reopened)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := reopenedRepository.LoadSessionBinding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settingsAfter, err := reopened.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.State != control.SessionRevoked || revoked.ActiveScope != scope || revoked.WhatsAppAccountID != "" {
		t.Fatalf("reopened revoked binding = %+v", revoked)
	}
	if settingsBefore.Revision != settingsAfter.Revision {
		t.Fatalf("session transitions changed settings revision: before=%d after=%d", settingsBefore.Revision, settingsAfter.Revision)
	}
}

func TestSessionBindingRejectsScopeChangesAndClearsCancelledPairing(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSettings(ctx, filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository, err := NewSessionBindingRepository(store)
	if err != nil {
		t.Fatal(err)
	}
	tenantID, _ := identity.NewTenantID()
	accountID, _ := identity.NewAccountID()
	otherAccountID, _ := identity.NewAccountID()
	scope := control.SessionScope{TenantID: tenantID, AccountID: accountID}
	otherScope := control.SessionScope{TenantID: tenantID, AccountID: otherAccountID}
	if err := repository.BeginSessionPairing(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkSessionPaired(ctx, otherScope, "123456789@s.whatsapp.net"); err == nil {
		t.Fatal("paired a scope other than the reserved one")
	}
	if err := repository.AbortSessionPairing(ctx, scope); err != nil {
		t.Fatal(err)
	}
	binding, err := repository.LoadSessionBinding(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if binding.HasPendingScope || binding.State != control.SessionUnpaired {
		t.Fatalf("cancelled binding = %+v", binding)
	}
}
