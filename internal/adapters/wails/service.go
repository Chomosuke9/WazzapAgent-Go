//go:build gui

package wails

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	agentapp "github.com/Chomosuke9/WazzapAgent-Go/internal/app"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/observability"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const AppName = "WazzapAgent"

// AppInfo is the small, stable information contract used by the first GUI binding.
type AppInfo struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

// PingEvent is emitted on app:ping when Ping is called by the frontend.
type PingEvent struct {
	Message  string `json:"message"`
	Sequence uint64 `json:"sequence"`
}

type LogEntryDTO struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Details string `json:"details"`
}

type WhatsAppConversationDTO struct {
	ID                  string               `json:"id"`
	Kind                string               `json:"kind"`
	Name                string               `json:"name"`
	LastMessage         string               `json:"lastMessage"`
	LastMessageAt       string               `json:"lastMessageAt"`
	LastFromBot         bool                 `json:"lastFromBot"`
	MessageCount        uint64               `json:"messageCount"`
	LastMessageMentions []WhatsAppMentionDTO `json:"lastMessageMentions"`
}

type WhatsAppUsageDTO struct {
	TotalMessages        uint64                  `json:"totalMessages"`
	TotalInvocations     uint64                  `json:"totalInvocations"`
	TotalChats           uint64                  `json:"totalChats"`
	MessagesInPeriod     uint64                  `json:"messagesInPeriod"`
	InvocationsInPeriod  uint64                  `json:"invocationsInPeriod"`
	ActiveChatsInPeriod  uint64                  `json:"activeChatsInPeriod"`
	TotalGroups          uint64                  `json:"totalGroups"`
	ActiveGroupsInPeriod uint64                  `json:"activeGroupsInPeriod"`
	PeriodStart          string                  `json:"periodStart"`
	PeriodDays           uint32                  `json:"periodDays"`
	Groups               []WhatsAppGroupUsageDTO `json:"groups"`
	InvocationGroups     []WhatsAppGroupUsageDTO `json:"invocationGroups"`
	DailyActivity        []WhatsAppDailyUsageDTO `json:"dailyActivity"`
}

type WhatsAppGroupUsageDTO struct {
	Name                string `json:"name"`
	Messages            uint64 `json:"messages"`
	MessagesInPeriod    uint64 `json:"messagesInPeriod"`
	Invocations         uint64 `json:"invocations"`
	InvocationsInPeriod uint64 `json:"invocationsInPeriod"`
}

type WhatsAppDailyUsageDTO struct {
	Date        string `json:"date"`
	Messages    uint64 `json:"messages"`
	Invocations uint64 `json:"invocations"`
}

type WhatsAppMentionDTO struct {
	Token       string `json:"token"`
	SenderRef   string `json:"senderRef"`
	DisplayName string `json:"displayName"`
	Bot         bool   `json:"bot"`
}

type WhatsAppQuoteDTO struct {
	MessageID string               `json:"messageID"`
	Role      string               `json:"role"`
	Sender    string               `json:"sender"`
	Content   string               `json:"content"`
	Mentions  []WhatsAppMentionDTO `json:"mentions"`
}

type WhatsAppMessageDTO struct {
	ID        string               `json:"id"`
	Role      string               `json:"role"`
	Sender    string               `json:"sender"`
	SenderRef string               `json:"senderRef"`
	Content   string               `json:"content"`
	CreatedAt string               `json:"createdAt"`
	Delivery  string               `json:"delivery"`
	Deleted   bool                 `json:"deleted"`
	Mentions  []WhatsAppMentionDTO `json:"mentions"`
	Quote     *WhatsAppQuoteDTO    `json:"quote"`
}

type WhatsAppGroupMemberDTO struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	IsAdmin      bool   `json:"isAdmin"`
	IsSuperAdmin bool   `json:"isSuperAdmin"`
	CanKick      bool   `json:"canKick"`
}

type WhatsAppGroupMembersDTO struct {
	BotIsAdmin bool                     `json:"botIsAdmin"`
	Members    []WhatsAppGroupMemberDTO `json:"members"`
}

type WhatsAppBroadcastGroupDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SendWhatsAppBroadcastRequestDTO struct {
	GroupIDs          []string `json:"groupIDs"`
	Format            string   `json:"format"`
	Payload           string   `json:"payload"`
	BatchSize         int      `json:"batchSize"`
	BatchDelaySeconds int      `json:"batchDelaySeconds"`
}

