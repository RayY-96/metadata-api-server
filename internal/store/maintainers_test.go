package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	sqlite3 "modernc.org/sqlite/lib"

	"github.com/RayY-96/metadata-api-server/internal/model"
)

var t0 = time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)

// setClock makes the store return the given times, one per call, so that
// timestamps are known in advance.
func setClock(s *Store, times ...time.Time) {
	s.now = func() time.Time {
		next := times[0]
		if len(times) > 1 {
			times = times[1:]
		}
		return next
	}
}

// assertSameJSON compares resources by their JSON form, which is what clients
// see: a POST response must equal a later GET byte for byte.
func assertSameJSON(t *testing.T, got, want any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(g) != string(w) {
		t.Errorf("got  %s\nwant %s", g, w)
	}
}

func mustCreate(t *testing.T, s *Store, name, email string) model.Maintainer {
	t.Helper()
	m, err := s.CreateMaintainer(context.Background(), model.MaintainerInput{Name: name, Email: email})
	if err != nil {
		t.Fatalf("CreateMaintainer: %v", err)
	}
	return m
}

func mustGet(t *testing.T, s *Store, id string) model.Maintainer {
	t.Helper()
	m, err := s.GetMaintainer(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMaintainer: %v", err)
	}
	return m
}

func maintainerIDs(items []model.Maintainer) []string {
	ids := []string{}
	for _, m := range items {
		ids = append(ids, m.ID)
	}
	return ids
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestCreateMaintainer(t *testing.T) {
	s := openTestStore(t)
	created := t0.Add(123456789 * time.Nanosecond) // sub-microsecond part is dropped
	// Both timestamps come from one clock reading: a second reading would
	// return the later time.
	setClock(s, created, created.Add(time.Hour))

	m := mustCreate(t, s, "Ada", "ada@example.com")

	if !uuidV4.MatchString(m.ID) {
		t.Errorf("ID = %q, want a version 4 UUID", m.ID)
	}
	want := model.Maintainer{
		ID: m.ID, Name: "Ada", Email: "ada@example.com", ResourceVersion: 1,
		CreatedAt: t0.Add(123456 * time.Microsecond), UpdatedAt: t0.Add(123456 * time.Microsecond),
	}
	assertSameJSON(t, m, want)
	assertSameJSON(t, mustGet(t, s, m.ID), m)

	other := mustCreate(t, s, "Bob", "bob@example.com")
	if other.ID == m.ID {
		t.Error("two Maintainers got the same ID")
	}
}

func TestCreateMaintainerDuplicateEmail(t *testing.T) {
	s := openTestStore(t)
	first := mustCreate(t, s, "Ada", "ada@example.com")

	_, err := s.CreateMaintainer(context.Background(), model.MaintainerInput{Name: "Other", Email: "ada@example.com"})
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
	items, _, err := s.ListMaintainers(context.Background(), 100, 0)
	if err != nil || len(items) != 1 || items[0].ID != first.ID {
		t.Errorf("after duplicate: items = %v, err = %v, want only the first Maintainer", items, err)
	}
}

func TestGetMaintainerNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.GetMaintainer(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateMaintainer(t *testing.T) {
	s := openTestStore(t)
	setClock(s, t0, t0.Add(time.Hour))
	m := mustCreate(t, s, "Ada", "ada@example.com")

	got, err := s.UpdateMaintainer(context.Background(), m.ID, 1,
		model.MaintainerInput{Name: "Ada Lovelace", Email: "ada.l@example.com"})
	if err != nil {
		t.Fatalf("UpdateMaintainer: %v", err)
	}
	want := model.Maintainer{
		ID: m.ID, Name: "Ada Lovelace", Email: "ada.l@example.com", ResourceVersion: 2,
		CreatedAt: t0, UpdatedAt: t0.Add(time.Hour),
	}
	assertSameJSON(t, got, want)
	assertSameJSON(t, mustGet(t, s, m.ID), want)
}

func TestUpdateMaintainerKeepsOwnEmail(t *testing.T) {
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")
	if _, err := s.UpdateMaintainer(context.Background(), m.ID, 1,
		model.MaintainerInput{Name: "Ada L", Email: "ada@example.com"}); err != nil {
		t.Errorf("UpdateMaintainer with unchanged email: %v", err)
	}
}

func TestUpdateMaintainerFailures(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		missing bool // update an id that does not exist
		version int64
		email   string
		wantErr error
	}{
		{name: "stale version", version: 1,
			email: "new@example.com", wantErr: ErrVersionMismatch},
		{name: "future version", version: 9,
			email: "new@example.com", wantErr: ErrVersionMismatch},
		{name: "missing", missing: true, version: 2,
			email: "new@example.com", wantErr: ErrNotFound},
		{name: "email of another Maintainer", version: 2,
			email: "bob@example.com", wantErr: ErrEmailTaken},
		// DESIGN.md §4.2: 404 before 412 before 409.
		{name: "missing beats taken email", missing: true, version: 2,
			email: "bob@example.com", wantErr: ErrNotFound},
		{name: "stale version beats taken email", version: 1,
			email: "bob@example.com", wantErr: ErrVersionMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			m := mustCreate(t, s, "Ada", "ada@example.com")
			mustCreate(t, s, "Bob", "bob@example.com")
			// Bring Ada to version 2, so that version 1 is stale.
			m, err := s.UpdateMaintainer(ctx, m.ID, 1, model.MaintainerInput{Name: "Ada", Email: "ada@example.com"})
			if err != nil {
				t.Fatalf("setup update: %v", err)
			}

			id := m.ID
			if tt.missing {
				id = "missing"
			}
			_, err = s.UpdateMaintainer(ctx, id, tt.version, model.MaintainerInput{Name: "X", Email: tt.email})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			assertSameJSON(t, mustGet(t, s, m.ID), m) // unchanged
		})
	}
}

