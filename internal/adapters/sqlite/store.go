package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Chomosuke9/WazzapAgent-Go/internal/agent"
	"github.com/Chomosuke9/WazzapAgent-Go/internal/identity"
	_ "modernc.org/sqlite"
)

const (
	defaultBusyTimeoutMS = 5000
	defaultGenerationTTL = 2 * time.Minute
	defaultActionTTL     = 30 * time.Second
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Options struct {
	GenerationLeaseTTL time.Duration
	ActionLeaseTTL     time.Duration
	Clock              agent.Clock
	SenderRefFactory   func() (identity.SenderRef, error)
}

type Store struct {
	db            *sql.DB
	generationTTL time.Duration
	actionTTL     time.Duration
	clock         agent.Clock
	senderRefs    func() (identity.SenderRef, error)
}

type ConfigStore struct{ *Store }
type TurnStore struct{ *Store }
type ActionStore struct{ *Store }
type EffectStore struct{ *Store }
type InboundStore struct{ *Store }
type HistoryStore struct{ *Store }

func (store *Store) Configs() *ConfigStore  { return &ConfigStore{Store: store} }
func (store *Store) Turns() *TurnStore      { return &TurnStore{Store: store} }
func (store *Store) Actions() *ActionStore  { return &ActionStore{Store: store} }
func (store *Store) Effects() *EffectStore  { return &EffectStore{Store: store} }
func (store *Store) Inbound() *InboundStore { return &InboundStore{Store: store} }
func (store *Store) History() *HistoryStore { return &HistoryStore{Store: store} }

func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithOptions(ctx, path, Options{})
}

func OpenWithOptions(ctx context.Context, path string, options Options) (*Store, error) {
	absolute, err := safeDatabasePath(path)
	if err != nil {
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open application store", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "create application store directory", err)
	}
	db, err := sql.Open("sqlite", databaseDSN(absolute, defaultBusyTimeoutMS))
	if err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "open application store", err)
	}
	// A single writer connection makes transaction ordering deterministic for
	// the embedded conversation deployment. WAL still permits the separate Hypermeow
	// database and external backup/checkpoint operations to progress safely.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, agent.NewError(agent.ErrorStorageFailure, "ping application store", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if options.GenerationLeaseTTL == 0 {
		options.GenerationLeaseTTL = defaultGenerationTTL
	}
	if options.ActionLeaseTTL == 0 {
		options.ActionLeaseTTL = defaultActionTTL
	}
	if options.Clock == nil {
		options.Clock = agent.SystemClock{}
	}
	if options.SenderRefFactory == nil {
		options.SenderRefFactory = identity.NewSenderRef
	}
	if options.GenerationLeaseTTL <= 0 || options.ActionLeaseTTL <= 0 {
		_ = db.Close()
		return nil, agent.NewError(agent.ErrorInvalidArgument, "open application store", fmt.Errorf("lease TTLs must be positive"))
	}
	return &Store{
		db: db, generationTTL: options.GenerationLeaseTTL, actionTTL: options.ActionLeaseTTL,
		clock: options.Clock, senderRefs: options.SenderRefFactory,
	}, nil
}

func (store *Store) Close() error { return store.db.Close() }

func (store *Store) Checkpoint(ctx context.Context) error {
	if _, err := store.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "checkpoint application store", err)
	}
	return nil
}

func safeDatabasePath(path string) (string, error) {
	if strings.ContainsRune(path, '\x00') || strings.ContainsAny(path, "?#%") {
		return "", fmt.Errorf("database path contains unsupported characters")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if filepath.Dir(absolute) == absolute {
		return "", fmt.Errorf("filesystem root is not a database path")
	}
	return filepath.Clean(absolute), nil
}

func databaseDSN(path string, busyTimeoutMS int) string {
	query := make(url.Values)
	query.Set("_foreign_keys", "on")
	query.Set("_busy_timeout", strconv.Itoa(busyTimeoutMS))
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "NORMAL")
	return "file:" + filepath.ToSlash(path) + "?" + query.Encode()
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY,
        name TEXT NOT NULL UNIQUE,
        checksum TEXT NOT NULL,
        applied_at_ms INTEGER NOT NULL
    ) STRICT`); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "initialize migration ledger", err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return agent.NewError(agent.ErrorInternal, "read embedded migrations", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		versionText, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return agent.NewError(agent.ErrorIntegrityFailure, "validate migration", fmt.Errorf("invalid migration name"))
		}
		version, err := strconv.Atoi(versionText)
		if err != nil || version <= 0 {
			return agent.NewError(agent.ErrorIntegrityFailure, "validate migration", fmt.Errorf("invalid migration version"))
		}
		contents, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "read embedded migration", err)
		}
		digest := sha256.Sum256(contents)
		checksum := hex.EncodeToString(digest[:])
		var existingName, existingChecksum string
		err = db.QueryRowContext(ctx, "SELECT name, checksum FROM schema_migrations WHERE version = ?", version).Scan(&existingName, &existingChecksum)
		switch {
		case err == nil:
			if existingName != entry.Name() || existingChecksum != checksum {
				return agent.NewError(agent.ErrorIntegrityFailure, "verify migration", fmt.Errorf("applied migration checksum mismatch"))
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return agent.NewError(agent.ErrorStorageFailure, "read migration ledger", err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return agent.NewError(agent.ErrorStorageFailure, "begin migration", err)
		}
		if _, err = tx.ExecContext(ctx, string(contents)); err == nil {
			_, err = tx.ExecContext(ctx,
				"INSERT INTO schema_migrations(version, name, checksum, applied_at_ms) VALUES (?, ?, ?, ?)",
				version, entry.Name(), checksum, time.Now().UTC().UnixMilli(),
			)
		}
		if err != nil {
			_ = tx.Rollback()
			return agent.NewError(agent.ErrorStorageFailure, "apply migration", err)
		}
		if err := tx.Commit(); err != nil {
			return agent.NewError(agent.ErrorStorageFailure, "commit migration", err)
		}
	}
	return nil
}
