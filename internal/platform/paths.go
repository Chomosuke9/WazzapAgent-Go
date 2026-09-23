package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Paths describes the fixed desktop configuration location and the data root
// selected by its bootstrap pointer. The paths are absolute, but the data
// root is canonicalized when a DataRootLease is acquired.
type Paths struct {
	ConfigDir         string
	BootstrapFile     string
	DefaultDataRoot   string
	EffectiveDataRoot string
}

// DefaultConfigDir returns the fixed per-platform directory that contains
// bootstrap.json. It is intentionally separate from the movable data root.
func DefaultConfigDir() (string, error) {
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
		base = os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve Linux home: %w", err)
			}
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "wazzapagent"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve macOS home: %w", err)
		}
		return filepath.Join(home, "Library", "Preferences", "WazzapAgent"), nil
	default:
		return "", ErrUnsupportedPlatform
	}
}

// ResolvePaths resolves the GUI's fixed config directory, bootstrap pointer,
// and default/effective data root without opening any stores.
func ResolvePaths() (Paths, error) {
	configDir, err := DefaultConfigDir()
	if err != nil {
		return Paths{}, err
	}
	defaultRoot, err := DefaultDataRoot()
	if err != nil {
		return Paths{}, err
	}
	effectiveRoot, err := ReadBootstrapDataRoot(configDir)
	if err != nil {
		return Paths{}, err
	}
	if effectiveRoot == "" {
		effectiveRoot = defaultRoot
	}
	return Paths{
		ConfigDir:         configDir,
		BootstrapFile:     BootstrapPath(configDir),
		DefaultDataRoot:   defaultRoot,
		EffectiveDataRoot: effectiveRoot,
	}, nil
}
