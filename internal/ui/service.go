package ui

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	agentapp "github.com/Chomosuke9/DiscordAgent-Go/internal/app"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/config"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/control"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/observability"
)

const AppName = "DiscordAgent"

// AppInfo is the small, stable information contract used by the first GUI binding.
type AppInfo struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

type LogEntryDTO struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Details string `json:"details"`
	ID      uint64 `json:"id"`
	HasFull bool   `json:"hasFull"`
}

type DiscordConversationDTO struct {
	ID                  string              `json:"id"`
	Kind                string              `json:"kind"`
	Name                string              `json:"name"`
	LastMessage         string              `json:"lastMessage"`
	LastMessageAt       string              `json:"lastMessageAt"`
	LastFromBot         bool                `json:"lastFromBot"`
	MessageCount        uint64              `json:"messageCount"`
	LastMessageMentions []DiscordMentionDTO `json:"lastMessageMentions"`
}

type DiscordUsageDTO struct {
	TotalMessages        uint64                 `json:"totalMessages"`
	TotalInvocations     uint64                 `json:"totalInvocations"`
	TotalChats           uint64                 `json:"totalChats"`
	MessagesInPeriod     uint64                 `json:"messagesInPeriod"`
	InvocationsInPeriod  uint64                 `json:"invocationsInPeriod"`
	ActiveChatsInPeriod  uint64                 `json:"activeChatsInPeriod"`
	TotalGroups          uint64                 `json:"totalGroups"`
	ActiveGroupsInPeriod uint64                 `json:"activeGroupsInPeriod"`
	PeriodStart          string                 `json:"periodStart"`
	PeriodDays           uint32                 `json:"periodDays"`
	Groups               []DiscordGroupUsageDTO `json:"groups"`
	InvocationGroups     []DiscordGroupUsageDTO `json:"invocationGroups"`
	DailyActivity        []DiscordDailyUsageDTO `json:"dailyActivity"`
}

type DiscordGroupUsageDTO struct {
	Name                string `json:"name"`
	Messages            uint64 `json:"messages"`
	MessagesInPeriod    uint64 `json:"messagesInPeriod"`
	Invocations         uint64 `json:"invocations"`
	InvocationsInPeriod uint64 `json:"invocationsInPeriod"`
}

type DiscordDailyUsageDTO struct {
	Date        string `json:"date"`
	Messages    uint64 `json:"messages"`
	Invocations uint64 `json:"invocations"`
}

type DiscordMentionDTO struct {
	Token       string `json:"token"`
	SenderRef   string `json:"senderRef"`
	DisplayName string `json:"displayName"`
	Bot         bool   `json:"bot"`
}

type DiscordQuoteDTO struct {
	MessageID    string              `json:"messageID"`
	Role         string              `json:"role"`
	Sender       string              `json:"sender"`
	Content      string              `json:"content"`
	IsAdmin      bool                `json:"isAdmin"`
	IsSuperAdmin bool                `json:"isSuperAdmin"`
	Mentions     []DiscordMentionDTO `json:"mentions"`
}

type DiscordMessageDTO struct {
	ID           string              `json:"id"`
	Role         string              `json:"role"`
	Sender       string              `json:"sender"`
	SenderRef    string              `json:"senderRef"`
	Content      string              `json:"content"`
	IsAdmin      bool                `json:"isAdmin"`
	IsSuperAdmin bool                `json:"isSuperAdmin"`
	CreatedAt    string              `json:"createdAt"`
	Delivery     string              `json:"delivery"`
	Deleted      bool                `json:"deleted"`
	Mentions     []DiscordMentionDTO `json:"mentions"`
	Quote        *DiscordQuoteDTO    `json:"quote"`
}

type DiscordGroupMemberDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	IsAdmin      bool   `json:"isAdmin"`
	IsSuperAdmin bool   `json:"isSuperAdmin"`
	CanKick      bool   `json:"canKick"`
}

type DiscordGroupMembersDTO struct {
	BotIsAdmin bool                    `json:"botIsAdmin"`
	Members    []DiscordGroupMemberDTO `json:"members"`
}

type DiscordBroadcastGroupDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SendDiscordBroadcastRequestDTO struct {
	GroupIDs          []string `json:"groupIDs"`
	Format            string   `json:"format"`
	Payload           string   `json:"payload"`
	BatchSize         int      `json:"batchSize"`
	BatchDelaySeconds int      `json:"batchDelaySeconds"`
}

type ScheduleDiscordBroadcastRequestDTO struct {
	GroupIDs          []string `json:"groupIDs"`
	Format            string   `json:"format"`
	Payload           string   `json:"payload"`
	BatchSize         int      `json:"batchSize"`
	BatchDelaySeconds int      `json:"batchDelaySeconds"`
	ScheduledAt       string   `json:"scheduledAt"`
}

type DiscordBroadcastGroupResultDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Sent      bool   `json:"sent"`
	ErrorCode string `json:"errorCode"`
}

type DiscordBroadcastScheduleResultDTO struct {
	Name      string `json:"name"`
	Sent      bool   `json:"sent"`
	ErrorCode string `json:"errorCode"`
}

type DiscordBroadcastScheduleDTO struct {
	ID                string                              `json:"id"`
	ScheduledAt       string                              `json:"scheduledAt"`
	BatchSize         int                                 `json:"batchSize"`
	BatchDelaySeconds int                                 `json:"batchDelaySeconds"`
	GroupCount        int                                 `json:"groupCount"`
	Status            string                              `json:"status"`
	Results           []DiscordBroadcastScheduleResultDTO `json:"results"`
}

