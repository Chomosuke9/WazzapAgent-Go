package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapPointerRoundTripAndAtomicReplacement(t *testing.T) {
	configDir := t.TempDir()
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	if err := SaveBootstrap(configDir, first); err != nil {
		t.Fatalf("save first pointer: %v", err)
	}
	got, found, err := LoadBootstrap(configDir)
	if err != nil || !found {
		t.Fatalf("load first pointer = %q/%t/%v", got, found, err)
	}
	want, err := CanonicalDataRoot(first)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("first root = %q, want %q", got, want)
	}
	if err := SaveBootstrap(configDir, second); err != nil {
		t.Fatalf("save second pointer: %v", err)
	}
	got, found, err = LoadBootstrap(configDir)
	if err != nil || !found {
		t.Fatalf("load second pointer = %q/%t/%v", got, found, err)
	}
	want, err = CanonicalDataRoot(second)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("second root = %q, want %q", got, want)
	}
}

func TestBootstrapMissingAndCorruptPointer(t *testing.T) {
	configDir := t.TempDir()
	if root, found, err := LoadBootstrap(configDir); err != nil || found || root != "" {
		t.Fatalf("missing pointer = %q/%t/%v", root, found, err)
	}
	if err := os.WriteFile(filepath.Join(configDir, bootstrapFilename), []byte(`{"schema_version":99,"data_root":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadBootstrap(configDir); !errors.Is(err, ErrBootstrapCorrupt) {
		t.Fatalf("corrupt pointer error = %v, want ErrBootstrapCorrupt", err)
	}
}
