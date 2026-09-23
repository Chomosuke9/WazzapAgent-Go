package control

import (
	"context"
	"errors"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
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

// ManagedAgentChatActions is an optional extension implemented by desktop
// Agent runtimes. Session-only clients intentionally do not implement it.
type ManagedAgentChatActions interface {
	SendChatMessage(context.Context, string, string) (BotMessage, error)
	DeleteChatMessage(context.Context, string, string) error
	ListGroupMembers(context.Context, string) (AgentGroupMembers, error)
	KickGroupMember(context.Context, string, string) error
	GetChatSettings(context.Context, string) (AgentChatSettings, error)
	SaveChatSettings(context.Context, string, AgentChatSettingsUpdate) (AgentChatSettings, error)
}

// WithChatActions holds the lifecycle operation lock while a bounded UI action
// uses the active runtime. This prevents Stop or settings apply from closing
// the WhatsApp client or SQLite store underneath the action.
func (controller *AgentController) WithChatActions(ctx context.Context, action func(ManagedAgentChatActions) error) error {
	if controller == nil || action == nil {
		return agent.NewError(agent.ErrorUnavailable, "use Agent chat actions", errors.New("Agent chat actions are unavailable"))
	}
	ctx = nonNilContext(ctx)
	controller.operations.Lock()
	defer controller.operations.Unlock()

	controller.mu.RLock()
	run, state, closed := controller.run, controller.state, controller.closed
	controller.mu.RUnlock()
	if closed {
		return agent.NewError(agent.ErrorNotReady, "use Agent chat actions", errors.New("Agent controller is closing"))
	}
	if run == nil || state != BotRunning {
		return agent.NewError(agent.ErrorNotReady, "use Agent chat actions", errors.New("start the Agent before managing WhatsApp chats"))
	}
	chatActions, ok := run.runtime.(ManagedAgentChatActions)
	if !ok || chatActions == nil {
		return agent.NewError(agent.ErrorUnsupported, "use Agent chat actions", errors.New("active runtime does not support chat actions"))
	}
	if err := ctx.Err(); err != nil {
		return agent.NewError(agent.ErrorCancelled, "use Agent chat actions", err)
	}
	return action(chatActions)
}