type DiscordChatSettingsDTO struct {
	Version            string `json:"version"`
	ModerationLevel    uint8  `json:"moderationLevel"`
	PromptOverrideMode string `json:"promptOverrideMode"`
	PromptOverrideText string `json:"promptOverrideText"`
	TriggerMention     bool   `json:"triggerMention"`
	TriggerName        bool   `json:"triggerName"`
	TriggerReply       bool   `json:"triggerReply"`
	TriggerNameRegex   bool   `json:"triggerNameRegex"`
	TriggerNamePattern string `json:"triggerNamePattern"`
	TriggerSmart       bool   `json:"triggerSmart"`
	TriggerSmartRules  string `json:"triggerSmartRules"`
}

// DiscordChatTaskDTO is a chat's scheduled (one-off) or daily task.
type DiscordChatTaskDTO struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	// NextRun is RFC 3339 with the bot's UTC offset.
	NextRun string `json:"nextRun"`
	Daily   bool   `json:"daily"`
}

// AddDiscordChatTaskRequestDTO adds a task that runs once at RunAt (RFC
// 3339, within 24 hours) or, when Daily, every day at Time ("HH:MM" in the
// bot's time zone).
type AddDiscordChatTaskRequestDTO struct {
	ChatID string `json:"chatID"`
	Prompt string `json:"prompt"`
	Daily  bool   `json:"daily"`
	RunAt  string `json:"runAt"`
	Time   string `json:"time"`
}

type SaveDiscordChatSettingsRequestDTO struct {
	ChatID             string `json:"chatID"`
	ExpectedVersion    string `json:"expectedVersion"`
	ModerationLevel    uint8  `json:"moderationLevel"`
	PromptOverrideMode string `json:"promptOverrideMode"`
	PromptOverrideText string `json:"promptOverrideText"`
	TriggerMention     bool   `json:"triggerMention"`
	TriggerName        bool   `json:"triggerName"`
	TriggerReply       bool   `json:"triggerReply"`
	TriggerNameRegex   bool   `json:"triggerNameRegex"`
	TriggerNamePattern string `json:"triggerNamePattern"`
	TriggerSmart       bool   `json:"triggerSmart"`
	TriggerSmartRules  string `json:"triggerSmartRules"`
}

type ResetDiscordChatSettingsRequestDTO struct {
	ExpectedSettingsRevision string `json:"expectedSettingsRevision"`
	Category                 string `json:"category"`
}

type ResetDiscordChatSettingsResultDTO struct {
	ChangedChats int64 `json:"changedChats"`
}

const chatActionTimeout = 45 * time.Second

// AppService exposes the stable application and settings bindings. Runtime and
// session operations are added only when their controller owns the lifecycle.
type AppService struct {
	version       string
	control       *control.Controller
	sessions      *control.SessionController
	agent         *control.AgentController
	conversations *control.ConversationController
	dataRoot      string
	logs          *observability.LogBuffer
}

// Options wires the controllers a transport exposes. Nil controllers are
// allowed; the matching operations then report that they are not ready.
type Options struct {
	Version       string
	Settings      *control.Controller
	Sessions      *control.SessionController
	Agent         *control.AgentController
	Conversations *control.ConversationController
	// DataRoot is the effective leased root shown in the public settings view.
	// Changing the root is a separate data operation and is intentionally not
	// exposed through SaveSettings.
	DataRoot string
	Logs     *observability.LogBuffer
}

// NewAppService builds the transport-neutral UI service shared by the Wails
// bindings and the browser HTTP bridge.
func NewAppService(opts Options) *AppService {
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	return &AppService{version: version, control: opts.Settings, sessions: opts.Sessions, agent: opts.Agent, conversations: opts.Conversations, dataRoot: opts.DataRoot, logs: opts.Logs}
}

func (s *AppService) GetAppInfo() AppInfo {
	return AppInfo{
		Name:     AppName,
		Version:  s.version,
		Platform: runtime.GOOS,
	}
}

// GetLogs returns the newest application events, including the warnings and
// errors kept from earlier runs. HasFull marks entries whose full error text
// GetLogDetails can return.
func (s *AppService) GetLogs() []LogEntryDTO {
	if s == nil || s.logs == nil {
		return nil
	}
	entries := s.logs.Entries()
	result := make([]LogEntryDTO, len(entries))
	for index, entry := range entries {
		result[index] = LogEntryDTO{Time: entry.Time, Level: entry.Level, Message: entry.Message, Details: entry.Details, ID: entry.ID, HasFull: entry.Full != ""}
	}
	return result
}

// GetLogDetails returns the full text of one warning or error: every logged
// field and the whole underlying error chain.
func (s *AppService) GetLogDetails(id uint64) (string, error) {
	if s == nil || s.logs == nil {
		return "", errors.New("app log is not available")
	}
	full, ok := s.logs.Full(id)
	if !ok {
		return "", agent.NewError(agent.ErrorNotFound, "read log details", errors.New("this log entry is no longer kept"))
	}
	return full, nil
}

func (s *AppService) GetDiscordConversations() ([]DiscordConversationDTO, error) {
	if s.conversations == nil {
		return nil, errors.New("conversation controller is not initialized")
	}
	conversations, err := s.conversations.List(context.Background())
	if err != nil {
		return nil, err
	}
	result := make([]DiscordConversationDTO, len(conversations))
	for index, item := range conversations {
		result[index] = DiscordConversationDTO{
			ID: item.ID.String(), Kind: item.Kind, Name: item.Name, LastMessage: item.LastMessage,
			LastMessageAt: item.LastMessageAt, LastFromBot: item.LastFromBot, MessageCount: item.MessageCount,
			LastMessageMentions: discordMentions(item.LastMessageMentions),
		}
	}
	return result, nil
}

