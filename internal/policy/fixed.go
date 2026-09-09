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
	HumanAccessReader
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

func (gate *FixedGate) AuthorizeInvocation(ctx context.Context, message conversation.IncomingMessage, permission agent.PermissionConfig) error {
	if !gate.enabled.Load() || message.FromMe || message.ChatKind == conversation.ChatStatus ||
		(message.ChatKind == conversation.ChatGroup && !message.MentionsBot && !message.RepliedToBot) {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize invocation", fmt.Errorf("message is not eligible"))
	}
	principal, err := HumanPrincipal(message)
	if err != nil {
		return err
	}
	return gate.authorizeHuman(ctx, principal, permission, false)
}

// AuthorizeCommand is intentionally separate from Agent. The principal was
// formed from a durably resolved LID at inbound intake, while this method
// rereads the current account policy before a command changes chat state.
func (gate *FixedGate) AuthorizeCommand(ctx context.Context, principal Principal, capability Capability, permission agent.PermissionConfig) error {
	if !gate.enabled.Load() || principal.Kind != PrincipalHuman {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize command", fmt.Errorf("eligible human principal is required"))
	}
	requiresOwner := false
	switch capability {
	case CapabilityCommandHelp, CapabilityCommandInfo:
	case CapabilityHistoryReset, CapabilityPromptWrite:
		requiresOwner = true
	default:
		return agent.NewError(agent.ErrorPermissionDenied, "authorize command", fmt.Errorf("command capability is not enabled"))
	}
	return gate.authorizeHuman(ctx, principal, permission, requiresOwner)
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

func (gate *FixedGate) authorizeHuman(ctx context.Context, principal Principal, permission agent.PermissionConfig, requiresOwner bool) error {
	if err := principal.Validate(); err != nil {
		return err
	}
	if err := gate.requirePolicy(permission); err != nil {
		return err
	}
	access, err := gate.chats.ReadHumanAccess(ctx, principal)
	if err != nil {
		if agent.IsCode(err, agent.ErrorNotFound) {
			return agent.NewError(agent.ErrorPermissionDenied, "read human command access", fmt.Errorf("principal is no longer current"))
		}
		return err
	}
	if err := access.Validate(); err != nil {
		return err
	}
	if !access.Allowlisted || access.ChatKind == conversation.ChatStatus || (requiresOwner && !access.ConfiguredOwner) {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize human access", fmt.Errorf("current chat policy denies principal"))
	}
	return nil
}
