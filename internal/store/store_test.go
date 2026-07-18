package store

import (
	"context"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOpenAppliesMigrations(t *testing.T) {
	store := openTestStore(t)
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migration count = %d, want 1", count)
	}
}

func TestEnsureAdminDoesNotOverwriteExistingHash(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.EnsureAdmin(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAdmin(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	got, err := store.AdminPasswordHash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != "first" {
		t.Fatalf("AdminPasswordHash() = %q, want first", got)
	}
}
