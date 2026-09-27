package policy

import (
	"context"
	"errors"
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
	policyID      identity.PolicyID
	revision      uint64
	configs       ConfigReader
	chats         ChatAccess
	authority     ChatAuthorityReader
	assistantName string
	enabled       atomic.Bool
}

func NewFixedGate(policyID identity.PolicyID, revision uint64, configs ConfigReader, chats ChatAccess, authority ChatAuthorityReader, assistantName string, enabled bool) (*FixedGate, error) {
	if policyID.IsZero() || revision == 0 || configs == nil || chats == nil || authority == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create fixed policy", errors.New("policy reference and stores are required"))
	}
	gate := &FixedGate{policyID: policyID, revision: revision, configs: configs, chats: chats, authority: authority, assistantName: assistantName}
	gate.enabled.Store(enabled)
	return gate, nil
}

func (gate *FixedGate) SetEnabled(enabled bool) { gate.enabled.Store(enabled) }
func (gate *FixedGate) Enabled() bool           { return gate.enabled.Load() }

func (gate *FixedGate) AuthorizeInvocation(ctx context.Context, message conversation.IncomingMessage, snapshot agent.ConfigSnapshot) error {
	if !gate.enabled.Load() || message.FromMe || message.ChatKind == conversation.ChatStatus ||
		(message.ChatKind == conversation.ChatGroup && !snapshot.Triggers.Matches(message.MentionsBot, message.RepliedToBot, conversation.AuthoredText(message.Text), gate.assistantName)) {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize invocation", errors.New("message is not eligible"))
	}
	principal, err := HumanPrincipal(message)
	if err != nil {
		return err
	}
	return gate.authorizeHuman(ctx, principal, snapshot.Permission, false)
}

// CommandPermissionFacts resolves the facts consumed by a command's
// declarative permission expression. Human facts come from the current
// durable participant/chat record plus the latest synchronized group snapshot. A
// model/bot invocation is represented by PrincipalModel and receives
// fromMe=true and the bot's current group-admin fact; it never receives the
// owner fact.
//
// The bool is intentionally explicit. Callers must not infer bot origin from
// a sender ID or from command text.
func (gate *FixedGate) CommandPermissionFacts(
	ctx context.Context,
	principal Principal,
	permission agent.PermissionConfig,
	fromMe bool,
) (PermissionFacts, error) {
	if !gate.enabled.Load() {
		return PermissionFacts{}, agent.NewError(agent.ErrorPermissionDenied, "resolve command permission facts", errors.New("agent kill switch is disabled"))
	}
	if err := principal.Validate(); err != nil {
		return PermissionFacts{}, err
	}
	if err := gate.requirePolicy(permission); err != nil {
		return PermissionFacts{}, err
	}

	switch principal.Kind {
	case PrincipalHuman:
		if fromMe {
			return PermissionFacts{}, agent.NewError(agent.ErrorPermissionDenied, "resolve command permission facts", errors.New("human principals cannot be marked as bot-originated"))
		}
		access, err := gate.chats.ReadHumanAccess(ctx, principal)
		if err != nil {
			if agent.IsCode(err, agent.ErrorNotFound) {
				return PermissionFacts{}, agent.NewError(agent.ErrorPermissionDenied, "read human command access", errors.New("principal is no longer current"))
			}
			return PermissionFacts{}, err
		}
		if err := access.Validate(); err != nil {
			return PermissionFacts{}, agent.NewError(agent.ErrorIntegrityFailure, "resolve human command permission facts", err)
		}
		if !access.Allowlisted || access.ChatKind == conversation.ChatStatus {
			return PermissionFacts{}, agent.NewError(agent.ErrorPermissionDenied, "resolve human command permission facts", errors.New("current chat policy denies principal"))
		}
		authority, err := gate.authority.ReadChatAuthority(ctx, principal)
		if err != nil {
			return PermissionFacts{}, err
		}
		if err := authority.Validate(); err != nil {
			return PermissionFacts{}, err
		}
		return PermissionFacts{
			IsOwner:    access.ConfiguredOwner,
			IsAdmin:    authority.ActorIsAdmin,
			BotIsAdmin: authority.BotIsAdmin,
			IsGroup:    authority.ChatKind == conversation.ChatGroup,
			IsPrivate:  authority.ChatKind == conversation.ChatDirect,
			FromMe:     false,
		}, nil

	case PrincipalModel:
		if !fromMe {
			return PermissionFacts{}, agent.NewError(agent.ErrorPermissionDenied, "resolve bot command permission facts", errors.New("model principals must be explicitly marked fromMe"))
		}
		current, err := gate.configs.Load(ctx, principal.Key())
		if err != nil {
			return PermissionFacts{}, err
		}
		if current.Permission != permission {
			return PermissionFacts{}, agent.NewError(agent.ErrorConflict, "resolve bot command permission facts", errors.New("command permission snapshot is stale"))
		}
		allowed, err := gate.chats.IsChatAllowlisted(ctx, principal.Key())
		if err != nil {
			return PermissionFacts{}, err
		}
		if !allowed {
			return PermissionFacts{}, agent.NewError(agent.ErrorPermissionDenied, "resolve bot command permission facts", errors.New("chat is not allowlisted"))
		}
		authority, err := gate.authority.ReadChatAuthority(ctx, principal)
		if err != nil {
			return PermissionFacts{}, err
		}
		if err := authority.Validate(); err != nil {
			return PermissionFacts{}, err
		}
		return PermissionFacts{
			IsOwner:    false,
			IsAdmin:    authority.BotIsAdmin,
			BotIsAdmin: authority.BotIsAdmin,
			IsGroup:    authority.ChatKind == conversation.ChatGroup,
			IsPrivate:  authority.ChatKind == conversation.ChatDirect,
			FromMe:     true,
		}, nil
	default:
		return PermissionFacts{}, agent.NewError(agent.ErrorPermissionDenied, "resolve command permission facts", errors.New("only human or model principals may dispatch commands"))
	}
}

