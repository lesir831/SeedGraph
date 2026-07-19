package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type IYUUSiteQuery struct {
	Query  string
	Status string
	Limit  int
	Offset int
}

// ListIYUUSitesPage returns the IYUU catalog enriched with the number of
// distinct, active Tracker identities currently mapped to each site. The
// catalog itself remains independent from observed announce identities.
func (s *Store) ListIYUUSitesPage(ctx context.Context, query IYUUSiteQuery) ([]IYUUSite, IYUUSyncState, int, error) {
	query.Query = strings.ToLower(strings.TrimSpace(query.Query))
	if query.Status == "" {
		query.Status = TrackerMappingStatusAll
	}
	if query.Limit < 1 || query.Limit > 200 {
		return nil, IYUUSyncState{}, 0, fmt.Errorf("limit must be between 1 and 200")
	}
	if query.Offset < 0 || query.Offset > 1_000_000 {
		return nil, IYUUSyncState{}, 0, fmt.Errorf("offset must be between 0 and 1000000")
	}
	switch query.Status {
	case TrackerMappingStatusAll, TrackerMappingStatusMapped, TrackerMappingStatusUnmapped:
	default:
		return nil, IYUUSyncState{}, 0, fmt.Errorf("status must be all, mapped, or unmapped")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, IYUUSyncState{}, 0, fmt.Errorf("begin IYUU catalog page read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := readIYUUSyncState(ctx, tx)
	if err != nil {
		return nil, IYUUSyncState{}, 0, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT iy.remote_id, iy.slug, iy.nickname, iy.base_url,
		       iy.download_page, iy.details_page, iy.is_https,
		       iy.cookie_required, iy.first_seen_at, iy.last_seen_at,
		       COALESCE(mapped.mapping_count, 0)
		FROM iyuu_sites iy
		LEFT JOIN sites si ON si.iyuu_remote_id = iy.remote_id
		LEFT JOIN (
			SELECT identities.site_id, COUNT(*) AS mapping_count
			FROM (
				SELECT DISTINCT tt.site_id, tt.host_identity, tt.path_hint
				FROM torrent_trackers tt
				JOIN torrent_instances ti ON ti.id = tt.instance_id
				WHERE tt.site_id IS NOT NULL AND ti.deleted_at IS NULL
			) identities
			GROUP BY identities.site_id
		) mapped ON mapped.site_id = si.id
		ORDER BY iy.nickname COLLATE NOCASE, iy.slug COLLATE NOCASE, iy.remote_id`)
	if err != nil {
		return nil, IYUUSyncState{}, 0, fmt.Errorf("list IYUU site page: %w", err)
	}
	defer func() { _ = rows.Close() }()

	filtered := make([]IYUUSite, 0)
	for rows.Next() {
		var item IYUUSite
		var cookieRequired int
		var firstSeenAt, lastSeenAt int64
		if err := rows.Scan(
			&item.RemoteID, &item.Slug, &item.Nickname, &item.BaseURL,
			&item.DownloadPage, &item.DetailsPage, &item.IsHTTPS,
			&cookieRequired, &firstSeenAt, &lastSeenAt, &item.MappingCount,
		); err != nil {
			return nil, IYUUSyncState{}, 0, fmt.Errorf("scan IYUU site page: %w", err)
		}
		item.CookieRequired = cookieRequired != 0
		item.FirstSeenAt = time.Unix(firstSeenAt, 0).UTC()
		item.LastSeenAt = time.Unix(lastSeenAt, 0).UTC()
		item.Stale = state.LastSuccessAt != nil && item.LastSeenAt.Before(*state.LastSuccessAt)
		item.Mapped = item.MappingCount > 0
		if iyuuSiteMatchesQuery(item, query) {
			filtered = append(filtered, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, IYUUSyncState{}, 0, fmt.Errorf("iterate IYUU site page: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, IYUUSyncState{}, 0, fmt.Errorf("close IYUU site page: %w", err)
	}

	total := len(filtered)
	items := []IYUUSite{}
	if query.Offset < total {
		end := min(query.Offset+query.Limit, total)
		items = filtered[query.Offset:end]
	}
	if err := tx.Commit(); err != nil {
		return nil, IYUUSyncState{}, 0, fmt.Errorf("commit IYUU catalog page read: %w", err)
	}
	return items, state, total, nil
}

func iyuuSiteMatchesQuery(item IYUUSite, query IYUUSiteQuery) bool {
	if query.Status == TrackerMappingStatusMapped && !item.Mapped ||
		query.Status == TrackerMappingStatusUnmapped && item.Mapped {
		return false
	}
	if query.Query == "" {
		return true
	}
	for _, value := range []string{item.Slug, item.Nickname, item.BaseURL} {
		if strings.Contains(strings.ToLower(value), query.Query) {
			return true
		}
	}
	return false
}
