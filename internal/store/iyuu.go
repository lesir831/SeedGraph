package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type IYUUSiteInput struct {
	RemoteID       int64
	Slug           string
	Nickname       string
	BaseURL        string
	DownloadPage   string
	DetailsPage    string
	IsHTTPS        int
	CookieRequired int
}

type IYUUSite struct {
	RemoteID       int64     `json:"remote_id"`
	Slug           string    `json:"slug"`
	Nickname       string    `json:"nickname"`
	BaseURL        string    `json:"base_url"`
	DownloadPage   string    `json:"download_page"`
	DetailsPage    string    `json:"details_page"`
	IsHTTPS        int       `json:"is_https"`
	CookieRequired bool      `json:"cookie_required"`
	FirstSeenAt    time.Time `json:"first_seen_at"`
	LastSeenAt     time.Time `json:"last_seen_at"`
	Stale          bool      `json:"stale"`
}

type IYUUSyncState struct {
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     string     `json:"last_error"`
	SiteCount     int        `json:"site_count"`
}

// ApplyIYUUCatalog atomically records one fully validated remote snapshot.
// Rows missing from a later response are retained and exposed as stale so a
// transient or upstream catalog change cannot silently erase local metadata.
func (s *Store) ApplyIYUUCatalog(ctx context.Context, sites []IYUUSiteInput, fetchedAt time.Time) error {
	if fetchedAt.IsZero() {
		fetchedAt = s.now()
	}
	seenIDs := make(map[int64]struct{}, len(sites))
	seenSlugs := make(map[string]struct{}, len(sites))
	for index := range sites {
		sites[index].Slug = strings.TrimSpace(sites[index].Slug)
		sites[index].Nickname = strings.TrimSpace(sites[index].Nickname)
		sites[index].BaseURL = strings.ToLower(strings.TrimSpace(sites[index].BaseURL))
		if sites[index].RemoteID <= 0 || sites[index].Slug == "" || sites[index].BaseURL == "" ||
			sites[index].IsHTTPS < 0 || sites[index].IsHTTPS > 2 ||
			(sites[index].CookieRequired != 0 && sites[index].CookieRequired != 1) {
			return fmt.Errorf("invalid IYUU site at index %d", index)
		}
		if _, duplicate := seenIDs[sites[index].RemoteID]; duplicate {
			return fmt.Errorf("duplicate IYUU remote ID %d", sites[index].RemoteID)
		}
		if _, duplicate := seenSlugs[sites[index].Slug]; duplicate {
			return fmt.Errorf("duplicate IYUU site slug %q", sites[index].Slug)
		}
		seenIDs[sites[index].RemoteID] = struct{}{}
		seenSlugs[sites[index].Slug] = struct{}{}
	}
	now := fetchedAt.UTC().Unix()
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		for _, site := range sites {
			// Resolve the unlikely case where upstream reassigns either a numeric
			// ID or slug. The incoming snapshot has already been checked for
			// uniqueness, so removing only mismatched catalog rows is deterministic.
			if _, err := tx.ExecContext(ctx, `
                DELETE FROM iyuu_sites
                WHERE (remote_id = ? OR slug = ?) AND NOT (remote_id = ? AND slug = ?)`,
				site.RemoteID, site.Slug, site.RemoteID, site.Slug); err != nil {
				return fmt.Errorf("resolve IYUU site identity: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO iyuu_sites(
                    remote_id, slug, nickname, base_url, download_page, details_page,
                    is_https, cookie_required, first_seen_at, last_seen_at
                ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(remote_id) DO UPDATE SET
                    slug = excluded.slug,
                    nickname = excluded.nickname,
                    base_url = excluded.base_url,
                    download_page = excluded.download_page,
                    details_page = excluded.details_page,
                    is_https = excluded.is_https,
                    cookie_required = excluded.cookie_required,
                    last_seen_at = excluded.last_seen_at`,
				site.RemoteID, site.Slug, site.Nickname, site.BaseURL, site.DownloadPage,
				site.DetailsPage, site.IsHTTPS, site.CookieRequired, now, now); err != nil {
				return fmt.Errorf("upsert IYUU site %q: %w", site.Slug, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO iyuu_sync_state(id, last_attempt_at, last_success_at, last_error, site_count, updated_at)
            VALUES(1, ?, ?, '', ?, ?)
            ON CONFLICT(id) DO UPDATE SET
                last_attempt_at = excluded.last_attempt_at,
                last_success_at = excluded.last_success_at,
                last_error = '',
                site_count = excluded.site_count,
                updated_at = excluded.updated_at`, now, now, len(sites), now); err != nil {
			return fmt.Errorf("record IYUU sync success: %w", err)
		}
		return nil
	})
}

