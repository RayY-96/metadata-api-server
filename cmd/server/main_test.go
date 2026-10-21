package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RayY-96/metadata-api-server/internal/store"
)

func TestLoadConfig(t *testing.T) {
	// Literal values, not the constants: the loopback default is a documented
	// safety property (DESIGN.md §7).
	defaults := config{addr: "127.0.0.1:8080", dbPath: "metadata.db"}
	tests := []struct {
		name string
		env  map[string]string
		want config
	}{
		{name: "defaults", env: nil, want: defaults},
		{name: "empty values use defaults", env: map[string]string{"ADDR": "", "DB_PATH": ""}, want: defaults},
		{name: "overrides", env: map[string]string{"ADDR": ":9090", "DB_PATH": "/tmp/x.db"},
			want: config{addr: ":9090", dbPath: "/tmp/x.db"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loadConfig(func(k string) string { return tt.env[k] })
			if got != tt.want {
				t.Errorf("loadConfig = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Behavior tests of these limits would need real waiting, so the configured
// values are checked against DESIGN.md §7 instead.
func TestTimeouts(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler())
	for name, tc := range map[string]struct{ got, want time.Duration }{
		"ReadHeaderTimeout": {srv.ReadHeaderTimeout, 5 * time.Second},
		"ReadTimeout":       {srv.ReadTimeout, 10 * time.Second},
		"WriteTimeout":      {srv.WriteTimeout, 15 * time.Second},
		"IdleTimeout":       {srv.IdleTimeout, 60 * time.Second},
		"drainTimeout":      {drainTimeout, 10 * time.Second},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", name, tc.got, tc.want)
		}
	}
}

// receive waits for a value from ch. The timeout only keeps a broken test from
// hanging until the go test deadline; no assertion depends on it.
func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

type result struct {
	status int
	err    error
}

type blockedServer struct {
	release chan struct{} // closing it lets the handler respond
	resp    <-chan result // outcome of the client request
	done    <-chan error  // return value of serve
}

// startBlockedRequest starts serve on ln with a handler that blocks until
// release is closed, sends one request and waits until the handler is running.
// onShutdown is registered with srv.RegisterOnShutdown, so it runs once
// Shutdown has begun (after the listener is closed).
func startBlockedRequest(t *testing.T, ctx context.Context, ln net.Listener, drain time.Duration, beforeShutdown, onShutdown func()) blockedServer {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	})

	srv := newHTTPServer(h)
	srv.RegisterOnShutdown(onShutdown)
	done := make(chan error, 1)
	go func() { done <- serve(ctx, srv, ln, drain, beforeShutdown) }()

	resp := make(chan result, 1)
	go func() {
		r, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			resp <- result{err: err}
			return
		}
		r.Body.Close()
		resp <- result{status: r.StatusCode}
	}()

	select {
	case <-entered:
	case r := <-resp:
		t.Fatalf("request ended before reaching the handler: %+v", r)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the handler")
	}
	return blockedServer{release: release, resp: resp, done: done}
}

func TestServeDrainsInFlightRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln := listen(t)
	addr := ln.Addr().String()

	// Both are written before shutdownBegan is closed and read after it.
	hookRan, hookRanFirst := false, false
	shutdownBegan := make(chan struct{})
	bs := startBlockedRequest(t, ctx, ln, 10*time.Second,
		func() { hookRan = true },
		func() {
			hookRanFirst = hookRan
			close(shutdownBegan)
		})

	cancel()
	// Release the handler only once Shutdown is in progress, so the request
	// succeeds only if Shutdown waits for it.
	select {
	case <-shutdownBegan:
	case err := <-bs.done:
		t.Fatalf("serve returned %v before Shutdown began", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Shutdown to begin")
	}
	if !hookRanFirst {
		t.Error("beforeShutdown did not run before Shutdown began")
	}
	close(bs.release)

	if r := receive(t, bs.resp, "response"); r.err != nil || r.status != http.StatusOK {
		t.Errorf("in-flight request = %+v, want status 200", r)
	}
	if err := receive(t, bs.done, "serve"); err != nil {
		t.Errorf("serve = %v, want nil", err)
	}
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Error("server still accepts connections after shutdown")
	}
}

func TestServeDrainTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bs := startBlockedRequest(t, ctx, listen(t), 50*time.Millisecond, func() {}, func() {})
	defer close(bs.release)

	cancel()

	if err := receive(t, bs.done, "serve"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("serve = %v, want context.DeadlineExceeded", err)
	}
	if r := receive(t, bs.resp, "response"); r.err == nil {
		t.Errorf("in-flight request = %+v, want connection error after forced close", r)
	}
}

// failingListener accepts one connection and then fails with errAccept once
// fail is closed, as a listener does when it breaks.
type failingListener struct {
	net.Listener
	accepted bool // only touched by the Serve goroutine
	fail     chan struct{}
}

var errAccept = errors.New("accept failed")

func (l *failingListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.Listener.Accept()
	}
	<-l.fail
	return nil, errAccept
}

func TestServeClosesConnectionsWhenServeFails(t *testing.T) {
	ln := &failingListener{Listener: listen(t), fail: make(chan struct{})}
	hookRan := false
	bs := startBlockedRequest(t, context.Background(), ln, 10*time.Second, func() { hookRan = true }, func() {})
	defer close(bs.release)

	close(ln.fail)

	if err := receive(t, bs.done, "serve"); !errors.Is(err, errAccept) {
		t.Errorf("serve = %v, want errAccept", err)
	}
	if hookRan {
		t.Error("beforeShutdown ran although no shutdown was requested")
	}
	// The handler is still blocked, so the request can only end because serve
	// closed its connection.
	if r := receive(t, bs.resp, "response"); r.err == nil {
		t.Errorf("in-flight request = %+v, want connection error after forced close", r)
	}
}

// recordingOpen returns a store.Open wrapper that records the store and error.
func recordingOpen(st **store.Store, openErr *error) func(string) (*store.Store, error) {
	return func(path string) (*store.Store, error) {
		*st, *openErr = store.Open(path)
		return *st, *openErr
	}
}

func TestRunShutsDownAndClosesDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	env := map[string]string{"ADDR": "127.0.0.1:0", "DB_PATH": dbPath}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // shut down immediately after startup

	var st *store.Store
	var openErr error
	if err := run(ctx, func(k string) string { return env[k] }, recordingOpen(&st, &openErr)); err != nil {
		t.Fatalf("run = %v, want nil", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("database file not created: %v", err)
	}
	// Ping on an open, healthy store succeeds, so an error means run closed it.
	if err := st.Ping(context.Background()); err == nil {
		t.Error("Ping after run succeeded, want error from closed database")
	}
}

func TestRunClosesDatabaseWhenListenFails(t *testing.T) {
	occupied := listen(t)
	defer occupied.Close()
	env := map[string]string{
		"ADDR":    occupied.Addr().String(),
		"DB_PATH": filepath.Join(t.TempDir(), "test.db"),
	}

	var st *store.Store
	var openErr error
	if err := run(context.Background(), func(k string) string { return env[k] }, recordingOpen(&st, &openErr)); err == nil {
		t.Fatal("run succeeded on an occupied address, want error")
	}
	if st == nil {
		t.Fatal("run did not open the database")
	}
	if err := st.Ping(context.Background()); err == nil {
		t.Error("Ping after run succeeded, want error from closed database")
	}
}

func TestRunFailsForBadDatabasePath(t *testing.T) {
	env := map[string]string{
		"ADDR":    "127.0.0.1:0",
		"DB_PATH": filepath.Join(t.TempDir(), "missing", "test.db"),
	}
	var st *store.Store
	var openErr error
	err := run(context.Background(), func(k string) string { return env[k] }, recordingOpen(&st, &openErr))
	if openErr == nil {
		t.Fatal("store.Open succeeded, want error")
	}
	if !errors.Is(err, openErr) {
		t.Errorf("run = %v, want the error from store.Open", err)
	}
}

func TestShutdownHookMarksNotReady(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	h, beforeShutdown := newHandler(st)

	readyz := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	if got := readyz(); got != http.StatusOK {
		t.Fatalf("readyz before hook = %d, want 200", got)
	}
	beforeShutdown()
	if got := readyz(); got != http.StatusServiceUnavailable {
		t.Errorf("readyz after hook = %d, want 503", got)
	}
	// In-flight requests still need the database during the drain.
	if err := st.Ping(context.Background()); err != nil {
		t.Errorf("hook closed the database: %v", err)
	}
}
