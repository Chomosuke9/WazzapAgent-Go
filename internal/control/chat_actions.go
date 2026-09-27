package control

import (
	"context"
	"errors"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
)

type ChatSettingsResetCategory string

const (
	ChatSettingsResetModeration ChatSettingsResetCategory = "moderation"
	ChatSettingsResetTriggers   ChatSettingsResetCategory = "triggers"
	ChatSettingsResetPrompt     ChatSettingsResetCategory = "instructions"
	ChatSettingsResetAll        ChatSettingsResetCategory = "all"
)

// AgentGroupMember contains a safe, short-lived group member handle for the
// local UI. Provider addresses never cross the WhatsApp adapter boundary.
type AgentGroupMember struct {
	ID           string
	Name         string
	IsAdmin      bool
	IsSuperAdmin bool
	CanKick      bool
}

type AgentGroupMembers struct {
	BotIsAdmin bool
	Members    []AgentGroupMember
}

type AgentBroadcastGroup struct {
	ID   string
	Name string
}

type AgentBroadcastGroupResult struct {
	ID        string
	Name      string
	Sent      bool
	ErrorCode string
}

type AgentBroadcastScheduleResult struct {
	Name      string
	Sent      bool
	ErrorCode string
}

type AgentBroadcastSchedule struct {
	ID                string
	ScheduledAt       time.Time
	BatchSize         int
	BatchDelaySeconds int
	GroupCount        int
	Status            string
	Results           []AgentBroadcastScheduleResult
}

type AgentChatSettings struct {
	Version            agent.ConfigVersion
	ModerationLevel    agent.ModerationLevel
	PromptOverrideMode agent.PromptOverrideMode
	PromptOverrideText string
	Triggers           agent.TriggerConfig
}

type AgentChatSettingsUpdate struct {
	ExpectedVersion    agent.ConfigVersion
	ModerationLevel    agent.ModerationLevel
	PromptOverrideMode agent.PromptOverrideMode
	PromptOverrideText string
	Triggers           agent.TriggerConfig
}

// ManagedAgentChatActions are the operator actions the UI runs against the
// live bot runtime.
type ManagedAgentChatActions interface {
	SendChatMessage(context.Context, string, string) (BotMessage, error)
	SendChatReply(context.Context, string, string, string) (BotMessage, error)
	DeleteChatMessage(context.Context, string, string) error
	ListGroupMembers(context.Context, string) (AgentGroupMembers, error)
	KickGroupMember(context.Context, string, string) error
	GetChatSettings(context.Context, string) (AgentChatSettings, error)
	SaveChatSettings(context.Context, string, AgentChatSettingsUpdate) (AgentChatSettings, error)
	ResetChatSettings(context.Context, ChatSettingsResetCategory, config.ChatDefaults) (int64, error)
	ListBroadcastGroups(context.Context) ([]AgentBroadcastGroup, error)
	BroadcastWhatsAppGroups(context.Context, []string, string, string, int, int) ([]AgentBroadcastGroupResult, error)
	ScheduleWhatsAppBroadcast(context.Context, []string, string, string, int, int, time.Time) (AgentBroadcastSchedule, error)
	ListWhatsAppBroadcastSchedules(context.Context) ([]AgentBroadcastSchedule, error)
	CancelWhatsAppBroadcastSchedule(context.Context, string) error
}

// WithChatActions runs a UI action against the running bot. Actions do not
// block each other or start/stop; the context handed to action is cancelled
// when the bot stops, and stop waits for the action to return before it
// closes the runtime.
func (controller *AgentController) WithChatActions(ctx context.Context, action func(context.Context, ManagedAgentChatActions) error) error {
	if controller == nil || action == nil {
		return agent.NewError(agent.ErrorUnavailable, "use Agent chat actions", errors.New("Agent chat actions are unavailable"))
	}
	ctx = nonNilContext(ctx)
	controller.actions.RLock()
	defer controller.actions.RUnlock()

	controller.mu.RLock()
	run, state, closed := controller.run, controller.state, controller.closed
	controller.mu.RUnlock()
	if closed {
		return agent.NewError(agent.ErrorNotReady, "use Agent chat actions", errors.New("Agent controller is closing"))
	}
	if run == nil || state != BotRunning {
		return agent.NewError(agent.ErrorNotReady, "use Agent chat actions", errors.New("start the Agent before managing WhatsApp chats"))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(run.actionsCtx, cancel)()
	if err := ctx.Err(); err != nil {
		return agent.NewError(agent.ErrorCancelled, "use Agent chat actions", err)
	}
	return action(ctx, run.runtime)
}
