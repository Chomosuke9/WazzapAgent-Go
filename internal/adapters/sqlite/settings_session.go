package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/control"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

// SessionBindingRepository is the typed adapter for the singleton session row
// in settings.db. It never reads or writes the settings revision.
type SessionBindingRepository struct{ store *SettingsStore }

func NewSessionBindingRepository(store *SettingsStore) (*SessionBindingRepository, error) {
	if store == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create session binding repository", errors.New("settings store is required"))
	}
	return &SessionBindingRepository{store: store}, nil
}

var _ control.SessionBindingRepository = (*SessionBindingRepository)(nil)

func (repository *SessionBindingRepository) LoadSessionBinding(ctx context.Context) (control.SessionBinding, error) {
	if repository == nil || repository.store == nil || repository.store.db == nil {
		return control.SessionBinding{}, agent.NewError(agent.ErrorStorageFailure, "load session binding", errors.New("session store is unavailable"))
	}
	store := repository.store
	store.mu.RLock()
	defer store.mu.RUnlock()
	return loadSessionBinding(ctx, store.db)
}

func (repository *SessionBindingRepository) BeginSessionLink(ctx context.Context, scope control.SessionScope) error {
	if err := validateSessionScope(scope); err != nil {
		return err
	}
	tx, unlock, err := repository.begin(ctx, "begin session link")
	if err != nil {
		return err
	}
	defer unlock()
	defer tx.Rollback()
	binding, err := loadSessionBinding(ctx, tx)
	if err != nil {
		return err
	}
	if binding.State == control.SessionLinked {
		return agent.NewError(agent.ErrorConflict, "begin session link", errors.New("a Discord bot is already linked"))
	}
	if binding.HasPendingScope {
		return agent.NewError(agent.ErrorConflict, "begin session link", errors.New("another session scope change is pending"))
	}
	_, err = tx.ExecContext(ctx, `UPDATE session_state SET pending_tenant_id = ?, pending_account_id = ?, updated_at_ms = ? WHERE id = 1`, scope.TenantID.String(), scope.AccountID.String(), time.Now().UTC().UnixMilli())
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "write pending session scope", err)
	}
	return commitSessionTx(tx, "begin session link")
}

func (repository *SessionBindingRepository) MarkSessionLinked(ctx context.Context, scope control.SessionScope, discordBotID string) error {
	if err := validateSessionScope(scope); err != nil {
		return err
	}
	discordBotID = strings.TrimSpace(discordBotID)
	if discordBotID == "" || len(discordBotID) > 256 {
		return agent.NewError(agent.ErrorInvalidArgument, "mark session linked", errors.New("Discord bot identity is invalid"))
	}
	tx, unlock, err := repository.begin(ctx, "mark session linked")
	if err != nil {
		return err
	}
	defer unlock()
	defer tx.Rollback()
	binding, err := loadSessionBinding(ctx, tx)
	if err != nil {
		return err
	}
	if binding.HasPendingScope {
		if binding.PendingScope != scope {
			return agent.NewError(agent.ErrorConflict, "mark session linked", errors.New("pending session scope changed"))
		}
	} else if !binding.HasActiveScope || binding.ActiveScope != scope {
		return agent.NewError(agent.ErrorConflict, "mark session linked", errors.New("session scope is not active or pending"))
	}
	_, err = tx.ExecContext(ctx, `UPDATE session_state
		SET active_tenant_id = ?, active_account_id = ?, discord_bot_id = ?, state = 'linked',
		    pending_tenant_id = NULL, pending_account_id = NULL, updated_at_ms = ?
		WHERE id = 1`, scope.TenantID.String(), scope.AccountID.String(), discordBotID, time.Now().UTC().UnixMilli())
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "write linked session", err)
	}
	return commitSessionTx(tx, "mark session linked")
}

func (repository *SessionBindingRepository) AbortSessionLink(ctx context.Context, scope control.SessionScope) error {
	if err := validateSessionScope(scope); err != nil {
		return err
	}
	tx, unlock, err := repository.begin(ctx, "abort session link")
	if err != nil {
		return err
	}
	defer unlock()
	defer tx.Rollback()
	binding, err := loadSessionBinding(ctx, tx)
	if err != nil {
		return err
	}
	if !binding.HasPendingScope {
		return nil
	}
	if binding.PendingScope != scope {
		return agent.NewError(agent.ErrorConflict, "abort session link", errors.New("pending session scope changed"))
	}
	_, err = tx.ExecContext(ctx, `UPDATE session_state SET pending_tenant_id = NULL, pending_account_id = NULL, updated_at_ms = ? WHERE id = 1`, time.Now().UTC().UnixMilli())
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "clear pending session scope", err)
	}
	return commitSessionTx(tx, "abort session link")
}

