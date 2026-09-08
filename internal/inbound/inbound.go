package inbound

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

type ClaimedMessage struct {
	Message   conversation.IncomingMessage
	Duplicate bool
	Handled   bool
}

type Store interface {
	ClaimAndResolveSender(context.Context, conversation.IncomingCandidate) (ClaimedMessage, error)
	MarkIgnored(context.Context, conversation.IncomingMessage, IgnoreReason) error
	BeginPromptMutation(context.Context, conversation.IncomingMessage, PromptCommand, agent.ConfigVersion) (PromptMutation, error)
	MarkPromptMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error
}

type IgnoreReason string

const (
	IgnoreFromMe            IgnoreReason = "from_me"
	IgnoreStatus            IgnoreReason = "status"
	IgnoreNotAllowlisted    IgnoreReason = "not_allowlisted"
	IgnoreGroupNotMentioned IgnoreReason = "group_not_mentioned"
	IgnorePolicyDenied      IgnoreReason = "policy_denied"
)

func (reason IgnoreReason) Valid() bool {
	switch reason {
	case IgnoreFromMe, IgnoreStatus, IgnoreNotAllowlisted, IgnoreGroupNotMentioned, IgnorePolicyDenied:
		return true
	default:
		return false
	}
}

type PromptMutation struct {
	ExpectedVersion agent.ConfigVersion
	AppliedVersion  agent.ConfigVersion
}

type Registry interface {
	AgentFor(context.Context, agent.Key) (*agent.Agent, error)
}

type Policy interface {
	AuthorizeInvocation(context.Context, conversation.IncomingMessage, agent.PermissionConfig) error
	AuthorizePrompt(context.Context, conversation.IncomingMessage, agent.PermissionConfig) error
}

type ResponseWriter interface {
	Resume(context.Context, conversation.IncomingMessage) (bool, error)
	Reply(context.Context, conversation.IncomingMessage, agent.ConfigVersion, string) error
}

type Observer interface {
	ObserveInboundClaimed()
	ObserveInboundDuplicate()
	ObserveInboundIgnored()
}

type DiscardObserver struct{}

func (DiscardObserver) ObserveInboundClaimed()   {}
func (DiscardObserver) ObserveInboundDuplicate() {}
func (DiscardObserver) ObserveInboundIgnored()   {}

type Handler struct {
	store     Store
	agents    Registry
	policy    Policy
	responses ResponseWriter
	observer  Observer
	stripes   [64]sync.Mutex
}

func NewHandler(store Store, agents Registry, policy Policy, responses ResponseWriter, observer Observer) (*Handler, error) {
	if store == nil || agents == nil || policy == nil || responses == nil || observer == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create inbound handler", fmt.Errorf("store, registry, policy, response writer, and observer are required"))
	}
	return &Handler{store: store, agents: agents, policy: policy, responses: responses, observer: observer}, nil
}

func (handler *Handler) Handle(ctx context.Context, candidate conversation.IncomingCandidate) error {
	if err := candidate.Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "handle incoming candidate", err)
	}
	claimed, err := handler.store.ClaimAndResolveSender(ctx, candidate)
	if err != nil {
		return err
	}
	if claimed.Duplicate {
		handler.observer.ObserveInboundDuplicate()
	} else {
		handler.observer.ObserveInboundClaimed()
	}
	if claimed.Handled {
		return nil
	}
	return handler.Resume(ctx, claimed.Message)
}

func (handler *Handler) Resume(ctx context.Context, message conversation.IncomingMessage) error {
	if err := message.Validate(); err != nil {
		return agent.NewError(agent.ErrorInvalidArgument, "resume incoming message", err)
	}
	stripe := handler.stripe(message.ChatID)
	stripe.Lock()
	defer stripe.Unlock()

	switch {
	case message.FromMe:
		return handler.ignore(ctx, message, IgnoreFromMe)
	case message.ChatKind == conversation.ChatStatus:
		return handler.ignore(ctx, message, IgnoreStatus)
	case !message.Allowlisted:
		return handler.ignore(ctx, message, IgnoreNotAllowlisted)
	case message.ChatKind == conversation.ChatGroup && !message.MentionsBot:
		return handler.ignore(ctx, message, IgnoreGroupNotMentioned)
	}

	key := agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID}
	currentAgent, err := handler.agents.AgentFor(ctx, key)
	if err != nil {
		return err
	}
	snapshot, err := currentAgent.Config().Refresh(ctx)
	if err != nil {
		return err
	}

	command, recognized := ParsePromptCommand(message.Text)
	if recognized {
		if resumed, err := handler.responses.Resume(ctx, message); err != nil || resumed {
			return err
		}
		if err := handler.policy.AuthorizePrompt(ctx, message, snapshot.Permission); err != nil {
			return handler.responses.Reply(ctx, message, snapshot.Version, "Perintah /prompt hanya dapat digunakan oleh owner yang dikonfigurasi.")
		}
		return handler.handlePrompt(ctx, currentAgent, snapshot, message, command)
	}

	if err := handler.policy.AuthorizeInvocation(ctx, message, snapshot.Permission); err != nil {
		return handler.ignore(ctx, message, IgnorePolicyDenied)
	}
	capabilities, err := agent.NewCapabilitySet()
	if err != nil {
		return err
	}
	_, err = currentAgent.Invoke(ctx, agent.Invocation{
		ID:        message.InvocationID,
		Cause:     agent.CauseInboundMessage,
		Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: message.CausationID},
		Sender: &agent.SenderContext{
			ParticipantID: message.SenderID,
			Ref:           message.SenderRef,
			DisplayName:   message.SenderName,
		},
		Input:         []agent.ContentPart{agent.TextPart{Text: message.Text}},
		Capabilities:  capabilities,
		PolicyVersion: snapshot.Version,
		RequestedAt:   message.OccurredAt,
	})
	return err
}

