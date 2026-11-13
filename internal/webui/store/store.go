//go:build webui

// Package store owns the WebUI variant's SQLite state: connection setup, the
// versioned schema, and (in later feature points) all typed read/write helpers.
package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps a single SQLite connection. SetMaxOpenConns(1) is deliberate: the
// WAL file plus one orchestrator means there is exactly one writer, and it also
// keeps per-connection PRAGMAs alive for the process lifetime.
type Store struct {
	db  *sql.DB
	log *slog.Logger
}

// Open creates the database (and its parent directory), verifies writability,
// applies the schema and returns a ready-to-use store.
func Open(path string, log *slog.Logger) (*Store, error) {
	return open(path, log, os.CreateTemp)
}

func open(path string, log *slog.Logger, createTemp createTempFunc) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("webui: create data directory %q: %w", dir, err)
	}
	// WAL needs -wal/-shm siblings next to the .db file, hence a directory probe.
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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, log: log}, nil
}

// DB exposes the underlying handle to the other webui packages (seed, auth,
// api, orchestrator). It is never reachable from the base build.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) Close() error { return s.db.Close() }

// Now returns the RFC3339 UTC timestamp used by every table in schema.sql.
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
