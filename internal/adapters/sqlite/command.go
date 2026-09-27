package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func (store *ActionStore) FindCommandResponse(
	ctx context.Context,
	message conversation.IncomingMessage,
) (agent.DispatchRef, bool, error) {
	var actionValue sql.NullString
	err := store.read.QueryRowContext(ctx, `SELECT a.action_id FROM inbound_events e
      LEFT JOIN outbound_actions a ON a.tenant_id = e.tenant_id AND a.account_id = e.account_id
        AND a.chat_id = e.chat_id AND a.invocation_id = e.invocation_id
      WHERE e.tenant_id = ? AND e.account_id = ? AND e.chat_id = ? AND e.invocation_id = ?`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
	).Scan(&actionValue)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.DispatchRef{}, false, agent.NewError(agent.ErrorNotFound, "find command response", errors.New("incoming message does not exist"))
	}
	if err != nil {
		return agent.DispatchRef{}, false, storageError("find command response", err)
	}
	if !actionValue.Valid {
		return agent.DispatchRef{}, false, nil
	}
	actionID, err := identity.ParseActionID(actionValue.String)
	if err != nil {
		return agent.DispatchRef{}, false, agent.NewError(agent.ErrorIntegrityFailure, "decode command response", err)
	}
	return agent.DispatchRef{
		Key:      agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID},
		ActionID: actionID,
	}, true, nil
}

func (store *ActionStore) PlanCommandResponse(
	ctx context.Context,
	message conversation.IncomingMessage,
	version agent.ConfigVersion,
	text string,
) (agent.DispatchRef, error) {
	if message.InvocationID.IsZero() || version == 0 || strings.TrimSpace(text) == "" {
		return agent.DispatchRef{}, agent.NewError(agent.ErrorInvalidArgument, "plan command response", errors.New("message, config version, and text are required"))
	}
	key := agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID}
	if err := key.Validate(); err != nil {
		return agent.DispatchRef{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.DispatchRef{}, storageError("begin command response", err)
	}
	defer tx.Rollback()
	var existingAction, existingText sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT a.action_id, a.text FROM inbound_events e
      LEFT JOIN outbound_actions a ON a.tenant_id = e.tenant_id AND a.account_id = e.account_id
        AND a.chat_id = e.chat_id AND a.invocation_id = e.invocation_id
      WHERE e.tenant_id = ? AND e.account_id = ? AND e.chat_id = ? AND e.invocation_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), message.InvocationID.String(),
	).Scan(&existingAction, &existingText)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.DispatchRef{}, agent.NewError(agent.ErrorNotFound, "plan command response", errors.New("incoming message does not exist"))
	}
	if err != nil {
		return agent.DispatchRef{}, storageError("load command response", err)
	}
	if existingAction.Valid {
		if !existingText.Valid || existingText.String != text {
			return agent.DispatchRef{}, agent.NewError(agent.ErrorConflict, "plan command response", errors.New("different response already planned"))
		}
		actionID, err := identity.ParseActionID(existingAction.String)
		if err != nil {
			return agent.DispatchRef{}, agent.NewError(agent.ErrorIntegrityFailure, "decode command response", err)
		}
		if err := tx.Commit(); err != nil {
			return agent.DispatchRef{}, storageError("commit command response replay", err)
		}
		return agent.DispatchRef{Key: key, ActionID: actionID}, nil
	}
	responseID, err := identity.NewMessageID()
	if err != nil {
		return agent.DispatchRef{}, agent.NewError(agent.ErrorInternal, "create command response ID", err)
	}
	actionID, err := identity.NewActionID()
	if err != nil {
		return agent.DispatchRef{}, agent.NewError(agent.ErrorInternal, "create command action ID", err)
	}
	nowMS := store.clock.Now().UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO outbound_actions(
        tenant_id, account_id, chat_id, action_id, invocation_id, response_id,
        text, state, created_at_ms, updated_at_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), actionID.String(),
		message.InvocationID.String(), responseID.String(), text,
		uint8(action.StatePending), nowMS, nowMS,
	)
	if err != nil {
		return agent.DispatchRef{}, storageError("insert command action", err)
	}
	if err := store.Store.appendHistoryEntryTx(ctx, tx, key, agent.HistoryEntry{
		MessageID: responseID, InvocationID: message.InvocationID,
		Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: message.CausationID},
		Role:      agent.HistoryAssistant, Content: []agent.ContentPart{agent.TextPart{Text: text}},
		Delivery: agent.DeliveryPending, CreatedAt: time.UnixMilli(nowMS).UTC(),
	}, nowMS); err != nil {
		return agent.DispatchRef{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
        turn_claimed = 1, config_version = ?, turn_state = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
		uint64(version), uint8(agent.TurnResponsePlanned),
		nowMS, key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), message.InvocationID.String(),
	)
	if err := requireOne(result, err, "publish command response"); err != nil {
		return agent.DispatchRef{}, err
	}
	if err := tx.Commit(); err != nil {
		return agent.DispatchRef{}, storageError("commit command response", err)
	}
	return agent.DispatchRef{Key: key, ActionID: actionID}, nil
}