func (s *AppService) GetDiscordUsage(periodDays uint32) (DiscordUsageDTO, error) {
	if s.conversations == nil {
		return DiscordUsageDTO{}, errors.New("conversation controller is not initialized")
	}
	usage, err := s.conversations.Usage(context.Background(), periodDays)
	if err != nil {
		return DiscordUsageDTO{}, err
	}
	dto := DiscordUsageDTO{
		TotalMessages: usage.TotalMessages, TotalInvocations: usage.TotalInvocations, TotalChats: usage.TotalChats,
		MessagesInPeriod: usage.MessagesInPeriod, InvocationsInPeriod: usage.InvocationsInPeriod,
		ActiveChatsInPeriod: usage.ActiveChatsInPeriod,
		TotalGroups:         usage.TotalGroups, ActiveGroupsInPeriod: usage.ActiveGroupsInPeriod,
		PeriodStart: usage.PeriodStart, PeriodDays: usage.PeriodDays,
		Groups:           make([]DiscordGroupUsageDTO, len(usage.Groups)),
		InvocationGroups: make([]DiscordGroupUsageDTO, len(usage.InvocationGroups)),
		DailyActivity:    make([]DiscordDailyUsageDTO, len(usage.DailyActivity)),
	}
	for index, group := range usage.Groups {
		dto.Groups[index] = discordGroupUsage(group)
	}
	for index, group := range usage.InvocationGroups {
		dto.InvocationGroups[index] = discordGroupUsage(group)
	}
	for index, day := range usage.DailyActivity {
		dto.DailyActivity[index] = DiscordDailyUsageDTO{Date: day.Date, Messages: day.Messages, Invocations: day.Invocations}
	}
	return dto, nil
}

func discordGroupUsage(group control.BotGroupUsage) DiscordGroupUsageDTO {
	return DiscordGroupUsageDTO{
		Name: group.Name, Messages: group.Messages, MessagesInPeriod: group.MessagesInPeriod,
		Invocations: group.Invocations, InvocationsInPeriod: group.InvocationsInPeriod,
	}
}

func (s *AppService) GetDiscordMessages(chatID string) ([]DiscordMessageDTO, error) {
	if s.conversations == nil {
		return nil, errors.New("conversation controller is not initialized")
	}
	messages, err := s.conversations.Messages(context.Background(), chatID)
	if err != nil {
		return nil, err
	}
	result := make([]DiscordMessageDTO, len(messages))
	for index, item := range messages {
		result[index] = discordMessage(item)
	}
	return result, nil
}

func discordMessage(item control.BotMessage) DiscordMessageDTO {
	result := DiscordMessageDTO{
		ID: item.ID.String(), Role: item.Role, Sender: item.Sender, Content: item.Content,
		IsAdmin: item.IsAdmin, IsSuperAdmin: item.IsSuperAdmin,
		CreatedAt: item.CreatedAt, Delivery: item.Delivery, Deleted: item.Deleted,
		Mentions: discordMentions(item.Mentions),
	}
	if !item.SenderRef.IsZero() {
		result.SenderRef = item.SenderRef.String()
	}
	if item.Quote != nil {
		result.Quote = &DiscordQuoteDTO{
			MessageID: item.Quote.MessageID.String(), Role: item.Quote.Role,
			Sender: item.Quote.Sender, Content: item.Quote.Content,
			IsAdmin: item.Quote.IsAdmin, IsSuperAdmin: item.Quote.IsSuperAdmin,
			Mentions: discordMentions(item.Quote.Mentions),
		}
	}
	return result
}

func discordMentions(mentions []control.BotMention) []DiscordMentionDTO {
	if len(mentions) == 0 {
		return nil
	}
	result := make([]DiscordMentionDTO, len(mentions))
	for index, mention := range mentions {
		result[index] = DiscordMentionDTO{
			Token: mention.Token, DisplayName: mention.DisplayName, Bot: mention.Bot,
		}
		if !mention.SenderRef.IsZero() {
			result[index].SenderRef = mention.SenderRef.String()
		}
	}
	return result
}

func (s *AppService) GetDiscordGroupMembers(chatID string) (DiscordGroupMembersDTO, error) {
	var group control.AgentGroupMembers
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		group, actionErr = runtime.ListGroupMembers(ctx, chatID)
		return actionErr
	})
	if err != nil {
		return DiscordGroupMembersDTO{}, err
	}
	result := DiscordGroupMembersDTO{BotIsAdmin: group.BotIsAdmin, Members: make([]DiscordGroupMemberDTO, len(group.Members))}
	for index, member := range group.Members {
		result.Members[index] = DiscordGroupMemberDTO{
			ID: member.ID, Name: member.Name, IsAdmin: member.IsAdmin,
			IsSuperAdmin: member.IsSuperAdmin, CanKick: member.CanKick,
		}
	}
	return result, nil
}

func (s *AppService) GetDiscordBroadcastGroups() ([]DiscordBroadcastGroupDTO, error) {
	var groups []control.AgentBroadcastGroup
	err := s.withBroadcastActions(chatActionTimeout, func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		groups, actionErr = runtime.ListBroadcastGroups(ctx)
		return actionErr
	})
	if err != nil {
		return nil, err
	}
	result := make([]DiscordBroadcastGroupDTO, len(groups))
	for index, group := range groups {
		result[index] = DiscordBroadcastGroupDTO{ID: group.ID, Name: group.Name}
	}
	return result, nil
}

func (s *AppService) NormalizeDiscordBroadcastPayload(payload string) (string, error) {
	return agentapp.NormalizeDiscordBroadcastPayload(payload)
}

