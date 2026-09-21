package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/lesir831/SeedGraph/internal/domain"
)

// Fetch all page summaries in one query, without joining trackers: a task's
// transfer counters must be counted once even when it has several trackers.
func (s *Store) populateTorrentGroupSummaries(ctx context.Context, groups []TorrentGroup) error {
	if len(groups) == 0 {
		return nil
	}
	byID := make(map[string]*TorrentGroup, len(groups))
	args := make([]any, 0, len(groups))
	for i := range groups {
		group := &groups[i]
		group.Categories = []string{}
		group.Paths = []string{}
		group.Downloaders = []string{}
		byID[group.ID] = group
		args = append(args, group.ID)
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT ti.content_group_id, ti.canonical_path, d.name, tr.ratio,
               tr.uploaded_bytes, tr.downloaded_bytes, tr.upload_speed, tr.download_speed
        FROM torrent_instances ti
        JOIN downloaders d ON d.id = ti.downloader_id
        LEFT JOIN torrent_runtime tr ON tr.instance_id = ti.id
        WHERE ti.deleted_at IS NULL AND ti.content_group_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)
        ORDER BY ti.content_group_id, ti.id`, args...)
	if err != nil {
		return fmt.Errorf("list torrent group summaries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, contentPath, downloaderName string
		var ratio sql.NullFloat64
		var uploaded, downloaded, uploadSpeed, downloadSpeed sql.NullInt64
		if err := rows.Scan(&id, &contentPath, &downloaderName, &ratio, &uploaded, &downloaded, &uploadSpeed, &downloadSpeed); err != nil {
			return err
		}
		group := byID[id]
		group.Paths = appendUniqueSummaryValue(group.Paths, contentPath)
		group.Categories = appendUniqueSummaryValue(group.Categories, domain.PathCategory(contentPath))
		group.Downloaders = appendUniqueSummaryValue(group.Downloaders, downloaderName)
		if !ratio.Valid {
			continue
		}
		if group.Runtime == nil {
			group.Runtime = &TorrentGroupRuntime{}
		}
		runtime := group.Runtime
		// Negative protocol sentinel values are not numeric share ratios.
		if ratio.Float64 >= 0 {
			value := ratio.Float64
			if runtime.RatioMin == nil || value < *runtime.RatioMin {
				runtime.RatioMin = &value
			}
			if runtime.RatioMax == nil || value > *runtime.RatioMax {
				runtime.RatioMax = &value
			}
		}
		runtime.UploadedBytes += uploaded.Int64
		runtime.DownloadedBytes += downloaded.Int64
		runtime.UploadSpeed += uploadSpeed.Int64
		runtime.DownloadSpeed += downloadSpeed.Int64
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range groups {
		sort.Strings(groups[i].Categories)
		sort.Strings(groups[i].Paths)
		sort.Strings(groups[i].Downloaders)
	}
	return nil
}

func appendUniqueSummaryValue(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
