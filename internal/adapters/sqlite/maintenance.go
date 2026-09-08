package sqlite

import (
	"context"
	"fmt"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/maintenance"
)

func (store *Store) Maintain(ctx context.Context, request maintenance.Request) (maintenance.Result, error) {
	if request.TenantID.IsZero() || request.Now.IsZero() || request.ScrubBefore.IsZero() || request.DeleteBefore.IsZero() ||
		!request.DeleteBefore.Before(request.ScrubBefore) || !request.ScrubBefore.Before(request.Now) ||
		request.BatchSize == 0 || request.BatchSize > 10_000 {
		return maintenance.Result{}, agent.NewError(agent.ErrorInvalidArgument, "maintain application store", fmt.Errorf("valid tenant, times, and batch size are required"))
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
	result.TurnsDeleted, err = deleteResult.RowsAffected()
	if err != nil {
		return maintenance.Result{}, storageError("inspect deleted terminal turns", err)
	}
	if err := tx.Commit(); err != nil {
		return maintenance.Result{}, storageError("commit application maintenance", err)
	}
	return result, nil
}