func (s *AppService) SendDiscordBroadcast(request SendDiscordBroadcastRequestDTO) ([]DiscordBroadcastGroupResultDTO, error) {
	format := request.Format
	if format != "text" && format != "payload" {
		format = "invalid"
	}
	s.recordChatActionDetails("INFO", "Discord broadcast send started", fmt.Sprintf("channels=%d · format=%s · batch_size=%d · pause_seconds=%d", len(request.GroupIDs), format, request.BatchSize, request.BatchDelaySeconds))
	var results []control.AgentBroadcastGroupResult
	err := s.withBroadcastActions(broadcastActionTimeout(len(request.GroupIDs), request.BatchSize, request.BatchDelaySeconds), func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		results, actionErr = runtime.BroadcastDiscordGroups(ctx, request.GroupIDs, request.Format, request.Payload, request.BatchSize, request.BatchDelaySeconds)
		return actionErr
	})
	if err != nil {
		return nil, err
	}
	result := make([]DiscordBroadcastGroupResultDTO, len(results))
	sent := 0
	for index, item := range results {
		result[index] = DiscordBroadcastGroupResultDTO{ID: item.ID, Name: item.Name, Sent: item.Sent, ErrorCode: item.ErrorCode}
		if item.Sent {
			sent++
		}
	}
	level, message := "INFO", "Discord broadcast completed"
	if sent != len(results) {
		level, message = "WARN", "Discord broadcast completed with delivery failures"
	}
	s.recordChatActionDetails(level, message, broadcastResultLogDetails(results, sent))
	return result, nil
}

func broadcastResultLogDetails(results []control.AgentBroadcastGroupResult, sent int) string {
	failedByCode := make(map[string]int)
	for _, item := range results {
		if item.Sent {
			continue
		}
		code := item.ErrorCode
		if code == "" {
			code = "unknown"
		}
		failedByCode[code]++
	}
	details := fmt.Sprintf("channels=%d · sent=%d · failed=%d", len(results), sent, len(results)-sent)
	if len(failedByCode) == 0 {
		return details
	}
	codes := make([]string, 0, len(failedByCode))
	for code := range failedByCode {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	counts := make([]string, 0, len(codes))
	for _, code := range codes {
		counts = append(counts, fmt.Sprintf("%s:%d", code, failedByCode[code]))
	}
	return details + " · error_codes=" + strings.Join(counts, ",")
}

func (s *AppService) ScheduleDiscordBroadcast(request ScheduleDiscordBroadcastRequestDTO) (DiscordBroadcastScheduleDTO, error) {
	scheduledAt, err := time.Parse(time.RFC3339, request.ScheduledAt)
	if err != nil {
		return DiscordBroadcastScheduleDTO{}, errors.New("Scheduled time is invalid.")
	}
	var schedule control.AgentBroadcastSchedule
	err = s.withBroadcastActions(chatActionTimeout, func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		schedule, actionErr = runtime.ScheduleDiscordBroadcast(ctx, request.GroupIDs, request.Format, request.Payload, request.BatchSize, request.BatchDelaySeconds, scheduledAt)
		return actionErr
	})
	if err != nil {
		return DiscordBroadcastScheduleDTO{}, err
	}
	s.recordChatAction("INFO", "Discord broadcast scheduled", nil)
	return broadcastScheduleDTO(schedule), nil
}

func (s *AppService) GetDiscordBroadcastSchedules() ([]DiscordBroadcastScheduleDTO, error) {
	var schedules []control.AgentBroadcastSchedule
	err := s.withBroadcastActions(chatActionTimeout, func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		schedules, actionErr = runtime.ListDiscordBroadcastSchedules(ctx)
		return actionErr
	})
	if err != nil {
		return nil, err
	}
	result := make([]DiscordBroadcastScheduleDTO, len(schedules))
	for index, schedule := range schedules {
		result[index] = broadcastScheduleDTO(schedule)
	}
	return result, nil
}

func (s *AppService) CancelDiscordBroadcastSchedule(id string) error {
	err := s.withBroadcastActions(chatActionTimeout, func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		return runtime.CancelDiscordBroadcastSchedule(ctx, id)
	})
	if err != nil {
		return err
	}
	s.recordChatAction("INFO", "Discord broadcast schedule cancelled", nil)
	return nil
}

func broadcastScheduleDTO(schedule control.AgentBroadcastSchedule) DiscordBroadcastScheduleDTO {
	result := DiscordBroadcastScheduleDTO{
		ID: schedule.ID, ScheduledAt: schedule.ScheduledAt.UTC().Format(time.RFC3339),
		BatchSize: schedule.BatchSize, BatchDelaySeconds: schedule.BatchDelaySeconds,
		GroupCount: schedule.GroupCount, Status: schedule.Status,
		Results: make([]DiscordBroadcastScheduleResultDTO, len(schedule.Results)),
	}
	for index, item := range schedule.Results {
		result.Results[index] = DiscordBroadcastScheduleResultDTO{Name: item.Name, Sent: item.Sent, ErrorCode: item.ErrorCode}
	}
	return result
}

func (s *AppService) GetDiscordChatSettings(chatID string) (DiscordChatSettingsDTO, error) {
	var settings control.AgentChatSettings
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		settings, actionErr = runtime.GetChatSettings(ctx, chatID)
		return actionErr
	})
	if err != nil {
		return DiscordChatSettingsDTO{}, err
	}
	s.recordChatAction("INFO", "Chat settings opened", nil)
	return chatSettingsDTO(settings), nil
}

func (s *AppService) ResetDiscordChatSettings(request ResetDiscordChatSettingsRequestDTO) (ResetDiscordChatSettingsResultDTO, error) {
	if s.control == nil {
		return ResetDiscordChatSettingsResultDTO{}, errors.New("settings controller is not initialized")
	}
	expectedRevision, err := strconv.ParseUint(request.ExpectedSettingsRevision, 10, 64)
	if err != nil || expectedRevision == 0 {
		return ResetDiscordChatSettingsResultDTO{}, errors.New("settings revision is invalid")
	}
	view, err := s.control.GetSettings(context.Background())
	if err != nil {
		return ResetDiscordChatSettingsResultDTO{}, err
	}
	if view.Revision != expectedRevision {
		return ResetDiscordChatSettingsResultDTO{}, errors.New("Saved settings changed. Reload Settings and try again.")
	}
	var changed int64
	err = s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var resetErr error
		changed, resetErr = runtime.ResetChatSettings(ctx, control.ChatSettingsResetCategory(request.Category), view.Values.ChatDefaults)
		return resetErr
	})
	if err != nil {
		return ResetDiscordChatSettingsResultDTO{}, err
	}
	s.recordChatAction("INFO", "Saved chat settings reset", nil)
	return ResetDiscordChatSettingsResultDTO{ChangedChats: changed}, nil
}

