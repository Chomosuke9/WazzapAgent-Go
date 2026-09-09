package policy

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type ConfigReader interface {
	Load(context.Context, agent.Key) (agent.ConfigSnapshot, error)
}

type ChatAccess interface {
	IsChatAllowlisted(context.Context, agent.Key) (bool, error)
}

// FixedGate is the deliberately narrow text-conversation policy. It never
// delegates authority to senderRef, prompt content, or model output.
type FixedGate struct {
	policyID identity.PolicyID
	revision uint64
	configs  ConfigReader
	chats    ChatAccess
	enabled  atomic.Bool
}

func NewFixedGate(policyID identity.PolicyID, revision uint64, configs ConfigReader, chats ChatAccess, enabled bool) (*FixedGate, error) {
	if policyID.IsZero() || revision == 0 || configs == nil || chats == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create fixed policy", fmt.Errorf("policy reference and stores are required"))
	}
	gate := &FixedGate{policyID: policyID, revision: revision, configs: configs, chats: chats}
	gate.enabled.Store(enabled)
	return gate, nil
}

func (gate *FixedGate) SetEnabled(enabled bool) { gate.enabled.Store(enabled) }
func (gate *FixedGate) Enabled() bool           { return gate.enabled.Load() }

func (gate *FixedGate) AuthorizeInvocation(_ context.Context, message conversation.IncomingMessage, permission agent.PermissionConfig) error {
	if !gate.enabled.Load() || !message.Allowlisted || message.FromMe || message.ChatKind == conversation.ChatStatus ||
		(message.ChatKind == conversation.ChatGroup && !message.MentionsBot && !message.RepliedToBot) {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize invocation", fmt.Errorf("message is not eligible"))
	}
	return gate.requirePolicy(permission)
}

func (gate *FixedGate) AuthorizePrompt(_ context.Context, message conversation.IncomingMessage, permission agent.PermissionConfig) error {
	if !gate.enabled.Load() || !message.Allowlisted || !message.Owner || message.FromMe || message.ChatKind == conversation.ChatStatus {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize prompt command", fmt.Errorf("configured owner is required"))
	}
	return gate.requirePolicy(permission)
}

func (gate *FixedGate) AuthorizeHistoryReset(_ context.Context, message conversation.IncomingMessage, permission agent.PermissionConfig) error {
	if !gate.enabled.Load() || !message.Allowlisted || !message.Owner || message.FromMe || message.ChatKind == conversation.ChatStatus {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize history reset", fmt.Errorf("configured owner is required"))
	}
	return gate.requirePolicy(permission)
}

func (gate *FixedGate) AuthorizeSend(ctx context.Context, key agent.Key) error {
	if !gate.enabled.Load() {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize send", fmt.Errorf("agent kill switch is disabled"))
	}
	allowed, err := gate.chats.IsChatAllowlisted(ctx, key)
	if err != nil {
		return err
	}
	if !allowed {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize send", fmt.Errorf("chat is not allowlisted"))
	}
	snapshot, err := gate.configs.Load(ctx, key)
	if err != nil {
		return err
	}
	return gate.requirePolicy(snapshot.Permission)
}

func (gate *FixedGate) requirePolicy(permission agent.PermissionConfig) error {
	if permission.PolicyID != gate.policyID || permission.Revision != gate.revision {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize policy reference", fmt.Errorf("unsupported policy revision"))
	}
	return nil
}
