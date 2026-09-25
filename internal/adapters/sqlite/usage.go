package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
)

const dashboardGroupLimit = 5

// ConversationUsage summarizes the active account's visible saved history. It
// reads the transcript database in read-only mode and scopes every query to the
// tenant/account supplied by the session controller.
func (reader *ConversationReader) ConversationUsage(ctx context.Context, scope control.SessionScope, periodDays uint32) (control.BotConversationUsage, error) {
	if err := validateTranscriptScope(scope); err != nil {
		return control.BotConversationUsage{}, err
	}
	if periodDays != 1 && periodDays != 7 && periodDays != 30 {
		return control.BotConversationUsage{}, agent.NewError(agent.ErrorInvalidArgument, "read conversation usage", errors.New("period must be 1, 7, or 30 days"))
	}
	usage, since := emptyConversationUsage(time.Now(), periodDays)
	db, exists, err := reader.open(ctx, scope)
	if err != nil {
		return control.BotConversationUsage{}, err
	}
	if !exists {
		return usage, nil
	}
	defer db.Close()

	groupNameColumn, err := hasGroupNameColumn(ctx, db)
	if err != nil {
		return control.BotConversationUsage{}, err
	}
	groupName := "''"
	if groupNameColumn {
		groupName = "COALESCE(NULLIF(TRIM(c.group_name), ''), '')"
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return control.BotConversationUsage{}, transcriptStorageError("begin conversation usage snapshot", err)
	}
	defer tx.Rollback()

	const visibleHistory = `WITH visible_history AS (
		SELECT h.chat_id, h.created_at_ms, h.invocation_id, h.role,
		       EXISTS (
		           SELECT 1 FROM inbound_events e
		           WHERE e.tenant_id = h.tenant_id AND e.account_id = h.account_id
		             AND e.chat_id = h.chat_id AND e.invocation_id = h.invocation_id
		             AND e.invocation_digest IS NOT NULL
	       ) AS was_invoked
		FROM history_entries h
		LEFT JOIN history_resets r ON r.tenant_id = h.tenant_id AND r.account_id = h.account_id AND r.chat_id = h.chat_id
		WHERE h.tenant_id = ? AND h.account_id = ? AND h.role IN (?, ?)
		  AND h.sequence > COALESCE(r.cutoff_sequence, 0)
	)`
	conversationRows, err := tx.QueryContext(ctx, visibleHistory+`
		SELECT c.kind, CASE WHEN c.kind = ? THEN `+groupName+` ELSE '' END,
		       COUNT(*), COUNT(CASE WHEN h.created_at_ms >= ? THEN 1 END),
		       COUNT(DISTINCT CASE WHEN h.role = ? AND h.was_invoked THEN h.invocation_id END),
	       COUNT(DISTINCT CASE WHEN h.role = ? AND h.was_invoked AND h.created_at_ms >= ? THEN h.invocation_id END)
		FROM visible_history h
		JOIN chats c ON c.tenant_id = ? AND c.account_id = ? AND c.id = h.chat_id
		GROUP BY c.id, c.kind, `+groupName,
		scope.TenantID.String(), scope.AccountID.String(), uint8(agent.HistoryUser), uint8(agent.HistoryAssistant),
		uint8(conversation.ChatGroup), since.UnixMilli(), uint8(agent.HistoryUser), uint8(agent.HistoryUser), since.UnixMilli(),
		scope.TenantID.String(), scope.AccountID.String(),
	)
	if err != nil {
		return control.BotConversationUsage{}, transcriptStorageError("query conversation usage", err)
	}
	for conversationRows.Next() {
		var kind uint8
		var name string
		var messages, recentMessages, invocations, recentInvocations int64
		if err := conversationRows.Scan(&kind, &name, &messages, &recentMessages, &invocations, &recentInvocations); err != nil {
			conversationRows.Close()
			return control.BotConversationUsage{}, transcriptStorageError("scan conversation usage", err)
		}
		if messages < 0 || recentMessages < 0 || invocations < 0 || recentInvocations < 0 {
			conversationRows.Close()
			return control.BotConversationUsage{}, agent.NewError(agent.ErrorIntegrityFailure, "decode conversation usage", errors.New("stored conversation usage is invalid"))
		}
		usage.TotalChats++
		usage.TotalMessages += uint64(messages)
		usage.TotalInvocations += uint64(invocations)
		usage.MessagesInPeriod += uint64(recentMessages)
		usage.InvocationsInPeriod += uint64(recentInvocations)
		if recentMessages > 0 {
			usage.ActiveChatsInPeriod++
		}
		if conversation.ChatKind(kind) == conversation.ChatGroup {
			if name == "" {
				name = "Group"
			}
			usage.TotalGroups++
			if recentMessages > 0 {
				usage.ActiveGroupsInPeriod++
			}
			group := control.BotGroupUsage{
				Name: name, Messages: uint64(messages), MessagesInPeriod: uint64(recentMessages),
				Invocations: uint64(invocations), InvocationsInPeriod: uint64(recentInvocations),
			}
			usage.Groups = append(usage.Groups, group)
			usage.InvocationGroups = append(usage.InvocationGroups, group)
		}
	}
	if err := conversationRows.Err(); err != nil {
		conversationRows.Close()
		return control.BotConversationUsage{}, transcriptStorageError("iterate conversation usage", err)
	}
	if err := conversationRows.Close(); err != nil {
		return control.BotConversationUsage{}, transcriptStorageError("close conversation usage", err)
	}
	sort.SliceStable(usage.Groups, func(left, right int) bool {
		if usage.Groups[left].MessagesInPeriod != usage.Groups[right].MessagesInPeriod {
			return usage.Groups[left].MessagesInPeriod > usage.Groups[right].MessagesInPeriod
		}
		return strings.ToLower(usage.Groups[left].Name) < strings.ToLower(usage.Groups[right].Name)
	})
	if len(usage.Groups) > dashboardGroupLimit {
		usage.Groups = usage.Groups[:dashboardGroupLimit]
	}
	sort.SliceStable(usage.InvocationGroups, func(left, right int) bool {
		if usage.InvocationGroups[left].InvocationsInPeriod != usage.InvocationGroups[right].InvocationsInPeriod {
			return usage.InvocationGroups[left].InvocationsInPeriod > usage.InvocationGroups[right].InvocationsInPeriod
		}
		return strings.ToLower(usage.InvocationGroups[left].Name) < strings.ToLower(usage.InvocationGroups[right].Name)
	})
	if len(usage.InvocationGroups) > dashboardGroupLimit {
		usage.InvocationGroups = usage.InvocationGroups[:dashboardGroupLimit]
	}

	dailyRows, err := tx.QueryContext(ctx, visibleHistory+`
		SELECT date(created_at_ms / 1000, 'unixepoch', 'localtime'), COUNT(*),
		       COUNT(DISTINCT CASE WHEN role = ? AND was_invoked THEN invocation_id END)
		FROM visible_history
		WHERE created_at_ms >= ?
		GROUP BY date(created_at_ms / 1000, 'unixepoch', 'localtime')`,
		scope.TenantID.String(), scope.AccountID.String(), uint8(agent.HistoryUser), uint8(agent.HistoryAssistant),
		uint8(agent.HistoryUser), since.UnixMilli(),
	)
	if err != nil {
		return control.BotConversationUsage{}, transcriptStorageError("query daily message activity", err)
	}
	type dailyCounts struct {
		messages    uint64
		invocations uint64
	}
	dailyByDate := make(map[string]dailyCounts, len(usage.DailyActivity))
	for dailyRows.Next() {
		var date string
		var messages, invocations int64
		if err := dailyRows.Scan(&date, &messages, &invocations); err != nil {
			dailyRows.Close()
			return control.BotConversationUsage{}, transcriptStorageError("scan daily message activity", err)
		}
		if messages < 0 || invocations < 0 {
			dailyRows.Close()
			return control.BotConversationUsage{}, agent.NewError(agent.ErrorIntegrityFailure, "decode daily message activity", errors.New("stored daily activity is invalid"))
		}
		dailyByDate[date] = dailyCounts{messages: uint64(messages), invocations: uint64(invocations)}
	}
	if err := dailyRows.Err(); err != nil {
		dailyRows.Close()
		return control.BotConversationUsage{}, transcriptStorageError("iterate daily message activity", err)
	}
	if err := dailyRows.Close(); err != nil {
		return control.BotConversationUsage{}, transcriptStorageError("close daily message activity", err)
	}
	for index := range usage.DailyActivity {
		counts := dailyByDate[usage.DailyActivity[index].Date]
		usage.DailyActivity[index].Messages = counts.messages
		usage.DailyActivity[index].Invocations = counts.invocations
	}
	if err := tx.Commit(); err != nil {
		return control.BotConversationUsage{}, transcriptStorageError("finish conversation usage snapshot", err)
	}
	return usage, nil
}

func emptyConversationUsage(now time.Time, periodDays uint32) (control.BotConversationUsage, time.Time) {
	location := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	start := today.AddDate(0, 0, 1-int(periodDays))
	usage := control.BotConversationUsage{
		Groups:           make([]control.BotGroupUsage, 0),
		InvocationGroups: make([]control.BotGroupUsage, 0),
		DailyActivity:    make([]control.BotDailyActivity, 0, periodDays),
		PeriodStart:      start.Format("2006-01-02"),
		PeriodDays:       periodDays,
	}
	for day := start; !day.After(today); day = day.AddDate(0, 0, 1) {
		usage.DailyActivity = append(usage.DailyActivity, control.BotDailyActivity{Date: day.Format("2006-01-02")})
	}
	return usage, start
}

var _ control.ConversationRepository = (*ConversationReader)(nil)
