package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
)

const (
	batchedTurnState = -2
	maxBatchSize     = 256
)

func (store *InboundStore) StageBatch(
	ctx context.Context,
	message conversation.IncomingMessage,
	readyAt time.Time,
) (inbound.BatchStage, error) {
	if err := message.Validate(); err != nil {
		return inbound.BatchStage{}, agent.NewError(agent.ErrorInvalidArgument, "stage message batch", err)
	}
	if readyAt.IsZero() {
		return inbound.BatchStage{}, agent.NewError(agent.ErrorInvalidArgument, "stage message batch", errors.New("ready time is required"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return inbound.BatchStage{}, storageError("begin message batch stage", err)
	}
	defer tx.Rollback()
	var rowID, state, receivedAtMS int64
	var storedReady sql.NullInt64
	var anchor sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT rowid, turn_state, received_at_ms, batch_ready_at_ms, batch_anchor_invocation_id
      FROM inbound_events WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
	).Scan(&rowID, &state, &receivedAtMS, &storedReady, &anchor)
	if errors.Is(err, sql.ErrNoRows) {
		return inbound.BatchStage{}, agent.NewError(agent.ErrorNotFound, "stage message batch", errors.New("message does not exist"))
	}
	if err != nil {
		return inbound.BatchStage{}, storageError("load message batch stage", err)
	}
	activeRecovery := state == int64(agent.TurnGenerating) || state == int64(agent.TurnFailedRetryable)
	if state != 0 && !activeRecovery {
		return inbound.BatchStage{Handled: true}, nil
	}
	var resetAtMS sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT reset_at_ms FROM history_resets
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(),
	).Scan(&resetAtMS); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return inbound.BatchStage{}, storageError("load message batch reset", err)
	}
	if resetAtMS.Valid && receivedAtMS <= resetAtMS.Int64 {
		result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
            turn_state = ?, ignored_reason = 'history_reset', updated_at_ms = ?
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
            AND turn_state = 0 AND invocation_digest IS NULL AND action_id IS NULL`,
			ignoredTurnState, store.clock.Now().UnixMilli(), message.TenantID.String(),
			message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
		)
		if err := requireOne(result, err, "discard pre-reset message batch"); err != nil {
			return inbound.BatchStage{}, err
		}
		if err := tx.Commit(); err != nil {
			return inbound.BatchStage{}, storageError("commit discarded message batch", err)
		}
		return inbound.BatchStage{Handled: true}, nil
	}
	blocked, err := hasEarlierUnfinishedTurn(ctx, tx, message, rowID)
	if err != nil {
		return inbound.BatchStage{}, err
	}
	if activeRecovery {
		if err := tx.Commit(); err != nil {
			return inbound.BatchStage{}, storageError("commit recoverable message batch stage", err)
		}
		return inbound.BatchStage{ReadyAt: store.clock.Now(), Wait: !blocked}, nil
	}
	if anchor.Valid {
		if err := tx.Commit(); err != nil {
			return inbound.BatchStage{}, storageError("commit existing message batch stage", err)
		}
		return inbound.BatchStage{ReadyAt: store.clock.Now(), Wait: true}, nil
	}
	if !storedReady.Valid {
		readyMS := readyAt.UTC().UnixMilli()
		result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET batch_ready_at_ms = ?, updated_at_ms = ?
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
            AND turn_state = 0 AND batch_anchor_invocation_id IS NULL AND batch_ready_at_ms IS NULL`,
			readyMS, store.clock.Now().UnixMilli(),
			message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
		)
		if err := requireOne(result, err, "stage message batch"); err != nil {
			return inbound.BatchStage{}, err
		}
		// A new eligible message extends the open chat batch's quiet window.
		if _, err := tx.ExecContext(ctx, `UPDATE inbound_events SET batch_ready_at_ms = ?
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
            AND turn_state = 0 AND batch_anchor_invocation_id IS NULL
            AND batch_ready_at_ms IS NOT NULL AND batch_ready_at_ms < ?`,
			readyMS, message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), readyMS,
		); err != nil {
			return inbound.BatchStage{}, storageError("extend message batch debounce", err)
		}
	}
	var maxReady int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(batch_ready_at_ms), 0) FROM inbound_events
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
        AND turn_state = 0 AND batch_anchor_invocation_id IS NULL AND batch_ready_at_ms IS NOT NULL`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(),
	).Scan(&maxReady); err != nil {
		return inbound.BatchStage{}, storageError("read message batch debounce", err)
	}
	if maxReady <= 0 {
		return inbound.BatchStage{}, agent.NewError(agent.ErrorIntegrityFailure, "stage message batch", errors.New("ready time disappeared"))
	}
	if blocked {
		// The message remains durably staged, but no waiter may process it ahead
		// of an older unfinished turn. The older turn's recovery waiter drains
		// this open batch after reaching a terminal state.
		if err := tx.Commit(); err != nil {
			return inbound.BatchStage{}, storageError("commit ordered message batch stage", err)
		}
		return inbound.BatchStage{ReadyAt: time.UnixMilli(maxReady).UTC()}, nil
	}
	var firstInvocation string
	if err := tx.QueryRowContext(ctx, `SELECT invocation_id FROM inbound_events
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
		AND turn_state = 0 AND batch_anchor_invocation_id IS NULL AND batch_ready_at_ms IS NOT NULL
	      ORDER BY rowid LIMIT 1`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(),
	).Scan(&firstInvocation); err != nil {
		return inbound.BatchStage{}, storageError("elect message batch waiter", err)
	}
	if err := tx.Commit(); err != nil {
		return inbound.BatchStage{}, storageError("commit message batch stage", err)
	}
	return inbound.BatchStage{
		ReadyAt: time.UnixMilli(maxReady).UTC(),
		Wait:    firstInvocation == message.InvocationID.String(),
	}, nil
}

func (store *InboundStore) ClaimBatch(
	ctx context.Context,
	message conversation.IncomingMessage,
	now time.Time,
	burstCap uint32,
) (inbound.BatchClaim, error) {
	if err := message.Validate(); err != nil {
		return inbound.BatchClaim{}, agent.NewError(agent.ErrorInvalidArgument, "claim message batch", err)
	}
	if now.IsZero() || burstCap == 0 || burstCap > maxBatchSize {
		return inbound.BatchClaim{}, agent.NewError(agent.ErrorInvalidArgument, "claim message batch", errors.New("current time and bounded burst cap are required"))
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return inbound.BatchClaim{}, storageError("begin message batch claim", err)
	}
	defer tx.Rollback()
	var state int64
	var anchor sql.NullString
	var currentProvider sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT turn_state, batch_anchor_invocation_id, provider_message_id FROM inbound_events
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
	).Scan(&state, &anchor, &currentProvider)
	if errors.Is(err, sql.ErrNoRows) {
		return inbound.BatchClaim{}, agent.NewError(agent.ErrorNotFound, "claim message batch", errors.New("message does not exist"))
	}
	if err != nil {
		return inbound.BatchClaim{}, storageError("load message batch claim", err)
	}
	activeRecovery := state == int64(agent.TurnGenerating) || state == int64(agent.TurnFailedRetryable)
	singletonProvider := ""
	if activeRecovery && !anchor.Valid {
		// Part 1 rows and a crash between generation claim and Part 2 batch
		// assignment have no anchor. They are still a valid one-message batch.
		if !currentProvider.Valid || currentProvider.String == "" {
			return inbound.BatchClaim{}, agent.NewError(agent.ErrorIntegrityFailure, "claim message batch", errors.New("recoverable turn lacks provider identity"))
		}
		singletonProvider = currentProvider.String
	} else if state != 0 && !activeRecovery {
		// A waiter may have completed its own batch while more messages were
		// staged behind the burst cap. It can keep draining the chat using the
		// same durable scope even though its original turn is now terminal.
		anchor = sql.NullString{}
	}
	anchorInvocation := anchor.String
	if !anchor.Valid && singletonProvider == "" {
		var maxReady int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(batch_ready_at_ms), 0) FROM inbound_events
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
            AND turn_state = 0 AND batch_anchor_invocation_id IS NULL AND batch_ready_at_ms IS NOT NULL`,
			message.TenantID.String(), message.AccountID.String(), message.ChatID.String(),
		).Scan(&maxReady); err != nil {
			return inbound.BatchClaim{}, storageError("read ready message batch", err)
		}
		if maxReady == 0 {
			if err := tx.Commit(); err != nil {
				return inbound.BatchClaim{}, storageError("commit empty message batch claim", err)
			}
			return inbound.BatchClaim{Handled: true}, nil
		}
		if maxReady > now.UTC().UnixMilli() {
			if err := tx.Commit(); err != nil {
				return inbound.BatchClaim{}, storageError("commit deferred message batch claim", err)
			}
			return inbound.BatchClaim{ReadyAt: time.UnixMilli(maxReady).UTC()}, nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT invocation_id, provider_message_id FROM inbound_events
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ?
            AND turn_state = 0 AND batch_anchor_invocation_id IS NULL
            AND batch_ready_at_ms IS NOT NULL AND batch_ready_at_ms <= ?
		  ORDER BY rowid LIMIT ?`,
			message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), now.UTC().UnixMilli(), burstCap,
		)
		if err != nil {
			return inbound.BatchClaim{}, storageError("select message batch members", err)
		}
		type member struct {
			invocationID      string
			providerMessageID string
		}
		members := make([]member, 0, burstCap)
		for rows.Next() {
			var value member
			if err := rows.Scan(&value.invocationID, &value.providerMessageID); err != nil {
				rows.Close()
				return inbound.BatchClaim{}, storageError("scan message batch member", err)
			}
			members = append(members, value)
		}
		if err := rows.Close(); err != nil {
			return inbound.BatchClaim{}, storageError("close message batch members", err)
		}
		if len(members) == 0 {
			return inbound.BatchClaim{}, agent.NewError(agent.ErrorConflict, "claim message batch", errors.New("no ready messages"))
		}
		anchorInvocation = members[len(members)-1].invocationID
		for index, member := range members {
			nextState := int64(0)
			ignoredReason := any(nil)
			if member.invocationID != anchorInvocation {
				nextState = batchedTurnState
				ignoredReason = "batched"
			}
			result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
                batch_anchor_invocation_id = ?, batch_position = ?, turn_state = ?,
                ignored_reason = ?, updated_at_ms = ?
              WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
                AND turn_state = 0 AND batch_anchor_invocation_id IS NULL`,
				anchorInvocation, index, nextState, ignoredReason, store.clock.Now().UnixMilli(),
				message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), member.invocationID,
			)
			if err := requireOne(result, err, "assign message batch member"); err != nil {
				return inbound.BatchClaim{}, err
			}
		}
	}
	providerIDs := make([]string, 0, burstCap)
	if singletonProvider != "" {
		providerIDs = append(providerIDs, singletonProvider)
	} else {
		rows, err := tx.QueryContext(ctx, `SELECT provider_message_id, batch_position FROM inbound_events
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND batch_anchor_invocation_id = ?
      ORDER BY batch_position LIMIT ?`,
			message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), anchorInvocation, maxBatchSize+1,
		)
		if err != nil {
			return inbound.BatchClaim{}, storageError("load claimed message batch", err)
		}
		for rows.Next() {
			var providerID sql.NullString
			var position sql.NullInt64
			if err := rows.Scan(&providerID, &position); err != nil {
				rows.Close()
				return inbound.BatchClaim{}, storageError("scan claimed message batch", err)
			}
			if !providerID.Valid || !position.Valid || position.Int64 != int64(len(providerIDs)) {
				rows.Close()
				return inbound.BatchClaim{}, agent.NewError(agent.ErrorIntegrityFailure, "load claimed message batch", errors.New("batch member identity or position is invalid"))
			}
			providerIDs = append(providerIDs, providerID.String)
		}
		if err := rows.Close(); err != nil {
			return inbound.BatchClaim{}, storageError("close claimed message batch", err)
		}
		if len(providerIDs) > maxBatchSize {
			return inbound.BatchClaim{}, agent.NewError(agent.ErrorIntegrityFailure, "load claimed message batch", errors.New("batch exceeds durable size bound"))
		}
	}
	messages := make([]conversation.IncomingMessage, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		loaded, _, err := loadInboundByProvider(ctx, tx, message.TenantID, message.AccountID, message.ChatID, providerID)
		if err != nil {
			return inbound.BatchClaim{}, storageError("decode claimed message batch", err)
		}
		if err := refreshMessagePolicy(ctx, tx, &loaded); err != nil {
			return inbound.BatchClaim{}, err
		}
		messages = append(messages, loaded)
	}
	if err := tx.Commit(); err != nil {
		return inbound.BatchClaim{}, storageError("commit message batch claim", err)
	}
	return inbound.BatchClaim{Messages: messages}, nil
}

