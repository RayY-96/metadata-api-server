// Command server runs the metadata API server.
//
// Configuration is read from the environment:
//
//	ADDR     listen address (default 127.0.0.1:8080)
//	DB_PATH  SQLite database file (default metadata.db)
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RayY-96/metadata-api-server/internal/api"
	"github.com/RayY-96/metadata-api-server/internal/store"
)

const (
	defaultAddr   = "127.0.0.1:8080"
	defaultDBPath = "metadata.db"

	// drainTimeout bounds how long shutdown waits for in-flight requests
	// (DESIGN.md §7).
	drainTimeout = 10 * time.Second
)

type config struct {
	addr   string
	dbPath string
}

func loadConfig(getenv func(string) string) config {
	cfg := config{addr: getenv("ADDR"), dbPath: getenv("DB_PATH")}
	if cfg.addr == "" {
		cfg.addr = defaultAddr
	}
	if cfg.dbPath == "" {
		cfg.dbPath = defaultDBPath
	}
	return cfg
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// Restore default signal handling as soon as shutdown begins, so a second
	// Ctrl+C or SIGTERM during the drain terminates the process immediately.
	context.AfterFunc(ctx, stop)
	err := run(ctx, os.Getenv, store.Open)
	stop()
	if err != nil {
		log.Printf("server: %v", err)
		os.Exit(1)
	}
}

// run serves until ctx is cancelled, then shuts down gracefully. The database
// is closed after serve returns, that is after the HTTP server has drained or
// been force-closed. main passes store.Open as openStore; tests pass a wrapper
// to inspect the store afterwards.
func run(ctx context.Context, getenv func(string) string, openStore func(string) (*store.Store, error)) (err error) {
	cfg := loadConfig(getenv)

	st, err := openStore(cfg.dbPath)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close database: %w", cerr))
		}
	}()

	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.addr, err)
	}
	log.Printf("listening on %s", ln.Addr())

	h, beforeShutdown := newHandler(st)
	return serve(ctx, newHTTPServer(h), ln, drainTimeout, beforeShutdown)
}

// newHandler returns the API handler and a hook for the start of shutdown: it
// logs and makes /readyz return 503. serve calls it before closing the
// listener.
func newHandler(st *store.Store) (http.Handler, func()) {
	a := api.NewServer(st)
	return a.Handler(), func() {
		log.Printf("shutting down")
		a.MarkShuttingDown()
	}
}

// newHTTPServer applies the server timeouts of DESIGN.md §7.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// serve runs srv on ln until ctx is cancelled or Serve fails.
//
// On cancellation it calls beforeShutdown, then Shutdown, which closes the
// listener and waits up to drain for in-flight requests. If the drain does not
// finish in time, the remaining connections are force-closed and an error is
// returned.
//
// If Serve fails on its own, it has closed only the listener; the connections
// it already accepted are force-closed too, so that the caller does not close
// the database under them.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration, beforeShutdown func()) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return closeServer(srv, fmt.Errorf("serve: %w", err))
	case <-ctx.Done():
	}

	beforeShutdown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return closeServer(srv, fmt.Errorf("drain in-flight requests: %w", err))
		}
		// Any other error comes from closing the listener; Shutdown has still
		// waited for the drain to finish.
		return fmt.Errorf("shutdown: %w", err)
	}

	// Serve returns ErrServerClosed as soon as Shutdown begins.
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// closeServer force-closes all connections of srv and returns err, joined
// with any error from closing.
func closeServer(srv *http.Server, err error) error {
	if cerr := srv.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("close server: %w", cerr))
	}
	return err
}
