//go:build webui

// Package store owns the WebUI variant's relational state: connection setup for
// SQLite, MySQL and PostgreSQL, the versioned schema, and the dialect shims that
// keep the rest of the WebUI writing portable SQL.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Config selects the backend. DSN is what the driver expects: a file path for
// SQLite, a native go-sql-driver DSN for MySQL
// ("user:pass@tcp(host:3306)/dbname"), a URL or keyword DSN for PostgreSQL
// ("postgres://user:pass@host:5432/dbname").
type Config struct {
	Dialect Dialect
	DSN     string
	// MaxOpenConns caps the pool of a network backend. SQLite ignores it: the
	// WAL file plus one orchestrator means exactly one writer, and a single
	// connection also keeps per-connection PRAGMAs alive for the process.
	MaxOpenConns int
}

// Store wraps the database handle. The WebUI never sees a raw *sql.DB, so every
// statement goes through the dialect shims.
type Store struct {
	db      *DB
	dialect Dialect
	log     *slog.Logger
}

// Open opens a SQLite database at path. It is the shorthand every test uses and
// the default the binary still picks when no driver is configured.
func Open(path string, log *slog.Logger) (*Store, error) {
	return OpenConfig(Config{Dialect: SQLite, DSN: path}, log)
}

// OpenConfig opens the configured backend, verifies it is reachable and
// migrates it to the schema this binary expects.
func OpenConfig(cfg Config, log *slog.Logger) (*Store, error) {
	return open(cfg, log, os.CreateTemp)
}

// open is the seam the tests use to inject a failing temp-file probe.
func open(cfg Config, log *slog.Logger, createTemp createTempFunc) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	dialect := cfg.Dialect
	if dialect == "" {
		dialect = SQLite
	}
	dsn := normalizeDSN(cfg.DSN)
	if dsn == "" {
		return nil, fmt.Errorf("webui: empty database DSN for %s", dialect)
	}

	var raw *sql.DB
	var err error
	switch dialect {
	case SQLite:
		if raw, err = openSQLite(dsn, log, createTemp); err != nil {
			return nil, err
		}
	case MySQL:
		if raw, err = sql.Open("mysql", dsn); err != nil {
			return nil, fmt.Errorf("webui: open mysql: %w", err)
		}
	case Postgres:
		if raw, err = sql.Open("pgx", dsn); err != nil {
			return nil, fmt.Errorf("webui: open postgres: %w", err)
		}
	default:
		return nil, fmt.Errorf("webui: unsupported database driver %q", dialect)
	}

	if dialect.IsNetwork() {
		conns := cfg.MaxOpenConns
		if conns <= 0 {
			conns = 10
		}
		raw.SetMaxOpenConns(conns)
		raw.SetMaxIdleConns(conns)
		raw.SetConnMaxLifetime(30 * time.Minute)
		// Fail fast with a readable error instead of a 500 on the first login:
		// a wrong password or an unreachable container must stop startup.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := raw.PingContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("webui: connect to %s: %w", dialect, err)
		}
	}

	db := wrapDB(raw, dialect)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, dialect: dialect, log: log}, nil
}

// openSQLite prepares the file database: create the parent directory, verify it
// is writable (WAL needs -wal/-shm siblings next to the .db file), then switch
// on WAL, a busy timeout and foreign keys.
func openSQLite(path string, log *slog.Logger, createTemp createTempFunc) (*sql.DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("webui: create data directory %q: %w", dir, err)
	}
	if err := probeWritableDir(dir, createTemp); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("webui: open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0) // keep the single connection (and its PRAGMAs) alive

	var mode string
	if err := db.QueryRow("PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
		db.Close()
		return nil, fmt.Errorf("webui: enable WAL: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		log.Warn("webui: SQLite did not switch to WAL", "journal_mode", mode)
	}
	for _, pragma := range []string{"PRAGMA busy_timeout = 5000", "PRAGMA foreign_keys = ON"} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("webui: %s: %w", pragma, err)
		}
	}
	return db, nil
}

// DB exposes the handle to the other webui packages (seed, auth, api,
// orchestrator). It is never reachable from the base build.
func (s *Store) DB() *DB { return s.db }

// Dialect reports the configured backend.
func (s *Store) Dialect() Dialect { return s.dialect }

func (s *Store) Close() error { return s.db.Close() }

// Now returns the RFC3339 UTC timestamp used by every table in the schema.
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