func (handler *Handler) ignore(ctx context.Context, message conversation.IncomingMessage, reason IgnoreReason) error {
	if err := handler.store.MarkIgnored(ctx, message, reason); err != nil {
		return err
	}
	handler.observer.ObserveInboundIgnored()
	return nil
}

func (handler *Handler) stripe(chatID identity.ChatID) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(chatID.String()))
	return &handler.stripes[hash.Sum32()%uint32(len(handler.stripes))]
}

func (handler *Handler) handlePrompt(
	ctx context.Context,
	currentAgent *agent.Agent,
	snapshot agent.ConfigSnapshot,
	message conversation.IncomingMessage,
	command PromptCommand,
) error {
	var response string
	switch command.Kind {
	case PromptView:
		if snapshot.PromptOverride == nil {
			response = "Prompt override belum diatur."
		} else {
			response = "Prompt override saat ini:\n" + snapshot.PromptOverride.Text
		}
	case PromptSet:
		updated, err := handler.applyPromptMutation(ctx, currentAgent, snapshot, message, command)
		if err != nil {
			return err
		}
		snapshot.Version = updated
		response = "Prompt override berhasil diperbarui."
	case PromptClear:
		updated, err := handler.applyPromptMutation(ctx, currentAgent, snapshot, message, command)
		if err != nil {
			return err
		}
		snapshot.Version = updated
		response = "Prompt override berhasil dihapus."
	case PromptInvalid:
		response = fmt.Sprintf("Format: /prompt view, /prompt set <teks>, atau /prompt clear. Panjang prompt maksimal %d byte.", agent.MaxPromptBytes)
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "handle prompt command", fmt.Errorf("unknown command kind"))
	}
	return handler.responses.Reply(ctx, message, snapshot.Version, response)
}

func (handler *Handler) applyPromptMutation(
	ctx context.Context,
	currentAgent *agent.Agent,
	snapshot agent.ConfigSnapshot,
	message conversation.IncomingMessage,
	command PromptCommand,
) (agent.ConfigVersion, error) {
	journal, err := handler.store.BeginPromptMutation(ctx, message, command, snapshot.Version)
	if err != nil {
		return 0, err
	}
	if journal.AppliedVersion != 0 {
		return journal.AppliedVersion, nil
	}
	if snapshot.Version == journal.ExpectedVersion+1 && promptMutationMatches(snapshot, command) {
		if err := handler.store.MarkPromptMutationApplied(ctx, message, journal.ExpectedVersion, snapshot.Version); err != nil {
			return 0, err
		}
		return snapshot.Version, nil
	}
	if snapshot.Version != journal.ExpectedVersion {
		return 0, agent.NewError(agent.ErrorConflict, "apply prompt mutation", fmt.Errorf("config changed after command authorization; resend the command"))
	}
	var updated agent.ConfigSnapshot
	switch command.Kind {
	case PromptSet:
		updated, err = currentAgent.Config().SetPromptOverride(ctx, snapshot.Version, agent.PromptOverride{Mode: agent.PromptAppend, Text: command.Text})
	case PromptClear:
		updated, err = currentAgent.Config().ClearPromptOverride(ctx, snapshot.Version)
	default:
		return 0, agent.NewError(agent.ErrorInvalidArgument, "apply prompt mutation", fmt.Errorf("command is not a mutation"))
	}
	if err != nil {
		return 0, err
	}
	if err := handler.store.MarkPromptMutationApplied(ctx, message, journal.ExpectedVersion, updated.Version); err != nil {
		return 0, err
	}
	return updated.Version, nil
}

func promptMutationMatches(snapshot agent.ConfigSnapshot, command PromptCommand) bool {
	switch command.Kind {
	case PromptSet:
		return snapshot.PromptOverride != nil && snapshot.PromptOverride.Mode == agent.PromptAppend && snapshot.PromptOverride.Text == command.Text
	case PromptClear:
		return snapshot.PromptOverride == nil
	default:
		return false
	}
}

type PromptCommandKind uint8

const (
	PromptInvalid PromptCommandKind = iota + 1
	PromptView
	PromptSet
	PromptClear
)

type PromptCommand struct {
	Kind PromptCommandKind
	Text string
}

func ParsePromptCommand(text string) (PromptCommand, bool) {
	if text == "/prompt" || text == "/prompt view" {
		return PromptCommand{Kind: PromptView}, true
	}
	if text == "/prompt clear" {
		return PromptCommand{Kind: PromptClear}, true
	}
	if strings.HasPrefix(text, "/prompt set ") {
		value := strings.TrimPrefix(text, "/prompt set ")
		if strings.TrimSpace(value) == "" || len(value) > agent.MaxPromptBytes {
			return PromptCommand{Kind: PromptInvalid}, true
		}
		return PromptCommand{Kind: PromptSet, Text: value}, true
	}
	if text == "/prompt set" || strings.HasPrefix(text, "/prompt ") {
		return PromptCommand{Kind: PromptInvalid}, true
	}
	return PromptCommand{}, false
}
