package observability

import (
	"bytes"
	"strings"
	"testing"
)

func TestCompactHandler(t *testing.T) {
	var buf bytes.Buffer
	logger, _, err := NewLogger(&buf, "info", "compact")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.Info("test message", "key1", "value1", "key2", "value2")
	output := buf.String()

	if !strings.Contains(output, "INF") {
		t.Errorf("output should contain INF level, got: %s", output)
	}
	if !strings.Contains(output, "test message") {
		t.Errorf("output should contain message, got: %s", output)
	}
	if !strings.Contains(output, "key1=value1") {
		t.Errorf("output should contain key1=value1, got: %s", output)
	}
	if !strings.Contains(output, "inst=") {
		t.Errorf("output should contain inst=, got: %s", output)
	}
}

func TestCompactHandlerWithChatName(t *testing.T) {
	var buf bytes.Buffer
	logger, _, err := NewLogger(&buf, "info", "compact")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.Info("message received", "chat_name", "HC (Hobi Coding)", "chatId", "120363429302106476@g.us")
	output := buf.String()

	if !strings.Contains(output, "[HC (Hobi Coding)") {
		t.Errorf("output should contain chat name prefix, got: %s", output)
	}
	if !strings.Contains(output, "message received") {
		t.Errorf("output should contain message, got: %s", output)
	}
	if !strings.Contains(output, "chatId=120363429302106476@g.us") {
		t.Errorf("output should contain chatId attribute, got: %s", output)
	}
}

func TestCompactHandlerLevels(t *testing.T) {
	tests := []struct {
		level    string
		expected string
	}{
		{"debug", "DBG"},
		{"info", "INF"},
		{"warn", "WRN"},
		{"error", "ERR"},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			var buf bytes.Buffer
			logger, _, err := NewLogger(&buf, tt.level, "compact")
			if err != nil {
				t.Fatalf("NewLogger failed: %v", err)
			}

			switch tt.level {
			case "debug":
				logger.Debug("test")
			case "info":
				logger.Info("test")
			case "warn":
				logger.Warn("test")
			case "error":
				logger.Error("test")
			}

			output := buf.String()
			if !strings.Contains(output, tt.expected) {
				t.Errorf("expected level %s in output, got: %s", tt.expected, output)
			}
		})
	}
}

func TestCompactHandlerTruncatesChatName(t *testing.T) {
	var buf bytes.Buffer
	logger, _, err := NewLogger(&buf, "info", "compact")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	longName := "This is a very long chat name that should be truncated"
	logger.Info("test", "chat_name", longName)
	output := buf.String()

	if strings.Contains(output, longName) {
		t.Errorf("long chat name should be truncated, got: %s", output)
	}
	if !strings.Contains(output, "...") {
		t.Errorf("truncated name should contain ..., got: %s", output)
	}
}
