package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RayY-96/metadata-api-server/internal/model"
)

func appInput(maintainerID, title string) model.ApplicationInput {
	return model.ApplicationInput{
		MaintainerID: maintainerID, Title: title, Version: "1.0.0", Company: "Example Corp",
		Website: "https://example.com", Source: "https://github.com/example/app",
		License: "MIT", Description: "An application.",
	}
}

func mustCreateApp(t *testing.T, s *Store, maintainerID, title string) model.Application {
	t.Helper()
	a, err := s.CreateApplication(context.Background(), appInput(maintainerID, title))
	if err != nil {
		t.Fatalf("CreateApplication: %v", err)
	}
	return a
}

func applicationIDs(items []model.Application) []string {
	ids := []string{}
	for _, a := range items {
		ids = append(ids, a.ID)
	}
	return ids
}

func mustGetApp(t *testing.T, s *Store, id string) model.Application {
	t.Helper()
	a, err := s.GetApplication(context.Background(), id)
	if err != nil {
		t.Fatalf("GetApplication: %v", err)
	}
	return a
}

func TestCreateApplication(t *testing.T) {
	s := openTestStore(t)
	setClock(s, t0, t0.Add(time.Hour))
	m := mustCreate(t, s, "Ada", "ada@example.com")
	setClock(s, t0.Add(time.Minute), t0.Add(time.Hour))

	a := mustCreateApp(t, s, m.ID, "Valet")

	if !uuidV4.MatchString(a.ID) {
		t.Errorf("ID = %q, want a version 4 UUID", a.ID)
	}
	in := appInput(m.ID, "Valet")
	want := model.Application{
		ID: a.ID, MaintainerID: m.ID, Title: in.Title, Version: in.Version, Company: in.Company,
		Website: in.Website, Source: in.Source, License: in.License, Description: in.Description,
		ResourceVersion: 1, CreatedAt: t0.Add(time.Minute), UpdatedAt: t0.Add(time.Minute),
	}
	assertSameJSON(t, a, want)
	assertSameJSON(t, mustGetApp(t, s, a.ID), a)
}

func TestCreateApplicationMissingMaintainer(t *testing.T) {
	s := openTestStore(t)
	_, err := s.CreateApplication(context.Background(), appInput("missing", "Valet"))
	if !errors.Is(err, ErrMaintainerNotFound) || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrMaintainerNotFound (not ErrNotFound)", err)
	}
	items, _, err := s.ListApplications(context.Background(), "", 10, 0)
	if err != nil || len(items) != 0 {
		t.Errorf("after failed create: %v, %v; want no Applications", items, err)
	}
}

func TestGetApplicationNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.GetApplication(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateApplication(t *testing.T) {
	// Distinct values for every optional field, so that a value written to
	// the wrong column is noticed.
	all := model.ApplicationInput{Title: "Valet 2", Version: "2.0.0", Company: "C2",
		Website: "https://w2.example", Source: "https://s2.example", License: "Apache-2.0", Description: "D2"}
	for _, tt := range []struct {
		name           string
		keepMaintainer bool // send the owner's id; otherwise omit it
		in             model.ApplicationInput
	}{
		// A full replacement (DESIGN.md A9): omitted optional fields are cleared.
		{name: "maintainerId omitted, optional fields cleared",
			in: model.ApplicationInput{Title: "Valet 2", Version: "2.0.0"}},
		{name: "maintainerId unchanged, every field replaced", keepMaintainer: true,
			in: all},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			setClock(s, t0, t0, t0.Add(time.Hour))
			m := mustCreate(t, s, "Ada", "ada@example.com")
			a := mustCreateApp(t, s, m.ID, "Valet")

			in := tt.in
			if tt.keepMaintainer {
				in.MaintainerID = m.ID
			}
			got, err := s.UpdateApplication(context.Background(), a.ID, 1, in)
			if err != nil {
				t.Fatalf("UpdateApplication: %v", err)
			}
			// The creator is kept.
			want := model.Application{
				ID: a.ID, MaintainerID: m.ID, Title: tt.in.Title, Version: tt.in.Version,
				Company: tt.in.Company, Website: tt.in.Website, Source: tt.in.Source,
				License: tt.in.License, Description: tt.in.Description,
				ResourceVersion: 2, CreatedAt: t0, UpdatedAt: t0.Add(time.Hour),
			}
			assertSameJSON(t, got, want)
			assertSameJSON(t, mustGetApp(t, s, a.ID), want)
		})
	}
}

