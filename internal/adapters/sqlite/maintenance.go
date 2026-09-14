package sqlite

import (
	"context"
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/maintenance"
)

func (store *Store) Maintain(ctx context.Context, request maintenance.Request) (maintenance.Result, error) {
	if request.TenantID.IsZero() || request.Now.IsZero() || request.ScrubBefore.IsZero() || request.DeleteBefore.IsZero() ||
		!request.DeleteBefore.Before(request.ScrubBefore) || !request.ScrubBefore.Before(request.Now) ||
		request.BatchSize == 0 || request.BatchSize > 10_000 {
		return maintenance.Result{}, agent.NewError(agent.ErrorInvalidArgument, "maintain application store", errors.New("valid tenant, times, and batch size are required"))
	}
	if (request.HistoryKeepLatest == 0) != request.HistoryBefore.IsZero() ||
		(!request.HistoryBefore.IsZero() && !request.HistoryBefore.Before(request.Now)) {
		return maintenance.Result{}, agent.NewError(agent.ErrorInvalidArgument, "maintain application store", errors.New("history retention bounds are incomplete"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return maintenance.Result{}, storageError("begin application maintenance", err)
	}
	defer tx.Rollback()
	result := maintenance.Result{}
	actionResult, err := tx.ExecContext(ctx, `UPDATE outbound_actions SET text = '', content_scrubbed = 1
      WHERE rowid IN (
        SELECT rowid FROM outbound_actions
        WHERE tenant_id = ? AND content_scrubbed = 0 AND completed_at_ms <= ?
          AND state IN (?, ?, ?)
        ORDER BY completed_at_ms, action_id LIMIT ?
      )`,
		request.TenantID.String(), request.ScrubBefore.UnixMilli(),
		uint8(action.StateSucceeded), uint8(action.StateFailedTerminal), uint8(action.StateUnknownOutcome), request.BatchSize,
	)
	if err != nil {
		return maintenance.Result{}, storageError("scrub terminal actions", err)
	}
	result.ActionsScrubbed, err = actionResult.RowsAffected()
	if err != nil {
		return maintenance.Result{}, storageError("inspect scrubbed actions", err)
	}
	inboundResult, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
        sender_name = '', input_text = '', response_text = CASE WHEN response_text IS NULL THEN NULL ELSE '' END,
        quoted_message_id = NULL, quoted_role = NULL, quoted_sender_ref = NULL, quoted_text = NULL, replied_to_bot = 0,
        content_scrubbed = 1
      WHERE rowid IN (
        SELECT rowid FROM inbound_events
        WHERE tenant_id = ? AND content_scrubbed = 0 AND updated_at_ms <= ?
          AND turn_state IN (?, ?, ?, ?)
        ORDER BY updated_at_ms, invocation_id LIMIT ?
      )`,
		request.TenantID.String(), request.ScrubBefore.UnixMilli(), ignoredTurnState,
		uint8(agent.TurnSucceeded), uint8(agent.TurnFailedTerminal), uint8(agent.TurnUnknownOutcome), request.BatchSize,
	)
	if err != nil {
		return maintenance.Result{}, storageError("scrub terminal inbound content", err)
	}
	result.InboundScrubbed, err = inboundResult.RowsAffected()
	if err != nil {
		return maintenance.Result{}, storageError("inspect scrubbed inbound", err)
	}
	batchedScrubResult, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
        sender_name = '', input_text = '', quoted_message_id = NULL, quoted_role = NULL,
        quoted_sender_ref = NULL, quoted_text = NULL, replied_to_bot = 0, content_scrubbed = 1
      WHERE rowid IN (
        SELECT member.rowid FROM inbound_events member
        JOIN inbound_events anchor ON anchor.tenant_id = member.tenant_id
          AND anchor.account_id = member.account_id AND anchor.chat_id = member.chat_id
          AND anchor.invocation_id = member.batch_anchor_invocation_id
        WHERE member.tenant_id = ? AND member.turn_state = ? AND member.content_scrubbed = 0
          AND anchor.updated_at_ms <= ? AND anchor.turn_state IN (?, ?, ?)
        ORDER BY anchor.updated_at_ms, member.batch_position LIMIT ?
      )`,
		request.TenantID.String(), batchedTurnState, request.ScrubBefore.UnixMilli(),
		uint8(agent.TurnSucceeded), uint8(agent.TurnFailedTerminal), uint8(agent.TurnUnknownOutcome), request.BatchSize,
	)
	if err != nil {
		return maintenance.Result{}, storageError("scrub batched inbound content", err)
	}
	if count, rowsErr := batchedScrubResult.RowsAffected(); rowsErr == nil {
		result.InboundScrubbed += count
	}
	batchedDeleteResult, err := tx.ExecContext(ctx, `DELETE FROM inbound_events
      WHERE rowid IN (
        SELECT member.rowid FROM inbound_events member
        JOIN inbound_events anchor ON anchor.tenant_id = member.tenant_id
          AND anchor.account_id = member.account_id AND anchor.chat_id = member.chat_id
          AND anchor.invocation_id = member.batch_anchor_invocation_id
        WHERE member.tenant_id = ? AND member.turn_state = ?
          AND anchor.updated_at_ms <= ? AND anchor.turn_state IN (?, ?, ?)
        ORDER BY anchor.updated_at_ms, member.batch_position LIMIT ?
      )`,
		request.TenantID.String(), batchedTurnState, request.DeleteBefore.UnixMilli(),
		uint8(agent.TurnSucceeded), uint8(agent.TurnFailedTerminal), uint8(agent.TurnUnknownOutcome), request.BatchSize,
	)
	if err != nil {
		return maintenance.Result{}, storageError("delete expired batched turns", err)
	}
	if count, rowsErr := batchedDeleteResult.RowsAffected(); rowsErr == nil {
		result.TurnsDeleted += count
	}
	deleteResult, err := tx.ExecContext(ctx, `DELETE FROM inbound_events
      WHERE rowid IN (
        SELECT rowid FROM inbound_events
        WHERE tenant_id = ? AND updated_at_ms <= ? AND turn_state IN (?, ?, ?, ?)
        ORDER BY updated_at_ms, invocation_id LIMIT ?
      )`,
		request.TenantID.String(), request.DeleteBefore.UnixMilli(), ignoredTurnState,
		uint8(agent.TurnSucceeded), uint8(agent.TurnFailedTerminal), uint8(agent.TurnUnknownOutcome), request.BatchSize,
	)
	if err != nil {
		return maintenance.Result{}, storageError("delete expired terminal turns", err)
	}
	deletedTerminal, err := deleteResult.RowsAffected()
	if err != nil {
		return maintenance.Result{}, storageError("inspect deleted terminal turns", err)
	}
	result.TurnsDeleted += deletedTerminal
	if request.HistoryKeepLatest > 0 {
		historyResult, err := tx.ExecContext(ctx, `DELETE FROM history_entries WHERE sequence IN (
          SELECT sequence FROM (
            SELECT h.sequence, h.delivery_status, h.created_at_ms,
              ROW_NUMBER() OVER (
                PARTITION BY h.tenant_id, h.account_id, h.chat_id ORDER BY h.sequence DESC
              ) AS position,
              COALESCE(r.cutoff_sequence, 0) AS reset_cutoff
            FROM history_entries h
            LEFT JOIN history_resets r ON r.tenant_id = h.tenant_id
              AND r.account_id = h.account_id AND r.chat_id = h.chat_id
            WHERE h.tenant_id = ?
          )
		  WHERE delivery_status != ? AND (
            sequence <= reset_cutoff OR (position > ? AND created_at_ms < ?)
          )
          ORDER BY sequence LIMIT ?
        )`,
			request.TenantID.String(), uint8(agent.DeliveryPending),
			request.HistoryKeepLatest, request.HistoryBefore.UnixMilli(), request.BatchSize,
		)
		if err != nil {
			return maintenance.Result{}, storageError("trim retained history", err)
		}
		result.HistoryRemoved, err = historyResult.RowsAffected()
		if err != nil {
			return maintenance.Result{}, storageError("inspect trimmed history", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return maintenance.Result{}, storageError("commit application maintenance", err)
	}
	return result, nil
}
