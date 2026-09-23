package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	bootstrapFilename = "bootstrap.json"
	maxBootstrapBytes = 4 * 1024
	bootstrapSchema   = 1
)

var ErrBootstrapCorrupt = errors.New("bootstrap pointer is corrupt")

type bootstrapDocument struct {
	SchemaVersion uint32 `json:"schema_version"`
	DataRoot      string `json:"data_root"`
}

// BootstrapPath returns the fixed path of the movable data-root pointer.
func BootstrapPath(configDir string) string { return filepath.Join(configDir, bootstrapFilename) }

// ReadBootstrapDataRoot returns the configured root, or an empty string when
// this is the first launch and no pointer exists.
func ReadBootstrapDataRoot(configDir string) (string, error) {
	root, found, err := LoadBootstrap(configDir)
	if err != nil || !found {
		return root, err
	}
	return root, nil
}

// WriteBootstrapDataRoot is the descriptive alias used by platform callers.
func WriteBootstrapDataRoot(configDir, dataRoot string) error {
	return SaveBootstrap(configDir, dataRoot)
}

// LoadBootstrap returns the configured data root. A missing pointer is a
// first-launch state and is returned as ("", false, nil); malformed pointers
// stop startup so the user can recover instead of silently opening another DB.
func LoadBootstrap(configDir string) (string, bool, error) {
	if strings.TrimSpace(configDir) == "" {
		return "", false, errors.New("bootstrap config directory is required")
	}
	data, err := os.ReadFile(BootstrapPath(configDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read bootstrap pointer: %w", err)
	}
	if len(data) > maxBootstrapBytes {
		return "", false, fmt.Errorf("%w: file exceeds %d bytes", ErrBootstrapCorrupt, maxBootstrapBytes)
	}
	var document bootstrapDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || ensureBootstrapEOF(decoder) != nil || document.SchemaVersion != bootstrapSchema {
		return "", false, ErrBootstrapCorrupt
	}
	root, err := CanonicalDataRoot(document.DataRoot)
	if err != nil {
		return "", false, fmt.Errorf("%w: invalid data root", ErrBootstrapCorrupt)
	}
	return root, true, nil
}

func ensureBootstrapEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON")
	}
	return err
}

// SaveBootstrap atomically updates the movable data-root pointer. The
// destination is canonicalized and created before the pointer is replaced.
func SaveBootstrap(configDir, dataRoot string) error {
	if strings.TrimSpace(configDir) == "" {
		return errors.New("bootstrap config directory is required")
	}
	root, err := CanonicalDataRoot(dataRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("create bootstrap directory: %w", err)
	}
	document, err := json.Marshal(bootstrapDocument{SchemaVersion: bootstrapSchema, DataRoot: root})
	if err != nil {
		return err
	}
	document = append(document, '\n')
	temporary, err := os.CreateTemp(configDir, ".bootstrap-*.tmp")
	if err != nil {
		return fmt.Errorf("create bootstrap temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(document); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, BootstrapPath(configDir)); err != nil {
		return fmt.Errorf("replace bootstrap pointer: %w", err)
	}
	return nil
}
