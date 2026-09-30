package inbound

import (
	"context"
	"errors"
	"time"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/command"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/conversation"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/policy"
)

type ClaimedMessage struct {
	Message   conversation.IncomingMessage
	Duplicate bool
	Handled   bool
}

type Store interface {
	ClaimAndResolveSender(context.Context, conversation.IncomingCandidate) (ClaimedMessage, error)
	MarkIgnored(context.Context, conversation.IncomingMessage, IgnoreReason) error
	command.Store
	IsChatMuted(context.Context, agent.Key, identity.SenderRef, time.Time) (bool, error)
	// ClaimBatch starts one turn for messages queued in memory, in arrival
	// order. It drops messages that no longer need a reply (already answered,
	// or cleared by a history reset) and returns the rest; the last one is
	// the anchor that gets the reply. It may consume only part of the input
	// and returns the rest, in arrival order, to queue again.
	ClaimBatch(context.Context, []conversation.IncomingMessage) ([]conversation.IncomingMessage, []conversation.IncomingMessage, error)
	// ListUnfinished returns up to limit messages the last run accepted but
	// never finished, oldest first, starting after the message after.
	ListUnfinished(ctx context.Context, tenantID identity.TenantID, after *conversation.IncomingMessage, limit int) ([]conversation.IncomingMessage, error)
	// ListUnfinishedInChat returns up to limit of one chat's unfinished
	// messages, oldest first.
	ListUnfinishedInChat(context.Context, agent.Key, int) ([]conversation.IncomingMessage, error)
	TaskStore
}

type AgentLifecycleObserver interface {
	ObserveAgentTriggered(conversation.IncomingMessage, uint32, string)
	ObserveAgentSucceeded(conversation.IncomingMessage, agent.InvokeResult, time.Duration, string)
}

type discardAgentLifecycleObserver struct{}

