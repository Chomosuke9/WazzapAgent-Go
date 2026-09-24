package platform

import (
	"errors"
	"path/filepath"
	"strings"
)

// ResolvePathsInSandbox places all persistent state beneath a native host's
// private files directory. Android supplies this directory via getFilesDir.
func ResolvePathsInSandbox(storageDir string) (Paths, error) {
	if strings.TrimSpace(storageDir) == "" || !filepath.IsAbs(storageDir) {
		return Paths{}, errors.New("absolute private storage directory is required")
	}
	base := filepath.Join(filepath.Clean(storageDir), "wazzapagent")
	configDir := filepath.Join(base, "config")
	dataRoot := filepath.Join(base, "data")
	return Paths{
		ConfigDir:         configDir,
		BootstrapFile:     BootstrapPath(configDir),
		DefaultDataRoot:   dataRoot,
		EffectiveDataRoot: dataRoot,
	}, nil
}