// TestUpdateApplicationFailures also checks the order of DESIGN.md §4.2:
// 404, then 412, then a changed maintainerId (422).
func TestUpdateApplicationFailures(t *testing.T) {
	tests := []struct {
		name         string
		missing      bool
		version      int64
		maintainerID string // given as is; "" means omitted
		useBob       bool   // give the other Maintainer's id instead
		wantErr      error
	}{
		{name: "stale version", version: 1, wantErr: ErrVersionMismatch},
		{name: "missing", missing: true, version: 2, wantErr: ErrNotFound},
		{name: "changed to another Maintainer", version: 2, useBob: true, wantErr: ErrMaintainerChanged},
		{name: "changed to a missing Maintainer", version: 2, maintainerID: "unknown", wantErr: ErrMaintainerChanged},
		{name: "missing beats changed Maintainer", missing: true, version: 2, useBob: true, wantErr: ErrNotFound},
		{name: "stale version beats changed Maintainer", version: 1, useBob: true, wantErr: ErrVersionMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			ada := mustCreate(t, s, "Ada", "ada@example.com")
			bob := mustCreate(t, s, "Bob", "bob@example.com")
			a := mustCreateApp(t, s, ada.ID, "Valet")
			// Bring the Application to version 2, so that version 1 is stale.
			a, err := s.UpdateApplication(ctx, a.ID, 1, appInput("", "Valet"))
			if err != nil {
				t.Fatalf("setup update: %v", err)
			}

			id := a.ID
			if tt.missing {
				id = "missing"
			}
			maintainerID := tt.maintainerID
			if tt.useBob {
				maintainerID = bob.ID
			}
			in := appInput(maintainerID, "Changed")
			_, err = s.UpdateApplication(ctx, id, tt.version, in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == ErrMaintainerChanged && (errors.Is(err, ErrNotFound) || errors.Is(err, ErrMaintainerNotFound)) {
				t.Errorf("err = %v also matches a not-found error", err)
			}
			assertSameJSON(t, mustGetApp(t, s, a.ID), a) // unchanged, still owned by Ada
		})
	}
}