type ScheduleWhatsAppBroadcastRequestDTO struct {
	GroupIDs          []string `json:"groupIDs"`
	Format            string   `json:"format"`
	Payload           string   `json:"payload"`
	BatchSize         int      `json:"batchSize"`
	BatchDelaySeconds int      `json:"batchDelaySeconds"`
	ScheduledAt       string   `json:"scheduledAt"`
}

type WhatsAppBroadcastGroupResultDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Sent      bool   `json:"sent"`
	ErrorCode string `json:"errorCode"`
}

type WhatsAppBroadcastScheduleResultDTO struct {
	Name      string `json:"name"`
	Sent      bool   `json:"sent"`
	ErrorCode string `json:"errorCode"`
}

type WhatsAppBroadcastScheduleDTO struct {
	ID                string                               `json:"id"`
	ScheduledAt       string                               `json:"scheduledAt"`
	BatchSize         int                                  `json:"batchSize"`
	BatchDelaySeconds int                                  `json:"batchDelaySeconds"`
	GroupCount        int                                  `json:"groupCount"`
	Status            string                               `json:"status"`
	Results           []WhatsAppBroadcastScheduleResultDTO `json:"results"`
}

type WhatsAppChatSettingsDTO struct {
	Version            string `json:"version"`
	ModerationLevel    uint8  `json:"moderationLevel"`
	PromptOverrideMode string `json:"promptOverrideMode"`
	PromptOverrideText string `json:"promptOverrideText"`
	TriggerMention     bool   `json:"triggerMention"`
	TriggerName        bool   `json:"triggerName"`
	TriggerReply       bool   `json:"triggerReply"`
	TriggerNameRegex   bool   `json:"triggerNameRegex"`
	TriggerNamePattern string `json:"triggerNamePattern"`
}

type SaveWhatsAppChatSettingsRequestDTO struct {
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
}

type ResetWhatsAppChatSettingsRequestDTO struct {
	ExpectedSettingsRevision string `json:"expectedSettingsRevision"`
	Category                 string `json:"category"`
}

type ResetWhatsAppChatSettingsResultDTO struct {
	ChangedChats int64 `json:"changedChats"`
}

const chatActionTimeout = 45 * time.Second

// AppService exposes the stable application and settings bindings. Runtime and
// session operations are added only when their controller owns the lifecycle.
type AppService struct {
	app           *application.App
	version       string
	control       *control.Controller
	sessions      *control.SessionController
	agent         *control.AgentController
	conversations *control.ConversationController
	dataRoot      string
	logs          *observability.LogBuffer
	sequence      atomic.Uint64
}

func NewAppService(app *application.App, version string, controllers ...*control.Controller) *AppService {
	if version == "" {
		version = "dev"
	}
	var controller *control.Controller
	if len(controllers) > 0 {
		controller = controllers[0]
	}
	return &AppService{app: app, version: version, control: controller}
}

// NewAppServiceWithDataRoot keeps the GUI's effective leased root visible in
// the public settings view. Changing the root is a separate data operation and
// is intentionally not exposed through SaveSettings.
func NewAppServiceWithDataRoot(app *application.App, version string, controller *control.Controller, dataRoot string) *AppService {
	return NewAppServiceWithControllers(app, version, controller, nil, dataRoot)
}

func NewAppServiceWithControllers(app *application.App, version string, settings *control.Controller, sessions *control.SessionController, dataRoot string, agents ...*control.AgentController) *AppService {
	service := NewAppService(app, version, settings)
	service.sessions = sessions
	service.dataRoot = dataRoot
	if len(agents) > 0 {
		service.agent = agents[0]
	}
	return service
}

func NewAppServiceWithLogBuffer(app *application.App, version string, settings *control.Controller, sessions *control.SessionController, dataRoot string, agentController *control.AgentController, logs *observability.LogBuffer) *AppService {
	service := NewAppServiceWithControllers(app, version, settings, sessions, dataRoot, agentController)
	service.logs = logs
	return service
}

func NewAppServiceWithConversations(app *application.App, version string, settings *control.Controller, sessions *control.SessionController, dataRoot string, agentController *control.AgentController, conversations *control.ConversationController, logs *observability.LogBuffer) *AppService {
	service := NewAppServiceWithLogBuffer(app, version, settings, sessions, dataRoot, agentController, logs)
	service.conversations = conversations
	return service
}

func (s *AppService) GetAppInfo() AppInfo {
	return AppInfo{
		Name:     AppName,
		Version:  s.version,
		Platform: runtime.GOOS,
	}
}

// GetLogs returns the newest safe application events captured in this run.
func (s *AppService) GetLogs() []LogEntryDTO {
	if s == nil || s.logs == nil {
		return nil
	}
	entries := s.logs.Entries()
	result := make([]LogEntryDTO, len(entries))
	for index, entry := range entries {
		result[index] = LogEntryDTO{Time: entry.Time, Level: entry.Level, Message: entry.Message, Details: entry.Details}
	}
	return result
}

