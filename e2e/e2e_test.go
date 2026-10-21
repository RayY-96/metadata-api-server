//go:build e2e

// Package e2e tests the running server over HTTP (DESIGN.md §8). Run it with
// the server's address in BASE_URL:
//
//	BASE_URL=http://127.0.0.1:8080 go test -tags=e2e -count=1 ./e2e/...
//
// Every test creates its own data with random emails, so the suite needs no
// existing data and does not depend on test order.
package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

var baseURL string

func TestMain(m *testing.M) {
	baseURL = strings.TrimRight(os.Getenv("BASE_URL"), "/")
	if baseURL == "" {
		// Running with -tags=e2e asks for the suite, so a missing server
		// address is a failure, not a reason to skip.
		fmt.Fprintln(os.Stderr, "e2e: BASE_URL must be set to the server's address, e.g. http://127.0.0.1:8080")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

var client = &http.Client{Timeout: 10 * time.Second}

// response is a received HTTP response.
type response struct {
	status int
	header http.Header
	body   []byte
}

// json checks a successful JSON response and decodes it into v.
func (r response) json(t *testing.T, v any) {
	t.Helper()
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

type fieldError struct {
	Field string `json:"field"`
}

type problem struct {
	Status int          `json:"status"`
	Code   string       `json:"code"`
	Errors []fieldError `json:"errors"`
}

// call sends a request. body is sent as JSON; headers (name, value pairs)
// are applied last and can override the Content-Type.
func call(t *testing.T, method, path, body string, headers ...string) response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+path, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: b}
}

// expectProblem checks a Problem Details error response.
func expectProblem(t *testing.T, r response, status int, code string) problem {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d: %s", r.status, status, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var p problem
	if err := json.Unmarshal(r.body, &p); err != nil {
		t.Fatalf("decode problem %s: %v", r.body, err)
	}
	if p.Status != status || p.Code != code {
		t.Errorf("problem = %+v, want status %d code %q", p, status, code)
	}
	return p
}

func expectFields(t *testing.T, p problem, want ...string) {
	t.Helper()
	var got []string
	for _, e := range p.Errors {
		got = append(got, e.Field)
	}
	if !slices.Equal(got, want) {
		t.Errorf("error fields = %v, want %v", got, want)
	}
}

// expectSame checks that a later GET returns exactly what an earlier write
// returned.
func expectSame(t *testing.T, got, want response) {
	t.Helper()
	if string(got.body) != string(want.body) {
		t.Errorf("GET body = %s\nwant       %s", got.body, want.body)
	}
	if got.header.Get("ETag") != want.header.Get("ETag") {
		t.Errorf("GET ETag = %q, want %q", got.header.Get("ETag"), want.header.Get("ETag"))
	}
}

func randomEmail(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return "e2e-" + hex.EncodeToString(b) + "@example.com"
}

type maintainer struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Email           string `json:"email"`
	ResourceVersion int64  `json:"resourceVersion"`
}

type application struct {
	ID              string    `json:"id"`
	MaintainerID    string    `json:"maintainerId"`
	Title           string    `json:"title"`
	Version         string    `json:"version"`
	Company         string    `json:"company"`
	License         string    `json:"license"`
	Description     string    `json:"description"`
	ResourceVersion int64     `json:"resourceVersion"`
	CreatedAt       time.Time `json:"createdAt"`
}

type page[T any] struct {
	Items   []T  `json:"items"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"hasMore"`
}

// expectCreated checks a 201 with ETag "1" and a Location that serves the
// same resource, and returns the response.
func expectCreated(t *testing.T, r response, collection string, v interface{ id() string }) {
	t.Helper()
	if r.status != http.StatusCreated || r.header.Get("ETag") != `"1"` {
		t.Fatalf("create = %d ETag %q: %s", r.status, r.header.Get("ETag"), r.body)
	}
	r.json(t, v)
	if loc := r.header.Get("Location"); loc != collection+"/"+v.id() {
		t.Fatalf("Location = %q, want %s/%s", loc, collection, v.id())
	}
	expectSame(t, call(t, "GET", r.header.Get("Location"), ""), r)
}

func (m *maintainer) id() string  { return m.ID }
func (a *application) id() string { return a.ID }

func createMaintainer(t *testing.T) maintainer {
	t.Helper()
	var m maintainer
	expectCreated(t, call(t, "POST", "/api/v1/maintainers", fmt.Sprintf(`{"name":"E2E","email":%q}`, randomEmail(t))),
		"/api/v1/maintainers", &m)
	return m
}

