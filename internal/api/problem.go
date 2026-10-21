// Package api implements the HTTP handlers of the metadata API.
package api

import (
	"encoding/json"
	"log"
	"net/http"
)

// Stable machine-readable error codes (DESIGN.md §4.3).
const (
	codeInvalidBody          = "invalid_body"
	codeBodyTooLarge         = "body_too_large"
	codeUnsupportedMediaType = "unsupported_media_type"
	codeNotFound             = "not_found"
	codeMethodNotAllowed     = "method_not_allowed"
	codeNotReady             = "not_ready"
	codeInternal             = "internal_error"
)

// problem is an RFC 9457 Problem Details body (DESIGN.md §4.3).
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// writeProblem writes an error response in the Problem Details format. code is
// the stable machine-readable error code; detail must not contain internal
// error messages.
func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	err := json.NewEncoder(w).Encode(problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Code:   code,
		Detail: detail,
	})
	if err != nil {
		log.Printf("write problem response: %v", err)
	}
}