func (s *AppService) GetWhatsAppConversations() ([]WhatsAppConversationDTO, error) {
	if s.conversations == nil {
		return nil, errors.New("conversation controller is not initialized")
	}
	conversations, err := s.conversations.List(context.Background())
	if err != nil {
		return nil, err
	}
	result := make([]WhatsAppConversationDTO, len(conversations))
	for index, item := range conversations {
		result[index] = WhatsAppConversationDTO{
			ID: item.ID.String(), Kind: item.Kind, Name: item.Name, LastMessage: item.LastMessage,
			LastMessageAt: item.LastMessageAt, LastFromBot: item.LastFromBot, MessageCount: item.MessageCount,
			LastMessageMentions: whatsAppMentions(item.LastMessageMentions),
		}
	}
	return result, nil
}

func (s *AppService) GetWhatsAppUsage(periodDays uint32) (WhatsAppUsageDTO, error) {
	if s.conversations == nil {
		return WhatsAppUsageDTO{}, errors.New("conversation controller is not initialized")
	}
	usage, err := s.conversations.Usage(context.Background(), periodDays)
	if err != nil {
		return WhatsAppUsageDTO{}, err
	}
	dto := WhatsAppUsageDTO{
		TotalMessages: usage.TotalMessages, TotalInvocations: usage.TotalInvocations, TotalChats: usage.TotalChats,
		MessagesInPeriod: usage.MessagesInPeriod, InvocationsInPeriod: usage.InvocationsInPeriod,
		ActiveChatsInPeriod: usage.ActiveChatsInPeriod,
		TotalGroups:         usage.TotalGroups, ActiveGroupsInPeriod: usage.ActiveGroupsInPeriod,
		PeriodStart: usage.PeriodStart, PeriodDays: usage.PeriodDays,
		Groups:           make([]WhatsAppGroupUsageDTO, len(usage.Groups)),
		InvocationGroups: make([]WhatsAppGroupUsageDTO, len(usage.InvocationGroups)),
		DailyActivity:    make([]WhatsAppDailyUsageDTO, len(usage.DailyActivity)),
	}
	for index, group := range usage.Groups {
		dto.Groups[index] = whatsAppGroupUsage(group)
	}
	for index, group := range usage.InvocationGroups {
		dto.InvocationGroups[index] = whatsAppGroupUsage(group)
	}
	for index, day := range usage.DailyActivity {
		dto.DailyActivity[index] = WhatsAppDailyUsageDTO{Date: day.Date, Messages: day.Messages, Invocations: day.Invocations}
	}
	return dto, nil
}

func whatsAppGroupUsage(group control.BotGroupUsage) WhatsAppGroupUsageDTO {
	return WhatsAppGroupUsageDTO{
		Name: group.Name, Messages: group.Messages, MessagesInPeriod: group.MessagesInPeriod,
		Invocations: group.Invocations, InvocationsInPeriod: group.InvocationsInPeriod,
	}
}

func (s *AppService) GetWhatsAppMessages(chatID string) ([]WhatsAppMessageDTO, error) {
	if s.conversations == nil {
		return nil, errors.New("conversation controller is not initialized")
	}
	messages, err := s.conversations.Messages(context.Background(), chatID)
	if err != nil {
		return nil, err
	}
	result := make([]WhatsAppMessageDTO, len(messages))
	for index, item := range messages {
		result[index] = whatsAppMessage(item)
	}
	return result, nil
}

func whatsAppMessage(item control.BotMessage) WhatsAppMessageDTO {
	result := WhatsAppMessageDTO{
		ID: item.ID.String(), Role: item.Role, Sender: item.Sender, Content: item.Content,
		CreatedAt: item.CreatedAt, Delivery: item.Delivery, Deleted: item.Deleted,
		Mentions: whatsAppMentions(item.Mentions),
	}
	if !item.SenderRef.IsZero() {
		result.SenderRef = item.SenderRef.String()
	}
	if item.Quote != nil {
		result.Quote = &WhatsAppQuoteDTO{
			MessageID: item.Quote.MessageID.String(), Role: item.Quote.Role,
			Sender: item.Quote.Sender, Content: item.Quote.Content,
			Mentions: whatsAppMentions(item.Quote.Mentions),
		}
	}
	return result
}

