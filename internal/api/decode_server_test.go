package api

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDecodeJSONIncompleteBodyIsNotSuccess sends only part of a declared body.
// net/http then cancels the request context, but the client is still there
// and reads the response, so the response must be an error, never net/http's
// implicit 200 (DESIGN.md D9).
func TestDecodeJSONIncompleteBodyIsNotSuccess(t *testing.T) {
	tests := []struct {
		name string
		// stop ends the client's sending after the partial body.
		stop func(t *testing.T, conn *net.TCPConn)
	}{
		{name: "read deadline fires", stop: func(t *testing.T, conn *net.TCPConn) {}},
		{name: "client half-closes", stop: func(t *testing.T, conn *net.TCPConn) {
			if err := conn.CloseWrite(); err != nil {
				t.Fatalf("close write: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctxErr := make(chan error, 1)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req testRequest
				ok := decodeJSON(w, r, &req)
				ctxErr <- r.Context().Err()
				if ok {
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			// The body read deadline is the mechanism under test, not a race:
			// without it the server would wait for the rest of the body
			// forever. The header deadline is generous so that a slow runner
			// cannot time out before the request line arrives.
			srv.Config.ReadHeaderTimeout = 10 * time.Second
			srv.Config.ReadTimeout = 200 * time.Millisecond
			srv.Start()
			defer srv.Close()

			c, err := net.Dial("tcp", srv.Listener.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			conn := c.(*net.TCPConn)
			defer conn.Close()
			// Guard against hanging if the server never answers.
			if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatalf("set deadline: %v", err)
			}
			_, err = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: test\r\n"+
				"Content-Type: application/json\r\nContent-Length: 1000\r\n\r\n"+`{"name":`)
			if err != nil {
				t.Fatalf("write request: %v", err)
			}
			tt.stop(t, conn)

			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatalf("read response: %v (want an error response)", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for an incomplete body", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); got != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json", got)
			}
			var p problem
			if err := json.NewDecoder(resp.Body).Decode(&p); err != nil || p.Code != "invalid_body" {
				t.Errorf("problem = %+v (decode error %v), want code invalid_body", p, err)
			}
			// The scenario only matters because the context is cancelled; if
			// net/http stops doing that, this test no longer covers D9.
			select {
			case err := <-ctxErr:
				if err == nil {
					t.Error("request context was not cancelled; the test no longer exercises D9")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("handler did not run")
			}
		})
	}
}
