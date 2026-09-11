package inbound

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/command"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/policy"
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
	BeginPermissionMutation(context.Context, conversation.IncomingMessage, PermissionCommand, agent.ConfigVersion) (PromptMutation, error)
	MarkPermissionMutationApplied(context.Context, conversation.IncomingMessage, agent.ConfigVersion, agent.ConfigVersion) error
	IsChatMuted(context.Context, agent.Key, identity.SenderRef, time.Time) (bool, error)
	StageBatch(context.Context, conversation.IncomingMessage, time.Time) (BatchStage, error)
	ClaimBatch(context.Context, conversation.IncomingMessage, time.Time, uint32) (BatchClaim, error)
}

type BatchStage struct {
	ReadyAt time.Time
	Handled bool
	Wait    bool
}

type BatchClaim struct {
	Messages []conversation.IncomingMessage
	ReadyAt  time.Time
	Handled  bool
}

type BatchOptions struct {
	Debounce time.Duration
	BurstCap uint32
	Clock    agent.Clock
	Activity AIActivity
}

// AIActivity owns best-effort WhatsApp UX signals. They are runtime behavior,
// not model tools and not permission-controlled moderation commands.
type AIActivity interface {
	MarkRead(context.Context, agent.Key, identity.MessageID) error
	SetComposing(context.Context, agent.Key, bool) error
}

type discardAIActivity struct{}

func (discardAIActivity) MarkRead(context.Context, agent.Key, identity.MessageID) error { return nil }
func (discardAIActivity) SetComposing(context.Context, agent.Key, bool) error           { return nil }

type IgnoreReason string

const (
	IgnoreFromMe            IgnoreReason = "from_me"
	IgnoreStatus            IgnoreReason = "status"
	IgnoreNotAllowlisted    IgnoreReason = "not_allowlisted"
	IgnoreGroupNotMentioned IgnoreReason = "group_not_mentioned"
	IgnorePolicyDenied      IgnoreReason = "policy_denied"
	IgnoreMuted             IgnoreReason = "muted"
)

func (reason IgnoreReason) Valid() bool {
	switch reason {
	case IgnoreFromMe, IgnoreStatus, IgnoreNotAllowlisted, IgnoreGroupNotMentioned, IgnorePolicyDenied, IgnoreMuted:
		return true
	default:
		return false
	}
}

type Registry interface {
	AgentFor(context.Context, agent.Key) (*agent.Agent, error)
}

type Policy interface {
	AuthorizeInvocation(context.Context, conversation.IncomingMessage, agent.PermissionConfig) error
	AuthorizeCommand(context.Context, policy.Principal, policy.Capability, agent.PermissionConfig) error
	ModelCapabilities(agent.PermissionConfig) (agent.CapabilitySet, error)
}

// CommandPermissionFactsProvider is implemented by policies that can resolve
// the current owner/admin/chat facts used by a command's permission DSL. It is
// optional for compatibility with small test policies; callers without it
// receive only the safe structural facts and therefore fail closed for owner
// or admin expressions.
type CommandPermissionFactsProvider interface {
	CommandPermissionFacts(context.Context, policy.Principal, agent.PermissionConfig, bool) (policy.PermissionFacts, error)
}

type ResponseWriter interface {
	Resume(context.Context, conversation.IncomingMessage) (bool, error)
	Reply(context.Context, conversation.IncomingMessage, agent.ConfigVersion, string) error
}

type Observer interface {
	ObserveInboundClaimed()
	ObserveInboundDuplicate()
	ObserveInboundIgnored()
	ObserveInboundBatch(uint32)
	ObserveHistoryReset()
}

type DiscardObserver struct{}

func (DiscardObserver) ObserveInboundClaimed()     {}
func (DiscardObserver) ObserveInboundDuplicate()   {}
func (DiscardObserver) ObserveInboundIgnored()     {}
func (DiscardObserver) ObserveInboundBatch(uint32) {}
func (DiscardObserver) ObserveHistoryReset()       {}