func createApplication(t *testing.T, maintainerID, title string) application {
	t.Helper()
	var a application
	expectCreated(t, call(t, "POST", "/api/v1/applications",
		fmt.Sprintf(`{"maintainerId":%q,"title":%q,"version":"1.0.0"}`, maintainerID, title)),
		"/api/v1/applications", &a)
	if a.MaintainerID != maintainerID || a.Title != title || a.Version != "1.0.0" || a.ResourceVersion != 1 {
		t.Fatalf("created %+v", a)
	}
	return a
}

func TestProbes(t *testing.T) {
	for _, path := range []string{"/livez", "/readyz"} {
		if r := call(t, "GET", path, ""); r.status != http.StatusOK || string(r.body) != "ok" {
			t.Errorf("%s = %d %q, want 200 ok", path, r.status, r.body)
		}
	}
}

func TestMaintainerLifecycle(t *testing.T) {
	email := randomEmail(t)
	other := createMaintainer(t)

	// Create: name and email are normalized.
	var m maintainer
	expectCreated(t, call(t, "POST", "/api/v1/maintainers",
		fmt.Sprintf(`{"name":" Ada ","email":%q}`, " "+strings.ToUpper(email)+" ")), "/api/v1/maintainers", &m)
	if m.Name != "Ada" || m.Email != email || m.ResourceVersion != 1 {
		t.Errorf("created %+v", m)
	}
	path := "/api/v1/maintainers/" + m.ID

	// A case variant of the email is a duplicate, on create and on update.
	expectProblem(t, call(t, "POST", "/api/v1/maintainers", fmt.Sprintf(`{"name":"X","email":%q}`, strings.ToUpper(email))),
		http.StatusConflict, "email_taken")
	expectProblem(t, call(t, "PUT", path, fmt.Sprintf(`{"name":"Ada","email":%q}`, other.Email), "If-Match", `"1"`),
		http.StatusConflict, "email_taken")

	// Update, and read back what was stored.
	body := fmt.Sprintf(`{"name":"Ada Lovelace","email":%q}`, email)
	r := call(t, "PUT", path, body, "If-Match", `"1"`)
	if r.status != http.StatusOK || r.header.Get("ETag") != `"2"` {
		t.Fatalf("update = %d ETag %q: %s", r.status, r.header.Get("ETag"), r.body)
	}
	var updated maintainer
	r.json(t, &updated)
	if updated.Name != "Ada Lovelace" || updated.ResourceVersion != 2 || updated.ID != m.ID {
		t.Errorf("updated %+v", updated)
	}
	expectSame(t, call(t, "GET", path, ""), r)

	// Preconditions: stale, missing and malformed If-Match.
	expectProblem(t, call(t, "PUT", path, body, "If-Match", `"1"`), http.StatusPreconditionFailed, "version_mismatch")
	expectProblem(t, call(t, "PUT", path, body), http.StatusPreconditionRequired, "if_match_required")
	expectProblem(t, call(t, "PUT", path, body, "If-Match", "*"), http.StatusBadRequest, "invalid_if_match")
	// A missing resource is 404, not 412, whatever the version (DESIGN.md §6.2).
	expectProblem(t, call(t, "PUT", "/api/v1/maintainers/missing", body, "If-Match", `"1"`), http.StatusNotFound, "not_found")

	// Delete.
	expectProblem(t, call(t, "DELETE", path, "", "If-Match", `"1"`), http.StatusPreconditionFailed, "version_mismatch")
	if r := call(t, "DELETE", path, "", "If-Match", `"2"`); r.status != http.StatusNoContent || len(r.body) != 0 {
		t.Fatalf("delete = %d %q, want 204", r.status, r.body)
	}
	expectProblem(t, call(t, "GET", path, ""), http.StatusNotFound, "not_found")
	expectProblem(t, call(t, "DELETE", path, "", "If-Match", `"2"`), http.StatusNotFound, "not_found")
}

func TestListMaintainers(t *testing.T) {
	createMaintainer(t)
	// Other tests' data may be present, so only the page shape is checked.
	r := call(t, "GET", "/api/v1/maintainers?limit=1", "")
	if r.status != http.StatusOK {
		t.Fatalf("list = %d %s", r.status, r.body)
	}
	var p page[maintainer]
	r.json(t, &p)
	if len(p.Items) != 1 || p.Limit != 1 || p.Offset != 0 {
		t.Errorf("page = %+v, want 1 item with limit 1 offset 0", p)
	}
	for _, q := range []string{"limit=101", "offset=-1", "limit=abc"} {
		expectProblem(t, call(t, "GET", "/api/v1/maintainers?"+q, ""), http.StatusBadRequest, "invalid_query")
	}
}