func whatsAppMentions(mentions []control.BotMention) []WhatsAppMentionDTO {
	if len(mentions) == 0 {
		return nil
	}
	result := make([]WhatsAppMentionDTO, len(mentions))
	for index, mention := range mentions {
		result[index] = WhatsAppMentionDTO{
			Token: mention.Token, DisplayName: mention.DisplayName, Bot: mention.Bot,
		}
		if !mention.SenderRef.IsZero() {
			result[index].SenderRef = mention.SenderRef.String()
		}
	}
	return result
}

func (s *AppService) GetWhatsAppGroupMembers(chatID string) (WhatsAppGroupMembersDTO, error) {
	var group control.AgentGroupMembers
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		group, actionErr = runtime.ListGroupMembers(ctx, chatID)
		return actionErr
	})
	if err != nil {
		return WhatsAppGroupMembersDTO{}, err
	}
	result := WhatsAppGroupMembersDTO{BotIsAdmin: group.BotIsAdmin, Members: make([]WhatsAppGroupMemberDTO, len(group.Members))}
	for index, member := range group.Members {
		result.Members[index] = WhatsAppGroupMemberDTO{
			ID: member.ID, Name: member.Name, IsAdmin: member.IsAdmin,
			IsSuperAdmin: member.IsSuperAdmin, CanKick: member.CanKick,
		}
	}
	return result, nil
}

func (s *AppService) GetWhatsAppBroadcastGroups() ([]WhatsAppBroadcastGroupDTO, error) {
	var groups []control.AgentBroadcastGroup
	err := s.withBroadcastActions(chatActionTimeout, func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		broadcaster, ok := runtime.(control.ManagedAgentBroadcastActions)
		if !ok {
			return errors.New("the active Agent runtime does not support WhatsApp broadcast")
		}
		var actionErr error
		groups, actionErr = broadcaster.ListBroadcastGroups(ctx)
		return actionErr
	})
	if err != nil {
		return nil, err
	}
	result := make([]WhatsAppBroadcastGroupDTO, len(groups))
	for index, group := range groups {
		result[index] = WhatsAppBroadcastGroupDTO{ID: group.ID, Name: group.Name}
	}
	return result, nil
}

func (s *AppService) NormalizeWhatsAppBroadcastPayload(payload string) (string, error) {
	return agentapp.NormalizeWhatsAppBroadcastPayload(payload)
}

func (s *AppService) SendWhatsAppBroadcast(request SendWhatsAppBroadcastRequestDTO) ([]WhatsAppBroadcastGroupResultDTO, error) {
	format := request.Format
	if format != "text" && format != "payload" {
		format = "invalid"
	}
	s.recordChatActionDetails("INFO", "WhatsApp broadcast send started", fmt.Sprintf("groups=%d · format=%s · batch_size=%d · pause_seconds=%d", len(request.GroupIDs), format, request.BatchSize, request.BatchDelaySeconds))
	var results []control.AgentBroadcastGroupResult
	err := s.withBroadcastActions(broadcastActionTimeout(len(request.GroupIDs), request.BatchSize, request.BatchDelaySeconds), func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		broadcaster, ok := runtime.(control.ManagedAgentBroadcastActions)
		if !ok {
			return errors.New("the active Agent runtime does not support WhatsApp broadcast")
		}
		var actionErr error
		results, actionErr = broadcaster.BroadcastWhatsAppGroups(ctx, request.GroupIDs, request.Format, request.Payload, request.BatchSize, request.BatchDelaySeconds)
		return actionErr
	})
	if err != nil {
		return nil, err
	}
	result := make([]WhatsAppBroadcastGroupResultDTO, len(results))
	sent := 0
	for index, item := range results {
		result[index] = WhatsAppBroadcastGroupResultDTO{ID: item.ID, Name: item.Name, Sent: item.Sent, ErrorCode: item.ErrorCode}
		if item.Sent {
			sent++
		}
	}
	level, message := "INFO", "WhatsApp broadcast completed"
	if sent != len(results) {
		level, message = "WARN", "WhatsApp broadcast completed with delivery failures"
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
	details := fmt.Sprintf("groups=%d · sent=%d · failed=%d", len(results), sent, len(results)-sent)
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

func (s *AppService) ScheduleWhatsAppBroadcast(request ScheduleWhatsAppBroadcastRequestDTO) (WhatsAppBroadcastScheduleDTO, error) {
	scheduledAt, err := time.Parse(time.RFC3339, request.ScheduledAt)
	if err != nil {
		return WhatsAppBroadcastScheduleDTO{}, errors.New("Scheduled time is invalid.")
	}
	var schedule control.AgentBroadcastSchedule
	err = s.withBroadcastActions(chatActionTimeout, func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		broadcaster, ok := runtime.(control.ManagedAgentBroadcastActions)
		if !ok {
			return errors.New("the active Agent runtime does not support WhatsApp broadcast schedules")
		}
		var actionErr error
		schedule, actionErr = broadcaster.ScheduleWhatsAppBroadcast(ctx, request.GroupIDs, request.Format, request.Payload, request.BatchSize, request.BatchDelaySeconds, scheduledAt)
		return actionErr
	})
	if err != nil {
		return WhatsAppBroadcastScheduleDTO{}, err
	}
	s.recordChatAction("INFO", "WhatsApp broadcast scheduled", nil)
	return broadcastScheduleDTO(schedule), nil
}

