package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// wantPragmas is the configuration documented in DESIGN.md §5.2.
var wantPragmas = map[string]string{
	"busy_timeout": "3000",
	"foreign_keys": "1",
	"journal_mode": "wal",
	"synchronous":  "2", // FULL
}

func assertPragmas(t *testing.T, conn *sql.Conn) {
	t.Helper()
	for name, want := range wantPragmas {
		var got string
		if err := conn.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", name, err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %q, want %q", name, got, want)
		}
	}
}

func TestOpenAppliesPragmas(t *testing.T) {
	s := openTestStore(t)

	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer conn.Close()

	assertPragmas(t, conn)
}

// DESIGN.md §5.2 relies on the pragmas applying to every connection, not just
// the first. Allow a second connection temporarily and check both.
func TestPragmasApplyToEveryConnection(t *testing.T) {
	s := openTestStore(t)
	s.db.SetMaxOpenConns(2)

	ctx := context.Background()
	first, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatalf("first Conn: %v", err)
	}
	defer first.Close()
	second, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatalf("second Conn: %v", err)
	}
	defer second.Close()

	assertPragmas(t, first)
	assertPragmas(t, second)
}

func TestOpenSingleConnection(t *testing.T) {
	s := openTestStore(t)
	if got := s.db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
}

func TestOpenPathWithSpaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dir with spaces", "test.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	defer s.Close()
	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestOpenFailsForMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "test.db")
	s, err := Open(path)
	if err == nil {
		s.Close()
		t.Fatalf("Open(%q) succeeded, want error", path)
	}
}

func TestOpenRejectsPathsTheDriverWouldMisparse(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{
		"",
		"   ",
		filepath.Join(dir, "app?v2.db"),
		"file:" + filepath.Join(dir, "test.db"),
	} {
		s, err := Open(path)
		if err == nil {
			s.Close()
			t.Errorf("Open(%q) succeeded, want error", path)
		}
	}
}

func TestPingAfterCloseFails(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Ping(context.Background()); err == nil {
		t.Error("Ping after Close succeeded, want error")
	}
}
