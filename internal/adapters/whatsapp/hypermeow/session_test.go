package hypermeow

import (
	"context"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/config"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	"github.com/polymorfa/hypermeow/types/events"
)

func TestSessionFactoryOpensSessionWithoutAgentConfiguration(t *testing.T) {
	settings := config.DefaultSettings()
	settings.DataDir = t.TempDir()
	settings.AgentEnabled = false
	settings.OwnerJID = ""
	settings.ChatAllowlist = nil
	snapshot, err := config.SessionSnapshotWithIdentity(settings, newTestTenantID(t), newTestAccountID(t))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewSessionFactory().OpenSession(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close(context.Background())
	if runtime.HasSession() {
		t.Fatal("new session unexpectedly contains a linked device")
	}
	if runtime.WhatsAppAccountID() != "" {
		t.Fatal("new session unexpectedly has a WhatsApp account identity")
	}
}

func TestEncodeQRDataURLProducesPNG(t *testing.T) {
	dataURL, err := encodeQRDataURL("pairing-payload-test")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("data URL prefix = %q", dataURL[:min(len(dataURL), len(prefix))])
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		t.Fatal(err)
	}
	image, err := png.Decode(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if image.Bounds().Dx() == 0 || image.Bounds().Dy() == 0 {
		t.Fatalf("QR image bounds = %v", image.Bounds())
	}
}

func TestSessionRuntimeDoesNotEnqueueIncomingMessages(t *testing.T) {
	runtime := &SessionRuntime{events: make(chan any, 2)}
	runtime.handleEvent(&events.Message{})
	if len(runtime.events) != 0 {
		t.Fatal("session-only runtime retained an inbound message event")
	}
	runtime.handleEvent(&events.Disconnected{})
	if len(runtime.events) != 1 {
		t.Fatal("session-only runtime dropped a connection status event")
	}
}

func newTestTenantID(t *testing.T) identity.TenantID {
	t.Helper()
	id, err := identity.NewTenantID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newTestAccountID(t *testing.T) identity.AccountID {
	t.Helper()
	id, err := identity.NewAccountID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
