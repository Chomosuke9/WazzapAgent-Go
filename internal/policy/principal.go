package policy

import (
	"context"
	"fmt"
	"sort"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

// Principal carries provenance that has already been verified at an external
// boundary. It is deliberately outside agent: the Agent has no authorization
// role and must never infer a principal from model or message content.
type PrincipalKind uint8

const (
	PrincipalHuman PrincipalKind = iota + 1
	PrincipalModel
	PrincipalSystem
	PrincipalRecovery
)

type Principal struct {
	Kind          PrincipalKind
	TenantID      identity.TenantID
	AccountID     identity.AccountID
	ChatID        identity.ChatID
	ParticipantID identity.ParticipantID
	LID           identity.LID
	InvocationID  identity.InvocationID
}

func HumanPrincipal(message conversation.IncomingMessage) (Principal, error) {
	if err := message.Validate(); err != nil {
		return Principal{}, agent.NewError(agent.ErrorInvalidArgument, "create human principal", err)
	}
	principal := Principal{
		Kind: PrincipalHuman, TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID,
		ParticipantID: message.SenderID, LID: message.SenderLID,
	}
	return principal, principal.Validate()
}

func ModelPrincipal(key agent.Key, invocationID identity.InvocationID) (Principal, error) {
	if err := key.Validate(); err != nil || invocationID.IsZero() {
		return Principal{}, agent.NewError(agent.ErrorInvalidArgument, "create model principal", fmt.Errorf("valid agent key and invocation ID are required"))
	}
	principal := Principal{Kind: PrincipalModel, TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID, InvocationID: invocationID}
	return principal, nil
}

func SystemPrincipal(key agent.Key) (Principal, error) {
	if err := key.Validate(); err != nil {
		return Principal{}, err
	}
	return Principal{Kind: PrincipalSystem, TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID}, nil
}

func RecoveryPrincipal(key agent.Key, invocationID identity.InvocationID) (Principal, error) {
	if err := key.Validate(); err != nil || invocationID.IsZero() {
		return Principal{}, agent.NewError(agent.ErrorInvalidArgument, "create recovery principal", fmt.Errorf("valid agent key and invocation ID are required"))
	}
	return Principal{Kind: PrincipalRecovery, TenantID: key.TenantID, AccountID: key.AccountID, ChatID: key.ChatID, InvocationID: invocationID}, nil
}

func (principal Principal) Key() agent.Key {
	return agent.Key{TenantID: principal.TenantID, AccountID: principal.AccountID, ChatID: principal.ChatID}
}

func (principal Principal) Validate() error {
	if err := principal.Key().Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "validate principal", err)
	}
	switch principal.Kind {
	case PrincipalHuman:
		if principal.ParticipantID.IsZero() || principal.LID.IsZero() || !principal.InvocationID.IsZero() {
			return agent.NewError(agent.ErrorInvalidArgument, "validate principal", fmt.Errorf("human principal requires participant and LID only"))
		}
	case PrincipalModel, PrincipalRecovery:
		if principal.InvocationID.IsZero() || !principal.ParticipantID.IsZero() || !principal.LID.IsZero() {
			return agent.NewError(agent.ErrorInvalidArgument, "validate principal", fmt.Errorf("model and recovery principals require invocation only"))
		}
	case PrincipalSystem:
		if !principal.InvocationID.IsZero() || !principal.ParticipantID.IsZero() || !principal.LID.IsZero() {
			return agent.NewError(agent.ErrorInvalidArgument, "validate principal", fmt.Errorf("system principal must not carry actor identity"))
		}
	default:
		return agent.NewError(agent.ErrorInvalidArgument, "validate principal", fmt.Errorf("principal kind is invalid"))
	}
	return nil
}

type Capability string

const (
	CapabilityCommandHelp     Capability = "chat.command.help"
	CapabilityCommandInfo     Capability = "chat.command.info"
	CapabilityHistoryReset    Capability = "chat.history.reset"
	CapabilityPromptWrite     Capability = "chat.prompt.write"
	CapabilityPermissionWrite Capability = "chat.permission.write"

	CapabilityMessageReact    Capability = "message.react"
	CapabilityMessageDelete   Capability = "message.delete"
	CapabilityMessageMarkRead Capability = "message.mark-read"
	CapabilityChatPresence    Capability = "chat.presence"
	CapabilityChatContextRead Capability = "chat.context.read"
)

type CapabilitySet struct{ values []Capability }

