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

// Store is the SQLite-backed storage layer.
type Store struct {
	db *sql.DB
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

	// sql.Open only validates its arguments. Connect now so that a bad path
	// fails at startup instead of on the first request.
	if err := db.PingContext(context.Background()); err != nil {
		err = fmt.Errorf("connect to database %q: %w", path, err)
		if cerr := db.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close database: %w", cerr))
		}
		return nil, err
	}

	return &Store{db: db}, nil
}

// dsn builds the driver data source name. Without a "file:" prefix the driver
// strips the query string and opens path as a plain file name, so Windows
// paths need no URI escaping.
func dsn(path string) string {
	q := url.Values{"_pragma": pragmas}
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
