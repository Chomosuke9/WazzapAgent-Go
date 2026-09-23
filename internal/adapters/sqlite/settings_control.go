package sqlite

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
)

// ControlSettingsRepository adapts the bounded JSON storage API to the
// typed control port. Keeping this bridge in the adapter prevents the
// controller from depending on SQLite or encoding details.
type ControlSettingsRepository struct {
	store *SettingsStore
}

func NewControlSettingsRepository(store *SettingsStore) (*ControlSettingsRepository, error) {
	if store == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create settings repository", errors.New("settings store is required"))
	}
	return &ControlSettingsRepository{store: store}, nil
}

var _ control.SettingsRepository = (*ControlSettingsRepository)(nil)

func (repository *ControlSettingsRepository) Load(ctx context.Context) (control.SettingsSnapshot, error) {
	if repository == nil || repository.store == nil {
		return control.SettingsSnapshot{}, agent.NewError(agent.ErrorStorageFailure, "load settings", errors.New("settings store is unavailable"))
	}
	snapshot, err := repository.store.Load(ctx)
	if err != nil {
		return control.SettingsSnapshot{}, err
	}
	values := config.DefaultSettings()
	if err := json.Unmarshal(snapshot.Values, &values); err != nil {
		return control.SettingsSnapshot{}, agent.NewError(agent.ErrorIntegrityFailure, "decode settings", errors.New("settings data is invalid"))
	}
	return control.SettingsSnapshot{Revision: snapshot.Revision, Values: values, UpdatedAt: snapshot.UpdatedAt}, nil
}

func (repository *ControlSettingsRepository) Save(ctx context.Context, expectedRevision uint64, values config.Settings) (control.SettingsSnapshot, error) {
	if repository == nil || repository.store == nil {
		return control.SettingsSnapshot{}, agent.NewError(agent.ErrorStorageFailure, "save settings", errors.New("settings store is unavailable"))
	}
	snapshot, err := repository.store.Save(ctx, expectedRevision, values)
	if err != nil {
		return control.SettingsSnapshot{}, err
	}
	return control.SettingsSnapshot{Revision: snapshot.Revision, Values: values, UpdatedAt: snapshot.UpdatedAt}, nil
}
