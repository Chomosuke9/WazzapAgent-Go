package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/subagent"
)

// SubagentStore keeps sub-agent tasks, the files sent for them, and the
// per-chat sub-agent switch.
type SubagentStore struct{ *Store }

func (store *Store) Subagent() *SubagentStore { return &SubagentStore{Store: store} }

const subagentTaskColumns = `account_id, chat_id, session_id, instruction, high_quality, previous_session_id,
    file_message_ids, invocation_id, causation_id, status, submitted, result_json, report,
    started_at_ms, ended_at_ms, delivered_at_ms`

func (store *SubagentStore) CreateTask(ctx context.Context, task subagent.Task) error {
	if err := task.Key.Validate(); err != nil || task.SessionID == "" || strings.TrimSpace(task.Instruction) == "" ||
		task.InvocationID.IsZero() || task.CausationID.IsZero() || task.StartedAt.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "create sub-agent task", errors.New("chat, session, instruction, turn identity and start time are required"))
	}
	fileIDs := make([]string, 0, len(task.FileMessageIDs))
	for _, id := range task.FileMessageIDs {
		fileIDs = append(fileIDs, id.String())
	}
	encodedFiles, err := json.Marshal(fileIDs)
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "create sub-agent task", err)
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO subagent_tasks(
	    tenant_id, account_id, chat_id, session_id, instruction, high_quality, previous_session_id,
	    file_message_ids, invocation_id, causation_id, status, submitted, started_at_ms
	  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.Key.TenantID.String(), task.Key.AccountID.String(), task.Key.ChatID.String(), task.SessionID,
		task.Instruction, boolInt(task.HighQuality), nullableString(task.PreviousSessionID), string(encodedFiles),
		task.InvocationID.String(), task.CausationID.String(), uint8(subagent.StatusRunning), boolInt(task.Submitted),
		task.StartedAt.UTC().UnixMilli(),
	)
	if err != nil && isUniqueConstraint(err) {
		return agent.NewError(agent.ErrorConflict, "create sub-agent task", errors.New("the chat already has a running sub-agent task"))
	}
	if err != nil {
		return storageError("create sub-agent task", err)
	}
	return nil
}

func (store *SubagentStore) MarkTaskSubmitted(ctx context.Context, tenantID identity.TenantID, sessionID string) error {
	_, err := store.db.ExecContext(ctx, `UPDATE subagent_tasks SET submitted = 1 WHERE tenant_id = ? AND session_id = ?`,
		tenantID.String(), sessionID)
	if err != nil {
		return storageError("mark sub-agent task submitted", err)
	}
	return nil
}

