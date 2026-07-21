package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrVersionConflict = errors.New("resource version conflict")

var ErrInvalidGroupSort = errors.New("invalid torrent group sort")

var ErrInvalidGroupFilter = errors.New("invalid torrent group filter")

type GroupSort struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

type TorrentGroupSite struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Mapped bool   `json:"mapped"`
}

type GroupFilters struct {
	ID             string
	Search         string
	Query          *TorrentGroupQuery
	QueryLocation  *time.Location
	NameContains   string
	SiteAll        []string
	SiteNone       []string
	SizeLT         *int64
	OldestAddedGTE *time.Time
	OldestAddedLT  *time.Time
	MaxSiteCount   *int
	MissingSite    string
	DownloaderID   string
	Status         string
	Stale          *bool
	StaleBefore    time.Time
	Sorts          []GroupSort
	SortBy         string
	SortOrder      string
	Limit          int
	Offset         int
}

type TorrentGroup struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	SizeBytes       int64              `json:"size_bytes"`
	TaskCount       int                `json:"task_count"`
	SiteCount       int                `json:"site_count"`
	DownloaderCount int                `json:"downloader_count"`
	DataCopyCount   int                `json:"data_copy_count"`
	Confidence      string             `json:"confidence"`
	Mode            string             `json:"mode"`
	Locked          bool               `json:"locked"`
	Version         int                `json:"version"`
	Stale           bool               `json:"stale"`
	OldestAddedAt   time.Time          `json:"oldest_added_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	Sites           []TorrentGroupSite `json:"sites"`
}

type TorrentInstanceView struct {
	ID               string     `json:"id"`
	DownloaderID     string     `json:"downloader_id"`
	DownloaderName   string     `json:"downloader_name"`
	DownloaderKind   string     `json:"downloader_kind"`
	StableHashKey    string     `json:"stable_hash_key"`
	Name             string     `json:"name"`
	CanonicalPath    string     `json:"canonical_path"`
	StorageID        string     `json:"storage_id"`
	WantedBytes      int64      `json:"wanted_bytes"`
	DataGroupID      string     `json:"data_group_id"`
	AssignmentSource string     `json:"assignment_source"`
	AddedAt          time.Time  `json:"added_at"`
	Status           string     `json:"status"`
	Progress         float64    `json:"progress"`
	Ratio            float64    `json:"ratio"`
	UpdatedAt        time.Time  `json:"updated_at"`
	LastSyncAt       *time.Time `json:"last_sync_at"`
	Sites            []string   `json:"sites"`
}

type TorrentGroupDetail struct {
	TorrentGroup
	Instances   []TorrentInstanceView `json:"instances"`
	OperationID string                `json:"operation_id,omitempty"`
}

func (s *Store) ListTorrentGroups(ctx context.Context, filters GroupFilters) ([]TorrentGroup, int, error) {
	if filters.Limit <= 0 || filters.Limit > 200 {
		filters.Limit = 50
	}
	if filters.Offset < 0 {
		filters.Offset = 0
	}
	if filters.StaleBefore.IsZero() {
		filters.StaleBefore = s.now().Add(-5 * time.Minute)
	}
	orderBy, err := torrentGroupOrderBy(filters.Sorts, filters.SortBy, filters.SortOrder)
	if err != nil {
		return nil, 0, err
	}
	siteAll, err := parseGroupSiteFilters(filters.SiteAll)
	if err != nil {
		return nil, 0, err
	}
	siteNone, err := parseGroupSiteFilters(filters.SiteNone)
	if err != nil {
		return nil, 0, err
	}
	if len(siteAll) > 20 || len(siteNone) > 20 {
		return nil, 0, fmt.Errorf("%w: too many site filters", ErrInvalidGroupFilter)
	}
	for _, required := range siteAll {
		for _, excluded := range siteNone {
			if required.key == excluded.key {
				return nil, 0, fmt.Errorf("%w: site cannot be both required and excluded", ErrInvalidGroupFilter)
			}
		}
	}
	if filters.SizeLT != nil && *filters.SizeLT <= 0 {
		return nil, 0, fmt.Errorf("%w: size_lt must be positive", ErrInvalidGroupFilter)
	}
	if filters.OldestAddedGTE != nil && filters.OldestAddedLT != nil &&
		!filters.OldestAddedGTE.Before(*filters.OldestAddedLT) {
		return nil, 0, fmt.Errorf("%w: oldest_added_gte must be before oldest_added_lt", ErrInvalidGroupFilter)
	}
	queryWhere, filterArgs, err := compileTorrentGroupQuery(filters.Query, filters.QueryLocation)
	if err != nil {
		return nil, 0, err
	}

	where := []string{"1 = 1"}
	args := make([]any, 0)
	if filters.ID != "" {
		where = append(where, "gm.id = ?")
		args = append(args, filters.ID)
	}
	if value := strings.TrimSpace(filters.NameContains); value != "" {
		where = append(where, "gm.display_name LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(value)+"%")
	}

	instanceWhere := []string{"fti.content_group_id = gm.id", "fti.deleted_at IS NULL"}
	instanceArgs := make([]any, 0)
	needsInstanceFilter := false
	if value := strings.TrimSpace(filters.Search); value != "" {
		like := "%" + escapeLike(value) + "%"
		instanceWhere = append(instanceWhere, "(gm.display_name LIKE ? ESCAPE '\\' OR fti.canonical_path LIKE ? ESCAPE '\\')")
		instanceArgs = append(instanceArgs, like, like)
		needsInstanceFilter = true
	}
	if filters.DownloaderID != "" {
		instanceWhere = append(instanceWhere, "fti.downloader_id = ?")
		instanceArgs = append(instanceArgs, filters.DownloaderID)
		needsInstanceFilter = true
	}
	if filters.Status != "" {
		instanceWhere = append(instanceWhere, "ftr.status = ?")
		instanceArgs = append(instanceArgs, filters.Status)
		needsInstanceFilter = true
	}
	if needsInstanceFilter {
		where = append(where, `EXISTS (
            SELECT 1
            FROM torrent_instances fti
            LEFT JOIN torrent_runtime ftr ON ftr.instance_id = fti.id
            WHERE `+strings.Join(instanceWhere, " AND ")+`
        )`)
		args = append(args, instanceArgs...)
	}
	if filters.Stale != nil {
		expected := 0
		if *filters.Stale {
			expected = 1
		}
		where = append(where, "gm.stale = ?")
		args = append(args, expected)
	}
	if filters.MissingSite != "" {
		where = append(where, `NOT EXISTS (
            SELECT 1 FROM torrent_instances mti
            JOIN torrent_trackers mtt ON mtt.instance_id = mti.id
            JOIN sites ms ON ms.id = mtt.site_id
		    WHERE mti.content_group_id = gm.id AND mti.deleted_at IS NULL AND ms.name = ?
		)`)
		args = append(args, filters.MissingSite)
	}
	for _, site := range siteAll {
		where = append(where, groupSiteExistsPredicate("EXISTS", site))
		args = append(args, site.value)
	}
	for _, site := range siteNone {
		where = append(where, groupSiteExistsPredicate("NOT EXISTS", site))
		args = append(args, site.value)
	}
	if filters.MaxSiteCount != nil {
		where = append(where, "gm.site_count <= ?")
		args = append(args, *filters.MaxSiteCount)
	}
	if filters.SizeLT != nil {
		where = append(where, "gm.size_bytes < ?")
		args = append(args, *filters.SizeLT)
	}
	if filters.OldestAddedGTE != nil {
		where = append(where, "gm.oldest_added_at >= ?")
		args = append(args, filters.OldestAddedGTE.Unix())
	}
	if filters.OldestAddedLT != nil {
		where = append(where, "gm.oldest_added_at < ?")
		args = append(args, filters.OldestAddedLT.Unix())
	}
	if queryWhere != "" {
		where = append(where, queryWhere)
		args = append(args, filterArgs...)
	}

	metricsCTE := `WITH group_metrics AS (
		SELECT
			cg.id, cg.display_name, MAX(ti.wanted_bytes) AS size_bytes,
			COUNT(DISTINCT ti.id) AS task_count,
			COUNT(DISTINCT CASE
				WHEN tt.site_id IS NOT NULL THEN 'site:' || tt.site_id
				ELSE 'unknown:' || tt.host_identity
			END) AS site_count,
			COUNT(DISTINCT ti.downloader_id) AS downloader_count,
			COUNT(DISTINCT ti.data_group_id) AS data_copy_count,
			cg.confidence, cg.mode, cg.locked, cg.version,
			MAX(CASE WHEN COALESCE(d.last_success_at, 0) < ? THEN 1 ELSE 0 END) AS stale,
			MIN(COALESCE(ti.added_at, ti.first_seen_at)) AS oldest_added_at,
			cg.updated_at
        FROM content_groups cg
        JOIN torrent_instances ti ON ti.content_group_id = cg.id
        JOIN data_groups dg ON dg.id = ti.data_group_id
        JOIN downloaders d ON d.id = ti.downloader_id
        LEFT JOIN torrent_trackers tt ON tt.instance_id = ti.id
		WHERE cg.deleted_at IS NULL AND ti.deleted_at IS NULL
		GROUP BY cg.id
	)`
	filtered := " FROM group_metrics gm WHERE " + strings.Join(where, " AND ")
	baseArgs := append([]any{filters.StaleBefore.Unix()}, args...)

	countQuery := metricsCTE + " SELECT COUNT(*)" + filtered
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, baseArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count torrent groups: %w", err)
	}

	query := metricsCTE + ` SELECT
		gm.id, gm.display_name, gm.size_bytes, gm.task_count, gm.site_count,
		gm.downloader_count, gm.data_copy_count, gm.confidence, gm.mode, gm.locked,
		gm.version, gm.stale, gm.oldest_added_at, gm.updated_at` + filtered +
		" ORDER BY " + orderBy + " LIMIT ? OFFSET ?"
	queryArgs := append([]any(nil), baseArgs...)
	queryArgs = append(queryArgs, filters.Limit, filters.Offset)
	rows, err := s.db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list torrent groups: %w", err)
	}
	groups := make([]TorrentGroup, 0)
	for rows.Next() {
		var group TorrentGroup
		var locked, stale int
		var oldestAddedAt, updatedAt int64
		if err := rows.Scan(
			&group.ID, &group.Name, &group.SizeBytes, &group.TaskCount, &group.SiteCount,
			&group.DownloaderCount, &group.DataCopyCount, &group.Confidence, &group.Mode,
			&locked, &group.Version, &stale, &oldestAddedAt, &updatedAt,
		); err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		group.Locked = locked != 0
		group.Stale = stale != 0
		group.OldestAddedAt = time.Unix(oldestAddedAt, 0).UTC()
		group.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		group.Sites = make([]TorrentGroupSite, 0)
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, 0, err
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	if err := s.populateTorrentGroupSites(ctx, groups); err != nil {
		return nil, 0, err
	}
	return groups, total, nil
}

func torrentGroupOrderBy(sorts []GroupSort, sortBy, sortOrder string) (string, error) {
	if len(sorts) != 0 && (sortBy != "" || sortOrder != "") {
		return "", fmt.Errorf("%w: legacy and multi-sort parameters cannot be combined", ErrInvalidGroupSort)
	}
	if len(sorts) == 0 && sortBy == "" {
		if sortOrder != "" {
			return "", fmt.Errorf("%w: sort_order requires sort_by", ErrInvalidGroupSort)
		}
		return "gm.updated_at DESC, gm.id ASC", nil
	}
	if len(sorts) == 0 {
		sorts = []GroupSort{{Field: sortBy, Order: sortOrder}}
	}
	if len(sorts) > 4 {
		return "", fmt.Errorf("%w: at most four sort levels are allowed", ErrInvalidGroupSort)
	}
	seen := make(map[string]struct{}, len(sorts))
	parts := make([]string, 0, len(sorts)+1)
	for _, item := range sorts {
		expression := ""
		switch item.Field {
		case "oldest_added_at":
			expression = "gm.oldest_added_at"
		case "instance_count":
			expression = "gm.task_count"
		case "size":
			expression = "gm.size_bytes"
		case "name":
			expression = "gm.display_name COLLATE NOCASE"
		default:
			return "", fmt.Errorf("%w: unsupported sort field", ErrInvalidGroupSort)
		}
		if _, duplicate := seen[item.Field]; duplicate {
			return "", fmt.Errorf("%w: duplicate sort field", ErrInvalidGroupSort)
		}
		seen[item.Field] = struct{}{}
		order := item.Order
		if order == "" {
			order = "asc"
		}
		if order != "asc" && order != "desc" {
			return "", fmt.Errorf("%w: unsupported sort order", ErrInvalidGroupSort)
		}
		parts = append(parts, expression+" "+strings.ToUpper(order))
	}
	parts = append(parts, "gm.id ASC")
	return strings.Join(parts, ", "), nil
}

type groupSiteFilter struct {
	key    string
	value  string
	mapped bool
}

func parseGroupSiteFilters(values []string) ([]groupSiteFilter, error) {
	values = normalizedUniqueStrings(values)
	result := make([]groupSiteFilter, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, key := range values {
		var filter groupSiteFilter
		switch {
		case strings.HasPrefix(key, "site:"):
			value := strings.TrimSpace(strings.TrimPrefix(key, "site:"))
			if value == "" {
				return nil, fmt.Errorf("%w: site key cannot be empty", ErrInvalidGroupFilter)
			}
			filter = groupSiteFilter{key: "site:" + value, value: value, mapped: true}
		case strings.HasPrefix(key, "tracker:"):
			value := strings.TrimSpace(strings.TrimPrefix(key, "tracker:"))
			if value == "" {
				return nil, fmt.Errorf("%w: tracker key cannot be empty", ErrInvalidGroupFilter)
			}
			host, err := privacySafeTrackerHost(value)
			if err != nil || host != value {
				return nil, fmt.Errorf("%w: tracker key must contain a canonical privacy-safe host", ErrInvalidGroupFilter)
			}
			filter = groupSiteFilter{key: "tracker:" + host, value: host}
		default:
			return nil, fmt.Errorf("%w: site key must use a site or tracker prefix", ErrInvalidGroupFilter)
		}
		if _, duplicate := seen[filter.key]; duplicate {
			continue
		}
		seen[filter.key] = struct{}{}
		result = append(result, filter)
	}
	return result, nil
}

func groupSiteExistsPredicate(operator string, filter groupSiteFilter) string {
	condition := "stt.site_id IS NULL AND stt.host_identity = ?"
	if filter.mapped {
		condition = "stt.site_id IS NOT NULL AND ss.name = ?"
	}
	return operator + ` (
            SELECT 1
            FROM torrent_instances sti
            JOIN torrent_trackers stt ON stt.instance_id = sti.id
            LEFT JOIN sites ss ON ss.id = stt.site_id
            WHERE sti.content_group_id = gm.id
              AND sti.deleted_at IS NULL
		      AND ` + condition + `
        )`
}

func normalizedUniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (s *Store) populateTorrentGroupSites(ctx context.Context, groups []TorrentGroup) error {
	if len(groups) == 0 {
		return nil
	}
	placeholders := make([]string, len(groups))
	args := make([]any, len(groups))
	byGroup := make(map[string]map[string]TorrentGroupSite, len(groups))
	for index := range groups {
		placeholders[index] = "?"
		args[index] = groups[index].ID
		byGroup[groups[index].ID] = make(map[string]TorrentGroupSite)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT ti.content_group_id, s.name, s.display_name, tt.host_identity
		FROM torrent_instances ti
		JOIN torrent_trackers tt ON tt.instance_id = ti.id
		LEFT JOIN sites s ON s.id = tt.site_id
		WHERE ti.deleted_at IS NULL AND ti.content_group_id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return fmt.Errorf("list torrent group sites: %w", err)
	}
	for rows.Next() {
		var groupID, rawHost string
		var siteName, displayName sql.NullString
		if err := rows.Scan(&groupID, &siteName, &displayName, &rawHost); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan torrent group site: %w", err)
		}
		option, ok := torrentGroupSite(siteName, displayName, rawHost)
		if !ok {
			continue
		}
		byGroup[groupID][option.Key] = option
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate torrent group sites: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close torrent group sites: %w", err)
	}
	for index := range groups {
		for _, option := range byGroup[groups[index].ID] {
			groups[index].Sites = append(groups[index].Sites, option)
		}
		sortTorrentGroupSites(groups[index].Sites)
	}
	return nil
}

func (s *Store) ListTorrentGroupSiteOptions(ctx context.Context) ([]TorrentGroupSite, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.name, s.display_name, tt.host_identity
		FROM torrent_instances ti
		JOIN content_groups cg ON cg.id = ti.content_group_id
		JOIN torrent_trackers tt ON tt.instance_id = ti.id
		LEFT JOIN sites s ON s.id = tt.site_id
		WHERE ti.deleted_at IS NULL AND cg.deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("list torrent group site options: %w", err)
	}
	unique := make(map[string]TorrentGroupSite)
	for rows.Next() {
		var rawHost string
		var siteName, displayName sql.NullString
		if err := rows.Scan(&siteName, &displayName, &rawHost); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan torrent group site option: %w", err)
		}
		option, ok := torrentGroupSite(siteName, displayName, rawHost)
		if ok {
			unique[option.Key] = option
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate torrent group site options: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close torrent group site options: %w", err)
	}
	result := make([]TorrentGroupSite, 0, len(unique))
	for _, option := range unique {
		result = append(result, option)
	}
	sortTorrentGroupSites(result)
	return result, nil
}

func torrentGroupSite(siteName, displayName sql.NullString, rawHost string) (TorrentGroupSite, bool) {
	if siteName.Valid && strings.TrimSpace(siteName.String) != "" {
		label := strings.TrimSpace(displayName.String)
		if label == "" {
			label = siteName.String
		}
		return TorrentGroupSite{Key: "site:" + siteName.String, Label: label, Mapped: true}, true
	}
	host, err := privacySafeTrackerHost(rawHost)
	if err != nil {
		return TorrentGroupSite{}, false
	}
	return TorrentGroupSite{Key: "tracker:" + host, Label: "Unknown · " + host}, true
}

func sortTorrentGroupSites(sites []TorrentGroupSite) {
	sort.Slice(sites, func(left, right int) bool {
		leftFolded := strings.ToLower(sites[left].Label)
		rightFolded := strings.ToLower(sites[right].Label)
		if leftFolded != rightFolded {
			return leftFolded < rightFolded
		}
		return sites[left].Key < sites[right].Key
	})
}

func (s *Store) GetTorrentGroup(ctx context.Context, id string, staleBefore time.Time) (TorrentGroupDetail, error) {
	groups, _, err := s.ListTorrentGroups(ctx, GroupFilters{ID: id, Limit: 1, StaleBefore: staleBefore})
	if err != nil {
		return TorrentGroupDetail{}, err
	}
	var summary TorrentGroup
	for _, group := range groups {
		if group.ID == id {
			summary = group
			break
		}
	}
	if summary.ID == "" {
		return TorrentGroupDetail{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT ti.id, ti.downloader_id, d.name, d.kind, ti.stable_hash_key, ti.name,
               ti.canonical_path, ti.storage_id, ti.wanted_bytes, ti.data_group_id,
		       ti.assignment_source, COALESCE(ti.added_at, ti.first_seen_at),
		       COALESCE(tr.status, 'unknown'), COALESCE(tr.progress, 0),
               COALESCE(tr.ratio, 0), COALESCE(tr.updated_at, ti.last_seen_at), d.last_success_at
        FROM torrent_instances ti
        JOIN downloaders d ON d.id = ti.downloader_id
        LEFT JOIN torrent_runtime tr ON tr.instance_id = ti.id
        WHERE ti.content_group_id = ? AND ti.deleted_at IS NULL
        ORDER BY d.name COLLATE NOCASE, ti.name COLLATE NOCASE`, id)
	if err != nil {
		return TorrentGroupDetail{}, err
	}
	defer func() { _ = rows.Close() }()
	detail := TorrentGroupDetail{TorrentGroup: summary}
	for rows.Next() {
		var instance TorrentInstanceView
		var addedAt, updatedAt int64
		var lastSync sql.NullInt64
		if err := rows.Scan(
			&instance.ID, &instance.DownloaderID, &instance.DownloaderName, &instance.DownloaderKind,
			&instance.StableHashKey, &instance.Name, &instance.CanonicalPath, &instance.StorageID,
			&instance.WantedBytes, &instance.DataGroupID, &instance.AssignmentSource, &addedAt,
			&instance.Status, &instance.Progress, &instance.Ratio, &updatedAt, &lastSync,
		); err != nil {
			return TorrentGroupDetail{}, err
		}
		instance.AddedAt = time.Unix(addedAt, 0).UTC()
		instance.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		if lastSync.Valid {
			value := time.Unix(lastSync.Int64, 0).UTC()
			instance.LastSyncAt = &value
		}
		instance.Sites, err = s.instanceSites(ctx, instance.ID)
		if err != nil {
			return TorrentGroupDetail{}, err
		}
		detail.Instances = append(detail.Instances, instance)
	}
	return detail, rows.Err()
}