func (discardAgentLifecycleObserver) ObserveAgentTriggered(conversation.IncomingMessage, uint32, string) {
}
func (discardAgentLifecycleObserver) ObserveAgentSucceeded(conversation.IncomingMessage, agent.InvokeResult, time.Duration, string) {
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
	AuthorizeInvocation(context.Context, conversation.IncomingMessage, agent.ConfigSnapshot) error
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

// handlerServices is the infrastructure a Dispatcher routes messages to.
type handlerServices struct {
	store     Store
	agents    Registry
	policy    Policy
	responses ResponseWriter
	observer  Observer
	platform  command.Platform
}

func (handler *handlerServices) resumeCommand(
	ctx context.Context,
	message conversation.IncomingMessage,
	request command.Request,
	cmd command.Command,
) error {
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
	deny := func() error {
		reply := cmd.DeniedReply
		if reply == "" {
			reply = "This command cannot be used in this chat."
		}
		return handler.responses.Reply(ctx, message, snapshot.Version, reply)
	}
	facts, err := handler.commandPermissionFacts(ctx, principal, snapshot.Permission, message.FromMe, message.ChatKind)
	if agent.IsCode(err, agent.ErrorPermissionDenied) {
		return deny()
	}
	if err != nil {
		return handler.failCommand(ctx, message, snapshot.Version, cmd, err)
	}
	err = builtinCommandRegistry.Dispatch(ctx, request, command.Invocation{
		Agent: currentAgent, Config: snapshot, Message: message, Facts: facts,
		Platform: handler.platform, Store: handler.store, Observer: handler.observer,
	})
	if errors.Is(err, command.ErrDenied) {
		return deny()
	}
	if err != nil {
		return handler.failCommand(ctx, message, snapshot.Version, cmd, err)
	}
	return nil
}

// failCommand makes a failed command final: the message is closed, and the
// user gets one short reply unless a send may already have reached the chat. Leaving it unhandled would make recovery re-run the
// command every few seconds, repeating whatever it already sent. Only a
// cancelled context (shutdown) leaves the message for recovery after restart.
// The original error is still returned so the lane logs it.
func (handler *handlerServices) failCommand(
	ctx context.Context,
	message conversation.IncomingMessage,
	version agent.ConfigVersion,
	cmd command.Command,
	cause error,
) error {
	if ctx.Err() != nil {
		return cause
	}
	switch agent.CodeOf(cause) {
	case agent.ErrorTimeout, agent.ErrorProviderFailure, agent.ErrorUnknownOutcome:
		// A send may have reached the chat before it failed, so an apology
		// could contradict a reply the user already has. Close quietly.
		if err := handler.store.MarkCommandHandled(ctx, message); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	reply := "Sorry, /" + cmd.Name + " failed. Please try again later."
	if agent.IsCode(cause, agent.ErrorNotReady) {
		reply = "WhatsApp is still starting up. Please try /" + cmd.Name + " again in a moment."
	}
	if err := handler.responses.Reply(ctx, message, version, reply); err != nil {
		if markErr := handler.store.MarkCommandHandled(ctx, message); markErr != nil {
			return errors.Join(cause, err, markErr)
		}
	}
	return cause
}

func commandPrincipal(message conversation.IncomingMessage) (policy.Principal, error) {
	if !message.FromMe {
		return policy.HumanPrincipal(message)
	}
	key := agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID}
	return policy.ModelPrincipal(key, message.InvocationID)
}

func (handler *handlerServices) commandPermissionFacts(
	ctx context.Context,
	principal policy.Principal,
	permission agent.PermissionConfig,
	fromMe bool,
	chatKind conversation.ChatKind,
) (command.PermissionFacts, error) {
	if provider, ok := handler.policy.(CommandPermissionFactsProvider); ok {
		facts, err := provider.CommandPermissionFacts(ctx, principal, permission, fromMe)
		return command.PermissionFacts(facts), err
	}
	// A custom policy that does not expose trusted role lookup can still use
	// structural expressions such as "public and !fromMe". Owner/admin facts
	// deliberately remain false instead of trusting inbound flags.
	return command.PermissionFacts{
		IsGroup:   chatKind == conversation.ChatGroup,
		IsPrivate: chatKind == conversation.ChatDirect,
		FromMe:    fromMe,
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

func (handler *Dispatcher) processBatch(
	ctx context.Context,
	currentAgent *agent.Agent,
	messages []conversation.IncomingMessage,
) error {
	if len(messages) == 0 {
		return agent.NewError(agent.ErrorIntegrityFailure, "process message batch", errors.New("batch has no messages"))
	}
	snapshot, err := currentAgent.Config().Refresh(ctx)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if err := handler.policy.AuthorizeInvocation(ctx, message, snapshot); err != nil {
			return err
		}
	}
	capabilities, err := handler.policy.ModelCapabilities(snapshot.Permission)
	if err != nil {
		return err
	}
	anchorMessage := messages[len(messages)-1]
	key := agent.Key{TenantID: anchorMessage.TenantID, AccountID: anchorMessage.AccountID, ChatID: anchorMessage.ChatID}
	modelPrincipal, err := policy.ModelPrincipal(key, anchorMessage.InvocationID)
	if err != nil {
		return err
	}
	facts, err := handler.commandPermissionFacts(ctx, modelPrincipal, snapshot.Permission, true, anchorMessage.ChatKind)
	if err != nil {
		return err
	}
	commandNames, err := modelCommandNames(facts)
	if err != nil {
		return err
	}
	chatName := anchorMessage.SenderName
	var observedChat *agent.ChatContext
	if anchorMessage.ChatKind == conversation.ChatGroup {
		chatName = ""
		if handler.options.ChatContext != nil {
			if chat, readErr := handler.options.ChatContext.ReadChatContext(ctx, key); readErr == nil {
				chatName = chat.Name
				observedChat = &chat
			}
		}
	}
	handler.options.Events.ObserveAgentTriggered(anchorMessage, uint32(len(messages)), chatName)
	for _, message := range messages {
		_ = handler.options.Activity.MarkRead(ctx, key, message.ID)
	}
	_ = handler.options.Activity.SetComposing(ctx, key, true)
	defer func() {
		pauseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = handler.options.Activity.SetComposing(pauseCtx, key, false)
	}()
	for _, message := range messages[:len(messages)-1] {
		invocation, err := invocationFromMessage(message, snapshot.Version, capabilities, commandNames)
		if err != nil {
			return err
		}
		if err := currentAgent.History().Append(ctx, agent.HistoryEntry{
			MessageID: message.ID, InvocationID: invocation.ID, Causation: invocation.Causation,
			Role: agent.HistoryUser, Sender: invocation.Sender, Quote: invocation.Quote,
			Content: invocation.Input, Mentions: invocation.Mentions,
			Delivery: agent.DeliveryNotStarted, CreatedAt: invocation.RequestedAt,
		}); err != nil {
			return err
		}
	}
	anchor, err := invocationFromMessage(anchorMessage, snapshot.Version, capabilities, commandNames)
	if err != nil {
		return err
	}
	anchor.Stickers = handler.stickerNames(ctx, key)
	started := time.Now()
	var result agent.InvokeResult
	result, err = currentAgent.InvokeWith(ctx, anchor, snapshot, observedChat)
	if err == nil && result.Delivery == agent.DeliverySucceeded {
		handler.options.Events.ObserveAgentSucceeded(anchorMessage, result, time.Since(started), chatName)
	}
	return err
}

// modelCommandNames lists the commands the model may issue under facts.
func modelCommandNames(facts command.PermissionFacts) ([]string, error) {
	names := make([]string, 0)
	for _, cmd := range builtinCommandRegistry.Commands() {
		allowed, err := command.EvaluatePermission(cmd.Permission, facts)
		if err != nil {
			return nil, agent.NewError(agent.ErrorIntegrityFailure, "list model commands", err)
		}
		if allowed {
			names = append(names, cmd.Name)
		}
	}
	return names, nil
}

// stickerNames is the chat's sticker catalog offered to send_sticker. A
// catalog that cannot be read only hides the tool for this turn.
func (handler *Dispatcher) stickerNames(ctx context.Context, key agent.Key) []string {
	if handler.options.Stickers == nil {
		return nil
	}
	names, err := handler.options.Stickers.StickerNames(ctx, key)
	if err != nil {
		return nil
	}
	return names
}

func invocationFromMessage(message conversation.IncomingMessage, version agent.ConfigVersion, capabilities agent.CapabilitySet, commands []string) (agent.Invocation, error) {
	var quote *agent.QuoteContext
	if message.Quote != nil {
		role := agent.HistoryUser
		if message.Quote.Role == conversation.QuoteAssistant {
			role = agent.HistoryAssistant
		}
		quote = &agent.QuoteContext{
			Sequence: message.Quote.Sequence, MessageID: message.Quote.ID, Role: role,
			SenderRef: message.Quote.SenderRef, Text: message.Quote.Text,
			SenderIsAdmin: message.Quote.SenderIsAdmin, SenderIsSuperAdmin: message.Quote.SenderIsSuperAdmin,
			Mentions: conversationMentions(message.Quote.Mentions),
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
			IsAdmin:       message.SenderIsAdmin,
			IsSuperAdmin:  message.SenderIsSuperAdmin,
		},
		Quote:         quote,
		Input:         []agent.ContentPart{agent.TextPart{Text: message.Text}},
		Mentions:      conversationMentions(message.Mentions),
		Capabilities:  capabilities,
		Commands:      append([]string(nil), commands...),
		PolicyVersion: version,
		RequestedAt:   message.OccurredAt,
	}, nil
}

func conversationMentions(bindings []conversation.MentionBinding) []agent.MentionContext {
	mentions := make([]agent.MentionContext, len(bindings))
	for index, binding := range bindings {
		mentions[index] = agent.MentionContext{Token: binding.Token, SenderRef: binding.SenderRef, Bot: binding.Bot}
	}
	return mentions
}

func (services *handlerServices) ignore(ctx context.Context, message conversation.IncomingMessage, reason IgnoreReason) error {
	if err := services.store.MarkIgnored(ctx, message, reason); err != nil {
		return err
	}
	services.observer.ObserveInboundIgnored()
	return nil
}
