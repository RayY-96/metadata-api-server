package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RayY-96/metadata-api-server/internal/model"
	"github.com/RayY-96/metadata-api-server/internal/validation"
)

// request describes one HTTP request to the API handler.
type request struct {
	method      string
	path        string
	body        string
	contentType string // defaults to application/json when body is set
	ifMatch     []string
	ctx         context.Context
}

func serve(t *testing.T, h http.Handler, req request) *httptest.ResponseRecorder {
	t.Helper()
	ctx := req.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	r := httptest.NewRequestWithContext(ctx, req.method, req.path, strings.NewReader(req.body))
	ct := req.contentType
	if ct == "" && req.body != "" {
		ct = "application/json"
	}
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	for _, v := range req.ifMatch {
		r.Header.Add("If-Match", v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	return NewServer(openStore(t)).Handler()
}

// createMaintainer creates a Maintainer through the API and returns it.
func createMaintainer(t *testing.T, h http.Handler, name, email string) model.Maintainer {
	t.Helper()
	rec := serve(t, h, request{method: "POST", path: "/api/v1/maintainers",
		body: `{"name":"` + name + `","email":"` + email + `"}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	return decodeMaintainer(t, rec)
}

func decodeMaintainer(t *testing.T, rec *httptest.ResponseRecorder) model.Maintainer {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var m model.Maintainer
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode maintainer: %v", err)
	}
	return m
}

func TestCreateMaintainer(t *testing.T) {
	h := newHandler(t)
	rec := serve(t, h, request{method: "POST", path: "/api/v1/maintainers",
		body: `{"name":"  Ada Lovelace ","email":" Ada@Example.COM "}`})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	m := decodeMaintainer(t, rec)
	if m.Name != "Ada Lovelace" || m.Email != "ada@example.com" || m.ResourceVersion != 1 || m.ID == "" {
		t.Errorf("created %+v, want normalized name and email, version 1, an id", m)
	}
	if !m.CreatedAt.Equal(m.UpdatedAt) || m.CreatedAt.Location() != time.UTC {
		t.Errorf("timestamps %v / %v, want equal and UTC", m.CreatedAt, m.UpdatedAt)
	}
	if got := rec.Header().Get("Location"); got != "/api/v1/maintainers/"+m.ID {
		t.Errorf("Location = %q, want /api/v1/maintainers/%s", got, m.ID)
	}
	if got := rec.Header().Get("ETag"); got != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", got)
	}

	// The stored value is the normalized one: a case variant is a duplicate.
	dup := serve(t, h, request{method: "POST", path: "/api/v1/maintainers",
		body: `{"name":"Other","email":"ADA@example.com"}`})
	assertProblem(t, dup, http.StatusConflict, "email_taken", "email is already in use")

	// The response equals what a later GET returns.
	get := serve(t, h, request{method: "GET", path: "/api/v1/maintainers/" + m.ID})
	if got := decodeMaintainer(t, get); !reflect.DeepEqual(got, m) {
		t.Errorf("GET = %+v, want the POST response %+v", got, m)
	}
}

func TestCreateMaintainerErrors(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantCode    string
	}{
		{name: "wrong content type", body: `{"name":"A","email":"a@example.com"}`, contentType: "text/plain",
			wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "body too large", body: `{"name":"` + strings.Repeat("a", 70000) + `"}`,
			wantStatus: 413, wantCode: "body_too_large"},
		{name: "malformed JSON", body: `{"name":`, wantStatus: 400, wantCode: "invalid_body"},
		{name: "server-managed field", body: `{"name":"A","email":"a@example.com","id":"x"}`,
			wantStatus: 400, wantCode: "invalid_body"},
		{name: "resourceVersion cannot be set", body: `{"name":"A","email":"a@example.com","resourceVersion":5}`,
			wantStatus: 400, wantCode: "invalid_body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, newHandler(t), request{method: "POST", path: "/api/v1/maintainers",
				body: tt.body, contentType: tt.contentType})
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if p := decodeProblem(t, rec); p.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", p.Code, tt.wantCode)
			}
		})
	}
}

func TestCreateMaintainerValidation(t *testing.T) {
	rec := serve(t, newHandler(t), request{method: "POST", path: "/api/v1/maintainers",
		body: `{"name":" ","email":"Ada <ada@example.com>"}`})
	assertValidationProblem(t, rec, []validation.FieldError{
		{Field: "name", Message: "is required"},
		{Field: "email", Message: "must be a bare email address such as name@example.com"},
	})
}

func assertValidationProblem(t *testing.T, rec *httptest.ResponseRecorder, want []validation.FieldError) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	p := decodeProblem(t, rec)
	if p.Code != "validation_failed" || p.Title != "Unprocessable Entity" || !reflect.DeepEqual(p.Errors, want) {
		t.Errorf("problem = %+v, want validation_failed with errors %v", p, want)
	}
}

func TestGetMaintainer(t *testing.T) {
	h := newHandler(t)
	m := createMaintainer(t, h, "Ada", "ada@example.com")

	rec := serve(t, h, request{method: "GET", path: "/api/v1/maintainers/" + m.ID})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("GET = %d ETag %q, want 200 \"1\"", rec.Code, rec.Header().Get("ETag"))
	}
	if got := decodeMaintainer(t, rec); !reflect.DeepEqual(got, m) {
		t.Errorf("GET = %+v, want %+v", got, m)
	}

	missing := serve(t, h, request{method: "GET", path: "/api/v1/maintainers/missing"})
	assertProblem(t, missing, http.StatusNotFound, "not_found", "resource not found")
}

func TestListMaintainers(t *testing.T) {
	h := newHandler(t)
	empty := serve(t, h, request{method: "GET", path: "/api/v1/maintainers"})
	if empty.Code != http.StatusOK || empty.Header().Get("ETag") != "" {
		t.Fatalf("empty list = %d ETag %q, want 200 without ETag", empty.Code, empty.Header().Get("ETag"))
	}
	if got := strings.TrimSpace(empty.Body.String()); got != `{"items":[],"limit":20,"offset":0,"hasMore":false}` {
		t.Errorf("empty list body = %s", got)
	}

	var created []model.Maintainer
	for _, e := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		created = append(created, createMaintainer(t, h, "M", e))
	}
	// Expected order is (createdAt, id): requests within the same clock tick
	// get equal timestamps, so creation order alone is not enough.
	slices.SortFunc(created, func(a, b model.Maintainer) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	var ids []string
	for _, m := range created {
		ids = append(ids, m.ID)
	}
	tests := []struct {
		query    string
		wantIDs  []string
		limit    int
		offset   int
		wantMore bool
	}{
		{query: "", wantIDs: ids, limit: 20},
		{query: "?limit=2", wantIDs: ids[:2], limit: 2, wantMore: true},
		{query: "?limit=2&offset=2", wantIDs: ids[2:], limit: 2, offset: 2},
		{query: "?limit=100&offset=3", wantIDs: []string{}, limit: 100, offset: 3},
		{query: "?offset=1&unknown=x", wantIDs: ids[1:], limit: 20, offset: 1}, // unknown parameters are ignored
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			rec := serve(t, h, request{method: "GET", path: "/api/v1/maintainers" + tt.query})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			var p page[model.Maintainer]
			if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got := []string{}
			for _, m := range p.Items {
				got = append(got, m.ID)
			}
			if !reflect.DeepEqual(got, tt.wantIDs) || p.Limit != tt.limit || p.Offset != tt.offset || p.HasMore != tt.wantMore {
				t.Errorf("page = ids %v limit %d offset %d hasMore %v; want %v %d %d %v",
					got, p.Limit, p.Offset, p.HasMore, tt.wantIDs, tt.limit, tt.offset, tt.wantMore)
			}
		})
	}
}

func TestListMaintainersInvalidQuery(t *testing.T) {
	h := newHandler(t)
	for _, q := range []string{
		"limit=0", "limit=101", "limit=-1", "limit=abc", "limit=", "limit=1.5", "limit=1&limit=2",
		"offset=-1", "offset=x", "offset=99999999999999999999",
		"limit=+5", "offset=-0", "offset=+1", // signs are not accepted, as in If-Match
		"limit=%zz", "limit=5;x", // malformed query strings are rejected, not ignored
	} {
		t.Run(q, func(t *testing.T) {
			rec := serve(t, h, request{method: "GET", path: "/api/v1/maintainers?" + q})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if p := decodeProblem(t, rec); p.Code != "invalid_query" {
				t.Errorf("code = %q, want invalid_query", p.Code)
			}
		})
	}
}

func TestUpdateMaintainer(t *testing.T) {
	h := newHandler(t)
	m := createMaintainer(t, h, "Ada", "ada@example.com")

	rec := serve(t, h, request{method: "PUT", path: "/api/v1/maintainers/" + m.ID, ifMatch: []string{`"1"`},
		body: `{"name":"Ada Lovelace","email":"ADA.L@example.com"}`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("PUT = %d ETag %q, want 200 \"2\": %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	got := decodeMaintainer(t, rec)
	if got.Name != "Ada Lovelace" || got.Email != "ada.l@example.com" || got.ResourceVersion != 2 ||
		!got.CreatedAt.Equal(m.CreatedAt) || got.ID != m.ID {
		t.Errorf("updated = %+v", got)
	}

	// The old ETag no longer matches.
	stale := serve(t, h, request{method: "PUT", path: "/api/v1/maintainers/" + m.ID, ifMatch: []string{`"1"`},
		body: `{"name":"X","email":"x@example.com"}`})
	assertProblem(t, stale, http.StatusPreconditionFailed, "version_mismatch",
		"If-Match does not match the current resource version")
}

// TestUpdateMaintainerErrors checks each error and the evaluation order of
// DESIGN.md §4.2 when a request has several problems at once.
func TestUpdateMaintainerErrors(t *testing.T) {
	const valid = `{"name":"New","email":"new@example.com"}`
	tests := []struct {
		name       string
		id         string // "" means the existing Maintainer
		body       string
		ifMatch    []string
		wantStatus int
		wantCode   string
		wantErrors []validation.FieldError
	}{
		{name: "missing If-Match", body: valid, wantStatus: 428, wantCode: "if_match_required"},
		{name: "stale version", body: valid, ifMatch: []string{`"1"`}, wantStatus: 412, wantCode: "version_mismatch"},
		{name: "missing Maintainer", id: "missing", body: valid, ifMatch: []string{`"2"`},
			wantStatus: 404, wantCode: "not_found"},
		{name: "email of another Maintainer", body: `{"name":"A","email":"bob@example.com"}`, ifMatch: []string{`"2"`},
			wantStatus: 409, wantCode: "email_taken"},
		{name: "invalid fields", body: `{"name":"","email":"new@example.com"}`, ifMatch: []string{`"2"`},
			wantStatus: 422, wantCode: "validation_failed",
			wantErrors: []validation.FieldError{{Field: "name", Message: "is required"}}},

		// Order: body syntax (1) before If-Match (2) before validation (3)
		// before lookup (4) before version (5) before constraints (6).
		{name: "malformed body beats missing If-Match", body: `{`, wantStatus: 400, wantCode: "invalid_body"},
		{name: "missing If-Match beats invalid fields", body: `{"name":""}`, wantStatus: 428, wantCode: "if_match_required"},
		{name: "malformed If-Match beats invalid fields", body: `{"name":""}`, ifMatch: []string{"*"},
			wantStatus: 400, wantCode: "invalid_if_match"},
		{name: "invalid fields beat stale version", body: `{"name":""}`, ifMatch: []string{`"1"`},
			wantStatus: 422, wantCode: "validation_failed",
			wantErrors: []validation.FieldError{{Field: "name", Message: "is required"}, {Field: "email", Message: "is required"}}},
		{name: "missing beats stale version", id: "missing", body: valid, ifMatch: []string{`"1"`},
			wantStatus: 404, wantCode: "not_found"},
		{name: "stale version beats taken email", body: `{"name":"A","email":"bob@example.com"}`, ifMatch: []string{`"1"`},
			wantStatus: 412, wantCode: "version_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(t)
			m := createMaintainer(t, h, "Ada", "ada@example.com")
			createMaintainer(t, h, "Bob", "bob@example.com")
			// Bring Ada to version 2, so that "1" is stale.
			if rec := serve(t, h, request{method: "PUT", path: "/api/v1/maintainers/" + m.ID, ifMatch: []string{`"1"`},
				body: `{"name":"Ada","email":"ada@example.com"}`}); rec.Code != http.StatusOK {
				t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
			}
			id := tt.id
			if id == "" {
				id = m.ID
			}

			rec := serve(t, h, request{method: "PUT", path: "/api/v1/maintainers/" + id, body: tt.body, ifMatch: tt.ifMatch})
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if p := decodeProblem(t, rec); p.Code != tt.wantCode || !reflect.DeepEqual(p.Errors, tt.wantErrors) {
				t.Errorf("code = %q errors %v, want %q %v", p.Code, p.Errors, tt.wantCode, tt.wantErrors)
			}
			// Nothing changed.
			get := serve(t, h, request{method: "GET", path: "/api/v1/maintainers/" + m.ID})
			if got := decodeMaintainer(t, get); got.ResourceVersion != 2 || got.Email != "ada@example.com" {
				t.Errorf("after failed update: %+v", got)
			}
		})
	}
}

func TestIfMatchFormat(t *testing.T) {
	malformed := [][]string{
		{`W/"1"`},      // weak
		{`*`},          // any
		{`"1", "2"`},   // list
		{`"1"`, `"1"`}, // repeated header
		{`1`},          // unquoted
		{`"01"`},       // leading zero: never sent by the server
		{`"0"`},        // not positive
		{`"-1"`},       // signed
		{`"+1"`},       // signed
		{`""`},         // empty tag
		{``},           // empty header
		{`"abc"`},      // not a number
		{`"1`},         // unterminated
		{`"1 "`},       // whitespace inside the tag
		{`" 1"`},
		{`"9223372036854775808"`}, // out of range
	}
	for _, ifMatch := range malformed {
		for _, method := range []string{"PUT", "DELETE"} {
			t.Run(method+" "+strings.Join(ifMatch, " | "), func(t *testing.T) {
				// A fresh Maintainer, so that one wrongly accepted value
				// cannot delete the record that later cases use.
				h := newHandler(t)
				m := createMaintainer(t, h, "Ada", "ada@example.com")
				req := request{method: method, path: "/api/v1/maintainers/" + m.ID, ifMatch: ifMatch}
				if method == "PUT" {
					req.body = `{"name":"Ada","email":"ada@example.com"}`
				}
				rec := serve(t, h, req)
				assertProblem(t, rec, http.StatusBadRequest, "invalid_if_match", `If-Match must be a single strong ETag such as "3"`)
			})
		}
	}
	// Surrounding whitespace is allowed by HTTP.
	h := newHandler(t)
	m := createMaintainer(t, h, "Ada", "ada@example.com")
	rec := serve(t, h, request{method: "PUT", path: "/api/v1/maintainers/" + m.ID, ifMatch: []string{` "1" `},
		body: `{"name":"Ada","email":"ada@example.com"}`})
	if rec.Code != http.StatusOK {
		t.Errorf("If-Match with spaces: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteMaintainer(t *testing.T) {
	h := newHandler(t)
	m := createMaintainer(t, h, "Ada", "ada@example.com")
	path := "/api/v1/maintainers/" + m.ID

	assertProblem(t, serve(t, h, request{method: "DELETE", path: path}),
		http.StatusPreconditionRequired, "if_match_required", "If-Match header with the current ETag is required")
	assertProblem(t, serve(t, h, request{method: "DELETE", path: path, ifMatch: []string{`"2"`}}),
		http.StatusPreconditionFailed, "version_mismatch", "If-Match does not match the current resource version")

	rec := serve(t, h, request{method: "DELETE", path: path, ifMatch: []string{`"1"`}})
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("DELETE = %d %q, want 204 with no body", rec.Code, rec.Body.String())
	}
	assertProblem(t, serve(t, h, request{method: "GET", path: path}), http.StatusNotFound, "not_found", "resource not found")
	assertProblem(t, serve(t, h, request{method: "DELETE", path: path, ifMatch: []string{`"1"`}}),
		http.StatusNotFound, "not_found", "resource not found")
}

// A missing or malformed If-Match is reported before the lookup (DESIGN.md
// §4.2), so a missing Maintainer gives 428 or 400, not 404.
func TestDeleteMaintainerOrder(t *testing.T) {
	h := newHandler(t)
	assertProblem(t, serve(t, h, request{method: "DELETE", path: "/api/v1/maintainers/missing"}),
		http.StatusPreconditionRequired, "if_match_required", "If-Match header with the current ETag is required")
	assertProblem(t, serve(t, h, request{method: "DELETE", path: "/api/v1/maintainers/missing", ifMatch: []string{"*"}}),
		http.StatusBadRequest, "invalid_if_match", `If-Match must be a single strong ETag such as "3"`)
}

func TestMaintainerRoutes(t *testing.T) {
	h := newHandler(t)
	tests := []struct {
		method, path string
		wantStatus   int
		wantAllow    string
	}{
		{"PATCH", "/api/v1/maintainers/x", 405, "DELETE, GET, HEAD, PUT"},
		{"POST", "/api/v1/maintainers/x", 405, "DELETE, GET, HEAD, PUT"},
		{"DELETE", "/api/v1/maintainers", 405, "GET, HEAD, POST"},
		{"GET", "/api/v1/maintainers/x/y", 404, ""},
		{"GET", "/api/v1/maintainers/", 404, ""},
		{"GET", "/api/v1/maintainer", 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := serve(t, h, request{method: tt.method, path: tt.path})
			if rec.Code != tt.wantStatus || rec.Header().Get("Allow") != tt.wantAllow {
				t.Errorf("= %d Allow %q, want %d Allow %q", rec.Code, rec.Header().Get("Allow"), tt.wantStatus, tt.wantAllow)
			}
			if rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// TestStoreFailures maps store errors that are not about the request itself:
// cancellation and deadlines (DESIGN.md §4.6, D9) and unexpected errors.
func TestStoreFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()

	// Every handler passes the request context to the store.
	for _, req := range []request{
		{method: "GET", path: "/api/v1/maintainers"},
		{method: "POST", path: "/api/v1/maintainers", body: `{"name":"Bob","email":"bob@example.com"}`},
		{method: "GET", path: "/api/v1/maintainers/{id}"},
		{method: "PUT", path: "/api/v1/maintainers/{id}", ifMatch: []string{`"1"`}, body: `{"name":"Bob","email":"bob@example.com"}`},
		{method: "DELETE", path: "/api/v1/maintainers/{id}", ifMatch: []string{`"1"`}},
	} {
		t.Run("request canceled: "+req.method+" "+req.path, func(t *testing.T) {
			h := newHandler(t)
			m := createMaintainer(t, h, "Ada", "ada@example.com")
			req.path = strings.Replace(req.path, "{id}", m.ID, 1)
			req.ctx = canceled
			assertProblem(t, serve(t, h, req), http.StatusServiceUnavailable, "unavailable", "request was canceled")
		})
	}
	t.Run("deadline exceeded", func(t *testing.T) {
		rec := serve(t, newHandler(t), request{method: "POST", path: "/api/v1/maintainers", ctx: expired,
			body: `{"name":"Ada","email":"ada@example.com"}`})
		assertProblem(t, rec, http.StatusServiceUnavailable, "unavailable", "database temporarily unavailable")
	})
	t.Run("unexpected error", func(t *testing.T) {
		st := openStore(t)
		h := NewServer(st).Handler()
		if err := st.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		rec := serve(t, h, request{method: "GET", path: "/api/v1/maintainers/x"})
		// The detail is generic; the store error is only logged.
		assertProblem(t, rec, http.StatusInternalServerError, "internal_error", "internal server error")
	})
}
