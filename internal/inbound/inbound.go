package inbound

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

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
	AuthorizeHistoryReset(context.Context, conversation.IncomingMessage, agent.PermissionConfig) error
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

type Handler struct {
	store     Store
	agents    Registry
	policy    Policy
	responses ResponseWriter
	observer  Observer
	batch     BatchOptions
	stripes   [64]sync.Mutex
}

func NewHandler(store Store, agents Registry, policy Policy, responses ResponseWriter, observer Observer) (*Handler, error) {
	return NewHandlerWithBatching(store, agents, policy, responses, observer, BatchOptions{
		Debounce: 0, BurstCap: 1, Clock: agent.SystemClock{},
	})
}

func NewHandlerWithBatching(
	store Store,
	agents Registry,
	policy Policy,
	responses ResponseWriter,
	observer Observer,
	options BatchOptions,
) (*Handler, error) {
	if store == nil || agents == nil || policy == nil || responses == nil || observer == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create inbound handler", fmt.Errorf("store, registry, policy, response writer, and observer are required"))
	}
	if options.Debounce < 0 || options.Debounce > time.Minute || options.BurstCap == 0 || options.BurstCap > 256 || options.Clock == nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "create inbound handler", fmt.Errorf("valid batching bounds and clock are required"))
	}
	return &Handler{store: store, agents: agents, policy: policy, responses: responses, observer: observer, batch: options}, nil
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
	switch {
	case message.FromMe:
		return handler.ignore(ctx, message, IgnoreFromMe)
	case message.ChatKind == conversation.ChatStatus:
		return handler.ignore(ctx, message, IgnoreStatus)
	case !message.Allowlisted:
		return handler.ignore(ctx, message, IgnoreNotAllowlisted)
	case message.ChatKind == conversation.ChatGroup && !message.MentionsBot && !message.RepliedToBot:
		return handler.ignore(ctx, message, IgnoreGroupNotMentioned)
	}

	if command, recognized := ParsePromptCommand(message.Text); recognized {
		stripe := handler.stripe(message.ChatID)
		stripe.Lock()
		defer stripe.Unlock()
		currentAgent, snapshot, err := handler.loadAgent(ctx, message)
		if err != nil {
			return err
		}
		if resumed, err := handler.responses.Resume(ctx, message); err != nil || resumed {
			return err
		}
		if err := handler.policy.AuthorizePrompt(ctx, message, snapshot.Permission); err != nil {
			return handler.responses.Reply(ctx, message, snapshot.Version, "Perintah /prompt hanya dapat digunakan oleh owner yang dikonfigurasi.")
		}
		return handler.handlePrompt(ctx, currentAgent, snapshot, message, command)
	}
	if control, recognized := ParseControlCommand(message.Text); recognized {
		stripe := handler.stripe(message.ChatID)
		stripe.Lock()
		defer stripe.Unlock()
		currentAgent, snapshot, err := handler.loadAgent(ctx, message)
		if err != nil {
			return err
		}
		if resumed, err := handler.responses.Resume(ctx, message); err != nil || resumed {
			return err
		}
		return handler.handleControl(ctx, currentAgent, snapshot, message, control)
	}

	currentAgent, snapshot, err := handler.loadAgent(ctx, message)
	if err != nil {
		return err
	}
	if err := handler.policy.AuthorizeInvocation(ctx, message, snapshot.Permission); err != nil {
		return handler.ignore(ctx, message, IgnorePolicyDenied)
	}
	if resumed, err := handler.responses.Resume(ctx, message); err != nil || resumed {
		return err
	}
	stage, err := handler.store.StageBatch(ctx, message, handler.batch.Clock.Now().Add(handler.batch.Debounce))
	if err != nil || stage.Handled || !stage.Wait {
		return err
	}
	readyAt := stage.ReadyAt
	for {
		if err := handler.waitUntil(ctx, readyAt); err != nil {
			return err
		}
		stripe := handler.stripe(message.ChatID)
		stripe.Lock()
		claim, claimErr := handler.store.ClaimBatch(ctx, message, handler.batch.Clock.Now(), handler.batch.BurstCap)
		if claimErr != nil {
			stripe.Unlock()
			return claimErr
		}
		if claim.Handled {
			stripe.Unlock()
			return nil
		}
		if !claim.ReadyAt.IsZero() {
			readyAt = claim.ReadyAt
			stripe.Unlock()
			continue
		}
		handler.observer.ObserveInboundBatch(uint32(len(claim.Messages)))
		err = handler.processBatch(ctx, currentAgent, claim.Messages)
		stripe.Unlock()
		if err != nil {
			return err
		}
		// The one elected waiter drains every ready burst-cap-sized batch. Other
		// inbound workers return after durable staging, so a hot chat cannot
		// occupy one worker per message during the debounce window.
		readyAt = handler.batch.Clock.Now()
	}
}

