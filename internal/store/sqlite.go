// Package store persists resources in SQLite and translates database errors
// into domain errors.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// pragmas are applied by the driver to every connection it opens, because
// they are part of the DSN rather than executed once after Open.
// See docs/DESIGN.md §5.2.
var pragmas = []string{
	"busy_timeout(3000)",
	"foreign_keys(1)",
	"journal_mode(WAL)",
	"synchronous(FULL)",
}

// schema creates the tables of DESIGN.md §3.2 if they do not exist yet.
const schema = `
CREATE TABLE IF NOT EXISTS maintainers (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    email            TEXT NOT NULL UNIQUE,
    resource_version INTEGER NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);`

// Store is the SQLite-backed storage layer.
type Store struct {
	db *sql.DB

	// now returns the current time. Tests replace it to control timestamps.
	now func() time.Time
}

// Open opens (creating if needed) the SQLite database at path and verifies
// that it is reachable.
func Open(path string) (*Store, error) {
	// The driver parses the query only when '?' is not the first character,
	// so an empty path would be opened as a file named after the pragmas.
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database path must not be empty")
	}
	// The driver splits the DSN at the first '?' and treats a "file:" prefix
	// as a URI, so either would open a different file than requested.
	if strings.ContainsRune(path, '?') || strings.HasPrefix(path, "file:") {
		return nil, fmt.Errorf("database path %q must not contain '?' or start with \"file:\"", path)
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}

	// A single connection serializes database access within this process
	// (DESIGN.md D2).
	db.SetMaxOpenConns(1)

	// sql.Open only validates its arguments. Connect and create the schema
	// now, so that a bad path fails at startup instead of on the first request.
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		return nil, closeOnError(db, fmt.Errorf("connect to database %q: %w", path, err))
	}
	// journal_mode(WAL) does not fail where WAL is unsupported (e.g. some
	// network file systems); it keeps the old mode. The guarantees of
	// DESIGN.md §5.3 depend on WAL, so check that it took effect.
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return nil, closeOnError(db, fmt.Errorf("read journal mode: %w", err))
	}
	if mode != "wal" {
		return nil, closeOnError(db, fmt.Errorf("database %q does not support WAL (journal mode %q)", path, mode))
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, closeOnError(db, fmt.Errorf("create schema: %w", err))
	}

	return &Store{db: db, now: time.Now}, nil
}

// closeOnError closes db after a failed Open and joins any close error to err.
func closeOnError(db *sql.DB, err error) error {
	if cerr := db.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("close database: %w", cerr))
	}
	return err
}

// dsn builds the driver data source name. Without a "file:" prefix the driver
// strips the query string and opens path as a plain file name, so Windows
// paths need no URI escaping.
//
// _txlock=immediate makes every transaction take the write lock at BEGIN, so
// a write never fails halfway with SQLITE_BUSY_SNAPSHOT; only writes use
// transactions.
func dsn(path string) string {
	q := url.Values{"_pragma": pragmas, "_txlock": {"immediate"}}
	return path + "?" + q.Encode()
}

// Ping reports whether the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Close closes the database. It must be called only after all requests that
// use the store have finished.
func (s *Store) Close() error {
	return s.db.Close()
}
