package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	TrackerMappingStatusAll      = "all"
	TrackerMappingStatusMapped   = "mapped"
	TrackerMappingStatusUnmapped = "unmapped"

	TrackerMatchTypeAll               = "all"
	TrackerMatchTypeExact             = "exact"
	TrackerMatchTypeRegistrableDomain = "registrable_domain"
	TrackerMatchTypeKeyword           = "keyword"
	TrackerMatchTypeCustom            = "custom"
)

// TrackerMapping is the privacy-preserving, aggregate view of one observed
// Tracker identity. HostIdentity and PathHint have both passed the same
// redaction used by the rest of the Tracker API; full announce URLs and query
// parameters are never returned.
type TrackerMapping struct {
	HostIdentity  string    `json:"host_identity"`
	PathHint      string    `json:"path_hint"`
	Mapped        bool      `json:"mapped"`
	MatchType     string    `json:"match_type"`
	SiteID        string    `json:"site_id"`
	SiteName      string    `json:"site_name"`
	DisplayName   string    `json:"display_name"`
	InstanceCount int       `json:"instance_count"`
	GroupCount    int       `json:"group_count"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

type TrackerMappingQuery struct {
	Query     string
	Status    string
	MatchType string
	Limit     int
	Offset    int
}

type trackerMappingAggregate struct {
	item       TrackerMapping
	instances  map[string]struct{}
	groups     map[string]struct{}
	candidates map[trackerMappingCandidate]struct{}
	unmapped   bool
}

type trackerMappingCandidate struct {
	matchType, siteID, siteName, displayName string
}

// ListTrackerMappings returns a filtered page after identities have been
// normalized and aggregated. Pagination is intentionally applied after
// privacy normalization so legacy rows which redact to the same identity
// cannot be exposed as separate results.
func (s *Store) ListTrackerMappings(ctx context.Context, query TrackerMappingQuery) ([]TrackerMapping, int, error) {
	query.Query = strings.ToLower(strings.TrimSpace(query.Query))
	if query.Status == "" {
		query.Status = TrackerMappingStatusAll
	}
	if query.MatchType == "" {
		query.MatchType = TrackerMatchTypeAll
	}
	if err := validateTrackerMappingQuery(query); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT tt.host_identity, tt.path_hint, ti.id,
		       COALESCE(ti.content_group_id, ''), ti.last_seen_at,
		       COALESCE(tt.match_type, ''), COALESCE(tt.site_id, ''),
		       COALESCE(si.name, ''), COALESCE(si.display_name, '')
		FROM torrent_trackers tt
		JOIN torrent_instances ti ON ti.id = tt.instance_id
		LEFT JOIN sites si ON si.id = tt.site_id
		WHERE ti.deleted_at IS NULL`)
	if err != nil {
		return nil, 0, fmt.Errorf("list Tracker mapping observations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	aggregates := make(map[string]*trackerMappingAggregate)
	for rows.Next() {
		var rawHost, rawPath, instanceID, groupID string
		var lastSeenAt int64
		var candidate trackerMappingCandidate
		if err := rows.Scan(
			&rawHost, &rawPath, &instanceID, &groupID, &lastSeenAt,
			&candidate.matchType, &candidate.siteID, &candidate.siteName, &candidate.displayName,
		); err != nil {
			return nil, 0, fmt.Errorf("scan Tracker mapping observation: %w", err)
		}

		host, err := privacySafeTrackerHost(rawHost)
		if err != nil {
			// Do not reflect malformed legacy or externally modified values.
			continue
		}
		pathHint := sanitizeObservedTrackerPath(rawPath)
		key := host + "\x00" + pathHint
		aggregate := aggregates[key]
		if aggregate == nil {
			aggregate = &trackerMappingAggregate{
				item:       TrackerMapping{HostIdentity: host, PathHint: pathHint},
				instances:  make(map[string]struct{}),
				groups:     make(map[string]struct{}),
				candidates: make(map[trackerMappingCandidate]struct{}),
			}
			aggregates[key] = aggregate
		}
		aggregate.instances[instanceID] = struct{}{}
		if groupID != "" {
			aggregate.groups[groupID] = struct{}{}
		}
		seenAt := time.Unix(lastSeenAt, 0).UTC()
		if seenAt.After(aggregate.item.LastSeenAt) {
			aggregate.item.LastSeenAt = seenAt
		}

		if candidate.siteID != "" {
			candidate.matchType = safeTrackerMatchType(candidate.matchType)
			aggregate.candidates[candidate] = struct{}{}
		} else {
			aggregate.unmapped = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate Tracker mapping observations: %w", err)
	}

	items := make([]TrackerMapping, 0, len(aggregates))
	for _, aggregate := range aggregates {
		aggregate.item.InstanceCount = len(aggregate.instances)
		aggregate.item.GroupCount = len(aggregate.groups)
		if candidate, ok := uniqueTrackerMappingCandidate(aggregate.candidates, aggregate.unmapped); ok {
			aggregate.item.Mapped = true
			aggregate.item.MatchType = candidate.matchType
			aggregate.item.SiteID = candidate.siteID
			aggregate.item.SiteName = candidate.siteName
			aggregate.item.DisplayName = candidate.displayName
		}
		if trackerMappingMatchesQuery(aggregate.item, query) {
			items = append(items, aggregate.item)
		}
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].InstanceCount != items[j].InstanceCount {
			return items[i].InstanceCount > items[j].InstanceCount
		}
		leftHost, rightHost := strings.ToLower(items[i].HostIdentity), strings.ToLower(items[j].HostIdentity)
		if leftHost != rightHost {
			return leftHost < rightHost
		}
		leftPath, rightPath := strings.ToLower(items[i].PathHint), strings.ToLower(items[j].PathHint)
		if leftPath != rightPath {
			return leftPath < rightPath
		}
		return items[i].HostIdentity < items[j].HostIdentity
	})

	total := len(items)
	if query.Offset >= total {
		return []TrackerMapping{}, total, nil
	}
	end := min(query.Offset+query.Limit, total)
	return items[query.Offset:end], total, nil
}