func TestDeleteApplication(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")
	a := mustCreateApp(t, s, m.ID, "Valet")

	if err := s.DeleteApplication(ctx, a.ID, 2); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("stale delete: err = %v, want ErrVersionMismatch", err)
	}
	assertSameJSON(t, mustGetApp(t, s, a.ID), a)
	if err := s.DeleteApplication(ctx, a.ID, 1); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if _, err := s.GetApplication(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteApplication(ctx, a.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

// TestDeleteMaintainerInUse is DESIGN.md A3: a Maintainer who still owns
// Applications cannot be deleted (409), and the version is checked first.
func TestDeleteMaintainerInUse(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")
	a := mustCreateApp(t, s, m.ID, "Valet")

	if err := s.DeleteMaintainer(ctx, m.ID, 1); !errors.Is(err, ErrMaintainerInUse) {
		t.Fatalf("delete referenced Maintainer: err = %v, want ErrMaintainerInUse", err)
	}
	if err := s.DeleteMaintainer(ctx, m.ID, 2); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale version beats in use: err = %v, want ErrVersionMismatch", err)
	}
	if err := s.DeleteMaintainer(ctx, "missing", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: err = %v, want ErrNotFound", err)
	}
	assertSameJSON(t, mustGet(t, s, m.ID), m) // still there
	assertSameJSON(t, mustGetApp(t, s, a.ID), a)

	// Once its Applications are gone, the Maintainer can be deleted.
	if err := s.DeleteApplication(ctx, a.ID, 1); err != nil {
		t.Fatalf("DeleteApplication: %v", err)
	}
	if err := s.DeleteMaintainer(ctx, m.ID, 1); err != nil {
		t.Errorf("delete after its Applications are gone: %v", err)
	}
}

func TestListApplications(t *testing.T) {
	s := openTestStore(t)
	times := []time.Time{t0, t0.Add(time.Second)} // the two Maintainers
	for i := range 5 {
		times = append(times, t0.Add(time.Duration(10+i)*time.Second))
	}
	setClock(s, times...)
	ada := mustCreate(t, s, "Ada", "ada@example.com")
	bob := mustCreate(t, s, "Bob", "bob@example.com")
	// Ada owns 0, 2, 4; Bob owns 1, 3.
	var apps []model.Application
	for i := range 5 {
		owner := ada.ID
		if i%2 == 1 {
			owner = bob.ID
		}
		apps = append(apps, mustCreateApp(t, s, owner, fmt.Sprintf("App %d", i)))
	}
	ids := func(idx ...int) []string {
		out := []string{}
		for _, i := range idx {
			out = append(out, apps[i].ID)
		}
		return out
	}

	tests := []struct {
		name          string
		maintainerID  string
		limit, offset int
		wantIDs       []string
		wantMore      bool
	}{
		{name: "all", limit: 20, wantIDs: ids(0, 1, 2, 3, 4)},
		{name: "all, first page", limit: 2, wantIDs: ids(0, 1), wantMore: true},
		{name: "all, last page", limit: 2, offset: 4, wantIDs: ids(4)},
		{name: "Ada's", maintainerID: ada.ID, limit: 20, wantIDs: ids(0, 2, 4)},
		{name: "Ada's, paged", maintainerID: ada.ID, limit: 2, wantIDs: ids(0, 2), wantMore: true},
		{name: "Ada's, second page", maintainerID: ada.ID, limit: 2, offset: 2, wantIDs: ids(4)},
		{name: "Bob's", maintainerID: bob.ID, limit: 20, wantIDs: ids(1, 3)},
		{name: "unknown Maintainer", maintainerID: "unknown", limit: 20, wantIDs: []string{}},
		// A page that is exactly full has nothing after it.
		{name: "all, exactly full", limit: 5, wantIDs: ids(0, 1, 2, 3, 4)},
		{name: "Bob's, exactly full", maintainerID: bob.ID, limit: 2, wantIDs: ids(1, 3)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, hasMore, err := s.ListApplications(context.Background(), tt.maintainerID, tt.limit, tt.offset)
			if err != nil {
				t.Fatalf("ListApplications: %v", err)
			}
			got := applicationIDs(items)
			if fmt.Sprint(got) != fmt.Sprint(tt.wantIDs) || hasMore != tt.wantMore {
				t.Errorf("ids = %v, hasMore = %v; want %v, %v", got, hasMore, tt.wantIDs, tt.wantMore)
			}
		})
	}

	for _, p := range []struct{ limit, offset int }{{0, 0}, {-2, 0}, {MaxPageSize + 1, 0}, {1 << 62, 0}, {10, -1}} {
		if _, _, err := s.ListApplications(context.Background(), "", p.limit, p.offset); err == nil {
			t.Errorf("limit %d offset %d: succeeded, want error", p.limit, p.offset)
		}
	}
	if _, _, err := s.ListApplications(context.Background(), "", MaxPageSize, 0); err != nil {
		t.Errorf("limit %d: %v", MaxPageSize, err)
	}
}

// TestListApplicationsTieBreak inserts rows with the same created_at directly,
// in reverse id order, so that only ORDER BY id can put them in order. Both
// list statements (with and without the filter) are checked.
func TestListApplicationsTieBreak(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")
	ts := t0.Format(timeLayout)
	for _, id := range []string{"c", "b", "a"} {
		_, err := s.db.ExecContext(ctx,
			`INSERT INTO applications (`+applicationColumns+`) VALUES (?, ?, ?, ?, '', '', '', '', '', 1, ?, ?)`,
			id, m.ID, "App", "1", ts, ts)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	for _, filter := range []string{"", m.ID} {
		items, _, err := s.ListApplications(ctx, filter, 10, 0)
		if err != nil {
			t.Fatalf("ListApplications: %v", err)
		}
		if ids := applicationIDs(items); fmt.Sprint(ids) != "[a b c]" {
			t.Errorf("filter %q: ids = %v, want [a b c]", filter, ids)
		}
		// Paging through the tie neither repeats nor skips.
		second, _, err := s.ListApplications(ctx, filter, 1, 1)
		if err != nil || len(second) != 1 || second[0].ID != "b" {
			t.Errorf("filter %q: second page = %v, %v; want [b]", filter, second, err)
		}
	}
}

func TestApplicationsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	m := mustCreate(t, s, "Ada", "ada@example.com")
	a := mustCreateApp(t, s, m.ID, "Valet")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestStoreAt(t, path)
	assertSameJSON(t, mustGetApp(t, reopened, a.ID), a)
	// The foreign key survives too.
	if err := reopened.DeleteMaintainer(context.Background(), m.ID, 1); !errors.Is(err, ErrMaintainerInUse) {
		t.Errorf("delete referenced Maintainer after reopen: err = %v, want ErrMaintainerInUse", err)
	}
}

