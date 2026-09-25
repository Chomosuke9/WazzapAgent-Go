package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/control"
)

// ManagedAgentRuntimeFactory adapts the existing headless Application runtime
// to the GUI controller. It does not create a second message-processing path or
// start a diagnostics HTTP listener.
type ManagedAgentRuntimeFactory struct {
	Logger *slog.Logger
}

func (factory ManagedAgentRuntimeFactory) OpenAgentRuntime(_ context.Context, snapshot config.Snapshot) (control.ManagedAgentRuntime, error) {
	logger := factory.Logger
	if logger == nil {
		logger = slog.Default()
	}
	application := New(snapshot, logger, Options{SystemPolicy: RenderSystemPolicy(snapshot.AssistantName(), time.Now())})
	return &managedAgentRuntime{application: application}, nil
}

type managedAgentRuntime struct {
	application *Application
}

func (runtime *managedAgentRuntime) Run(ctx context.Context) error {
	return runtime.application.Run(ctx)
}

func (runtime *managedAgentRuntime) Close(ctx context.Context) error {
	return runtime.application.Close(ctx)
}

func (runtime *managedAgentRuntime) Snapshot() control.AgentRuntimeSnapshot {
	result := control.AgentRuntimeSnapshot{Started: runtime.application.Started(), WhatsAppState: "stopped"}
	if accountSnapshot, ok := runtime.application.WhatsAppSnapshot(); ok {
		result.WhatsAppState = accountSnapshot.State.String()
		result.WhatsAppErrorCode = accountSnapshot.ErrorCode
	}
	return result
}

func (runtime *managedAgentRuntime) SendChatMessage(ctx context.Context, chatID, text string) (control.BotMessage, error) {
	return runtime.application.SendChatMessage(ctx, chatID, text)
}

func (runtime *managedAgentRuntime) SendChatReply(ctx context.Context, chatID, text, replyToMessageID string) (control.BotMessage, error) {
	return runtime.application.SendChatReply(ctx, chatID, text, replyToMessageID)
}

func (runtime *managedAgentRuntime) DeleteChatMessage(ctx context.Context, chatID, messageID string) error {
	return runtime.application.DeleteChatMessage(ctx, chatID, messageID)
}

func (runtime *managedAgentRuntime) ListGroupMembers(ctx context.Context, chatID string) (control.AgentGroupMembers, error) {
	return runtime.application.ListGroupMembers(ctx, chatID)
}

func (runtime *managedAgentRuntime) KickGroupMember(ctx context.Context, chatID, memberID string) error {
	return runtime.application.KickGroupMember(ctx, chatID, memberID)
}

func (runtime *managedAgentRuntime) GetChatSettings(ctx context.Context, chatID string) (control.AgentChatSettings, error) {
	return runtime.application.GetChatSettings(ctx, chatID)
}

func (runtime *managedAgentRuntime) SaveChatSettings(ctx context.Context, chatID string, update control.AgentChatSettingsUpdate) (control.AgentChatSettings, error) {
	return runtime.application.SaveChatSettings(ctx, chatID, update)
}

func (runtime *managedAgentRuntime) ResetChatSettings(ctx context.Context, category control.ChatSettingsResetCategory, defaults config.ChatDefaults) (int64, error) {
	return runtime.application.ResetChatSettings(ctx, category, defaults)
}

func (runtime *managedAgentRuntime) ListBroadcastGroups(ctx context.Context) ([]control.AgentBroadcastGroup, error) {
	return runtime.application.ListBroadcastGroups(ctx)
}

func (runtime *managedAgentRuntime) BroadcastWhatsAppGroups(ctx context.Context, groupIDs []string, format, payload string, batchSize, batchDelaySeconds int) ([]control.AgentBroadcastGroupResult, error) {
	return runtime.application.BroadcastWhatsAppGroups(ctx, groupIDs, format, payload, batchSize, batchDelaySeconds)
}

func (runtime *managedAgentRuntime) ScheduleWhatsAppBroadcast(ctx context.Context, groupIDs []string, format, payload string, batchSize, batchDelaySeconds int, scheduledAt time.Time) (control.AgentBroadcastSchedule, error) {
	return runtime.application.ScheduleWhatsAppBroadcast(ctx, groupIDs, format, payload, batchSize, batchDelaySeconds, scheduledAt)
}

func (runtime *managedAgentRuntime) ListWhatsAppBroadcastSchedules(ctx context.Context) ([]control.AgentBroadcastSchedule, error) {
	return runtime.application.ListWhatsAppBroadcastSchedules(ctx)
}

func (runtime *managedAgentRuntime) CancelWhatsAppBroadcastSchedule(ctx context.Context, id string) error {
	return runtime.application.CancelWhatsAppBroadcastSchedule(ctx, id)
}
