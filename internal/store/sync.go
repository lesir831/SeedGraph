package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type RuntimeRecord struct {
	Status             string
	Progress           float64
	Ratio              float64
	UploadedBytes      int64
	DownloadedBytes    int64
	UploadSpeed        int64
	DownloadSpeed      int64
	RuntimeFingerprint string
}

type TrackerRecord struct {
	HostIdentity string
	PathHint     string
	SiteID       string
}

type TorrentRecord struct {
	ID                  string
	DownloaderID        string
	StableHashKey       string
	RemoteID            string
	Name                string
	SourcePath          string
	CanonicalPath       string
	StorageID           string
	WantedBytes         int64
	ManifestFingerprint string
	SelectedFileCount   int
	MetadataFingerprint string
	ContentGroupID      string
	ContentGroupAutoKey string
	DataGroupID         string
	DataGroupAutoKey    string
	Confidence          string
	Runtime             RuntimeRecord
	Trackers            []TrackerRecord
}

type ApplySyncParams struct {
	RunID               string
	DownloaderID        string
	Mode                string
	Complete            bool
	CursorBefore        string
	CursorAfter         string
	Torrents            []TorrentRecord
	RemovedStableHashes []string
	RemovedRemoteIDs    []string
}

type ApplySyncResult struct {
	RunID        string `json:"run_id"`
	SeenCount    int    `json:"seen_count"`
	ChangedCount int    `json:"changed_count"`
	RemovedCount int    `json:"removed_count"`
}

