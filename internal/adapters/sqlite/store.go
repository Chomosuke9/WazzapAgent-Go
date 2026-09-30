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

	"github.com/Chomosuke9/DiscordAgent-Go/internal/agent"
	"github.com/Chomosuke9/DiscordAgent-Go/internal/identity"
	_ "modernc.org/sqlite"
)

const defaultBusyTimeoutMS = 5000

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Options struct {
	Clock            agent.Clock
	SenderRefFactory func() (identity.SenderRef, error)
}

type Store struct {
	db         *sql.DB // the one writer; transactions that write use it
	read       *sql.DB // readers
	clock      agent.Clock
	senderRefs func() (identity.SenderRef, error)
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
	db, err := openDatabase(ctx, absolute, "&_txlock=immediate", 1)
	if err != nil {
		return nil, agent.NewError(agent.ErrorStorageFailure, "open application store", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// WAL lets readers run beside the one writer, so a slow write (a turn
	// commit, maintenance) never makes a read wait behind it.
	read, err := openDatabase(ctx, absolute, "", readConnections)
	if err != nil {
		_ = db.Close()
		return nil, agent.NewError(agent.ErrorStorageFailure, "open application store readers", err)
	}
	if options.Clock == nil {
		options.Clock = agent.SystemClock{}
	}
	if options.SenderRefFactory == nil {
		options.SenderRefFactory = identity.NewSenderRef
	}
	return &Store{db: db, read: read, clock: options.Clock, senderRefs: options.SenderRefFactory}, nil
}

func (store *Store) Close() error { return errors.Join(store.read.Close(), store.db.Close()) }

// ResolveInterrupted runs once at startup, before anything is sent. A send
// or effect still marked executing was cut off by the last shutdown; it may
// or may not have reached Discord, so it becomes unknown and is never sent
// again.
func (store *Store) ResolveInterrupted(ctx context.Context, tenantID identity.TenantID) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin interrupted outbox resolution", err)
	}
	defer tx.Rollback()
	nowMS := store.clock.Now().UnixMilli()
	if err := resolveInterruptedActions(ctx, tx, tenantID, nowMS); err != nil {
		return err
	}
	if err := resolveInterruptedEffects(ctx, tx, tenantID, nowMS); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageError("commit interrupted outbox resolution", err)
	}
	return nil
}

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

// readConnections bounds the application store's reader pool.
const readConnections = 4

// openDatabase opens a WAL handle on path with up to connections connections.
func openDatabase(ctx context.Context, path, extraDSN string, connections int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", databaseDSN(path, defaultBusyTimeoutMS)+extraDSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(connections)
	db.SetMaxIdleConns(connections)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
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
	return applyMigrations(ctx, db, migrationFiles, "migrations")
}

// applyMigrations applies every embedded NNN_name.sql file in dir that the
// ledger has not recorded yet. Each file runs in its own BEGIN IMMEDIATE
// transaction and the ledger is re-read inside it, so two handles on the same
// file (the agent store and the UI reader) cannot apply a migration twice.
// Foreign keys are off while a migration runs, as SQLite requires for table
// rebuilds, and foreign_key_check must pass before it commits.
func applyMigrations(ctx context.Context, db *sql.DB, files embed.FS, dir string) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version INTEGER PRIMARY KEY,
        name TEXT NOT NULL UNIQUE,
        checksum TEXT NOT NULL,
        applied_at_ms INTEGER NOT NULL
    ) STRICT`); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "initialize migration ledger", err)
	}
	entries, err := files.ReadDir(dir)
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
		contents, err := files.ReadFile(dir + "/" + entry.Name())
		if err != nil {
			return agent.NewError(agent.ErrorInternal, "read embedded migration", err)
		}
		if err := applyMigration(ctx, db, version, entry.Name(), contents); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, version int, name string, contents []byte) error {
	digest := sha256.Sum256(contents)
	checksum := hex.EncodeToString(digest[:])
	conn, err := db.Conn(ctx)
	if err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "begin migration", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "begin migration", err)
	}
	defer conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return agent.NewError(agent.ErrorStorageFailure, "begin migration", err)
	}
	fail := func(operation string, err error) error {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return agent.NewError(agent.ErrorStorageFailure, operation, err)
	}
	var existingName, existingChecksum string
	err = conn.QueryRowContext(ctx, "SELECT name, checksum FROM schema_migrations WHERE version = ?", version).Scan(&existingName, &existingChecksum)
	switch {
	case err == nil:
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		if existingName != name || existingChecksum != checksum {
			return agent.NewError(agent.ErrorIntegrityFailure, "verify migration", fmt.Errorf("applied migration checksum mismatch"))
		}
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fail("read migration ledger", err)
	}
	if _, err := conn.ExecContext(ctx, string(contents)); err != nil {
		return fail("apply migration", err)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, checksum, applied_at_ms) VALUES (?, ?, ?, ?)",
		version, name, checksum, time.Now().UTC().UnixMilli(),
	); err != nil {
		return fail("record migration", err)
	}
	var violation string
	switch err := conn.QueryRowContext(ctx, "SELECT \"table\" FROM pragma_foreign_key_check LIMIT 1").Scan(&violation); {
	case err == nil:
		return fail("check migration foreign keys", fmt.Errorf("foreign key violation in %s", violation))
	case !errors.Is(err, sql.ErrNoRows):
		return fail("check migration foreign keys", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fail("commit migration", err)
	}
	return nil
}
