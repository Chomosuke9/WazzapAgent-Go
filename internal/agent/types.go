package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/mention"
)

const (
	MaxInputBytes       = 32 * 1024
	MaxDisplayNameBytes = 512
	MaxResponseBytes    = 16 * 1024
)

type Key struct {
	TenantID  identity.TenantID
	AccountID identity.AccountID
	ChatID    identity.ChatID
}

func (key Key) Validate() error {
	if key.TenantID.IsZero() || key.AccountID.IsZero() || key.ChatID.IsZero() {
		return NewError(ErrorInvalidArgument, "validate agent key", fmt.Errorf("tenant, account, and chat IDs are required"))
	}
	return nil
}

type InvocationCause uint8

const (
	CauseInboundMessage InvocationCause = iota + 1
	CauseScheduledTask
	CauseDirectRequest
	CauseSubagentResult
)

type CausationKind uint8

const (
	CausationMessage CausationKind = iota + 1
	CausationTask
	CausationRequest
	CausationSubagent
)

type CausationRef struct {
	Kind CausationKind
	ID   identity.CausationID
}

type SenderContext struct {
	ParticipantID identity.ParticipantID
	Ref           identity.SenderRef
	DisplayName   string
	IsAdmin       bool
	IsSuperAdmin  bool
}

// MentionContext binds one raw WhatsApp token to a model-safe identity. The
// display name is a mutable local-registry value and is intentionally excluded
// from identity digests.
type MentionContext struct {
	Token       string
	SenderRef   identity.SenderRef
	DisplayName string
	Bot         bool
}

// Capability names something the model may do beyond replying.
type Capability string

const (
	CapabilityCommandExecute Capability = "command.execute"
	CapabilityMessageReact   Capability = "message.react"
	CapabilityMessageDelete  Capability = "message.delete"
	CapabilityMessageSticker Capability = "message.sticker"
)

// Valid reports whether capability is one the application knows.
func (capability Capability) Valid() bool {
	switch capability {
	case CapabilityCommandExecute, CapabilityMessageReact, CapabilityMessageDelete, CapabilityMessageSticker:
		return true
	default:
		return false
	}
}

type CapabilitySet struct {
	values []Capability
}