func NewCapabilitySet(values ...Capability) (CapabilitySet, error) {
	copyValues := append([]Capability(nil), values...)
	sort.Slice(copyValues, func(left, right int) bool { return copyValues[left] < copyValues[right] })
	for index, capability := range copyValues {
		if !capability.Valid() {
			return CapabilitySet{}, agent.NewError(agent.ErrorInvalidArgument, "create policy capability set", fmt.Errorf("capability is invalid"))
		}
		if index > 0 && copyValues[index-1] == capability {
			return CapabilitySet{}, agent.NewError(agent.ErrorInvalidArgument, "create policy capability set", fmt.Errorf("capability is duplicated"))
		}
	}
	return CapabilitySet{values: copyValues}, nil
}

func (set CapabilitySet) Has(capability Capability) bool {
	index := sort.Search(len(set.values), func(index int) bool { return set.values[index] >= capability })
	return index < len(set.values) && set.values[index] == capability
}

func (set CapabilitySet) Values() []Capability { return append([]Capability(nil), set.values...) }

func (capability Capability) Valid() bool {
	switch capability {
	case CapabilityCommandHelp, CapabilityCommandInfo, CapabilityHistoryReset, CapabilityPromptWrite, CapabilityPermissionWrite,
		CapabilityMessageReact, CapabilityMessageDelete, CapabilityMessageMarkRead, CapabilityChatPresence, CapabilityChatContextRead:
		return true
	default:
		return false
	}
}

// HumanAccess is the current durable policy observation for one verified
// human principal. It intentionally carries policy facts, not provider DTOs
// or an address which a caller could treat as another identity proof.
type HumanAccess struct {
	ChatKind        conversation.ChatKind
	Allowlisted     bool
	ConfiguredOwner bool
}

func (access HumanAccess) Validate() error {
	if access.ChatKind != conversation.ChatDirect && access.ChatKind != conversation.ChatGroup && access.ChatKind != conversation.ChatStatus {
		return agent.NewError(agent.ErrorInvalidArgument, "validate human access", fmt.Errorf("chat kind is invalid"))
	}
	if access.ChatKind == conversation.ChatStatus && (access.Allowlisted || access.ConfiguredOwner) {
		return agent.NewError(agent.ErrorIntegrityFailure, "validate human access", fmt.Errorf("status chat cannot have access grants"))
	}
	return nil
}

// HumanAccessReader is implemented by the application persistence boundary.
// It must look up the principal using both its internal participant ID and
// verified LID, so a stale surrogate alone cannot gain command authority.
type HumanAccessReader interface {
	ReadHumanAccess(context.Context, Principal) (HumanAccess, error)
}

// ChatAuthority is a current provider observation. It is not persisted as a
// standing role grant and must be refreshed immediately before a privileged
// native effect.
type ChatAuthority struct {
	ChatKind     conversation.ChatKind
	ActorIsAdmin bool
	BotIsAdmin   bool
	ObservedAt   int64 // Unix milliseconds; avoids passing provider DTOs inward.
}

func (authority ChatAuthority) Validate() error {
	if authority.ChatKind != conversation.ChatDirect && authority.ChatKind != conversation.ChatGroup {
		return agent.NewError(agent.ErrorInvalidArgument, "validate chat authority", fmt.Errorf("chat kind is invalid"))
	}
	if authority.ObservedAt <= 0 {
		return agent.NewError(agent.ErrorInvalidArgument, "validate chat authority", fmt.Errorf("observation time is required"))
	}
	if authority.ChatKind == conversation.ChatDirect && (authority.ActorIsAdmin || authority.BotIsAdmin) {
		return agent.NewError(agent.ErrorIntegrityFailure, "validate chat authority", fmt.Errorf("direct chat cannot claim group authority"))
	}
	return nil
}

// ChatAuthorityReader is implemented by the provider edge. Policy receives
// only typed authority facts, never a WhatsApp client or group metadata DTO.
type ChatAuthorityReader interface {
	ReadChatAuthority(context.Context, Principal) (ChatAuthority, error)
}

// EffectAuthorization is the narrow request policy receives immediately
// before native execution. It deliberately contains no effect payload or
// provider identity: those were already validated by the durable outbox.
type EffectAuthorization struct {
	Key        agent.Key
	Principal  Principal
	Capability Capability
}

func (request EffectAuthorization) Validate() error {
	if err := request.Key.Validate(); err != nil {
		return err
	}
	if err := request.Principal.Validate(); err != nil {
		return err
	}
	if request.Principal.Key() != request.Key || !request.Capability.Valid() {
		return agent.NewError(agent.ErrorInvalidArgument, "validate effect authorization", fmt.Errorf("principal scope and capability are required"))
	}
	return nil
}
