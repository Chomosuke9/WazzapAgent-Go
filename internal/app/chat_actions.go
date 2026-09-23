package app

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/action"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/effect"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
)

func (application *Application) SendChatMessage(ctx context.Context, chatID, text string) (control.BotMessage, error) {
	runtime, key, err := application.chatActionScope(chatID)
	if err != nil {
		return control.BotMessage{}, err
	}
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > agent.MaxHistoryBytes {
		return control.BotMessage{}, agent.NewError(agent.ErrorInvalidArgument, "send WhatsApp chat message", errors.New("message must be non-empty and within the supported length"))
	}
	if err := runtime.gate.AuthorizeSend(ctx, key); err != nil {
		return control.BotMessage{}, err
	}
	messageID, err := identity.NewMessageID()
	if err != nil {
		return control.BotMessage{}, agent.NewError(agent.ErrorInternal, "create WhatsApp message ID", errors.New("could not allocate a message identifier"))
	}
	actionID, err := identity.NewActionID()
	if err != nil {
		return control.BotMessage{}, agent.NewError(agent.ErrorInternal, "create WhatsApp action ID", errors.New("could not allocate an action identifier"))
	}
	invocationID, err := identity.NewInvocationID()
	if err != nil {
		return control.BotMessage{}, agent.NewError(agent.ErrorInternal, "create WhatsApp invocation ID", errors.New("could not allocate an invocation identifier"))
	}
	causationID, err := identity.NewCausationID()
	if err != nil {
		return control.BotMessage{}, agent.NewError(agent.ErrorInternal, "create WhatsApp request ID", errors.New("could not allocate a request identifier"))
	}
	sent, err := runtime.adapter.SendText(ctx, action.SendTextRequest{Key: key, ActionID: actionID, Text: text})
	if err != nil {
		return control.BotMessage{}, err
	}
	if strings.TrimSpace(sent.ProviderReceipt) == "" {
		return control.BotMessage{}, agent.NewError(agent.ErrorUnknownOutcome, "send WhatsApp chat message", errors.New("WhatsApp returned no message receipt"))
	}
	createdAt := time.Now().UTC()
	entry := agent.HistoryEntry{
		MessageID: messageID, InvocationID: invocationID,
		Causation: agent.CausationRef{Kind: agent.CausationRequest, ID: causationID},
		Role:      agent.HistoryAssistant, Content: []agent.ContentPart{agent.TextPart{Text: text}},
		Delivery: agent.DeliverySucceeded, CreatedAt: createdAt,
	}
	if err := runtime.store.RecordManualAssistantMessage(ctx, key, entry, sent.ProviderReceipt); err != nil {
		return control.BotMessage{}, agent.NewError(agent.ErrorUnknownOutcome, "record sent WhatsApp chat message", errors.New("WhatsApp accepted the message but its local transcript could not be updated"))
	}
	return control.BotMessage{
		ID: messageID, Role: "assistant", Sender: "Bot", Content: text,
		CreatedAt: createdAt.Format(time.RFC3339Nano), Delivery: "sent",
	}, nil
}

func (application *Application) DeleteChatMessage(ctx context.Context, chatID, messageID string) error {
	runtime, key, err := application.chatActionScope(chatID)
	if err != nil {
		return err
	}
	targetID, err := identity.ParseMessageID(messageID)
	if err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "delete WhatsApp chat message", errors.New("message identifier is invalid"))
	}
	if err := runtime.gate.AuthorizeSend(ctx, key); err != nil {
		return err
	}
	settings, err := runtime.store.Configs().Load(ctx, key)
	if err != nil {
		return err
	}
	history, err := runtime.store.History().ListIfConfigVersion(ctx, key, settings.Version, agent.HistoryQuery{Limit: agent.MaxHistoryPageSize})
	if err != nil {
		return err
	}
	deletableMessage := false
	for _, entry := range history.Entries {
		if entry.MessageID != targetID {
			continue
		}
		if entry.Role == agent.HistoryUser || entry.Role == agent.HistoryAssistant && entry.Delivery == agent.DeliverySucceeded {
			deletableMessage = true
			break
		}
	}
	if !deletableMessage {
		return agent.NewError(agent.ErrorPermissionDenied, "delete WhatsApp chat message", errors.New("message is not an incoming chat message or a sent Agent message"))
	}
	deleted, err := runtime.store.IsMessageDeleted(ctx, key, targetID)
	if err != nil {
		return err
	}
	if deleted {
		return nil
	}
	effectID, err := identity.NewEffectID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "create delete effect ID", errors.New("could not allocate a deletion identifier"))
	}
	invocationID, err := identity.NewInvocationID()
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "create delete invocation ID", errors.New("could not allocate a deletion invocation"))
	}
	principal, err := policy.SystemPrincipal(key)
	if err != nil {
		return err
	}
	request := effect.PlanRequest{
		Ref: effect.Ref{Key: key, EffectID: effectID}, InvocationID: invocationID,
		Principal: principal, Effect: effect.DeleteMessage{TargetMessageID: targetID},
	}
	stored, err := runtime.effectDispatcher.Plan(ctx, request)
	if err != nil {
		return err
	}
	return runtime.effectDispatcher.Dispatch(ctx, stored.Request.Ref)
}

