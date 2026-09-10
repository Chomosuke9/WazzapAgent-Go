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
	policyID  identity.PolicyID
	revision  uint64
	configs   ConfigReader
	chats     ChatAccess
	authority ChatAuthorityReader
	enabled   atomic.Bool
}

func NewFixedGate(policyID identity.PolicyID, revision uint64, configs ConfigReader, chats ChatAccess, authority ChatAuthorityReader, enabled bool) (*FixedGate, error) {
	if policyID.IsZero() || revision == 0 || configs == nil || chats == nil || authority == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create fixed policy", fmt.Errorf("policy reference and stores are required"))
	}
	gate := &FixedGate{policyID: policyID, revision: revision, configs: configs, chats: chats, authority: authority}
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
	case CapabilityHistoryReset, CapabilityPromptWrite, CapabilityPermissionWrite:
		requiresOwner = true
	default:
		return agent.NewError(agent.ErrorPermissionDenied, "authorize command", fmt.Errorf("command capability is not enabled"))
	}
	return gate.authorizeHuman(ctx, principal, permission, requiresOwner)
}

// AuthorizeEffect is deliberately outside Agent. It reevaluates the durable
// policy and reads live provider authority just before the native dispatcher
// crosses its boundary. Only a principal minted for this model invocation can
// use an opt-in model capability.
func (gate *FixedGate) AuthorizeEffect(ctx context.Context, request EffectAuthorization) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if !gate.enabled.Load() || request.Principal.Kind != PrincipalModel {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize effect", fmt.Errorf("model effect is not eligible"))
	}
	allowed, err := gate.chats.IsChatAllowlisted(ctx, request.Key)
	if err != nil {
		return err
	}
	if !allowed {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize effect", fmt.Errorf("chat is not allowlisted"))
	}
	snapshot, err := gate.configs.Load(ctx, request.Key)
	if err != nil {
		return err
	}
	if err := gate.requirePolicy(snapshot.Permission); err != nil {
		return err
	}
	if !snapshot.Permission.ModelToolCapabilities().Has(agent.Capability(request.Capability)) {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize effect", fmt.Errorf("model capability is not currently granted"))
	}
	authority, err := gate.authority.ReadChatAuthority(ctx, request.Principal)
	if err != nil {
		return err
	}
	if err := authority.Validate(); err != nil {
		return err
	}
	moderation := request.Capability == CapabilityGroupDelete || request.Capability == CapabilityGroupMute || request.Capability == CapabilityGroupKick
	if moderation && (authority.ChatKind != conversation.ChatGroup || !authority.BotIsAdmin) {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize effect", fmt.Errorf("bot group command requires current group-admin authority"))
	}
	return nil
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

// ModelCapabilities returns the policy-scoped tool set only after validating
// the immutable policy reference carried by the current config snapshot. The
// caller binds this set into one invocation digest; it is not a standing model
// role and is rechecked again by AuthorizeEffect before execution.
func (gate *FixedGate) ModelCapabilities(permission agent.PermissionConfig) (agent.CapabilitySet, error) {
	if err := gate.requirePolicy(permission); err != nil {
		return agent.CapabilitySet{}, err
	}
	return permission.ModelToolCapabilities(), nil
}

func (gate *FixedGate) requirePolicy(permission agent.PermissionConfig) error {
	if err := permission.Validate(); err != nil {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize policy reference", err)
	}
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