func (s *AppService) SaveDiscordChatSettings(request SaveDiscordChatSettingsRequestDTO) (DiscordChatSettingsDTO, error) {
	expectedVersion, err := strconv.ParseUint(request.ExpectedVersion, 10, 64)
	if err != nil || expectedVersion == 0 {
		return DiscordChatSettingsDTO{}, safeChatActionError(agent.NewError(agent.ErrorInvalidArgument, "save Discord chat settings", errors.New("settings revision is invalid")))
	}
	mode := agent.PromptOverrideMode(0)
	switch strings.TrimSpace(request.PromptOverrideMode) {
	case "":
	case "append":
		mode = agent.PromptAppend
	case "replace":
		mode = agent.PromptReplace
	default:
		return DiscordChatSettingsDTO{}, safeChatActionError(agent.NewError(agent.ErrorInvalidArgument, "save Discord chat settings", errors.New("prompt mode is invalid")))
	}
	var settings control.AgentChatSettings
	err = s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		settings, actionErr = runtime.SaveChatSettings(ctx, request.ChatID, control.AgentChatSettingsUpdate{
			ExpectedVersion: agent.ConfigVersion(expectedVersion), ModerationLevel: agent.ModerationLevel(request.ModerationLevel),
			PromptOverrideMode: mode, PromptOverrideText: request.PromptOverrideText,
			Triggers: agent.TriggerConfig{Mention: request.TriggerMention, Name: request.TriggerName, Reply: request.TriggerReply, NameRegex: request.TriggerNameRegex, NamePattern: request.TriggerNamePattern, Smart: request.TriggerSmart, SmartRules: request.TriggerSmartRules},
		})
		return actionErr
	})
	if err != nil {
		return DiscordChatSettingsDTO{}, err
	}
	s.recordChatAction("INFO", "Chat settings saved", nil)
	return chatSettingsDTO(settings), nil
}

func chatSettingsDTO(settings control.AgentChatSettings) DiscordChatSettingsDTO {
	mode := ""
	switch settings.PromptOverrideMode {
	case agent.PromptAppend:
		mode = "append"
	case agent.PromptReplace:
		mode = "replace"
	}
	return DiscordChatSettingsDTO{
		Version: strconv.FormatUint(uint64(settings.Version), 10), ModerationLevel: uint8(settings.ModerationLevel),
		PromptOverrideMode: mode, PromptOverrideText: settings.PromptOverrideText,
		TriggerMention: settings.Triggers.Mention, TriggerName: settings.Triggers.Name, TriggerReply: settings.Triggers.Reply,
		TriggerNameRegex: settings.Triggers.NameRegex, TriggerNamePattern: settings.Triggers.NamePattern,
		TriggerSmart: settings.Triggers.Smart, TriggerSmartRules: settings.Triggers.SmartRules,
	}
}

func (s *AppService) GetDiscordChatTasks(chatID string) ([]DiscordChatTaskDTO, error) {
	var tasks []control.AgentChatTask
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		tasks, actionErr = runtime.ListChatTasks(ctx, chatID)
		return actionErr
	})
	if err != nil {
		return nil, err
	}
	result := make([]DiscordChatTaskDTO, len(tasks))
	for index, task := range tasks {
		result[index] = chatTaskDTO(task)
	}
	return result, nil
}

func (s *AppService) AddDiscordChatTask(request AddDiscordChatTaskRequestDTO) (DiscordChatTaskDTO, error) {
	input := control.AgentChatTaskInput{Prompt: request.Prompt, Daily: request.Daily}
	if request.Daily {
		clock, err := time.Parse("15:04", strings.TrimSpace(request.Time))
		if err != nil {
			return DiscordChatTaskDTO{}, errors.New("Choose a time of day, such as 07:00.")
		}
		input.DailyMinute = clock.Hour()*60 + clock.Minute()
	} else {
		runAt, err := time.Parse(time.RFC3339, request.RunAt)
		if err != nil {
			return DiscordChatTaskDTO{}, errors.New("Choose when the task should run.")
		}
		input.RunAt = runAt
	}
	var task control.AgentChatTask
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		task, actionErr = runtime.AddChatTask(ctx, request.ChatID, input)
		return actionErr
	})
	if err != nil {
		return DiscordChatTaskDTO{}, err
	}
	s.recordChatAction("INFO", "Chat task added", nil)
	return chatTaskDTO(task), nil
}

func (s *AppService) DeleteDiscordChatTask(chatID, taskID string, daily bool) error {
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		return runtime.DeleteChatTask(ctx, chatID, taskID, daily)
	})
	if err != nil {
		return err
	}
	s.recordChatAction("INFO", "Chat task deleted", nil)
	return nil
}

func chatTaskDTO(task control.AgentChatTask) DiscordChatTaskDTO {
	return DiscordChatTaskDTO{ID: task.ID, Prompt: task.Prompt, NextRun: task.NextRun.Format(time.RFC3339), Daily: task.Daily}
}

func (s *AppService) SendDiscordMessage(chatID, text, replyToMessageID string) (DiscordMessageDTO, error) {
	var message control.BotMessage
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		if strings.TrimSpace(replyToMessageID) == "" {
			message, actionErr = runtime.SendChatMessage(ctx, chatID, text)
			return actionErr
		}
		message, actionErr = runtime.SendChatReply(ctx, chatID, text, replyToMessageID)
		return actionErr
	})
	if err != nil {
		return DiscordMessageDTO{}, err
	}
	s.recordChatAction("INFO", "Discord message sent from Chat", nil)
	return discordMessage(message), nil
}