func TestDeleteMaintainer(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")

	if err := s.DeleteMaintainer(ctx, m.ID, 2); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("delete with wrong version: err = %v, want ErrVersionMismatch", err)
	}
	assertSameJSON(t, mustGet(t, s, m.ID), m) // still there

	if err := s.DeleteMaintainer(ctx, m.ID, 1); err != nil {
		t.Fatalf("DeleteMaintainer: %v", err)
	}
	if _, err := s.GetMaintainer(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteMaintainer(ctx, m.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
	// The email is free again.
	mustCreate(t, s, "Ada", "ada@example.com")
}

func TestListMaintainers(t *testing.T) {
	s := openTestStore(t)
	empty, hasMore, err := s.ListMaintainers(context.Background(), 20, 0)
	if err != nil || empty == nil || len(empty) != 0 || hasMore {
		t.Fatalf("empty list = %v, %v, %v; want [], false, nil", empty, hasMore, err)
	}

	// Created out of order. With a variable-width layout such as
	// RFC3339Nano, "…:01Z" would sort after "…:01.5Z" because 'Z' > '.';
	// the fixed-width layout keeps the order chronological.
	times := []time.Time{
		t0.Add(1500 * time.Millisecond),                  // b
		t0.Add(time.Second),                              // a: on a whole second
		t0.Add(2 * time.Second),                          // c
		t0.Add(3 * time.Second), t0.Add(3 * time.Second), // d1, d2: tie, ordered by id
	}
	setClock(s, times...)
	var created []model.Maintainer
	for i := range times {
		created = append(created, mustCreate(t, s, "M", fmt.Sprintf("m%d@example.com", i)))
	}
	d1, d2 := created[3], created[4]
	if d2.ID < d1.ID {
		d1, d2 = d2, d1
	}
	wantOrder := []string{created[1].ID, created[0].ID, created[2].ID, d1.ID, d2.ID}

	tests := []struct {
		limit, offset int
		wantIDs       []string
		wantMore      bool
	}{
		{limit: 20, offset: 0, wantIDs: wantOrder, wantMore: false},
		{limit: 2, offset: 0, wantIDs: wantOrder[0:2], wantMore: true},
		{limit: 2, offset: 2, wantIDs: wantOrder[2:4], wantMore: true},
		{limit: 2, offset: 4, wantIDs: wantOrder[4:5], wantMore: false},
		{limit: 5, offset: 0, wantIDs: wantOrder, wantMore: false}, // exactly all: no more
		{limit: 1, offset: 5, wantIDs: []string{}, wantMore: false},
		{limit: 2, offset: 100, wantIDs: []string{}, wantMore: false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("limit %d offset %d", tt.limit, tt.offset), func(t *testing.T) {
			items, hasMore, err := s.ListMaintainers(context.Background(), tt.limit, tt.offset)
			if err != nil {
				t.Fatalf("ListMaintainers: %v", err)
			}
			ids := maintainerIDs(items)
			if fmt.Sprint(ids) != fmt.Sprint(tt.wantIDs) || hasMore != tt.wantMore {
				t.Errorf("ids = %v, hasMore = %v; want %v, %v", ids, hasMore, tt.wantIDs, tt.wantMore)
			}
		})
	}
}

// TestListMaintainersTieBreak inserts rows with the same created_at directly,
// in reverse id order, so that only ORDER BY id can put them in order.
func TestListMaintainersTieBreak(t *testing.T) {
	s := openTestStore(t)
	ts := t0.Format(timeLayout)
	for _, id := range []string{"c", "b", "a"} {
		_, err := s.db.ExecContext(context.Background(),
			`INSERT INTO maintainers (`+maintainerColumns+`) VALUES (?, ?, ?, 1, ?, ?)`,
			id, "M", id+"@example.com", ts, ts)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	items, _, err := s.ListMaintainers(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("ListMaintainers: %v", err)
	}
	if ids := maintainerIDs(items); fmt.Sprint(ids) != "[a b c]" {
		t.Errorf("ids = %v, want [a b c]", ids)
	}
}

func TestListMaintainersRejectsInvalidPages(t *testing.T) {
	s := openTestStore(t)
	for _, p := range []struct{ limit, offset int }{
		{0, 0}, {-2, 0}, {MaxPageSize + 1, 0}, {1 << 62, 0}, {10, -1},
	} {
		if _, _, err := s.ListMaintainers(context.Background(), p.limit, p.offset); err == nil {
			t.Errorf("limit %d offset %d: succeeded, want error", p.limit, p.offset)
		}
	}
	if _, _, err := s.ListMaintainers(context.Background(), MaxPageSize, 0); err != nil {
		t.Errorf("limit %d: %v", MaxPageSize, err)
	}
}

func TestMaintainersPersistAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	m := mustCreate(t, s, "Ada", "ada@example.com")
	m, err = s.UpdateMaintainer(ctx, m.ID, 1, model.MaintainerInput{Name: "Ada L", Email: "ada@example.com"})
	if err != nil {
		t.Fatalf("UpdateMaintainer: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestStoreAt(t, path)
	assertSameJSON(t, mustGet(t, reopened, m.ID), m)
	// Constraints survive too.
	if _, err := reopened.CreateMaintainer(ctx, model.MaintainerInput{Name: "X", Email: "ada@example.com"}); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate after reopen: err = %v, want ErrEmailTaken", err)
	}
}

func TestContextErrors(t *testing.T) {
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()

	ops := map[string]func(context.Context) error{
		"get":  func(ctx context.Context) error { _, err := s.GetMaintainer(ctx, m.ID); return err },
		"list": func(ctx context.Context) error { _, _, err := s.ListMaintainers(ctx, 10, 0); return err },
		"create": func(ctx context.Context) error {
			_, err := s.CreateMaintainer(ctx, model.MaintainerInput{Name: "B", Email: "b@example.com"})
			return err
		},
		"update": func(ctx context.Context) error {
			_, err := s.UpdateMaintainer(ctx, m.ID, 1, model.MaintainerInput{Name: "B", Email: "b@example.com"})
			return err
		},
		"delete": func(ctx context.Context) error { return s.DeleteMaintainer(ctx, m.ID, 1) },
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			// DESIGN.md §4.6: cancellation is returned as-is, a deadline is
			// ErrUnavailable.
			if err := op(canceled); !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
				t.Errorf("canceled: err = %v, want context.Canceled and not ErrUnavailable", err)
			}
			if err := op(expired); !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("expired: err = %v, want ErrUnavailable wrapping context.DeadlineExceeded", err)
			}
		})
	}
	assertSameJSON(t, mustGet(t, s, m.ID), m) // nothing changed
}

