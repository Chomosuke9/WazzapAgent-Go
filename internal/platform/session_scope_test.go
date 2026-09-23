package platform

import (
	"context"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestSessionScopeResolverPersistsInitialIdentityAndHonorsSessionScope(t *testing.T) {
	dataRoot := t.TempDir()
	settings := config.DefaultSettings()
	resolver := SessionScopeResolver{}
	first, err := resolver.ResolveSessionSnapshot(context.Background(), dataRoot, settings, control.SessionScope{})
	if err != nil {
		t.Fatal(err)
	}
	firstScope := control.SessionScope{TenantID: first.TenantID(), AccountID: first.AccountID()}
	if firstScope.TenantID.IsZero() || firstScope.AccountID.IsZero() {
		t.Fatalf("first session scope = %+v", firstScope)
	}
	reopened, err := resolver.ResolveSessionSnapshot(context.Background(), dataRoot, settings, control.SessionScope{})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.TenantID() != firstScope.TenantID || reopened.AccountID() != firstScope.AccountID {
		t.Fatalf("reopened identity changed: first=%+v reopened=(%s,%s)", firstScope, reopened.TenantID(), reopened.AccountID())
	}
	newTenantID, err := identity.NewTenantID()
	if err != nil {
		t.Fatal(err)
	}
	newAccountID, err := identity.NewAccountID()
	if err != nil {
		t.Fatal(err)
	}
	newScope := control.SessionScope{TenantID: newTenantID, AccountID: newAccountID}
	rotated, err := resolver.ResolveSessionSnapshot(context.Background(), dataRoot, settings, newScope)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.TenantID() != newTenantID || rotated.AccountID() != newAccountID {
		t.Fatalf("explicit session scope was ignored: tenant=%s account=%s", rotated.TenantID(), rotated.AccountID())
	}
}