func TestUpdateApplicationConcurrent(t *testing.T) {
	const n = 20
	ctx := context.Background()
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")
	a := mustCreateApp(t, s, m.ID, "Valet")

	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = s.UpdateApplication(ctx, a.ID, 1, appInput("", fmt.Sprintf("writer %d", i)))
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
	got := mustGetApp(t, s, a.ID)
	if got.ResourceVersion != 2 || got.Title != fmt.Sprintf("writer %d", winner) {
		t.Errorf("stored = version %d, title %q; want version 2, title of writer %d", got.ResourceVersion, got.Title, winner)
	}
}

func TestDeleteApplicationConcurrent(t *testing.T) {
	const n = 20
	ctx := context.Background()
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")
	a := mustCreateApp(t, s, m.ID, "Valet")

	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = s.DeleteApplication(ctx, a.ID, 1)
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
	if _, err := s.GetApplication(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after deletes: err = %v, want ErrNotFound", err)
	}
}

// TestDeleteMaintainerRacesCreateApplication runs a Maintainer delete against
// concurrent Application creates (DESIGN.md A3). Whichever wins, no
// Application may be left without its Maintainer.
func TestDeleteMaintainerRacesCreateApplication(t *testing.T) {
	const n = 10
	ctx := context.Background()
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")

	createErrs := make([]error, n)
	var deleteErr error
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, createErrs[i] = s.CreateApplication(ctx, appInput(m.ID, fmt.Sprintf("App %d", i)))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		deleteErr = s.DeleteMaintainer(ctx, m.ID, 1)
	}()
	close(start)
	wg.Wait()

	created := 0
	for i, err := range createErrs {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, ErrMaintainerNotFound):
			t.Errorf("create %d: err = %v, want nil or ErrMaintainerNotFound", i, err)
		}
	}
	apps, _, err := s.ListApplications(ctx, m.ID, MaxPageSize, 0)
	if err != nil {
		t.Fatalf("ListApplications: %v", err)
	}
	switch {
	case deleteErr == nil:
		// The delete ran before any create: all creates failed.
		if created != 0 || len(apps) != 0 {
			t.Errorf("Maintainer deleted, but %d creates succeeded and %d Applications remain", created, len(apps))
		}
		if _, err := s.GetMaintainer(ctx, m.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("get deleted Maintainer: err = %v, want ErrNotFound", err)
		}
	case errors.Is(deleteErr, ErrMaintainerInUse):
		// At least one create ran first; the Maintainer and all its
		// Applications remain.
		mustGet(t, s, m.ID)
		if created == 0 || len(apps) != created {
			t.Errorf("Maintainer in use: %d creates succeeded, %d Applications stored", created, len(apps))
		}
	default:
		t.Errorf("DeleteMaintainer: err = %v, want nil or ErrMaintainerInUse", deleteErr)
	}
}

func TestApplicationContextErrors(t *testing.T) {
	s := openTestStore(t)
	m := mustCreate(t, s, "Ada", "ada@example.com")
	a := mustCreateApp(t, s, m.ID, "Valet")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()

	ops := map[string]func(context.Context) error{
		"get":    func(ctx context.Context) error { _, err := s.GetApplication(ctx, a.ID); return err },
		"list":   func(ctx context.Context) error { _, _, err := s.ListApplications(ctx, m.ID, 10, 0); return err },
		"create": func(ctx context.Context) error { _, err := s.CreateApplication(ctx, appInput(m.ID, "B")); return err },
		"update": func(ctx context.Context) error {
			_, err := s.UpdateApplication(ctx, a.ID, 1, appInput("", "B"))
			return err
		},
		"delete": func(ctx context.Context) error { return s.DeleteApplication(ctx, a.ID, 1) },
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			if err := op(canceled); !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnavailable) {
				t.Errorf("canceled: err = %v, want context.Canceled and not ErrUnavailable", err)
			}
			if err := op(expired); !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("expired: err = %v, want ErrUnavailable wrapping context.DeadlineExceeded", err)
			}
		})
	}
	assertSameJSON(t, mustGetApp(t, s, a.ID), a)
}