func TestApplicationLifecycle(t *testing.T) {
	m := createMaintainer(t)
	var a application
	expectCreated(t, call(t, "POST", "/api/v1/applications",
		fmt.Sprintf(`{"maintainerId":%q,"title":"Valet","version":"1.0.0","company":"Example Corp","description":"Parks cars."}`, m.ID)),
		"/api/v1/applications", &a)
	path := "/api/v1/applications/" + a.ID
	if a.Company != "Example Corp" || a.Description != "Parks cars." {
		t.Fatalf("created %+v", a)
	}

	// PUT is a full replacement (DESIGN.md A9): company and description are
	// omitted, so they are cleared; maintainerId is omitted, so it is kept.
	body := `{"title":"Valet 2","version":"2.0.0","license":"MIT"}`
	r := call(t, "PUT", path, body, "If-Match", `"1"`)
	if r.status != http.StatusOK || r.header.Get("ETag") != `"2"` {
		t.Fatalf("update = %d ETag %q: %s", r.status, r.header.Get("ETag"), r.body)
	}
	var updated application
	r.json(t, &updated)
	if updated.MaintainerID != m.ID || updated.Title != "Valet 2" || updated.License != "MIT" ||
		updated.Company != "" || updated.Description != "" || updated.ResourceVersion != 2 {
		t.Errorf("updated %+v", updated)
	}
	expectSame(t, call(t, "GET", path, ""), r)

	// The same preconditions as for Maintainers.
	expectProblem(t, call(t, "PUT", path, body, "If-Match", `"1"`), http.StatusPreconditionFailed, "version_mismatch")
	expectProblem(t, call(t, "PUT", path, body), http.StatusPreconditionRequired, "if_match_required")
	expectProblem(t, call(t, "DELETE", path, ""), http.StatusPreconditionRequired, "if_match_required")
	expectProblem(t, call(t, "DELETE", path, "", "If-Match", `W/"2"`), http.StatusBadRequest, "invalid_if_match")
	expectProblem(t, call(t, "DELETE", path, "", "If-Match", `"1"`), http.StatusPreconditionFailed, "version_mismatch")
	expectProblem(t, call(t, "PUT", "/api/v1/applications/missing", body, "If-Match", `"1"`), http.StatusNotFound, "not_found")

	if r := call(t, "DELETE", path, "", "If-Match", `"2"`); r.status != http.StatusNoContent || len(r.body) != 0 {
		t.Fatalf("delete = %d %q, want 204", r.status, r.body)
	}
	expectProblem(t, call(t, "GET", path, ""), http.StatusNotFound, "not_found")
	expectProblem(t, call(t, "DELETE", path, "", "If-Match", `"2"`), http.StatusNotFound, "not_found")
}

// TestRelationshipRules covers DESIGN.md A1–A3.
func TestRelationshipRules(t *testing.T) {
	m := createMaintainer(t)
	other := createMaintainer(t)

	// An Application needs an existing Maintainer.
	p := expectProblem(t, call(t, "POST", "/api/v1/applications", `{"maintainerId":"missing","title":"T","version":"1"}`),
		http.StatusUnprocessableEntity, "maintainer_not_found")
	expectFields(t, p, "maintainerId")

	a := createApplication(t, m.ID, "Valet")
	appPath := "/api/v1/applications/" + a.ID

	// The creator cannot be changed, and the refused update changes nothing.
	p = expectProblem(t, call(t, "PUT", appPath, fmt.Sprintf(`{"maintainerId":%q,"title":"T","version":"1"}`, other.ID), "If-Match", `"1"`),
		http.StatusUnprocessableEntity, "maintainer_changed")
	expectFields(t, p, "maintainerId")
	var stored application
	call(t, "GET", appPath, "").json(t, &stored)
	if stored.MaintainerID != m.ID || stored.Title != "Valet" || stored.ResourceVersion != 1 {
		t.Errorf("after refused update: %+v", stored)
	}

	// A Maintainer who still owns Applications cannot be deleted.
	mPath := "/api/v1/maintainers/" + m.ID
	expectProblem(t, call(t, "DELETE", mPath, "", "If-Match", `"1"`), http.StatusConflict, "maintainer_in_use")
	if r := call(t, "GET", mPath, ""); r.status != http.StatusOK {
		t.Errorf("Maintainer after refused delete = %d", r.status)
	}

	// Once its Applications are gone, it can.
	if r := call(t, "DELETE", appPath, "", "If-Match", `"1"`); r.status != http.StatusNoContent {
		t.Fatalf("delete application = %d %s", r.status, r.body)
	}
	if r := call(t, "DELETE", mPath, "", "If-Match", `"1"`); r.status != http.StatusNoContent {
		t.Errorf("delete maintainer = %d %s", r.status, r.body)
	}
}

