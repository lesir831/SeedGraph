package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/lesir831/SeedGraph/internal/store"
)

const (
	defaultMappingPageSize = 20
	maxMappingPageSize     = 200
	maxMappingSearchRunes  = 200
)

func (s *Server) listTrackerMappings(w http.ResponseWriter, r *http.Request) {
	query, err := trackerMappingQuery(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	items, total, err := s.store.ListTrackerMappings(r.Context(), query)
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": query.Limit, "offset": query.Offset,
	})
}

func trackerMappingQuery(r *http.Request) (store.TrackerMappingQuery, error) {
	q, err := safeListSearchQuery(r)
	if err != nil {
		return store.TrackerMappingQuery{}, err
	}
	limit, err := intQuery(r, "limit", defaultMappingPageSize, 1, maxMappingPageSize)
	if err != nil {
		return store.TrackerMappingQuery{}, err
	}
	offset, err := intQuery(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		return store.TrackerMappingQuery{}, err
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = store.TrackerMappingStatusAll
	}
	switch status {
	case store.TrackerMappingStatusAll, store.TrackerMappingStatusMapped, store.TrackerMappingStatusUnmapped:
	default:
		return store.TrackerMappingQuery{}, fmt.Errorf("status 必须是 all、mapped 或 unmapped")
	}
	matchType := strings.TrimSpace(r.URL.Query().Get("match_type"))
	if matchType == "" {
		matchType = store.TrackerMatchTypeAll
	}
	switch matchType {
	case store.TrackerMatchTypeAll, store.TrackerMatchTypeExact,
		store.TrackerMatchTypeRegistrableDomain, store.TrackerMatchTypeKeyword,
		store.TrackerMatchTypeCustom:
	default:
		return store.TrackerMappingQuery{}, fmt.Errorf("match_type 必须是 all、exact、registrable_domain、keyword 或 custom")
	}
	return store.TrackerMappingQuery{
		Query: q, Status: status, MatchType: matchType, Limit: limit, Offset: offset,
	}, nil
}

func iyuuSiteQuery(r *http.Request) (store.IYUUSiteQuery, error) {
	q, err := safeListSearchQuery(r)
	if err != nil {
		return store.IYUUSiteQuery{}, err
	}
	limit, err := intQuery(r, "limit", defaultMappingPageSize, 1, maxMappingPageSize)
	if err != nil {
		return store.IYUUSiteQuery{}, err
	}
	offset, err := intQuery(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		return store.IYUUSiteQuery{}, err
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = store.TrackerMappingStatusAll
	}
	switch status {
	case store.TrackerMappingStatusAll, store.TrackerMappingStatusMapped, store.TrackerMappingStatusUnmapped:
	default:
		return store.IYUUSiteQuery{}, fmt.Errorf("status 必须是 all、mapped 或 unmapped")
	}
	return store.IYUUSiteQuery{Query: q, Status: status, Limit: limit, Offset: offset}, nil
}

func safeListSearchQuery(r *http.Request) (string, error) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(query) > maxMappingSearchRunes {
		return "", fmt.Errorf("q 最多包含 %d 个字符", maxMappingSearchRunes)
	}
	return query, nil
}
