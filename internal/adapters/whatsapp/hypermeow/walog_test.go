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
	logger.Warnf("Failed to encrypt %s for %s: %v", "msg-1", "123@s.whatsapp.net", "no session")
	logger.Errorf("Error in socket: %d", 1006)
	logger.Infof("connected")
	logger.Debugf("sending node")

	text := output.String()
	for _, want := range []string{"Failed to encrypt msg-1 for 123@s.whatsapp.net: no session", "Error in socket: 1006", "module=whatsmeow/Client"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "connected") || strings.Contains(text, "sending node") {
		t.Fatalf("info or debug output was forwarded: %q", text)
	}
}