func (handler *Handler) loadAgent(
	ctx context.Context,
	message conversation.IncomingMessage,
) (*agent.Agent, agent.ConfigSnapshot, error) {
	key := agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID}
	currentAgent, err := handler.agents.AgentFor(ctx, key)
	if err != nil {
		return nil, agent.ConfigSnapshot{}, err
	}
	snapshot, err := currentAgent.Config().Refresh(ctx)
	if err != nil {
		return nil, agent.ConfigSnapshot{}, err
	}
	return currentAgent, snapshot, nil
}

func (handler *Handler) waitUntil(ctx context.Context, readyAt time.Time) error {
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

func (handler *Handler) processBatch(
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
	for _, message := range messages[:len(messages)-1] {
		invocation, err := invocationFromMessage(message, snapshot.Version)
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
	anchor, err := invocationFromMessage(messages[len(messages)-1], snapshot.Version)
	if err != nil {
		return err
	}
	_, err = currentAgent.Invoke(ctx, anchor)
	return err
}

func invocationFromMessage(message conversation.IncomingMessage, version agent.ConfigVersion) (agent.Invocation, error) {
	capabilities, err := agent.NewCapabilitySet()
	if err != nil {
		return agent.Invocation{}, err
	}
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

type ControlCommandKind uint8

const (
	ControlHelp ControlCommandKind = iota + 1
	ControlInfo
	ControlReset
)

func ParseControlCommand(text string) (ControlCommandKind, bool) {
	switch text {
	case "/help":
		return ControlHelp, true
	case "/info":
		return ControlInfo, true
	case "/reset":
		return ControlReset, true
	default:
		return 0, false
	}
}

func (handler *Handler) handleControl(
	ctx context.Context,
	currentAgent *agent.Agent,
	snapshot agent.ConfigSnapshot,
	message conversation.IncomingMessage,
	command ControlCommandKind,
) error {
	response := ""
	switch command {
	case ControlHelp:
		response = "Perintah: /help, /info, /reset, /prompt view, /prompt set <teks>, /prompt clear."
	case ControlInfo:
		page, err := currentAgent.History().List(ctx, snapshot.Version, agent.HistoryQuery{Limit: 1})
		if err != nil {
			return err
		}
		historyState := "kosong"
		if len(page.Entries) > 0 {
			historyState = "aktif"
		}
		response = fmt.Sprintf("Agent aktif. Model: %s. Config version: %d. History: %s.", snapshot.Model.Model, snapshot.Version, historyState)
	case ControlReset:
		if err := handler.policy.AuthorizeHistoryReset(ctx, message, snapshot.Permission); err != nil {
			return handler.responses.Reply(ctx, message, snapshot.Version, "Perintah /reset hanya dapat digunakan oleh owner yang dikonfigurasi.")
		}
		if err := currentAgent.History().Reset(ctx, snapshot.Version); err != nil {
			return err
		}
		response = "History percakapan berhasil direset."
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "handle control command", fmt.Errorf("unknown control command"))
	}
	if command == ControlReset {
		// The confirmation itself is part of the full transcript, but it must
		// not immediately repopulate the new conversation context. The second
		// tombstone is idempotent and runs after the durable response plan exists.
		replyErr := handler.responses.Reply(ctx, message, snapshot.Version, response)
		resetErr := currentAgent.History().Reset(ctx, snapshot.Version)
		handler.observer.ObserveHistoryReset()
		if replyErr != nil {
			return replyErr
		}
		return resetErr
	}
	return handler.responses.Reply(ctx, message, snapshot.Version, response)
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
