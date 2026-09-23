package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	_ "modernc.org/sqlite"
)

const (
	settingsPayloadLimit = 1 << 20
	settingsRevision     = uint64(1)
)

//go:embed settings_migrations/*.sql
var settingsMigrationFiles embed.FS

var (
	// ErrSettingsConflict means that the expected revision is no longer current.
	ErrSettingsConflict = errors.New("settings revision conflict")
	// ErrSettingsPayloadTooLarge means a settings snapshot exceeds the bounded
	// storage limit. Keeping this bound prevents accidental unbounded UI input.
	ErrSettingsPayloadTooLarge = errors.New("settings payload exceeds limit")
)

// SettingsSnapshot is the durable settings value returned by Load and Save.
// Values is a copy of the canonical JSON object and can be decoded into the
// typed config.Settings by the application/controller layer.
type SettingsSnapshot struct {
	Revision  uint64
	Values    json.RawMessage
	UpdatedAt time.Time
}

// SettingsStore owns settings.db. It is deliberately separate from Store:
// setup must remain readable while no tenant conversation database exists.
type SettingsStore struct {
	db    *sql.DB
	clock func() time.Time
	mu    sync.RWMutex
}

// OpenSettings opens a settings database and applies only settings migrations.
// The database is created with private directory/file permissions where the OS
// permits them. Runtime secrets remain inside the backend and are never logged.
func OpenSettings(ctx context.Context, path string) (*SettingsStore, error) {
	absolute, err := safeDatabasePath(path)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open settings store", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "create settings store directory", err)
	}
	// Every settings write starts with a RESERVED lock. This prevents two
	// editors from both reading the same revision and one later failing with a
	// low-level "database is locked" error instead of the documented CAS
	// conflict.
	db, err := sql.Open("sqlite", databaseDSN(absolute, defaultBusyTimeoutMS)+"&_txlock=immediate")
	if err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "open settings store", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	closeOnError := func(err error) (*SettingsStore, error) {
		_ = db.Close()
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		return closeOnError(agent.NewError(agent.ErrorStorageFailure, "ping settings store", err))
	}
	if err := migrateSettings(ctx, db); err != nil {
		return closeOnError(err)
	}
	if err := initializeSettings(ctx, db); err != nil {
		return closeOnError(err)
	}
	return &SettingsStore{db: db, clock: time.Now}, nil
}

// SettingsPath returns the settings database path for a canonical data root.
func SettingsPath(dataRoot string) string { return filepath.Join(dataRoot, "settings.db") }

// Load reads the current durable snapshot. Values is always copied so callers
// cannot mutate a value held by the store.
func (store *SettingsStore) Load(ctx context.Context) (SettingsSnapshot, error) {
	if store == nil || store.db == nil {
		return SettingsSnapshot{}, agent.NewError(agent.ErrorStorageFailure, "load settings", errors.New("settings store is closed"))
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var revision uint64
	var raw string
	var updatedMS int64
	if err := store.db.QueryRowContext(ctx, "SELECT revision, values_json, updated_at_ms FROM application_settings WHERE id = 1").Scan(&revision, &raw, &updatedMS); err != nil {
		return SettingsSnapshot{}, agent.NewError(agent.ErrorStorageFailure, "load settings", err)
	}
	values, err := validateSettingsJSON([]byte(raw))
	if err != nil {
		return SettingsSnapshot{}, agent.NewError(agent.ErrorIntegrityFailure, "decode settings", err)
	}
	return SettingsSnapshot{Revision: revision, Values: values, UpdatedAt: time.UnixMilli(updatedMS).UTC()}, nil
}

// LoadInto decodes the current settings into dst, retaining the same bounded
// validation as Load. dst must be a pointer accepted by encoding/json.
func (store *SettingsStore) LoadInto(ctx context.Context, dst any) (SettingsSnapshot, error) {
	snapshot, err := store.Load(ctx)
	if err != nil {
		return SettingsSnapshot{}, err
	}
	if dst == nil {
		return SettingsSnapshot{}, agent.NewError(agent.ErrorInvalidArgument, "decode settings", errors.New("destination is required"))
	}
	if err := json.Unmarshal(snapshot.Values, dst); err != nil {
		return SettingsSnapshot{}, agent.NewError(agent.ErrorIntegrityFailure, "decode settings", err)
	}
	return snapshot, nil
}

// Save replaces the typed JSON object only when expectedRevision matches the
// durable row. A successful write increments revision exactly once.
func (store *SettingsStore) Save(ctx context.Context, expectedRevision uint64, values any) (SettingsSnapshot, error) {
	if store == nil || store.db == nil {
		return SettingsSnapshot{}, agent.NewError(agent.ErrorStorageFailure, "save settings", errors.New("settings store is closed"))
	}
	payload, err := marshalSettings(values)
	if err != nil {
		return SettingsSnapshot{}, agent.NewError(agent.ErrorInvalidArgument, "save settings", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return SettingsSnapshot{}, agent.NewError(agent.ErrorStorageFailure, "begin settings save", err)
	}
	rollback := func(err error) (SettingsSnapshot, error) {
		_ = tx.Rollback()
		return SettingsSnapshot{}, err
	}
	var current uint64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM application_settings WHERE id = 1").Scan(&current); err != nil {
		return rollback(agent.NewError(agent.ErrorStorageFailure, "read settings revision", err))
	}
	if current != expectedRevision {
		return rollback(agent.NewError(agent.ErrorConflict, "save settings", fmt.Errorf("%w: expected %d, current %d", ErrSettingsConflict, expectedRevision, current)))
	}
	if current == math.MaxUint64 {
		return rollback(agent.NewError(agent.ErrorIntegrityFailure, "save settings", errors.New("settings revision exhausted")))
	}
	next := current + 1
	updated := store.clock().UTC()
	result, err := tx.ExecContext(ctx, "UPDATE application_settings SET revision = ?, values_json = ?, updated_at_ms = ? WHERE id = 1 AND revision = ?", next, string(payload), updated.UnixMilli(), current)
	if err != nil {
		return rollback(agent.NewError(agent.ErrorStorageFailure, "write settings", err))
	}
	if affected, err := result.RowsAffected(); err != nil {
		return rollback(agent.NewError(agent.ErrorStorageFailure, "inspect settings write", err))
	} else if affected != 1 {
		return rollback(agent.NewError(agent.ErrorConflict, "save settings", ErrSettingsConflict))
	}
	if err := tx.Commit(); err != nil {
		return SettingsSnapshot{}, agent.NewError(agent.ErrorStorageFailure, "commit settings", err)
	}
	return SettingsSnapshot{Revision: next, Values: append(json.RawMessage(nil), payload...), UpdatedAt: updated}, nil
}

func (store *SettingsStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.db.Close()
}

func (store *SettingsStore) Checkpoint(ctx context.Context) error {
	if store == nil || store.db == nil {
		return agent.NewError(agent.ErrorStorageFailure, "checkpoint settings", errors.New("settings store is closed"))
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if _, err := store.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "checkpoint settings", err)
	}
	return nil
}

