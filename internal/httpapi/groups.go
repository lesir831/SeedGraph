package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lesir831/SeedGraph/internal/store"
)

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.GetOverview(r.Context(), time.Now().Add(-s.staleAfter))
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, value)
}

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
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
	filters := store.GroupFilters{
		Search: r.URL.Query().Get("q"), MissingSite: r.URL.Query().Get("missing_site"),
		DownloaderID: r.URL.Query().Get("downloader_id"), Status: r.URL.Query().Get("status"),
		StaleBefore: time.Now().Add(-s.staleAfter), Limit: limit, Offset: offset,
	}
	if value := r.URL.Query().Get("max_site_count"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeAPIError(w, http.StatusBadRequest, "invalid_query", "max_site_count 必须是非负整数")
			return
		}
		filters.MaxSiteCount = &parsed
	}
	if value := r.URL.Query().Get("stale"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_query", "stale 必须是布尔值")
			return
		}
		filters.Stale = &parsed
	}
	items, total, err := s.store.ListTorrentGroups(r.Context(), filters)
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

func (s *Server) getGroup(w http.ResponseWriter, r *http.Request) {
	group, err := s.store.GetTorrentGroup(r.Context(), chi.URLParam(r, "id"), time.Now().Add(-s.staleAfter))
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, group)
}

func (s *Server) mergeGroups(w http.ResponseWriter, r *http.Request) {
	var request struct {
		GroupIDs         []string       `json:"group_ids"`
		ExpectedVersions map[string]int `json:"expected_versions"`
		DisplayName      string         `json:"display_name"`
	}
	if err := decodeBody(w, r, &request); err != nil || len(request.GroupIDs) < 2 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "至少选择两个聚合组，并提供期望版本")
		return
	}
	for _, id := range request.GroupIDs {
		if _, ok := request.ExpectedVersions[id]; !ok {
			writeAPIError(w, http.StatusBadRequest, "missing_version", "每个聚合组都必须提供 expected_version")
			return
		}
	}
	group, err := s.store.MergeGroups(r.Context(), store.MergeGroupsParams{
		GroupIDs: request.GroupIDs, ExpectedVersions: request.ExpectedVersions,
		DisplayName: request.DisplayName,
	})
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	w.Header().Set("X-SeedGraph-Operation-ID", group.OperationID)
	_ = s.store.AddAuditEvent(r.Context(), store.AuditEvent{
		Actor: "admin", Action: "group.merge", TargetType: "content_group", TargetID: group.ID,
		Details: map[string]any{"source_groups": request.GroupIDs, "operation_id": group.OperationID},
	})
	writeData(w, http.StatusCreated, group)
}

func (s *Server) splitGroup(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExpectedVersion int      `json:"expected_version"`
		InstanceIDs     []string `json:"instance_ids"`
		DisplayName     string   `json:"display_name"`
	}
	if err := decodeBody(w, r, &request); err != nil || request.ExpectedVersion < 1 || len(request.InstanceIDs) == 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "必须提供版本和要拆分的任务")
		return
	}
	group, err := s.store.SplitGroup(r.Context(), store.SplitGroupParams{
		GroupID: chi.URLParam(r, "id"), ExpectedVersion: request.ExpectedVersion,
		InstanceIDs: request.InstanceIDs, DisplayName: request.DisplayName,
	})
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	w.Header().Set("X-SeedGraph-Operation-ID", group.OperationID)
	_ = s.store.AddAuditEvent(r.Context(), store.AuditEvent{
		Actor: "admin", Action: "group.split", TargetType: "content_group", TargetID: group.ID,
		Details: map[string]any{"source_group": chi.URLParam(r, "id"), "operation_id": group.OperationID},
	})
	writeData(w, http.StatusCreated, group)
}

func (s *Server) moveGroupMember(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TargetGroupID         string `json:"target_group_id"`
		ExpectedSourceVersion int    `json:"expected_source_version"`
		ExpectedTargetVersion int    `json:"expected_target_version"`
	}
	sourceGroupID := chi.URLParam(r, "id")
	instanceID := chi.URLParam(r, "instance_id")
	if err := decodeBody(w, r, &request); err != nil || request.TargetGroupID == "" ||
		request.ExpectedSourceVersion < 1 || request.ExpectedTargetVersion < 1 ||
		sourceGroupID == request.TargetGroupID {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "必须提供不同的目标组以及源组和目标组的有效版本")
		return
	}
	group, err := s.store.MoveInstance(r.Context(), store.MoveInstanceParams{
		InstanceID: instanceID, SourceGroupID: sourceGroupID, TargetGroupID: request.TargetGroupID,
		ExpectedSourceVersion: request.ExpectedSourceVersion,
		ExpectedTargetVersion: request.ExpectedTargetVersion,
	})
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	w.Header().Set("X-SeedGraph-Operation-ID", group.OperationID)
	_ = s.store.AddAuditEvent(r.Context(), store.AuditEvent{
		Actor: "admin", Action: "group.move", TargetType: "torrent_instance", TargetID: instanceID,
		Details: map[string]any{
			"source_group": sourceGroupID, "target_group": request.TargetGroupID,
			"operation_id": group.OperationID,
		},
	})
	writeData(w, http.StatusOK, group)
}

func (s *Server) undoGroupOperation(w http.ResponseWriter, r *http.Request) {
	result, err := s.store.UndoGroupOperation(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	_ = s.store.AddAuditEvent(r.Context(), store.AuditEvent{
		Actor: "admin", Action: "group.undo", TargetType: "group_operation", TargetID: result.OperationID,
		Details: map[string]any{
			"operation_type": result.OperationType, "restored_groups": result.RestoredGroupIDs,
			"retired_groups": result.RetiredGroupIDs,
		},
	})
	writeData(w, http.StatusOK, result)
}

func (s *Server) lockGroup(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExpectedVersion int  `json:"expected_version"`
		Locked          bool `json:"locked"`
	}
	if err := decodeBody(w, r, &request); err != nil || request.ExpectedVersion < 1 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "必须提供有效版本")
		return
	}
	if err := s.store.SetGroupLock(r.Context(), chi.URLParam(r, "id"), request.ExpectedVersion, request.Locked); err != nil {
		s.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) restoreAutoGroup(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExpectedVersion int `json:"expected_version"`
	}
	if err := decodeBody(w, r, &request); err != nil || request.ExpectedVersion < 1 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "必须提供有效版本")
		return
	}
	if err := s.store.RestoreAutomaticGroup(r.Context(), chi.URLParam(r, "id"), request.ExpectedVersion); err != nil {
		s.handleError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
