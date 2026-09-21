package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSearchPresetsPersistAndRejectInvalidConfiguration(t *testing.T) {
	ctx := context.Background()
	file := t.TempDir() + "/presets.db"
	db, err := Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	filter := groupQueryDocument(groupQueryCondition(t, "path", "contains", "/Movies/"))
	created, err := db.CreateGroupSearchPreset(ctx, "  Movies  ", filter)
	if err != nil || created.Name != "Movies" || created.ID == "" {
		t.Fatalf("create: %+v, %v", created, err)
	}
	if _, err := db.CreateGroupSearchPreset(ctx, "movies", filter); !errors.Is(err, ErrPresetNameConflict) {
		t.Fatalf("duplicate error: %v", err)
	}
	for _, name := range []string{" ", strings.Repeat("长", 81)} {
		if _, err := db.CreateGroupSearchPreset(ctx, name, filter); !errors.Is(err, ErrInvalidGroupFilter) {
			t.Fatalf("invalid name: %v", err)
		}
	}
	for _, invalid := range []*TorrentGroupQuery{nil, groupQueryDocument(), groupQueryDocument(groupQueryCondition(t, "raw_sql", "eq", "1=1"))} {
		if _, err := db.CreateGroupSearchPreset(ctx, "invalid", invalid); !errors.Is(err, ErrInvalidGroupFilter) {
			t.Fatalf("invalid filter: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	items, err := db.ListGroupSearchPresets(ctx)
	if err != nil || len(items) != 1 || items[0].ID != created.ID || !reflect.DeepEqual(items[0].Filter, filter) {
		t.Fatalf("persisted presets: %+v, %v", items, err)
	}
	if err := db.DeleteGroupSearchPreset(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteGroupSearchPreset(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing preset: %v", err)
	}
	items, err = db.ListGroupSearchPresets(ctx)
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("empty list: %+v, %v", items, err)
	}
}
