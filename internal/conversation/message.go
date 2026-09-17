package conversation

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/mention"
)

const MaxTextBytes = 32 * 1024
const MaxMentions = mention.MaxBindings

type ChatKind uint8

const (
	ChatDirect ChatKind = iota + 1
	ChatGroup
	ChatStatus
)

// IncomingMention is provider metadata for one raw token in IncomingCandidate
// text. Human targets carry a canonical LID; the current bot is represented
// explicitly because it intentionally has no chat-scoped senderRef.
type IncomingMention struct {
	Token       string
	TargetLID   identity.LID
	DisplayName string
	Bot         bool
}

// MentionBinding is the durable, provider-neutral identity bound to a raw
// mention token. It is used only when rendering a model-facing view.
type MentionBinding struct {
	Token     string
	SenderRef identity.SenderRef
	Bot       bool
}

// IncomingCandidate is the narrow trusted boundary between a provider adapter
// and durable intake. Provider addresses must not be logged or sent to a model.
type IncomingCandidate struct {
	TenantID                identity.TenantID
	AccountID               identity.AccountID
	ProviderMessageID       string
	ProviderQuotedMessageID string
	ProviderChatAddress     string
	SenderLID               identity.LID
	// ProviderSenderPhone is an optional delivery/addressing alias, never identity.
	ProviderSenderPhone string
	SenderName          string
	ChatKind            ChatKind
	Text                string
	Mentions            []IncomingMention
	MentionsBot         bool
	FromMe              bool
	Owner               bool
	Allowlisted         bool
	OccurredAt          time.Time
	ReceivedAt          time.Time
}

func (candidate IncomingCandidate) Validate() error {
	if candidate.TenantID.IsZero() || candidate.AccountID.IsZero() {
		return fmt.Errorf("tenant and account IDs are required")
	}
	if strings.TrimSpace(candidate.ProviderMessageID) == "" || len(candidate.ProviderMessageID) > 512 {
		return fmt.Errorf("provider message ID is invalid")
	}
	if len(candidate.ProviderQuotedMessageID) > 512 {
		return fmt.Errorf("provider quoted message ID is invalid")
	}
	if strings.TrimSpace(candidate.ProviderChatAddress) == "" || len(candidate.ProviderChatAddress) > 512 {
		return fmt.Errorf("provider chat address is invalid")
	}
	if candidate.SenderLID.IsZero() {
		return fmt.Errorf("sender LID is required")
	}
	if len(candidate.ProviderSenderPhone) > 512 {
		return fmt.Errorf("provider sender phone alias is invalid")
	}
	if candidate.ChatKind != ChatDirect && candidate.ChatKind != ChatGroup && candidate.ChatKind != ChatStatus {
		return fmt.Errorf("chat kind is invalid")
	}
	if candidate.Text == "" || !utf8.ValidString(candidate.Text) || len(candidate.Text) > MaxTextBytes {
		return fmt.Errorf("text must be non-empty valid UTF-8 within %d bytes", MaxTextBytes)
	}
	if err := validateIncomingMentions(candidate.Text, candidate.Mentions); err != nil {
		return err
	}
	if !utf8.ValidString(candidate.SenderName) || len(candidate.SenderName) > 512 {
		return fmt.Errorf("sender name is invalid")
	}
	if candidate.OccurredAt.IsZero() || candidate.ReceivedAt.IsZero() {
		return fmt.Errorf("timestamps are required")
	}
	return nil
}

type QuoteRole uint8

const (
	QuoteUser QuoteRole = iota + 1
	QuoteAssistant
)

type QuotedMessage struct {
	Sequence  uint64
	ID        identity.MessageID
	Role      QuoteRole
	SenderRef identity.SenderRef
	Text      string
	Mentions  []MentionBinding
}