func (s *Store) instanceSites(ctx context.Context, instanceID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT s.display_name, tt.host_identity
        FROM torrent_trackers tt LEFT JOIN sites s ON s.id = tt.site_id
		WHERE tt.instance_id = ?`, instanceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	unique := make(map[string]struct{})
	for rows.Next() {
		var displayName sql.NullString
		var rawHost string
		if err := rows.Scan(&displayName, &rawHost); err != nil {
			return nil, err
		}
		if displayName.Valid {
			unique[displayName.String] = struct{}{}
			continue
		}
		host, err := privacySafeTrackerHost(rawHost)
		if err != nil {
			unique["Unknown"] = struct{}{}
			continue
		}
		unique["Unknown · "+host] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := strings.ToLower(result[i]), strings.ToLower(result[j])
		if left != right {
			return left < right
		}
		return result[i] < result[j]
	})
	return result, nil
}

type MergeGroupsParams struct {
	GroupIDs         []string
	ExpectedVersions map[string]int
	DisplayName      string
}

type SplitGroupParams struct {
	GroupID         string
	ExpectedVersion int
	InstanceIDs     []string
	DisplayName     string
}

type MoveInstanceParams struct {
	InstanceID            string
	SourceGroupID         string
	TargetGroupID         string
	ExpectedSourceVersion int
	ExpectedTargetVersion int
}

type UndoGroupOperationResult struct {
	OperationID      string         `json:"operation_id"`
	OperationType    string         `json:"operation_type"`
	RestoredGroupIDs []string       `json:"restored_group_ids"`
	RetiredGroupIDs  []string       `json:"retired_group_ids"`
	Versions         map[string]int `json:"versions"`
	UndoneAt         time.Time      `json:"undone_at"`
}

const groupOperationPayloadVersion = 1

type groupOperationGroupState struct {
	ID          string `json:"id"`
	AutoKey     string `json:"auto_key"`
	DisplayName string `json:"display_name"`
	Mode        string `json:"mode"`
	Confidence  string `json:"confidence"`
	Locked      bool   `json:"locked"`
	Version     int    `json:"version"`
	DeletedAt   *int64 `json:"deleted_at,omitempty"`
}

type groupOperationMemberState struct {
	ID                     string `json:"id"`
	BeforeGroupID          string `json:"before_group_id"`
	BeforeAssignmentSource string `json:"before_assignment_source"`
	AfterGroupID           string `json:"after_group_id"`
	AfterAssignmentSource  string `json:"after_assignment_source"`
}

type groupOperationPayload struct {
	Version         int                         `json:"version"`
	Groups          []groupOperationGroupState  `json:"groups"`
	CreatedGroupIDs []string                    `json:"created_group_ids"`
	Members         []groupOperationMemberState `json:"members"`
	AfterVersions   map[string]int              `json:"after_versions"`
	AfterDeleted    map[string]bool             `json:"after_deleted"`
}

func (s *Store) SplitGroup(ctx context.Context, params SplitGroupParams) (TorrentGroupDetail, error) {
	instanceIDs, duplicate := uniqueSortedIDs(params.InstanceIDs)
	if params.GroupID == "" || params.ExpectedVersion < 1 || len(instanceIDs) == 0 {
		return TorrentGroupDetail{}, errors.New("group ID and at least one instance are required")
	}
	if duplicate {
		return TorrentGroupDetail{}, errors.New("instance IDs must be unique")
	}
	newID := uuid.NewString()
	operationID := uuid.NewString()
	now := s.now().Unix()
	err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if err := checkGroupVersionsTx(ctx, tx, map[string]int{params.GroupID: params.ExpectedVersion}); err != nil {
			return err
		}
		groups, err := loadActiveGroupStatesTx(ctx, tx, []string{params.GroupID})
		if err != nil {
			return err
		}
		members, err := loadSpecificGroupMembersTx(ctx, tx, params.GroupID, instanceIDs)
		if err != nil {
			return err
		}
		if params.DisplayName == "" {
			if err := tx.QueryRowContext(ctx, "SELECT display_name FROM content_groups WHERE id = ?", params.GroupID).Scan(&params.DisplayName); err != nil {
				return err
			}
			params.DisplayName += " · split"
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO content_groups(id, display_name, mode, confidence, version, created_at, updated_at)
            VALUES(?, ?, 'manual', 'manual', 1, ?, ?)`, newID, params.DisplayName, now, now); err != nil {
			return err
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(instanceIDs)), ",")
		args := []any{newID, params.GroupID}
		for _, instanceID := range instanceIDs {
			args = append(args, instanceID)
		}
		result, err := tx.ExecContext(ctx, `UPDATE torrent_instances
            SET content_group_id = ?, assignment_source = 'manual'
            WHERE content_group_id = ? AND deleted_at IS NULL AND id IN (`+placeholders+")", args...)
		if err != nil {
			return err
		}
		moved, _ := result.RowsAffected()
		if moved != int64(len(instanceIDs)) {
			return errors.New("one or more instances no longer belong to the source group")
		}
		if _, err := tx.ExecContext(ctx, `
            UPDATE content_groups SET mode = 'manual', confidence = 'manual', version = version + 1, updated_at = ?
            WHERE id = ?`, now, params.GroupID); err != nil {
			return err
		}
		for i := range members {
			members[i].AfterGroupID = newID
			members[i].AfterAssignmentSource = "manual"
		}
		payload := groupOperationPayload{
			Version: groupOperationPayloadVersion, Groups: groups,
			CreatedGroupIDs: []string{newID}, Members: members,
			AfterVersions: map[string]int{params.GroupID: params.ExpectedVersion + 1, newID: 1},
			AfterDeleted:  map[string]bool{params.GroupID: false, newID: false},
		}
		return insertGroupOperationTx(ctx, tx, operationID, "split", newID, 0, 1, payload, now)
	})
	if err != nil {
		return TorrentGroupDetail{}, err
	}
	detail, err := s.GetTorrentGroup(ctx, newID, time.Time{})
	detail.OperationID = operationID
	return detail, err
}

