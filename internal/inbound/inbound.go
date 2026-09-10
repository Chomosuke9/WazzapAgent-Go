package inbound

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
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

type PromptMutation struct {
	ExpectedVersion agent.ConfigVersion
	AppliedVersion  agent.ConfigVersion
}

type Registry interface {
	AgentFor(context.Context, agent.Key) (*agent.Agent, error)
}

type Policy interface {
	AuthorizeInvocation(context.Context, conversation.IncomingMessage, agent.PermissionConfig) error
	AuthorizeCommand(context.Context, policy.Principal, policy.Capability, agent.PermissionConfig) error
	ModelCapabilities(agent.PermissionConfig) (agent.CapabilitySet, error)
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
	if options.Activity == nil {
		options.Activity = discardAIActivity{}
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

	if request, descriptor, recognized := parseRegisteredCommand(message.Text); recognized {
		return handler.resumeCommand(ctx, message, request, descriptor)
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

func (handler *Handler) resumeCommand(
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
	principal, err := policy.HumanPrincipal(message)
	if err != nil {
		return err
	}
	if err := handler.policy.AuthorizeCommand(ctx, principal, descriptor.Capability, snapshot.Permission); err != nil {
		return handler.responses.Reply(ctx, message, snapshot.Version, commandDeniedReply(descriptor.Capability))
	}

	switch request.Name {
	case "prompt":
		if descriptor.Capability != policy.CapabilityPromptWrite {
			return commandCapabilityMismatch(request, descriptor.Capability)
		}
		parsed, recognized := ParsePromptCommand(canonicalCommandText(request))
		if !recognized {
			return agent.NewError(agent.ErrorIntegrityFailure, "dispatch registered command", fmt.Errorf("prompt command was not parsed"))
		}
		return handler.handlePrompt(ctx, currentAgent, snapshot, message, parsed)
	case "help", "info", "reset", "dump":
		parsed, valid := parseControlRequest(request)
		if !valid {
			return handler.responses.Reply(ctx, message, snapshot.Version, invalidControlReply(request))
		}
		if descriptor.Capability != controlCapability(parsed) {
			return commandCapabilityMismatch(request, descriptor.Capability)
		}
		return handler.handleControl(ctx, currentAgent, snapshot, message, parsed)
	case "permission":
		if descriptor.Capability != policy.CapabilityPermissionWrite {
			return commandCapabilityMismatch(request, descriptor.Capability)
		}
		parsed, recognized := ParsePermissionCommand(canonicalCommandText(request))
		if !recognized {
			return agent.NewError(agent.ErrorIntegrityFailure, "dispatch registered command", fmt.Errorf("permission command was not parsed"))
		}
		return handler.handlePermission(ctx, currentAgent, snapshot, message, parsed)
	default:
		return commandCapabilityMismatch(request, descriptor.Capability)
	}
}

func commandDeniedReply(capability policy.Capability) string {
	switch capability {
	case policy.CapabilityPromptWrite:
		return "Perintah /prompt hanya dapat digunakan oleh owner yang dikonfigurasi."
	case policy.CapabilityHistoryReset:
		return "Perintah /reset hanya dapat digunakan oleh owner yang dikonfigurasi."
	case policy.CapabilityChatContextRead:
		return "Perintah /dump hanya dapat digunakan oleh owner yang dikonfigurasi."
	case policy.CapabilityPermissionWrite:
		return "Perintah /permission hanya dapat digunakan oleh owner yang dikonfigurasi."
	default:
		return "Perintah ini tidak dapat digunakan pada chat ini."
	}
}

func controlCapability(command ControlCommandKind) policy.Capability {
	switch command {
	case ControlHelp:
		return policy.CapabilityCommandHelp
	case ControlInfo:
		return policy.CapabilityCommandInfo
	case ControlReset:
		return policy.CapabilityHistoryReset
	case ControlDump:
		return policy.CapabilityChatContextRead
	default:
		return ""
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

type ControlCommandKind uint8

const (
	ControlHelp ControlCommandKind = iota + 1
	ControlInfo
	ControlReset
	ControlDump
)

func ParseControlCommand(text string) (ControlCommandKind, bool) {
	switch text {
	case "/help":
		return ControlHelp, true
	case "/info":
		return ControlInfo, true
	case "/reset":
		return ControlReset, true
	case "/dump":
		return ControlDump, true
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
		response = "Perintah: /help, /info, /dump, /reset, /prompt view, /prompt set <teks>, /prompt clear, /permission <0-3>."
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
		if err := currentAgent.History().Reset(ctx, snapshot.Version); err != nil {
			return err
		}
		response = "History percakapan berhasil direset."
	case ControlDump:
		input, err := currentAgent.BuildInput(ctx, snapshot.Version, message.InvocationID)
		if err != nil {
			return err
		}
		response = agent.SerializeModelMessages(input)
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

func (handler *Handler) handlePermission(
	ctx context.Context,
	currentAgent *agent.Agent,
	snapshot agent.ConfigSnapshot,
	message conversation.IncomingMessage,
	command PermissionCommand,
) error {
	switch command.Kind {
	case PermissionView:
		return handler.responses.Reply(ctx, message, snapshot.Version, formatModerationLevel(snapshot.Permission.ModerationLevel))
	case PermissionSet:
		updated, err := handler.applyPermissionMutation(ctx, currentAgent, snapshot, message, command)
		if err != nil {
			return err
		}
		return handler.responses.Reply(ctx, message, updated, "Permission diperbarui. "+formatModerationLevel(command.Level))
	case PermissionInvalid:
		return handler.responses.Reply(ctx, message, snapshot.Version, "Format: /permission 0, 1, 2, atau 3. Level 0: tanpa moderasi; 1: delete; 2: delete+mute; 3: delete+mute+kick.")
	default:
		return agent.NewError(agent.ErrorIntegrityFailure, "handle permission command", fmt.Errorf("unknown command kind"))
	}
}

func (handler *Handler) applyPermissionMutation(
	ctx context.Context,
	currentAgent *agent.Agent,
	snapshot agent.ConfigSnapshot,
	message conversation.IncomingMessage,
	command PermissionCommand,
) (agent.ConfigVersion, error) {
	journal, err := handler.store.BeginPermissionMutation(ctx, message, command, snapshot.Version)
	if err != nil {
		return 0, err
	}
	if journal.AppliedVersion != 0 {
		return journal.AppliedVersion, nil
	}
	if snapshot.Version == journal.ExpectedVersion+1 && permissionMutationMatches(snapshot, command) {
		if err := handler.store.MarkPermissionMutationApplied(ctx, message, journal.ExpectedVersion, snapshot.Version); err != nil {
			return 0, err
		}
		return snapshot.Version, nil
	}
	if snapshot.Version != journal.ExpectedVersion {
		return 0, agent.NewError(agent.ErrorConflict, "apply permission mutation", fmt.Errorf("config changed after command authorization; resend the command"))
	}
	permission := snapshot.Permission
	permission.ModerationLevel = command.Level
	updated, err := currentAgent.Config().SetPermission(ctx, snapshot.Version, permission)
	if err != nil {
		return 0, err
	}
	if err := handler.store.MarkPermissionMutationApplied(ctx, message, journal.ExpectedVersion, updated.Version); err != nil {
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

func permissionMutationMatches(snapshot agent.ConfigSnapshot, command PermissionCommand) bool {
	return command.Kind == PermissionSet && snapshot.Permission.ModerationLevel == command.Level
}

type PermissionCommandKind uint8

const (
	PermissionInvalid PermissionCommandKind = iota + 1
	PermissionView
	PermissionSet
)

type PermissionCommand struct {
	Kind  PermissionCommandKind
	Level agent.ModerationLevel
}

func ParsePermissionCommand(text string) (PermissionCommand, bool) {
	if text == "/permission" || text == "/permission view" {
		return PermissionCommand{Kind: PermissionView}, true
	}
	if !strings.HasPrefix(text, "/permission ") {
		return PermissionCommand{}, false
	}
	argument := strings.TrimSpace(strings.TrimPrefix(text, "/permission "))
	if len(argument) == 1 && argument[0] >= '0' && argument[0] <= '3' {
		return PermissionCommand{Kind: PermissionSet, Level: agent.ModerationLevel(argument[0] - '0')}, true
	}
	return PermissionCommand{Kind: PermissionInvalid}, true
}

func formatModerationLevel(level agent.ModerationLevel) string {
	labels := [...]string{"Level 0: moderasi nonaktif.", "Level 1: delete.", "Level 2: delete dan mute.", "Level 3: delete, mute, dan kick."}
	if !level.Valid() {
		return "Permission tidak valid."
	}
	return labels[level]
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
