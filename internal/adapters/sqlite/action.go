package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/action"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
)

func (store *ActionStore) Load(ctx context.Context, ref agent.DispatchRef) (action.StoredAction, error) {
	if err := ref.Key.Validate(); err != nil {
		return action.StoredAction{}, err
	}
	if ref.ActionID.IsZero() {
		return action.StoredAction{}, agent.NewError(agent.ErrorInvalidArgument, "load outbound action", errors.New("action ID is required"))
	}
	stored, err := loadAction(ctx, store.read, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return action.StoredAction{}, agent.NewError(agent.ErrorNotFound, "load outbound action", errors.New("action does not exist"))
	}
	if err != nil {
		return action.StoredAction{}, storageError("load outbound action", err)
	}
	return stored, nil
}

// Start claims a pending reply for sending. The state condition is the whole
// claim: a second caller finds no pending row and gets a conflict.
func (store *ActionStore) Start(ctx context.Context, ref agent.DispatchRef, now time.Time) error {
	if now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "start outbound action", errors.New("current time is required"))
	}
	nowMS := now.UnixMilli()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin outbound action start", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE outbound_actions SET
        state = ?, attempts = attempts + 1, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ? AND state = ?`,
		uint8(action.StateExecuting), nowMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(),
		uint8(action.StatePending),
	)
	if err := requireOne(result, err, "start outbound action"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE inbound_events SET turn_state = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND turn_state = ?
        AND invocation_id = (SELECT invocation_id FROM outbound_actions
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?)`,
		uint8(agent.TurnDeliveryPending), nowMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), uint8(agent.TurnResponsePlanned),
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(),
	); err != nil {
		return storageError("mark turn delivery pending", err)
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit outbound action start", err)
	}
	return nil
}

// ListPending returns replies that are planned but not yet sent, oldest first.
// A non-zero plannedBefore skips replies planned at or after it.
func (store *ActionStore) ListPending(ctx context.Context, tenantID identity.TenantID, plannedBefore time.Time) ([]agent.DispatchRef, error) {
	if tenantID.IsZero() {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "list pending actions", errors.New("tenant is required"))
	}
	rows, err := store.read.QueryContext(ctx, `SELECT account_id, chat_id, action_id FROM outbound_actions
      WHERE tenant_id = ? AND state = ? AND created_at_ms < ? ORDER BY created_at_ms, action_id`,
		tenantID.String(), uint8(action.StatePending), cutoffMillis(plannedBefore),
	)
	if err != nil {
		return nil, storageError("list pending actions", err)
	}
	defer rows.Close()
	refs := make([]agent.DispatchRef, 0)
	for rows.Next() {
		var accountValue, chatValue, actionValue string
		if err := rows.Scan(&accountValue, &chatValue, &actionValue); err != nil {
			return nil, storageError("scan pending action", err)
		}
		accountID, err := identity.ParseAccountID(accountValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode pending action", err)
		}
		chatID, err := identity.ParseChatID(chatValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode pending action", err)
		}
		actionID, err := identity.ParseActionID(actionValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode pending action", err)
		}
		refs = append(refs, agent.DispatchRef{
			Key:      agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID},
			ActionID: actionID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("iterate pending actions", err)
	}
	return refs, nil
}

func (store *ActionStore) Complete(ctx context.Context, ref agent.DispatchRef, providerReceipt string, now time.Time) error {
	if providerReceipt == "" || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "complete outbound action", errors.New("receipt and current time are required"))
	}
	return store.finish(ctx, ref, action.StateExecuting, action.StateSucceeded, providerReceipt, "", now)
}

// FailTerminal records a reply that will never be sent. Nothing was sent, so
// it applies to a pending row only.
func (store *ActionStore) FailTerminal(ctx context.Context, ref agent.DispatchRef, code agent.ErrorCode, now time.Time) error {
	if code == "" || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "fail outbound action", errors.New("code and time are required"))
	}
	return store.finish(ctx, ref, action.StatePending, action.StateFailedTerminal, "", code, now)
}

// MarkUnknown records a send whose outcome cannot be known. It is never
// retried: Discord may already have delivered it.
func (store *ActionStore) MarkUnknown(ctx context.Context, ref agent.DispatchRef, code agent.ErrorCode, now time.Time) error {
	if code == "" || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "mark action unknown", errors.New("code and time are required"))
	}
	return store.finish(ctx, ref, action.StateExecuting, action.StateUnknownOutcome, "", code, now)
}

func (store *ActionStore) finish(
	ctx context.Context,
	ref agent.DispatchRef,
	from, to action.State,
	providerReceipt string,
	code agent.ErrorCode,
	now time.Time,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin action outcome", err)
	}
	defer tx.Rollback()
	if err := finishActionTx(ctx, tx, ref, from, to, providerReceipt, code, now.UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit action outcome", err)
	}
	return nil
}

func finishActionTx(
	ctx context.Context,
	tx *sql.Tx,
	ref agent.DispatchRef,
	from, to action.State,
	providerReceipt string,
	code agent.ErrorCode,
	nowMS int64,
) error {
	result, err := tx.ExecContext(ctx, `UPDATE outbound_actions SET
        state = ?, provider_receipt = COALESCE(?, provider_receipt), last_error_code = ?,
        completed_at_ms = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ? AND state = ?`,
		uint8(to), nullableString(providerReceipt), nullableErrorCode(code), nowMS, nowMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(), uint8(from),
	)
	if err := requireOne(result, err, "record outbound action outcome"); err != nil {
		return err
	}
	delivery, turnState := agent.DeliverySucceeded, agent.TurnSucceeded
	switch to {
	case action.StateFailedTerminal:
		delivery, turnState = agent.DeliveryFailedTerminal, agent.TurnFailedTerminal
	case action.StateUnknownOutcome:
		delivery, turnState = agent.DeliveryUnknownOutcome, agent.TurnUnknownOutcome
	}
	return updateTurnDelivery(ctx, tx, ref, delivery, turnState, code, nowMS)
}

