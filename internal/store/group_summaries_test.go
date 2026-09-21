package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestGroupSummariesCountEachActiveTaskOnce(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	ctx := context.Background()
	first := operationTestRecord(downloader, "first", "movies")
	first.CanonicalPath = "/volume1/001/media/Movies/The.Whisper.Man.2026"
	first.AddedAt = time.Unix(1000, 0).UTC()
	first.Runtime = RuntimeRecord{Ratio: 2.5, UploadedBytes: 250, DownloadedBytes: 100, UploadSpeed: 10, DownloadSpeed: 20}
	first.Trackers = []TrackerRecord{{HostIdentity: "one.example.org"}, {HostIdentity: "two.example.org"}}
	second := operationTestRecord(downloader, "second", "movies")
	second.CanonicalPath = first.CanonicalPath
	second.Runtime = RuntimeRecord{Ratio: 4, UploadedBytes: 400, DownloadedBytes: 100, UploadSpeed: 30, DownloadSpeed: 40}
	deleted := operationTestRecord(downloader, "deleted", "movies")
	deleted.CanonicalPath = "/archive/Other/removed"
	deleted.Runtime = RuntimeRecord{Ratio: 999, UploadedBytes: 9999}
	unrelated := operationTestRecord(downloader, "other", "other-group")
	unrelated.Runtime = RuntimeRecord{Ratio: 999, UploadedBytes: 9999}
	if _, err := store.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{first, second, deleted, unrelated},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE torrent_instances SET deleted_at = 1 WHERE id = ?`, deleted.ID); err != nil {
		t.Fatal(err)
	}
	groups, total, err := store.ListTorrentGroups(ctx, GroupFilters{ID: "movies", Search: "Whisper"})
	if err != nil || total != 1 || len(groups) != 1 {
		t.Fatalf("groups = %+v, total = %d, err = %v", groups, total, err)
	}
	group := groups[0]
	if !reflect.DeepEqual(group.Categories, []string{"Movies"}) || !reflect.DeepEqual(group.Paths, []string{first.CanonicalPath}) ||
		!reflect.DeepEqual(group.Downloaders, []string{downloader.Name}) {
		t.Fatalf("unexpected summary metadata: %+v", group)
	}
	runtime := group.Runtime
	if runtime == nil || runtime.RatioMin == nil || runtime.RatioMax == nil || *runtime.RatioMin != 2.5 || *runtime.RatioMax != 4 ||
		runtime.UploadedBytes != 650 || runtime.DownloadedBytes != 200 || runtime.UploadSpeed != 40 || runtime.DownloadSpeed != 60 {
		t.Fatalf("unexpected runtime summary: %+v", runtime)
	}
	if !group.OldestAddedAt.Equal(first.AddedAt) {
		t.Fatalf("oldest added = %v", group.OldestAddedAt)
	}
	detail, err := store.GetTorrentGroup(ctx, group.ID, time.Time{})
	if err != nil || len(detail.Instances) != 2 {
		t.Fatalf("detail = %+v, err = %v", detail, err)
	}
	for _, instance := range detail.Instances {
		if instance.Category != "Movies" || instance.DownloadedBytes != 100 || instance.LastSyncAt == nil {
			t.Fatalf("unexpected instance metrics: %+v", instance)
		}
		if instance.ID == first.ID && (instance.UploadedBytes != 250 || instance.UploadSpeed != 10 || instance.DownloadSpeed != 20) {
			t.Fatalf("first instance metrics: %+v", instance)
		}
	}
	// A manually merged group can span categories; expose every distinct one.
	if _, err := store.db.Exec(`UPDATE torrent_instances SET canonical_path = '/media/TV/series' WHERE id = ?`, second.ID); err != nil {
		t.Fatal(err)
	}
	groups, _, err = store.ListTorrentGroups(ctx, GroupFilters{ID: "movies"})
	if err != nil || !reflect.DeepEqual(groups[0].Categories, []string{"Movies", "TV"}) || len(groups[0].Paths) != 2 {
		t.Fatalf("merged categories = %+v, err = %v", groups, err)
	}
}

func TestGroupSummaryDistinguishesMissingRuntimeFromZero(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	ctx := context.Background()
	record := operationTestRecord(downloader, "zero", "group")
	if _, err := store.ApplySync(ctx, ApplySyncParams{DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record}}); err != nil {
		t.Fatal(err)
	}
	groups, _, err := store.ListTorrentGroups(ctx, GroupFilters{ID: "group"})
	if err != nil || groups[0].Runtime == nil || groups[0].Runtime.RatioMin == nil || *groups[0].Runtime.RatioMin != 0 {
		t.Fatalf("zero runtime = %+v, err = %v", groups, err)
	}
	if _, err := store.db.Exec(`DELETE FROM torrent_runtime WHERE instance_id = ?`, record.ID); err != nil {
		t.Fatal(err)
	}
	groups, _, err = store.ListTorrentGroups(ctx, GroupFilters{ID: "group"})
	if err != nil || groups[0].Runtime != nil {
		t.Fatalf("missing runtime = %+v, err = %v", groups, err)
	}
}
