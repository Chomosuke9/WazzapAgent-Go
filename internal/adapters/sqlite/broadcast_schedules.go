package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/broadcast"
)

const broadcastScheduleRetention = 30 * 24 * time.Hour

func (store *Store) SaveBroadcastSchedule(ctx context.Context, schedule broadcast.Schedule) error {
	if schedule.ID == "" || schedule.ScheduledAt.IsZero() || schedule.CreatedAt.IsZero() || schedule.UpdatedAt.IsZero() || len(schedule.Targets) == 0 {
		return agent.NewError(agent.ErrorInvalidArgument, "save broadcast schedule", errors.New("schedule identity, due time, timestamps, and recipients are required"))
	}
	targets, err := json.Marshal(schedule.Targets)
	if err != nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "encode broadcast recipients", err)
	}
	results, err := json.Marshal(schedule.Results)
	if err != nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "encode broadcast results", err)
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO broadcast_schedules (
		id, scheduled_at_ms, format, payload, batch_size, batch_delay_seconds,
		targets_json, status, results_json, created_at_ms, updated_at_ms
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		schedule.ID, schedule.ScheduledAt.UTC().UnixMilli(), schedule.Format, schedule.Payload,
		schedule.BatchSize, schedule.BatchDelaySeconds, string(targets), string(schedule.Status), string(results),
		schedule.CreatedAt.UTC().UnixMilli(), schedule.UpdatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "save broadcast schedule", err)
	}
	return nil
}

func (store *Store) ListBroadcastSchedules(ctx context.Context) ([]broadcast.Schedule, error) {
	cutoff := time.Now().UTC().Add(-broadcastScheduleRetention).UnixMilli()
	rows, err := store.read.QueryContext(ctx, `SELECT id, scheduled_at_ms, format, payload, batch_size,
		batch_delay_seconds, targets_json, status, results_json, created_at_ms, updated_at_ms
		FROM broadcast_schedules
		WHERE status = 'scheduled' OR updated_at_ms >= ?
		ORDER BY CASE WHEN status = 'scheduled' THEN 0 ELSE 1 END, scheduled_at_ms, updated_at_ms DESC
		LIMIT 100`, cutoff)
	if err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "list broadcast schedules", err)
	}
	defer rows.Close()
	var schedules []broadcast.Schedule
	for rows.Next() {
		schedule, err := scanBroadcastSchedule(rows)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "read broadcast schedules", err)
	}
	return schedules, nil
}

func (store *Store) ClaimDueBroadcastSchedule(ctx context.Context, now time.Time) (*broadcast.Schedule, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "claim broadcast schedule", err)
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT id, scheduled_at_ms, format, payload, batch_size,
		batch_delay_seconds, targets_json, status, results_json, created_at_ms, updated_at_ms
		FROM broadcast_schedules
		WHERE status = 'scheduled' AND scheduled_at_ms <= ?
		ORDER BY scheduled_at_ms, created_at_ms LIMIT 1`, now.UTC().UnixMilli())
	schedule, err := scanBroadcastSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE broadcast_schedules
		SET status = 'sending', updated_at_ms = ? WHERE id = ? AND status = 'scheduled'`,
		now.UTC().UnixMilli(), schedule.ID)
	if err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "claim broadcast schedule", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil || changed != 1 {
		if err == nil {
			err = errors.New("schedule changed before it could be claimed")
		}
		return nil, agent.NewError(agent.ErrorConflict, "claim broadcast schedule", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "commit broadcast schedule claim", err)
	}
	schedule.Status = broadcast.StatusSending
	schedule.UpdatedAt = now.UTC()
	return &schedule, nil
}

func (store *Store) CompleteBroadcastSchedule(ctx context.Context, id string, status broadcast.Status, results []broadcast.Result) error {
	if status != broadcast.StatusCompleted && status != broadcast.StatusPartial && status != broadcast.StatusFailed {
		return agent.NewError(agent.ErrorInvalidArgument, "complete broadcast schedule", errors.New("final schedule status is invalid"))
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		return agent.NewError(agent.ErrorIntegrityFailure, "encode broadcast results", err)
	}
	updated, err := store.db.ExecContext(ctx, `UPDATE broadcast_schedules
		SET status = ?, results_json = ?, updated_at_ms = ? WHERE id = ? AND status = 'sending'`,
		string(status), string(encoded), time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "complete broadcast schedule", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "complete broadcast schedule", err)
	}
	if changed != 1 {
		return agent.NewError(agent.ErrorConflict, "complete broadcast schedule", errors.New("schedule is no longer being sent"))
	}
	return nil
}