// resolveInterruptedActions runs at startup, before anything can send: a
// reply still executing was cut off mid-send by the last shutdown or crash.
func resolveInterruptedActions(ctx context.Context, tx *sql.Tx, tenantID identity.TenantID, nowMS int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT account_id, chat_id, action_id FROM outbound_actions
      WHERE tenant_id = ? AND state = ?`, tenantID.String(), uint8(action.StateExecuting))
	if err != nil {
		return storageError("list interrupted actions", err)
	}
	refs := make([]agent.DispatchRef, 0)
	for rows.Next() {
		var accountValue, chatValue, actionValue string
		if err := rows.Scan(&accountValue, &chatValue, &actionValue); err != nil {
			rows.Close()
			return storageError("scan interrupted action", err)
		}
		accountID, accountErr := identity.ParseAccountID(accountValue)
		chatID, chatErr := identity.ParseChatID(chatValue)
		actionID, actionErr := identity.ParseActionID(actionValue)
		if err := errors.Join(accountErr, chatErr, actionErr); err != nil {
			rows.Close()
			return agent.NewError(agent.ErrorIntegrityFailure, "decode interrupted action", err)
		}
		refs = append(refs, agent.DispatchRef{Key: agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}, ActionID: actionID})
	}
	if err := rows.Close(); err != nil {
		return storageError("close interrupted actions", err)
	}
	for _, ref := range refs {
		if err := finishActionTx(ctx, tx, ref, action.StateExecuting, action.StateUnknownOutcome, "", agent.ErrorUnknownOutcome, nowMS); err != nil {
			return err
		}
	}
	return nil
}

type actionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadAction(ctx context.Context, query actionQuerier, ref agent.DispatchRef) (action.StoredAction, error) {
	var (
		invocationValue string
		responseValue   string
		replyToValue    sql.NullString
		text            string
		state           int64
		completedAt     sql.NullInt64
		providerReceipt sql.NullString
	)
	err := query.QueryRowContext(ctx, `SELECT invocation_id, response_id, text, state, completed_at_ms, provider_receipt, reply_to_message_id
      FROM outbound_actions WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?`,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(),
	).Scan(&invocationValue, &responseValue, &text, &state, &completedAt, &providerReceipt, &replyToValue)
	if err != nil {
		return action.StoredAction{}, err
	}
	invocationID, err := identity.ParseInvocationID(invocationValue)
	if err != nil {
		return action.StoredAction{}, agent.NewError(agent.ErrorIntegrityFailure, "decode outbound action", err)
	}
	responseID, err := identity.ParseMessageID(responseValue)
	if err != nil {
		return action.StoredAction{}, agent.NewError(agent.ErrorIntegrityFailure, "decode outbound action", err)
	}
	var replyTo identity.MessageID
	if replyToValue.Valid {
		replyTo, err = identity.ParseMessageID(replyToValue.String)
		if err != nil {
			return action.StoredAction{}, agent.NewError(agent.ErrorIntegrityFailure, "decode outbound reply target", err)
		}
	}
	stored := action.StoredAction{
		Ref:              ref,
		InvocationID:     invocationID,
		ResponseID:       responseID,
		ReplyToMessageID: replyTo,
		Text:             text,
		State:            action.State(state),
		ProviderReceipt:  providerReceipt.String,
	}
	if completedAt.Valid {
		value := time.UnixMilli(completedAt.Int64).UTC()
		stored.CompletedAt = &value
	}
	return stored, nil
}

// updateTurnDelivery moves the turn and the assistant history row to match a
// reply's delivery outcome. The outbound_actions row is the source of truth;
// these are the two views of it that callers read.
func updateTurnDelivery(
	ctx context.Context,
	tx *sql.Tx,
	ref agent.DispatchRef,
	delivery agent.DeliveryStatus,
	turnState agent.TurnState,
	code agent.ErrorCode,
	updatedAtMS int64,
) error {
	turnResult, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
        turn_state = ?, last_error_code = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
        AND invocation_id = (SELECT invocation_id FROM outbound_actions
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?)`,
		uint8(turnState), nullableErrorCode(code), updatedAtMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(),
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(),
	)
	if err := requireOne(turnResult, err, "update turn delivery"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE history_entries SET
        delivery_status = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
        AND message_id = (SELECT response_id FROM outbound_actions
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?)`,
		uint8(delivery), updatedAtMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(),
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(),
	); err != nil {
		return storageError("update assistant history delivery", err)
	}
	return nil
}

func requireOne(result sql.Result, err error, operation string) error {
	if err != nil {
		return storageError(operation, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storageError(operation, err)
	}
	if changed != 1 {
		return agent.NewError(agent.ErrorConflict, operation, errors.New("row is not in the expected state"))
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableErrorCode(value agent.ErrorCode) any {
	if value == "" {
		return nil
	}
	return string(value)
}

// cutoffMillis turns a plannedBefore bound into a created_at_ms bound; the
// zero time means no bound.
func cutoffMillis(before time.Time) int64 {
	if before.IsZero() {
		return math.MaxInt64
	}
	return before.UnixMilli()
}
