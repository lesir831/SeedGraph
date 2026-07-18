package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type PathMapping struct {
	ID           string `json:"id"`
	SourcePrefix string `json:"source_prefix"`
	TargetPrefix string `json:"target_prefix"`
	Position     int    `json:"position"`
}

type Downloader struct {
	ID                 string        `json:"id"`
	Name               string        `json:"name"`
	Kind               string        `json:"kind"`
	BaseURL            string        `json:"base_url"`
	UsernameCiphertext string        `json:"-"`
	PasswordCiphertext string        `json:"-"`
	StorageID          string        `json:"storage_id"`
	StorageName        string        `json:"storage_name"`
	PathMappings       []PathMapping `json:"path_mappings"`
	Enabled            bool          `json:"enabled"`
	Online             bool          `json:"online"`
	Version            string        `json:"version"`
	SyncCursor         string        `json:"-"`
	LastSuccessAt      *time.Time    `json:"last_success_at"`
	LastError          string        `json:"last_error"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
}

type CreateDownloaderParams struct {
	Name               string
	Kind               string
	BaseURL            string
	UsernameCiphertext string
	PasswordCiphertext string
	StorageID          string
	StorageName        string
	PathMappings       []PathMapping
	Enabled            bool
}

func (s *Store) CreateDownloader(ctx context.Context, params CreateDownloaderParams) (Downloader, error) {
	if params.IDValidationError() != nil {
		return Downloader{}, params.IDValidationError()
	}
	id := uuid.NewString()
	storageID := params.StorageID
	if storageID == "" {
		storageID = uuid.NewString()
	}
	if params.StorageName == "" {
		params.StorageName = params.Name + " storage"
	}
	now := s.now().Unix()
	err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO storages(id, name, created_at, updated_at) VALUES(?, ?, ?, ?)
            ON CONFLICT(id) DO UPDATE SET name = excluded.name, updated_at = excluded.updated_at`,
			storageID, params.StorageName, now, now); err != nil {
			return fmt.Errorf("upsert storage: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO downloaders(
                id, name, kind, base_url, username_ciphertext, password_ciphertext,
                storage_id, enabled, created_at, updated_at
            ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, params.Name, params.Kind, params.BaseURL, params.UsernameCiphertext,
			params.PasswordCiphertext, storageID, boolInt(params.Enabled), now, now); err != nil {
			return fmt.Errorf("insert downloader: %w", err)
		}
		for position, mapping := range params.PathMappings {
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO path_mappings(id, downloader_id, source_prefix, target_prefix, position)
                VALUES(?, ?, ?, ?, ?)`, uuid.NewString(), id, mapping.SourcePrefix, mapping.TargetPrefix, position); err != nil {
				return fmt.Errorf("insert path mapping: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Downloader{}, err
	}
	return s.GetDownloader(ctx, id)
}

func (p CreateDownloaderParams) IDValidationError() error {
	if p.Name == "" || p.BaseURL == "" {
		return errors.New("downloader name and base URL are required")
	}
	if p.Kind != "qbittorrent" && p.Kind != "transmission" {
		return errors.New("downloader kind must be qbittorrent or transmission")
	}
	return nil
}

func (s *Store) ListDownloaders(ctx context.Context) ([]Downloader, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT d.id, d.name, d.kind, d.base_url, d.username_ciphertext, d.password_ciphertext,
               d.storage_id, st.name, d.enabled, d.online, d.version, d.sync_cursor,
               d.last_success_at, d.last_error, d.created_at, d.updated_at
        FROM downloaders d
        JOIN storages st ON st.id = d.storage_id
        ORDER BY d.name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list downloaders: %w", err)
	}
	defer func() { _ = rows.Close() }()

	result := make([]Downloader, 0)
	for rows.Next() {
		downloader, err := scanDownloader(rows)
		if err != nil {
			return nil, err
		}
		downloader.PathMappings, err = s.listPathMappings(ctx, downloader.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, downloader)
	}
	return result, rows.Err()
}

func (s *Store) GetDownloader(ctx context.Context, id string) (Downloader, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT d.id, d.name, d.kind, d.base_url, d.username_ciphertext, d.password_ciphertext,
               d.storage_id, st.name, d.enabled, d.online, d.version, d.sync_cursor,
               d.last_success_at, d.last_error, d.created_at, d.updated_at
        FROM downloaders d
        JOIN storages st ON st.id = d.storage_id
        WHERE d.id = ?`, id)
	downloader, err := scanDownloader(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Downloader{}, ErrNotFound
	}
	if err != nil {
		return Downloader{}, err
	}
	downloader.PathMappings, err = s.listPathMappings(ctx, id)
	return downloader, err
}

func (s *Store) listPathMappings(ctx context.Context, downloaderID string) ([]PathMapping, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, source_prefix, target_prefix, position
        FROM path_mappings WHERE downloader_id = ? ORDER BY position, source_prefix`, downloaderID)
	if err != nil {
		return nil, fmt.Errorf("list path mappings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]PathMapping, 0)
	for rows.Next() {
		var mapping PathMapping
		if err := rows.Scan(&mapping.ID, &mapping.SourcePrefix, &mapping.TargetPrefix, &mapping.Position); err != nil {
			return nil, fmt.Errorf("scan path mapping: %w", err)
		}
		result = append(result, mapping)
	}
	return result, rows.Err()
}

func (s *Store) UpdateDownloaderConnectionState(ctx context.Context, id string, online bool, version, lastError string, successful bool) error {
	now := s.now().Unix()
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var result sql.Result
		var err error
		if successful {
			result, err = tx.ExecContext(ctx, `
                UPDATE downloaders
                SET online = ?, version = ?, last_error = ?, last_success_at = ?, updated_at = ?
                WHERE id = ?`, boolInt(online), version, lastError, now, now, id)
		} else {
			result, err = tx.ExecContext(ctx, `
                UPDATE downloaders
                SET online = ?, version = ?, last_error = ?, updated_at = ?
                WHERE id = ?`, boolInt(online), version, lastError, now, id)
		}
		if err != nil {
			return fmt.Errorf("update downloader state: %w", err)
		}
		return requireAffected(result)
	})
}

func (s *Store) DeleteDownloader(ctx context.Context, id string) error {
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM downloaders WHERE id = ?", id)
		if err != nil {
			return fmt.Errorf("delete downloader: %w", err)
		}
		return requireAffected(result)
	})
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDownloader(row scanner) (Downloader, error) {
	var downloader Downloader
	var enabled, online int
	var lastSuccess sql.NullInt64
	var createdAt, updatedAt int64
	if err := row.Scan(
		&downloader.ID, &downloader.Name, &downloader.Kind, &downloader.BaseURL,
		&downloader.UsernameCiphertext, &downloader.PasswordCiphertext,
		&downloader.StorageID, &downloader.StorageName, &enabled, &online,
		&downloader.Version, &downloader.SyncCursor, &lastSuccess, &downloader.LastError,
		&createdAt, &updatedAt,
	); err != nil {
		return Downloader{}, err
	}
	downloader.Enabled = enabled != 0
	downloader.Online = online != 0
	downloader.CreatedAt = time.Unix(createdAt, 0).UTC()
	downloader.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if lastSuccess.Valid {
		value := time.Unix(lastSuccess.Int64, 0).UTC()
		downloader.LastSuccessAt = &value
	}
	return downloader, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

var ErrNotFound = errors.New("not found")

func requireAffected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
