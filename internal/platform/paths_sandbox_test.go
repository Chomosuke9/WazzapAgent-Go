package platform

import (
	"path/filepath"
	"testing"
)

func TestResolvePathsInSandbox(t *testing.T) {
	storage := t.TempDir()
	paths, err := ResolvePathsInSandbox(storage)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(storage, "discordagent", "data")
	if paths.EffectiveDataRoot != want || paths.DefaultDataRoot != want || paths.BootstrapFile != filepath.Join(storage, "discordagent", "config", "bootstrap.json") {
		t.Fatalf("unexpected sandbox paths: %+v", paths)
	}
	if _, err := ResolvePathsInSandbox("relative/path"); err == nil {
		t.Fatal("relative storage path accepted")
	}
}
