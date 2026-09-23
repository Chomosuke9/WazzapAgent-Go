package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	// ErrDataRootLocked indicates that another process owns the data root.
	ErrDataRootLocked = errors.New("data root is already in use")
	// ErrUnsupportedPlatform indicates that no default desktop data location is
	// available. Android callers must supply the internal app-storage path.
	ErrUnsupportedPlatform = errors.New("unsupported platform data root")
)

const lockFileName = ".wazzapagent.lock"

// DataRootLease holds the OS lock for the canonical data root. Closing it is
// idempotent and releases ownership only after all callers stop using storage.
type DataRootLease struct {
	root string
	file *os.File
	once sync.Once
	err  error
}

// Root returns the canonical path protected by this lease.
func (lease *DataRootLease) Root() string {
	if lease == nil {
		return ""
	}
	return lease.root
}

// Close releases the lock and closes its descriptor. Repeated Close calls
// return the result of the first release attempt.
func (lease *DataRootLease) Close() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		if lease.file == nil {
			return
		}
		unlockErr := unlockFile(lease.file)
		closeErr := lease.file.Close()
		lease.err = errors.Join(unlockErr, closeErr)
	})
	return lease.err
}

// CanonicalDataRoot creates (if necessary) and resolves a data root. Resolving
// symlinks before locking makes aliases such as ./data and a symlink to data
// contend on the same lock file.
func CanonicalDataRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, '\x00') {
		return "", errors.New("data root path is empty or contains NUL")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve data root: %w", err)
	}
	abs = filepath.Clean(abs)
	if filepath.Dir(abs) == abs {
		return "", errors.New("filesystem root is not an application data root")
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", fmt.Errorf("create data root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve data root symlinks: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", fmt.Errorf("normalize data root: %w", err)
	}
	return filepath.Clean(canonical), nil
}

// AcquireDataRootLease claims the canonical root before identity or databases
// are opened. A second process receives ErrDataRootLocked immediately.
func AcquireDataRootLease(ctx context.Context, path string) (*DataRootLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	root, err := CanonicalDataRoot(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, lockFileName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open data root lock: %w", err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errLockContention) {
			return nil, fmt.Errorf("%w: %s", ErrDataRootLocked, root)
		}
		return nil, fmt.Errorf("lock data root: %w", err)
	}
	return &DataRootLease{root: root, file: file}, nil
}

// DefaultDataRoot returns the documented per-platform application location.
// Android supplies its internal storage directory from the native host.
func DefaultDataRoot() (string, error) {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve Windows home: %w", err)
			}
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, "WazzapAgent"), nil
	case "linux":
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve Linux home: %w", err)
			}
			base = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(base, "wazzapagent"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve macOS home: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "WazzapAgent"), nil
	default:
		return "", ErrUnsupportedPlatform
	}
}
