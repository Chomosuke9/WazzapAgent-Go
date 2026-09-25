package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	manifestName     = "backup-manifest.json"
	manifestVersion  = 1
	maxManifestSize  = 16 * 1024 * 1024
	maxManifestFiles = 100_000
)

type File struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Files     []File    `json:"files"`
}

// Create makes an offline, checksum-addressed copy of the complete data root.
// The caller must stop the runtime first so both independent SQLite databases
// represent the same operational point in time.
func Create(ctx context.Context, sourceRoot, destinationParent string, now time.Time) (string, error) {
	source, err := checkedRoot(sourceRoot, true)
	if err != nil {
		return "", fmt.Errorf("backup source: %w", err)
	}
	parent, err := checkedRoot(destinationParent, false)
	if err != nil {
		return "", fmt.Errorf("backup destination: %w", err)
	}
	if now.IsZero() {
		return "", fmt.Errorf("backup time is required")
	}
	if inside(source, parent) {
		return "", fmt.Errorf("backup destination must not be inside the source data directory")
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create backup parent: %w", err)
	}
	parent, err = checkedRoot(parent, true)
	if err != nil {
		return "", fmt.Errorf("resolve backup destination: %w", err)
	}
	if inside(source, parent) {
		return "", fmt.Errorf("backup destination must not be inside the source data directory")
	}
	suffix, err := randomSuffix()
	if err != nil {
		return "", err
	}
	name := "wazzapagent-backup-" + now.UTC().Format("20060102T150405Z") + "-" + suffix
	finalPath := filepath.Join(parent, name)
	temporaryPath := finalPath + ".partial"
	if err := os.Mkdir(temporaryPath, 0o700); err != nil {
		return "", fmt.Errorf("create temporary backup: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporaryPath)
		}
	}()
	files, err := copyTree(ctx, source, temporaryPath)
	if err != nil {
		return "", err
	}
	if len(files) == 0 || len(files) > maxManifestFiles {
		return "", fmt.Errorf("backup source file count is outside the supported range")
	}
	manifest := Manifest{Version: manifestVersion, CreatedAt: now.UTC(), Files: files}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode backup manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxManifestSize {
		return "", fmt.Errorf("backup manifest exceeds the supported size")
	}
	if err := writeSyncedFile(filepath.Join(temporaryPath, manifestName), encoded); err != nil {
		return "", fmt.Errorf("write backup manifest: %w", err)
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return "", fmt.Errorf("commit backup: %w", err)
	}
	committed = true
	return finalPath, nil
}

func Verify(ctx context.Context, backupRoot string) (Manifest, error) {
	root, err := checkedRoot(backupRoot, true)
	if err != nil {
		return Manifest{}, fmt.Errorf("verify backup: %w", err)
	}
	encoded, err := os.ReadFile(filepath.Join(root, manifestName))
	if err != nil {
		return Manifest{}, fmt.Errorf("read backup manifest: %w", err)
	}
	if len(encoded) > maxManifestSize {
		return Manifest{}, fmt.Errorf("backup manifest is too large")
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("backup manifest has trailing data")
	}
	if manifest.Version != manifestVersion || manifest.CreatedAt.IsZero() ||
		len(manifest.Files) == 0 || len(manifest.Files) > maxManifestFiles {
		return Manifest{}, fmt.Errorf("backup manifest metadata is invalid")
	}
	expected := make(map[string]File, len(manifest.Files))
	for _, file := range manifest.Files {
		relative, err := checkedRelative(file.Path)
		if err != nil || file.Bytes < 0 || len(file.SHA256) != sha256.Size*2 {
			return Manifest{}, fmt.Errorf("backup manifest file is invalid")
		}
		if _, duplicate := expected[relative]; duplicate {
			return Manifest{}, fmt.Errorf("backup manifest contains a duplicate path")
		}
		expected[relative] = file
	}
	seen := make(map[string]struct{}, len(expected))
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == manifestName {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup contains a symbolic link: %s", relative)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("backup contains a non-regular file: %s", relative)
		}
		wanted, exists := expected[relative]
		if !exists {
			return fmt.Errorf("backup contains an unmanifested file: %s", relative)
		}
		size, digest, err := hashFile(ctx, path)
		if err != nil {
			return err
		}
		if size != wanted.Bytes || digest != wanted.SHA256 {
			return fmt.Errorf("backup checksum mismatch: %s", relative)
		}
		seen[relative] = struct{}{}
		return nil
	})
	if err != nil {
		return Manifest{}, fmt.Errorf("verify backup contents: %w", err)
	}
	if len(seen) != len(expected) {
		return Manifest{}, fmt.Errorf("backup is missing one or more manifested files")
	}
	return manifest, nil
}

