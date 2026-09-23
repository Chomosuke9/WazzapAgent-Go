package platform

import (
	"context"
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
)

// SessionScopeResolver connects the durable session scope in settings.db to
// the immutable config snapshot used by the WhatsApp device store.
type SessionScopeResolver struct{}

func (SessionScopeResolver) ResolveSessionSnapshot(_ context.Context, dataRoot string, settings config.Settings, preferred control.SessionScope) (config.Snapshot, error) {
	settings.DataDir = dataRoot
	if preferred.TenantID.IsZero() && preferred.AccountID.IsZero() {
		snapshot, err := config.SessionSnapshotFromSettings(settings)
		if err != nil {
			return config.Snapshot{}, err
		}
		return snapshot.ResolveRuntimeIdentity()
	}
	if preferred.TenantID.IsZero() || preferred.AccountID.IsZero() {
		return config.Snapshot{}, errors.New("session scope is incomplete")
	}
	return config.SessionSnapshotWithIdentity(settings, preferred.TenantID, preferred.AccountID)
}
