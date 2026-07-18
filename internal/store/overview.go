package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Overview struct {
	LogicalResources  int64      `json:"logical_resources"`
	TorrentTasks      int64      `json:"torrent_tasks"`
	LogicalBytes      int64      `json:"logical_bytes"`
	RawTaskBytes      int64      `json:"raw_task_bytes"`
	KnownSites        int64      `json:"known_sites"`
	UnknownTrackers   int64      `json:"unknown_trackers"`
	OnlineDownloaders int64      `json:"online_downloaders"`
	TotalDownloaders  int64      `json:"total_downloaders"`
	StaleGroups       int64      `json:"stale_groups"`
	LastSyncAt        *time.Time `json:"last_sync_at,omitempty"`
}

func (s *Store) GetOverview(ctx context.Context, staleBefore time.Time) (Overview, error) {
	if staleBefore.IsZero() {
		staleBefore = s.now().Add(-5 * time.Minute)
	}
	var overview Overview
	queries := []struct {
		query string
		args  []any
		dest  *int64
	}{
		{`SELECT COUNT(*) FROM content_groups cg WHERE cg.deleted_at IS NULL
            AND EXISTS (SELECT 1 FROM torrent_instances ti WHERE ti.content_group_id = cg.id AND ti.deleted_at IS NULL)`, nil, &overview.LogicalResources},
		{"SELECT COUNT(*) FROM torrent_instances WHERE deleted_at IS NULL", nil, &overview.TorrentTasks},
		{`SELECT COALESCE(SUM(size_bytes), 0) FROM (
            SELECT MAX(ti.wanted_bytes) size_bytes FROM torrent_instances ti
            WHERE ti.deleted_at IS NULL GROUP BY ti.content_group_id
        )`, nil, &overview.LogicalBytes},
		{"SELECT COALESCE(SUM(wanted_bytes), 0) FROM torrent_instances WHERE deleted_at IS NULL", nil, &overview.RawTaskBytes},
		{"SELECT COUNT(*) FROM sites", nil, &overview.KnownSites},
		{"SELECT COUNT(DISTINCT host_identity) FROM torrent_trackers WHERE site_id IS NULL", nil, &overview.UnknownTrackers},
		{"SELECT COUNT(*) FROM downloaders WHERE enabled = 1 AND online = 1", nil, &overview.OnlineDownloaders},
		{"SELECT COUNT(*) FROM downloaders WHERE enabled = 1", nil, &overview.TotalDownloaders},
		{`SELECT COUNT(DISTINCT ti.content_group_id)
            FROM torrent_instances ti JOIN downloaders d ON d.id = ti.downloader_id
            WHERE ti.deleted_at IS NULL AND COALESCE(d.last_success_at, 0) < ?`, []any{staleBefore.Unix()}, &overview.StaleGroups},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(ctx, item.query, item.args...).Scan(item.dest); err != nil {
			return Overview{}, fmt.Errorf("query overview: %w", err)
		}
	}
	var lastSync sql.NullInt64
	if err := s.db.QueryRowContext(ctx, "SELECT MAX(last_success_at) FROM downloaders").Scan(&lastSync); err != nil {
		return Overview{}, fmt.Errorf("query last sync time: %w", err)
	}
	overview.LastSyncAt = scanNullableTime(lastSync)
	return overview, nil
}

type AuditEvent struct {
	ID         string         `json:"id"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	Status     string         `json:"status"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Details    map[string]any `json:"details"`
	CreatedAt  time.Time      `json:"created_at"`
}

func (s *Store) AddAuditEvent(ctx context.Context, event AuditEvent) error {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.Actor == "" {
		event.Actor = "system"
	}
	if event.Status == "" {
		event.Status = "success"
	}
	encoded, err := json.Marshal(event.Details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO audit_logs(id, actor, action, status, target_type, target_id, details_json, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.Actor, event.Action, event.Status, event.TargetType,
			event.TargetID, string(encoded), s.now().Unix())
		if err != nil {
			return fmt.Errorf("insert audit event: %w", err)
		}
		return nil
	})
}

func (s *Store) ListAuditEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, actor, action, status, target_type, target_id, details_json, created_at
        FROM audit_logs ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	events := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		var details string
		var createdAt int64
		if err := rows.Scan(&event.ID, &event.Actor, &event.Action, &event.Status, &event.TargetType,
			&event.TargetID, &details, &createdAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(details), &event.Details); err != nil {
			event.Details = map[string]any{"decode_error": true}
		}
		event.CreatedAt = time.Unix(createdAt, 0).UTC()
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) ListAuditEventsPage(ctx context.Context, action, status string, limit, offset int) ([]AuditEvent, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where := ""
	args := make([]any, 0, 3)
	if action != "" {
		where = " WHERE action = ?"
		args = append(args, action)
	}
	if status != "" && status != "all" {
		if where == "" {
			where = " WHERE status = ?"
		} else {
			where += " AND status = ?"
		}
		args = append(args, status)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs"+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit events: %w", err)
	}
	queryArgs := append(append([]any(nil), args...), limit, offset)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, actor, action, status, target_type, target_id, details_json, created_at
        FROM audit_logs`+where+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	events := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		var details string
		var createdAt int64
		if err := rows.Scan(&event.ID, &event.Actor, &event.Action, &event.Status, &event.TargetType,
			&event.TargetID, &details, &createdAt); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal([]byte(details), &event.Details); err != nil {
			event.Details = map[string]any{"decode_error": true}
		}
		event.CreatedAt = time.Unix(createdAt, 0).UTC()
		events = append(events, event)
	}
	return events, total, rows.Err()
}

func scanNullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed := time.Unix(value.Int64, 0).UTC()
	return &parsed
}
