package agent

import (
	"fmt"
	"strings"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

const (
	DefaultHistoryWindow   = 64
	DefaultMaxContextBytes = 64 * 1024
	MaxContextBytes        = 1024 * 1024
)

type ContextBuildRequest struct {
	Config              ConfigSnapshot
	History             []HistoryEntry
	CurrentInvocationID identity.InvocationID
}

type ContextBuilder interface {
	Build(ContextBuildRequest) ([]ModelMessage, error)
}

type DeterministicContextBuilder struct {
	maxBytes uint32
}

func NewDeterministicContextBuilder(maxBytes uint32) (*DeterministicContextBuilder, error) {
	if maxBytes == 0 || maxBytes > MaxContextBytes {
		return nil, NewError(ErrorInvalidArgument, "create context builder", fmt.Errorf("max context bytes must be between 1 and %d", MaxContextBytes))
	}
	return &DeterministicContextBuilder{maxBytes: maxBytes}, nil
}

func (builder *DeterministicContextBuilder) Build(request ContextBuildRequest) ([]ModelMessage, error) {
	if builder == nil || request.CurrentInvocationID.IsZero() {
		return nil, NewError(ErrorInvalidArgument, "build model context", fmt.Errorf("builder and current invocation ID are required"))
	}
	if err := validateConfigSnapshot(request.Config); err != nil {
		return nil, err
	}
	messages := make([]ModelMessage, 0, len(request.History)+2)
	if request.Config.PromptOverride == nil || request.Config.PromptOverride.Mode != PromptReplace {
		messages = append(messages, ModelMessage{
			Role: ModelSystem, Provenance: ProvenanceBasePrompt, Content: request.Config.Prompt,
		})
	}
	if request.Config.PromptOverride != nil {
		messages = append(messages, ModelMessage{
			Role: ModelSystem, Provenance: ProvenancePromptOverride, Content: request.Config.PromptOverride.Text,
		})
	}
	type builtHistoryMessage struct {
		message      ModelMessage
		invocationID identity.InvocationID
		current      bool
	}
	instructions := append([]ModelMessage(nil), messages...)
	historyMessages := make([]builtHistoryMessage, 0, len(request.History))
	currentFound := false
	for _, entry := range request.History {
		if _, err := DigestHistoryEntry(entry); err != nil {
			return nil, NewError(ErrorIntegrityFailure, "build model context", err)
		}
		if entry.Role == HistoryAssistant && entry.Delivery != DeliverySucceeded {
			continue
		}
		current := entry.InvocationID == request.CurrentInvocationID
		message, err := serializeHistoryMessage(entry, current)
		if err != nil {
			return nil, err
		}
		if current {
			if entry.Role != HistoryUser || currentFound {
				return nil, NewError(ErrorIntegrityFailure, "build model context", fmt.Errorf("current invocation history is ambiguous"))
			}
			currentFound = true
		}
		historyMessages = append(historyMessages, builtHistoryMessage{
			message: message, invocationID: entry.InvocationID, current: current,
		})
	}
	if !currentFound {
		return nil, NewError(ErrorIntegrityFailure, "build model context", fmt.Errorf("current user history is missing"))
	}
	if len(historyMessages) == 0 || !historyMessages[len(historyMessages)-1].current {
		return nil, NewError(ErrorConflict, "build model context", fmt.Errorf("newer chat context exists; retry in chat order"))
	}
	for {
		messages = append(messages[:0], instructions...)
		for _, built := range historyMessages {
			messages = append(messages, built.message)
		}
		if modelMessagesBytes(messages) <= int(builder.maxBytes) {
			return cloneModelMessages(messages), nil
		}
		if len(historyMessages) <= 1 {
			return nil, NewError(ErrorResourceExhausted, "build model context", fmt.Errorf("trusted prompts and current message exceed context byte limit"))
		}
		// Preserve trusted instructions and the current user entry. Remove every
		// message of the oldest logical invocation together so trimming cannot
		// leave an orphan assistant response or partial UTF-8/identity envelope.
		oldest := historyMessages[0].invocationID
		retained := make([]builtHistoryMessage, 0, len(historyMessages)-1)
		for _, built := range historyMessages {
			if built.invocationID != oldest {
				retained = append(retained, built)
			}
		}
		if len(retained) == len(historyMessages) {
			return nil, NewError(ErrorIntegrityFailure, "build model context", fmt.Errorf("history trimming made no progress"))
		}
		historyMessages = retained
	}
}

func serializeHistoryMessage(entry HistoryEntry, current bool) (ModelMessage, error) {
	text := flattenContent(entry.Content)
	switch entry.Role {
	case HistoryUser:
		provenance := ProvenanceHistoryUser
		if current {
			provenance = ProvenanceCurrentUser
		}
		return ModelMessage{Role: ModelUser, Provenance: provenance, Content: formatLegacyHistoryEntry(entry, text)}, nil
	case HistoryAssistant:
		return ModelMessage{Role: ModelAssistant, Provenance: ProvenanceHistoryAssistant, Content: formatLegacyHistoryEntry(entry, text)}, nil
	case HistorySystem:
		return ModelMessage{Role: ModelSystem, Provenance: ProvenanceHistorySystem, Content: text}, nil
	default:
		return ModelMessage{}, NewError(ErrorIntegrityFailure, "serialize model context", fmt.Errorf("unsupported history role"))
	}
}

// formatLegacyHistoryEntry deliberately keeps the compact transcript grammar
// used by the original WazzapAgent. Durable storage still keeps structured
// identity, quote, delivery, and timestamp fields; this is only the view sent
// to the model. Keeping the view compact matters because it is repeated on
// every invocation.
//
// Example:
//
//	【000040】 12:56
//	REPLYING TO 【000038】
//	Alice 【u_01234567】: lanjutkan
func formatLegacyHistoryEntry(entry HistoryEntry, text string) string {
	timestamp := entry.CreatedAt.UTC().Format("15:04")
	if entry.Role == HistorySystem {
		return fmt.Sprintf("【system】 %s\nSYSTEM: %s", timestamp, text)
	}

	contextID := formatLegacyContextID(entry.Sequence)
	if entry.Role == HistoryAssistant && entry.Delivery != DeliverySucceeded {
		// Pending/unknown assistant output is normally omitted from model
		// context. Keep this marker for callers that render an entry directly,
		// matching the legacy transcript while delivery is unresolved.
		contextID = "pending"
	}
	lines := []string{fmt.Sprintf("【%s】 %s", contextID, timestamp)}
	if entry.Quote != nil {
		lines = append(lines, fmt.Sprintf("REPLYING TO 【%s】", formatLegacyContextID(entry.Quote.Sequence)))
	}

	if entry.Role == HistoryAssistant {
		lines = append(lines, fmt.Sprintf("You 【You】: %s", text))
		return strings.Join(lines, "\n")
	}

	displayName := "unknown"
	senderRef := "unknown"
	if entry.Sender != nil {
		if trimmed := strings.TrimSpace(entry.Sender.DisplayName); trimmed != "" {
			displayName = trimmed
		}
		if value := entry.Sender.Ref.String(); value != "" {
			senderRef = value
		}
	}
	lines = append(lines, fmt.Sprintf("%s 【%s】: %s", displayName, senderRef, text))
	return strings.Join(lines, "\n")
}

// Legacy context IDs are six decimal digits and wrap at 999999. The durable
// sequence remains the source of truth for ordering and lookup; this modulo
// only preserves the old compact display representation.
func formatLegacyContextID(sequence uint64) string {
	return fmt.Sprintf("%06d", sequence%1_000_000)
}

func flattenContent(parts []ContentPart) string {
	var builder strings.Builder
	for index, part := range parts {
		if index > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(part.(TextPart).Text)
	}
	return builder.String()
}

func modelMessagesBytes(messages []ModelMessage) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content) + 8
	}
	return total
}