// handlerServices contains only infrastructure shared by the two independent
// lanes. It deliberately contains no routing or lane-specific orchestration.
// CommandHandler and AIHandler each receive their own value of this type, so
// their serialization stripes and runtime state are not shared.
type handlerServices struct {
	store     Store
	agents    Registry
	policy    Policy
	responses ResponseWriter
	observer  Observer
	stripes   *[64]sync.Mutex
}

func (handler *CommandHandler) resumeCommand(
	ctx context.Context,
	message conversation.IncomingMessage,
	request command.Request,
	descriptor command.Descriptor,
) error {
	stripe := handler.stripe(message.ChatID)
	stripe.Lock()
	defer stripe.Unlock()
	currentAgent, snapshot, err := handler.loadAgent(ctx, message)
	if err != nil {
		return err
	}
	// A response already planned by an earlier authorized execution remains
	// replayable through the normal durable send policy. It must not be
	// regenerated from the original command text.
	if resumed, err := handler.responses.Resume(ctx, message); err != nil || resumed {
		return err
	}
	principal, err := commandPrincipal(message)
	if err != nil {
		return err
	}
	facts, err := handler.commandPermissionFacts(ctx, principal, snapshot.Permission, message)
	if err != nil {
		if agent.IsCode(err, agent.ErrorPermissionDenied) {
			reply := descriptor.DeniedReply
			if reply == "" {
				reply = "Perintah ini tidak dapat digunakan pada chat ini."
			}
			return handler.responses.Reply(ctx, message, snapshot.Version, reply)
		}
		return err
	}
	allowed, err := builtinCommandRegistry.Allows(request, facts)
	if err != nil {
		return err
	}
	if !allowed {
		reply := descriptor.DeniedReply
		if reply == "" {
			reply = "Perintah ini tidak dapat digunakan pada chat ini."
		}
		return handler.responses.Reply(ctx, message, snapshot.Version, reply)
	}
	if !message.FromMe {
		if err := handler.policy.AuthorizeCommand(ctx, principal, descriptor.Capability, snapshot.Permission); err != nil {
			reply := descriptor.DeniedReply
			if reply == "" {
				reply = "Perintah ini tidak dapat digunakan pada chat ini."
			}
			return handler.responses.Reply(ctx, message, snapshot.Version, reply)
		}
	}

	return builtinCommandRegistry.Dispatch(ctx, request, command.Context{
		Agent:     currentAgent,
		Snapshot:  snapshot,
		Message:   message,
		Facts:     facts,
		Registry:  builtinCommandRegistry,
		Store:     handler.store,
		Responses: handler.responses,
		Observer:  handler.observer,
	})
}

func commandPrincipal(message conversation.IncomingMessage) (policy.Principal, error) {
	if !message.FromMe {
		return policy.HumanPrincipal(message)
	}
	key := agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID}
	return policy.ModelPrincipal(key, message.InvocationID)
}

func (handler *CommandHandler) commandPermissionFacts(
	ctx context.Context,
	principal policy.Principal,
	permission agent.PermissionConfig,
	message conversation.IncomingMessage,
) (command.PermissionFacts, error) {
	if provider, ok := handler.policy.(CommandPermissionFactsProvider); ok {
		facts, err := provider.CommandPermissionFacts(ctx, principal, permission, message.FromMe)
		return command.PermissionFacts(facts), err
	}
	// A custom policy that does not expose trusted role lookup can still use
	// structural expressions such as "public and !fromMe". Owner/admin facts
	// deliberately remain false instead of trusting inbound flags.
	return command.PermissionFacts{
		IsGroup:   message.ChatKind == conversation.ChatGroup,
		IsPrivate: message.ChatKind == conversation.ChatDirect,
		FromMe:    message.FromMe,
	}, nil
}

func (services *handlerServices) loadAgent(
	ctx context.Context,
	message conversation.IncomingMessage,
) (*agent.Agent, agent.ConfigSnapshot, error) {
	key := agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID}
	currentAgent, err := services.agents.AgentFor(ctx, key)
	if err != nil {
		return nil, agent.ConfigSnapshot{}, err
	}
	snapshot, err := currentAgent.Config().Refresh(ctx)
	if err != nil {
		return nil, agent.ConfigSnapshot{}, err
	}
	return currentAgent, snapshot, nil
}