func (store *SubagentStore) FinishTask(ctx context.Context, tenantID identity.TenantID, sessionID string, status subagent.Status, report string, result subagent.Result, endedAt time.Time) (bool, error) {
	if status != subagent.StatusCompleted && status != subagent.StatusFailed || endedAt.IsZero() {
		return false, agent.NewError(agent.ErrorInvalidArgument, "finish sub-agent task", errors.New("a final status and end time are required"))
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return false, agent.NewError(agent.ErrorInternal, "finish sub-agent task", err)
	}
	updated, err := store.db.ExecContext(ctx, `UPDATE subagent_tasks SET status = ?, report = ?, result_json = ?, ended_at_ms = ?
	  WHERE tenant_id = ? AND session_id = ? AND status = ?`,
		uint8(status), report, string(encoded), endedAt.UTC().UnixMilli(), tenantID.String(), sessionID, uint8(subagent.StatusRunning))
	if err != nil {
		return false, storageError("finish sub-agent task", err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return false, storageError("inspect sub-agent task finish", err)
	}
	return changed == 1, nil
}

func (store *SubagentStore) LoadTask(ctx context.Context, tenantID identity.TenantID, sessionID string) (subagent.Task, error) {
	row := store.read.QueryRowContext(ctx, `SELECT `+subagentTaskColumns+` FROM subagent_tasks WHERE tenant_id = ? AND session_id = ?`,
		tenantID.String(), sessionID)
	task, err := scanSubagentTask(tenantID, row)
	if errors.Is(err, sql.ErrNoRows) {
		return subagent.Task{}, agent.NewError(agent.ErrorNotFound, "load sub-agent task", errors.New("no task has that session"))
	}
	return task, err
}

func (store *SubagentStore) ActiveTask(ctx context.Context, key agent.Key) (*subagent.Task, error) {
	return store.chatTask(ctx, key, `status = 1`)
}

func (store *SubagentStore) TaskByInvocation(ctx context.Context, key agent.Key, invocationID identity.InvocationID) (*subagent.Task, error) {
	return store.chatTask(ctx, key, `invocation_id = ?`, invocationID.String())
}

func (store *SubagentStore) LatestUndelivered(ctx context.Context, key agent.Key) (*subagent.Task, error) {
	return store.chatTask(ctx, key, `status <> 1 AND delivered_at_ms IS NULL`)
}

// chatTask returns the chat's newest task matching where, or nil.
func (store *SubagentStore) chatTask(ctx context.Context, key agent.Key, where string, args ...any) (*subagent.Task, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	args = append([]any{key.TenantID.String(), key.AccountID.String(), key.ChatID.String()}, args...)
	row := store.read.QueryRowContext(ctx, `SELECT `+subagentTaskColumns+` FROM subagent_tasks
	  WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND `+where+`
	  ORDER BY started_at_ms DESC, session_id DESC LIMIT 1`, args...)
	task, err := scanSubagentTask(key.TenantID, row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (store *SubagentStore) ListUndelivered(ctx context.Context, tenantID identity.TenantID) ([]subagent.Task, error) {
	rows, err := store.read.QueryContext(ctx, `SELECT `+subagentTaskColumns+` FROM subagent_tasks
	  WHERE tenant_id = ? AND delivered_at_ms IS NULL ORDER BY started_at_ms, session_id`, tenantID.String())
	if err != nil {
		return nil, storageError("list sub-agent tasks", err)
	}
	defer rows.Close()
	var tasks []subagent.Task
	for rows.Next() {
		task, err := scanSubagentTask(tenantID, rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, storageError("iterate sub-agent tasks", err)
	}
	return tasks, nil
}

type subagentRowScanner interface{ Scan(...any) error }

func scanSubagentTask(tenantID identity.TenantID, row subagentRowScanner) (subagent.Task, error) {
	var (
		accountValue, chatValue, fileIDs, invocationValue, causationValue, resultJSON string
		previous                                                                      sql.NullString
		highQuality, status, submitted, startedMS                                     int64
		endedMS, deliveredMS                                                          sql.NullInt64
		task                                                                          subagent.Task
	)
	err := row.Scan(&accountValue, &chatValue, &task.SessionID, &task.Instruction, &highQuality, &previous,
		&fileIDs, &invocationValue, &causationValue, &status, &submitted, &resultJSON, &task.Report,
		&startedMS, &endedMS, &deliveredMS)
	if errors.Is(err, sql.ErrNoRows) {
		return subagent.Task{}, err
	}
	if err != nil {
		return subagent.Task{}, storageError("read sub-agent task", err)
	}
	accountID, accountErr := identity.ParseAccountID(accountValue)
	chatID, chatErr := identity.ParseChatID(chatValue)
	invocationID, invocationErr := identity.ParseInvocationID(invocationValue)
	causationID, causationErr := identity.ParseCausationID(causationValue)
	var rawFiles []string
	filesErr := json.Unmarshal([]byte(fileIDs), &rawFiles)
	resultErr := json.Unmarshal([]byte(resultJSON), &task.Result)
	if err := errors.Join(accountErr, chatErr, invocationErr, causationErr, filesErr, resultErr); err != nil {
		return subagent.Task{}, agent.NewError(agent.ErrorIntegrityFailure, "decode sub-agent task", err)
	}
	for _, raw := range rawFiles {
		id, err := identity.ParseMessageID(raw)
		if err != nil {
			return subagent.Task{}, agent.NewError(agent.ErrorIntegrityFailure, "decode sub-agent task files", err)
		}
		task.FileMessageIDs = append(task.FileMessageIDs, id)
	}
	task.Key = agent.Key{TenantID: tenantID, AccountID: accountID, ChatID: chatID}
	task.InvocationID, task.CausationID = invocationID, causationID
	task.HighQuality, task.Submitted, task.Status = highQuality == 1, submitted == 1, subagent.Status(status)
	task.PreviousSessionID = previous.String
	task.StartedAt = time.UnixMilli(startedMS).UTC()
	if endedMS.Valid {
		task.EndedAt = time.UnixMilli(endedMS.Int64).UTC()
	}
	if deliveredMS.Valid {
		task.DeliveredAt = time.UnixMilli(deliveredMS.Int64).UTC()
	}
	return task, nil
}

func (store *SubagentStore) DeliveryPartDone(ctx context.Context, tenantID identity.TenantID, sessionID, part string) (bool, error) {
	var done int
	err := store.read.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM subagent_delivery_parts
	  WHERE tenant_id = ? AND session_id = ? AND part = ?)`, tenantID.String(), sessionID, part).Scan(&done)
	if err != nil {
		return false, storageError("read sub-agent delivery part", err)
	}
	return done == 1, nil
}

func (store *SubagentStore) MarkDeliveryPartDone(ctx context.Context, tenantID identity.TenantID, sessionID, part string) error {
	_, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO subagent_delivery_parts(tenant_id, session_id, part) VALUES (?, ?, ?)`,
		tenantID.String(), sessionID, part)
	if err != nil {
		return storageError("record sub-agent delivery part", err)
	}
	return nil
}

func (store *SubagentStore) MarkTaskDelivered(ctx context.Context, tenantID identity.TenantID, sessionID string, at time.Time) error {
	_, err := store.db.ExecContext(ctx, `UPDATE subagent_tasks SET delivered_at_ms = ?
	  WHERE tenant_id = ? AND session_id = ? AND delivered_at_ms IS NULL`, at.UTC().UnixMilli(), tenantID.String(), sessionID)
	if err != nil {
		return storageError("mark sub-agent task delivered", err)
	}
	return nil
}

func (store *SubagentStore) ChatExists(ctx context.Context, key agent.Key) (bool, error) {
	if err := key.Validate(); err != nil {
		return false, err
	}
	var exists int
	err := store.read.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chats WHERE tenant_id = ? AND account_id = ? AND id = ?)`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String()).Scan(&exists)
	if err != nil {
		return false, storageError("read sub-agent chat", err)
	}
	return exists == 1, nil
}

func (store *SubagentStore) SubagentEnabled(ctx context.Context, key agent.Key) (enabled, set bool, err error) {
	if err := key.Validate(); err != nil {
		return false, false, err
	}
	var value int
	err = store.read.QueryRowContext(ctx, `SELECT enabled FROM subagent_chat_settings
	  WHERE tenant_id = ? AND account_id = ? AND chat_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String()).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		err = store.read.QueryRowContext(ctx, `SELECT default_enabled FROM subagent_account_settings
		  WHERE tenant_id = ? AND account_id = ?`, key.TenantID.String(), key.AccountID.String()).Scan(&value)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, storageError("read sub-agent setting", err)
	}
	return value == 1, true, nil
}

