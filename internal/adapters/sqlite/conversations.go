package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	_ "modernc.org/sqlite"
)

const transcriptPageSize = 100

type ConversationReader struct {
	dataRoot string
}

func NewConversationReader(dataRoot string) (*ConversationReader, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create transcript reader", errors.New("data root is required"))
	}
	absolute, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create transcript reader", errors.New("data root is invalid"))
	}
	return &ConversationReader{dataRoot: filepath.Clean(absolute)}, nil
}

func (reader *ConversationReader) ListBotConversations(ctx context.Context, scope control.SessionScope, limit uint32) ([]control.BotConversation, error) {
	if err := validateTranscriptScope(scope); err != nil {
		return nil, err
	}
	if limit == 0 || limit > transcriptPageSize {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "list bot conversations", errors.New("conversation list limit is invalid"))
	}
	db, exists, err := reader.open(ctx, scope)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []control.BotConversation{}, nil
	}
	defer db.Close()
	groupNameColumn, err := hasGroupNameColumn(ctx, db)
	if err != nil {
		return nil, err
	}
	groupNameExpression := "''"
	if groupNameColumn {
		groupNameExpression = "COALESCE(c.group_name, '')"
	}

	rows, err := db.QueryContext(ctx, `WITH visible_history AS (
	    SELECT h.sequence, h.chat_id, h.message_id, h.sender_name, h.content_text, h.role, h.created_at_ms
	    FROM history_entries h
	    LEFT JOIN history_resets r ON r.tenant_id = h.tenant_id AND r.account_id = h.account_id AND r.chat_id = h.chat_id
	    WHERE h.tenant_id = ? AND h.account_id = ? AND h.role IN (?, ?)
	      AND h.sequence > COALESCE(r.cutoff_sequence, 0)
	), latest AS (
	    SELECT chat_id, MAX(sequence) AS last_sequence, COUNT(*) AS message_count
	    FROM visible_history GROUP BY chat_id
	)
	SELECT c.id, c.kind, COALESCE(c.provider_address, ''), `+groupNameExpression+`,
	       COALESCE((SELECT v.sender_name FROM visible_history v WHERE v.chat_id = l.chat_id AND v.role = ? ORDER BY v.sequence DESC LIMIT 1), ''),
	       CASE WHEN EXISTS (
	           SELECT 1 FROM typed_effects e
	           WHERE e.tenant_id = c.tenant_id AND e.account_id = c.account_id AND e.chat_id = c.id
	             AND e.target_message_id = last_entry.message_id AND e.effect_kind = ? AND e.state = ?
	       ) THEN 'Message deleted on WhatsApp' ELSE last_entry.content_text END,
	       last_entry.role, last_entry.created_at_ms, l.message_count
	FROM latest l
	JOIN visible_history last_entry ON last_entry.chat_id = l.chat_id AND last_entry.sequence = l.last_sequence
	JOIN chats c ON c.tenant_id = ? AND c.account_id = ? AND c.id = l.chat_id
	ORDER BY l.last_sequence DESC LIMIT ?`,
		scope.TenantID.String(), scope.AccountID.String(), uint8(agent.HistoryUser), uint8(agent.HistoryAssistant),
		uint8(agent.HistoryUser), uint8(effect.KindDeleteMessage), uint8(effect.StateSucceeded),
		scope.TenantID.String(), scope.AccountID.String(), limit,
	)
	if err != nil {
		return nil, transcriptStorageError("query conversations", err)
	}
	defer rows.Close()

	result := make([]control.BotConversation, 0, limit)
	for rows.Next() {
		var chatValue string
		var kind uint8
		var address, groupName, senderName, lastMessage string
		var role uint8
		var createdAtMS, count int64
		if err := rows.Scan(&chatValue, &kind, &address, &groupName, &senderName, &lastMessage, &role, &createdAtMS, &count); err != nil {
			return nil, transcriptStorageError("scan conversation", err)
		}
		chatID, err := identity.ParseChatID(chatValue)
		if err != nil || count < 0 {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode conversation", errors.New("stored conversation metadata is invalid"))
		}
		result = append(result, control.BotConversation{
			ID: chatID, Kind: transcriptChatKind(kind), Name: transcriptChatName(kind, address, groupName, senderName),
			LastMessage: transcriptPreview(lastMessage), LastMessageAt: time.UnixMilli(createdAtMS).UTC().Format(time.RFC3339Nano),
			LastFromBot: agent.HistoryRole(role) == agent.HistoryAssistant, MessageCount: uint64(count),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, transcriptStorageError("iterate conversations", err)
	}
	return result, nil
}

func (reader *ConversationReader) ListBotMessages(ctx context.Context, scope control.SessionScope, chatID identity.ChatID, limit uint32) ([]control.BotMessage, error) {
	if err := validateTranscriptScope(scope); err != nil {
		return nil, err
	}
	if chatID.IsZero() || limit == 0 || limit > transcriptPageSize {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "list bot messages", errors.New("chat ID or message limit is invalid"))
	}
	db, exists, err := reader.open(ctx, scope)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []control.BotMessage{}, nil
	}
	defer db.Close()
	store := &Store{db: db, clock: agent.SystemClock{}}
	key := agent.Key{TenantID: scope.TenantID, AccountID: scope.AccountID, ChatID: chatID}
	configSnapshot, err := store.Configs().Load(ctx, key)
	var page agent.HistoryPage
	if agent.IsCode(err, agent.ErrorNotFound) {
		page, err = store.History().listForTranscript(ctx, key, agent.HistoryQuery{Limit: limit})
	} else if err != nil {
		return nil, transcriptStorageError("load conversation config", err)
	} else {
		page, err = store.History().ListIfConfigVersion(ctx, key, configSnapshot.Version, agent.HistoryQuery{Limit: limit})
		if agent.IsCode(err, agent.ErrorConflict) {
			// A settings or permission update can advance the version between
			// Load and ListIfConfigVersion. Refresh once so transcript polling
			// does not blank the chat during that short race.
			configSnapshot, err = store.Configs().Load(ctx, key)
			if agent.IsCode(err, agent.ErrorNotFound) {
				page, err = store.History().listForTranscript(ctx, key, agent.HistoryQuery{Limit: limit})
			} else if err == nil {
				page, err = store.History().ListIfConfigVersion(ctx, key, configSnapshot.Version, agent.HistoryQuery{Limit: limit})
			}
		}
	}
	if err != nil {
		return nil, transcriptStorageError("read conversation history", err)
	}
	result := make([]control.BotMessage, 0, len(page.Entries))
	for _, entry := range page.Entries {
		if len(entry.Content) != 1 {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode conversation message", errors.New("stored message content is invalid"))
		}
		text, ok := entry.Content[0].(agent.TextPart)
		if !ok {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "decode conversation message", errors.New("stored message format is unsupported"))
		}
		message := control.BotMessage{
			ID: entry.MessageID, Content: text.Text,
			CreatedAt: entry.CreatedAt.UTC().Format(time.RFC3339Nano),
			Delivery:  transcriptDelivery(entry.Delivery),
		}
		switch entry.Role {
		case agent.HistoryUser:
			message.Role = "user"
			message.Sender = "Contact"
			if entry.Sender != nil && strings.TrimSpace(entry.Sender.DisplayName) != "" {
				message.Sender = entry.Sender.DisplayName
			}
		case agent.HistoryAssistant:
			message.Role, message.Sender = "assistant", "Bot"
		default:
			continue
		}
		result = append(result, message)
	}
	deletedIDs, err := deletedMessageIDs(ctx, db, scope, chatID)
	if err != nil {
		return nil, err
	}
	for index := range result {
		if _, deleted := deletedIDs[result[index].ID.String()]; deleted {
			result[index].Deleted = true
			result[index].Content = "This message was deleted on WhatsApp."
		}
	}
	return result, nil
}

