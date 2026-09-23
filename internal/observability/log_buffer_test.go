package observability

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLogBufferKeepsNewestEntriesAndRedactsSecrets(t *testing.T) {
	buffer := NewLogBuffer(2)
	buffer.Record("info", "first", "")
	buffer.Record("error", "Agent failed", "api_key=do-not-show · code=not_ready")
	buffer.Record("info", "last", "")

	entries := buffer.Entries()
	if len(entries) != 2 || entries[0].Message != "Agent failed" || entries[1].Message != "last" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	if strings.Contains(entries[0].Details, "do-not-show") || !strings.Contains(entries[0].Details, "<redacted>") {
		t.Fatalf("secret was not redacted: %q", entries[0].Details)
	}
}

func TestLogBufferHandlerStoresOnlySafeStructuredAttributes(t *testing.T) {
	var console bytes.Buffer
	consoleLogger := slog.New(slog.NewTextHandler(&console, &slog.HandlerOptions{Level: slog.LevelInfo}))
	buffer := NewLogBuffer(10)
	logger := slog.New(NewMultiHandler(consoleLogger.Handler(), buffer.Handler()))
	logger.Info("agent invoke started", "chat_name", "Study Group", "model", "example/model", "api_key", "hidden", "chat_id", "private-id")
	logger.Debug("debug is filtered from the in-app log")

	entries := buffer.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected one info entry, got %#v", entries)
	}
	if entries[0].Details != "chat_name=Study Group · model=example/model" {
		t.Fatalf("unsafe or unexpected detail fields: %q", entries[0].Details)
	}
	if !strings.Contains(console.String(), "api_key=hidden") {
		t.Fatalf("multi handler did not preserve console output: %q", console.String())
	}
}

func TestLogBufferHandlerWithAttrsAndGroupDoesNotPanic(t *testing.T) {
	buffer := NewLogBuffer(2)
	logger := slog.New(buffer.Handler()).With("provider", "primary").WithGroup("agent")
	if err := logger.Handler().Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "ready", 0)); err != nil {
		t.Fatal(err)
	}
	if got := buffer.Entries()[0].Details; got != "provider=primary" {
		t.Fatalf("unexpected details: %q", got)
	}
}
