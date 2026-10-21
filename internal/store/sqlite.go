// Package store persists resources in SQLite and translates database errors
// into domain errors.
package store

import (
	"context"
	"crypto/rand"
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
);

CREATE TABLE IF NOT EXISTS applications (
    id               TEXT PRIMARY KEY,
    -- No ON DELETE action: the default (NO ACTION, checked at the end of
    -- each statement) blocks the delete and reports
    -- SQLITE_CONSTRAINT_FOREIGNKEY; RESTRICT would report
    -- SQLITE_CONSTRAINT_TRIGGER (DESIGN.md D10).
    maintainer_id    TEXT NOT NULL REFERENCES maintainers(id),
    title            TEXT NOT NULL,
    version          TEXT NOT NULL,
    company          TEXT NOT NULL DEFAULT '',
    website          TEXT NOT NULL DEFAULT '',
    source           TEXT NOT NULL DEFAULT '',
    license          TEXT NOT NULL DEFAULT '',
    description      TEXT NOT NULL DEFAULT '',
    resource_version INTEGER NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

-- Serves both FK checks on maintainer delete and the ?maintainerId= filter.
CREATE INDEX IF NOT EXISTS idx_applications_maintainer ON applications(maintainer_id);`

// timeLayout stores timestamps as fixed-width UTC text, so that ordering by
// the TEXT column is chronological (DESIGN.md §4.5).
const timeLayout = "2006-01-02T15:04:05.000000Z"

// MaxPageSize is the largest page a list method returns (DESIGN.md §4.5).
const MaxPageSize = 100

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
	// Without foreign keys, a missing or in-use Maintainer would be accepted
	// silently (DESIGN.md A1-A3), so check that they are enforced.
	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return nil, closeOnError(db, fmt.Errorf("read foreign_keys: %w", err))
	}
	if foreignKeys != 1 {
		return nil, closeOnError(db, fmt.Errorf("foreign keys are not enforced on database %q", path))
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

// timestamp returns the current time at the stored precision, so that the
// value returned to the client equals the value read back later.
func (s *Store) timestamp() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}

// newID returns a random (version 4) UUID.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// scanner is implemented by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// parseTimestamps parses the stored created_at and updated_at of the
// resource kind with the given id.
func parseTimestamps(kind, id, created, updated string) (time.Time, time.Time, error) {
	c, err := time.Parse(timeLayout, created)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse created_at of %s %s: %w", kind, id, err)
	}
	u, err := time.Parse(timeLayout, updated)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse updated_at of %s %s: %w", kind, id, err)
	}
	return c, u, nil
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
