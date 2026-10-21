package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/RayY-96/metadata-api-server/internal/model"
	"github.com/RayY-96/metadata-api-server/internal/validation"
)

func appBody(maintainerID, title string) string {
	return fmt.Sprintf(`{"maintainerId":%q,"title":%q,"version":"1.0.0","company":"Example Corp",`+
		`"website":"https://example.com","source":"https://github.com/example/app","license":"MIT",`+
		`"description":"An application."}`, maintainerID, title)
}

// createApplication creates an Application through the API and returns it.
func createApplication(t *testing.T, h http.Handler, maintainerID, title string) model.Application {
	t.Helper()
	rec := serve(t, h, request{method: "POST", path: "/api/v1/applications", body: appBody(maintainerID, title)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create application: %d %s", rec.Code, rec.Body.String())
	}
	return decodeApplication(t, rec)
}

func decodeApplication(t *testing.T, rec *httptest.ResponseRecorder) model.Application {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var a model.Application
	dec := json.NewDecoder(rec.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		t.Fatalf("decode application: %v", err)
	}
	return a
}

// getApplication reads an Application through the API and checks that the
// response carries its current ETag.
func getApplication(t *testing.T, h http.Handler, id string) model.Application {
	t.Helper()
	rec := serve(t, h, request{method: "GET", path: "/api/v1/applications/" + id})
	if rec.Code != http.StatusOK {
		t.Fatalf("get application: %d %s", rec.Code, rec.Body.String())
	}
	a := decodeApplication(t, rec)
	if got, want := rec.Header().Get("ETag"), etag(a.ResourceVersion); got != want {
		t.Errorf("GET ETag = %q, want %q", got, want)
	}
	return a
}

func TestCreateApplication(t *testing.T) {
	h := newHandler(t)
	m := createMaintainer(t, h, "Ada", "ada@example.com")

	rec := serve(t, h, request{method: "POST", path: "/api/v1/applications",
		body: `{"maintainerId":" ` + m.ID + ` ","title":"  Valet ","version":"1.0.0"}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	a := decodeApplication(t, rec)
	if a.MaintainerID != m.ID || a.Title != "Valet" || a.Version != "1.0.0" || a.ResourceVersion != 1 ||
		a.Company != "" || a.Website != "" {
		t.Errorf("created %+v, want normalized fields, empty optional fields, version 1", a)
	}
	if got := rec.Header().Get("Location"); got != "/api/v1/applications/"+a.ID {
		t.Errorf("Location = %q", got)
	}
	if got := rec.Header().Get("ETag"); got != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", got)
	}
	if got := getApplication(t, h, a.ID); !reflect.DeepEqual(got, a) {
		t.Errorf("GET = %+v, want the POST response %+v", got, a)
	}
}

func TestCreateApplicationErrors(t *testing.T) {
	h := newHandler(t)
	m := createMaintainer(t, h, "Ada", "ada@example.com")

	t.Run("missing Maintainer", func(t *testing.T) {
		rec := serve(t, h, request{method: "POST", path: "/api/v1/applications", body: appBody("missing", "Valet")})
		assertFieldProblem(t, rec, "maintainer_not_found", "the referenced maintainer does not exist",
			validation.FieldError{Field: "maintainerId", Message: "must refer to an existing maintainer"})
	})
	t.Run("invalid fields", func(t *testing.T) {
		rec := serve(t, h, request{method: "POST", path: "/api/v1/applications",
			body: `{"title":"","version":"1","website":"ftp://example.com"}`})
		assertValidationProblem(t, rec, []validation.FieldError{
			{Field: "maintainerId", Message: "is required"},
			{Field: "title", Message: "is required"},
			{Field: "website", Message: "must be an absolute http or https URL"},
		})
	})
	t.Run("invalid fields beat missing Maintainer", func(t *testing.T) {
		rec := serve(t, h, request{method: "POST", path: "/api/v1/applications",
			body: `{"maintainerId":"missing","title":"","version":"1"}`})
		assertValidationProblem(t, rec, []validation.FieldError{{Field: "title", Message: "is required"}})
	})
	for _, tt := range []struct {
		name, body, contentType, wantCode string
		wantStatus                        int
	}{
		{name: "wrong content type", body: appBody(m.ID, "V"), contentType: "text/plain", wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "server-managed field", body: `{"maintainerId":"` + m.ID + `","title":"V","version":"1","resourceVersion":3}`,
			wantStatus: 400, wantCode: "invalid_body"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, h, request{method: "POST", path: "/api/v1/applications", body: tt.body, contentType: tt.contentType})
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if p := decodeProblem(t, rec); p.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", p.Code, tt.wantCode)
			}
		})
	}
}

func assertFieldProblem(t *testing.T, rec *httptest.ResponseRecorder, code, detail string, want ...validation.FieldError) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if p := decodeProblem(t, rec); p.Code != code || p.Detail != detail || p.Title != "Unprocessable Entity" ||
		!reflect.DeepEqual(p.Errors, want) {
		t.Errorf("problem = %+v, want code %q detail %q errors %v", p, code, detail, want)
	}
}

func TestListApplications(t *testing.T) {
	h := newHandler(t)
	ada := createMaintainer(t, h, "Ada", "ada@example.com")
	bob := createMaintainer(t, h, "Bob", "bob@example.com")
	a1 := createApplication(t, h, ada.ID, "A1")
	b1 := createApplication(t, h, bob.ID, "B1")
	a2 := createApplication(t, h, ada.ID, "A2")

	list := func(t *testing.T, query string) page[model.Application] {
		t.Helper()
		rec := serve(t, h, request{method: "GET", path: "/api/v1/applications" + query})
		if rec.Code != http.StatusOK {
			t.Fatalf("list %s: %d %s", query, rec.Code, rec.Body.String())
		}
		var p page[model.Application]
		if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return p
	}
	// Expected order is (createdAt, id), as requests within one clock tick
	// get equal timestamps.
	ordered := func(apps ...model.Application) []string {
		slices.SortFunc(apps, func(x, y model.Application) int {
			if c := x.CreatedAt.Compare(y.CreatedAt); c != 0 {
				return c
			}
			return strings.Compare(x.ID, y.ID)
		})
		out := []string{}
		for _, a := range apps {
			out = append(out, a.ID)
		}
		return out
	}
	ids := func(items []model.Application) []string {
		out := []string{}
		for _, a := range items {
			out = append(out, a.ID)
		}
		return out
	}

	if got, want := ids(list(t, "").Items), ordered(a1, b1, a2); !reflect.DeepEqual(got, want) {
		t.Errorf("all = %v, want %v", got, want)
	}
	adas := ordered(a1, a2)
	if got := ids(list(t, "?maintainerId="+ada.ID).Items); !reflect.DeepEqual(got, adas) {
		t.Errorf("Ada's = %v, want %v", got, adas)
	}
	// Paging through the filtered list: two pages, no overlap, offset echoed.
	p1 := list(t, "?maintainerId="+ada.ID+"&limit=1")
	if got := ids(p1.Items); !reflect.DeepEqual(got, adas[:1]) || !p1.HasMore || p1.Limit != 1 || p1.Offset != 0 {
		t.Errorf("Ada's first page = %+v, want %v with hasMore", p1, adas[:1])
	}
	p2 := list(t, "?maintainerId="+ada.ID+"&limit=1&offset=1")
	if got := ids(p2.Items); !reflect.DeepEqual(got, adas[1:]) || p2.HasMore || p2.Limit != 1 || p2.Offset != 1 {
		t.Errorf("Ada's second page = %+v, want %v without hasMore", p2, adas[1:])
	}
	// The filter is trimmed, like the maintainerId field.
	if got := ids(list(t, "?maintainerId=%20"+ada.ID+"%20").Items); !reflect.DeepEqual(got, adas) {
		t.Errorf("padded filter = %v, want %v", got, adas)
	}
	// A Maintainer that does not exist has no Applications: empty, not 404.
	if p := list(t, "?maintainerId=unknown"); len(p.Items) != 0 || p.Items == nil || p.HasMore {
		t.Errorf("unknown Maintainer = %+v, want an empty page", p)
	}

	for _, q := range []string{"maintainerId=", "maintainerId=%20%09", "maintainerId=" + ada.ID + "&maintainerId=" + bob.ID, "limit=0"} {
		t.Run(q, func(t *testing.T) {
			rec := serve(t, h, request{method: "GET", path: "/api/v1/applications?" + q})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if p := decodeProblem(t, rec); p.Code != "invalid_query" {
				t.Errorf("code = %q, want invalid_query", p.Code)
			}
		})
	}
}

func TestGetApplicationNotFound(t *testing.T) {
	rec := serve(t, newHandler(t), request{method: "GET", path: "/api/v1/applications/missing"})
	assertProblem(t, rec, http.StatusNotFound, "not_found", "resource not found")
}

func TestUpdateApplication(t *testing.T) {
	h := newHandler(t)
	m := createMaintainer(t, h, "Ada", "ada@example.com")

	for _, tt := range []struct {
		name         string
		maintainerID string // as sent
	}{
		{name: "maintainerId omitted", maintainerID: ""},
		{name: "maintainerId unchanged", maintainerID: m.ID},
		{name: "maintainerId unchanged after trimming", maintainerID: "  " + m.ID + "\t"},
		{name: "whitespace-only maintainerId counts as omitted", maintainerID: " \t"},
		{name: "null maintainerId counts as omitted", maintainerID: "null"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := createApplication(t, h, m.ID, "Valet")
			body := `{"title":"Valet 2","version":"2.0.0","license":"Apache-2.0"}`
			switch tt.maintainerID {
			case "":
			case "null":
				body = `{"maintainerId":null,"title":"Valet 2","version":"2.0.0","license":"Apache-2.0"}`
			default:
				body = fmt.Sprintf(`{"maintainerId":%q,"title":"Valet 2","version":"2.0.0","license":"Apache-2.0"}`, tt.maintainerID)
			}
			rec := serve(t, h, request{method: "PUT", path: "/api/v1/applications/" + a.ID, ifMatch: []string{`"1"`}, body: body})
			if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
				t.Fatalf("PUT = %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
			}
			got := decodeApplication(t, rec)
			// Full replacement (DESIGN.md A9): omitted optional fields are
			// cleared; the creator is kept.
			if got.MaintainerID != m.ID || got.Title != "Valet 2" || got.License != "Apache-2.0" ||
				got.Company != "" || got.Website != "" || got.ResourceVersion != 2 {
				t.Errorf("updated = %+v", got)
			}
			if stored := getApplication(t, h, a.ID); !reflect.DeepEqual(stored, got) {
				t.Errorf("GET = %+v, want %+v", stored, got)
			}
		})
	}
}

// TestUpdateApplicationErrors covers each error and the order of DESIGN.md
// §4.2, including the stateful 422 for a changed maintainerId (step 6).
func TestUpdateApplicationErrors(t *testing.T) {
	tests := []struct {
		name       string
		missing    bool
		body       func(other string) string
		ifMatch    []string
		wantStatus int
		wantCode   string
	}{
		{name: "missing If-Match", body: func(string) string { return `{"title":"T","version":"1"}` },
			wantStatus: 428, wantCode: "if_match_required"},
		{name: "stale version", body: func(string) string { return `{"title":"T","version":"1"}` }, ifMatch: []string{`"1"`},
			wantStatus: 412, wantCode: "version_mismatch"},
		{name: "missing Application", missing: true, body: func(string) string { return `{"title":"T","version":"1"}` },
			ifMatch: []string{`"2"`}, wantStatus: 404, wantCode: "not_found"},
		{name: "changed maintainerId", body: func(other string) string { return `{"maintainerId":"` + other + `","title":"T","version":"1"}` },
			ifMatch: []string{`"2"`}, wantStatus: 422, wantCode: "maintainer_changed"},
		{name: "invalid fields", body: func(string) string { return `{"title":"","version":"1"}` }, ifMatch: []string{`"2"`},
			wantStatus: 422, wantCode: "validation_failed"},
		// Order.
		{name: "malformed body beats missing If-Match", body: func(string) string { return `{` },
			wantStatus: 400, wantCode: "invalid_body"},
		{name: "missing If-Match beats invalid fields", body: func(string) string { return `{"title":""}` },
			wantStatus: 428, wantCode: "if_match_required"},
		{name: "body too large", body: func(string) string { return `{"title":"` + strings.Repeat("a", 70000) + `"}` },
			ifMatch: []string{`"2"`}, wantStatus: 413, wantCode: "body_too_large"},
		{name: "invalid fields beat stale version", body: func(string) string { return `{"title":"","version":"1"}` },
			ifMatch: []string{`"1"`}, wantStatus: 422, wantCode: "validation_failed"},
		{name: "stale version beats changed maintainerId",
			body:    func(other string) string { return `{"maintainerId":"` + other + `","title":"T","version":"1"}` },
			ifMatch: []string{`"1"`}, wantStatus: 412, wantCode: "version_mismatch"},
		{name: "missing beats changed maintainerId", missing: true,
			body:    func(other string) string { return `{"maintainerId":"` + other + `","title":"T","version":"1"}` },
			ifMatch: []string{`"2"`}, wantStatus: 404, wantCode: "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler(t)
			ada := createMaintainer(t, h, "Ada", "ada@example.com")
			bob := createMaintainer(t, h, "Bob", "bob@example.com")
			a := createApplication(t, h, ada.ID, "Valet")
			// Bring the Application to version 2, so that "1" is stale.
			if rec := serve(t, h, request{method: "PUT", path: "/api/v1/applications/" + a.ID, ifMatch: []string{`"1"`},
				body: appBody("", "Valet")}); rec.Code != http.StatusOK {
				t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
			}
			before := getApplication(t, h, a.ID)
			id := a.ID
			if tt.missing {
				id = "missing"
			}

			rec := serve(t, h, request{method: "PUT", path: "/api/v1/applications/" + id, ifMatch: tt.ifMatch, body: tt.body(bob.ID)})
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			p := decodeProblem(t, rec)
			if p.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", p.Code, tt.wantCode)
			}
			if tt.wantCode == "maintainer_changed" && (p.Detail != "an application's maintainer cannot be changed" ||
				!reflect.DeepEqual(p.Errors,
					[]validation.FieldError{{Field: "maintainerId", Message: "must be omitted or equal the current value"}})) {
				t.Errorf("problem = %+v", p)
			}
			if after := getApplication(t, h, a.ID); !reflect.DeepEqual(after, before) {
				t.Errorf("after failed update: %+v, want unchanged %+v", after, before)
			}
		})
	}
}

func TestDeleteApplication(t *testing.T) {
	h := newHandler(t)
	m := createMaintainer(t, h, "Ada", "ada@example.com")
	a := createApplication(t, h, m.ID, "Valet")
	path := "/api/v1/applications/" + a.ID

	assertProblem(t, serve(t, h, request{method: "DELETE", path: path}),
		http.StatusPreconditionRequired, "if_match_required", "If-Match header with the current ETag is required")
	assertProblem(t, serve(t, h, request{method: "DELETE", path: path, ifMatch: []string{`"2"`}}),
		http.StatusPreconditionFailed, "version_mismatch", "If-Match does not match the current resource version")

	// The Maintainer cannot be deleted while it owns the Application.
	mPath := "/api/v1/maintainers/" + m.ID
	assertProblem(t, serve(t, h, request{method: "DELETE", path: mPath, ifMatch: []string{`"1"`}}),
		http.StatusConflict, "maintainer_in_use", "maintainer still owns applications; delete them first")

	if rec := serve(t, h, request{method: "DELETE", path: path, ifMatch: []string{`"1"`}}); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("DELETE = %d %q, want 204 with no body", rec.Code, rec.Body.String())
	}
	assertProblem(t, serve(t, h, request{method: "GET", path: path}), http.StatusNotFound, "not_found", "resource not found")
	assertProblem(t, serve(t, h, request{method: "DELETE", path: path, ifMatch: []string{`"1"`}}),
		http.StatusNotFound, "not_found", "resource not found")

	// Now it can.
	if rec := serve(t, h, request{method: "DELETE", path: mPath, ifMatch: []string{`"1"`}}); rec.Code != http.StatusNoContent {
		t.Errorf("delete Maintainer without Applications = %d %s", rec.Code, rec.Body.String())
	}
}

// A missing or malformed If-Match is reported before the lookup (DESIGN.md
// §4.2), so a missing Application gives 428 or 400, not 404.
func TestDeleteApplicationOrder(t *testing.T) {
	h := newHandler(t)
	assertProblem(t, serve(t, h, request{method: "DELETE", path: "/api/v1/applications/missing"}),
		http.StatusPreconditionRequired, "if_match_required", "If-Match header with the current ETag is required")
	assertProblem(t, serve(t, h, request{method: "DELETE", path: "/api/v1/applications/missing", ifMatch: []string{`W/"1"`}}),
		http.StatusBadRequest, "invalid_if_match", `If-Match must be a single strong ETag such as "3"`)
}

func TestApplicationRoutes(t *testing.T) {
	h := newHandler(t)
	for _, tt := range []struct {
		method, path string
		wantStatus   int
		wantAllow    string
	}{
		{"PATCH", "/api/v1/applications/x", 405, "DELETE, GET, HEAD, PUT"},
		{"DELETE", "/api/v1/applications", 405, "GET, HEAD, POST"},
		{"GET", "/api/v1/applications/x/y", 404, ""},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := serve(t, h, request{method: tt.method, path: tt.path})
			if rec.Code != tt.wantStatus || rec.Header().Get("Allow") != tt.wantAllow ||
				rec.Header().Get("Content-Type") != "application/problem+json" {
				t.Errorf("= %d Allow %q Content-Type %q", rec.Code, rec.Header().Get("Allow"), rec.Header().Get("Content-Type"))
			}
		})
	}
}

// Every Application handler passes the request context to the store
// (DESIGN.md §4.6, D9).
func TestApplicationCanceledRequests(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, req := range []request{
		{method: "GET", path: "/api/v1/applications"},
		{method: "POST", path: "/api/v1/applications", body: `{"maintainerId":"{m}","title":"T","version":"1"}`},
		{method: "GET", path: "/api/v1/applications/{id}"},
		{method: "PUT", path: "/api/v1/applications/{id}", ifMatch: []string{`"1"`}, body: `{"title":"T","version":"1"}`},
		{method: "DELETE", path: "/api/v1/applications/{id}", ifMatch: []string{`"1"`}},
	} {
		t.Run(req.method+" "+req.path, func(t *testing.T) {
			h := newHandler(t)
			m := createMaintainer(t, h, "Ada", "ada@example.com")
			a := createApplication(t, h, m.ID, "Valet")
			req.path = strings.Replace(req.path, "{id}", a.ID, 1)
			req.body = strings.Replace(req.body, "{m}", m.ID, 1)
			req.ctx = canceled
			assertProblem(t, serve(t, h, req), http.StatusServiceUnavailable, "unavailable", "request was canceled")
		})
	}
}
