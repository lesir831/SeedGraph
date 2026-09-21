package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lesir831/SeedGraph/internal/store"
)

func (s *Server) listSearchPresets(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListGroupSearchPresets(r.Context())
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, items)
}

func (s *Server) createSearchPreset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTorrentGroupQueryRequestBytes)
	var request struct {
		Name   string                   `json:"name"`
		Filter *store.TorrentGroupQuery `json:"filter"`
	}
	if err := decodeBody(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "预设格式无效")
		return
	}
	item, err := s.store.CreateGroupSearchPreset(r.Context(), request.Name, request.Filter)
	if errors.Is(err, store.ErrPresetNameConflict) {
		writeAPIError(w, http.StatusConflict, "preset_name_conflict", "该预设名称已存在，请使用其他名称")
		return
	}
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, item)
}

func (s *Server) deleteSearchPreset(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteGroupSearchPreset(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
