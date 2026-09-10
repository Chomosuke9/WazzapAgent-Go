package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
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
}

type Capability string

type CapabilitySet struct {
	values []Capability
}

func NewCapabilitySet(values ...Capability) (CapabilitySet, error) {
	copyValues := append([]Capability(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	for index, value := range copyValues {
		text := string(value)
		if text == "" || len(text) > 128 || !utf8.ValidString(text) || strings.TrimSpace(text) != text {
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
	ID            identity.InvocationID
	Causation     CausationRef
	Cause         InvocationCause
	Sender        *SenderContext
	Quote         *QuoteContext
	Input         []ContentPart
	Capabilities  CapabilitySet
	PolicyVersion ConfigVersion
	RequestedAt   time.Time
}

type QuoteContext struct {
	// Sequence is optional transcript ordering metadata. It is deliberately not
	// part of invocation identity so quoted Part 2 turns remain replayable.
	Sequence  uint64
	MessageID identity.MessageID
	Role      HistoryRole
	SenderRef identity.SenderRef
	Text      string
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
	ProvenanceHistoryUser
	ProvenanceHistoryAssistant
	ProvenanceHistorySystem
	ProvenanceCurrentUser
	// ProvenanceHistoryTranscript is one user-role block containing the
	// complete compact transcript, including the current invocation. Keeping
	// the transcript in one block is part of the provider prompt contract.
	ProvenanceHistoryTranscript
)

type ModelMessage struct {
	Role       ModelRole
	Provenance ModelProvenance
	Content    string
}

type ModelRequest struct {
	Key              Key
	InvocationID     identity.InvocationID
	CurrentMessageID identity.MessageID
	ConfigVersion    ConfigVersion
	Model            ModelConfig
	Messages         []ModelMessage
	Capabilities     CapabilitySet
	ContextMessages  map[string]identity.MessageID
}

const MaxModelEffects = 8

// EffectIntent is a closed, provider-neutral model output. Unlike a command
// string or JSON blob, every variant is typed, chat-bound by the invocation,
// and validated before a durable effect row can be planned.
type EffectKind uint8

const (
	EffectReact EffectKind = iota + 1
	EffectDeleteMessage
	EffectMarkRead
	EffectSetChatPresence
	EffectRunGroupCommand
)

type PresenceState string

const (
	PresenceComposing PresenceState = "composing"
	PresencePaused    PresenceState = "paused"
)

type EffectIntent struct {
	Kind            EffectKind
	TargetMessageID identity.MessageID
	Emoji           string
	Presence        PresenceState
	Command         string
}

func (intent EffectIntent) Capability() Capability {
	switch intent.Kind {
	case EffectReact:
		return "message.react"
	case EffectDeleteMessage:
		return "message.delete"
	case EffectMarkRead:
		return "message.mark-read"
	case EffectSetChatPresence:
		return "chat.presence"
	case EffectRunGroupCommand:
		fields := strings.Fields(intent.Command)
		if len(fields) >= 2 && fields[0] == "/group" {
			return Capability("group." + fields[1])
		}
		return ""
	default:
		return ""
	}
}

func (intent EffectIntent) Durable() bool {
	return intent.Kind == EffectReact || intent.Kind == EffectDeleteMessage || intent.Kind == EffectRunGroupCommand
}

func (intent EffectIntent) Validate() error {
	switch intent.Kind {
	case EffectReact:
		if intent.TargetMessageID.IsZero() || strings.TrimSpace(intent.Emoji) == "" || !utf8.ValidString(intent.Emoji) || len(intent.Emoji) > 64 || intent.Presence != "" || intent.Command != "" {
			return NewError(ErrorInvalidArgument, "validate reaction intent", fmt.Errorf("target and bounded emoji are required"))
		}
	case EffectDeleteMessage, EffectMarkRead:
		if intent.TargetMessageID.IsZero() || intent.Emoji != "" || intent.Presence != "" || intent.Command != "" {
			return NewError(ErrorInvalidArgument, "validate message effect intent", fmt.Errorf("only a target message is allowed"))
		}
	case EffectSetChatPresence:
		if !intent.TargetMessageID.IsZero() || intent.Emoji != "" || intent.Command != "" || (intent.Presence != PresenceComposing && intent.Presence != PresencePaused) {
			return NewError(ErrorInvalidArgument, "validate presence intent", fmt.Errorf("valid presence state is required"))
		}
	case EffectRunGroupCommand:
		fields := strings.Fields(intent.Command)
		if len(intent.Command) > 1024 || len(fields) < 2 || fields[0] != "/group" || (fields[1] != "delete" && fields[1] != "mute" && fields[1] != "kick") || intent.Emoji != "" || intent.Presence != "" {
			return NewError(ErrorInvalidArgument, "validate group command intent", fmt.Errorf("only bounded /group delete, mute, or kick commands are allowed"))
		}
		if fields[1] == "delete" && intent.TargetMessageID.IsZero() {
			return NewError(ErrorInvalidArgument, "validate group command intent", fmt.Errorf("group delete requires a message anchor"))
		}
		if fields[1] != "delete" && !intent.TargetMessageID.IsZero() {
			return NewError(ErrorInvalidArgument, "validate group command intent", fmt.Errorf("only group delete accepts a message anchor"))
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
		return NewError(ErrorInvalidArgument, "validate model effect", fmt.Errorf("tool call ID is invalid"))
	}
	if err := effect.Intent.Validate(); err != nil {
		return err
	}
	if !capabilities.Has(effect.Intent.Capability()) {
		return NewError(ErrorPermissionDenied, "validate model effect", fmt.Errorf("effect capability was not granted"))
	}
	return nil
}

type ModelResult struct {
	Text    string
	Effects []ModelEffect
}

type ModelInvoker interface {
	Generate(context.Context, ModelRequest) (ModelResult, error)
}

type InvocationDigest [32]byte

func DigestInvocation(key Key, invocation Invocation) (InvocationDigest, error) {
	if err := validateInvocation(key, invocation); err != nil {
		return InvocationDigest{}, err
	}
	var canonical bytes.Buffer
	// Preserve the exact Part 1 digest for invocations without a quote so
	// durable turns remain replayable after upgrading. Quote-aware invocations
	// use a new canonical version instead of silently changing v1.
	if invocation.Quote == nil {
		canonical.WriteString("wazzapagent.invocation.v1")
	} else {
		canonical.WriteString("wazzapagent.invocation.v2")
	}
	writeField(&canonical, key.TenantID.String())
	writeField(&canonical, key.AccountID.String())
	writeField(&canonical, key.ChatID.String())
	canonical.WriteByte(byte(invocation.Cause))
	canonical.WriteByte(byte(invocation.Causation.Kind))
	writeField(&canonical, invocation.Causation.ID.String())
	if invocation.Sender == nil {
		canonical.WriteByte(0)
	} else {
		canonical.WriteByte(1)
		writeField(&canonical, invocation.Sender.ParticipantID.String())
		writeField(&canonical, invocation.Sender.Ref.String())
		writeField(&canonical, invocation.Sender.DisplayName)
	}
	if invocation.Quote != nil {
		writeField(&canonical, invocation.Quote.MessageID.String())
		canonical.WriteByte(byte(invocation.Quote.Role))
		writeField(&canonical, invocation.Quote.SenderRef.String())
		writeField(&canonical, invocation.Quote.Text)
	}
	_ = binary.Write(&canonical, binary.BigEndian, uint32(len(invocation.Input)))
	for _, part := range invocation.Input {
		switch typed := part.(type) {
		case TextPart:
			canonical.WriteByte(1)
			writeField(&canonical, typed.Text)
		default:
			return InvocationDigest{}, NewError(ErrorUnsupported, "digest invocation", fmt.Errorf("unsupported content part"))
		}
	}
	values := invocation.Capabilities.Values()
	_ = binary.Write(&canonical, binary.BigEndian, uint32(len(values)))
	for _, capability := range values {
		writeField(&canonical, string(capability))
	}
	return sha256.Sum256(canonical.Bytes()), nil
}

func validateInvocation(key Key, invocation Invocation) error {
	if err := key.Validate(); err != nil {
		return err
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
		return NewError(ErrorInvalidArgument, "validate invocation", fmt.Errorf("invalid sender display name"))
	}
	if err := validateQuoteContext(invocation.Quote); err != nil {
		return err
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
	return &copyQuote
}

func cloneContent(parts []ContentPart) []ContentPart {
	result := make([]ContentPart, len(parts))
	copy(result, parts)
	return result
}

func writeField(buffer *bytes.Buffer, value string) {
	_ = binary.Write(buffer, binary.BigEndian, uint32(len(value)))
	buffer.WriteString(value)
}