func (application *Application) ListGroupMembers(ctx context.Context, chatID string) (control.AgentGroupMembers, error) {
	runtime, key, err := application.chatActionScope(chatID)
	if err != nil {
		return control.AgentGroupMembers{}, err
	}
	group, err := runtime.adapter.ListGroupMembers(ctx, key)
	if err != nil {
		return control.AgentGroupMembers{}, err
	}
	result := control.AgentGroupMembers{BotIsAdmin: group.BotIsAdmin, Members: make([]control.AgentGroupMember, len(group.Members))}
	for index, member := range group.Members {
		result.Members[index] = control.AgentGroupMember{
			ID: member.ID, Name: member.Name, IsAdmin: member.IsAdmin,
			IsSuperAdmin: member.IsSuperAdmin, CanKick: member.CanKick,
		}
	}
	return result, nil
}

func (application *Application) GetChatSettings(ctx context.Context, chatID string) (control.AgentChatSettings, error) {
	runtime, key, err := application.chatActionScope(chatID)
	if err != nil {
		return control.AgentChatSettings{}, err
	}
	snapshot, err := runtime.store.Configs().LoadOrCreate(ctx, key, runtime.configDefaults)
	if err != nil {
		return control.AgentChatSettings{}, err
	}
	return chatSettingsDTO(snapshot), nil
}

func (application *Application) SaveChatSettings(ctx context.Context, chatID string, update control.AgentChatSettingsUpdate) (control.AgentChatSettings, error) {
	runtime, key, err := application.chatActionScope(chatID)
	if err != nil {
		return control.AgentChatSettings{}, err
	}
	if update.ExpectedVersion == 0 || !update.ModerationLevel.Valid() {
		return control.AgentChatSettings{}, agent.NewError(agent.ErrorInvalidArgument, "save WhatsApp chat settings", errors.New("settings revision and a valid moderation level are required"))
	}
	snapshot, err := runtime.store.Configs().LoadOrCreate(ctx, key, runtime.configDefaults)
	if err != nil {
		return control.AgentChatSettings{}, err
	}
	values := snapshot.Values()
	values.Permission.ModerationLevel = update.ModerationLevel
	values.Triggers = update.Triggers
	if strings.TrimSpace(update.PromptOverrideText) == "" {
		values.PromptOverride = nil
	} else {
		if update.PromptOverrideMode != agent.PromptAppend && update.PromptOverrideMode != agent.PromptReplace {
			return control.AgentChatSettings{}, agent.NewError(agent.ErrorInvalidArgument, "save WhatsApp chat settings", errors.New("prompt mode must append or replace"))
		}
		values.PromptOverride = &agent.PromptOverride{Mode: update.PromptOverrideMode, Text: update.PromptOverrideText}
	}
	updated, err := runtime.store.Configs().CompareAndSwap(ctx, key, update.ExpectedVersion, values)
	if err != nil {
		return control.AgentChatSettings{}, err
	}
	return chatSettingsDTO(updated), nil
}

func chatSettingsDTO(snapshot agent.ConfigSnapshot) control.AgentChatSettings {
	result := control.AgentChatSettings{Version: snapshot.Version, ModerationLevel: snapshot.Permission.ModerationLevel, Triggers: snapshot.Triggers}
	if snapshot.PromptOverride != nil {
		result.PromptOverrideMode = snapshot.PromptOverride.Mode
		result.PromptOverrideText = snapshot.PromptOverride.Text
	}
	return result
}

func (application *Application) KickGroupMember(ctx context.Context, chatID, memberID string) error {
	runtime, key, err := application.chatActionScope(chatID)
	if err != nil {
		return err
	}
	if err := runtime.gate.AuthorizeSend(ctx, key); err != nil {
		return err
	}
	return runtime.adapter.KickGroupMember(ctx, key, memberID)
}

func (application *Application) chatActionScope(chatID string) (*conversationRuntime, agent.Key, error) {
	if application == nil || !application.ready.Load() {
		return nil, agent.Key{}, agent.NewError(agent.ErrorNotReady, "use WhatsApp chat actions", errors.New("Agent runtime is not ready"))
	}
	runtime := application.runtimeState.Load()
	if runtime == nil || runtime.adapter == nil || runtime.store == nil || runtime.gate == nil {
		return nil, agent.Key{}, agent.NewError(agent.ErrorNotReady, "use WhatsApp chat actions", errors.New("WhatsApp chat runtime is not ready"))
	}
	chat, err := identity.ParseChatID(chatID)
	if err != nil {
		return nil, agent.Key{}, agent.NewError(agent.ErrorInvalidArgument, "use WhatsApp chat actions", errors.New("conversation identifier is invalid"))
	}
	key := agent.Key{TenantID: application.config.TenantID(), AccountID: application.config.AccountID(), ChatID: chat}
	if err := key.Validate(); err != nil {
		return nil, agent.Key{}, err
	}
	return runtime, key, nil
}
