package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func seedDownloader(t *testing.T, store *Store) Downloader {
	t.Helper()
	downloader, err := store.CreateDownloader(context.Background(), CreateDownloaderParams{
		Name: "Transmission", Kind: "transmission", BaseURL: "http://tr:9091",
		StorageName: "media", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return downloader
}

func torrentRecord(downloader Downloader, hash string) TorrentRecord {
	return TorrentRecord{
		ID: hash + "-instance", DownloaderID: downloader.ID, StableHashKey: hash,
		RemoteID: "1", Name: "Example", SourcePath: "/downloads/Example",
		CanonicalPath: "/media/Example", StorageID: downloader.StorageID, WantedBytes: 42,
		MetadataFingerprint: "metadata-" + hash, ManifestFingerprint: "manifest",
		ContentGroupID: "content-" + hash, ContentGroupAutoKey: "content-key-" + hash,
		DataGroupID: "data-" + hash, DataGroupAutoKey: "data-key-" + hash,
		Confidence: "verified",
		Runtime:    RuntimeRecord{Status: "seeding", Progress: 1, RuntimeFingerprint: "runtime"},
		Trackers:   []TrackerRecord{{HostIdentity: "tracker.example.com"}},
	}
}

func TestIncompleteSnapshotNeverTombstonesMissingTorrent(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	ctx := context.Background()
	first := torrentRecord(downloader, "hash-one")
	if _, err := store.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, CursorAfter: "one", Torrents: []TorrentRecord{first},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: false, CursorBefore: "one", CursorAfter: "two",
	}); err != nil {
		t.Fatal(err)
	}
	var deleted any
	if err := store.db.QueryRow("SELECT deleted_at FROM torrent_instances WHERE id = ?", first.ID).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Fatalf("incomplete snapshot tombstoned torrent: %v", deleted)
	}
}

func TestCompleteSnapshotTombstonesAndRevivalPreservesManualMembership(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	ctx := context.Background()
	record := torrentRecord(downloader, "hash-one")
	if _, err := store.ApplySync(ctx, ApplySyncParams{DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE torrent_instances SET content_group_id = 'manual-group', assignment_source = 'manual' WHERE id = ?`, record.ID); err == nil {
		// The referenced group must exist, so create it before applying the actual update.
		t.Fatal("manual membership update unexpectedly succeeded without a group")
	}
	now := store.now().Unix()
	if _, err := store.db.Exec(`INSERT INTO content_groups(id, display_name, mode, confidence, created_at, updated_at) VALUES('manual-group','Manual','manual','manual',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE torrent_instances SET content_group_id = 'manual-group', assignment_source = 'manual' WHERE id = ?`, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySync(ctx, ApplySyncParams{DownloaderID: downloader.ID, Mode: "full", Complete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySync(ctx, ApplySyncParams{DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record}}); err != nil {
		t.Fatal(err)
	}
	var groupID, source string
	var deleted any
	if err := store.db.QueryRow(`SELECT content_group_id, assignment_source, deleted_at FROM torrent_instances WHERE id = ?`, record.ID).Scan(&groupID, &source, &deleted); err != nil {
		t.Fatal(err)
	}
	if groupID != "manual-group" || source != "manual" || deleted != nil {
		t.Fatalf("revival lost manual membership: group=%q source=%q deleted=%v", groupID, source, deleted)
	}
}

func TestCursorAdvancesWithSuccessfulTransaction(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "delta", CursorBefore: "1", CursorAfter: "2",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDownloader(context.Background(), downloader.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncCursor != "2" {
		t.Fatalf("cursor = %q, want 2", got.SyncCursor)
	}
}

func TestSyncPersistsAddedAtAndUnknownDoesNotOverwrite(t *testing.T) {
	store := openTestStore(t)
	store.now = func() time.Time { return time.Unix(2000, 0).UTC() }
	downloader := seedDownloader(t, store)
	record := torrentRecord(downloader, "hash-added-at")
	record.AddedAt = time.Unix(1000, 0).UTC()
	ctx := context.Background()

	if _, err := store.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	record.AddedAt = time.Time{}
	if _, err := store.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "delta", Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	var addedAt int64
	if err := store.db.QueryRow("SELECT added_at FROM torrent_instances WHERE id = ?", record.ID).Scan(&addedAt); err != nil {
		t.Fatal(err)
	}
	if addedAt != 1000 {
		t.Fatalf("added_at = %d after unknown update, want 1000", addedAt)
	}

	record.AddedAt = time.Unix(900, 0).UTC()
	if _, err := store.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "delta", Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT added_at FROM torrent_instances WHERE id = ?", record.ID).Scan(&addedAt); err != nil {
		t.Fatal(err)
	}
	if addedAt != 900 {
		t.Fatalf("added_at = %d after valid update, want 900", addedAt)
	}
}

func TestSyncRejectsNegativeAddedAt(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	record := torrentRecord(downloader, "hash-negative-added-at")
	record.AddedAt = time.Unix(-1, 0).UTC()
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err == nil {
		t.Fatal("ApplySync() accepted a negative added time")
	}
}

func TestSyncNeverPersistsTrackerSecretsFromHostOrPath(t *testing.T) {
	database := openTestStore(t)
	downloader := seedDownloader(t, database)
	secret := "a91f3c7e5b2d8046a91f3c7e5b2d8046"
	record := torrentRecord(downloader, "host-passkey")
	// Exercise the store boundary directly: callers must not be able to bypass
	// TrackerIdentity and persist remote tracker material verbatim.
	record.Trackers = []TrackerRecord{{
		HostIdentity: secret + ".tracker.example.com",
		PathHint:     "/announce/" + secret + "?passkey=" + secret,
	}}
	if _, err := database.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}

	var host, path string
	if err := database.db.QueryRow(`
		SELECT host_identity, path_hint FROM torrent_trackers WHERE instance_id = ?`, record.ID).
		Scan(&host, &path); err != nil {
		t.Fatal(err)
	}
	if host != "_redacted.tracker.example.com" || path != "/announce/*" ||
		strings.Contains(host+path, secret) || strings.Contains(host+path, "passkey") {
		t.Fatalf("unsafe persisted tracker identity: host=%q path=%q", host, path)
	}
}

func TestSyncFallsBackForUnencodableAddedAt(t *testing.T) {
	store := openTestStore(t)
	store.now = func() time.Time { return time.Unix(2000, 0).UTC() }
	downloader := seedDownloader(t, store)
	record := torrentRecord(downloader, "hash-unencodable-added-at")
	record.AddedAt = time.Unix(253402300800, 0).UTC()

	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	var addedAt int64
	if err := store.db.QueryRow("SELECT added_at FROM torrent_instances WHERE id = ?", record.ID).Scan(&addedAt); err != nil {
		t.Fatal(err)
	}
	if addedAt != 2000 {
		t.Fatalf("added_at = %d, want first-seen fallback 2000", addedAt)
	}
}
