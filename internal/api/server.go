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
	return mux
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
// shutting down, and not while the database is unreachable. If the request is
// cancelled, nobody is waiting for the answer, so nothing is written and the
// cancellation is not reported as a database failure.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.shuttingDown.Load() {
		writeProblem(w, http.StatusServiceUnavailable, codeNotReady, "server is shutting down")
		return
	}
	if err := s.store.Ping(r.Context()); err != nil {
		if errors.Is(err, context.Canceled) {
			log.Printf("readyz: request canceled")
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
