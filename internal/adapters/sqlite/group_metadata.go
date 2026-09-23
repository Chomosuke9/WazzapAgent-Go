package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func validateGroupMetadataScope(tenantID identity.TenantID, accountID identity.AccountID) error {
	if tenantID.IsZero() || accountID.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "group metadata scope", errors.New("tenant and account are required"))
	}
	return nil
}

func validateGroupMetadataAddress(address string) error {
	if !strings.HasSuffix(address, "@g.us") || len(address) > 512 {
		return agent.NewError(agent.ErrorInvalidArgument, "group metadata address", errors.New("group address is invalid"))
	}
	return nil
}

func (store *InboundStore) InvalidateGroupMetadata(ctx context.Context, tenantID identity.TenantID, accountID identity.AccountID) error {
	if err := validateGroupMetadataScope(tenantID, accountID); err != nil {
		return err
	}
	_, err := store.db.ExecContext(ctx, `INSERT INTO group_metadata_state (tenant_id, account_id, ready) VALUES (?, ?, 0)
		ON CONFLICT (tenant_id, account_id) DO UPDATE SET ready = 0`, tenantID.String(), accountID.String())
	if err != nil {
		return storageError("invalidate group metadata", err)
	}
	return nil
}

func (store *InboundStore) ReplaceGroupMetadata(ctx context.Context, tenantID identity.TenantID, accountID identity.AccountID, groups map[string][]byte, observedAt int64) error {
	if err := validateGroupMetadataScope(tenantID, accountID); err != nil {
		return err
	}
	if observedAt <= 0 {
		return agent.NewError(agent.ErrorInvalidArgument, "replace group metadata", errors.New("observation time is required"))
	}
	for address, payload := range groups {
		if err := validateGroupMetadataAddress(address); err != nil {
			return err
		}
		if len(payload) == 0 {
			return agent.NewError(agent.ErrorInvalidArgument, "replace group metadata", errors.New("group payload is empty"))
		}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin group metadata replacement", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_metadata WHERE tenant_id = ? AND account_id = ?`, tenantID.String(), accountID.String()); err != nil {
		return storageError("clear group metadata", err)
	}
	for address, payload := range groups {
		if _, err := tx.ExecContext(ctx, `INSERT INTO group_metadata (tenant_id, account_id, group_address, payload, observed_at_ms) VALUES (?, ?, ?, ?, ?)`,
			tenantID.String(), accountID.String(), address, payload, observedAt); err != nil {
			return storageError("write group metadata", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO group_metadata_state (tenant_id, account_id, ready) VALUES (?, ?, 1)
		ON CONFLICT (tenant_id, account_id) DO UPDATE SET ready = 1`, tenantID.String(), accountID.String()); err != nil {
		return storageError("mark group metadata ready", err)
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit group metadata replacement", err)
	}
	return nil
}

func (store *InboundStore) LoadGroupMetadata(ctx context.Context, tenantID identity.TenantID, accountID identity.AccountID, address string) ([]byte, int64, error) {
	if err := validateGroupMetadataScope(tenantID, accountID); err != nil {
		return nil, 0, err
	}
	if err := validateGroupMetadataAddress(address); err != nil {
		return nil, 0, err
	}
	var payload []byte
	var observedAt sql.NullInt64
	err := store.db.QueryRowContext(ctx, `SELECT m.payload, m.observed_at_ms FROM group_metadata_state AS s
		LEFT JOIN group_metadata AS m ON m.tenant_id = s.tenant_id AND m.account_id = s.account_id AND m.group_address = ?
		WHERE s.tenant_id = ? AND s.account_id = ? AND s.ready = 1`,
		address, tenantID.String(), accountID.String()).Scan(&payload, &observedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, agent.NewError(agent.ErrorNotReady, "load group metadata", errors.New("group metadata is synchronizing"))
	}
	if err != nil {
		return nil, 0, storageError("load group metadata", err)
	}
	if !observedAt.Valid {
		return nil, 0, agent.NewError(agent.ErrorNotFound, "load group metadata", errors.New("group is not in the joined-group snapshot"))
	}
	return payload, observedAt.Int64, nil
}

func (store *InboundStore) UpsertGroupMetadata(ctx context.Context, tenantID identity.TenantID, accountID identity.AccountID, address string, payload []byte, observedAt int64) error {
	if err := validateGroupMetadataScope(tenantID, accountID); err != nil {
		return err
	}
	if err := validateGroupMetadataAddress(address); err != nil {
		return err
	}
	if len(payload) == 0 || observedAt <= 0 {
		return agent.NewError(agent.ErrorInvalidArgument, "upsert group metadata", errors.New("payload and observation time are required"))
	}
	_, err := store.db.ExecContext(ctx, `INSERT INTO group_metadata (tenant_id, account_id, group_address, payload, observed_at_ms)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (tenant_id, account_id, group_address)
		DO UPDATE SET payload = excluded.payload, observed_at_ms = excluded.observed_at_ms`,
		tenantID.String(), accountID.String(), address, payload, observedAt)
	if err != nil {
		return storageError("upsert group metadata", err)
	}
	return nil
}

func (store *InboundStore) DeleteGroupMetadata(ctx context.Context, tenantID identity.TenantID, accountID identity.AccountID, address string) error {
	if err := validateGroupMetadataScope(tenantID, accountID); err != nil {
		return err
	}
	if err := validateGroupMetadataAddress(address); err != nil {
		return err
	}
	_, err := store.db.ExecContext(ctx, `DELETE FROM group_metadata WHERE tenant_id = ? AND account_id = ? AND group_address = ?`,
		tenantID.String(), accountID.String(), address)
	if err != nil {
		return storageError("delete group metadata", err)
	}
	return nil
}