func validateTrackerMappingQuery(query TrackerMappingQuery) error {
	if query.Limit < 1 || query.Limit > 200 {
		return fmt.Errorf("limit must be between 1 and 200")
	}
	if query.Offset < 0 || query.Offset > 1_000_000 {
		return fmt.Errorf("offset must be between 0 and 1000000")
	}
	switch query.Status {
	case TrackerMappingStatusAll, TrackerMappingStatusMapped, TrackerMappingStatusUnmapped:
	default:
		return fmt.Errorf("status must be all, mapped, or unmapped")
	}
	switch query.MatchType {
	case TrackerMatchTypeAll, TrackerMatchTypeExact, TrackerMatchTypeRegistrableDomain,
		TrackerMatchTypeKeyword, TrackerMatchTypeCustom:
	default:
		return fmt.Errorf("match_type is invalid")
	}
	return nil
}

func safeTrackerMatchType(value string) string {
	switch value {
	case TrackerMatchTypeExact, TrackerMatchTypeRegistrableDomain, TrackerMatchTypeKeyword, TrackerMatchTypeCustom:
		return value
	default:
		// Rows mapped before match_type was introduced came from an explicit
		// custom rule. Treating those as custom keeps upgraded installations
		// filterable until the next reclassification pass.
		return TrackerMatchTypeCustom
	}
}

func uniqueTrackerMappingCandidate(candidates map[trackerMappingCandidate]struct{}, hasUnmapped bool) (trackerMappingCandidate, bool) {
	if hasUnmapped || len(candidates) != 1 {
		return trackerMappingCandidate{}, false
	}
	for candidate := range candidates {
		return candidate, true
	}
	return trackerMappingCandidate{}, false
}

func trackerMappingMatchesQuery(item TrackerMapping, query TrackerMappingQuery) bool {
	if query.Status == TrackerMappingStatusMapped && !item.Mapped ||
		query.Status == TrackerMappingStatusUnmapped && item.Mapped {
		return false
	}
	if query.MatchType != TrackerMatchTypeAll && item.MatchType != query.MatchType {
		return false
	}
	if query.Query == "" {
		return true
	}
	for _, value := range []string{
		item.HostIdentity, item.PathHint, item.SiteName, item.DisplayName,
	} {
		if strings.Contains(strings.ToLower(value), query.Query) {
			return true
		}
	}
	return false
}