func (s *AppService) GetWhatsAppBroadcastSchedules() ([]WhatsAppBroadcastScheduleDTO, error) {
	var schedules []control.AgentBroadcastSchedule
	err := s.withBroadcastActions(chatActionTimeout, func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		broadcaster, ok := runtime.(control.ManagedAgentBroadcastActions)
		if !ok {
			return errors.New("the active Agent runtime does not support WhatsApp broadcast schedules")
		}
		var actionErr error
		schedules, actionErr = broadcaster.ListWhatsAppBroadcastSchedules(ctx)
		return actionErr
	})
	if err != nil {
		return nil, err
	}
	result := make([]WhatsAppBroadcastScheduleDTO, len(schedules))
	for index, schedule := range schedules {
		result[index] = broadcastScheduleDTO(schedule)
	}
	return result, nil
}

func (s *AppService) CancelWhatsAppBroadcastSchedule(id string) error {
	err := s.withBroadcastActions(chatActionTimeout, func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		broadcaster, ok := runtime.(control.ManagedAgentBroadcastActions)
		if !ok {
			return errors.New("the active Agent runtime does not support WhatsApp broadcast schedules")
		}
		return broadcaster.CancelWhatsAppBroadcastSchedule(ctx, id)
	})
	if err != nil {
		return err
	}
	s.recordChatAction("INFO", "WhatsApp broadcast schedule cancelled", nil)
	return nil
}

func broadcastScheduleDTO(schedule control.AgentBroadcastSchedule) WhatsAppBroadcastScheduleDTO {
	result := WhatsAppBroadcastScheduleDTO{
		ID: schedule.ID, ScheduledAt: schedule.ScheduledAt.UTC().Format(time.RFC3339),
		BatchSize: schedule.BatchSize, BatchDelaySeconds: schedule.BatchDelaySeconds,
		GroupCount: schedule.GroupCount, Status: schedule.Status,
		Results: make([]WhatsAppBroadcastScheduleResultDTO, len(schedule.Results)),
	}
	for index, item := range schedule.Results {
		result.Results[index] = WhatsAppBroadcastScheduleResultDTO{Name: item.Name, Sent: item.Sent, ErrorCode: item.ErrorCode}
	}
	return result
}

func (s *AppService) GetWhatsAppChatSettings(chatID string) (WhatsAppChatSettingsDTO, error) {
	var settings control.AgentChatSettings
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		settings, actionErr = runtime.GetChatSettings(ctx, chatID)
		return actionErr
	})
	if err != nil {
		return WhatsAppChatSettingsDTO{}, err
	}
	s.recordChatAction("INFO", "Chat settings opened", nil)
	return chatSettingsDTO(settings), nil
}

func (s *AppService) ResetWhatsAppChatSettings(request ResetWhatsAppChatSettingsRequestDTO) (ResetWhatsAppChatSettingsResultDTO, error) {
	if s.control == nil {
		return ResetWhatsAppChatSettingsResultDTO{}, errors.New("settings controller is not initialized")
	}
	expectedRevision, err := strconv.ParseUint(request.ExpectedSettingsRevision, 10, 64)
	if err != nil || expectedRevision == 0 {
		return ResetWhatsAppChatSettingsResultDTO{}, errors.New("settings revision is invalid")
	}
	view, err := s.control.GetSettings(context.Background())
	if err != nil {
		return ResetWhatsAppChatSettingsResultDTO{}, err
	}
	if view.Revision != expectedRevision {
		return ResetWhatsAppChatSettingsResultDTO{}, errors.New("Saved settings changed. Reload Settings and try again.")
	}
	var changed int64
	err = s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		resetter, ok := runtime.(control.ManagedAgentChatSettingsReset)
		if !ok {
			return errors.New("the active Agent runtime does not support chat settings reset")
		}
		var resetErr error
		changed, resetErr = resetter.ResetChatSettings(ctx, control.ChatSettingsResetCategory(request.Category), view.Values.ChatDefaults)
		return resetErr
	})
	if err != nil {
		return ResetWhatsAppChatSettingsResultDTO{}, err
	}
	s.recordChatAction("INFO", "Saved chat settings reset", nil)
	return ResetWhatsAppChatSettingsResultDTO{ChangedChats: changed}, nil
}