type IncomingMessage struct {
	ID           identity.MessageID
	InvocationID identity.InvocationID
	CausationID  identity.CausationID
	TenantID     identity.TenantID
	AccountID    identity.AccountID
	ChatID       identity.ChatID
	SenderID     identity.ParticipantID
	SenderLID    identity.LID
	SenderRef    identity.SenderRef
	SenderName   string
	ChatKind     ChatKind
	Text         string
	Mentions     []MentionBinding
	Quote        *QuotedMessage
	RepliedToBot bool
	MentionsBot  bool
	FromMe       bool
	Owner        bool
	Allowlisted  bool
	OccurredAt   time.Time
	ReceivedAt   time.Time
}

func (message IncomingMessage) Validate() error {
	if message.ID.IsZero() || message.InvocationID.IsZero() || message.CausationID.IsZero() ||
		message.TenantID.IsZero() || message.AccountID.IsZero() || message.ChatID.IsZero() ||
		message.SenderID.IsZero() || message.SenderLID.IsZero() || message.SenderRef.IsZero() {
		return fmt.Errorf("canonical message identities are required")
	}
	if message.ChatKind != ChatDirect && message.ChatKind != ChatGroup && message.ChatKind != ChatStatus {
		return fmt.Errorf("chat kind is invalid")
	}
	if message.Text == "" || !utf8.ValidString(message.Text) || len(message.Text) > MaxTextBytes {
		return fmt.Errorf("text must be non-empty valid UTF-8 within %d bytes", MaxTextBytes)
	}
	if err := validateMentionBindings(message.Text, message.Mentions); err != nil {
		return err
	}
	if !utf8.ValidString(message.SenderName) || len(message.SenderName) > 512 {
		return fmt.Errorf("sender name is invalid")
	}
	if message.Quote != nil {
		if message.Quote.ID.IsZero() || (message.Quote.Role != QuoteUser && message.Quote.Role != QuoteAssistant) ||
			message.Quote.Text == "" || !utf8.ValidString(message.Quote.Text) || len(message.Quote.Text) > MaxTextBytes {
			return fmt.Errorf("quoted message is invalid")
		}
		if message.Quote.Role == QuoteUser && message.Quote.SenderRef.IsZero() {
			return fmt.Errorf("quoted user sender ref is required")
		}
		if message.Quote.Role == QuoteAssistant && !message.Quote.SenderRef.IsZero() {
			return fmt.Errorf("quoted assistant must not have a sender ref")
		}
		if message.Quote.Role == QuoteAssistant && len(message.Quote.Mentions) != 0 {
			return fmt.Errorf("quoted assistant must not carry raw mention bindings")
		}
		if err := validateMentionBindings(message.Quote.Text, message.Quote.Mentions); err != nil {
			return fmt.Errorf("quoted message: %w", err)
		}
	}
	if message.RepliedToBot != (message.Quote != nil && message.Quote.Role == QuoteAssistant) {
		return fmt.Errorf("replied-to-bot marker does not match quote")
	}
	if message.OccurredAt.IsZero() || message.ReceivedAt.IsZero() {
		return fmt.Errorf("timestamps are required")
	}
	return nil
}

func (message IncomingMessage) AgentKey() (identity.TenantID, identity.AccountID, identity.ChatID) {
	return message.TenantID, message.AccountID, message.ChatID
}

func validateIncomingMentions(text string, mentions []IncomingMention) error {
	if len(mentions) > MaxMentions {
		return fmt.Errorf("mentions exceed %d", MaxMentions)
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
		if binding.Bot != binding.TargetLID.IsZero() {
			return fmt.Errorf("mention target is invalid")
		}
		if !utf8.ValidString(binding.DisplayName) || len(binding.DisplayName) > 512 {
			return fmt.Errorf("mention display name is invalid")
		}
	}
	return nil
}

func validateMentionBindings(text string, mentions []MentionBinding) error {
	if len(mentions) > MaxMentions {
		return fmt.Errorf("mentions exceed %d", MaxMentions)
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
			return fmt.Errorf("mention binding is invalid")
		}
	}
	return nil
}