func hasEarlierUnfinishedTurn(
	ctx context.Context,
	query actionQuerier,
	message conversation.IncomingMessage,
	rowID int64,
) (bool, error) {
	var blocked int64
	err := query.QueryRowContext(ctx, `SELECT EXISTS(
      SELECT 1 FROM inbound_events
	      WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id <> ?
	        AND turn_state IN (0, ?, ?, ?, ?)
	        AND rowid < ?
	    )`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
		uint8(agent.TurnGenerating), uint8(agent.TurnFailedRetryable),
		uint8(agent.TurnResponsePlanned), uint8(agent.TurnDeliveryPending),
		rowID,
	).Scan(&blocked)
	if err != nil {
		return false, storageError("guard message batch ordering", err)
	}
	return blocked == 1, nil
}

func refreshMessagePolicy(ctx context.Context, query actionQuerier, message *conversation.IncomingMessage) error {
	var allowlisted, owner int64
	err := query.QueryRowContext(ctx, `SELECT c.allowlisted, p.owner
      FROM chats c JOIN participants p ON p.tenant_id = c.tenant_id AND p.account_id = c.account_id
      WHERE c.tenant_id = ? AND c.account_id = ? AND c.id = ? AND p.id = ?`,
		message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.SenderID.String(),
	).Scan(&allowlisted, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.NewError(agent.ErrorIntegrityFailure, "refresh batch policy", errors.New("chat or participant disappeared"))
	}
	if err != nil {
		return storageError("refresh batch policy", err)
	}
	message.Allowlisted = allowlisted == 1
	message.Owner = owner == 1
	return nil
}
