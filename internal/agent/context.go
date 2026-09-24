package agent

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/mention"
)

const (
	DefaultHistoryWindow    = 64
	DefaultMaxContextBytes  = 64 * 1024
	MaxContextBytes         = 1024 * 1024
	compactContextIDModulus = 1_000_000
	contextReasoning        = "<reasoning>\nBefore you act, examine the LATEST message in `Current messages (burst)` FIRST, then answer EACH of these to yourself in your thinking — never in your reply `text`:\n1. What is the latest message actually saying/asking? (read its SENDER line, not the REPLYING TO line.)\n2. What does the sender actually want — and am I replying to the right person in a multi-party thread? Should I just leave it to not reply someone by just sending reaction or sticker?\n3. Does answering it need a tool, command, or sub-agent — or is text alone enough? If yes, what's the rule for using these things?\n4. Which exact `context_msg_id` and `senderRef` do I target? (copy them; do not guess. Wrong target ID is the #1 failure.)\n5. Does anything in `<long_term_memory>`, group state, or chat-state (private/group) change my answer?\nOnly after answering all five do you produce your tool call. DO NOT produce a tool call before you answer ALL of them. No exception.\n</reasoning>"
)

type ContextBuildRequest struct {
	Config              ConfigSnapshot
	Chat                ChatContext
	History             []HistoryEntry
	CurrentInvocationID identity.InvocationID
}

// ChatContext is the provider-neutral, read-only chat metadata shown to the
// model. It is not an authorization decision; effects still recheck authority.
type ChatContext struct {
	Kind        string
	Name        string
	Description string
	BotIsAdmin  bool
}

func (chat ChatContext) Validate() error {
	if chat.Kind != "group" && chat.Kind != "private" {
		return fmt.Errorf("chat kind must be group or private")
	}
	if !utf8.ValidString(chat.Name) || !utf8.ValidString(chat.Description) ||
		len(chat.Name) > 512 || len(chat.Description) > 4096 {
		return fmt.Errorf("chat metadata is invalid")
	}
	if chat.Kind == "private" && chat.BotIsAdmin {
		return fmt.Errorf("private chat cannot have group admin role")
	}
	return nil
}

type ContextBuilder interface {
	Build(ContextBuildRequest) ([]ModelMessage, error)
}

type DeterministicContextBuilder struct {
	maxBytes      uint32
	assistantName string
}

func NewDeterministicContextBuilder(maxBytes uint32, assistantName string) (*DeterministicContextBuilder, error) {
	if maxBytes == 0 || maxBytes > MaxContextBytes {
		return nil, NewError(ErrorInvalidArgument, "create context builder", fmt.Errorf("max context bytes must be between 1 and %d", MaxContextBytes))
	}
	if assistantName == "" || cleanMentionName(assistantName) != assistantName {
		return nil, NewError(ErrorInvalidArgument, "create context builder", fmt.Errorf("assistant name is invalid for bot mentions"))
	}
	return &DeterministicContextBuilder{maxBytes: maxBytes, assistantName: assistantName}, nil
}

