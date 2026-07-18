package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

type TrackerRule struct {
	ID          string    `json:"id"`
	HostPattern string    `json:"host_pattern"`
	PathPrefix  string    `json:"path_prefix"`
	SiteID      string    `json:"site_id"`
	SiteName    string    `json:"site_name"`
	DisplayName string    `json:"display_name"`
	Source      string    `json:"source"`
	Priority    int       `json:"priority"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateTrackerRuleParams struct {
	HostPattern string
	PathPrefix  string
	SiteName    string
	DisplayName string
}

func (s *Store) CreateCustomTrackerRule(ctx context.Context, params CreateTrackerRuleParams) (TrackerRule, error) {
	host, err := normalizeTrackerHost(params.HostPattern)
	if err != nil {
		return TrackerRule{}, err
	}
	params.PathPrefix = sanitizePathHint(params.PathPrefix)
	params.SiteName = strings.TrimSpace(params.SiteName)
	params.DisplayName = strings.TrimSpace(params.DisplayName)
	if params.SiteName == "" || params.DisplayName == "" {
		return TrackerRule{}, errors.New("site name and display name are required")
	}

	siteID := uuid.NewString()
	ruleID := uuid.NewString()
	now := s.now().Unix()
	err = s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT id FROM sites WHERE name = ?", params.SiteName).Scan(&siteID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("look up site: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO sites(id, name, display_name, source, created_at, updated_at)
            VALUES(?, ?, ?, 'custom', ?, ?)
            ON CONFLICT(name) DO UPDATE SET
                display_name = excluded.display_name,
                source = CASE WHEN sites.source = 'custom' THEN 'custom' ELSE sites.source END,
                updated_at = excluded.updated_at`,
			siteID, params.SiteName, params.DisplayName, now, now); err != nil {
			return fmt.Errorf("upsert site: %w", err)
		}
		if err := tx.QueryRowContext(ctx, "SELECT id FROM sites WHERE name = ?", params.SiteName).Scan(&siteID); err != nil {
			return fmt.Errorf("resolve site after upsert: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO tracker_rules(
                id, host_pattern, path_prefix, site_id, source, priority, created_at, updated_at
            ) VALUES(?, ?, ?, ?, 'custom', 1000, ?, ?)`,
			ruleID, host, params.PathPrefix, siteID, now, now); err != nil {
			return fmt.Errorf("insert tracker rule: %w", err)
		}
		return nil
	})
	if err != nil {
		return TrackerRule{}, err
	}
	return s.GetTrackerRule(ctx, ruleID)
}

func (s *Store) GetTrackerRule(ctx context.Context, id string) (TrackerRule, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT r.id, r.host_pattern, r.path_prefix, r.site_id, s.name, s.display_name,
               r.source, r.priority, r.created_at, r.updated_at
        FROM tracker_rules r JOIN sites s ON s.id = r.site_id WHERE r.id = ?`, id)
	rule, err := scanTrackerRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TrackerRule{}, ErrNotFound
	}
	return rule, err
}

func (s *Store) ListTrackerRules(ctx context.Context) ([]TrackerRule, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT r.id, r.host_pattern, r.path_prefix, r.site_id, s.name, s.display_name,
               r.source, r.priority, r.created_at, r.updated_at
        FROM tracker_rules r JOIN sites s ON s.id = r.site_id
        ORDER BY r.priority DESC, s.display_name COLLATE NOCASE, r.host_pattern`)
	if err != nil {
		return nil, fmt.Errorf("list tracker rules: %w", err)
	}
	defer rows.Close()
	result := make([]TrackerRule, 0)
	for rows.Next() {
		rule, err := scanTrackerRule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, rule)
	}
	return result, rows.Err()
}

func (s *Store) DeleteCustomTrackerRule(ctx context.Context, id string) error {
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM tracker_rules WHERE id = ? AND source = 'custom'", id)
		if err != nil {
			return fmt.Errorf("delete tracker rule: %w", err)
		}
		return requireAffected(result)
	})
}

func scanTrackerRule(row scanner) (TrackerRule, error) {
	var rule TrackerRule
	var createdAt, updatedAt int64
	if err := row.Scan(
		&rule.ID, &rule.HostPattern, &rule.PathPrefix, &rule.SiteID,
		&rule.SiteName, &rule.DisplayName, &rule.Source, &rule.Priority,
		&createdAt, &updatedAt,
	); err != nil {
		return TrackerRule{}, err
	}
	rule.CreatedAt = time.Unix(createdAt, 0).UTC()
	rule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rule, nil
}

// TrackerIdentity strips credentials, query parameters, fragments, and ports.
// Only the normalized host and a static path hint are suitable for persistence.
func TrackerIdentity(raw string) (host, pathHint string, err error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", "", errors.New("invalid tracker URL")
	}
	host, err = normalizeTrackerHost(parsed.Hostname())
	if err != nil {
		return "", "", err
	}
	return host, sanitizeObservedTrackerPath(parsed.EscapedPath()), nil
}

func normalizeTrackerHost(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" {
		value = parsed.Hostname()
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.TrimSuffix(value, ".")
	if value == "" || strings.ContainsAny(value, "/?#@") {
		return "", errors.New("invalid tracker host pattern")
	}
	return value, nil
}

func sanitizePathHint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return ""
	}
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		value = value[:index]
	}
	segments := strings.Split(value, "/")
	for i, segment := range segments {
		lower := strings.ToLower(segment)
		if len(segment) >= 24 || strings.Contains(lower, "passkey") || strings.Contains(lower, "token") || strings.Contains(lower, "auth") {
			segments[i] = "*"
		}
	}
	return strings.Join(segments, "/")
}

// sanitizeObservedTrackerPath uses an allowlist because an arbitrary path
// segment may itself be a passkey even when it is short or has no revealing
// name. User-authored rules still accept explicit static prefixes through
// sanitizePathHint; remotely observed values retain only common endpoint
// structure.
func sanitizeObservedTrackerPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return ""
	}
	allowed := map[string]struct{}{
		"announce": {}, "announce.php": {}, "tracker": {}, "tracker.php": {},
		"api": {}, "v1": {}, "v2": {}, "v3": {}, "bt": {}, "torrent": {},
	}
	segments := strings.Split(value, "/")
	lastRedacted := false
	for index, segment := range segments {
		if segment == "" {
			continue
		}
		lower := strings.ToLower(segment)
		if _, ok := allowed[lower]; ok {
			lastRedacted = false
			continue
		}
		if lastRedacted {
			segments[index] = ""
			continue
		}
		segments[index] = "*"
		lastRedacted = true
	}
	result := strings.Join(segments, "/")
	for strings.Contains(result, "//") {
		result = strings.ReplaceAll(result, "//", "/")
	}
	return strings.TrimSuffix(result, "/")
}
