package api

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/RayY-96/metadata-api-server/internal/store"
)

const defaultPageSize = 20

// etag formats a resource version as a strong entity tag (DESIGN.md §4.4).
func etag(version int64) string {
	return `"` + strconv.FormatInt(version, 10) + `"`
}

// parseIfMatch returns the resource version in the If-Match header. It must
// be exactly one strong entity tag holding a positive integer in the form the
// server sends ("3"); a missing header is 428, anything else 400 (DESIGN.md
// §4.4). On failure it writes the response and returns false.
func parseIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		writeProblem(w, http.StatusPreconditionRequired, codeIfMatchRequired,
			"If-Match header with the current ETag is required")
		return 0, false
	}
	v := strings.TrimSpace(values[0])
	digits, hasOpen := strings.CutPrefix(v, `"`)
	digits, hasClose := strings.CutSuffix(digits, `"`)
	version, err := strconv.ParseInt(digits, 10, 64) // fails when out of range
	if len(values) > 1 || !hasOpen || !hasClose || !isPositiveInteger(digits) || err != nil {
		writeProblem(w, http.StatusBadRequest, codeInvalidIfMatch,
			`If-Match must be a single strong ETag such as "3"`)
		return 0, false
	}
	return version, true
}

// isPositiveInteger reports whether s is a decimal positive integer without
// sign or leading zeros.
func isPositiveInteger(s string) bool {
	return isDigits(s) && s[0] != '0'
}

// isDigits reports whether s is a non-empty string of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parsePage parses the query string and reads the limit and offset
// parameters (DESIGN.md §4.5); it returns the parsed query for any other
// parameters. Values out of range are rejected, not clamped. On failure it
// writes the response and returns false.
func parsePage(w http.ResponseWriter, r *http.Request) (q url.Values, limit, offset int, ok bool) {
	// r.URL.Query() silently drops pairs it cannot parse (bad escapes, ';'),
	// which would turn "limit=%zz" into the default; reject them instead.
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, codeInvalidQuery, "query string is malformed")
		return nil, 0, 0, false
	}
	limit, ok = intParam(w, q["limit"], defaultPageSize, 1, store.MaxPageSize,
		"limit must be an integer from 1 to "+strconv.Itoa(store.MaxPageSize))
	if !ok {
		return nil, 0, 0, false
	}
	offset, ok = intParam(w, q["offset"], 0, 0, math.MaxInt, "offset must be a non-negative integer")
	if !ok {
		return nil, 0, 0, false
	}
	return q, limit, offset, true
}

// intParam parses an optional query parameter given at most once, written as
// plain digits (no sign). A missing parameter is def; anything else outside
// [minVal, maxVal] is 400 with detail.
func intParam(w http.ResponseWriter, values []string, def, minVal, maxVal int, detail string) (int, bool) {
	if len(values) == 0 {
		return def, true
	}
	n, err := strconv.Atoi(values[0])
	if len(values) > 1 || !isDigits(values[0]) || err != nil || n < minVal || n > maxVal {
		writeProblem(w, http.StatusBadRequest, codeInvalidQuery, detail)
		return 0, false
	}
	return n, true
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("%s %q: write response: %v", r.Method, r.URL.Path, err)
	}
}