func (s *AppService) SaveWhatsAppChatSettings(request SaveWhatsAppChatSettingsRequestDTO) (WhatsAppChatSettingsDTO, error) {
	expectedVersion, err := strconv.ParseUint(request.ExpectedVersion, 10, 64)
	if err != nil || expectedVersion == 0 {
		return WhatsAppChatSettingsDTO{}, safeChatActionError(agent.NewError(agent.ErrorInvalidArgument, "save WhatsApp chat settings", errors.New("settings revision is invalid")))
	}
	mode := agent.PromptOverrideMode(0)
	switch strings.TrimSpace(request.PromptOverrideMode) {
	case "":
	case "append":
		mode = agent.PromptAppend
	case "replace":
		mode = agent.PromptReplace
	default:
		return WhatsAppChatSettingsDTO{}, safeChatActionError(agent.NewError(agent.ErrorInvalidArgument, "save WhatsApp chat settings", errors.New("prompt mode is invalid")))
	}
	var settings control.AgentChatSettings
	err = s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		settings, actionErr = runtime.SaveChatSettings(ctx, request.ChatID, control.AgentChatSettingsUpdate{
			ExpectedVersion: agent.ConfigVersion(expectedVersion), ModerationLevel: agent.ModerationLevel(request.ModerationLevel),
			PromptOverrideMode: mode, PromptOverrideText: request.PromptOverrideText,
			Triggers: agent.TriggerConfig{Mention: request.TriggerMention, Name: request.TriggerName, Reply: request.TriggerReply, NameRegex: request.TriggerNameRegex, NamePattern: request.TriggerNamePattern},
		})
		return actionErr
	})
	if err != nil {
		return WhatsAppChatSettingsDTO{}, err
	}
	s.recordChatAction("INFO", "Chat settings saved", nil)
	return chatSettingsDTO(settings), nil
}

func chatSettingsDTO(settings control.AgentChatSettings) WhatsAppChatSettingsDTO {
	mode := ""
	switch settings.PromptOverrideMode {
	case agent.PromptAppend:
		mode = "append"
	case agent.PromptReplace:
		mode = "replace"
	}
	return WhatsAppChatSettingsDTO{
		Version: strconv.FormatUint(uint64(settings.Version), 10), ModerationLevel: uint8(settings.ModerationLevel),
		PromptOverrideMode: mode, PromptOverrideText: settings.PromptOverrideText,
		TriggerMention: settings.Triggers.Mention, TriggerName: settings.Triggers.Name, TriggerReply: settings.Triggers.Reply,
		TriggerNameRegex: settings.Triggers.NameRegex, TriggerNamePattern: settings.Triggers.NamePattern,
	}
}

func (s *AppService) SendWhatsAppMessage(chatID, text, replyToMessageID string) (WhatsAppMessageDTO, error) {
	var message control.BotMessage
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		var actionErr error
		if strings.TrimSpace(replyToMessageID) == "" {
			message, actionErr = runtime.SendChatMessage(ctx, chatID, text)
			return actionErr
		}
		replyRuntime, ok := runtime.(control.ManagedAgentChatReplyActions)
		if !ok {
			return errors.New("the active Agent runtime does not support chat replies")
		}
		message, actionErr = replyRuntime.SendChatReply(ctx, chatID, text, replyToMessageID)
		return actionErr
	})
	if err != nil {
		return WhatsAppMessageDTO{}, err
	}
	s.recordChatAction("INFO", "WhatsApp message sent from Chat", nil)
	return whatsAppMessage(message), nil
}

func (s *AppService) DeleteWhatsAppMessage(chatID, messageID string) error {
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		return runtime.DeleteChatMessage(ctx, chatID, messageID)
	})
	if err != nil {
		s.recordChatAction("WARN", "Could not delete WhatsApp message", err)
		return err
	}
	s.recordChatAction("INFO", "WhatsApp message deleted", nil)
	return nil
}

func (s *AppService) KickWhatsAppGroupMember(chatID, memberID string) error {
	err := s.withChatActions(func(runtime control.ManagedAgentChatActions, ctx context.Context) error {
		return runtime.KickGroupMember(ctx, chatID, memberID)
	})
	if err != nil {
		s.recordChatAction("WARN", "Could not remove group member", err)
		return err
	}
	s.recordChatAction("INFO", "Group member removed from WhatsApp", nil)
	return nil
}