// AuthorizeEffect is deliberately outside Agent. It reevaluates the durable
// policy and reads the synchronized group snapshot just before the native dispatcher
// crosses its boundary. Only a principal minted for this model invocation can
// use an opt-in model capability.
func (gate *FixedGate) AuthorizeEffect(ctx context.Context, request EffectAuthorization) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if !gate.enabled.Load() {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize effect", errors.New("model effect is not eligible"))
	}
	if request.Principal.Kind == PrincipalSystem {
		return gate.authorizeDesktopEffect(ctx, request)
	}
	if request.Principal.Kind != PrincipalModel {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize effect", errors.New("effect principal is not eligible"))
	}
	allowed, err := gate.chats.IsChatAllowlisted(ctx, request.Key)
	if err != nil {
		return err
	}
	if !allowed {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize effect", errors.New("chat is not allowlisted"))
	}
	snapshot, err := gate.configs.Load(ctx, request.Key)
	if err != nil {
		return err
	}
	if err := gate.requirePolicy(snapshot.Permission); err != nil {
		return err
	}
	if !snapshot.Permission.ModelToolCapabilities().Has(request.Capability) {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize effect", errors.New("model capability is not currently granted"))
	}
	return nil
}

// authorizeDesktopEffect is reserved for explicit local UI actions. It does
// not grant model tools: the desktop verifies the transcript target, while the
// provider adapter rechecks group-admin authority before revoking another
// participant's message.
func (gate *FixedGate) authorizeDesktopEffect(ctx context.Context, request EffectAuthorization) error {
	if request.Capability != agent.CapabilityMessageDelete {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize desktop effect", errors.New("desktop effect capability is not supported"))
	}
	allowed, err := gate.chats.IsChatAllowlisted(ctx, request.Key)
	if err != nil {
		return err
	}
	if !allowed {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize desktop effect", errors.New("chat is not allowlisted"))
	}
	snapshot, err := gate.configs.Load(ctx, request.Key)
	if err != nil {
		return err
	}
	if err := gate.requirePolicy(snapshot.Permission); err != nil {
		return err
	}
	return nil
}

func (gate *FixedGate) AuthorizeSend(ctx context.Context, key agent.Key) error {
	if !gate.enabled.Load() {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize send", errors.New("agent kill switch is disabled"))
	}
	allowed, err := gate.chats.IsChatAllowlisted(ctx, key)
	if err != nil {
		return err
	}
	if !allowed {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize send", errors.New("chat is not allowlisted"))
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
		return agent.NewError(agent.ErrorPermissionDenied, "authorize policy reference", errors.New("unsupported policy revision"))
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
			return agent.NewError(agent.ErrorPermissionDenied, "read human command access", errors.New("principal is no longer current"))
		}
		return err
	}
	if err := access.Validate(); err != nil {
		return err
	}
	if !access.Allowlisted || access.ChatKind == conversation.ChatStatus || (requiresOwner && !access.ConfiguredOwner) {
		return agent.NewError(agent.ErrorPermissionDenied, "authorize human access", errors.New("current chat policy denies principal"))
	}
	return nil
}
