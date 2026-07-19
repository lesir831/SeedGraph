package store

import (
	"context"
	"database/sql"
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
	if count != 3 {
		t.Fatalf("migration count = %d, want 3", count)
	}
}

func TestAddedAtMigrationBackfillsFirstSeenAt(t *testing.T) {
	path := t.TempDir() + "/seedgraph.db"
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	v1, err := migrationFiles.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(v1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO schema_migrations(version, applied_at) VALUES(1, 100);
		INSERT INTO storages(id, name, created_at, updated_at) VALUES('storage', 'Storage', 100, 100);
		INSERT INTO downloaders(id, name, kind, base_url, storage_id, created_at, updated_at)
		VALUES('downloader', 'Downloader', 'transmission', 'http://example', 'storage', 100, 100);
		INSERT INTO content_groups(id, display_name, created_at, updated_at)
		VALUES('content', 'Content', 100, 100);
		INSERT INTO data_groups(id, auto_key, storage_id, canonical_path, wanted_bytes, created_at, updated_at)
		VALUES('data', 'data-key', 'storage', '/data', 1, 100, 100);
		INSERT INTO torrent_instances(
			id, downloader_id, stable_hash_key, name, source_path, canonical_path,
			storage_id, wanted_bytes, content_group_id, data_group_id, first_seen_at, last_seen_at
		) VALUES('instance', 'downloader', 'hash', 'Torrent', '/data', '/data',
			'storage', 1, 'content', 'data', 1234, 1234);
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var addedAt int64
	if err := store.db.QueryRow("SELECT added_at FROM torrent_instances WHERE id = 'instance'").Scan(&addedAt); err != nil {
		t.Fatal(err)
	}
	if addedAt != 1234 {
		t.Fatalf("added_at = %d, want first_seen_at 1234", addedAt)
	}
}

func TestTrackerMappingMigrationBackfillsCachedIYUUSites(t *testing.T) {
	path := t.TempDir() + "/seedgraph.db"
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"migrations/001_init.sql", "migrations/002_torrent_added_at.sql"} {
		script, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(script)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`
		INSERT INTO schema_migrations(version, applied_at) VALUES(1, 100), (2, 100);
		INSERT INTO iyuu_sites(
			remote_id, slug, nickname, base_url, is_https, cookie_required,
			first_seen_at, last_seen_at
		) VALUES(42, 'soulvoice', 'Soul Voice', 'pt.soulvoice.club', 1, 0, 100, 100);
		INSERT INTO storages(id, name, created_at, updated_at) VALUES('storage', 'Storage', 100, 100);
		INSERT INTO downloaders(id, name, kind, base_url, storage_id, created_at, updated_at)
		VALUES('downloader', 'Downloader', 'transmission', 'http://example', 'storage', 100, 100);
		INSERT INTO content_groups(id, display_name, created_at, updated_at)
		VALUES('content', 'Content', 100, 100);
		INSERT INTO data_groups(id, auto_key, storage_id, canonical_path, wanted_bytes, created_at, updated_at)
		VALUES('data', 'data-key', 'storage', '/data', 1, 100, 100);
		INSERT INTO torrent_instances(
			id, downloader_id, stable_hash_key, name, source_path, canonical_path,
			storage_id, wanted_bytes, content_group_id, data_group_id, first_seen_at, last_seen_at
		) VALUES('instance', 'downloader', 'hash', 'Torrent', '/data', '/data',
			'storage', 1, 'content', 'data', 100, 100);
		INSERT INTO torrent_trackers(instance_id, host_identity, path_hint)
		VALUES('instance', 'pt.soulvoice.club', '/announce');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var remoteID int64
	var siteName, source, matchType string
	if err := database.db.QueryRow(`
		SELECT s.iyuu_remote_id, s.name, s.source, tt.match_type
		FROM torrent_trackers tt JOIN sites s ON s.id = tt.site_id
		WHERE tt.instance_id = 'instance'
	`).Scan(&remoteID, &siteName, &source, &matchType); err != nil {
		t.Fatal(err)
	}
	if remoteID != 42 || siteName != "soulvoice" || source != "iyuu" || matchType != "exact" {
		t.Fatalf("upgraded mapping = remote %d site %q source %q type %q", remoteID, siteName, source, matchType)
	}

	// Simulate a process stopping after the schema/catalog migration committed
	// but before its post-migration classification completed. A later startup
	// must repair the row even though there are no new migrations to apply.
	if _, err := database.db.Exec(`
		UPDATE torrent_trackers SET site_id = NULL, match_type = '' WHERE instance_id = 'instance'
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`
		SELECT s.iyuu_remote_id, s.name, s.source, tt.match_type
		FROM torrent_trackers tt JOIN sites s ON s.id = tt.site_id
		WHERE tt.instance_id = 'instance'
	`).Scan(&remoteID, &siteName, &source, &matchType); err != nil {
		t.Fatal(err)
	}
	if remoteID != 42 || siteName != "soulvoice" || source != "iyuu" || matchType != "exact" {
		t.Fatalf("reopened mapping = remote %d site %q source %q type %q", remoteID, siteName, source, matchType)
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