func TestListApplicationsByMaintainer(t *testing.T) {
	m := createMaintainer(t)
	var apps []application
	for i := range 3 {
		apps = append(apps, createApplication(t, m.ID, fmt.Sprintf("App %d", i)))
	}
	createApplication(t, createMaintainer(t).ID, "Someone else's")
	// Pages are ordered by (createdAt, id); creations within one clock tick
	// share a timestamp, so the expected order is computed the same way.
	slices.SortFunc(apps, func(x, y application) int {
		if c := x.CreatedAt.Compare(y.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	var want []string
	for _, a := range apps {
		want = append(want, a.ID)
	}

	listPage := func(offset int) page[application] {
		t.Helper()
		r := call(t, "GET", fmt.Sprintf("/api/v1/applications?maintainerId=%s&limit=2&offset=%d", m.ID, offset), "")
		if r.status != http.StatusOK {
			t.Fatalf("list = %d %s", r.status, r.body)
		}
		var p page[application]
		r.json(t, &p)
		return p
	}
	ids := func(items []application) []string {
		out := []string{}
		for _, a := range items {
			out = append(out, a.ID)
		}
		return out
	}

	first := listPage(0)
	if !reflect.DeepEqual(ids(first.Items), want[:2]) || !first.HasMore || first.Limit != 2 || first.Offset != 0 {
		t.Errorf("first page = ids %v hasMore %v limit %d offset %d; want %v, true, 2, 0",
			ids(first.Items), first.HasMore, first.Limit, first.Offset, want[:2])
	}
	second := listPage(2)
	if !reflect.DeepEqual(ids(second.Items), want[2:]) || second.HasMore || second.Offset != 2 {
		t.Errorf("second page = ids %v hasMore %v offset %d; want %v, false, 2",
			ids(second.Items), second.HasMore, second.Offset, want[2:])
	}

	// A Maintainer that does not exist has no Applications: an empty page.
	r := call(t, "GET", "/api/v1/applications?maintainerId=missing", "")
	var empty page[application]
	r.json(t, &empty)
	if r.status != http.StatusOK || len(empty.Items) != 0 || empty.HasMore {
		t.Errorf("unknown Maintainer = %d %+v, want 200 and an empty page", r.status, empty)
	}

	expectProblem(t, call(t, "GET", "/api/v1/applications?limit=101", ""), http.StatusBadRequest, "invalid_query")
	expectProblem(t, call(t, "GET", "/api/v1/applications?maintainerId=", ""), http.StatusBadRequest, "invalid_query")
}

// TestErrorContract checks the Problem Details responses of DESIGN.md §4.3
// that do not depend on stored data. Bodies use random emails so that a
// broken server accepting one cannot affect later runs.
func TestErrorContract(t *testing.T) {
	email := randomEmail(t)
	expectProblem(t, call(t, "GET", "/nope", ""), http.StatusNotFound, "not_found")
	r := call(t, "PATCH", "/api/v1/maintainers/x", "")
	expectProblem(t, r, http.StatusMethodNotAllowed, "method_not_allowed")
	if allow := r.header.Get("Allow"); allow != "DELETE, GET, HEAD, PUT" {
		t.Errorf("Allow = %q", allow)
	}
	expectProblem(t, call(t, "POST", "/api/v1/maintainers", fmt.Sprintf(`{"name":"A","email":%q}`, email), "Content-Type", "text/plain"),
		http.StatusUnsupportedMediaType, "unsupported_media_type")
	expectProblem(t, call(t, "POST", "/api/v1/maintainers", `{"name":"`+strings.Repeat("a", 70000)+`"}`),
		http.StatusRequestEntityTooLarge, "body_too_large")
	expectProblem(t, call(t, "POST", "/api/v1/maintainers", fmt.Sprintf(`{"name":"A","email":%q,"id":"x"}`, email)),
		http.StatusBadRequest, "invalid_body")
	p := expectProblem(t, call(t, "POST", "/api/v1/maintainers", `{"name":"","email":"Ada <a@example.com>"}`),
		http.StatusUnprocessableEntity, "validation_failed")
	expectFields(t, p, "name", "email")
}