func cloneModelMessages(messages []ModelMessage) []ModelMessage {
	return append([]ModelMessage(nil), messages...)
}

// SerializeModelMessages renders the exact role/content list built for a
// model invocation in a human-readable form used by /dump.
func SerializeModelMessages(messages []ModelMessage) string {
	sections := make([]string, 0, len(messages))
	for _, message := range messages {
		role := "UNKNOWN"
		switch message.Role {
		case ModelSystem:
			role = "SYSTEM"
		case ModelUser:
			role = "USER"
		case ModelAssistant:
			role = "ASSISTANT"
		}
		sections = append(sections, fmt.Sprintf("=== %s ===\n%s", role, message.Content))
	}
	return strings.Join(sections, "\n\n")
}

// ValidateModelMessages enforces that only known typed provenance can occupy a
// model role. Adapters call it before translating to provider-specific DTOs.
func ValidateModelMessages(messages []ModelMessage) error {
	if len(messages) == 0 {
		return NewError(ErrorInvalidArgument, "validate model messages", fmt.Errorf("model messages are required"))
	}
	currentCount := 0
	basePromptCount := 0
	overrideCount := 0
	promptPhase := true
	for _, message := range messages {
		if strings.TrimSpace(message.Content) == "" {
			return NewError(ErrorInvalidArgument, "validate model messages", fmt.Errorf("model message content is required"))
		}
		valid := false
		switch message.Provenance {
		case ProvenanceBasePrompt:
			basePromptCount++
			valid = promptPhase && overrideCount == 0 && message.Role == ModelSystem
		case ProvenancePromptOverride:
			overrideCount++
			valid = promptPhase && message.Role == ModelSystem
		case ProvenanceHistorySystem:
			promptPhase = false
			valid = message.Role == ModelSystem
		case ProvenanceHistoryUser:
			promptPhase = false
			valid = message.Role == ModelUser
		case ProvenanceCurrentUser:
			promptPhase = false
			currentCount++
			valid = message.Role == ModelUser
		case ProvenanceHistoryAssistant:
			promptPhase = false
			valid = message.Role == ModelAssistant
		}
		if !valid {
			return NewError(ErrorInvalidArgument, "validate model messages", fmt.Errorf("model role and provenance do not match"))
		}
	}
	if currentCount != 1 || messages[len(messages)-1].Provenance != ProvenanceCurrentUser {
		return NewError(ErrorInvalidArgument, "validate model messages", fmt.Errorf("current user message must be last"))
	}
	if basePromptCount > 1 || overrideCount > 1 {
		return NewError(ErrorInvalidArgument, "validate model messages", fmt.Errorf("model prompts must not be duplicated"))
	}
	return nil
}
