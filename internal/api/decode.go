package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
)

// maxBodyBytes is the request body limit (DESIGN.md §7).
const maxBodyBytes = 64 << 10

// unknownFieldPrefix starts the error encoding/json returns for an unknown
// field; it has no typed error for this case.
const unknownFieldPrefix = "json: unknown field "

// decodeJSON decodes the request body into dst, which must be a non-nil
// pointer to a request struct. It applies the body checks of DESIGN.md §4.2
// step 1 in order: content type (415), size (413), then syntax (400: empty,
// malformed, wrong type, unknown field, trailing data).
//
// The body is buffered up to the limit before it is parsed, so any body over
// the limit is a 413, however malformed it is.
//
// On failure it writes the error response and returns false; the caller must
// then return without writing anything else.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblem(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType,
			"Content-Type must be application/json")
		return false
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, http.StatusRequestEntityTooLarge, codeBodyTooLarge,
				fmt.Sprintf("request body must not exceed %d bytes", maxBodyBytes))
			return false
		}
		// Only a cancellation is the client's doing; an expired request
		// deadline is logged as a plain read error.
		if errors.Is(r.Context().Err(), context.Canceled) {
			log.Printf("read request body: request canceled: %v", err)
		} else {
			log.Printf("read request body: %v", err)
		}
		// Answer even if the request context is cancelled: after the read
		// deadline fires the client may still be reading, and writing nothing
		// would send net/http's implicit 200 (DESIGN.md D9).
		writeProblem(w, http.StatusBadRequest, codeInvalidBody, "request body could not be read")
		return false
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeDecodeError(w, err)
		return false
	}
	// Only whitespace may follow the value.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeProblem(w, http.StatusBadRequest, codeInvalidBody,
			"request body must contain a single JSON value")
		return false
	}
	return true
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var (
		wrongType *json.UnmarshalTypeError
		invalid   *json.InvalidUnmarshalError
	)
	switch {
	case errors.Is(err, io.EOF):
		writeProblem(w, http.StatusBadRequest, codeInvalidBody, "request body must not be empty")
	case errors.As(err, &wrongType) && wrongType.Field == "":
		writeProblem(w, http.StatusBadRequest, codeInvalidBody, "request body must be a JSON object")
	case errors.As(err, &wrongType):
		writeProblem(w, http.StatusBadRequest, codeInvalidBody,
			fmt.Sprintf("field %q has the wrong type", wrongType.Field))
	case errors.As(err, &invalid):
		// dst is nil or not a pointer: a programming error, not a client
		// error. Decode reports it only after reading a well-formed value.
		log.Printf("decode request body: %v", err)
		writeProblem(w, http.StatusInternalServerError, codeInternal, "internal server error")
	case strings.HasPrefix(err.Error(), unknownFieldPrefix):
		// Only the detail depends on the message; the status is 400 either way.
		writeProblem(w, http.StatusBadRequest, codeInvalidBody,
			"request body contains unknown field "+strings.TrimPrefix(err.Error(), unknownFieldPrefix))
	default:
		// Syntax errors and truncated bodies.
		writeProblem(w, http.StatusBadRequest, codeInvalidBody, "request body is not valid JSON")
	}
}
