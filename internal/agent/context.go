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
	// Model history is intentionally one logical user block. The transcript
	// renderer is compact and already carries role/sender/quote markers, so
	// turning every durable entry into a provider message wastes tokens and
	// changes the compact prompt shape selected for this project.
	messages := make([]ModelMessage, 0, 3)
	if request.Config.PromptOverride == nil || request.Config.PromptOverride.Mode != PromptReplace {
		messages = append(messages, ModelMessage{
			Role: ModelSystem, Provenance: ProvenanceBasePrompt, Content: request.Config.Prompt,
		})
	}
	if request.Config.PromptOverride != nil {
		messages = append(messages, ModelMessage{
			Role: ModelUser, Provenance: ProvenancePromptOverride, Content: request.Config.PromptOverride.Text,
		})
	}
	type builtHistoryEntry struct {
		rendered     string
		invocationID identity.InvocationID
		current      bool
	}
	instructions := append([]ModelMessage(nil), messages...)
	historyEntries := make([]builtHistoryEntry, 0, len(request.History))
	currentFound := false
	for _, entry := range request.History {
		if _, err := DigestHistoryEntry(entry); err != nil {
			return nil, NewError(ErrorIntegrityFailure, "build model context", err)
		}
		if entry.Role == HistoryAssistant && entry.Delivery != DeliverySucceeded {
			continue
		}
		current := entry.InvocationID == request.CurrentInvocationID
		if current {
			if entry.Role != HistoryUser || currentFound {
				return nil, NewError(ErrorIntegrityFailure, "build model context", fmt.Errorf("current invocation history is ambiguous"))
			}
			currentFound = true
		}
		rendered, err := serializeHistoryEntry(entry)
		if err != nil {
			return nil, err
		}
		historyEntries = append(historyEntries, builtHistoryEntry{
			rendered: rendered, invocationID: entry.InvocationID, current: current,
		})
	}
	if !currentFound {
		return nil, NewError(ErrorIntegrityFailure, "build model context", fmt.Errorf("current user history is missing"))
	}
	if len(historyEntries) == 0 || !historyEntries[len(historyEntries)-1].current {
		return nil, NewError(ErrorConflict, "build model context", fmt.Errorf("newer chat context exists; retry in chat order"))
	}
	for {
		messages = append(messages[:0], instructions...)
		rendered := make([]string, 0, len(historyEntries))
		for _, entry := range historyEntries {
			rendered = append(rendered, entry.rendered)
		}
		messages = append(messages, ModelMessage{
			Role:       ModelUser,
			Provenance: ProvenanceHistoryTranscript,
			Content:    strings.Join(rendered, "\n\n"),
		})
		if modelMessagesBytes(messages) <= int(builder.maxBytes) {
			return cloneModelMessages(messages), nil
		}
		if len(historyEntries) <= 1 {
			return nil, NewError(ErrorResourceExhausted, "build model context", fmt.Errorf("trusted prompts and current message exceed context byte limit"))
		}
		// Preserve trusted instructions and the current user entry. Remove every
		// message of the oldest logical invocation together so trimming cannot
		// leave an orphan assistant response or partial UTF-8/identity envelope.
		oldest := historyEntries[0].invocationID
		retained := make([]builtHistoryEntry, 0, len(historyEntries)-1)
		for _, entry := range historyEntries {
			if entry.invocationID != oldest {
				retained = append(retained, entry)
			}
		}
		if len(retained) == len(historyEntries) {
			return nil, NewError(ErrorIntegrityFailure, "build model context", fmt.Errorf("history trimming made no progress"))
		}
		historyEntries = retained
	}
}

func serializeHistoryEntry(entry HistoryEntry) (string, error) {
	text := flattenContent(entry.Content)
	switch entry.Role {
	case HistoryUser:
		return formatCompactHistoryEntry(entry, text), nil
	case HistoryAssistant:
		return formatCompactHistoryEntry(entry, text), nil
	case HistorySystem:
		return formatCompactHistoryEntry(entry, text), nil
	default:
		return "", NewError(ErrorIntegrityFailure, "serialize model context", fmt.Errorf("unsupported history role"))
	}
}

// formatCompactHistoryEntry deliberately keeps the compact transcript grammar
// used by the model context. Durable storage still keeps structured
// identity, quote, delivery, and timestamp fields; this is only the view sent
// to the model. Keeping the view compact matters because it is repeated on
// every invocation.
//
// Example:
//
//	【000040】 12:56
//	REPLYING TO 【000038】
//	Alice 【012345】: lanjutkan
func formatCompactHistoryEntry(entry HistoryEntry, text string) string {
	timestamp := entry.CreatedAt.UTC().Format("15:04")
	if entry.Role == HistorySystem {
		return fmt.Sprintf("【system】 %s\nSYSTEM: %s", timestamp, text)
	}

	contextID := formatCompactContextID(entry.Sequence)
	if entry.Role == HistoryAssistant && entry.Delivery != DeliverySucceeded {
		// Pending/unknown assistant output is normally omitted from model
		// context. Keep this marker for callers that render an entry directly,
		// matching the compact transcript while delivery is unresolved.
		contextID = "pending"
	}
	lines := []string{fmt.Sprintf("【%s】 %s", contextID, timestamp)}
	if entry.Quote != nil {
		lines = append(lines, fmt.Sprintf("REPLYING TO 【%s】", formatCompactContextID(entry.Quote.Sequence)))
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

// Compact context IDs are six decimal digits and wrap at 999999. The durable
// sequence remains the source of truth for ordering and lookup; this modulo
// only affects the display representation sent to the model.
func formatCompactContextID(sequence uint64) string {
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
	historyTranscriptCount := 0
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
			// Prompt overrides originate from the user/configuration boundary,
			// so they are deliberately a user-role block rather than trusted
			// system instructions. The application safety policy remains the
			// provider-owned system message that precedes this list.
			valid = promptPhase && message.Role == ModelUser
			promptPhase = false
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
		case ProvenanceHistoryTranscript:
			promptPhase = false
			historyTranscriptCount++
			valid = message.Role == ModelUser
		}
		if !valid {
			return NewError(ErrorInvalidArgument, "validate model messages", fmt.Errorf("model role and provenance do not match"))
		}
	}
	last := messages[len(messages)-1].Provenance
	if historyTranscriptCount > 0 {
		if historyTranscriptCount != 1 || currentCount != 0 || last != ProvenanceHistoryTranscript {
			return NewError(ErrorInvalidArgument, "validate model messages", fmt.Errorf("history transcript must be one final user block"))
		}
	} else if currentCount != 1 || last != ProvenanceCurrentUser {
		return NewError(ErrorInvalidArgument, "validate model messages", fmt.Errorf("current user message must be last"))
	}
	if basePromptCount > 1 || overrideCount > 1 {
		return NewError(ErrorInvalidArgument, "validate model messages", fmt.Errorf("model prompts must not be duplicated"))
	}
	return nil
}
