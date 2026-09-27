package hypermeow

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLibraryLoggerForwardsWarningsAndErrorsOnly(t *testing.T) {
	var output bytes.Buffer
	logger := newLibraryLogger(slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))).Sub("Client")
	logger.Warnf("Failed to encrypt %s for %s: %v", "3EB0C4F2A1B2C3D4E5F6", "6281234567890@s.whatsapp.net", "no session")
	logger.Errorf("Error in socket: %d", 1006)
	logger.Infof("connected")
	logger.Debugf("sending node")

	text := output.String()
	for _, want := range []string{"Failed to encrypt <message-id> for <redacted>@s.whatsapp.net: no session", "Error in socket: 1006", "module=whatsmeow/Client"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "connected") || strings.Contains(text, "sending node") {
		t.Fatalf("info or debug output was forwarded: %q", text)
	}
}