// TestLockedDatabase holds the write lock from a second connection, as a
// backup tool or a second server process could, so that a write has to wait
// in the driver until busy_timeout (3 s) expires.
func TestLockedDatabase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want func(error) bool
	}{
		{name: "busy timeout is ErrUnavailable",
			ctx:  func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			want: func(err error) bool { return errors.Is(err, ErrUnavailable) }},
		{name: "deadline while waiting is ErrUnavailable",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 100*time.Millisecond)
			},
			want: func(err error) bool {
				return errors.Is(err, ErrUnavailable) && errors.Is(err, context.DeadlineExceeded)
			}},
		{name: "cancellation while waiting stays Canceled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(100*time.Millisecond, cancel)
				return ctx, cancel
			},
			want: func(err error) bool { return errors.Is(err, context.Canceled) && !errors.Is(err, ErrUnavailable) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "test.db")
			s := openTestStoreAt(t, path)
			lockDatabase(t, path)

			ctx, cancel := tt.ctx()
			defer cancel()
			_, err := s.CreateMaintainer(ctx, model.MaintainerInput{Name: "Ada", Email: "ada@example.com"})
			if !tt.want(err) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// TestWriteLockTakenAtBegin checks _txlock=immediate (DESIGN.md §5.2): while
// another connection holds the write lock, BEGIN itself fails with
// SQLITE_BUSY. A deferred BEGIN would succeed and fail only at the first
// write.
func TestWriteLockTakenAtBegin(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "test.db")
	s := openTestStoreAt(t, path)
	lockDatabase(t, path)

	tx, err := s.db.BeginTx(context.Background(), nil)
	if err == nil {
		if rerr := tx.Rollback(); rerr != nil {
			t.Errorf("rollback: %v", rerr)
		}
		t.Fatal("BEGIN succeeded while another connection holds the write lock")
	}
	if sqliteCode(err)&0xff != sqlite3.SQLITE_BUSY {
		t.Errorf("BEGIN err = %v, want SQLITE_BUSY", err)
	}
}

