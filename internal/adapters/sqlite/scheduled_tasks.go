package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/inbound"
)

// SaveScheduledTask stores task and reports whether it is new. A second task
// from the same source message is not stored.
func (store *InboundStore) SaveScheduledTask(ctx context.Context, task inbound.ScheduledTask) (bool, error) {
	if err := task.Validate(); err != nil {
		return false, agent.NewError(agent.ErrorInvalidArgument, "save scheduled task", err)
	}
	var dailyMinute sql.NullInt64
	if task.Daily {
		dailyMinute = sql.NullInt64{Int64: int64(task.DailyMinute), Valid: true}
	}
	result, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO scheduled_tasks(
	    tenant_id, account_id, chat_id, id, invocation_id, source_message_id, prompt, fire_at_ms, created_at_ms, daily_minute
	  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.Key.TenantID.String(), task.Key.AccountID.String(), task.Key.ChatID.String(),
		task.ID.String(), task.InvocationID.String(), task.Source.String(), task.Prompt,
		task.FireAt.UTC().UnixMilli(), store.clock.Now().UTC().UnixMilli(), dailyMinute,
	)
	if err != nil {
		return false, storageError("save scheduled task", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, storageError("inspect scheduled task save", err)
	}
	return changed == 1, nil
}

// ListScheduledTasks returns every task of the tenant, soonest first.
func (store *InboundStore) ListScheduledTasks(ctx context.Context, tenantID identity.TenantID) ([]inbound.ScheduledTask, error) {
	rows, err := store.read.QueryContext(ctx, `SELECT account_id, chat_id, id, invocation_id, source_message_id, prompt, fire_at_ms, daily_minute
	  FROM scheduled_tasks WHERE tenant_id = ? ORDER BY fire_at_ms, id`, tenantID.String())
	if err != nil {
		return nil, storageError("list scheduled tasks", err)
	}
	defer rows.Close()
	var tasks []inbound.ScheduledTask
	for rows.Next() {
		var account, chat, id, invocation, source, prompt string
		var fireAt int64
		var dailyMinute sql.NullInt64
		if err := rows.Scan(&account, &chat, &id, &invocation, &source, &prompt, &fireAt, &dailyMinute); err != nil {
			return nil, storageError("scan scheduled task", err)
		}
		task := inbound.ScheduledTask{
			Key: agent.Key{TenantID: tenantID}, Prompt: prompt, FireAt: time.UnixMilli(fireAt).UTC(),
			Daily: dailyMinute.Valid, DailyMinute: int(dailyMinute.Int64),
		}
		var parseErr error
		if task.Key.AccountID, err = identity.ParseAccountID(account); err != nil {
			parseErr = err
		} else if task.Key.ChatID, err = identity.ParseChatID(chat); err != nil {
			parseErr = err
		} else if task.ID, err = identity.ParseCausationID(id); err != nil {
			parseErr = err
		} else if task.InvocationID, err = identity.ParseInvocationID(invocation); err != nil {
			parseErr = err
		} else if task.Source, err = identity.ParseMessageID(source); err != nil {
			parseErr = err
		}
		if parseErr != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode scheduled task", parseErr)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("iterate scheduled tasks", err)
	}
	return tasks, nil
}

// RescheduleScheduledTask moves a daily task to its next run under a new
// invocation. It reports false when the task was deleted meanwhile.
func (store *InboundStore) RescheduleScheduledTask(ctx context.Context, key agent.Key, id identity.CausationID, invocation identity.InvocationID, fireAt time.Time) (bool, error) {
	if id.IsZero() || invocation.IsZero() || fireAt.IsZero() {
		return false, agent.NewError(agent.ErrorInvalidArgument, "reschedule task", errors.New("task ID, invocation ID and fire time are required"))
	}
	result, err := store.db.ExecContext(ctx, `UPDATE scheduled_tasks SET invocation_id = ?, fire_at_ms = ?
	  WHERE tenant_id = ? AND id = ? AND daily_minute IS NOT NULL`,
		invocation.String(), fireAt.UTC().UnixMilli(), key.TenantID.String(), id.String())
	if err != nil {
		return false, storageError("reschedule task", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, storageError("inspect task reschedule", err)
	}
	return changed == 1, nil
}

// DeleteScheduledTask removes a task that has run or was cancelled. Deleting
// a missing task is not an error.
func (store *InboundStore) DeleteScheduledTask(ctx context.Context, key agent.Key, id identity.CausationID) error {
	if id.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "delete scheduled task", errors.New("task ID is required"))
	}
	_, err := store.db.ExecContext(ctx, `DELETE FROM scheduled_tasks WHERE tenant_id = ? AND id = ?`,
		key.TenantID.String(), id.String())
	if err != nil {
		return storageError("delete scheduled task", err)
	}
	return nil
}