func (s *Store) RecordIYUUSyncFailure(ctx context.Context, attemptedAt time.Time, syncErr error) error {
	if attemptedAt.IsZero() {
		attemptedAt = s.now()
	}
	message := "IYUU catalog sync failed"
	if syncErr != nil {
		message = syncErr.Error()
	}
	if len(message) > 500 {
		message = message[:500]
	}
	now := attemptedAt.UTC().Unix()
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
            INSERT INTO iyuu_sync_state(id, last_attempt_at, last_error, updated_at)
            VALUES(1, ?, ?, ?)
            ON CONFLICT(id) DO UPDATE SET
                last_attempt_at = excluded.last_attempt_at,
                last_error = excluded.last_error,
                updated_at = excluded.updated_at`, now, message, now)
		if err != nil {
			return fmt.Errorf("record IYUU sync failure: %w", err)
		}
		return nil
	})
}

func (s *Store) ListIYUUSites(ctx context.Context) ([]IYUUSite, IYUUSyncState, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, IYUUSyncState{}, fmt.Errorf("begin IYUU catalog read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := readIYUUSyncState(ctx, tx)
	if err != nil {
		return nil, IYUUSyncState{}, err
	}
	rows, err := tx.QueryContext(ctx, `
        SELECT remote_id, slug, nickname, base_url, download_page, details_page,
               is_https, cookie_required, first_seen_at, last_seen_at
        FROM iyuu_sites ORDER BY nickname COLLATE NOCASE, slug`)
	if err != nil {
		return nil, IYUUSyncState{}, fmt.Errorf("list IYUU sites: %w", err)
	}
	defer rows.Close()
	items := make([]IYUUSite, 0)
	for rows.Next() {
		var item IYUUSite
		var cookieRequired int
		var firstSeenAt, lastSeenAt int64
		if err := rows.Scan(
			&item.RemoteID, &item.Slug, &item.Nickname, &item.BaseURL,
			&item.DownloadPage, &item.DetailsPage, &item.IsHTTPS, &cookieRequired,
			&firstSeenAt, &lastSeenAt,
		); err != nil {
			return nil, IYUUSyncState{}, fmt.Errorf("scan IYUU site: %w", err)
		}
		item.CookieRequired = cookieRequired != 0
		item.FirstSeenAt = time.Unix(firstSeenAt, 0).UTC()
		item.LastSeenAt = time.Unix(lastSeenAt, 0).UTC()
		item.Stale = state.LastSuccessAt != nil && item.LastSeenAt.Before(*state.LastSuccessAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, IYUUSyncState{}, fmt.Errorf("iterate IYUU sites: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, IYUUSyncState{}, fmt.Errorf("close IYUU sites query: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, IYUUSyncState{}, fmt.Errorf("commit IYUU catalog read: %w", err)
	}
	return items, state, nil
}

func (s *Store) IYUUSyncState(ctx context.Context) (IYUUSyncState, error) {
	return readIYUUSyncState(ctx, s.db)
}

type iyuuStateQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readIYUUSyncState(ctx context.Context, queryer iyuuStateQueryer) (IYUUSyncState, error) {
	var state IYUUSyncState
	var lastAttempt, lastSuccess sql.NullInt64
	err := queryer.QueryRowContext(ctx, `
        SELECT last_attempt_at, last_success_at, last_error, site_count
        FROM iyuu_sync_state WHERE id = 1`).Scan(
		&lastAttempt, &lastSuccess, &state.LastError, &state.SiteCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return IYUUSyncState{}, fmt.Errorf("read IYUU sync state: %w", err)
	}
	state.LastAttemptAt = scanNullableTime(lastAttempt)
	state.LastSuccessAt = scanNullableTime(lastSuccess)
	return state, nil
}
