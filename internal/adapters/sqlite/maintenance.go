package sqlite

import (
	"context"
	"errors"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/effect"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/maintenance"
)

// Maintain deletes operational rows that have outlived their use. Finished
// inbound turns (and, by cascade, their replies) go after DeleteBefore; they
// only serve deduplication and usage stats by then. Message text lives on in
// history_entries, which has its own keep-latest/max-age rule.
func (store *Store) Maintain(ctx context.Context, request maintenance.Request) (maintenance.Result, error) {
	if request.TenantID.IsZero() || request.Now.IsZero() || request.DeleteBefore.IsZero() ||
		!request.DeleteBefore.Before(request.Now) || request.BatchSize == 0 || request.BatchSize > 10_000 {
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
	deleteResult, err := tx.ExecContext(ctx, `DELETE FROM inbound_events
      WHERE rowid IN (
        SELECT rowid FROM inbound_events
        WHERE tenant_id = ? AND updated_at_ms <= ? AND turn_state IN (?, ?, ?, ?, ?)
        ORDER BY updated_at_ms, invocation_id LIMIT ?
      )`,
		request.TenantID.String(), request.DeleteBefore.UnixMilli(), ignoredTurnState, batchedTurnState,
		uint8(agent.TurnSucceeded), uint8(agent.TurnFailedTerminal), uint8(agent.TurnUnknownOutcome), request.BatchSize,
	)
	if err != nil {
		return maintenance.Result{}, storageError("delete expired terminal turns", err)
	}
	deletedTerminal, err := deleteResult.RowsAffected()
	if err != nil {
		return maintenance.Result{}, storageError("inspect deleted terminal turns", err)
	}
	result.TurnsDeleted = deletedTerminal
	// A finished effect is only still read when it deleted a message that is
	// in retained history (the transcript marks that message deleted).
	if _, err := tx.ExecContext(ctx, `DELETE FROM typed_effects
      WHERE rowid IN (
        SELECT e.rowid FROM typed_effects e
        WHERE e.tenant_id = ? AND e.updated_at_ms <= ? AND e.state IN (?, ?, ?)
          AND NOT EXISTS (
            SELECT 1 FROM history_entries h
            WHERE h.tenant_id = e.tenant_id AND h.account_id = e.account_id
              AND h.chat_id = e.chat_id AND h.message_id = e.target_message_id
          )
        ORDER BY e.updated_at_ms LIMIT ?
      )`,
		request.TenantID.String(), request.DeleteBefore.UnixMilli(),
		uint8(effect.StateSucceeded), uint8(effect.StateFailedTerminal), uint8(effect.StateUnknownOutcome),
		request.BatchSize,
	); err != nil {
		return maintenance.Result{}, storageError("delete expired effects", err)
	}
	// A sent sticker stays a reply target as long as the turns that could
	// have sent it.
	if _, err := tx.ExecContext(ctx, `DELETE FROM sent_stickers
      WHERE rowid IN (
        SELECT rowid FROM sent_stickers
        WHERE tenant_id = ? AND sent_at_ms <= ?
        ORDER BY sent_at_ms LIMIT ?
      )`,
		request.TenantID.String(), request.DeleteBefore.UnixMilli(), request.BatchSize,
	); err != nil {
		return maintenance.Result{}, storageError("delete expired sent stickers", err)
	}
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
	// Mention bindings follow message reachability rather than inbound-event
	// retention: retained history and quote snapshots can still reference an
	// original message after its operational inbound row is deleted.
	if _, err := tx.ExecContext(ctx, `DELETE FROM message_mentions
      WHERE tenant_id = ?
        AND NOT EXISTS (
          SELECT 1 FROM inbound_events e
          WHERE e.tenant_id = message_mentions.tenant_id
            AND e.account_id = message_mentions.account_id
            AND e.chat_id = message_mentions.chat_id
            AND e.message_id = message_mentions.message_id
        )
        AND NOT EXISTS (
          SELECT 1 FROM history_entries h
          WHERE h.tenant_id = message_mentions.tenant_id
            AND h.account_id = message_mentions.account_id
            AND h.chat_id = message_mentions.chat_id
            AND (h.message_id = message_mentions.message_id
              OR h.quoted_message_id = message_mentions.message_id)
        )`, request.TenantID.String()); err != nil {
		return maintenance.Result{}, storageError("expire message mention bindings", err)
	}
	// Display names are a convenience cache, not durable identity. Keep them
	// only while retained inbound rows or history can use the ref.
	if _, err := tx.ExecContext(ctx, `UPDATE sender_refs AS r SET display_name = ''
      WHERE r.tenant_id = ? AND trim(r.display_name) != ''
        AND NOT EXISTS (
          SELECT 1 FROM inbound_events e
          WHERE e.tenant_id = r.tenant_id AND e.account_id = r.account_id
            AND e.chat_id = r.chat_id AND e.sender_ref = r.sender_ref
		)
		AND NOT EXISTS (
		  SELECT 1 FROM message_mentions m
		  WHERE m.tenant_id = r.tenant_id AND m.account_id = r.account_id
		    AND m.chat_id = r.chat_id AND m.sender_ref = r.sender_ref
		)
        AND NOT EXISTS (
          SELECT 1 FROM history_entries h
          WHERE h.tenant_id = r.tenant_id AND h.account_id = r.account_id
            AND h.chat_id = r.chat_id AND h.sender_ref = r.sender_ref
        )`, request.TenantID.String()); err != nil {
		return maintenance.Result{}, storageError("expire sender display names", err)
	}
	if err := tx.Commit(); err != nil {
		return maintenance.Result{}, storageError("commit application maintenance", err)
	}
	return result, nil
}
