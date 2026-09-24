//go:build gui

package wails

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
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
	if s == nil || s.agent == nil {
		return errors.New("Agent chat actions are not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatActionTimeout)
	defer cancel()
	err := s.agent.WithChatActions(ctx, func(runtime control.ManagedAgentChatActions) error {
		return action(runtime, ctx)
	})
	if err != nil {
		s.recordChatAction("WARN", "WhatsApp action from Chat failed", err)
		return safeChatActionError(err)
	}
	return nil
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
		return errors.New("The action data is invalid.")
	case agent.ErrorTimeout:
		return errors.New("WhatsApp did not respond before the timeout.")
	case agent.ErrorConflict:
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
