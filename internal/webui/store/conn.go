//go:build webui

package store

import (
	"context"
	"database/sql"
	"fmt"
)

// queryer is the slice of *sql.DB / *sql.Tx the wrapper actually needs.
type queryer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// gateway is the statement path shared by DB and Tx. Every query is rebound to
// the active dialect here, and the portable insert shapes (ignore / replace /
// upsert / returning id) are rendered here, so a call site writes plain SQL and
// only the divergent statements are generated.
type gateway struct {
	q queryer
	d Dialect
}

// Dialect reports the engine behind this handle. Only the few statements whose
// SET clause cannot be expressed by the helpers below need it.
func (g gateway) Dialect() Dialect { return g.d }

func (g gateway) Exec(query string, args ...any) (sql.Result, error) {
	return g.q.Exec(g.d.rebind(query), args...)
}

func (g gateway) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return g.q.ExecContext(ctx, g.d.rebind(query), args...)
}

func (g gateway) Query(query string, args ...any) (*sql.Rows, error) {
	return g.q.Query(g.d.rebind(query), args...)
}

func (g gateway) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return g.q.QueryContext(ctx, g.d.rebind(query), args...)
}

func (g gateway) QueryRow(query string, args ...any) *sql.Row {
	return g.q.QueryRow(g.d.rebind(query), args...)
}

func (g gateway) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return g.q.QueryRowContext(ctx, g.d.rebind(query), args...)
}

// ExecIgnore runs an INSERT that must not fail when the row already exists.
// The statement is written as a plain "INSERT INTO ..." in every caller.
func (g gateway) ExecIgnore(query string, args ...any) (sql.Result, error) {
	stmt, err := g.d.insertIgnore(query)
	if err != nil {
		return nil, err
	}
	return g.q.Exec(stmt, args...)
}

// ExecReplace runs an INSERT that overwrites the row identified by conflictKey.
// Reserve it for leaf tables: MySQL implements it as DELETE + INSERT.
func (g gateway) ExecReplace(query, conflictKey string, args ...any) (sql.Result, error) {
	stmt, err := g.d.insertReplace(query, conflictKey)
	if err != nil {
		return nil, err
	}
	return g.q.Exec(stmt, args...)
}

// Upsert runs an INSERT that updates updateCols from the proposed row when the
// conflict target already exists.
func (g gateway) Upsert(query, conflictKey string, updateCols []string, args ...any) (sql.Result, error) {
	stmt, err := g.d.upsert(query, conflictKey, updateCols...)
	if err != nil {
		return nil, err
	}
	return g.q.Exec(stmt, args...)
}

// InsertID runs an INSERT and returns the generated primary key.
func (g gateway) InsertID(query string, args ...any) (int64, error) {
	stmt, returning, err := g.d.insertReturningID(query)
	if err != nil {
		return 0, err
	}
	if returning {
		var id int64
		if err := g.q.QueryRow(stmt, args...).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}
	res, err := g.q.Exec(stmt, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DB is the WebUI's handle on the database. It wraps *sql.DB so every statement
// is dialect-corrected; the embedded gateway provides Exec, Query, QueryRow and
// the insert helpers.
type DB struct {
	gateway
	raw *sql.DB
}

func wrapDB(raw *sql.DB, d Dialect) *DB {
	return &DB{gateway: gateway{q: raw, d: d}, raw: raw}
}

func (db *DB) Close() error { return db.raw.Close() }

func (db *DB) PingContext(ctx context.Context) error { return db.raw.PingContext(ctx) }

func (db *DB) Stats() sql.DBStats { return db.raw.Stats() }

func (db *DB) Begin() (*Tx, error) {
	raw, err := db.raw.Begin()
	if err != nil {
		return nil, err
	}
	return wrapTx(raw, db.d), nil
}

func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	raw, err := db.raw.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return wrapTx(raw, db.d), nil
}

// Tx is a transaction with the same dialect-corrected statements as DB.
type Tx struct {
	gateway
	raw *sql.Tx
}

func wrapTx(raw *sql.Tx, d Dialect) *Tx {
	return &Tx{gateway: gateway{q: raw, d: d}, raw: raw}
}

func (tx *Tx) Commit() error   { return tx.raw.Commit() }
func (tx *Tx) Rollback() error { return tx.raw.Rollback() }

// explain wraps a statement failure with the SQL text, which is the fastest way
// to diagnose a dialect mismatch on a backend we cannot test locally.
func explain(stmt string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w (statement: %s)", err, stmt)
}
