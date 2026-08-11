// Package store is everything this application keeps: registries, cards, where
// they stand and what has happened to them. One SQLite file, opened once.
//
// SQL is written by hand. A query builder was considered and left out — the one
// live candidate has been frozen since 2023, and hiding a couple of dozen
// statements behind it costs more than writing them.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the build cross-compiles
)

// Store is the database and the dialect it speaks.
type Store struct {
	db *sqlx.DB
	d  Dialect
}

// Open opens (creating if needed) the database at path and brings the schema up
// to date.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("создать папку для базы: %w", err)
		}
	}
	d := sqliteDialect{}
	db, err := sqlx.Open(d.Name(), d.DSN(path))
	if err != nil {
		return nil, fmt.Errorf("открыть базу: %w", err)
	}
	// SQLite takes one writer at a time; more connections buy contention, not
	// throughput, and WAL already keeps readers out of the writer's way.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("база не отвечает: %w", err)
	}
	for _, stmt := range d.Setup() {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("подготовить соединение (%s): %w", stmt, err)
		}
	}
	s := &Store{db: db, d: d}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// OpenMemory is an empty database for tests.
func OpenMemory() (*Store, error) {
	d := sqliteDialect{}
	db, err := sqlx.Open(d.Name(), ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one connection, or every query gets its own empty database
	for _, stmt := range d.Setup() {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	s := &Store{db: db, d: d}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate applies the steps this database has not seen, in order, each in its
// own transaction. A half-applied step would leave a schema nothing describes.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migration (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("таблица миграций: %w", err)
	}
	var applied int
	if err := s.db.Get(&applied, `SELECT COALESCE(MAX(version), 0) FROM schema_migration`); err != nil {
		return fmt.Errorf("прочитать версию схемы: %w", err)
	}
	steps := s.d.Migrations()
	if applied > len(steps) {
		return fmt.Errorf("база собрана более новой версией приложения (схема %d, известно %d)", applied, len(steps))
	}
	for i := applied; i < len(steps); i++ {
		version := i + 1
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("миграция %d: %w", version, err)
		}
		if _, err := tx.Exec(steps[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("миграция %d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migration (version, applied_at) VALUES (?, ?)`,
			version, millis(time.Now())); err != nil {
			tx.Rollback()
			return fmt.Errorf("миграция %d: отметка о применении: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("миграция %d: %w", version, err)
		}
	}
	return nil
}

// tx runs fn in a transaction, rolling back on error or panic. Every compound
// write in this package goes through it — saving a flow is four tables, and
// half a flow is not a flow.
func (s *Store) tx(fn func(*sqlx.Tx) error) error {
	tx, err := s.db.Beginx()
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---- times ----
//
// Stored as INTEGER unix milliseconds: a representation every dialect reads the
// same way, which keeps the seam out of every scan. A zero time stays zero, so
// "never" survives the round trip.

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func nullMillis(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func fromNullMillis(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64).UTC()
}

// ---- json columns ----
//
// A few columns hold JSON: an agent's environment, a rule's match. They are
// values nothing queries *into* — always read whole, never filtered on — which
// is exactly when a column beats a table.

func encodeJSON(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func decodeJSON(text string, into any) {
	if text == "" {
		return
	}
	_ = json.Unmarshal([]byte(text), into)
}

// ctx is the timeout every store call runs under when the caller has none of
// its own. The database is local and single-writer: anything slower than this
// is a bug, not load.
func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
