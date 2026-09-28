package typesafe

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
)

const (
	// AddressThreshold is the yes probability above which a message counts as
	// meant for the assistant. 0.5 means "more likely yes than no"; the
	// judged probability is logged so it can be tuned against real chats.
	AddressThreshold = 0.5

	recentMessages   = 8
	maxRecentTextLen = 500
	maxNewTextLen    = 2000
)

// HistoryReader is the part of the history store the judge reads.
type HistoryReader interface {
	ListIfConfigVersion(context.Context, agent.Key, agent.ConfigVersion, agent.HistoryQuery) (agent.HistoryPage, error)
}

// AddressJudge decides whether a group message that matched no mention,
// reply or name trigger is still meant for the assistant, from the message
// and the chat's recent transcript.
type AddressJudge struct {
	client        *Client
	history       HistoryReader
	assistantName string
	logger        *slog.Logger
}

func NewAddressJudge(client *Client, history HistoryReader, assistantName string, logger *slog.Logger) *AddressJudge {
	if logger == nil {
		logger = slog.Default()
	}
	return &AddressJudge{client: client, history: history, assistantName: strings.TrimSpace(assistantName), logger: logger}
}

type chatLine struct {
	From string `json:"from"`
	Text string `json:"text"`
}

type newMessage struct {
	From     string    `json:"from"`
	Text     string    `json:"text"`
	Quoting  *chatLine `json:"quoting,omitempty"`
	Mentions int       `json:"mentions_other_people,omitempty"`
}

var addressedQuestion = Question{
	Type: "noul",
	Instructions: "Is `new_message` meant for the assistant described in `assistant`, so that the assistant should answer it? " +
		"Use `recent_messages` (oldest first) for context. Count it as meant for the assistant when the sender continues a " +
		"conversation with the assistant, answers something the assistant just asked, or asks the bot or AI for something, " +
		"even without using its name.",
	Criteria: NoulCriteria{
		True:  "The sender is talking to the assistant or expects the assistant to respond.",
		False: "The sender is talking to other people in the group, or the message needs nothing from the assistant.",
	},
}

// AddressedToAssistant reports whether message is meant for the assistant.
func (judge *AddressJudge) AddressedToAssistant(ctx context.Context, message conversation.IncomingMessage, snapshot agent.ConfigSnapshot) (bool, error) {
	key := agent.Key{TenantID: message.TenantID, AccountID: message.AccountID, ChatID: message.ChatID}
	page, err := judge.history.ListIfConfigVersion(ctx, key, snapshot.Version, agent.HistoryQuery{Limit: recentMessages + 1})
	if err != nil {
		return false, err
	}
	state := map[string]any{
		"assistant":       map[string]string{"name": judge.assistantName, "role": "AI assistant (a bot) taking part in this WhatsApp group"},
		"recent_messages": judge.recent(page.Entries, message),
		"new_message":     judge.newMessage(message),
	}
	result, err := judge.client.SystemOne(ctx, state, map[string]Question{"addressed": addressedQuestion})
	if err != nil {
		judge.logger.Warn("smart trigger judgment failed", "chat", message.ChatID.String(), "error", err)
		return false, err
	}
	probability := result.Answers["addressed"].Noul
	addressed := probability > AddressThreshold
	judge.logger.Info("smart trigger judged", "chat", message.ChatID.String(), "probability", probability,
		"addressed", addressed, "input_tokens", result.Usage.InputTokens)
	return addressed, nil
}

func (judge *AddressJudge) recent(entries []agent.HistoryEntry, current conversation.IncomingMessage) []chatLine {
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b agent.HistoryEntry) int { return cmp.Compare(a.Sequence, b.Sequence) })
	lines := make([]chatLine, 0, len(entries))
	for _, entry := range entries {
		if entry.MessageID == current.ID || entry.Role == agent.HistorySystem {
			continue
		}
		lines = append(lines, chatLine{From: judge.speaker(entry), Text: truncate(historyText(entry), maxRecentTextLen)})
	}
	if len(lines) > recentMessages {
		lines = lines[len(lines)-recentMessages:]
	}
	return lines
}

func (judge *AddressJudge) newMessage(message conversation.IncomingMessage) newMessage {
	result := newMessage{
		From:     nonEmpty(message.SenderName, "a group member"),
		Text:     truncate(conversation.AuthoredText(message.Text), maxNewTextLen),
		Mentions: len(message.Mentions),
	}
	if message.Quote != nil {
		from := "another group member"
		if message.Quote.Role == conversation.QuoteAssistant {
			from = judge.assistantLabel()
		}
		result.Quoting = &chatLine{From: from, Text: truncate(message.Quote.Text, maxRecentTextLen)}
	}
	return result
}

func (judge *AddressJudge) speaker(entry agent.HistoryEntry) string {
	if entry.Role == agent.HistoryAssistant {
		return judge.assistantLabel()
	}
	if entry.Sender != nil {
		return nonEmpty(entry.Sender.DisplayName, "a group member")
	}
	return "a group member"
}

func (judge *AddressJudge) assistantLabel() string {
	return nonEmpty(judge.assistantName, "assistant") + " (the assistant)"
}

func historyText(entry agent.HistoryEntry) string {
	parts := make([]string, 0, len(entry.Content))
	for _, part := range entry.Content {
		if text, ok := part.(agent.TextPart); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