func (s *Store) MoveInstance(ctx context.Context, params MoveInstanceParams) (TorrentGroupDetail, error) {
	if params.InstanceID == "" || params.SourceGroupID == "" || params.TargetGroupID == "" {
		return TorrentGroupDetail{}, errors.New("instance, source group, and target group IDs are required")
	}
	if params.SourceGroupID == params.TargetGroupID {
		return TorrentGroupDetail{}, errors.New("source and target groups must differ")
	}
	if params.ExpectedSourceVersion < 1 || params.ExpectedTargetVersion < 1 {
		return TorrentGroupDetail{}, errors.New("source and target group versions are required")
	}
	operationID := uuid.NewString()
	now := s.now().Unix()
	err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		expectedVersions := map[string]int{
			params.SourceGroupID: params.ExpectedSourceVersion,
			params.TargetGroupID: params.ExpectedTargetVersion,
		}
		if err := checkGroupVersionsTx(ctx, tx, expectedVersions); err != nil {
			return err
		}
		groups, err := loadActiveGroupStatesTx(ctx, tx, []string{params.SourceGroupID, params.TargetGroupID})
		if err != nil {
			return err
		}
		var sourceGroupID, assignmentSource string
		if err := tx.QueryRowContext(ctx, `
			SELECT content_group_id, assignment_source FROM torrent_instances
			WHERE id = ? AND deleted_at IS NULL`, params.InstanceID).Scan(&sourceGroupID, &assignmentSource); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if sourceGroupID != params.SourceGroupID {
			return fmt.Errorf("%w: instance no longer belongs to source group %s", ErrVersionConflict, params.SourceGroupID)
		}
		if _, err := tx.ExecContext(ctx, `
            UPDATE torrent_instances SET content_group_id = ?, assignment_source = 'manual' WHERE id = ?`,
			params.TargetGroupID, params.InstanceID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
            UPDATE content_groups SET mode = 'manual', confidence = 'manual', version = version + 1, updated_at = ?
			WHERE id IN (?, ?)`, now, params.SourceGroupID, params.TargetGroupID)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 2 {
			return fmt.Errorf("%w: source or target group changed", ErrVersionConflict)
		}
		payload := groupOperationPayload{
			Version: groupOperationPayloadVersion, Groups: groups,
			CreatedGroupIDs: []string{},
			Members: []groupOperationMemberState{{
				ID: params.InstanceID, BeforeGroupID: sourceGroupID,
				BeforeAssignmentSource: assignmentSource, AfterGroupID: params.TargetGroupID,
				AfterAssignmentSource: "manual",
			}},
			AfterVersions: map[string]int{
				params.SourceGroupID: params.ExpectedSourceVersion + 1,
				params.TargetGroupID: params.ExpectedTargetVersion + 1,
			},
			AfterDeleted: map[string]bool{params.SourceGroupID: false, params.TargetGroupID: false},
		}
		return insertGroupOperationTx(
			ctx, tx, operationID, "move", params.TargetGroupID,
			params.ExpectedTargetVersion, params.ExpectedTargetVersion+1, payload, now,
		)
	})
	if err != nil {
		return TorrentGroupDetail{}, err
	}
	detail, err := s.GetTorrentGroup(ctx, params.TargetGroupID, time.Time{})
	detail.OperationID = operationID
	return detail, err
}

func (s *Store) MergeGroups(ctx context.Context, params MergeGroupsParams) (TorrentGroupDetail, error) {
	groupIDs, duplicate := uniqueSortedIDs(params.GroupIDs)
	if len(groupIDs) < 2 {
		return TorrentGroupDetail{}, errors.New("at least two groups are required")
	}
	if duplicate {
		return TorrentGroupDetail{}, errors.New("group IDs must be unique")
	}
	expectedVersions := make(map[string]int, len(groupIDs))
	for _, id := range groupIDs {
		version, ok := params.ExpectedVersions[id]
		if !ok || version < 1 {
			return TorrentGroupDetail{}, fmt.Errorf("expected version is required for group %s", id)
		}
		expectedVersions[id] = version
	}
	newID := uuid.NewString()
	operationID := uuid.NewString()
	now := s.now().Unix()
	err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if err := checkGroupVersionsTx(ctx, tx, expectedVersions); err != nil {
			return err
		}
		groups, err := loadActiveGroupStatesTx(ctx, tx, groupIDs)
		if err != nil {
			return err
		}
		members, err := loadMembersForGroupsTx(ctx, tx, groupIDs)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return ErrNotFound
		}
		if params.DisplayName == "" {
			if err := tx.QueryRowContext(ctx, "SELECT display_name FROM content_groups WHERE id = ?", groupIDs[0]).Scan(&params.DisplayName); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO content_groups(id, display_name, mode, confidence, version, created_at, updated_at)
            VALUES(?, ?, 'manual', 'manual', 1, ?, ?)`, newID, params.DisplayName, now, now); err != nil {
			return fmt.Errorf("create merged group: %w", err)
		}
		placeholders := strings.TrimRight(strings.Repeat("?,", len(groupIDs)), ",")
		args := make([]any, 0, len(groupIDs)+1)
		args = append(args, newID)
		for _, id := range groupIDs {
			args = append(args, id)
		}
		result, err := tx.ExecContext(ctx, `UPDATE torrent_instances
            SET content_group_id = ?, assignment_source = 'manual'
            WHERE deleted_at IS NULL AND content_group_id IN (`+placeholders+")", args...)
		if err != nil {
			return fmt.Errorf("move merged group members: %w", err)
		}
		moved, _ := result.RowsAffected()
		if moved == 0 {
			return ErrNotFound
		}
		deleteArgs := make([]any, 0, len(groupIDs)+2)
		deleteArgs = append(deleteArgs, now, now)
		for _, id := range groupIDs {
			deleteArgs = append(deleteArgs, id)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE content_groups SET deleted_at = ?, version = version + 1, updated_at = ? WHERE id IN ("+placeholders+")", deleteArgs...); err != nil {
			return fmt.Errorf("retire merged groups: %w", err)
		}
		for i := range members {
			members[i].AfterGroupID = newID
			members[i].AfterAssignmentSource = "manual"
		}
		afterVersions := map[string]int{newID: 1}
		afterDeleted := map[string]bool{newID: false}
		for _, group := range groups {
			afterVersions[group.ID] = group.Version + 1
			afterDeleted[group.ID] = true
		}
		payload := groupOperationPayload{
			Version: groupOperationPayloadVersion, Groups: groups,
			CreatedGroupIDs: []string{newID}, Members: members,
			AfterVersions: afterVersions, AfterDeleted: afterDeleted,
		}
		return insertGroupOperationTx(ctx, tx, operationID, "merge", newID, 0, 1, payload, now)
	})
	if err != nil {
		return TorrentGroupDetail{}, err
	}
	detail, err := s.GetTorrentGroup(ctx, newID, time.Time{})
	detail.OperationID = operationID
	return detail, err
}