func (s *AppService) withChatActions(action func(control.ManagedAgentChatActions, context.Context) error) error {
	return s.withChatActionsTimeout(chatActionTimeout, action)
}

func (s *AppService) withChatActionsTimeout(timeout time.Duration, action func(control.ManagedAgentChatActions, context.Context) error) error {
	return s.withManagedChatActions(timeout, "WhatsApp action from Chat failed", action)
}

func (s *AppService) withBroadcastActions(timeout time.Duration, action func(control.ManagedAgentChatActions, context.Context) error) error {
	return s.withManagedChatActions(timeout, "WhatsApp broadcast action failed", action)
}

func (s *AppService) withManagedChatActions(timeout time.Duration, failureLogMessage string, action func(control.ManagedAgentChatActions, context.Context) error) error {
	if s == nil || s.agent == nil {
		return errors.New("Agent chat actions are not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := s.agent.WithChatActions(ctx, func(runtime control.ManagedAgentChatActions) error {
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
			case "use WhatsApp chat actions", "send WhatsApp text", "list WhatsApp group members", "kick WhatsApp group member":
				return errors.New("The Agent is running, but its WhatsApp connection is not ready. Check the WhatsApp status on Overview and try again.")
			case "list WhatsApp broadcast groups", "send WhatsApp broadcast", "schedule WhatsApp broadcast":
				return errors.New("The Agent is running, but its WhatsApp connection is not ready. Check the WhatsApp status on Overview and try again.")
			}
		}
		return errors.New("The Agent or WhatsApp connection is not ready. Check both statuses and try again.")
	case agent.ErrorPermissionDenied:
		var operation interface{ Operation() string }
		if errors.As(err, &operation) && operation.Operation() == "authorize WhatsApp message deletion" {
			return errors.New("The bot's WhatsApp account must be a group admin to delete members' messages.")
		}
		return errors.New("This action is not allowed in this conversation.")
	case agent.ErrorNotFound:
		var operation interface{ Operation() string }
		if errors.As(err, &operation) {
			switch operation.Operation() {
			case "kick WhatsApp group member":
				return errors.New("The group member list has changed. Refresh it and try again.")
			case "send WhatsApp broadcast":
				return errors.New("The selected group list expired. Refresh the groups and try again.")
			case "schedule WhatsApp broadcast":
				return errors.New("The selected group list changed. Refresh the groups and try again.")
			case "resolve chat target":
				return errors.New("This chat is not available on the current Agent connection. Reload the chat list.")
			case "resolve message target", "resolve WhatsApp effect target", "delete WhatsApp chat message", "delete WhatsApp message":
				return errors.New("This message is no longer available for that action. Reload the chat history.")
			case "compare and swap config":
				return errors.New("Chat settings are not available. Close and reopen the settings panel.")
			}
		}
		return errors.New("Chat data is no longer available. Reload the chat list.")
	case agent.ErrorInvalidArgument:
		var operation interface{ Operation() string }
		if errors.As(err, &operation) {
			switch operation.Operation() {
			case "validate WhatsApp broadcast payload":
				return errors.New("The JSON must match the WhatsApp waE2E.Message payload format.")
			case "validate WhatsApp broadcast timing":
				return errors.New("Batch size must be 1–100 and the pause must be 0–300 seconds.")
			case "schedule WhatsApp broadcast":
				return errors.New("Choose a future date within the next year.")
			}
		}
		return errors.New("The action data is invalid.")
	case agent.ErrorTimeout:
		return errors.New("WhatsApp did not respond before the timeout.")
	case agent.ErrorConflict:
		var operation interface{ Operation() string }
		if errors.As(err, &operation) && operation.Operation() == "cancel WhatsApp broadcast schedule" {
			return errors.New("This schedule has already started or was cancelled.")
		}
		return errors.New("Chat settings have changed. Close and reopen the Chat settings panel.")
	default:
		return errors.New("The action failed. Check the Logs page for details.")
	}
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
	s.recordChatActionDetails(level, message, details)
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
	s.logs.Record(level, message, details)
}