func NewCapabilitySet(values ...Capability) (CapabilitySet, error) {
	copyValues := append([]Capability(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	for index, value := range copyValues {
		if !value.Valid() {
			return CapabilitySet{}, NewError(ErrorInvalidArgument, "create capability set", fmt.Errorf("invalid capability"))
		}
		if index > 0 && copyValues[index-1] == value {
			return CapabilitySet{}, NewError(ErrorInvalidArgument, "create capability set", fmt.Errorf("duplicate capability"))
		}
	}
	return CapabilitySet{values: copyValues}, nil
}

func (set CapabilitySet) Has(wanted Capability) bool {
	index := sort.Search(len(set.values), func(index int) bool { return set.values[index] >= wanted })
	return index < len(set.values) && set.values[index] == wanted
}

func (set CapabilitySet) Values() []Capability { return append([]Capability(nil), set.values...) }

type ContentPart interface{ isContentPart() }

type TextPart struct{ Text string }

func (TextPart) isContentPart() {}

type Invocation struct {
	ID           identity.InvocationID
	Causation    CausationRef
	Cause        InvocationCause
	Sender       *SenderContext
	Quote        *QuoteContext
	Input        []ContentPart
	Mentions     []MentionContext
	Capabilities CapabilitySet
	Commands     []string
	// Stickers are the chat's sticker catalog names, sorted.
	Stickers      []string
	PolicyVersion ConfigVersion
	RequestedAt   time.Time
}

type QuoteContext struct {
	// Sequence is optional transcript ordering metadata. It is deliberately not
	// part of invocation identity so quoted Part 2 turns remain replayable.
	Sequence           uint64
	MessageID          identity.MessageID
	Role               HistoryRole
	SenderRef          identity.SenderRef
	SenderIsAdmin      bool
	SenderIsSuperAdmin bool
	Text               string
	Mentions           []MentionContext
}

type ModelRole uint8

const (
	ModelSystem ModelRole = iota + 1
	ModelUser
	ModelAssistant
)

type ModelProvenance uint8

const (
	ProvenanceBasePrompt ModelProvenance = iota + 1
	ProvenancePromptOverride
	ProvenanceChatInformation
	ProvenanceHistoryUser
	ProvenanceHistoryAssistant
	ProvenanceHistorySystem
	ProvenanceCurrentUser
	// ProvenanceHistoryTranscript is one user-role block containing the
	// complete compact transcript, including the current invocation. Keeping
	// the transcript in one block is part of the provider prompt contract.
	ProvenanceHistoryTranscript
	// ProvenanceChatState is the <chat_state> block: this chat's settings
	// and scheduled tasks, each with the command that changes it.
	ProvenanceChatState
)

type ModelMessage struct {
	Role       ModelRole
	Provenance ModelProvenance
	Content    string
	// AdditionalPrompt fills the immutable system policy's per-request
	// {{additional_prompt}} placeholder and is valid only for the base prompt.
	AdditionalPrompt string
}

type ModelRequest struct {
	Key              Key
	InvocationID     identity.InvocationID
	CurrentMessageID identity.MessageID
	ConfigVersion    ConfigVersion
	Model            ModelConfig
	Messages         []ModelMessage
	Capabilities     CapabilitySet
	Commands         []string
	Stickers         []string
	ContextMessages  map[string]identity.MessageID
}

const MaxModelEffects = 8

// EffectIntent is a closed, provider-neutral model output: a reaction from
// react_to_message, a sticker from send_sticker, or a command from
// reply_message. Each is chat-bound by the
// invocation and validated before a durable effect row can be planned.
type EffectKind uint8

const (
	EffectReact      EffectKind = 1
	EffectRunCommand EffectKind = 5
	EffectSticker    EffectKind = 6
)

type EffectIntent struct {
	Kind            EffectKind
	TargetMessageID identity.MessageID
	Emoji           string
	Command         string
	// Sticker is a catalog name. TargetMessageID is the optional quote.
	Sticker string
}

// Capability is the tool capability the intent needs. RunCommand has none:
// the command registry's permission expression is the only check, evaluated
// when the command runs.
func (intent EffectIntent) Capability() Capability {
	switch intent.Kind {
	case EffectReact:
		return CapabilityMessageReact
	case EffectSticker:
		return CapabilityMessageSticker
	}
	return ""
}

func (intent EffectIntent) Validate() error {
	switch intent.Kind {
	case EffectReact:
		if intent.TargetMessageID.IsZero() || strings.TrimSpace(intent.Emoji) == "" || !utf8.ValidString(intent.Emoji) || len(intent.Emoji) > 64 || intent.Command != "" || intent.Sticker != "" {
			return NewError(ErrorInvalidArgument, "validate reaction intent", fmt.Errorf("target and bounded emoji are required"))
		}
	case EffectRunCommand:
		if strings.TrimSpace(intent.Command) != intent.Command || !strings.HasPrefix(intent.Command, "/") ||
			len(intent.Command) == 0 || len(intent.Command) > MaxInputBytes || !utf8.ValidString(intent.Command) ||
			intent.Emoji != "" || intent.Sticker != "" {
			return NewError(ErrorInvalidArgument, "validate command intent", fmt.Errorf("registered command is malformed"))
		}
	case EffectSticker:
		if !validStickerName(intent.Sticker) || intent.Emoji != "" || intent.Command != "" {
			return NewError(ErrorInvalidArgument, "validate sticker intent", fmt.Errorf("sticker name is malformed"))
		}
	default:
		return NewError(ErrorInvalidArgument, "validate effect intent", fmt.Errorf("effect kind is invalid"))
	}
	return nil
}

type ModelEffect struct {
	CallID string
	Intent EffectIntent
}

func (effect ModelEffect) Validate(capabilities CapabilitySet) error {
	if strings.TrimSpace(effect.CallID) != effect.CallID || len(effect.CallID) == 0 || len(effect.CallID) > 128 || !utf8.ValidString(effect.CallID) {
		return Errorf(ErrorInvalidArgument, "validate model effect", "tool call ID is invalid")
	}
	if err := effect.Intent.Validate(); err != nil {
		return NewError(ErrorInvalidArgument, "validate model effect", err)
	}
	if capability := effect.Intent.Capability(); capability != "" && !capabilities.Has(capability) {
		return Errorf(ErrorPermissionDenied, "validate model effect", "effect capability was not granted")
	}
	return nil
}

type ModelResult struct {
	Text             string
	ReplyToMessageID identity.MessageID
	Effects          []ModelEffect
}

type ModelInvoker interface {
	Generate(context.Context, ModelRequest) (ModelResult, error)
}

func validateInvocation(key Key, invocation Invocation) error {
	if err := key.Validate(); err != nil {
		return NewError(ErrorInvalidArgument, "validate invocation", err)
	}
	if invocation.ID.IsZero() || invocation.Causation.ID.IsZero() {
		return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("invocation and causation IDs are required"))
	}
	expectedKind := map[InvocationCause]CausationKind{
		CauseInboundMessage: CausationMessage,
		CauseScheduledTask:  CausationTask,
		CauseDirectRequest:  CausationRequest,
		CauseSubagentResult: CausationSubagent,
	}[invocation.Cause]
	if expectedKind == 0 || invocation.Causation.Kind != expectedKind {
		return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("cause and causation kind do not match"))
	}
	if invocation.Cause == CauseInboundMessage {
		if invocation.Sender == nil || invocation.Sender.ParticipantID.IsZero() || invocation.Sender.Ref.IsZero() {
			return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("inbound invocation requires trusted sender context"))
		}
	} else if invocation.Cause == CauseScheduledTask || invocation.Cause == CauseSubagentResult {
		if invocation.Sender != nil {
			return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("system invocation must not carry sender context"))
		}
	}
	if invocation.Sender != nil && (!utf8.ValidString(invocation.Sender.DisplayName) || len(invocation.Sender.DisplayName) > MaxDisplayNameBytes) {
		return Errorf(ErrorInvalidArgument, "validate invocation", "invalid sender display name")
	}
	if invocation.Sender != nil && invocation.Sender.IsSuperAdmin && !invocation.Sender.IsAdmin {
		return Errorf(ErrorInvalidArgument, "validate invocation", "superadmin sender must also be an admin")
	}
	if err := validateQuoteContext(invocation.Quote); err != nil {
		return NewError(ErrorInvalidArgument, "validate invocation", err)
	}
	if len(invocation.Input) == 0 {
		return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("input is required"))
	}
	total := 0
	for _, part := range invocation.Input {
		text, ok := part.(TextPart)
		if !ok {
			return NewError(ErrorUnsupported, "validate invocation", fmt.Errorf("Part 2 accepts text only"))
		}
		if text.Text == "" || !utf8.ValidString(text.Text) {
			return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("text must be non-empty valid UTF-8"))
		}
		total += len(text.Text)
		if total > MaxInputBytes {
			return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("text exceeds %d bytes", MaxInputBytes))
		}
	}
	if err := validateMentionContexts(flattenContent(invocation.Input), invocation.Mentions); err != nil {
		return NewError(ErrorInvalidArgument, "validate invocation", err)
	}
	for index, name := range invocation.Commands {
		if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, " /\t\r\n") {
			return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("model command name is invalid"))
		}
		if index > 0 && invocation.Commands[index-1] >= name {
			return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("model command names must be sorted and unique"))
		}
	}
	for index, name := range invocation.Stickers {
		if !validStickerName(name) || (index > 0 && invocation.Stickers[index-1] >= name) {
			return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("sticker names must be valid, sorted and unique"))
		}
	}
	if invocation.PolicyVersion == 0 {
		return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("policy version is required"))
	}
	if invocation.RequestedAt.IsZero() {
		return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("request time is required"))
	}
	return nil
}

