package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RayY-96/metadata-api-server/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		// Some cases close the store early; Store.Close is idempotent because
		// sql.DB.Close is.
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return st
}

func TestProbes(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		dbClosed     bool
		shuttingDown bool
		wantStatus   int
		wantDetail   string
	}{
		{name: "livez healthy", path: "/livez", wantStatus: http.StatusOK},
		{name: "livez ignores database", path: "/livez", dbClosed: true, wantStatus: http.StatusOK},
		{name: "livez during shutdown", path: "/livez", shuttingDown: true, wantStatus: http.StatusOK},
		{name: "readyz healthy", path: "/readyz", wantStatus: http.StatusOK},
		{name: "readyz database unreachable", path: "/readyz", dbClosed: true,
			wantStatus: http.StatusServiceUnavailable, wantDetail: "database is unreachable"},
		{name: "readyz during shutdown", path: "/readyz", shuttingDown: true,
			wantStatus: http.StatusServiceUnavailable, wantDetail: "server is shutting down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openStore(t)
			s := NewServer(st)
			if tt.dbClosed {
				if err := st.Close(); err != nil {
					t.Fatalf("close store: %v", err)
				}
			}
			if tt.shuttingDown {
				s.MarkShuttingDown()
			}

			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK {
				if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
					t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", got)
				}
				if got := rec.Body.String(); got != "ok" {
					t.Errorf("body = %q, want %q", got, "ok")
				}
				return
			}
			// The literal, not codeNotReady: the code is part of the public contract.
			assertProblem(t, rec, tt.wantStatus, "not_ready", tt.wantDetail)
		})
	}
}

// decodeProblem checks the Problem Details content type and decodes the body,
// rejecting fields that are not part of the format.
func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) problem {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var p problem
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("decode problem body %q: %v", rec.Body.String(), err)
	}
	// A second value means the response was written twice.
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		t.Errorf("problem body %q contains more than one JSON value", rec.Body.String())
	}
	return p
}

// problemTitles are the literal titles of the contract, so that a change in
// http.StatusText cannot change them unnoticed.
var problemTitles = map[int]string{
	400: "Bad Request",
	404: "Not Found",
	405: "Method Not Allowed",
	409: "Conflict",
	412: "Precondition Failed",
	413: "Request Entity Too Large",
	415: "Unsupported Media Type",
	422: "Unprocessable Entity",
	428: "Precondition Required",
	500: "Internal Server Error",
	503: "Service Unavailable",
}

func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code, detail string) {
	t.Helper()
	if rec.Code != status {
		t.Errorf("status = %d, want %d", rec.Code, status)
	}
	title, ok := problemTitles[status]
	if !ok {
		t.Fatalf("no literal title for status %d", status)
	}
	p := decodeProblem(t, rec)
	want := problem{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Code:   code,
		Detail: detail,
	}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("problem = %+v, want %+v", p, want)
	}
}

func TestRouteErrors(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantAllow  string
		wantCode   string
		wantDetail string
	}{
		{name: "unknown path", method: http.MethodGet, path: "/nope",
			wantStatus: 404, wantCode: "not_found", wantDetail: "no resource matches this path"},
		{name: "trailing slash is a different path", method: http.MethodGet, path: "/livez/",
			wantStatus: 404, wantCode: "not_found", wantDetail: "no resource matches this path"},
		{name: "asterisk request target", method: http.MethodGet, path: "*",
			wantStatus: 404, wantCode: "not_found", wantDetail: "no resource matches this path"},
		{name: "unsupported method", method: http.MethodPost, path: "/livez",
			wantStatus: 405, wantAllow: "GET, HEAD", wantCode: "method_not_allowed",
			wantDetail: "method is not allowed for this path"},
		{name: "unsupported method on readyz", method: http.MethodDelete, path: "/readyz",
			wantStatus: 405, wantAllow: "GET, HEAD", wantCode: "method_not_allowed",
			wantDetail: "method is not allowed for this path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewServer(openStore(t)).Handler().ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			assertProblem(t, rec, tt.wantStatus, tt.wantCode, tt.wantDetail)
			allow := rec.Header().Values("Allow")
			switch {
			case tt.wantAllow == "" && len(allow) != 0:
				t.Errorf("Allow = %q, want no Allow header", allow)
			case tt.wantAllow != "" && (len(allow) != 1 || allow[0] != tt.wantAllow):
				t.Errorf("Allow = %q, want %q", allow, tt.wantAllow)
			}
		})
	}
}

func TestMatchedRoutesPassThrough(t *testing.T) {
	h := NewServer(openStore(t)).Handler()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/livez", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Errorf("%s /livez = %d %q, want 200 \"ok\"", method, rec.Code, rec.Body.String())
		}
	}
}

// The mux redirects unclean paths to their clean form, whether or not the
// clean path supports the method; the redirect is passed through.
func TestUncleanPathsRedirect(t *testing.T) {
	h := NewServer(openStore(t)).Handler()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/a/../livez", nil))
		if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/livez" {
			t.Errorf("%s /a/../livez = %d Location %q, want 307 to /livez",
				method, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// Writing nothing would send an implicit 200, which a probe reads as ready.
func TestReadyzCanceledRequestIsNotReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", nil)
	NewServer(openStore(t)).Handler().ServeHTTP(rec, req)

	assertProblem(t, rec, http.StatusServiceUnavailable, "not_ready", "request was canceled")
}