func (s *AppService) DeleteDiscordMessage(chatID, messageID string) error {
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		return runtime.DeleteChatMessage(ctx, chatID, messageID)
	})
	if err != nil {
		s.recordChatAction("WARN", "Could not delete Discord message", err)
		return err
	}
	s.recordChatAction("INFO", "Discord message deleted", nil)
	return nil
}

func (s *AppService) KickDiscordGroupMember(chatID, memberID string) error {
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		return runtime.KickGroupMember(ctx, chatID, memberID)
	})
	if err != nil {
		s.recordChatAction("WARN", "Could not kick the member", err)
		return err
	}
	s.recordChatAction("INFO", "Member kicked from the Discord server", nil)
	return nil
}

func (s *AppService) withChatActions(action func(control.ManagedAgentChatActions, context.Context) error) error {
	return s.withChatActionsTimeout(chatActionTimeout, action)
}

func (s *AppService) withChatActionsTimeout(timeout time.Duration, action func(control.ManagedAgentChatActions, context.Context) error) error {
	return s.withManagedChatActions(timeout, "Discord action from Chat failed", action)
}

func (s *AppService) withBroadcastActions(timeout time.Duration, action func(control.ManagedAgentChatActions, context.Context) error) error {
	return s.withManagedChatActions(timeout, "Discord broadcast action failed", action)
}

func (s *AppService) withManagedChatActions(timeout time.Duration, failureLogMessage string, action func(control.ManagedAgentChatActions, context.Context) error) error {
	if s == nil || s.agent == nil {
		return errors.New("Agent chat actions are not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := s.agent.WithChatActions(ctx, func(ctx context.Context, runtime control.ManagedAgentChatActions) error {
		return action(runtime, ctx)
	})
	if err != nil {
		s.recordChatAction("WARN", failureLogMessage, err)
		return safeChatActionError(err)
	}
	return nil
}

func broadcastActionTimeout(groupCount, batchSize, batchDelaySeconds int) time.Duration {
	if groupCount < 0 {
		groupCount = 0
	}
	if batchSize < 1 || batchSize > 100 {
		batchSize = 20
	}
	if batchDelaySeconds < 0 || batchDelaySeconds > 300 {
		batchDelaySeconds = 0
	}
	batches := (groupCount + batchSize - 1) / batchSize
	timeout := chatActionTimeout + time.Duration(batches)*(5*time.Minute+time.Duration(batchDelaySeconds)*time.Second)
	if timeout > 24*time.Hour {
		return 24 * time.Hour
	}
	return timeout
}

func safeChatActionError(err error) error {
	switch agent.CodeOf(err) {
	case agent.ErrorNotReady:
		var operation interface{ Operation() string }
		if errors.As(err, &operation) {
			switch operation.Operation() {
			case "use Agent chat actions":
				return errors.New("The Agent is not running. Start it from Overview.")
			case "use Discord chat actions", "send Discord message", "list Discord members", "kick Discord member", "moderate Discord channel",
				"list Discord broadcast channels", "send Discord broadcast", "schedule Discord broadcast":
				return errors.New("The Agent is running, but its Discord connection is not ready. Check the Discord status on Overview and try again.")
			}
		}
		return errors.New("The Agent or Discord connection is not ready. Check both statuses and try again.")
	case agent.ErrorPermissionDenied:
		var operation interface{ Operation() string }
		if errors.As(err, &operation) {
			switch operation.Operation() {
			case "check Discord permission", "delete Discord message", "kick Discord member", "list Discord members":
				// These explain which Discord permission or intent is missing,
				// and carry no IDs.
				if typed, ok := operation.(error); ok {
					if cause := errors.Unwrap(typed); cause != nil {
						return errors.New(sentence(cause.Error()))
					}
				}
			}
		}
		return errors.New("This action is not allowed in this conversation.")
	case agent.ErrorNotFound:
		var operation interface{ Operation() string }
		if errors.As(err, &operation) {
			switch operation.Operation() {
			case "kick Discord member":
				return errors.New("The member list has changed. Refresh it and try again.")
			case "send Discord broadcast":
				return errors.New("The selected channel list expired. Refresh the channels and try again.")
			case "schedule Discord broadcast":
				return errors.New("The selected channel list changed. Refresh the channels and try again.")
			case "resolve chat target":
				return errors.New("This chat is not available on the current Agent connection. Reload the chat list.")
			case "resolve message target", "resolve Discord message", "delete Discord chat message", "delete Discord message":
				return errors.New("This message is no longer available for that action. Reload the chat history.")
			case "compare and swap config":
				return errors.New("Chat settings are not available. Close and reopen the settings panel.")
			case "delete chat task":
				return errors.New("This task already ran or was deleted.")
			}
		}
		return errors.New("Chat data is no longer available. Reload the chat list.")
	case agent.ErrorInvalidArgument:
		var operation interface{ Operation() string }
		if errors.As(err, &operation) {
			switch operation.Operation() {
			case "validate Discord broadcast payload":
				return errors.New("The JSON must be a Discord message object, such as {\"content\": \"Hello\"}.")
			case "validate Discord broadcast timing":
				return errors.New("Batch size must be 1–100 and the pause must be 0–300 seconds.")
			case "moderate Discord channel":
				return errors.New("Members can only be managed in server channels.")
			case "schedule Discord broadcast":
				return errors.New("Choose a future date within the next year.")
			case "add chat task":
				return errors.New("Choose a time in the future.")
			case "schedule task":
				return errors.New("Write the task in at most 4,000 characters, and choose a time within the next 24 hours.")
			}
		}
		return errors.New("The action data is invalid.")
	case agent.ErrorResourceExhausted:
		var operation interface{ Operation() string }
		if errors.As(err, &operation) && operation.Operation() == "schedule daily task" {
			return errors.New("This chat already has 10 daily tasks. Delete one first.")
		}
		return errors.New("The action failed. Check the Logs page for details.")
	case agent.ErrorTimeout:
		return errors.New("Discord did not respond before the timeout.")
	case agent.ErrorConflict:
		var operation interface{ Operation() string }
		if errors.As(err, &operation) && operation.Operation() == "cancel Discord broadcast schedule" {
			return errors.New("This schedule has already started or was cancelled.")
		}
		return errors.New("Chat settings have changed. Close and reopen the Chat settings panel.")
	default:
		return errors.New("The action failed. Check the Logs page for details.")
	}
}

