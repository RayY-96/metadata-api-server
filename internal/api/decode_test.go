package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
)

type testRequest struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// sizeFrame is a testRequest body without the name's content.
const sizeFrame = `{"name":""}`

// bodyOfSize returns a valid testRequest body of exactly n bytes.
func bodyOfSize(n int) string {
	return `{"name":"` + strings.Repeat("a", n-len(sizeFrame)) + `"}`
}

func TestDecodeJSON(t *testing.T) {
	const jsonType = "application/json"
	tests := []struct {
		name        string
		contentType string
		body        string
		wantStatus  int // 0: decoding succeeds and want is decoded
		wantCode    string
		wantDetail  string
		want        testRequest
	}{
		{name: "valid", contentType: jsonType, body: `{"name":"x","count":2}`,
			want: testRequest{Name: "x", Count: 2}},
		{name: "media type parameters allowed", contentType: "application/json; charset=utf-8", body: `{"name":"x"}`,
			want: testRequest{Name: "x"}},
		{name: "media type is case-insensitive", contentType: "Application/JSON", body: `{"name":"x"}`,
			want: testRequest{Name: "x"}},
		{name: "trailing whitespace allowed", contentType: jsonType, body: "{\"name\":\"x\"}\n\t ",
			want: testRequest{Name: "x"}},
		{name: "body at the limit", contentType: jsonType, body: bodyOfSize(65536),
			want: testRequest{Name: strings.Repeat("a", 65536-len(sizeFrame))}},
		// Standard encoding/json behavior, recorded in DESIGN.md §4.1.
		{name: "field names match case-insensitively", contentType: jsonType, body: `{"NAME":"x"}`,
			want: testRequest{Name: "x"}},
		{name: "duplicate key: last wins", contentType: jsonType, body: `{"name":"a","name":"b"}`,
			want: testRequest{Name: "b"}},
		{name: "invalid UTF-8 is replaced", contentType: jsonType, body: "{\"name\":\"\xff\"}",
			want: testRequest{Name: "\uFFFD"}},
		{name: "null decodes to the zero value", contentType: jsonType, body: `null`,
			want: testRequest{}},

		{name: "missing content type", contentType: "", body: `{"name":"x"}`,
			wantStatus: 415, wantCode: "unsupported_media_type", wantDetail: "Content-Type must be application/json"},
		{name: "wrong content type", contentType: "text/plain", body: `{"name":"x"}`,
			wantStatus: 415, wantCode: "unsupported_media_type", wantDetail: "Content-Type must be application/json"},
		{name: "malformed content type", contentType: "application/json; =", body: `{"name":"x"}`,
			wantStatus: 415, wantCode: "unsupported_media_type", wantDetail: "Content-Type must be application/json"},
		{name: "problem+json is a different media type", contentType: "application/problem+json", body: `{"name":"x"}`,
			wantStatus: 415, wantCode: "unsupported_media_type", wantDetail: "Content-Type must be application/json"},
		{name: "json suffix is a different media type", contentType: "application/merge-patch+json", body: `{"name":"x"}`,
			wantStatus: 415, wantCode: "unsupported_media_type", wantDetail: "Content-Type must be application/json"},
		{name: "text/json is a different media type", contentType: "text/json", body: `{"name":"x"}`,
			wantStatus: 415, wantCode: "unsupported_media_type", wantDetail: "Content-Type must be application/json"},
		{name: "content type checked before size", contentType: "text/plain", body: bodyOfSize(65537),
			wantStatus: 415, wantCode: "unsupported_media_type", wantDetail: "Content-Type must be application/json"},

		{name: "body one byte over the limit", contentType: jsonType, body: bodyOfSize(65537),
			wantStatus: 413, wantCode: "body_too_large", wantDetail: "request body must not exceed 65536 bytes"},
		{name: "size checked before syntax", contentType: jsonType, body: strings.Repeat("x", 65537),
			wantStatus: 413, wantCode: "body_too_large", wantDetail: "request body must not exceed 65536 bytes"},

		{name: "empty body", contentType: jsonType, body: "",
			wantStatus: 400, wantCode: "invalid_body", wantDetail: "request body must not be empty"},
		{name: "whitespace only", contentType: jsonType, body: "  \n",
			wantStatus: 400, wantCode: "invalid_body", wantDetail: "request body must not be empty"},
		{name: "syntax error", contentType: jsonType, body: `{"name":}`,
			wantStatus: 400, wantCode: "invalid_body", wantDetail: "request body is not valid JSON"},
		{name: "truncated", contentType: jsonType, body: `{"name":"x"`,
			wantStatus: 400, wantCode: "invalid_body", wantDetail: "request body is not valid JSON"},
		{name: "wrong field type", contentType: jsonType, body: `{"count":"two"}`,
			wantStatus: 400, wantCode: "invalid_body", wantDetail: `field "count" has the wrong type`},
		{name: "array instead of object", contentType: jsonType, body: `[{"name":"x"}]`,
			wantStatus: 400, wantCode: "invalid_body", wantDetail: "request body must be a JSON object"},
		{name: "string instead of object", contentType: jsonType, body: `"x"`,
			wantStatus: 400, wantCode: "invalid_body", wantDetail: "request body must be a JSON object"},
		{name: "unknown field", contentType: jsonType, body: `{"name":"x","id":"123"}`,
			wantStatus: 400, wantCode: "invalid_body", wantDetail: `request body contains unknown field "id"`},
		{name: "trailing object", contentType: jsonType, body: `{"name":"x"}{"name":"y"}`,
			wantStatus: 400, wantCode: "invalid_body", wantDetail: "request body must contain a single JSON value"},
		{name: "trailing garbage", contentType: jsonType, body: `{"name":"x"} x`,
			wantStatus: 400, wantCode: "invalid_body", wantDetail: "request body must contain a single JSON value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()

			var got testRequest
			ok := decodeJSON(rec, req, &got)

			if tt.wantStatus == 0 {
				if !ok {
					t.Fatalf("decodeJSON failed: %d %s", rec.Code, rec.Body.String())
				}
				if got != tt.want {
					t.Errorf("decoded %+v, want %+v", got, tt.want)
				}
				// The caller writes the response; decodeJSON must not.
				assertNothingWritten(t, rec)
				return
			}
			if ok {
				t.Fatalf("decodeJSON succeeded, want status %d", tt.wantStatus)
			}
			assertProblem(t, rec, tt.wantStatus, tt.wantCode, tt.wantDetail)
		})
	}
}