func (s *Store) UndoGroupOperation(ctx context.Context, operationID string) (UndoGroupOperationResult, error) {
	if operationID == "" {
		return UndoGroupOperationResult{}, errors.New("operation ID is required")
	}
	now := s.now().Unix()
	result := UndoGroupOperationResult{
		OperationID: operationID, RestoredGroupIDs: []string{}, RetiredGroupIDs: []string{},
		Versions: make(map[string]int), UndoneAt: time.Unix(now, 0).UTC(),
	}
	err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var payloadJSON string
		var undoneAt sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT operation_type, payload_json, undone_at FROM group_operations WHERE id = ?`, operationID).
			Scan(&result.OperationType, &payloadJSON, &undoneAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if undoneAt.Valid {
			return fmt.Errorf("%w: group operation was already undone", ErrVersionConflict)
		}
		var payload groupOperationPayload
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return fmt.Errorf("decode group operation payload: %w", err)
		}
		if payload.Version != groupOperationPayloadVersion {
			return fmt.Errorf("unsupported group operation payload version %d", payload.Version)
		}
		if err := verifyGroupOperationStateTx(ctx, tx, payload); err != nil {
			return err
		}
		for _, group := range payload.Groups {
			var deletedAt any
			if group.DeletedAt != nil {
				deletedAt = *group.DeletedAt
			}
			update, err := tx.ExecContext(ctx, `
				UPDATE content_groups SET auto_key = ?, display_name = ?, mode = ?, confidence = ?,
					locked = ?, deleted_at = ?, version = version + 1, updated_at = ?
				WHERE id = ? AND version = ?`, group.AutoKey, group.DisplayName, group.Mode,
				group.Confidence, boolInt(group.Locked), deletedAt, now, group.ID, payload.AfterVersions[group.ID])
			if err != nil {
				return err
			}
			changed, _ := update.RowsAffected()
			if changed != 1 {
				return fmt.Errorf("%w: group %s changed while undoing", ErrVersionConflict, group.ID)
			}
			result.RestoredGroupIDs = append(result.RestoredGroupIDs, group.ID)
			result.Versions[group.ID] = payload.AfterVersions[group.ID] + 1
		}
		for _, groupID := range payload.CreatedGroupIDs {
			update, err := tx.ExecContext(ctx, `
				UPDATE content_groups SET deleted_at = ?, version = version + 1, updated_at = ?
				WHERE id = ? AND version = ?`, now, now, groupID, payload.AfterVersions[groupID])
			if err != nil {
				return err
			}
			changed, _ := update.RowsAffected()
			if changed != 1 {
				return fmt.Errorf("%w: group %s changed while undoing", ErrVersionConflict, groupID)
			}
			result.RetiredGroupIDs = append(result.RetiredGroupIDs, groupID)
			result.Versions[groupID] = payload.AfterVersions[groupID] + 1
		}
		for _, member := range payload.Members {
			update, err := tx.ExecContext(ctx, `
				UPDATE torrent_instances SET content_group_id = ?, assignment_source = ? WHERE id = ?`,
				member.BeforeGroupID, member.BeforeAssignmentSource, member.ID)
			if err != nil {
				return err
			}
			changed, _ := update.RowsAffected()
			if changed != 1 {
				return fmt.Errorf("%w: torrent instance %s changed while undoing", ErrVersionConflict, member.ID)
			}
		}
		update, err := tx.ExecContext(ctx, `
			UPDATE group_operations SET undone_at = ? WHERE id = ? AND undone_at IS NULL`, now, operationID)
		if err != nil {
			return err
		}
		changed, _ := update.RowsAffected()
		if changed != 1 {
			return fmt.Errorf("%w: group operation was already undone", ErrVersionConflict)
		}
		sort.Strings(result.RestoredGroupIDs)
		sort.Strings(result.RetiredGroupIDs)
		return nil
	})
	if err != nil {
		return UndoGroupOperationResult{}, err
	}
	return result, nil
}

func insertGroupOperationTx(
	ctx context.Context,
	tx *sql.Tx,
	operationID, operationType, contentGroupID string,
	beforeVersion, afterVersion int,
	payload groupOperationPayload,
	createdAt int64,
) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode group operation payload: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO group_operations(
			id, operation_type, content_group_id, before_version, after_version, payload_json, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?)`, operationID, operationType, contentGroupID,
		beforeVersion, afterVersion, string(payloadJSON), createdAt)
	return err
}