func (s *Store) ApplySync(ctx context.Context, params ApplySyncParams) (ApplySyncResult, error) {
	if params.RunID == "" {
		params.RunID = uuid.NewString()
	}
	result := ApplySyncResult{RunID: params.RunID, SeenCount: len(params.Torrents)}
	now := s.now().Unix()
	err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO sync_runs(
                id, downloader_id, mode, status, complete, cursor_before, cursor_after, started_at
            ) VALUES(?, ?, ?, 'running', ?, ?, ?, ?)`,
			params.RunID, params.DownloaderID, params.Mode, boolInt(params.Complete),
			params.CursorBefore, params.CursorAfter, now); err != nil {
			return fmt.Errorf("start sync run: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
            CREATE TEMP TABLE IF NOT EXISTS seedgraph_sync_seen (
                downloader_id TEXT NOT NULL,
                stable_hash_key TEXT NOT NULL,
                PRIMARY KEY (downloader_id, stable_hash_key)
            ) WITHOUT ROWID`); err != nil {
			return fmt.Errorf("create sync seen table: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM seedgraph_sync_seen WHERE downloader_id = ?", params.DownloaderID); err != nil {
			return fmt.Errorf("clear sync seen rows: %w", err)
		}

		for _, torrent := range params.Torrents {
			changed, err := s.upsertTorrentTx(ctx, tx, torrent, now)
			if err != nil {
				return err
			}
			if changed {
				result.ChangedCount++
			}
			if _, err := tx.ExecContext(ctx, `
                INSERT OR IGNORE INTO seedgraph_sync_seen(downloader_id, stable_hash_key) VALUES(?, ?)`,
				params.DownloaderID, torrent.StableHashKey); err != nil {
				return fmt.Errorf("record seen torrent: %w", err)
			}
		}

		if params.Complete {
			queryResult, err := tx.ExecContext(ctx, `
                UPDATE torrent_instances
                SET deleted_at = ?
                WHERE downloader_id = ? AND deleted_at IS NULL
                  AND NOT EXISTS (
                    SELECT 1 FROM seedgraph_sync_seen seen
                    WHERE seen.downloader_id = torrent_instances.downloader_id
                      AND seen.stable_hash_key = torrent_instances.stable_hash_key
                  )`, now, params.DownloaderID)
			if err != nil {
				return fmt.Errorf("tombstone torrents absent from complete snapshot: %w", err)
			}
			removed, _ := queryResult.RowsAffected()
			result.RemovedCount += int(removed)
		}
		for _, hash := range params.RemovedStableHashes {
			queryResult, err := tx.ExecContext(ctx, `
                UPDATE torrent_instances SET deleted_at = ?
                WHERE downloader_id = ? AND stable_hash_key = ? AND deleted_at IS NULL`, now, params.DownloaderID, hash)
			if err != nil {
				return fmt.Errorf("tombstone removed torrent by hash: %w", err)
			}
			removed, _ := queryResult.RowsAffected()
			result.RemovedCount += int(removed)
		}
		for _, remoteID := range params.RemovedRemoteIDs {
			queryResult, err := tx.ExecContext(ctx, `
                UPDATE torrent_instances SET deleted_at = ?
                WHERE downloader_id = ? AND remote_id = ? AND deleted_at IS NULL`, now, params.DownloaderID, remoteID)
			if err != nil {
				return fmt.Errorf("tombstone removed torrent by remote ID: %w", err)
			}
			removed, _ := queryResult.RowsAffected()
			result.RemovedCount += int(removed)
		}

		if _, err := tx.ExecContext(ctx, `
            UPDATE downloaders
            SET sync_cursor = ?, online = 1, last_error = '', last_success_at = ?, updated_at = ?
            WHERE id = ?`, params.CursorAfter, now, now, params.DownloaderID); err != nil {
			return fmt.Errorf("advance downloader cursor: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
            UPDATE sync_runs
            SET status = 'completed', seen_count = ?, changed_count = ?, removed_count = ?, finished_at = ?
            WHERE id = ?`, result.SeenCount, result.ChangedCount, result.RemovedCount, now, params.RunID); err != nil {
			return fmt.Errorf("complete sync run: %w", err)
		}
		return nil
	})
	if err != nil {
		return ApplySyncResult{}, err
	}
	return result, nil
}

func (s *Store) upsertTorrentTx(ctx context.Context, tx *sql.Tx, torrent TorrentRecord, now int64) (bool, error) {
	if torrent.ID == "" || torrent.StableHashKey == "" || torrent.DataGroupID == "" || torrent.ContentGroupID == "" {
		return false, fmt.Errorf("torrent identity, hash, data group, and content group are required")
	}
	if torrent.Confidence == "" {
		torrent.Confidence = "tentative"
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO data_groups(
            id, auto_key, storage_id, canonical_path, wanted_bytes,
            manifest_fingerprint, selected_file_count, confidence, created_at, updated_at
        ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(auto_key) DO UPDATE SET
            version = CASE WHEN
                data_groups.canonical_path <> excluded.canonical_path OR
                data_groups.wanted_bytes <> excluded.wanted_bytes OR
                data_groups.manifest_fingerprint <> excluded.manifest_fingerprint OR
                data_groups.selected_file_count <> excluded.selected_file_count OR
                data_groups.confidence <> excluded.confidence
                THEN data_groups.version + 1 ELSE data_groups.version END,
            canonical_path = excluded.canonical_path,
            wanted_bytes = excluded.wanted_bytes,
            manifest_fingerprint = excluded.manifest_fingerprint,
            selected_file_count = excluded.selected_file_count,
            confidence = excluded.confidence,
            updated_at = excluded.updated_at`,
		torrent.DataGroupID, torrent.DataGroupAutoKey, torrent.StorageID, torrent.CanonicalPath,
		torrent.WantedBytes, torrent.ManifestFingerprint, torrent.SelectedFileCount, torrent.Confidence, now, now); err != nil {
		return false, fmt.Errorf("upsert data group: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO content_groups(
            id, auto_key, display_name, mode, confidence, created_at, updated_at
        ) VALUES(?, ?, ?, 'auto', ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            display_name = CASE WHEN content_groups.mode = 'auto' THEN excluded.display_name ELSE content_groups.display_name END,
            confidence = CASE WHEN content_groups.mode = 'auto' THEN excluded.confidence ELSE content_groups.confidence END,
            updated_at = excluded.updated_at`,
		torrent.ContentGroupID, torrent.ContentGroupAutoKey, torrent.Name, torrent.Confidence, now, now); err != nil {
		return false, fmt.Errorf("upsert content group: %w", err)
	}

	var oldFingerprint string
	var oldDeletedAt sql.NullInt64
	err := tx.QueryRowContext(ctx, `
        SELECT metadata_fingerprint, deleted_at FROM torrent_instances
        WHERE downloader_id = ? AND stable_hash_key = ?`, torrent.DownloaderID, torrent.StableHashKey).
		Scan(&oldFingerprint, &oldDeletedAt)
	newRecord := err == sql.ErrNoRows
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("read existing torrent: %w", err)
	}
	changed := newRecord || oldFingerprint != torrent.MetadataFingerprint || oldDeletedAt.Valid

	if _, err := tx.ExecContext(ctx, `
        INSERT INTO torrent_instances(
            id, downloader_id, stable_hash_key, remote_id, name, source_path,
            canonical_path, storage_id, wanted_bytes, manifest_fingerprint,
            selected_file_count,
            metadata_fingerprint, suggested_content_group_id, suggested_content_auto_key,
            content_group_id, data_group_id, assignment_source,
            first_seen_at, last_seen_at, deleted_at
        ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'auto', ?, ?, NULL)
        ON CONFLICT(downloader_id, stable_hash_key) DO UPDATE SET
            remote_id = excluded.remote_id,
            name = excluded.name,
            source_path = excluded.source_path,
            canonical_path = excluded.canonical_path,
            storage_id = excluded.storage_id,
            wanted_bytes = excluded.wanted_bytes,
            manifest_fingerprint = excluded.manifest_fingerprint,
            selected_file_count = excluded.selected_file_count,
            metadata_fingerprint = excluded.metadata_fingerprint,
            suggested_content_group_id = excluded.suggested_content_group_id,
            suggested_content_auto_key = excluded.suggested_content_auto_key,
            data_group_id = excluded.data_group_id,
            content_group_id = CASE
                WHEN torrent_instances.assignment_source = 'manual' THEN torrent_instances.content_group_id
                ELSE excluded.content_group_id
            END,
            assignment_source = CASE
                WHEN torrent_instances.assignment_source = 'manual' THEN 'manual'
                ELSE 'auto'
            END,
            last_seen_at = excluded.last_seen_at,
            deleted_at = NULL`,
		torrent.ID, torrent.DownloaderID, torrent.StableHashKey, torrent.RemoteID,
		torrent.Name, torrent.SourcePath, torrent.CanonicalPath, torrent.StorageID,
		torrent.WantedBytes, torrent.ManifestFingerprint, torrent.SelectedFileCount, torrent.MetadataFingerprint,
		torrent.ContentGroupID, torrent.ContentGroupAutoKey,
		torrent.ContentGroupID, torrent.DataGroupID, now, now); err != nil {
		return false, fmt.Errorf("upsert torrent instance: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO torrent_runtime(
            instance_id, status, progress, ratio, uploaded_bytes, downloaded_bytes,
            upload_speed, download_speed, runtime_fingerprint, updated_at
        ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(instance_id) DO UPDATE SET
            status = excluded.status,
            progress = excluded.progress,
            ratio = excluded.ratio,
            uploaded_bytes = excluded.uploaded_bytes,
            downloaded_bytes = excluded.downloaded_bytes,
            upload_speed = excluded.upload_speed,
            download_speed = excluded.download_speed,
            runtime_fingerprint = excluded.runtime_fingerprint,
            updated_at = excluded.updated_at
        WHERE torrent_runtime.runtime_fingerprint <> excluded.runtime_fingerprint`,
		torrent.ID, torrent.Runtime.Status, torrent.Runtime.Progress, torrent.Runtime.Ratio,
		torrent.Runtime.UploadedBytes, torrent.Runtime.DownloadedBytes,
		torrent.Runtime.UploadSpeed, torrent.Runtime.DownloadSpeed,
		torrent.Runtime.RuntimeFingerprint, now); err != nil {
		return false, fmt.Errorf("upsert torrent runtime: %w", err)
	}
	if changed {
		if _, err := tx.ExecContext(ctx, "DELETE FROM torrent_trackers WHERE instance_id = ?", torrent.ID); err != nil {
			return false, fmt.Errorf("replace torrent trackers: %w", err)
		}
		for _, tracker := range torrent.Trackers {
			var siteID any
			if tracker.SiteID != "" {
				siteID = tracker.SiteID
			}
			if _, err := tx.ExecContext(ctx, `
                INSERT OR IGNORE INTO torrent_trackers(instance_id, host_identity, path_hint, site_id)
                VALUES(?, ?, ?, ?)`, torrent.ID, tracker.HostIdentity, tracker.PathHint, siteID); err != nil {
				return false, fmt.Errorf("insert torrent tracker identity: %w", err)
			}
		}
	}
	return changed, nil
}

func (s *Store) RecordFailedSync(ctx context.Context, downloaderID, mode, cursorBefore string, syncErr error) error {
	now := s.now().Unix()
	errText := syncErr.Error()
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO sync_runs(
                id, downloader_id, mode, status, cursor_before, error, started_at, finished_at
            ) VALUES(?, ?, ?, 'failed', ?, ?, ?, ?)`,
			uuid.NewString(), downloaderID, mode, cursorBefore, errText, now, now); err != nil {
			return fmt.Errorf("record failed sync run: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
            UPDATE downloaders SET online = 0, last_error = ?, updated_at = ? WHERE id = ?`,
			errText, now, downloaderID); err != nil {
			return fmt.Errorf("mark downloader sync failure: %w", err)
		}
		return nil
	})
}

