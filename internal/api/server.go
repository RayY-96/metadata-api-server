package api

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"sync/atomic"

	"github.com/RayY-96/metadata-api-server/internal/store"
)

// Server holds the dependencies and state shared by the HTTP handlers.
type Server struct {
	store *store.Store

	// shuttingDown is set once graceful shutdown begins. Handlers read it
	// concurrently, so it is atomic.
	shuttingDown atomic.Bool
}

// NewServer returns a Server that serves requests from st.
func NewServer(st *store.Store) *Server {
	return &Server{store: st}
}

// Handler returns the HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", s.livez)
	mux.HandleFunc("GET /readyz", s.readyz)

	mux.HandleFunc("POST "+maintainersPath, s.createMaintainer)
	mux.HandleFunc("GET "+maintainersPath, s.listMaintainers)
	mux.HandleFunc("GET "+maintainersPath+"/{id}", s.getMaintainer)
	mux.HandleFunc("PUT "+maintainersPath+"/{id}", s.updateMaintainer)
	mux.HandleFunc("DELETE "+maintainersPath+"/{id}", s.deleteMaintainer)
	return routeErrors(mux)
}

// routeErrors replaces the ServeMux's plain-text 404 and 405 responses with
// Problem Details (DESIGN.md §4.1). A catch-all "/" route cannot do this: it
// would also match known paths with an unsupported method and turn 405 into 404.
func routeErrors(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern == "" {
			// No route matched. Run the mux's fallback handler against a
			// recorder and keep only its status and, for 405, its Allow
			// header; its plain-text headers and body are discarded.
			rec := &headerRecorder{header: http.Header{}}
			h.ServeHTTP(rec, r)
			switch {
			case rec.status == http.StatusMethodNotAllowed:
				w.Header().Set("Allow", rec.header.Get("Allow"))
				writeProblem(w, http.StatusMethodNotAllowed, codeMethodNotAllowed,
					"method is not allowed for this path")
				return
			// A redirect to the cleaned path (e.g. /a/../livez -> /livez) is
			// not an error; the mux sends it below. The "*" request target
			// also yields a redirect, but the mux would answer it with a
			// bodiless 400, so it is a 404 here.
			case rec.status >= 300 && rec.status < 400 && r.RequestURI != "*":
			default:
				writeProblem(w, http.StatusNotFound, codeNotFound, "no resource matches this path")
				return
			}
		}
		// The mux, not h, serves matched routes: it sets r.Pattern and the
		// path values.
		mux.ServeHTTP(w, r)
	})
}

// headerRecorder records the status and headers a handler writes and discards
// the body.
type headerRecorder struct {
	header http.Header
	status int
}

func (rec *headerRecorder) Header() http.Header { return rec.header }

func (rec *headerRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return len(b), nil
}

func (rec *headerRecorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
}

// MarkShuttingDown makes /readyz report 503 from now on (DESIGN.md §7).
func (s *Server) MarkShuttingDown() {
	s.shuttingDown.Store(true)
}

// livez reports that the process is serving HTTP. It checks no dependencies,
// so a database outage cannot turn into a restart loop (DESIGN.md §4.1).
func (s *Server) livez(w http.ResponseWriter, r *http.Request) {
	writeOK(w)
}

// readyz reports whether the server should receive traffic: not while it is
// shutting down, and not while the database is unreachable. Every failure is
// answered with 503: writing nothing would send net/http's implicit 200,
// which a probe would read as ready (DESIGN.md D9).
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.shuttingDown.Load() {
		writeProblem(w, http.StatusServiceUnavailable, codeNotReady, "server is shutting down")
		return
	}
	if err := s.store.Ping(r.Context()); err != nil {
		// The driver does not always wrap context.Canceled, so the request
		// context is checked too.
		if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
			// A client cancellation, not a database failure.
			log.Printf("readyz: request canceled")
			writeProblem(w, http.StatusServiceUnavailable, codeNotReady, "request was canceled")
			return
		}
		log.Printf("readyz: database ping failed: %v", err)
		writeProblem(w, http.StatusServiceUnavailable, codeNotReady, "database is unreachable")
		return
	}
	writeOK(w)
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, "ok"); err != nil {
		log.Printf("write response: %v", err)
	}
}