func (store *Store) CancelBroadcastSchedule(ctx context.Context, id string) error {
	updated, err := store.db.ExecContext(ctx, `UPDATE broadcast_schedules
		SET status = 'cancelled', updated_at_ms = ? WHERE id = ? AND status = 'scheduled'`,
		time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "cancel broadcast schedule", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "cancel broadcast schedule", err)
	}
	if changed != 1 {
		return agent.NewError(agent.ErrorConflict, "cancel broadcast schedule", errors.New("schedule is no longer pending"))
	}
	return nil
}

func (store *Store) RecoverInterruptedBroadcastSchedules(ctx context.Context) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "recover broadcast schedules", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, targets_json FROM broadcast_schedules WHERE status = 'sending'`)
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "find interrupted broadcast schedules", err)
	}
	type interrupted struct {
		id      string
		targets []broadcast.Target
	}
	var pending []interrupted
	for rows.Next() {
		var item interrupted
		var targets string
		if err := rows.Scan(&item.id, &targets); err != nil {
			_ = rows.Close()
			return agent.NewError(agent.ErrorStorageFailure, "read interrupted broadcast schedule", err)
		}
		if err := json.Unmarshal([]byte(targets), &item.targets); err != nil {
			_ = rows.Close()
			return agent.NewError(agent.ErrorIntegrityFailure, "decode interrupted broadcast recipients", err)
		}
		pending = append(pending, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return agent.NewError(agent.ErrorStorageFailure, "read interrupted broadcast schedules", err)
	}
	if err := rows.Close(); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "close interrupted broadcast schedules", err)
	}
	now := time.Now().UTC().UnixMilli()
	for _, item := range pending {
		results := make([]broadcast.Result, len(item.targets))
		for index, target := range item.targets {
			results[index] = broadcast.Result{Name: target.Name, ErrorCode: string(agent.ErrorUnknownOutcome)}
		}
		encoded, err := json.Marshal(results)
		if err != nil {
			return agent.NewError(agent.ErrorIntegrityFailure, "encode interrupted broadcast results", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE broadcast_schedules
			SET status = 'failed', results_json = ?, updated_at_ms = ? WHERE id = ? AND status = 'sending'`,
			string(encoded), now, item.id); err != nil {
			return agent.NewError(agent.ErrorStorageFailure, "mark interrupted broadcast schedule", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "commit interrupted broadcast recovery", err)
	}
	return nil
}

type broadcastScheduleScanner interface {
	Scan(...any) error
}

func scanBroadcastSchedule(scanner broadcastScheduleScanner) (broadcast.Schedule, error) {
	var schedule broadcast.Schedule
	var scheduledAtMS, createdAtMS, updatedAtMS int64
	var targetsJSON, status, resultsJSON string
	err := scanner.Scan(&schedule.ID, &scheduledAtMS, &schedule.Format, &schedule.Payload,
		&schedule.BatchSize, &schedule.BatchDelaySeconds, &targetsJSON, &status, &resultsJSON, &createdAtMS, &updatedAtMS)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return broadcast.Schedule{}, err
		}
		return broadcast.Schedule{}, agent.NewError(agent.ErrorStorageFailure, "read broadcast schedule", err)
	}
	if err := json.Unmarshal([]byte(targetsJSON), &schedule.Targets); err != nil {
		return broadcast.Schedule{}, agent.NewError(agent.ErrorIntegrityFailure, "decode broadcast recipients", fmt.Errorf("schedule %s: %w", schedule.ID, err))
	}
	if err := json.Unmarshal([]byte(resultsJSON), &schedule.Results); err != nil {
		return broadcast.Schedule{}, agent.NewError(agent.ErrorIntegrityFailure, "decode broadcast results", fmt.Errorf("schedule %s: %w", schedule.ID, err))
	}
	schedule.Status = broadcast.Status(status)
	schedule.ScheduledAt = time.UnixMilli(scheduledAtMS).UTC()
	schedule.CreatedAt = time.UnixMilli(createdAtMS).UTC()
	schedule.UpdatedAt = time.UnixMilli(updatedAtMS).UTC()
	return schedule, nil
}
