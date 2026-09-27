package observability

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
)

var errServerReturned = errors.New("server returned error")

func TestErrorReportShowsEveryLayerOfTheChain(t *testing.T) {
	native := fmt.Errorf("%w %d", errServerReturned, 405)
	err := agent.NewError(agent.ErrorProviderFailure, "send WhatsApp broadcast", native)

	report := ErrorReport(err)
	for _, want := range []string{
		"send WhatsApp broadcast: server returned error 405",
		"- *agent.Error code=provider_failure op=\"send WhatsApp broadcast\"\n",
		"*fmt.wrapError: server returned error 405",
		"*errors.errorString: server returned error",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report is missing %q:\n%s", want, report)
		}
	}
}

func TestErrorReportShowsOnlyWhatEachLayerAdds(t *testing.T) {
	report := ErrorReport(fmt.Errorf("open store: %w", errors.New("disk is full")))
	if !strings.Contains(report, "- *fmt.wrapError: open store\n") || !strings.Contains(report, "- *errors.errorString: disk is full") {
		t.Fatalf("unexpected chain:\n%s", report)
	}
}

func TestErrorReportFollowsJoinedErrors(t *testing.T) {
	report := ErrorReport(errors.Join(errors.New("first cause"), errors.New("second cause")))
	if !strings.Contains(report, "- *errors.errorString: first cause") || !strings.Contains(report, "- *errors.errorString: second cause") {
		t.Fatalf("joined causes are missing:\n%s", report)
	}
}

func TestWarningKeepsTheFullErrorBehindTheShortLine(t *testing.T) {
	buffer := NewLogBuffer(10)
	logger := slog.New(buffer.Handler())
	sendErr := agent.NewError(agent.ErrorProviderFailure, "send WhatsApp broadcast", fmt.Errorf("%w %d", errServerReturned, 405))
	logger.Warn("WhatsApp broadcast send failed", "chat_name", "Study Group", "code", agent.CodeOf(sendErr), "error", sendErr, "api_key", "token=do-not-show")
	logger.Info("an info line has no full details", "error", sendErr)

	entries := buffer.Entries()
	if len(entries) != 2 {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	warning := entries[0]
	if warning.Details != "chat_name=Study Group · code=provider_failure" {
		t.Fatalf("short line changed: %q", warning.Details)
	}
	if !strings.Contains(warning.Full, "*errors.errorString: server returned error") || !strings.Contains(warning.Full, "chat_name=Study Group") {
		t.Fatalf("full details lost the underlying error:\n%s", warning.Full)
	}
	if strings.Contains(warning.Full, "do-not-show") {
		t.Fatalf("secret leaked into full details:\n%s", warning.Full)
	}
	if entries[1].Full != "" {
		t.Fatalf("info entry should not carry full details: %q", entries[1].Full)
	}
	if full, ok := buffer.Full(warning.ID); !ok || full != warning.Full {
		t.Fatalf("Full(%d) = %q, %v", warning.ID, full, ok)
	}
}

func TestFullDetailsHideProviderPayloadsAndAddresses(t *testing.T) {
	buffer := NewLogBuffer(10)
	logger := slog.New(buffer.Handler())
	logger.Warn("WhatsApp stream error", "code", "503", "raw", "<stream:error secret-node/>", "continuation", "passkey-state")
	logger.Warn("WhatsApp library: Failed to encrypt 3EB0C4F2A1B2C3D4E5F6 for 6281234567890@s.whatsapp.net and 12345678901234@lid and 98765432101@hosted.lid in 120363000000000001@g.us")
	logger.Warn("WhatsApp account disconnected; reconnecting")

	entries := buffer.Entries()
	if strings.Contains(entries[0].Full, "secret-node") || strings.Contains(entries[0].Full, "passkey-state") || !strings.Contains(entries[0].Full, "code=503") {
		t.Fatalf("opaque attributes reached full details:\n%s", entries[0].Full)
	}
	for _, hidden := range []string{"6281234567890", "12345678901234", "98765432101", "120363000000000001", "3EB0C4F2A1B2C3D4E5F6"} {
		if strings.Contains(entries[1].Full, hidden) {
			t.Fatalf("%q reached full details:\n%s", hidden, entries[1].Full)
		}
	}
	if !strings.Contains(entries[1].Full, "<redacted>@s.whatsapp.net") || !strings.Contains(entries[1].Full, "<redacted>@g.us") || !strings.Contains(entries[1].Full, "<redacted>@hosted.lid") {
		t.Fatalf("address kinds were lost:\n%s", entries[1].Full)
	}
	if entries[2].Full != "WhatsApp account disconnected; reconnecting" {
		t.Fatalf("a warning without fields has no full details: %q", entries[2].Full)
	}
}

func TestPersistKeepsWarningsAndErrorsAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", ProblemLogFile)
	first := NewLogBuffer(10)
	if err := first.Persist(path); err != nil {
		t.Fatal(err)
	}
	first.Record("INFO", "not kept", "")
	first.RecordError("ERROR", "Could not start Agent", "code=internal", errors.New("model endpoint refused the connection"))

	second := NewLogBuffer(10)
	second.Record("INFO", "this run", "")
	if err := second.Persist(path); err != nil {
		t.Fatal(err)
	}
	entries := second.Entries()
	if len(entries) != 2 || entries[0].Message != "Could not start Agent" || entries[1].Message != "this run" {
		t.Fatalf("unexpected entries after restart: %#v", entries)
	}
	if !strings.Contains(entries[0].Full, "model endpoint refused the connection") {
		t.Fatalf("persisted entry lost its error: %q", entries[0].Full)
	}
}

func TestPersistTrimsTheProblemLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), ProblemLogFile)
	buffer := NewLogBuffer(10)
	if err := buffer.Persist(path); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2*problemJournalKeep+5; index++ {
		buffer.Record("WARN", fmt.Sprintf("warning %d", index), "")
	}
	entries, err := readProblemLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 2*problemJournalKeep || entries[len(entries)-1].Message != fmt.Sprintf("warning %d", 2*problemJournalKeep+4) {
		t.Fatalf("problem log was not trimmed: %d entries, last %q", len(entries), entries[len(entries)-1].Message)
	}
}
