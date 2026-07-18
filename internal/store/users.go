package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) EnsureAdmin(ctx context.Context, passwordHash string) error {
	now := s.now().Unix()
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
            INSERT INTO users(id, username, password_hash, created_at, updated_at)
            VALUES(1, 'admin', ?, ?, ?)
            ON CONFLICT(id) DO NOTHING`, passwordHash, now, now)
		if err != nil {
			return fmt.Errorf("ensure admin user: %w", err)
		}
		return nil
	})
}

func (s *Store) AdminPasswordHash(ctx context.Context) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id = 1").Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("admin user is not initialized")
	}
	if err != nil {
		return "", fmt.Errorf("read admin password hash: %w", err)
	}
	return hash, nil
}