type SyncRun struct {
	ID           string     `json:"id"`
	DownloaderID string     `json:"downloader_id"`
	Mode         string     `json:"mode"`
	Status       string     `json:"status"`
	Complete     bool       `json:"complete"`
	SeenCount    int        `json:"seen_count"`
	ChangedCount int        `json:"changed_count"`
	RemovedCount int        `json:"removed_count"`
	Error        string     `json:"error"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

func (s *Store) ListSyncRuns(ctx context.Context, limit int) ([]SyncRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, downloader_id, mode, status, complete, seen_count, changed_count,
               removed_count, error, started_at, finished_at
        FROM sync_runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list sync runs: %w", err)
	}
	defer rows.Close()
	runs := make([]SyncRun, 0)
	for rows.Next() {
		var run SyncRun
		var complete int
		var started int64
		var finished sql.NullInt64
		if err := rows.Scan(&run.ID, &run.DownloaderID, &run.Mode, &run.Status, &complete,
			&run.SeenCount, &run.ChangedCount, &run.RemovedCount, &run.Error, &started, &finished); err != nil {
			return nil, err
		}
		run.Complete = complete != 0
		run.StartedAt = time.Unix(started, 0).UTC()
		if finished.Valid {
			value := time.Unix(finished.Int64, 0).UTC()
			run.FinishedAt = &value
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func JSONFingerprint(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