func (builder *DeterministicContextBuilder) Build(request ContextBuildRequest) ([]ModelMessage, error) {
	if builder == nil || request.CurrentInvocationID.IsZero() {
		return nil, NewError(ErrorInvalidArgument, "build model context", fmt.Errorf("builder and current invocation ID are required"))
	}
	if err := validateConfigSnapshot(request.Config); err != nil {
		return nil, NewError(ErrorIntegrityFailure, "build model context", err)
	}
	if err := request.Chat.Validate(); err != nil {
		return nil, NewError(ErrorIntegrityFailure, "build model context", err)
	}
	// Model history is intentionally one logical user block. The transcript
	// renderer is compact and already carries role/sender/quote markers, so
	// turning every durable entry into a provider message wastes tokens and
	// changes the compact prompt shape selected for this project.
	messages := make([]ModelMessage, 0, 4)
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
	messages = append(messages, ModelMessage{
		Role: ModelUser, Provenance: ProvenanceChatInformation,
		Content: formatChatInformation(request.Chat, request.Config.Permission.ModerationLevel),
	})
	type builtHistoryEntry struct {
		rendered     string
		invocationID identity.InvocationID
		current      bool
		role         HistoryRole
	}
	instructions := append([]ModelMessage(nil), messages...)
	historyEntries := make([]builtHistoryEntry, 0, len(request.History))
	mentionNames := historyDisplayNames(request.History)
	currentFound := false
	for _, entry := range request.History {
		if _, err := DigestHistoryEntry(entry); err != nil {
			return nil, err
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
		rendered, err := serializeHistoryEntry(entry, mentionNames, builder.assistantName)
		if err != nil {
			return nil, err
		}
		historyEntries = append(historyEntries, builtHistoryEntry{
			rendered: rendered, invocationID: entry.InvocationID, current: current, role: entry.Role,
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
		rendered := make([]string, 0, len(historyEntries)+3)
		burstStart := 0
		for index, entry := range historyEntries {
			if entry.role == HistoryAssistant {
				burstStart = index + 1
			}
		}
		rendered = append(rendered, "older messages:")
		for _, entry := range historyEntries[:burstStart] {
			rendered = append(rendered, entry.rendered)
		}
		rendered = append(rendered, contextReasoning)
		rendered = append(rendered, "current messages(burst):")
		for _, entry := range historyEntries[burstStart:] {
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

func formatChatInformation(chat ChatContext, level ModerationLevel) string {
	name := strings.Join(strings.Fields(chat.Name), " ")
	description := strings.Join(strings.Fields(chat.Description), " ")
	if name == "" {
		name = "(unnamed group)"
	}
	if description == "" {
		description = "(none)"
	}
	lines := []string{"Chat information:"}
	if chat.Kind == "group" {
		lines = append(lines, "- Group name: "+name, "- Group description: "+description)
	} else {
		lines = append(lines, "- Chat name: (private chat)")
	}
	role := "regular member"
	if chat.Kind == "group" && chat.BotIsAdmin {
		role = "admin"
	}
	capabilities := "none"
	effectiveLevel := ModerationNone
	if chat.Kind == "group" && chat.BotIsAdmin {
		effectiveLevel = level
		switch level {
		case ModerationDelete:
			capabilities = "delete messages"
		case ModerationDeleteMute:
			capabilities = "delete messages, mute members"
		case ModerationDeleteMuteKick:
			capabilities = "delete messages, mute members, kick members"
		}
	}
	lines = append(lines,
		"- Chat state: "+chat.Kind,
		"- Bot role: "+role,
		fmt.Sprintf("- Bot moderation permission: %d", effectiveLevel),
		"- Bot moderation capabilities: "+capabilities+" (configured maximum; command permissions apply separately)",
	)
	return strings.Join(lines, "\n")
}

func serializeHistoryEntry(entry HistoryEntry, mentionNames map[string]string, assistantName string) (string, error) {
	text := renderMentionView(flattenContent(entry.Content), entry.Mentions, mentionNames, assistantName)
	if entry.Quote != nil {
		entry.Quote = cloneQuote(entry.Quote)
		entry.Quote.Text = renderMentionView(entry.Quote.Text, entry.Quote.Mentions, mentionNames, assistantName)
	}
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

func historyDisplayNames(history []HistoryEntry) map[string]string {
	names := make(map[string]string)
	for _, entry := range history {
		if entry.Sender == nil || entry.Sender.Ref.IsZero() {
			continue
		}
		if name := cleanMentionName(entry.Sender.DisplayName); name != "" {
			names[entry.Sender.Ref.String()] = name
		}
	}
	return names
}

func renderMentionView(text string, bindings []MentionContext, names map[string]string, assistantName string) string {
	if len(bindings) == 0 {
		return text
	}
	replacements := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if binding.Bot {
			replacements[binding.Token] = "@" + assistantName + " (bot)"
			continue
		}
		name := cleanMentionName(names[binding.SenderRef.String()])
		if name == "" {
			name = cleanMentionName(binding.DisplayName)
		}
		if name == "" {
			name = "Unknown"
		}
		replacements[binding.Token] = fmt.Sprintf("@%s (%s)", name, binding.SenderRef.String())
	}
	return mention.Rewrite(text, replacements)
}

func cleanMentionName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '@' || r == '(' || r == ')' || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, name)
	return strings.Join(strings.Fields(name), " ")
}

// formatCompactHistoryEntry deliberately keeps the compact transcript grammar
// used by the model context. Durable storage still keeps structured
// identity, quote, delivery, and timestamp fields; this is only the view sent
// to the model. Keeping the view compact matters because it is repeated on
// every invocation.
//
// Example:
//
//	【#000040】 12:56
//	REPLYING TO 【#000038】 Alice: "earlier text"
//	Alice 【012345】: lanjutkan
func formatCompactHistoryEntry(entry HistoryEntry, text string) string {
	timestamp := entry.CreatedAt.UTC().Format("15:04")
	if entry.Role == HistorySystem {
		return fmt.Sprintf("【#system】 %s\nSYSTEM: %s", timestamp, text)
	}

	contextID := formatCompactContextID(entry.Sequence)
	if entry.Role == HistoryAssistant && entry.Delivery != DeliverySucceeded {
		// Pending/unknown assistant output is normally omitted from model
		// context. Keep this marker for callers that render an entry directly,
		// matching the compact transcript while delivery is unresolved.
		contextID = "pending"
	}
	lines := []string{fmt.Sprintf("【#%s】 %s", contextID, timestamp)}
	if entry.Quote != nil {
		quotedName := entry.Quote.SenderRef.String()
		if entry.Quote.Role == HistoryAssistant {
			quotedName = "You"
		}
		lines = append(lines, fmt.Sprintf("REPLYING TO 【#%s】 %s: %q", formatCompactContextID(entry.Quote.Sequence), quotedName, entry.Quote.Text))
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
	return fmt.Sprintf("%06d", sequence%compactContextIDModulus)
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
	chatInformationCount := 0
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
		case ProvenanceChatInformation:
			chatInformationCount++
			valid = message.Role == ModelUser && historyTranscriptCount == 0 && currentCount == 0
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
	if basePromptCount > 1 || overrideCount > 1 || chatInformationCount > 1 {
		return NewError(ErrorInvalidArgument, "validate model messages", fmt.Errorf("model prompts must not be duplicated"))
	}
	return nil
}
