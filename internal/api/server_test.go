package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func assertProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code, detail string) {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var p problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem body %q: %v", rec.Body.String(), err)
	}
	want := problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Code:   code,
		Detail: detail,
	}
	if p != want {
		t.Errorf("problem = %+v, want %+v", p, want)
	}
}
