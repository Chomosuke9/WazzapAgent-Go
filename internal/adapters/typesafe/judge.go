package typesafe

import (
	"cmp"
	"context"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
)

const (
	// Threshold is the yes probability above which a question counts as
	// yes. 0.5 means "more likely yes than no"; every probability is logged
	// so it can be tuned against real chats.
	Threshold = 0.5

	recentMessages   = 8
	maxRecentTextLen = 500
	maxNewTextLen    = 2000
)

// HistoryReader is the part of the history store the judge reads.
type HistoryReader interface {
	ListIfConfigVersion(context.Context, agent.Key, agent.ConfigVersion, agent.HistoryQuery) (agent.HistoryPage, error)
}

// ResponseJudge decides whether a group message that matched no mention,
// reply or name trigger is worth a response. TypeSafe answers narrow yes/no
// questions about the message and the chat's recent transcript; the rule
// that combines them lives in Decide.
type ResponseJudge struct {
	client        *Client
	history       HistoryReader
	assistantName string
	logger        *slog.Logger
}

func NewResponseJudge(client *Client, history HistoryReader, assistantName string, logger *slog.Logger) *ResponseJudge {
	if logger == nil {
		logger = slog.Default()
	}
	return &ResponseJudge{client: client, history: history, assistantName: strings.TrimSpace(assistantName), logger: logger}
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

// The fixed questions. Each asks one narrow thing; Decide combines them.
var fixedQuestions = map[string]Question{
	"followup": {
		Type: "noul",
		Instructions: "Does `new_message` answer or continue something the assistant said in `recent_messages` " +
			"(oldest first)? For example, the assistant asked a question and the sender is answering it, or the " +
			"sender reacts to the assistant's last reply with a further request.",
		Criteria: NoulCriteria{
			True:  "The message is a response to, or a continuation of, the assistant's recent messages.",
			False: "The message does not follow on from anything the assistant said recently.",
		},
	},
	"addressed": {
		Type: "noul",
		Instructions: "Does `new_message` speak to the assistant directly, by role or nickname rather than by its name " +
			"`assistant.name`? For example \"bot, ...\", \"min, ...\", \"AI, ...\", or a request in the second person " +
			"that only a bot could carry out.",
		Criteria: NoulCriteria{
			True:  "The sender is talking to the assistant.",
			False: "The sender is not talking to the assistant.",
		},
	},
	"to_someone_else": {
		Type: "noul",
		Instructions: "Is `new_message` aimed at a specific person other than the assistant? For example it names or " +
			"mentions another member, or quotes another member's message and answers them.",
		Criteria: NoulCriteria{
			True:  "The message is for a particular other person.",
			False: "The message is not for a particular other person.",
		},
	},
	"chatter": {
		Type: "noul",
		Instructions: "Is `new_message` only a reaction or filler that needs no answer, such as laughter (\"wkwk\", " +
			"\"haha\"), \"ok\", \"sip\", thanks, an emoji, or a sticker?",
		Criteria: NoulCriteria{
			True:  "The message is only a reaction or filler.",
			False: "The message has content that could call for an answer.",
		},
	},
}

func ruleQuestion(rule string) Question {
	return Question{
		Type: "noul",
		Instructions: map[string]string{
			"task": "Does `new_message` match this rule set by the group admins?",
			"rule": rule,
		},
		Criteria: NoulCriteria{
			True:  "The message is a case the rule describes.",
			False: "The message is not a case the rule describes.",
		},
	}
}

// Probabilities are the yes probabilities TypeSafe returned; Rules is in the
// order of the chat's smart rules.
type Probabilities struct {
	FollowUp, Addressed, ToSomeoneElse, Chatter float64
	Rules                                       []float64
}

// Decide is the response rule: a matched admin rule always wakes the Agent
// (a scam link needs handling whoever it is sent to); otherwise the message
// must follow up on or speak to the assistant, and be neither aimed at
// someone else nor mere chatter.
func Decide(p Probabilities) (bool, string) {
	for index, probability := range p.Rules {
		if probability > Threshold {
			return true, "rule " + strconv.Itoa(index+1)
		}
	}
	switch {
	case p.FollowUp <= Threshold && p.Addressed <= Threshold:
		return false, "not for the assistant"
	case p.ToSomeoneElse > Threshold:
		return false, "aimed at someone else"
	case p.Chatter > Threshold:
		return false, "chatter"
	case p.FollowUp > Threshold:
		return true, "follow-up"
	}
	return true, "addressed"
}

// ShouldRespond reports whether message is worth a response.
func (judge *ResponseJudge) ShouldRespond(ctx context.Context, message conversation.IncomingMessage, snapshot agent.ConfigSnapshot) (bool, error) {
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
	questions := maps.Clone(fixedQuestions)
	rules := snapshot.Triggers.SmartRuleList()
	for index, rule := range rules {
		questions["rule_"+strconv.Itoa(index+1)] = ruleQuestion(rule)
	}
	result, err := judge.client.SystemOne(ctx, state, questions)
	if err != nil {
		judge.logger.Warn("smart trigger judgment failed", "chat", message.ChatID.String(), "error", err)
		return false, err
	}
	answers := Probabilities{
		FollowUp: result.Answers["followup"].Noul, Addressed: result.Answers["addressed"].Noul,
		ToSomeoneElse: result.Answers["to_someone_else"].Noul, Chatter: result.Answers["chatter"].Noul,
	}
	for index := range rules {
		answers.Rules = append(answers.Rules, result.Answers["rule_"+strconv.Itoa(index+1)].Noul)
	}
	respond, reason := Decide(answers)
	judge.logger.Info("smart trigger judged", "chat", message.ChatID.String(), "respond", respond, "reason", reason,
		"followup", answers.FollowUp, "addressed", answers.Addressed, "to_someone_else", answers.ToSomeoneElse,
		"chatter", answers.Chatter, "rules", answers.Rules, "input_tokens", result.Usage.InputTokens)
	return respond, nil
}

func (judge *ResponseJudge) recent(entries []agent.HistoryEntry, current conversation.IncomingMessage) []chatLine {
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

func (judge *ResponseJudge) newMessage(message conversation.IncomingMessage) newMessage {
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

func (judge *ResponseJudge) speaker(entry agent.HistoryEntry) string {
	if entry.Role == agent.HistoryAssistant {
		return judge.assistantLabel()
	}
	if entry.Sender != nil {
		return nonEmpty(entry.Sender.DisplayName, "a group member")
	}
	return "a group member"
}

func (judge *ResponseJudge) assistantLabel() string {
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