func (handler *AIHandler) waitUntil(ctx context.Context, readyAt time.Time) error {
	delay := readyAt.Sub(handler.batch.Clock.Now())
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		code := agent.ErrorCancelled
		if ctx.Err() == context.DeadlineExceeded {
			code = agent.ErrorTimeout
		}
		return agent.NewError(code, "wait for message debounce", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func (handler *AIHandler) processBatch(
	ctx context.Context,
	currentAgent *agent.Agent,
	messages []conversation.IncomingMessage,
) error {
	if len(messages) == 0 {
		return agent.NewError(agent.ErrorIntegrityFailure, "process message batch", fmt.Errorf("batch has no messages"))
	}
	snapshot, err := currentAgent.Config().Refresh(ctx)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if err := handler.policy.AuthorizeInvocation(ctx, message, snapshot.Permission); err != nil {
			return err
		}
	}
	capabilities, err := handler.policy.ModelCapabilities(snapshot.Permission)
	if err != nil {
		return err
	}
	key := agent.Key{TenantID: messages[0].TenantID, AccountID: messages[0].AccountID, ChatID: messages[0].ChatID}
	for _, message := range messages {
		_ = handler.batch.Activity.MarkRead(ctx, key, message.ID)
	}
	_ = handler.batch.Activity.SetComposing(ctx, key, true)
	defer func() {
		pauseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = handler.batch.Activity.SetComposing(pauseCtx, key, false)
	}()
	for _, message := range messages[:len(messages)-1] {
		invocation, err := invocationFromMessage(message, snapshot.Version, capabilities)
		if err != nil {
			return err
		}
		if err := currentAgent.History().Append(ctx, agent.HistoryEntry{
			MessageID: message.ID, InvocationID: invocation.ID, Causation: invocation.Causation,
			Role: agent.HistoryUser, Sender: invocation.Sender, Quote: invocation.Quote,
			Content: invocation.Input, Delivery: agent.DeliveryNotStarted, CreatedAt: invocation.RequestedAt,
		}); err != nil {
			return err
		}
	}
	anchor, err := invocationFromMessage(messages[len(messages)-1], snapshot.Version, capabilities)
	if err != nil {
		return err
	}
	_, err = currentAgent.Invoke(ctx, anchor)
	return err
}

func invocationFromMessage(message conversation.IncomingMessage, version agent.ConfigVersion, capabilities agent.CapabilitySet) (agent.Invocation, error) {
	var quote *agent.QuoteContext
	if message.Quote != nil {
		role := agent.HistoryUser
		if message.Quote.Role == conversation.QuoteAssistant {
			role = agent.HistoryAssistant
		}
		quote = &agent.QuoteContext{
			Sequence: message.Quote.Sequence, MessageID: message.Quote.ID, Role: role,
			SenderRef: message.Quote.SenderRef, Text: message.Quote.Text,
		}
	}
	return agent.Invocation{
		ID:        message.InvocationID,
		Cause:     agent.CauseInboundMessage,
		Causation: agent.CausationRef{Kind: agent.CausationMessage, ID: message.CausationID},
		Sender: &agent.SenderContext{
			ParticipantID: message.SenderID,
			Ref:           message.SenderRef,
			DisplayName:   message.SenderName,
		},
		Quote:         quote,
		Input:         []agent.ContentPart{agent.TextPart{Text: message.Text}},
		Capabilities:  capabilities,
		PolicyVersion: version,
		RequestedAt:   message.OccurredAt,
	}, nil
}

func (services *handlerServices) ignore(ctx context.Context, message conversation.IncomingMessage, reason IgnoreReason) error {
	if err := services.store.MarkIgnored(ctx, message, reason); err != nil {
		return err
	}
	services.observer.ObserveInboundIgnored()
	return nil
}

func (services *handlerServices) stripe(chatID identity.ChatID) *sync.Mutex {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(chatID.String()))
	return &services.stripes[hash.Sum32()%uint32(len(services.stripes))]
}