func marshalSettings(values any) ([]byte, error) {
	if values == nil {
		return nil, errors.New("settings values are required")
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode values: %w", err)
	}
	return validateSettingsJSON(payload)
}

func validateSettingsJSON(payload []byte) (json.RawMessage, error) {
	if len(payload) == 0 {
		return nil, errors.New("settings values are empty")
	}
	if len(payload) > settingsPayloadLimit {
		return nil, ErrSettingsPayloadTooLarge
	}
	if !json.Valid(payload) {
		return nil, errors.New("settings values are invalid JSON")
	}
	trimmed := bytesTrimSpace(payload)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("settings values must be a JSON object")
	}
	return append(json.RawMessage(nil), payload...), nil
}

// bytesTrimSpace avoids retaining a second package-level JSON representation
// and handles the complete whitespace set accepted by encoding/json.
func bytesTrimSpace(value []byte) []byte { return []byte(strings.TrimSpace(string(value))) }

func initializeSettings(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "INSERT OR IGNORE INTO application_settings(id, revision, values_json, updated_at_ms) VALUES (1, ?, '{}', ?)", settingsRevision, time.Now().UTC().UnixMilli())
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "initialize settings", err)
	}
	_, err = db.ExecContext(ctx, "INSERT OR IGNORE INTO session_state(id, state, updated_at_ms) VALUES (1, 'unpaired', ?)", time.Now().UTC().UnixMilli())
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "initialize session state", err)
	}
	return nil
}

func migrateSettings(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY,
        name TEXT NOT NULL UNIQUE,
        checksum TEXT NOT NULL,
        applied_at_ms INTEGER NOT NULL
    ) STRICT`); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "initialize settings migration ledger", err)
	}
	entries, err := settingsMigrationFiles.ReadDir("settings_migrations")
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "read settings migrations", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		versionText, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return agent.NewError(agent.ErrorIntegrityFailure, "validate settings migration", errors.New("invalid migration name"))
		}
		version, err := strconv.Atoi(versionText)
		if err != nil || version <= 0 {
			return agent.NewError(agent.ErrorIntegrityFailure, "validate settings migration", errors.New("invalid migration version"))
		}
		contents, err := settingsMigrationFiles.ReadFile("settings_migrations/" + entry.Name())
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "read settings migration", err)
		}
		digest := sha256.Sum256(contents)
		checksum := hex.EncodeToString(digest[:])
		var existingName, existingChecksum string
		err = db.QueryRowContext(ctx, "SELECT name, checksum FROM schema_migrations WHERE version = ?", version).Scan(&existingName, &existingChecksum)
		switch {
		case err == nil:
			if existingName != entry.Name() || existingChecksum != checksum {
				return agent.NewError(agent.ErrorIntegrityFailure, "verify settings migration", errors.New("applied migration checksum mismatch"))
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return agent.NewError(agent.ErrorStorageFailure, "read settings migration ledger", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return agent.NewError(agent.ErrorStorageFailure, "begin settings migration", err)
		}
		if _, err = tx.ExecContext(ctx, string(contents)); err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, name, checksum, applied_at_ms) VALUES (?, ?, ?, ?)", version, entry.Name(), checksum, time.Now().UTC().UnixMilli())
		}
		if err != nil {
			_ = tx.Rollback()
			return agent.NewError(agent.ErrorStorageFailure, "apply settings migration", err)
		}
		if err := tx.Commit(); err != nil {
			return agent.NewError(agent.ErrorStorageFailure, "commit settings migration", err)
		}
	}
	return nil
}
