package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
)

// commandConfigMutationKind marks a journal row written through
// command.Context.UpdateConfig. Earlier builds used per-command kinds.
const commandConfigMutationKind = 200

// BeginConfigMutation journals that the command message is about to write
// config version expected+1, or returns the journal an earlier attempt left.
func (store *InboundStore) BeginConfigMutation(
	ctx context.Context,
	message conversation.IncomingMessage,
	expected agent.ConfigVersion,
) (command.ConfigMutation, error) {
	if expected == 0 {
		return command.ConfigMutation{}, agent.NewError(agent.ErrorInvalidArgument, "begin config mutation", errors.New("config version is required"))
	}
	digest := sha256.Sum256([]byte("wazzapagent.command-config.v1\x00" + message.InvocationID.String() + "\x00" + message.Text))
	return store.beginConfigMutation(ctx, message, commandConfigMutationKind, digest, expected)
}

func (store *InboundStore) beginConfigMutation(
	ctx context.Context,
	message conversation.IncomingMessage,
	kind uint8,
	digest [32]byte,
	expected agent.ConfigVersion,
) (command.ConfigMutation, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return command.ConfigMutation{}, storageError("begin config mutation journal", err)
	}
	defer tx.Rollback()
	var (
		storedKind     sql.NullInt64
		storedDigest   nullableBytes
		storedExpected sql.NullInt64
		storedApplied  sql.NullInt64
	)
	err = tx.QueryRowContext(ctx, `SELECT command_kind, command_digest,
        command_expected_version, command_applied_version
      FROM inbound_events WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
	).Scan(&storedKind, &storedDigest, &storedExpected, &storedApplied)
	if errors.Is(err, sql.ErrNoRows) {
		return command.ConfigMutation{}, agent.NewError(agent.ErrorNotFound, "begin config mutation", errors.New("incoming command does not exist"))
	}
	if err != nil {
		return command.ConfigMutation{}, storageError("load config mutation journal", err)
	}
	if storedKind.Valid || storedDigest.Valid || storedExpected.Valid {
		if !storedKind.Valid || !storedDigest.Valid || !storedExpected.Valid ||
			uint8(storedKind.Int64) != kind || !bytes.Equal(storedDigest.Bytes, digest[:]) {
			return command.ConfigMutation{}, agent.NewError(agent.ErrorConflict, "begin config mutation", errors.New("incoming command is bound to a different mutation"))
		}
		mutation := command.ConfigMutation{ExpectedVersion: agent.ConfigVersion(storedExpected.Int64)}
		if storedApplied.Valid {
			mutation.AppliedVersion = agent.ConfigVersion(storedApplied.Int64)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE inbound_events SET updated_at_ms = ?
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
			store.clock.Now().UnixMilli(), message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
		); err != nil {
			return command.ConfigMutation{}, storageError("renew prompt mutation journal", err)
		}
		if err := tx.Commit(); err != nil {
			return command.ConfigMutation{}, storageError("commit prompt mutation lookup", err)
		}
		return mutation, nil
	}
	var pendingInvocation string
	err = tx.QueryRowContext(ctx, `SELECT invocation_id FROM inbound_events
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
        AND invocation_id <> ? AND command_kind IS NOT NULL
        AND command_applied_version IS NULL
      ORDER BY updated_at_ms, invocation_id LIMIT 1`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
	).Scan(&pendingInvocation)
	if err == nil {
		return command.ConfigMutation{}, agent.NewError(agent.ErrorConflict, "begin config mutation", errors.New("an earlier config mutation must be recovered first"))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return command.ConfigMutation{}, storageError("find pending prompt mutation", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
        command_kind = ?, command_digest = ?, command_expected_version = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
        AND command_kind IS NULL AND action_id IS NULL AND turn_state = 0`,
		kind, digest[:], uint64(expected), store.clock.Now().UnixMilli(),
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
	)
	if err := requireOne(result, err, "persist prompt mutation journal"); err != nil {
		return command.ConfigMutation{}, err
	}
	if err := tx.Commit(); err != nil {
		return command.ConfigMutation{}, storageError("commit prompt mutation journal", err)
	}
	return command.ConfigMutation{ExpectedVersion: expected}, nil
}

func (store *InboundStore) MarkConfigMutationApplied(
	ctx context.Context,
	message conversation.IncomingMessage,
	expected agent.ConfigVersion,
	applied agent.ConfigVersion,
) error {
	return store.markConfigMutationApplied(ctx, message, expected, applied)
}

func (store *InboundStore) markConfigMutationApplied(
	ctx context.Context,
	message conversation.IncomingMessage,
	expected agent.ConfigVersion,
	applied agent.ConfigVersion,
) error {
	if expected == 0 || applied != expected+1 || applied == 0 {
		return agent.NewError(agent.ErrorInvalidArgument, "complete prompt mutation", errors.New("applied version must follow expected version"))
	}
	result, err := store.db.ExecContext(ctx, `UPDATE inbound_events SET
        command_applied_version = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
        AND command_expected_version = ?
        AND (command_applied_version IS NULL OR command_applied_version = ?)`,
		uint64(applied), store.clock.Now().UnixMilli(), message.TenantID.String(), message.AccountID.String(),
		message.ChatID.String(), message.InvocationID.String(), uint64(expected), uint64(applied),
	)
	if err := requireOne(result, err, "complete prompt mutation"); err != nil {
		return err
	}
	return nil
}