func deletedMessageIDs(ctx context.Context, db *sql.DB, scope control.SessionScope, chatID identity.ChatID) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT target_message_id FROM typed_effects
	    WHERE tenant_id = ? AND account_id = ? AND chat_id = ? AND effect_kind = ? AND state = ?
	      AND target_message_id IS NOT NULL`,
		scope.TenantID.String(), scope.AccountID.String(), chatID.String(),
		uint8(effect.KindDeleteMessage), uint8(effect.StateSucceeded),
	)
	if err != nil {
		return nil, transcriptStorageError("query deleted message markers", err)
	}
	defer rows.Close()
	result := make(map[string]struct{})
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			return nil, transcriptStorageError("scan deleted message marker", err)
		}
		result[messageID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, transcriptStorageError("iterate deleted message markers", err)
	}
	return result, nil
}

func (reader *ConversationReader) open(ctx context.Context, scope control.SessionScope) (*sql.DB, bool, error) {
	if reader == nil || strings.TrimSpace(reader.dataRoot) == "" {
		return nil, false, agent.NewError(agent.ErrorUnavailable, "open transcript database", errors.New("transcript reader is unavailable"))
	}
	path := filepath.Join(reader.dataRoot, "tenants", scope.TenantID.String(), "app.db")
	absolute, err := safeDatabasePath(path)
	if err != nil {
		return nil, false, agent.NewError(agent.ErrorInvalidArgument, "open transcript database", errors.New("transcript database path is invalid"))
	}
	if _, err := os.Stat(absolute); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, agent.NewError(agent.ErrorStorageFailure, "inspect transcript database", errors.New("transcript database is unavailable"))
	}
	db, err := sql.Open("sqlite", readOnlyDatabaseDSN(absolute, defaultBusyTimeoutMS))
	if err != nil {
		return nil, false, agent.NewError(agent.ErrorStorageFailure, "open transcript database", errors.New("transcript database is unavailable"))
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, false, agent.NewError(agent.ErrorStorageFailure, "open transcript database", errors.New("transcript database is unavailable"))
	}
	return db, true, nil
}

func readOnlyDatabaseDSN(path string, busyTimeoutMS int) string {
	query := make(url.Values)
	query.Set("mode", "ro")
	query.Set("_busy_timeout", strconv.Itoa(busyTimeoutMS))
	query.Set("_foreign_keys", "on")
	return "file:" + filepath.ToSlash(path) + "?" + query.Encode()
}

func validateTranscriptScope(scope control.SessionScope) error {
	if scope.TenantID.IsZero() || scope.AccountID.IsZero() {
		return agent.NewError(agent.ErrorInvalidArgument, "validate transcript scope", errors.New("tenant and account IDs are required"))
	}
	return nil
}

func transcriptChatKind(kind uint8) string {
	switch conversation.ChatKind(kind) {
	case conversation.ChatDirect:
		return "direct"
	case conversation.ChatGroup:
		return "group"
	case conversation.ChatStatus:
		return "status"
	default:
		return "chat"
	}
}

func transcriptChatName(kind uint8, address, groupName, senderName string) string {
	switch conversation.ChatKind(kind) {
	case conversation.ChatDirect:
		if name := strings.TrimSpace(senderName); name != "" {
			return name
		}
		if address != "" {
			return address
		}
		return "Contact"
	case conversation.ChatGroup:
		if name := strings.TrimSpace(groupName); name != "" {
			return name
		}
		return "Group"
	case conversation.ChatStatus:
		return "WhatsApp Status"
	default:
		return "Conversation"
	}
}

func hasGroupNameColumn(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(chats)`)
	if err != nil {
		return false, transcriptStorageError("inspect conversation schema", err)
	}
	defer rows.Close()
	for rows.Next() {
		var columnID, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&columnID, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, transcriptStorageError("inspect conversation schema", err)
		}
		if name == "group_name" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, transcriptStorageError("inspect conversation schema", err)
	}
	return false, nil
}

func transcriptDelivery(delivery agent.DeliveryStatus) string {
	switch delivery {
	case agent.DeliverySucceeded:
		return "sent"
	case agent.DeliveryPending:
		return "pending"
	case agent.DeliveryFailedTerminal:
		return "failed"
	case agent.DeliveryUnknownOutcome:
		return "unknown"
	default:
		return "none"
	}
}

func transcriptPreview(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if !utf8.ValidString(text) {
		return "Message cannot be displayed"
	}
	runes := []rune(text)
	if len(runes) > 140 {
		return string(runes[:137]) + "…"
	}
	return text
}

func transcriptStorageError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return agent.NewError(agent.ErrorStorageFailure, operation, errors.New("stored conversation data could not be read"))
}

var _ control.ConversationRepository = (*ConversationReader)(nil)
