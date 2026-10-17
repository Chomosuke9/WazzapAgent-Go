package observability

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/conversation"
)

type AgentLogger struct {
	logger *slog.Logger
}

func NewAgentLogger(logger *slog.Logger) *AgentLogger {
	if logger == nil {
		logger = slog.Default()
	}
	return &AgentLogger{logger: logger}
}

func (logger *AgentLogger) ObserveAgentTriggered(message conversation.IncomingMessage, batchSize uint32, chatName string) {
	attrs := []any{
		"chat_id", message.ChatID.String(),
		"trigger", agentTrigger(message),
		"batch", batchSize,
	}
	if name := strings.TrimSpace(chatName); name != "" {
		attrs = append(attrs, "chat_name", name)
	}
	if name := strings.TrimSpace(message.SenderName); name != "" {
		attrs = append(attrs, "sender", name)
	}
	logger.logger.Info("agent triggered", attrs...)
}

func (logger *AgentLogger) ObserveAgentInvokeStarted(key agent.Key, model agent.ModelConfig, chatName string) {
	logger.logger.Info(
		"agent invoke started",
		"chat_id", key.ChatID.String(),
		"chat_name", strings.TrimSpace(chatName),
		"provider", model.ProviderID.String(),
		"model", model.Model,
	)
}

func (logger *AgentLogger) ObserveAgentInvokeFinished(
	key agent.Key,
	model agent.ModelConfig,
	chatName string,
	elapsed time.Duration,
	result agent.ModelResult,
	err error,
) {
	attrs := []any{
		"chat_id", key.ChatID.String(),
		"chat_name", strings.TrimSpace(chatName),
		"provider", model.ProviderID.String(),
		"model", model.Model,
		"elapsed", elapsedMilliseconds(elapsed),
	}
	if err != nil {
		attrs = append(attrs, "error", err)
		logger.logger.Error("agent invoke failed", attrs...)
		return
	}
	attrs = append(attrs, "response_bytes", len(result.Text), "effects", len(result.Effects))
	logger.logger.Info("agent invoke finished", attrs...)
}

func (logger *AgentLogger) ObserveAgentSucceeded(
	message conversation.IncomingMessage,
	result agent.InvokeResult,
	elapsed time.Duration,
	chatName string,
) {
	logger.logger.Info(
		"message sent; agent invoke succeeded",
		"chat_id", message.ChatID.String(),
		"chat_name", strings.TrimSpace(chatName),
		"elapsed", elapsedMilliseconds(elapsed),
		"response_bytes", len(result.Text),
	)
}

func agentTrigger(message conversation.IncomingMessage) string {
	switch {
	case message.ChatKind == conversation.ChatGroup && message.RepliedToBot:
		return "group_reply"
	case message.ChatKind == conversation.ChatGroup && message.MentionsBot:
		return "group_mention"
	default:
		return "direct_message"
	}
}

func elapsedMilliseconds(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	return strconv.FormatInt(elapsed.Round(time.Millisecond).Milliseconds(), 10) + "ms"
}
