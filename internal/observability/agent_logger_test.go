package observability

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
)

func TestAgentLoggerWritesLifecycleMilestones(t *testing.T) {
	var output bytes.Buffer
	base, _, err := NewLogger(&output, "info", "compact")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	logger := NewAgentLogger(base)
	chatID, _ := identity.NewChatID()
	providerID, _ := identity.ParseProviderID("openai-compatible")
	message := conversation.IncomingMessage{
		ChatID: chatID, ChatKind: conversation.ChatGroup, MentionsBot: true, SenderName: "Budi",
	}
	model := agent.ModelConfig{ProviderID: providerID, Model: "test-model"}
	result := agent.ModelResult{Text: "hello"}

	logger.ObserveAgentTriggered(message, 2, "Tim")
	logger.ObserveAgentInvokeStarted(agent.Key{ChatID: chatID}, model, "Tim")
	logger.ObserveAgentInvokeFinished(agent.Key{ChatID: chatID}, model, "Tim", 1500*time.Millisecond, result, nil)
	logger.ObserveAgentSucceeded(message, agent.InvokeResult{Text: result.Text}, 2*time.Second, "Tim")

	logged := output.String()
	for _, expected := range []string{
		"agent triggered trigger=group_mention batch=2 sender=Budi",
		"agent invoke started provider=openai-compatible model=test-model",
		"agent invoke finished provider=openai-compatible model=test-model elapsed=1500ms response_bytes=5 effects=0",
		"message sent; agent invoke succeeded elapsed=2000ms response_bytes=5",
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("lifecycle log omitted %q:\n%s", expected, logged)
		}
	}
	if lines := strings.Count(strings.TrimSpace(logged), "\n") + 1; lines != 4 {
		t.Fatalf("lifecycle log lines = %d, want 4:\n%s", lines, logged)
	}
	if count := strings.Count(logged, "[Tim               ]"); count != 4 {
		t.Fatalf("chat name prefix count = %d, want 4:\n%s", count, logged)
	}
	if strings.Contains(logged, chatID.String()) {
		t.Fatalf("chat ID should not appear in compact lifecycle logs:\n%s", logged)
	}
}

func TestAgentLoggerUsesPrivatePushName(t *testing.T) {
	var output bytes.Buffer
	base, _, err := NewLogger(&output, "info", "compact")
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}
	chatID, _ := identity.NewChatID()
	message := conversation.IncomingMessage{ChatID: chatID, ChatKind: conversation.ChatDirect, SenderName: "Andi"}
	NewAgentLogger(base).ObserveAgentTriggered(message, 1, message.SenderName)
	if !strings.Contains(output.String(), "[Andi              ] agent triggered") {
		t.Fatalf("private chat prefix = %q", output.String())
	}
	if strings.Contains(output.String(), chatID.String()) {
		t.Fatalf("private chat ID should not appear in compact log: %q", output.String())
	}
}