// Restore materializes a verified backup into a new destination. It never
// overwrites an existing path; swapping it into production remains explicit.
func Restore(ctx context.Context, backupRoot, destinationRoot string) error {
	source, err := checkedRoot(backupRoot, true)
	if err != nil {
		return fmt.Errorf("restore source: %w", err)
	}
	destination, err := checkedRoot(destinationRoot, false)
	if err != nil {
		return fmt.Errorf("restore destination: %w", err)
	}
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("restore destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect restore destination: %w", err)
	}
	if source == destination || inside(source, destination) {
		return fmt.Errorf("restore destination must be separate from the backup")
	}
	manifest, err := Verify(ctx, source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create restore parent: %w", err)
	}
	realParent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("resolve restore parent: %w", err)
	}
	destination = filepath.Join(realParent, filepath.Base(destination))
	if source == destination || inside(source, destination) {
		return fmt.Errorf("restore destination must be separate from the backup")
	}
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("restore destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect resolved restore destination: %w", err)
	}
	temporary := destination + ".partial"
	if err := os.Mkdir(temporary, 0o700); err != nil {
		return fmt.Errorf("create temporary restore: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, file := range manifest.Files {
		if err := contextErr(ctx); err != nil {
			return err
		}
		relative, _ := checkedRelative(file.Path)
		if _, _, err := copyFile(ctx, filepath.Join(source, filepath.FromSlash(relative)), filepath.Join(temporary, filepath.FromSlash(relative))); err != nil {
			return fmt.Errorf("restore file %s: %w", relative, err)
		}
	}
	if err := os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("commit restore: %w", err)
	}
	committed = true
	return nil
}

func copyTree(ctx context.Context, source, destination string) ([]File, error) {
	files := make([]File, 0)
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		if strings.EqualFold(filepath.ToSlash(relative), manifestName) {
			return fmt.Errorf("source contains reserved backup manifest path: %s", relative)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source contains a symbolic link: %s", relative)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("source contains a non-regular file: %s", relative)
		}
		size, digest, err := copyFile(ctx, path, target)
		if err != nil {
			return err
		}
		files = append(files, File{Path: filepath.ToSlash(relative), Bytes: size, SHA256: digest})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

func copyFile(ctx context.Context, source, destination string) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return 0, "", err
	}
	input, err := os.Open(source)
	if err != nil {
		return 0, "", err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), &contextReader{ctx: ctx, reader: input})
	var syncErr error
	if copyErr == nil {
		syncErr = output.Sync()
	}
	closeErr := output.Close()
	if copyErr != nil {
		return 0, "", copyErr
	}
	if syncErr != nil {
		return 0, "", syncErr
	}
	if closeErr != nil {
		return 0, "", closeErr
	}
	return written, hex.EncodeToString(hash.Sum(nil)), nil
}

func writeSyncedFile(path string, data []byte) error {
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := output.Write(data)
	var syncErr error
	if writeErr == nil {
		syncErr = output.Sync()
	}
	closeErr := output.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func hashFile(ctx context.Context, path string) (int64, string, error) {
	input, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer input.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, &contextReader{ctx: ctx, reader: input})
	if err != nil {
		return 0, "", err
	}
	return written, hex.EncodeToString(hash.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := contextErr(reader.ctx); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func checkedRoot(path string, mustExist bool) (string, error) {
	if strings.ContainsRune(path, '\x00') || strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is invalid")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if filepath.Dir(absolute) == absolute {
		return "", fmt.Errorf("filesystem root is not allowed")
	}
	if mustExist {
		info, err := os.Stat(absolute)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("path is not a directory")
		}
	}
	resolved, err := resolveExistingPrefix(absolute)
	if err != nil {
		return "", err
	}
	if filepath.Dir(resolved) == resolved {
		return "", fmt.Errorf("filesystem root is not allowed")
	}
	return resolved, nil
}

// resolveExistingPrefix canonicalizes the deepest existing ancestor before
// appending any missing path components. Besides resolving symlinks, this
// makes comparisons reliable on Windows where one directory can have both a
// long name and an 8.3 short name.
func resolveExistingPrefix(path string) (string, error) {
	missing := make([]string, 0)
	prefix := path
	for {
		_, err := os.Lstat(prefix)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return "", err
		}
		missing = append(missing, filepath.Base(prefix))
		prefix = parent
	}

	resolved, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return "", err
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	return filepath.Clean(resolved), nil
}

func checkedRelative(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("relative path is invalid")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("relative path escapes the root")
	}
	return filepath.ToSlash(clean), nil
}

func inside(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func randomSuffix() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate backup suffix: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func contextErr(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
