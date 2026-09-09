package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const actionPendingRecoveryGrace = 2 * time.Second

func (store *ActionStore) Claim(ctx context.Context, ref agent.DispatchRef, now time.Time) (action.StoredAction, error) {
	if err := ref.Key.Validate(); err != nil {
		return action.StoredAction{}, err
	}
	if ref.ActionID.IsZero() || now.IsZero() {
		return action.StoredAction{}, agent.NewError(agent.ErrorInvalidArgument, "claim outbound action", fmt.Errorf("action ID and current time are required"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return action.StoredAction{}, storageError("begin outbound action claim", err)
	}
	defer tx.Rollback()
	stored, leaseUntil, retryAfter, err := loadAction(ctx, tx, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return action.StoredAction{}, agent.NewError(agent.ErrorNotFound, "claim outbound action", fmt.Errorf("action does not exist"))
	}
	if err != nil {
		return action.StoredAction{}, storageError("load outbound action", err)
	}
	nowMS := now.UnixMilli()
	switch stored.State {
	case action.StateSucceeded, action.StateFailedTerminal, action.StateUnknownOutcome:
		if err := tx.Commit(); err != nil {
			return action.StoredAction{}, storageError("commit action observation", err)
		}
		return stored, nil
	case action.StateExecuting:
		if leaseUntil.Valid && leaseUntil.Int64 > nowMS {
			if err := tx.Commit(); err != nil {
				return action.StoredAction{}, storageError("commit executing action observation", err)
			}
			return stored, nil
		}
		if err := markUnknownTx(ctx, tx, stored, agent.ErrorUnknownOutcome, nowMS); err != nil {
			return action.StoredAction{}, err
		}
		stored.State = action.StateUnknownOutcome
		completedAt := now.UTC()
		stored.CompletedAt = &completedAt
		if err := tx.Commit(); err != nil {
			return action.StoredAction{}, storageError("commit expired executing action", err)
		}
		return stored, nil
	case action.StateClaimed:
		if leaseUntil.Valid && leaseUntil.Int64 > nowMS {
			// The existing private lease is not returned to another dispatcher.
			stored.State = action.StatePending
			stored.Lease = ""
			if err := tx.Commit(); err != nil {
				return action.StoredAction{}, storageError("commit claimed action observation", err)
			}
			return stored, nil
		}
	case action.StateFailedRetryable:
		if retryAfter.Valid && retryAfter.Int64 > nowMS {
			if err := tx.Commit(); err != nil {
				return action.StoredAction{}, storageError("commit deferred action observation", err)
			}
			return stored, nil
		}
	case action.StatePending:
		// Claim below.
	default:
		return action.StoredAction{}, agent.NewError(agent.ErrorIntegrityFailure, "claim outbound action", fmt.Errorf("invalid action state"))
	}
	lease, err := randomLease("act")
	if err != nil {
		return action.StoredAction{}, agent.NewError(agent.ErrorInternal, "create action lease", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE outbound_actions SET
        state = ?, action_lease = ?, action_lease_until_ms = ?, retry_after_ms = NULL,
        attempts = attempts + 1, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?
        AND state = ?`,
		uint8(action.StateClaimed), lease, now.Add(store.actionTTL).UnixMilli(), nowMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(), uint8(stored.State),
	)
	if err != nil {
		return action.StoredAction{}, storageError("claim outbound action", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return action.StoredAction{}, agent.NewError(agent.ErrorConflict, "claim outbound action", fmt.Errorf("action changed concurrently"))
	}
	turnResult, err := tx.ExecContext(ctx, `UPDATE inbound_events SET turn_state = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?
        AND turn_state IN (?, ?)`,
		uint8(agent.TurnDeliveryPending), nowMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(),
		uint8(agent.TurnResponsePlanned), uint8(agent.TurnDeliveryPending),
	)
	if err := requireOne(turnResult, err, "mark turn delivery pending"); err != nil {
		return action.StoredAction{}, err
	}
	if err := tx.Commit(); err != nil {
		return action.StoredAction{}, storageError("commit outbound action claim", err)
	}
	stored.State = action.StateClaimed
	stored.Lease = action.Lease(lease)
	return stored, nil
}

func (store *ActionStore) ListRecoverable(
	ctx context.Context,
	tenantID identity.TenantID,
	now time.Time,
	limit uint32,
) ([]agent.DispatchRef, error) {
	if tenantID.IsZero() || now.IsZero() || limit == 0 || limit > 10_000 {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "list recoverable actions", fmt.Errorf("valid tenant, time, and limit are required"))
	}
	rows, err := store.db.QueryContext(ctx, `SELECT account_id, chat_id, action_id
      FROM outbound_actions
      WHERE tenant_id = ? AND (
		(state = ? AND updated_at_ms <= ?) OR
        (state = ? AND (retry_after_ms IS NULL OR retry_after_ms <= ?)) OR
        (state = ? AND (action_lease_until_ms IS NULL OR action_lease_until_ms <= ?)) OR
        (state = ? AND (action_lease_until_ms IS NULL OR action_lease_until_ms <= ?))
      )
      ORDER BY updated_at_ms, action_id
      LIMIT ?`,
		tenantID.String(), uint8(action.StatePending), now.Add(-actionPendingRecoveryGrace).UnixMilli(),
		uint8(action.StateFailedRetryable), now.UnixMilli(),
		uint8(action.StateClaimed), now.UnixMilli(),
		uint8(action.StateExecuting), now.UnixMilli(), limit,
	)
	if err != nil {
		return nil, storageError("list recoverable actions", err)
	}
	defer rows.Close()
	refs := make([]agent.DispatchRef, 0)
	for rows.Next() {
		var accountValue, chatValue, actionValue string
		if err := rows.Scan(&accountValue, &chatValue, &actionValue); err != nil {
			return nil, storageError("scan recoverable action", err)
		}
		accountID, err := identity.ParseAccountID(accountValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable action", err)
		}
		chatID, err := identity.ParseChatID(chatValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable action", err)
		}
		actionID, err := identity.ParseActionID(actionValue)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode recoverable action", err)
		}
		refs = append(refs, agent.DispatchRef{
			Key:      agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID},
			ActionID: actionID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("iterate recoverable actions", err)
	}
	return refs, nil
}

func (store *ActionStore) MarkExecuting(ctx context.Context, ref agent.DispatchRef, lease action.Lease, now time.Time) error {
	if lease == "" || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "start outbound action", fmt.Errorf("lease and current time are required"))
	}
	result, err := store.db.ExecContext(ctx, `UPDATE outbound_actions SET
        state = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?
        AND state = ? AND action_lease = ? AND action_lease_until_ms > ?`,
		uint8(action.StateExecuting), now.UnixMilli(), ref.Key.TenantID.String(), ref.Key.AccountID.String(),
		ref.Key.ChatID.String(), ref.ActionID.String(), uint8(action.StateClaimed), string(lease), now.UnixMilli(),
	)
	return requireOne(result, err, "start outbound action")
}

func (store *ActionStore) Complete(
	ctx context.Context,
	ref agent.DispatchRef,
	lease action.Lease,
	providerReceipt string,
	now time.Time,
) error {
	if lease == "" || providerReceipt == "" || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "complete outbound action", fmt.Errorf("lease and current time are required"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin action completion", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE outbound_actions SET
        state = ?, action_lease = NULL, action_lease_until_ms = NULL,
        provider_receipt = ?, last_error_code = NULL, completed_at_ms = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?
        AND state = ? AND action_lease = ?`,
		uint8(action.StateSucceeded), nullableString(providerReceipt), now.UnixMilli(), now.UnixMilli(),
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(),
		uint8(action.StateExecuting), string(lease),
	)
	if err := requireOne(result, err, "complete outbound action"); err != nil {
		return err
	}
	if err := updateReceiptAndTurn(ctx, tx, ref, agent.DeliverySucceeded, agent.TurnSucceeded, providerReceipt, "", now.UnixMilli(), now.UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit action completion", err)
	}
	return nil
}

func (store *ActionStore) Release(
	ctx context.Context,
	ref agent.DispatchRef,
	lease action.Lease,
	code agent.ErrorCode,
	retryable bool,
	retryAt time.Time,
) error {
	if lease == "" || code == "" || retryAt.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "release outbound action", fmt.Errorf("lease, code, and time are required"))
	}
	state := action.StateFailedTerminal
	delivery := agent.DeliveryFailedTerminal
	turnState := agent.TurnFailedTerminal
	if retryable {
		state = action.StateFailedRetryable
		delivery = agent.DeliveryPending
		turnState = agent.TurnDeliveryPending
	}
	nowMS := store.clock.Now().UnixMilli()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin action release", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE outbound_actions SET
        state = ?, action_lease = NULL, action_lease_until_ms = NULL,
        retry_after_ms = ?, last_error_code = ?, completed_at_ms = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?
        AND state = ? AND action_lease = ?`,
		uint8(state), nullableRetry(retryable, retryAt), string(code), nullableCompleted(retryable, nowMS), nowMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(),
		uint8(action.StateClaimed), string(lease),
	)
	if err := requireOne(result, err, "release outbound action"); err != nil {
		return err
	}
	if err := updateReceiptAndTurn(ctx, tx, ref, delivery, turnState, "", code, nullableCompleted(retryable, nowMS), nowMS); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit action release", err)
	}
	return nil
}

func (store *ActionStore) MarkUnknown(
	ctx context.Context,
	ref agent.DispatchRef,
	lease action.Lease,
	code agent.ErrorCode,
	now time.Time,
) error {
	if lease == "" || code == "" || now.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "mark action unknown", fmt.Errorf("lease, code, and time are required"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin unknown action", err)
	}
	defer tx.Rollback()
	stored, _, _, err := loadAction(ctx, tx, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.NewError(agent.ErrorNotFound, "mark action unknown", fmt.Errorf("action does not exist"))
	}
	if err != nil {
		return storageError("load unknown action", err)
	}
	if stored.State == action.StateUnknownOutcome {
		return tx.Commit()
	}
	if stored.State != action.StateExecuting || stored.Lease != lease {
		return agent.NewError(agent.ErrorConflict, "mark action unknown", fmt.Errorf("action execution lease changed"))
	}
	if err := markUnknownTx(ctx, tx, stored, code, now.UnixMilli()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit unknown action", err)
	}
	return nil
}

type actionQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadAction(ctx context.Context, query actionQuerier, ref agent.DispatchRef) (action.StoredAction, sql.NullInt64, sql.NullInt64, error) {
	var (
		invocationValue string
		responseValue   string
		text            string
		payloadDigest   []byte
		state           int64
		lease           sql.NullString
		leaseUntil      sql.NullInt64
		retryAfter      sql.NullInt64
		completedAt     sql.NullInt64
		providerReceipt sql.NullString
		contentScrubbed int64
	)
	err := query.QueryRowContext(ctx, `SELECT invocation_id, response_id, text, payload_digest, state, action_lease,
        action_lease_until_ms, retry_after_ms, completed_at_ms, provider_receipt, content_scrubbed
      FROM outbound_actions WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?`,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(),
	).Scan(&invocationValue, &responseValue, &text, &payloadDigest, &state, &lease, &leaseUntil, &retryAfter, &completedAt, &providerReceipt, &contentScrubbed)
	if err != nil {
		return action.StoredAction{}, leaseUntil, retryAfter, err
	}
	invocationID, err := identity.ParseInvocationID(invocationValue)
	if err != nil {
		return action.StoredAction{}, leaseUntil, retryAfter, agent.NewError(agent.ErrorIntegrityFailure, "decode outbound action", err)
	}
	responseID, err := identity.ParseMessageID(responseValue)
	if err != nil {
		return action.StoredAction{}, leaseUntil, retryAfter, agent.NewError(agent.ErrorIntegrityFailure, "decode outbound action", err)
	}
	if contentScrubbed == 0 {
		wantedDigest := digestAction(ref.Key, ref.ActionID, text)
		if len(payloadDigest) != len(wantedDigest) || !bytes.Equal(payloadDigest, wantedDigest[:]) {
			return action.StoredAction{}, leaseUntil, retryAfter, agent.NewError(agent.ErrorIntegrityFailure, "decode outbound action", fmt.Errorf("payload digest mismatch"))
		}
	} else if contentScrubbed != 1 || text != "" || len(payloadDigest) != 32 ||
		(action.State(state) != action.StateSucceeded && action.State(state) != action.StateFailedTerminal && action.State(state) != action.StateUnknownOutcome) {
		return action.StoredAction{}, leaseUntil, retryAfter, agent.NewError(agent.ErrorIntegrityFailure, "decode outbound action", fmt.Errorf("invalid scrubbed action"))
	}
	stored := action.StoredAction{
		Ref:             ref,
		InvocationID:    invocationID,
		ResponseID:      responseID,
		Text:            text,
		State:           action.State(state),
		ProviderReceipt: providerReceipt.String,
	}
	if lease.Valid {
		stored.Lease = action.Lease(lease.String)
	}
	if completedAt.Valid {
		value := time.UnixMilli(completedAt.Int64).UTC()
		stored.CompletedAt = &value
	}
	return stored, leaseUntil, retryAfter, nil
}

func markUnknownTx(ctx context.Context, tx *sql.Tx, stored action.StoredAction, code agent.ErrorCode, nowMS int64) error {
	result, err := tx.ExecContext(ctx, `UPDATE outbound_actions SET
        state = ?, action_lease = NULL, action_lease_until_ms = NULL,
        last_error_code = ?, completed_at_ms = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?
        AND state = ? AND action_lease = ?`,
		uint8(action.StateUnknownOutcome), string(code), nowMS, nowMS,
		stored.Ref.Key.TenantID.String(), stored.Ref.Key.AccountID.String(), stored.Ref.Key.ChatID.String(), stored.Ref.ActionID.String(),
		uint8(action.StateExecuting), string(stored.Lease),
	)
	if err := requireOne(result, err, "mark action unknown"); err != nil {
		return err
	}
	return updateReceiptAndTurn(ctx, tx, stored.Ref, agent.DeliveryUnknownOutcome, agent.TurnUnknownOutcome, "", code, nowMS, nowMS)
}

func updateReceiptAndTurn(
	ctx context.Context,
	tx *sql.Tx,
	ref agent.DispatchRef,
	delivery agent.DeliveryStatus,
	turnState agent.TurnState,
	providerReceipt string,
	code agent.ErrorCode,
	completedAt any,
	updatedAtMS int64,
) error {
	receiptResult, err := tx.ExecContext(ctx, `UPDATE action_receipts SET
        status = ?, provider_receipt = ?, error_code = ?, completed_at_ms = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?`,
		uint8(delivery), nullableString(providerReceipt), nullableErrorCode(code), completedAt, updatedAtMS,
		ref.Key.TenantID.String(), ref.Key.AccountID.String(), ref.Key.ChatID.String(), ref.ActionID.String(),
	)
	if err := requireOne(receiptResult, err, "update action receipt"); err != nil {
		return err
	}
	turnResult, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
        delivery_status = ?, turn_state = ?, last_error_code = ?, updated_at_ms = ?
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND action_id = ?`,
		uint8(delivery), uint8(turnState), nullableErrorCode(code), updatedAtMS,
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
		return agent.NewError(agent.ErrorConflict, operation, fmt.Errorf("state or lease changed"))
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

func nullableRetry(retryable bool, value time.Time) any {
	if !retryable {
		return nil
	}
	return value.UnixMilli()
}

func nullableCompleted(retryable bool, nowMS int64) any {
	if retryable {
		return nil
	}
	return nowMS
}
