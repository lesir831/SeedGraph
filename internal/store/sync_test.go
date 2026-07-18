package store

import (
	"context"
	"testing"
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
