package platform

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCanonicalDataRootUsesPhysicalPath(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create root: %v", err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	canonical, err := CanonicalDataRoot(filepath.Join(alias, "."))
	if err != nil {
		t.Fatalf("canonical root: %v", err)
	}
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("physical root: %v", err)
	}
	if canonical != physical {
		t.Fatalf("canonical root = %q, want %q", canonical, physical)
	}
}

func TestDataRootLeaseContendsAcrossAliasesAndCloseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "root")
	first, err := AcquireDataRootLease(ctx, root)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	defer first.Close()
	alias := filepath.Join(filepath.Dir(root), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := AcquireDataRootLease(ctx, alias); !errors.Is(err, ErrDataRootLocked) {
		t.Fatalf("second lease error = %v, want ErrDataRootLocked", err)
	}
	if first.Root() == root {
		t.Logf("lease root is canonical path %q", first.Root())
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	second, err := AcquireDataRootLease(ctx, alias)
	if err != nil {
		t.Fatalf("acquire after close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second lease: %v", err)
	}
}

func TestDataRootLeaseHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := AcquireDataRootLease(ctx, filepath.Join(t.TempDir(), "root")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquire error = %v", err)
	}
}

func TestBootstrapPointerRoundTripsAtomically(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	root := filepath.Join(t.TempDir(), "data with spaces", "日本語")
	if err := WriteBootstrapDataRoot(configDir, root); err != nil {
		t.Fatalf("write bootstrap pointer: %v", err)
	}
	got, err := ReadBootstrapDataRoot(configDir)
	if err != nil {
		t.Fatalf("read bootstrap pointer: %v", err)
	}
	want, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("resolve expected root: %v", err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("bootstrap root = %q, want %q", got, filepath.Clean(want))
	}
	if runtime.GOOS != "windows" {
		if mode := func() os.FileMode { info, _ := os.Stat(BootstrapPath(configDir)); return info.Mode().Perm() }(); mode != 0o600 {
			t.Fatalf("bootstrap permissions = %o, want 600", mode)
		}
	}
}

func TestBootstrapPointerCorruptDoesNotReset(t *testing.T) {
	configDir := t.TempDir()
	path := BootstrapPath(configDir)
	if err := os.WriteFile(path, []byte(`{"data_root":""}`), 0o600); err != nil {
		t.Fatalf("write corrupt pointer: %v", err)
	}
	if _, err := ReadBootstrapDataRoot(configDir); !errors.Is(err, ErrBootstrapCorrupt) {
		t.Fatalf("corrupt pointer error = %v, want ErrBootstrapCorrupt", err)
	}
}

func TestAcquireLeasePrecedesStorePathUse(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	lease, err := AcquireDataRootLease(context.Background(), root)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	defer lease.Close()
	if _, err := AcquireDataRootLease(context.Background(), root); !errors.Is(err, ErrDataRootLocked) {
		t.Fatalf("second owner error = %v, want ErrDataRootLocked", err)
	}
	if _, err := os.Stat(filepath.Join(lease.Root(), lockFileName)); err != nil {
		t.Fatalf("lease lock file is unavailable: %v", err)
	}
}
