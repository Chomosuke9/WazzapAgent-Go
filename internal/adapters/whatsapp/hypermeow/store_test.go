package hypermeow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenDeviceStoreUsesPrivatePersistentPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "tenant", "whatsapp.db")
	container, err := openDeviceStore(ctx, path, nil)
	if err != nil {
		t.Fatalf("open device store: %v", err)
	}
	if err := container.Close(); err != nil {
		t.Fatalf("close device store: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat device store: %v", err)
	}
	if _, err := openDeviceStore(ctx, "bad\x00path", nil); err == nil {
		t.Fatal("device store accepted a null byte")
	}
	if _, err := openDeviceStore(ctx, "bad#path.db", nil); err == nil {
		t.Fatal("device store accepted a URI-reserved path")
	}
}
