// Package api implements the HTTP handlers of the metadata API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/RayY-96/metadata-api-server/internal/store"
	"github.com/RayY-96/metadata-api-server/internal/validation"
)

// Stable machine-readable error codes (DESIGN.md §4.3).
const (
	codeInvalidBody          = "invalid_body"
	codeInvalidQuery         = "invalid_query"
	codeInvalidIfMatch       = "invalid_if_match"
	codeNotFound             = "not_found"
	codeMethodNotAllowed     = "method_not_allowed"
	codeEmailTaken           = "email_taken"
	codeMaintainerInUse      = "maintainer_in_use"
	codeVersionMismatch      = "version_mismatch"
	codeBodyTooLarge         = "body_too_large"
	codeUnsupportedMediaType = "unsupported_media_type"
	codeValidationFailed     = "validation_failed"
	codeMaintainerNotFound   = "maintainer_not_found"
	codeMaintainerChanged    = "maintainer_changed"
	codeIfMatchRequired      = "if_match_required"
	codeInternal             = "internal_error"
	codeNotReady             = "not_ready"
	codeUnavailable          = "unavailable"
)

// problem is an RFC 9457 Problem Details body (DESIGN.md §4.3).
type problem struct {
	Type   string                  `json:"type"`
	Title  string                  `json:"title"`
	Status int                     `json:"status"`
	Code   string                  `json:"code"`
	Detail string                  `json:"detail,omitempty"`
	Errors []validation.FieldError `json:"errors,omitempty"`
}

// writeProblem writes an error response in the Problem Details format. code is
// the stable machine-readable error code; detail must not contain internal
// error messages.
func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	writeProblemBody(w, newProblem(status, code, detail))
}

// writeValidationProblem writes a 422 listing every invalid field.
func writeValidationProblem(w http.ResponseWriter, errs []validation.FieldError) {
	writeFieldProblem(w, codeValidationFailed, "request body failed validation", errs...)
}

// writeFieldProblem writes a 422 with the given code and per-field errors.
func writeFieldProblem(w http.ResponseWriter, code, detail string, errs ...validation.FieldError) {
	p := newProblem(http.StatusUnprocessableEntity, code, detail)
	p.Errors = errs
	writeProblemBody(w, p)
}

func newProblem(status int, code, detail string) problem {
	return problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Code: code, Detail: detail}
}

func writeProblemBody(w http.ResponseWriter, p problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	if err := json.NewEncoder(w).Encode(p); err != nil {
		log.Printf("write problem response: %v", err)
	}
}

// writeStoreError maps an error from the store to a response (DESIGN.md
// §4.6). Internal details are logged, never returned.
func writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, http.StatusNotFound, codeNotFound, "resource not found")
	case errors.Is(err, store.ErrVersionMismatch):
		writeProblem(w, http.StatusPreconditionFailed, codeVersionMismatch,
			"If-Match does not match the current resource version")
	case errors.Is(err, store.ErrEmailTaken):
		writeProblem(w, http.StatusConflict, codeEmailTaken, "email is already in use")
	case errors.Is(err, store.ErrMaintainerInUse):
		writeProblem(w, http.StatusConflict, codeMaintainerInUse,
			"maintainer still owns applications; delete them first")
	case errors.Is(err, store.ErrMaintainerNotFound):
		writeFieldProblem(w, codeMaintainerNotFound, "the referenced maintainer does not exist",
			validation.FieldError{Field: "maintainerId", Message: "must refer to an existing maintainer"})
	case errors.Is(err, store.ErrMaintainerChanged):
		writeFieldProblem(w, codeMaintainerChanged, "an application's maintainer cannot be changed",
			validation.FieldError{Field: "maintainerId", Message: "must be omitted or equal the current value"})
	case errors.Is(err, context.Canceled):
		// A client cancellation, not a server error. The response is still
		// written: the client may still be reading (DESIGN.md D9).
		log.Printf("%s %q: request canceled: %v", r.Method, r.URL.Path, err)
		writeProblem(w, http.StatusServiceUnavailable, codeUnavailable, "request was canceled")
	case errors.Is(err, context.DeadlineExceeded):
		// The request ran out of its time budget (DESIGN.md §7). This also
		// happens when a slow body upload used it up before the database was
		// ever asked, so the database is not blamed.
		log.Printf("%s %q: request deadline exceeded: %v", r.Method, r.URL.Path, err)
		writeProblem(w, http.StatusServiceUnavailable, codeUnavailable, "request deadline exceeded")
	case errors.Is(err, store.ErrUnavailable):
		log.Printf("%s %q: %v", r.Method, r.URL.Path, err)
		writeProblem(w, http.StatusServiceUnavailable, codeUnavailable, "database temporarily unavailable")
	default:
		log.Printf("%s %q: %v", r.Method, r.URL.Path, err)
		writeProblem(w, http.StatusInternalServerError, codeInternal, "internal server error")
	}
}
