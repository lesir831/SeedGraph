package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lesir831/SeedGraph/internal/store"
)

const maxTorrentGroupQueryRequestBytes = 64 << 10

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
	query := r.URL.Query()
	sortBy := query.Get("sort_by")
	sortOrder := query.Get("sort_order")
	sortValues, hasMultiSort := query["sort"]
	if hasMultiSort && (sortBy != "" || sortOrder != "") {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "sort 不能与 sort_by 或 sort_order 同时使用")
		return
	}
	if sortBy == "" && sortOrder != "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "sort_order 必须与 sort_by 一起提供")
		return
	}
	if sortBy != "" && sortBy != "oldest_added_at" && sortBy != "instance_count" && sortBy != "size" && sortBy != "name" {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "sort_by 必须是 oldest_added_at、instance_count、size 或 name")
		return
	}
	if sortOrder != "" && sortOrder != "asc" && sortOrder != "desc" {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "sort_order 必须是 asc 或 desc")
		return
	}
	sorts, err := parseGroupSorts(sortValues)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	siteAll, err := repeatedGroupFilter(query["site_all"], "site_all")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	siteNone, err := repeatedGroupFilter(query["site_none"], "site_none")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	excludedSites := make(map[string]struct{}, len(siteNone))
	for _, site := range siteNone {
		excludedSites[site] = struct{}{}
	}
	for _, site := range siteAll {
		if _, excluded := excludedSites[site]; excluded {
			writeAPIError(w, http.StatusBadRequest, "invalid_query", "同一站点不能同时出现在 site_all 和 site_none")
			return
		}
	}
	filters := store.GroupFilters{
		Search: query.Get("q"), NameContains: query.Get("name_contains"),
		SiteAll: siteAll, SiteNone: siteNone, MissingSite: query.Get("missing_site"),
		DownloaderID: query.Get("downloader_id"), Status: query.Get("status"),
		StaleBefore: time.Now().Add(-s.staleAfter), Sorts: sorts, SortBy: sortBy, SortOrder: sortOrder,
		Limit: limit, Offset: offset,
	}
	if value := query.Get("size_lt"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			writeAPIError(w, http.StatusBadRequest, "invalid_query", "size_lt 必须是正整数")
			return
		}
		filters.SizeLT = &parsed
	}
	if value := query.Get("oldest_added_gte"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_query", "oldest_added_gte 必须是 RFC3339 时间")
			return
		}
		filters.OldestAddedGTE = &parsed
	}
	if value := query.Get("oldest_added_lt"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_query", "oldest_added_lt 必须是 RFC3339 时间")
			return
		}
		filters.OldestAddedLT = &parsed
	}
	if filters.OldestAddedGTE != nil && filters.OldestAddedLT != nil &&
		!filters.OldestAddedGTE.Before(*filters.OldestAddedLT) {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "oldest_added_gte 必须早于 oldest_added_lt")
		return
	}
	if value := query.Get("max_site_count"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeAPIError(w, http.StatusBadRequest, "invalid_query", "max_site_count 必须是非负整数")
			return
		}
		filters.MaxSiteCount = &parsed
	}
	if value := query.Get("stale"); value != "" {
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

type torrentGroupQueryRequest struct {
	Filter       *store.TorrentGroupQuery `json:"filter"`
	Query        string                   `json:"q"`
	Status       string                   `json:"status"`
	DownloaderID string                   `json:"downloader_id"`
	Sorts        []store.GroupSort        `json:"sorts"`
	Timezone     string                   `json:"timezone"`
	Limit        int                      `json:"limit"`
	Offset       int                      `json:"offset"`
}

func (s *Server) queryGroups(w http.ResponseWriter, r *http.Request) {
	request, err := decodeTorrentGroupQueryRequest(w, r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	if request.Filter == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "filter 是必填项")
		return
	}
	if request.Limit == 0 {
		request.Limit = 50
	}
	if request.Limit < 1 || request.Limit > 200 {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "limit 必须在 1 到 200 之间")
		return
	}
	if request.Offset < 0 || request.Offset > 1_000_000 {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "offset 必须在 0 到 1000000 之间")
		return
	}
	if len(request.Query) > 1024 || len(request.Status) > 128 || len(request.DownloaderID) > 256 || len(request.Timezone) > 128 {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "查询文本过长")
		return
	}
	var queryLocation *time.Location
	if request.Timezone != "" {
		queryLocation, err = time.LoadLocation(request.Timezone)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_query", "timezone 必须是有效的 IANA 时区")
			return
		}
	}
	for _, sortRule := range request.Sorts {
		if sortRule.Order != "asc" && sortRule.Order != "desc" {
			writeAPIError(w, http.StatusBadRequest, "invalid_query", "sorts 中的 order 必须明确为 asc 或 desc")
			return
		}
	}
	queryContext, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	items, total, err := s.store.ListTorrentGroups(queryContext, store.GroupFilters{
		Query: request.Filter, QueryLocation: queryLocation, Search: request.Query, Status: request.Status,
		DownloaderID: request.DownloaderID, Sorts: request.Sorts,
		StaleBefore: time.Now().Add(-s.staleAfter), Limit: request.Limit, Offset: request.Offset,
	})
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": request.Limit, "offset": request.Offset,
	})
}

func decodeTorrentGroupQueryRequest(w http.ResponseWriter, r *http.Request) (torrentGroupQueryRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTorrentGroupQueryRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request torrentGroupQueryRequest
	if err := decoder.Decode(&request); err != nil {
		return torrentGroupQueryRequest{}, fmt.Errorf("请求体必须是有效的 JSON：%w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return torrentGroupQueryRequest{}, errors.New("请求体只能包含一个 JSON 值")
	}
	return request, nil
}

func parseGroupSorts(values []string) ([]store.GroupSort, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > 4 {
		return nil, fmt.Errorf("sort 最多支持 4 级")
	}
	result := make([]store.GroupSort, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.Count(value, ":") != 1 {
			return nil, fmt.Errorf("sort 必须使用 field:order 格式")
		}
		field, order, _ := strings.Cut(value, ":")
		if field != "oldest_added_at" && field != "instance_count" && field != "size" && field != "name" {
			return nil, fmt.Errorf("sort 字段必须是 oldest_added_at、instance_count、size 或 name")
		}
		if order != "asc" && order != "desc" {
			return nil, fmt.Errorf("sort 方向必须是 asc 或 desc")
		}
		if _, duplicate := seen[field]; duplicate {
			return nil, fmt.Errorf("sort 不能重复使用同一字段")
		}
		seen[field] = struct{}{}
		result = append(result, store.GroupSort{Field: field, Order: order})
	}
	return result, nil
}

func repeatedGroupFilter(values []string, name string) ([]string, error) {
	if len(values) > 20 {
		return nil, fmt.Errorf("%s 最多允许 20 个值", name)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s 不能包含空值", name)
		}
		prefix, identifier, found := strings.Cut(value, ":")
		if !found || (prefix != "site" && prefix != "tracker") || strings.TrimSpace(identifier) == "" {
			return nil, fmt.Errorf("%s 必须使用非空的 site: 或 tracker: key", name)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func (s *Server) listGroupSiteOptions(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListTorrentGroupSiteOptions(r.Context())
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, items)
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
