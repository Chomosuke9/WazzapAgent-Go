package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
)

const batchedTurnState = -2

// ClaimBatch turns messages queued in memory for one chat into one turn.
// Messages that no longer need a reply are dropped: already answered or
// ignored, or received before a history reset. A message whose turn already
// started (a crash or a failed generation) can only be an anchor, so it ends
// the batch: either it is first and runs alone, or the batch stops just
// before it. The second result holds the messages not consumed, in arrival
// order, for the caller to queue again. Every returned message except the last
// is recorded as batched into the last one.
func (store *InboundStore) ClaimBatch(
	ctx context.Context,
	batch []conversation.IncomingMessage,
) ([]conversation.IncomingMessage, []conversation.IncomingMessage, error) {
	if len(batch) == 0 {
		return nil, nil, nil
	}
	first := batch[0]
	for _, message := range batch {
		if err := message.Validate(); err != nil {
			return nil, nil, agent.NewError(agent.ErrorInvalidArgument, "claim message batch", err)
		}
		if message.TenantID != first.TenantID || message.AccountID != first.AccountID || message.ChatID != first.ChatID {
			return nil, nil, agent.NewError(agent.ErrorInvalidArgument, "claim message batch", errors.New("batch spans more than one chat"))
		}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, storageError("begin message batch claim", err)
	}
	defer tx.Rollback()
	var resetAtMS sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT reset_at_ms FROM history_resets
      WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
		first.TenantID.String(), first.AccountID.String(), first.ChatID.String(),
	).Scan(&resetAtMS); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, storageError("load message batch reset", err)
	}
	// Messages can reach the queue out of order when two workers accept them
	// at the same moment; the inbox's row order is the arrival order.
	rowIDs := make(map[string]int64, len(batch))
	for _, message := range batch {
		var rowID int64
		err := tx.QueryRowContext(ctx, `SELECT rowid FROM inbound_events
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
			message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
		).Scan(&rowID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, agent.NewError(agent.ErrorNotFound, "claim message batch", errors.New("message does not exist"))
		}
		if err != nil {
			return nil, nil, storageError("order message batch", err)
		}
		rowIDs[message.InvocationID.String()] = rowID
	}
	batch = append([]conversation.IncomingMessage(nil), batch...)
	sort.SliceStable(batch, func(left, right int) bool {
		return rowIDs[batch[left].InvocationID.String()] < rowIDs[batch[right].InvocationID.String()]
	})
	nowMS := store.clock.Now().UnixMilli()
	claimed := make([]conversation.IncomingMessage, 0, len(batch))
	used := 0
	for _, message := range batch {
		var state, started, receivedAtMS int64
		err := tx.QueryRowContext(ctx, `SELECT turn_state, turn_claimed, received_at_ms FROM inbound_events
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
			message.TenantID.String(), message.AccountID.String(), message.ChatID.String(), message.InvocationID.String(),
		).Scan(&state, &started, &receivedAtMS)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, agent.NewError(agent.ErrorNotFound, "claim message batch", errors.New("message does not exist"))
		}
		if err != nil {
			return nil, nil, storageError("load message batch member", err)
		}
		if started == 1 {
			if state != int64(agent.TurnGenerating) && state != int64(agent.TurnFailedRetryable) {
				used++ // answered since it was queued
				continue
			}
			if len(claimed) > 0 {
				break
			}
			if err := refreshMessagePolicy(ctx, tx, &message); err != nil {
				return nil, nil, err
			}
			claimed = append(claimed, message)
			used++
			break
		}
		used++
		if state != 0 {
			continue
		}
		if resetAtMS.Valid && receivedAtMS <= resetAtMS.Int64 {
			if _, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
                turn_state = ?, ignored_reason = 'history_reset', updated_at_ms = ?
              WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?`,
				ignoredTurnState, nowMS, message.TenantID.String(), message.AccountID.String(),
				message.ChatID.String(), message.InvocationID.String(),
			); err != nil {
				return nil, nil, storageError("discard pre-reset message", err)
			}
			continue
		}
		if err := refreshMessagePolicy(ctx, tx, &message); err != nil {
			return nil, nil, err
		}
		claimed = append(claimed, message)
	}
	for _, message := range claimed[:max(len(claimed)-1, 0)] {
		result, err := tx.ExecContext(ctx, `UPDATE inbound_events SET
            turn_state = ?, ignored_reason = 'batched', updated_at_ms = ?
          WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND invocation_id = ?
            AND turn_state = 0 AND turn_claimed = 0`,
			batchedTurnState, nowMS, message.TenantID.String(), message.AccountID.String(),
			message.ChatID.String(), message.InvocationID.String(),
		)
		if err := requireOne(result, err, "record batched message"); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, storageError("commit message batch claim", err)
	}
	return claimed, batch[used:], nil
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