func cloneSender(sender *SenderContext) *SenderContext {
	if sender == nil {
		return nil
	}
	copySender := *sender
	return &copySender
}

func cloneQuote(quote *QuoteContext) *QuoteContext {
	if quote == nil {
		return nil
	}
	copyQuote := *quote
	copyQuote.Mentions = cloneMentions(quote.Mentions)
	return &copyQuote
}

func cloneContent(parts []ContentPart) []ContentPart {
	result := make([]ContentPart, len(parts))
	copy(result, parts)
	return result
}

func cloneMentions(mentions []MentionContext) []MentionContext {
	return append([]MentionContext(nil), mentions...)
}

func validateMentionContexts(text string, mentions []MentionContext) error {
	if len(mentions) > mention.MaxBindings {
		return fmt.Errorf("mentions exceed %d", mention.MaxBindings)
	}
	seen := make(map[string]struct{}, len(mentions))
	for _, binding := range mentions {
		if !mention.ValidToken(binding.Token) || !mention.Contains(text, binding.Token) {
			return fmt.Errorf("mention token is invalid or absent from text")
		}
		if _, exists := seen[binding.Token]; exists {
			return fmt.Errorf("mention token is duplicated")
		}
		seen[binding.Token] = struct{}{}
		if binding.Bot != binding.SenderRef.IsZero() {
			return fmt.Errorf("mention identity is invalid")
		}
		if !utf8.ValidString(binding.DisplayName) || len(binding.DisplayName) > MaxDisplayNameBytes {
			return fmt.Errorf("mention display name is invalid")
		}
	}
	return nil
}

func quoteMentions(quote *QuoteContext) []MentionContext {
	if quote == nil {
		return nil
	}
	return quote.Mentions
}

func writeField(buffer *bytes.Buffer, value string) {
	_ = binary.Write(buffer, binary.BigEndian, uint32(len(value)))
	buffer.WriteString(value)
}

// validStickerName matches the sticker catalog's names: 1 to 64 of a-z, 0-9,
// "_" and "-".
func validStickerName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return false
		}
	}
	return true
}