func loadActiveGroupStatesTx(ctx context.Context, tx *sql.Tx, groupIDs []string) ([]groupOperationGroupState, error) {
	states := make([]groupOperationGroupState, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		var state groupOperationGroupState
		var locked int
		var deletedAt sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT id, auto_key, display_name, mode, confidence, locked, version, deleted_at
			FROM content_groups WHERE id = ? AND deleted_at IS NULL`, groupID).
			Scan(&state.ID, &state.AutoKey, &state.DisplayName, &state.Mode, &state.Confidence,
				&locked, &state.Version, &deletedAt)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		state.Locked = locked != 0
		if deletedAt.Valid {
			value := deletedAt.Int64
			state.DeletedAt = &value
		}
		states = append(states, state)
	}
	return states, nil
}

func loadSpecificGroupMembersTx(
	ctx context.Context,
	tx *sql.Tx,
	groupID string,
	instanceIDs []string,
) ([]groupOperationMemberState, error) {
	members := make([]groupOperationMemberState, 0, len(instanceIDs))
	for _, instanceID := range instanceIDs {
		var member groupOperationMemberState
		err := tx.QueryRowContext(ctx, `
			SELECT id, content_group_id, assignment_source FROM torrent_instances
			WHERE id = ? AND content_group_id = ? AND deleted_at IS NULL`, instanceID, groupID).
			Scan(&member.ID, &member.BeforeGroupID, &member.BeforeAssignmentSource)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w: instance %s no longer belongs to group %s", ErrVersionConflict, instanceID, groupID)
			}
			return nil, err
		}
		members = append(members, member)
	}
	return members, nil
}

func loadMembersForGroupsTx(
	ctx context.Context,
	tx *sql.Tx,
	groupIDs []string,
) ([]groupOperationMemberState, error) {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(groupIDs)), ",")
	args := make([]any, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		args = append(args, groupID)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, content_group_id, assignment_source FROM torrent_instances
		WHERE deleted_at IS NULL AND content_group_id IN (`+placeholders+") ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	members := make([]groupOperationMemberState, 0)
	for rows.Next() {
		var member groupOperationMemberState
		if err := rows.Scan(&member.ID, &member.BeforeGroupID, &member.BeforeAssignmentSource); err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func verifyGroupOperationStateTx(ctx context.Context, tx *sql.Tx, payload groupOperationPayload) error {
	if len(payload.AfterVersions) == 0 {
		return errors.New("group operation payload has no affected groups")
	}
	for groupID, expectedVersion := range payload.AfterVersions {
		var actualVersion int
		var deletedAt sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT version, deleted_at FROM content_groups WHERE id = ?`, groupID).
			Scan(&actualVersion, &deletedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: affected group %s no longer exists", ErrVersionConflict, groupID)
			}
			return err
		}
		expectedDeleted, ok := payload.AfterDeleted[groupID]
		if !ok || actualVersion != expectedVersion || deletedAt.Valid != expectedDeleted {
			return fmt.Errorf(
				"%w: group %s expected version %d (deleted=%t), got %d (deleted=%t)",
				ErrVersionConflict, groupID, expectedVersion, expectedDeleted, actualVersion, deletedAt.Valid,
			)
		}
	}
	for _, member := range payload.Members {
		var groupID, assignmentSource string
		var deletedAt sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT content_group_id, assignment_source, deleted_at FROM torrent_instances WHERE id = ?`, member.ID).
			Scan(&groupID, &assignmentSource, &deletedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: affected torrent instance %s no longer exists", ErrVersionConflict, member.ID)
			}
			return err
		}
		if deletedAt.Valid || groupID != member.AfterGroupID || assignmentSource != member.AfterAssignmentSource {
			return fmt.Errorf("%w: torrent instance %s membership changed", ErrVersionConflict, member.ID)
		}
	}
	return nil
}

func uniqueSortedIDs(ids []string) ([]string, bool) {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	duplicate := false
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			duplicate = true
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result, duplicate
}

func (s *Store) SetGroupLock(ctx context.Context, id string, expectedVersion int, locked bool) error {
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
            UPDATE content_groups SET locked = ?, version = version + 1, updated_at = ?
            WHERE id = ? AND version = ? AND deleted_at IS NULL`,
			boolInt(locked), s.now().Unix(), id, expectedVersion)
		if err != nil {
			return fmt.Errorf("update group lock: %w", err)
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return ErrVersionConflict
		}
		return nil
	})
}

func (s *Store) RestoreAutomaticGroup(ctx context.Context, id string, expectedVersion int) error {
	now := s.now().Unix()
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if err := checkGroupVersionsTx(ctx, tx, map[string]int{id: expectedVersion}); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
            SELECT DISTINCT suggested_content_group_id, suggested_content_auto_key, name
            FROM torrent_instances WHERE content_group_id = ? AND deleted_at IS NULL`, id)
		if err != nil {
			return err
		}
		type target struct{ id, key, name string }
		var targets []target
		for rows.Next() {
			var item target
			if err := rows.Scan(&item.id, &item.key, &item.name); err != nil {
				_ = rows.Close()
				return err
			}
			targets = append(targets, item)
		}
		_ = rows.Close()
		if len(targets) == 0 {
			return ErrNotFound
		}
		for _, item := range targets {
			if item.id == "" {
				return errors.New("group member has no automatic suggestion")
			}
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO content_groups(id, auto_key, display_name, mode, confidence, created_at, updated_at)
                VALUES(?, ?, ?, 'auto', 'tentative', ?, ?)
                ON CONFLICT(id) DO UPDATE SET deleted_at = NULL, updated_at = excluded.updated_at`,
				item.id, item.key, item.name, now, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
                UPDATE torrent_instances SET content_group_id = suggested_content_group_id, assignment_source = 'auto'
                WHERE content_group_id = ? AND suggested_content_group_id = ?`, id, item.id); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE content_groups
            SET deleted_at = ?, version = version + 1, updated_at = ? WHERE id = ?`, now, now, id)
		return err
	})
}

func checkGroupVersionsTx(ctx context.Context, tx *sql.Tx, expected map[string]int) error {
	for id, version := range expected {
		var actual int
		if err := tx.QueryRowContext(ctx, "SELECT version FROM content_groups WHERE id = ? AND deleted_at IS NULL", id).Scan(&actual); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if actual != version {
			return fmt.Errorf("%w: group %s expected %d, got %d", ErrVersionConflict, id, version, actual)
		}
	}
	return nil
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(value)
}