func TestDecodeJSONNonPointerIsInternalError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	if decodeJSON(rec, req, testRequest{}) {
		t.Fatal("decodeJSON succeeded with a non-pointer destination")
	}
	assertProblem(t, rec, http.StatusInternalServerError, "internal_error", "internal server error")
}

func assertNothingWritten(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 || rec.Flushed {
		t.Errorf("wrote headers %v and body %q, want nothing", rec.Header(), rec.Body.String())
	}
}

func TestDecodeJSONReadErrors(t *testing.T) {
	readErr := errors.New("connection reset by peer")
	newRequest := func(ctx context.Context) *http.Request {
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", io.NopCloser(iotest.ErrReader(readErr)))
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	t.Run("client still connected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		if decodeJSON(rec, newRequest(context.Background()), &testRequest{}) {
			t.Fatal("decodeJSON succeeded, want read error")
		}
		// The detail is generic: the read error is logged, never returned.
		assertProblem(t, rec, http.StatusBadRequest, "invalid_body", "request body could not be read")
	})

	// A cancelled request context does not mean nobody reads the response
	// (see TestDecodeJSONIncompleteBodyIsNotSuccess), so it is still answered.
	t.Run("request context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := httptest.NewRecorder()
		if decodeJSON(rec, newRequest(ctx), &testRequest{}) {
			t.Fatal("decodeJSON succeeded, want read error")
		}
		assertProblem(t, rec, http.StatusBadRequest, "invalid_body", "request body could not be read")
	})
}