// Ping provides an explicit UI action for checking the Go-to-frontend event path.
func (s *AppService) Ping() error {
	if s.app == nil {
		return errors.New("wails application is not initialized")
	}

	event := PingEvent{
		Message:  "pong",
		Sequence: s.sequence.Add(1),
	}
	s.app.Event.Emit("app:ping", event)
	return nil
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

func (s *AppService) GetWhatsAppSessionStatus() (WhatsAppSessionStatusDTO, error) {
	if s.sessions == nil {
		return WhatsAppSessionStatusDTO{}, errors.New("WhatsApp session controller is not initialized")
	}
	status, err := s.sessions.GetStatus(context.Background())
	if err != nil {
		return WhatsAppSessionStatusDTO{}, err
	}
	dto := whatsappSessionStatusDTO(status)
	if s.agent != nil && s.agent.IsActive() {
		agentStatus, agentErr := s.agent.GetStatus(context.Background())
		if agentErr != nil {
			return WhatsAppSessionStatusDTO{}, agentErr
		}
		dto.AgentActive = true
		dto.RuntimeState = agentStatus.WhatsAppState
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

func (s *AppService) BeginWhatsAppPairing(request BeginWhatsAppPairingRequestDTO) (WhatsAppSessionOperationDTO, error) {
	if s.sessions == nil {
		return WhatsAppSessionOperationDTO{}, errors.New("WhatsApp session controller is not initialized")
	}
	var operation control.SessionOperation
	err := s.withSessionControl(func() error {
		var err error
		operation, err = s.sessions.BeginPairing(context.Background(), control.BeginPairingRequest{Method: control.PairingMethod(request.Method), Phone: request.Phone})
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not start WhatsApp pairing", err)
		return WhatsAppSessionOperationDTO{}, err
	}
	s.recordLog("INFO", "WhatsApp pairing started", nil)
	return whatsappSessionOperationDTO(operation), nil
}

func (s *AppService) ResumeWhatsAppSession() (WhatsAppSessionOperationDTO, error) {
	if s.sessions == nil {
		return WhatsAppSessionOperationDTO{}, errors.New("WhatsApp session controller is not initialized")
	}
	var operation control.SessionOperation
	err := s.withSessionControl(func() error {
		var err error
		operation, err = s.sessions.Resume(context.Background())
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not resume WhatsApp session", err)
		return WhatsAppSessionOperationDTO{}, err
	}
	s.recordLog("INFO", "Resuming WhatsApp session", nil)
	return whatsappSessionOperationDTO(operation), nil
}

func (s *AppService) StopWhatsAppSession() (WhatsAppSessionStatusDTO, error) {
	if s.sessions == nil {
		return WhatsAppSessionStatusDTO{}, errors.New("WhatsApp session controller is not initialized")
	}
	var status control.SessionStatus
	err := s.withSessionControl(func() error {
		var err error
		status, err = s.sessions.Stop(context.Background())
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not stop WhatsApp session", err)
		return WhatsAppSessionStatusDTO{}, err
	}
	s.recordLog("INFO", "WhatsApp session stopped", nil)
	return whatsappSessionStatusDTO(status), nil
}

func (s *AppService) CancelWhatsAppPairing(operationID string) (WhatsAppSessionStatusDTO, error) {
	if s.sessions == nil {
		return WhatsAppSessionStatusDTO{}, errors.New("WhatsApp session controller is not initialized")
	}
	var status control.SessionStatus
	err := s.withSessionControl(func() error {
		var err error
		status, err = s.sessions.CancelPairing(context.Background(), operationID)
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not cancel WhatsApp pairing", err)
		return WhatsAppSessionStatusDTO{}, err
	}
	s.recordLog("INFO", "WhatsApp pairing canceled", nil)
	return whatsappSessionStatusDTO(status), nil
}

func (s *AppService) ReconnectWhatsAppSession() (WhatsAppSessionOperationDTO, error) {
	if s.sessions == nil {
		return WhatsAppSessionOperationDTO{}, errors.New("WhatsApp session controller is not initialized")
	}
	var operation control.SessionOperation
	err := s.withSessionControl(func() error {
		var err error
		operation, err = s.sessions.Reconnect(context.Background())
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not reconnect WhatsApp session", err)
		return WhatsAppSessionOperationDTO{}, err
	}
	s.recordLog("INFO", "WhatsApp reconnection started", nil)
	return whatsappSessionOperationDTO(operation), nil
}

func (s *AppService) LogoutWhatsAppSession() (WhatsAppSessionOperationDTO, error) {
	if s.sessions == nil {
		return WhatsAppSessionOperationDTO{}, errors.New("WhatsApp session controller is not initialized")
	}
	var operation control.SessionOperation
	err := s.withSessionControl(func() error {
		var err error
		operation, err = s.sessions.Logout(context.Background())
		return err
	})
	if err != nil {
		s.recordLog("ERROR", "Could not log out of WhatsApp", err)
		return WhatsAppSessionOperationDTO{}, err
	}
	s.recordLog("INFO", "WhatsApp logout completed", nil)
	return whatsappSessionOperationDTO(operation), nil
}