// sentence capitalizes a message and ends it with a period.
func sentence(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return message
	}
	message = strings.ToUpper(message[:1]) + message[1:]
	if !strings.HasSuffix(message, ".") {
		message += "."
	}
	return message
}

func (s *AppService) recordChatAction(level, message string, err error) {
	if s == nil || s.logs == nil {
		return
	}
	details := ""
	if err != nil {
		details = "code=" + string(agent.CodeOf(err))
		var operation interface{ Operation() string }
		if errors.As(err, &operation) && operation.Operation() != "" {
			details += " op=" + operation.Operation()
		}
	}
	s.logs.RecordError(level, message, details, err)
}

func (s *AppService) recordChatActionDetails(level, message, details string) {
	if s == nil || s.logs == nil {
		return
	}
	s.logs.Record(level, message, details)
}

func (s *AppService) recordLog(level, message string, err error) {
	if s == nil || s.logs == nil {
		return
	}
	details := ""
	if err != nil {
		details = "code=" + string(agent.CodeOf(err)) + " · " + err.Error()
	}
	s.logs.RecordError(level, message, details, err)
}

// GetSettings returns the current public settings snapshot. Secret values are
// replaced by configured flags inside the DTO conversion.
func (s *AppService) GetSettings() (SettingsViewDTO, error) {
	if s.control == nil {
		return SettingsViewDTO{}, errors.New("settings controller is not initialized")
	}
	view, err := s.control.GetSettings(context.Background())
	if err != nil {
		return SettingsViewDTO{}, err
	}
	return s.settingsViewDTO(view), nil
}

// GetSettingsSchema returns the typed field catalog used by the form.
func (s *AppService) GetSettingsSchema() []FieldDescriptorDTO {
	fields := config.SettingsSchema()
	result := make([]FieldDescriptorDTO, len(fields))
	for index, field := range fields {
		result[index] = FieldDescriptorDTO{Key: field.Key, Group: field.Group, Kind: string(field.Kind), Sensitive: field.Sensitive, ReadOnly: field.ReadOnly, CLIOnly: field.CLIOnly, Default: field.Default}
	}
	return result
}

// ValidateSettings validates a complete public draft without persisting it.
func (s *AppService) ValidateSettings(request SettingsPatchDTO) (ValidationResultDTO, error) {
	if s.control == nil {
		return ValidationResultDTO{}, errors.New("settings controller is not initialized")
	}
	patch, err := patchFromDTO(request)
	if err != nil {
		return ValidationResultDTO{}, err
	}
	result, err := s.control.ValidateSettings(context.Background(), patch)
	if err != nil {
		return ValidationResultDTO{}, err
	}
	return validationResultDTO(result), nil
}

// SaveSettings persists a validated draft with compare-and-swap revision
// semantics. The response contains the new public revision and readiness.
func (s *AppService) SaveSettings(request SaveSettingsRequestDTO) (SaveSettingsResultDTO, error) {
	if s.control == nil {
		return SaveSettingsResultDTO{}, errors.New("settings controller is not initialized")
	}
	expected, err := strconv.ParseUint(request.ExpectedRevision, 10, 64)
	if err != nil {
		return SaveSettingsResultDTO{}, errors.New("expectedRevision must be an unsigned integer")
	}
	patch, err := patchFromDTO(request.Patch)
	if err != nil {
		return SaveSettingsResultDTO{}, err
	}
	result, err := s.control.Save(context.Background(), expected, patch)
	if err != nil {
		s.recordLog("ERROR", "Could not save settings", err)
		return SaveSettingsResultDTO{}, err
	}
	result.View.Values.DataDir = s.effectiveDataRoot(result.View.Values.DataDir)
	s.recordLog("INFO", "App settings saved", nil)
	return saveSettingsResultDTO(result), nil
}

func (s *AppService) settingsViewDTO(view control.SettingsView) SettingsViewDTO {
	dto := settingsViewDTO(view)
	dto.Values.DataDir = s.effectiveDataRoot(dto.Values.DataDir)
	return dto
}

func (s *AppService) effectiveDataRoot(fallback string) string {
	if s != nil && s.dataRoot != "" {
		return s.dataRoot
	}
	return fallback
}

func (s *AppService) GetDiscordSessionStatus() (DiscordSessionStatusDTO, error) {
	if s.sessions == nil {
		return DiscordSessionStatusDTO{}, errors.New("Discord session controller is not initialized")
	}
	status, err := s.sessions.GetStatus(context.Background())
	if err != nil {
		return DiscordSessionStatusDTO{}, err
	}
	dto := discordSessionStatusDTO(status)
	if s.agent != nil && s.agent.IsActive() {
		agentStatus, agentErr := s.agent.GetStatus(context.Background())
		if agentErr != nil {
			return DiscordSessionStatusDTO{}, agentErr
		}
		dto.AgentActive = true
		dto.RuntimeState = agentStatus.DiscordState
		dto.ErrorCode = string(agentStatus.ErrorCode)
	}
	return dto, nil
}