func (repository *SessionBindingRepository) MarkSessionRevoked(ctx context.Context, scope control.SessionScope) error {
	if err := validateSessionScope(scope); err != nil {
		return err
	}
	tx, unlock, err := repository.begin(ctx, "mark session revoked")
	if err != nil {
		return err
	}
	defer unlock()
	defer tx.Rollback()
	binding, err := loadSessionBinding(ctx, tx)
	if err != nil {
		return err
	}
	if !binding.HasActiveScope || binding.ActiveScope != scope {
		return agent.NewError(agent.ErrorConflict, "mark session revoked", errors.New("session scope is not active"))
	}
	if binding.State == control.SessionRevoked {
		return nil
	}
	if binding.State != control.SessionLinked {
		return agent.NewError(agent.ErrorConflict, "mark session revoked", errors.New("no Discord bot is linked"))
	}
	_, err = tx.ExecContext(ctx, `UPDATE session_state SET state = 'revoked', updated_at_ms = ? WHERE id = 1`, time.Now().UTC().UnixMilli())
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "write revoked session", err)
	}
	return commitSessionTx(tx, "mark session revoked")
}

func (repository *SessionBindingRepository) begin(ctx context.Context, operation string) (*sql.Tx, func(), error) {
	if repository == nil || repository.store == nil || repository.store.db == nil {
		return nil, func() {}, agent.NewError(agent.ErrorStorageFailure, operation, errors.New("session store is unavailable"))
	}
	store := repository.store
	store.mu.Lock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		store.mu.Unlock()
		return nil, func() {}, agent.NewError(agent.ErrorStorageFailure, operation, err)
	}
	return tx, store.mu.Unlock, nil
}

func validateSessionScope(scope control.SessionScope) error {
	if scope.TenantID.IsZero() || scope.AccountID.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "validate session scope", errors.New("tenant and account identity are required"))
	}
	return nil
}

type sessionBindingQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadSessionBinding(ctx context.Context, query sessionBindingQuerier) (control.SessionBinding, error) {
	var activeTenant, activeAccount, discordID, pendingTenant, pendingAccount sql.NullString
	var state string
	var updatedMS int64
	if err := query.QueryRowContext(ctx, `SELECT active_tenant_id, active_account_id, discord_bot_id, state,
		pending_tenant_id, pending_account_id, updated_at_ms FROM session_state WHERE id = 1`).Scan(
		&activeTenant, &activeAccount, &discordID, &state, &pendingTenant, &pendingAccount, &updatedMS,
	); err != nil {
		return control.SessionBinding{}, agent.NewError(agent.ErrorStorageFailure, "read session binding", err)
	}
	binding := control.SessionBinding{State: control.SessionBindingState(state), UpdatedAt: time.UnixMilli(updatedMS).UTC()}
	switch binding.State {
	case control.SessionUnlinked, control.SessionLinked, control.SessionRevoked:
	default:
		return control.SessionBinding{}, agent.NewError(agent.ErrorIntegrityFailure, "decode session binding", errors.New("session state is invalid"))
	}
	var err error
	if activeTenant.Valid {
		binding.ActiveScope.TenantID, err = identity.ParseTenantID(activeTenant.String)
		if err == nil {
			binding.ActiveScope.AccountID, err = identity.ParseAccountID(activeAccount.String)
		}
		if err != nil {
			return control.SessionBinding{}, agent.NewError(agent.ErrorIntegrityFailure, "decode active session scope", fmt.Errorf("active session identity is invalid: %w", err))
		}
		binding.HasActiveScope = true
	}
	if pendingTenant.Valid {
		binding.PendingScope.TenantID, err = identity.ParseTenantID(pendingTenant.String)
		if err == nil {
			binding.PendingScope.AccountID, err = identity.ParseAccountID(pendingAccount.String)
		}
		if err != nil {
			return control.SessionBinding{}, agent.NewError(agent.ErrorIntegrityFailure, "decode pending session scope", fmt.Errorf("pending session identity is invalid: %w", err))
		}
		binding.HasPendingScope = true
	}
	binding.DiscordBotID = discordID.String
	if binding.State == control.SessionLinked && (!binding.HasActiveScope || binding.DiscordBotID == "") {
		return control.SessionBinding{}, agent.NewError(agent.ErrorIntegrityFailure, "decode session binding", errors.New("linked session is incomplete"))
	}
	return binding, nil
}

func commitSessionTx(tx *sql.Tx, operation string) error {
	if err := tx.Commit(); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, operation, err)
	}
	return nil
}
