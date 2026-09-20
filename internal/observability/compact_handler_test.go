package observability

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
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
	if !strings.Contains(output, "INF [system            ] test message") {
		t.Errorf("output should contain the padded system context, got: %s", output)
	}
	if strings.Contains(output, "inst=") || strings.Contains(output, "instance_id") {
		t.Errorf("compact output should omit the instance ID, got: %s", output)
	}
}

func TestCompactHandlerWithChatName(t *testing.T) {
	var buf bytes.Buffer
	logger, _, err := NewLogger(&buf, "info", "compact")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.With("chat_name", "HC (Hobi Coding)").Info("message received", "chatId", "120363429302106476@g.us")
	output := buf.String()

	if !strings.Contains(output, "[HC (Hobi Coding)  ]") {
		t.Errorf("output should contain chat name prefix, got: %s", output)
	}
	if !strings.Contains(output, "message received") {
		t.Errorf("output should contain message, got: %s", output)
	}
	if strings.Contains(output, "120363429302106476@g.us") {
		t.Errorf("output should omit chatId when chat name is available, got: %s", output)
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

func TestCompactHandlerUsesChatIDWhenNameIsUnavailable(t *testing.T) {
	var buf bytes.Buffer
	logger, _, err := NewLogger(&buf, "info", "compact")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	logger.Info("downloaded", "chat_id", "120363427296079434@g.us")
	output := buf.String()
	if !strings.Contains(output, "INF [120363427296079...] downloaded") {
		t.Fatalf("chat ID context = %q", output)
	}
	if strings.Contains(output, "chat_id=") {
		t.Fatalf("chat ID used as context should not be duplicated: %q", output)
	}
}

func TestCompactHandlerPrintsFullNativeError(t *testing.T) {
	var buf bytes.Buffer
	logger, _, err := NewLogger(&buf, "info", "compact")
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}

	native := detailedTestError{summary: "socket closed", detail: "socket closed\nnative stack frame"}
	wrapped := fmt.Errorf("send WhatsApp media: %w", native)
	logger.Error("delivery failed", "error", wrapped)
	output := buf.String()
	if !strings.Contains(output, "error=send WhatsApp media: socket closed") || !strings.Contains(output, "native stack frame") {
		t.Fatalf("full error detail was not printed: %q", output)
	}
	if !errors.Is(wrapped, native) {
		t.Fatal("test error chain is invalid")
	}
}

func TestCompactHandlerAddsColorWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	handler := newCompactHandler(&buf, nil, true)
	logger := slog.New(handler)
	logger.Info("colored message")
	output := buf.String()
	for _, color := range []string{ansiDim, ansiGreen, ansiMagenta, ansiReset} {
		if !strings.Contains(output, color) {
			t.Fatalf("compact color output omitted %q: %q", color, output)
		}
	}
}

type detailedTestError struct {
	summary string
	detail  string
}

func (err detailedTestError) Error() string { return err.summary }

func (err detailedTestError) Format(state fmt.State, verb rune) {
	if verb == 'v' && state.Flag('+') {
		_, _ = fmt.Fprint(state, err.detail)
		return
	}
	_, _ = fmt.Fprint(state, err.summary)
}
