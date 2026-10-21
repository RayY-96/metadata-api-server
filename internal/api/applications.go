package api

import (
	"net/http"
	"strings"

	"github.com/RayY-96/metadata-api-server/internal/model"
	"github.com/RayY-96/metadata-api-server/internal/validation"
)

const applicationsPath = "/api/v1/applications"

// The handlers run the checks in the order of DESIGN.md §4.2, as for
// Maintainers.

func (s *Server) createApplication(w http.ResponseWriter, r *http.Request) {
	var in model.ApplicationInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in, errs := validation.CreateApplication(in)
	if len(errs) > 0 {
		writeValidationProblem(w, errs)
		return
	}
	a, err := s.store.CreateApplication(r.Context(), in)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	w.Header().Set("Location", applicationsPath+"/"+a.ID)
	w.Header().Set("ETag", etag(a.ResourceVersion))
	writeJSON(w, r, http.StatusCreated, a)
}

func (s *Server) listApplications(w http.ResponseWriter, r *http.Request) {
	q, limit, offset, ok := parsePage(w, r)
	if !ok {
		return
	}
	// An optional filter, trimmed like the maintainerId field. A Maintainer
	// that does not exist simply has no Applications, so it is not an error.
	var maintainerID string
	if values, given := q["maintainerId"]; given {
		maintainerID = strings.TrimSpace(values[0])
		if len(values) > 1 || maintainerID == "" {
			writeProblem(w, http.StatusBadRequest, codeInvalidQuery, "maintainerId must be given once and not be empty")
			return
		}
	}
	items, hasMore, err := s.store.ListApplications(r.Context(), maintainerID, limit, offset)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, page[model.Application]{
		Items: items, Limit: limit, Offset: offset, HasMore: hasMore,
	})
}

func (s *Server) getApplication(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.GetApplication(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(a.ResourceVersion))
	writeJSON(w, r, http.StatusOK, a)
}

func (s *Server) updateApplication(w http.ResponseWriter, r *http.Request) {
	var in model.ApplicationInput
	if !decodeJSON(w, r, &in) {
		return
	}
	version, ok := parseIfMatch(w, r)
	if !ok {
		return
	}
	in, errs := validation.UpdateApplication(in)
	if len(errs) > 0 {
		writeValidationProblem(w, errs)
		return
	}
	a, err := s.store.UpdateApplication(r.Context(), r.PathValue("id"), version, in)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(a.ResourceVersion))
	writeJSON(w, r, http.StatusOK, a)
}

func (s *Server) deleteApplication(w http.ResponseWriter, r *http.Request) {
	version, ok := parseIfMatch(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteApplication(r.Context(), r.PathValue("id"), version); err != nil {
		writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
