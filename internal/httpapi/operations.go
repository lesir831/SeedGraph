package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lesir831/SeedGraph/internal/store"
)

func (s *Server) listTrackerRules(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListTrackerRules(r.Context())
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, items)
}

func (s *Server) createTrackerRule(w http.ResponseWriter, r *http.Request) {
	var request struct {
		HostPattern string `json:"host_pattern"`
		PathPrefix  string `json:"path_prefix"`
		SiteName    string `json:"site_name"`
		DisplayName string `json:"display_name"`
	}
	if err := decodeBody(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	rule, err := s.store.CreateCustomTrackerRule(r.Context(), store.CreateTrackerRuleParams{
		HostPattern: request.HostPattern, PathPrefix: request.PathPrefix,
		SiteName: request.SiteName, DisplayName: request.DisplayName,
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_tracker_rule", err.Error())
		return
	}
	writeData(w, http.StatusCreated, rule)
}

func (s *Server) deleteTrackerRule(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteCustomTrackerRule(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncStatus(w http.ResponseWriter, r *http.Request) {
	running := s.syncer.Running()
	runs, err := s.store.ListSyncRuns(r.Context(), 1)
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	status := map[string]any{
		"status": "idle", "running": len(running) > 0, "running_downloaders": running,
		"scanned_instances": 0, "updated_groups": 0,
	}
	if len(running) > 0 {
		status["status"] = "running"
	}
	if len(runs) > 0 {
		status["seen_count"] = runs[0].SeenCount
		status["changed_count"] = runs[0].ChangedCount
		status["started_at"] = runs[0].StartedAt
		status["completed_at"] = runs[0].FinishedAt
		status["error"] = runs[0].Error
		if len(running) == 0 {
			status["status"] = runs[0].Status
		}
	}
	writeData(w, http.StatusOK, status)
}

func (s *Server) runSync(w http.ResponseWriter, r *http.Request) {
	count, err := s.syncer.TriggerAll(false)
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{
		"status": "running", "running": count > 0, "queued_downloaders": count,
	})
}

func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	limit, err := intQuery(r, "limit", 50, 1, 200)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	offset, err := intQuery(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	items, total, err := s.store.ListAuditEventsPage(
		r.Context(), r.URL.Query().Get("action"), r.URL.Query().Get("status"), limit, offset,
	)
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}