// TestUpdateMaintainerConcurrent sends n updates with the same version at
// once (DESIGN.md §6.4): exactly one wins, the rest get ErrVersionMismatch,
// and the stored state is the winner's.
func TestUpdateMaintainerConcurrent(t *testing.T) {
	const n = 20
	ctx := context.Background()
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")

	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = s.UpdateMaintainer(ctx, m.ID, 1,
				model.MaintainerInput{Name: fmt.Sprintf("writer %d", i), Email: "ada@example.com"})
		}()
	}
	close(start)
	wg.Wait()

	winner := -1
	for i, err := range errs {
		switch {
		case err == nil && winner == -1:
			winner = i
		case err == nil:
			t.Errorf("writers %d and %d both succeeded", winner, i)
		case !errors.Is(err, ErrVersionMismatch):
			t.Errorf("writer %d: err = %v, want ErrVersionMismatch", i, err)
		}
	}
	if winner == -1 {
		t.Fatal("no writer succeeded")
	}
	got := mustGet(t, s, m.ID)
	if got.ResourceVersion != 2 || got.Name != fmt.Sprintf("writer %d", winner) {
		t.Errorf("stored = version %d, name %q; want version 2, name of writer %d",
			got.ResourceVersion, got.Name, winner)
	}
	if _, err := s.UpdateMaintainer(ctx, m.ID, 1, model.MaintainerInput{Name: "late", Email: "ada@example.com"}); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("write with the old version afterwards: err = %v, want ErrVersionMismatch", err)
	}
}

// TestDeleteMaintainerConcurrent sends n deletes with the same version at
// once: exactly one wins, and the rest find the row gone (404, not 412).
func TestDeleteMaintainerConcurrent(t *testing.T) {
	const n = 20
	ctx := context.Background()
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")

	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = s.DeleteMaintainer(ctx, m.ID, 1)
		}()
	}
	close(start)
	wg.Wait()

	wins := 0
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, ErrNotFound):
			t.Errorf("deleter %d: err = %v, want ErrNotFound", i, err)
		}
	}
	if wins != 1 {
		t.Errorf("%d deletes succeeded, want exactly 1", wins)
	}
	if _, err := s.GetMaintainer(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after deletes: err = %v, want ErrNotFound", err)
	}
}

// TestWrap covers the classification rules independently of when the driver
// returns which error.
func TestWrap(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	// A generic error: a context ending does not cause it, so it must stay a
	// server error even after the context ended. The context-caused driver
	// errors (SQLITE_INTERRUPT, SQLITE_BUSY) cannot be constructed here; the
	// real-database tests cover how the driver reports them.
	driverErr := errors.New("disk I/O error")

	tests := []struct {
		name            string
		ctx             context.Context
		err             error
		wantCanceled    bool
		wantDeadline    bool
		wantUnavailable bool
	}{
		{name: "plain error", ctx: context.Background(), err: driverErr},
		{name: "context error itself", ctx: canceled, err: context.Canceled, wantCanceled: true},
		{name: "deadline itself", ctx: expired, err: context.DeadlineExceeded, wantDeadline: true, wantUnavailable: true},
		{name: "rolled-back transaction after cancellation", ctx: canceled, err: sql.ErrTxDone, wantCanceled: true},
		{name: "rolled-back transaction after deadline", ctx: expired, err: sql.ErrTxDone,
			wantDeadline: true, wantUnavailable: true},
		{name: "unrelated error after cancellation stays a server error", ctx: canceled, err: driverErr},
		{name: "unrelated error after deadline stays a server error", ctx: expired, err: driverErr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := wrap(tt.ctx, "op", tt.err)
			if !errors.Is(err, tt.err) {
				t.Errorf("%v does not wrap the original error", err)
			}
			if errors.Is(err, context.Canceled) != tt.wantCanceled ||
				errors.Is(err, context.DeadlineExceeded) != tt.wantDeadline ||
				errors.Is(err, ErrUnavailable) != tt.wantUnavailable {
				t.Errorf("err = %v; want canceled %v, deadline %v, unavailable %v",
					err, tt.wantCanceled, tt.wantDeadline, tt.wantUnavailable)
			}
		})
	}
}

// lockDatabase takes the write lock on the database at path from a separate
// connection until the test ends.
func lockDatabase(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open locker: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("locker conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	t.Cleanup(func() {
		if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
			t.Errorf("unlock: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Errorf("close locker conn: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Errorf("close locker: %v", err)
		}
	})
}
