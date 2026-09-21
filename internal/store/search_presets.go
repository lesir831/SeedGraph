package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrPresetNameConflict = errors.New("search preset name already exists")

type GroupSearchPreset struct {
	ID     string             `json:"id"`
	Name   string             `json:"name"`
	Filter *TorrentGroupQuery `json:"filter"`
}

func (s *Store) ListGroupSearchPresets(ctx context.Context) ([]GroupSearchPreset, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, filter_json FROM group_search_presets ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]GroupSearchPreset, 0)
	for rows.Next() {
		var item GroupSearchPreset
		var filterJSON string
		if err := rows.Scan(&item.ID, &item.Name, &filterJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(filterJSON), &item.Filter); err != nil {
			return nil, fmt.Errorf("decode search preset: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateGroupSearchPreset(ctx context.Context, name string, filter *TorrentGroupQuery) (GroupSearchPreset, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 80 {
		return GroupSearchPreset{}, fmt.Errorf("%w: 预设名称须为 1 到 80 个字符", ErrInvalidGroupFilter)
	}
	if filter == nil {
		return GroupSearchPreset{}, fmt.Errorf("%w: 请先配置高级搜索条件", ErrInvalidGroupFilter)
	}
	if _, _, err := compileTorrentGroupQuery(filter, nil); err != nil {
		return GroupSearchPreset{}, err
	}
	encoded, err := json.Marshal(filter)
	if err != nil {
		return GroupSearchPreset{}, err
	}
	item := GroupSearchPreset{ID: uuid.NewString(), Name: name, Filter: filter}
	result, err := s.db.ExecContext(ctx, `INSERT INTO group_search_presets (id, name, filter_json, created_at)
        VALUES (?, ?, ?, ?) ON CONFLICT(name) DO NOTHING`, item.ID, name, string(encoded), s.now().Unix())
	if err != nil {
		return GroupSearchPreset{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return GroupSearchPreset{}, err
	}
	if count == 0 {
		return GroupSearchPreset{}, ErrPresetNameConflict
	}
	return item, nil
}

func (s *Store) DeleteGroupSearchPreset(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM group_search_presets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