func (s *AppService) GetAgentRuntimeStatus() (AgentRuntimeStatusDTO, error) {
	if s.agent == nil {
		return AgentRuntimeStatusDTO{}, errors.New("Agent runtime controller is not initialized")
	}
	status, err := s.agent.GetStatus(context.Background())
	if err != nil {
		return AgentRuntimeStatusDTO{}, err
	}
	return agentRuntimeStatusDTO(status), nil
}

func (s *AppService) StartAgent() (AgentRuntimeStatusDTO, error) {
	if s.agent == nil {
		return AgentRuntimeStatusDTO{}, errors.New("Agent runtime controller is not initialized")
	}
	status, err := s.agent.Start(context.Background())
	if err != nil {
		s.recordLog("ERROR", "Could not start Agent", err)
		return AgentRuntimeStatusDTO{}, err
	}
	s.recordLog("INFO", "Agent started", nil)
	return agentRuntimeStatusDTO(status), nil
}

func (s *AppService) StopAgent() (AgentRuntimeStatusDTO, error) {
	if s.agent == nil {
		return AgentRuntimeStatusDTO{}, errors.New("Agent runtime controller is not initialized")
	}
	status, err := s.agent.Stop(context.Background())
	if err != nil {
		s.recordLog("ERROR", "Could not stop Agent", err)
		return AgentRuntimeStatusDTO{}, err
	}
	s.recordLog("INFO", "Agent stopped", nil)
	return agentRuntimeStatusDTO(status), nil
}

func (s *AppService) ApplyAgentSettings(request ApplyAgentSettingsRequestDTO) (AgentRuntimeStatusDTO, error) {
	if s.agent == nil {
		return AgentRuntimeStatusDTO{}, errors.New("Agent runtime controller is not initialized")
	}
	revision, err := strconv.ParseUint(request.ExpectedRevision, 10, 64)
	if err != nil {
		return AgentRuntimeStatusDTO{}, errors.New("expectedRevision must be an unsigned integer")
	}
	status, err := s.agent.ApplySettings(context.Background(), revision)
	if err != nil {
		s.recordLog("ERROR", "Could not apply settings to Agent", err)
		return AgentRuntimeStatusDTO{}, err
	}
	s.recordLog("INFO", "Settings applied to Agent", nil)
	return agentRuntimeStatusDTO(status), nil
}

func (s *AppService) withSessionControl(action func() error) error {
	if s.agent == nil {
		return action()
	}
	return s.agent.WithSessionControl(action)
}

func (s *AppService) BeginDiscordLink(request BeginDiscordLinkRequestDTO) (DiscordSessionOperationDTO, error) {
	if s.sessions == nil {
		return DiscordSessionOperationDTO{}, errors.New("Discord session controller is not initialized")
	}
	var operation control.SessionOperation
	err := s.withSessionControl(func() error {
		var err error
		operation, err = s.sessions.BeginLink(context.Background(), control.BeginLinkRequest{Token: request.Token})
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not link the Discord bot", err)
		return DiscordSessionOperationDTO{}, err
	}
	s.recordLog("INFO", "Discord bot link started", nil)
	return discordSessionOperationDTO(operation), nil
}

func (s *AppService) ResumeDiscordSession() (DiscordSessionOperationDTO, error) {
	if s.sessions == nil {
		return DiscordSessionOperationDTO{}, errors.New("Discord session controller is not initialized")
	}
	var operation control.SessionOperation
	err := s.withSessionControl(func() error {
		var err error
		operation, err = s.sessions.Resume(context.Background())
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not resume Discord session", err)
		return DiscordSessionOperationDTO{}, err
	}
	s.recordLog("INFO", "Resuming Discord session", nil)
	return discordSessionOperationDTO(operation), nil
}

func (s *AppService) StopDiscordSession() (DiscordSessionStatusDTO, error) {
	if s.sessions == nil {
		return DiscordSessionStatusDTO{}, errors.New("Discord session controller is not initialized")
	}
	var status control.SessionStatus
	err := s.withSessionControl(func() error {
		var err error
		status, err = s.sessions.Stop(context.Background())
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not stop Discord session", err)
		return DiscordSessionStatusDTO{}, err
	}
	s.recordLog("INFO", "Discord session stopped", nil)
	return discordSessionStatusDTO(status), nil
}

func (s *AppService) CancelDiscordLink(operationID string) (DiscordSessionStatusDTO, error) {
	if s.sessions == nil {
		return DiscordSessionStatusDTO{}, errors.New("Discord session controller is not initialized")
	}
	var status control.SessionStatus
	err := s.withSessionControl(func() error {
		var err error
		status, err = s.sessions.CancelLink(context.Background(), operationID)
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not cancel the Discord bot link", err)
		return DiscordSessionStatusDTO{}, err
	}
	s.recordLog("INFO", "Discord bot link canceled", nil)
	return discordSessionStatusDTO(status), nil
}

func (s *AppService) ReconnectDiscordSession() (DiscordSessionOperationDTO, error) {
	if s.sessions == nil {
		return DiscordSessionOperationDTO{}, errors.New("Discord session controller is not initialized")
	}
	var operation control.SessionOperation
	err := s.withSessionControl(func() error {
		var err error
		operation, err = s.sessions.Reconnect(context.Background())
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not reconnect Discord session", err)
		return DiscordSessionOperationDTO{}, err
	}
	s.recordLog("INFO", "Discord reconnection started", nil)
	return discordSessionOperationDTO(operation), nil
}

func (s *AppService) UnlinkDiscordBot() (DiscordSessionOperationDTO, error) {
	if s.sessions == nil {
		return DiscordSessionOperationDTO{}, errors.New("Discord session controller is not initialized")
	}
	var operation control.SessionOperation
	err := s.withSessionControl(func() error {
		var err error
		operation, err = s.sessions.Logout(context.Background())
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not unlink the Discord bot", err)
		return DiscordSessionOperationDTO{}, err
	}
	s.recordLog("INFO", "Discord bot unlinked", nil)
	return discordSessionOperationDTO(operation), nil
}
