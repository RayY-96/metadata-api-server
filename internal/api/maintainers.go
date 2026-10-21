package api

import (
	"net/http"

	"github.com/RayY-96/metadata-api-server/internal/model"
	"github.com/RayY-96/metadata-api-server/internal/validation"
)

const maintainersPath = "/api/v1/maintainers"

// page is the body of a list response (DESIGN.md §4.5).
type page[T any] struct {
	Items   []T  `json:"items"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"hasMore"`
}

// Each handler runs the checks in the order of DESIGN.md §4.2: body, then
// If-Match, then field validation, then the store (404, 412, 409).

func (s *Server) createMaintainer(w http.ResponseWriter, r *http.Request) {
	var in model.MaintainerInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in, errs := validation.Maintainer(in)
	if len(errs) > 0 {
		writeValidationProblem(w, errs)
		return
	}
	m, err := s.store.CreateMaintainer(r.Context(), in)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	w.Header().Set("Location", maintainersPath+"/"+m.ID)
	w.Header().Set("ETag", etag(m.ResourceVersion))
	writeJSON(w, r, http.StatusCreated, m)
}

func (s *Server) listMaintainers(w http.ResponseWriter, r *http.Request) {
	_, limit, offset, ok := parsePage(w, r)
	if !ok {
		return
	}
	items, hasMore, err := s.store.ListMaintainers(r.Context(), limit, offset)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	writeJSON(w, r, http.StatusOK, page[model.Maintainer]{
		Items: items, Limit: limit, Offset: offset, HasMore: hasMore,
	})
}

func (s *Server) getMaintainer(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.GetMaintainer(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(m.ResourceVersion))
	writeJSON(w, r, http.StatusOK, m)
}

func (s *Server) updateMaintainer(w http.ResponseWriter, r *http.Request) {
	var in model.MaintainerInput
	if !decodeJSON(w, r, &in) {
		return
	}
	version, ok := parseIfMatch(w, r)
	if !ok {
		return
	}
	in, errs := validation.Maintainer(in)
	if len(errs) > 0 {
		writeValidationProblem(w, errs)
		return
	}
	m, err := s.store.UpdateMaintainer(r.Context(), r.PathValue("id"), version, in)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(m.ResourceVersion))
	writeJSON(w, r, http.StatusOK, m)
}

func (s *Server) deleteMaintainer(w http.ResponseWriter, r *http.Request) {
	version, ok := parseIfMatch(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteMaintainer(r.Context(), r.PathValue("id"), version); err != nil {
		writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
