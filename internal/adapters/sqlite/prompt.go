package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
)

func (store *InboundStore) BeginPromptMutation(
	ctx context.Context,
	message conversation.IncomingMessage,
	command inbound.PromptCommand,
	expected agent.ConfigVersion,
) (inbound.PromptMutation, error) {
	if expected == 0 || (command.Kind != inbound.PromptSet && command.Kind != inbound.PromptClear) {
		return inbound.PromptMutation{}, agent.NewError(agent.ErrorInvalidArgument, "begin prompt mutation", fmt.Errorf("mutation command and config version are required"))
	}
	digest := promptCommandDigest(message, command)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return inbound.PromptMutation{}, storageError("begin prompt mutation journal", err)
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
		return inbound.PromptMutation{}, agent.NewError(agent.ErrorNotFound, "begin prompt mutation", fmt.Errorf("incoming command does not exist"))
	}
	if err != nil {
		return inbound.PromptMutation{}, storageError("load prompt mutation journal", err)
	}
	if storedKind.Valid || storedDigest.Valid || storedExpected.Valid {
		if !storedKind.Valid || !storedDigest.Valid || !storedExpected.Valid ||
			inbound.PromptCommandKind(storedKind.Int64) != command.Kind || !bytes.Equal(storedDigest.Bytes, digest[:]) {
			return inbound.PromptMutation{}, agent.NewError(agent.ErrorConflict, "begin prompt mutation", fmt.Errorf("incoming command is bound to a different mutation"))
		}
		mutation := inbound.PromptMutation{ExpectedVersion: agent.ConfigVersion(storedExpected.Int64)}
		if storedApplied.Valid {
			mutation.AppliedVersion = agent.ConfigVersion(storedApplied.Int64)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE inbound_events SET updated_at_ms = ?
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
			store.clock.Now().UnixMilli(), message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
		); err != nil {
			return inbound.PromptMutation{}, storageError("renew prompt mutation journal", err)
		}
		if err := tx.Commit(); err != nil {
			return inbound.PromptMutation{}, storageError("commit prompt mutation lookup", err)
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
		return inbound.PromptMutation{}, agent.NewError(agent.ErrorConflict, "begin prompt mutation", fmt.Errorf("an earlier prompt mutation must be recovered first"))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return inbound.PromptMutation{}, storageError("find pending prompt mutation", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
        command_kind = ?, command_digest = ?, command_expected_version = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
        AND command_kind IS NULL AND action_id IS NULL AND turn_state = 0`,
		uint8(command.Kind), digest[:], uint64(expected), store.clock.Now().UnixMilli(),
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
	)
	if err := requireOne(result, err, "persist prompt mutation journal"); err != nil {
		return inbound.PromptMutation{}, err
	}
	if err := tx.Commit(); err != nil {
		return inbound.PromptMutation{}, storageError("commit prompt mutation journal", err)
	}
	return inbound.PromptMutation{ExpectedVersion: expected}, nil
}

func (store *InboundStore) MarkPromptMutationApplied(
	ctx context.Context,
	message conversation.IncomingMessage,
	expected agent.ConfigVersion,
	applied agent.ConfigVersion,
) error {
	if expected == 0 || applied != expected+1 || applied == 0 {
		return agent.NewError(agent.ErrorInvalidArgument, "complete prompt mutation", fmt.Errorf("applied version must follow expected version"))
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

func promptCommandDigest(message conversation.IncomingMessage, command inbound.PromptCommand) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("wazzapagent.prompt.v1\x00%d\x00%s\x00%s", command.Kind, message.InvocationID.String(), command.Text)))
}