func (store *SubagentStore) SetSubagentEnabled(ctx context.Context, key agent.Key, scope subagent.Scope, enabled bool) error {
	if err := key.Validate(); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin sub-agent setting", err)
	}
	defer tx.Rollback()
	tenant, account := key.TenantID.String(), key.AccountID.String()
	switch scope {
	case subagent.ScopeChat:
		_, err = tx.ExecContext(ctx, `INSERT INTO subagent_chat_settings(tenant_id, account_id, chat_id, enabled) VALUES (?, ?, ?, ?)
		  ON CONFLICT(tenant_id, account_id, chat_id) DO UPDATE SET enabled = excluded.enabled`,
			tenant, account, key.ChatID.String(), boolInt(enabled))
	case subagent.ScopeDefault, subagent.ScopeGlobal:
		if scope == subagent.ScopeGlobal {
			if _, err = tx.ExecContext(ctx, `DELETE FROM subagent_chat_settings WHERE tenant_id = ? AND account_id = ?`, tenant, account); err != nil {
				return storageError("clear sub-agent chat settings", err)
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO subagent_account_settings(tenant_id, account_id, default_enabled) VALUES (?, ?, ?)
		  ON CONFLICT(tenant_id, account_id) DO UPDATE SET default_enabled = excluded.default_enabled`,
			tenant, account, boolInt(enabled))
	default:
		return agent.NewError(agent.ErrorInvalidArgument, "save sub-agent setting", errors.New("scope is invalid"))
	}
	if err != nil {
		return storageError("save sub-agent setting", err)
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit sub-agent setting", err)
	}
	return nil
}

// RecordSubagentMessage stores a message the sub-agent flow sent (a notice,
// a report, or an output file) as the bot's own message in the transcript and
// binds its provider receipt, so it can be quoted. A file is also kept as a
// possible input of a later task.
func (store *SubagentStore) RecordSubagentMessage(ctx context.Context, key agent.Key, text, providerReceipt string, file *subagent.SentFile, sentAt time.Time) error {
	if err := key.Validate(); err != nil || strings.TrimSpace(text) == "" || strings.TrimSpace(providerReceipt) == "" || sentAt.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "record sub-agent message", errors.New("chat, text, provider receipt and time are required"))
	}
	messageID, messageErr := identity.NewMessageID()
	invocationID, invocationErr := identity.NewInvocationID()
	causationID, causationErr := identity.NewCausationID()
	if err := errors.Join(messageErr, invocationErr, causationErr); err != nil {
		return agent.NewError(agent.ErrorInternal, "record sub-agent message", err)
	}
	entry := agent.HistoryEntry{
		MessageID: messageID, InvocationID: invocationID,
		Causation: agent.CausationRef{Kind: agent.CausationSubagent, ID: causationID},
		Role:      agent.HistoryAssistant, Content: []agent.ContentPart{agent.TextPart{Text: text}},
		Delivery: agent.DeliverySucceeded, CreatedAt: sentAt.UTC(),
	}
	if err := agent.ValidateHistoryEntry(entry); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin sub-agent message record", err)
	}
	defer tx.Rollback()
	sentMS := sentAt.UTC().UnixMilli()
	if err := store.appendHistoryEntryTx(ctx, tx, key, entry, sentMS); err != nil {
		return err
	}
	tenant, account, chat := key.TenantID.String(), key.AccountID.String(), key.ChatID.String()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO manual_message_targets(
	    tenant_id, account_id, chat_id, message_id, provider_receipt, created_at_ms
	  ) VALUES (?, ?, ?, ?, ?, ?)`, tenant, account, chat, messageID.String(), providerReceipt, sentMS); err != nil {
		return storageError("record sub-agent message target", err)
	}
	if file != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO subagent_files(
		    tenant_id, account_id, chat_id, message_id, provider_receipt, path, name, mime, kind, size_bytes, created_at_ms
		  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tenant, account, chat, messageID.String(), providerReceipt, file.Path, file.Name, file.Mime, file.Kind, file.Size, sentMS); err != nil {
			return storageError("record sub-agent file", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit sub-agent message record", err)
	}
	return nil
}

// SentFile returns the file the bot sent as messageID, or an
// agent.ErrorNotFound error when the message is not such a file.
func (store *SubagentStore) SentFile(ctx context.Context, key agent.Key, messageID identity.MessageID) (subagent.SentFile, error) {
	if err := key.Validate(); err != nil || messageID.IsZero() {
		return subagent.SentFile{}, agent.NewError(agent.ErrorInvalidArgument, "read sub-agent file", errors.New("chat and message are required"))
	}
	var file subagent.SentFile
	err := store.read.QueryRowContext(ctx, `SELECT path, name, mime, kind, size_bytes FROM subagent_files
	  WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND message_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), messageID.String(),
	).Scan(&file.Path, &file.Name, &file.Mime, &file.Kind, &file.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return subagent.SentFile{}, agent.NewError(agent.ErrorNotFound, "read sub-agent file", err)
	}
	if err != nil {
		return subagent.SentFile{}, storageError("read sub-agent file", err)
	}
	return file, nil
}

// MessageText returns the transcript text of messageID and whether the bot
// sent it. It fails with agent.ErrorNotFound when the message is not in the
// chat's history.
func (store *SubagentStore) MessageText(ctx context.Context, key agent.Key, messageID identity.MessageID) (text string, fromBot bool, err error) {
	if err := key.Validate(); err != nil || messageID.IsZero() {
		return "", false, agent.NewError(agent.ErrorInvalidArgument, "read message text", errors.New("chat and message are required"))
	}
	var role int64
	err = store.read.QueryRowContext(ctx, `SELECT content_text, role FROM history_entries
	  WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND message_id = ?`,
		key.TenantID.String(), key.AccountID.String(), key.ChatID.String(), messageID.String()).Scan(&text, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, agent.NewError(agent.ErrorNotFound, "read message text", err)
	}
	if err != nil {
		return "", false, storageError("read message text", err)
	}
	return text, agent.HistoryRole(role) == agent.HistoryAssistant, nil
}
