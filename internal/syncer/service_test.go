package syncer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lesir831/SeedGraph/internal/cryptox"
	"github.com/lesir831/SeedGraph/internal/downloader"
	"github.com/lesir831/SeedGraph/internal/store"
)

type fakeClient struct {
	full          downloader.Snapshot
	delta         downloader.Snapshot
	manifests     map[string][]downloader.TorrentFile
	manifestCalls int
	started       chan struct{}
	release       chan struct{}
}

func (f *fakeClient) Version(context.Context) (string, error) { return "test-1.0", nil }
func (f *fakeClient) FullSnapshot(ctx context.Context) (downloader.Snapshot, error) {
	if err := f.wait(ctx); err != nil {
		return downloader.Snapshot{}, err
	}
	return f.full, nil
}
func (f *fakeClient) Delta(ctx context.Context, _ string) (downloader.Snapshot, error) {
	if err := f.wait(ctx); err != nil {
		return downloader.Snapshot{}, err
	}
	return f.delta, nil
}
func (f *fakeClient) Delete(context.Context, downloader.TorrentRef, bool) error { return nil }
func (f *fakeClient) FileManifest(_ context.Context, torrent downloader.Torrent) ([]downloader.TorrentFile, error) {
	f.manifestCalls++
	return append([]downloader.TorrentFile(nil), f.manifests[torrent.StableHash]...), nil
}

func (f *fakeClient) wait(ctx context.Context) error {
	if f.started != nil {
		close(f.started)
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func testService(t *testing.T, snapshot downloader.Snapshot) (*Service, *store.Store, store.Downloader) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	cipher, _ := cryptox.New([]byte(strings.Repeat("s", 32)))
	username, _ := cipher.Encrypt([]byte("user"))
	password, _ := cipher.Encrypt([]byte("password"))
	item, err := database.CreateDownloader(ctx, store.CreateDownloaderParams{
		Name: "qB", Kind: "qbittorrent", BaseURL: "http://qb:8080",
		UsernameCiphertext: username, PasswordCiphertext: password,
		StorageName: "media", Enabled: true,
		PathMappings: []store.PathMapping{{SourcePrefix: "/downloads", TargetPrefix: "/media"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := New(database, cipher, nil, time.Minute, time.Hour)
	service.SetClientFactory(func(config downloader.Config) (downloader.Client, error) {
		if config.Username != "user" || config.Password != "password" {
			t.Fatalf("credentials were not decrypted for client factory: %+v", config)
		}
		return &fakeClient{full: snapshot, delta: snapshot}, nil
	})
	return service, database, item
}

func TestFullSyncAggregatesVerifiedCrossSeedsAndRedactsTrackers(t *testing.T) {
	files := []int64{10, 20, 30}
	manifest := []downloader.TorrentFile{
		{Path: "/downloads/Example/a.mkv", Size: 10, Selected: true},
		{Path: "/downloads/Example/b.mkv", Size: 20, Selected: true},
		{Path: "/downloads/Example/c.mkv", Size: 30, Selected: true},
	}
	snapshot := downloader.Snapshot{Full: true, Torrents: []downloader.Torrent{
		{
			StableHash: "hash-one", Name: "Example A", ContentPath: "/downloads/Example",
			WantedBytes: 60, SelectedFilesKnown: true, SelectedFileCount: 3,
			SelectedFileSizes: files, State: "seeding", Progress: 1, AddedAt: time.Unix(1000, 0).UTC(),
			FileManifestKnown: true, Files: manifest,
			TrackerURLs: []string{"https://tracker.example.com/announce/abcdefghijklmnopqrstuvwx?passkey=secret"},
		},
		{
			StableHash: "hash-two", Name: "Example B", ContentPath: "/downloads/Example",
			WantedBytes: 60, SelectedFilesKnown: true, SelectedFileCount: 3,
			SelectedFileSizes: files, State: "seeding", Progress: 1, AddedAt: time.Unix(2000, 0).UTC(),
			FileManifestKnown: true, Files: manifest,
			TrackerURLs: []string{"https://tracker.other.test/announce?token=also-secret"},
		},
	}}
	service, database, item := testService(t, snapshot)
	result, err := service.SyncNow(context.Background(), item.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.SeenCount != 2 {
		t.Fatalf("seen = %d, want 2", result.SeenCount)
	}
	groups, total, err := database.ListTorrentGroups(context.Background(), store.GroupFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(groups) != 1 || groups[0].TaskCount != 2 || groups[0].Confidence != "verified" {
		t.Fatalf("unexpected groups: total=%d groups=%+v", total, groups)
	}
	if !groups[0].OldestAddedAt.Equal(time.Unix(1000, 0).UTC()) {
		t.Fatalf("oldest_added_at = %s", groups[0].OldestAddedAt)
	}
	var combined string
	rows, err := database.DB().Query("SELECT host_identity || path_hint FROM torrent_trackers")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		combined += value
	}
	_ = rows.Close()
	if strings.Contains(combined, "secret") || strings.Contains(combined, "abcdefghijklmnopqrstuvwx") {
		t.Fatalf("tracker persistence leaked a secret: %q", combined)
	}
}

func TestClassifyTrackersMatchesCanonicalSensitiveWildcardIdentity(t *testing.T) {
	secret := "qrstuvwxyzabcdef"
	items := classifyTrackers(
		[]string{"https://node." + secret + ".intentional.example.com/announce"},
		[]store.TrackerRule{{
			HostPattern: "_redacted.intentional.example.com",
			PathPrefix:  "/announce",
			SiteID:      "sensitive-site",
		}},
	)
	if len(items) != 1 || items[0].HostIdentity != "_redacted.intentional.example.com" ||
		items[0].SiteID != "sensitive-site" || strings.Contains(items[0].HostIdentity, secret) {
		t.Fatalf("sensitive wildcard identity was not classified safely: %+v", items)
	}
}

func TestTestConnectionPersistsOnlineVersion(t *testing.T) {
	service, database, item := testService(t, downloader.Snapshot{Full: true})
	version, err := service.TestConnection(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := database.GetDownloader(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if version != "test-1.0" || !updated.Online || updated.Version != version {
		t.Fatalf("unexpected connection state: %+v", updated)
	}
}

func TestQBittorrentSingletonFetchesManifestForSafeDeletionEvidence(t *testing.T) {
	snapshot := downloader.Snapshot{Full: true, Torrents: []downloader.Torrent{{
		StableHash: "hash-single", Name: "Single", ContentPath: "/downloads/Single",
		WantedBytes: 60, State: "uploading", Progress: 1,
	}}}
	service, database, item := testService(t, downloader.Snapshot{})
	client := &fakeClient{
		full: snapshot, delta: snapshot,
		manifests: map[string][]downloader.TorrentFile{"hash-single": {
			{Path: "/downloads/Single/episode-01.mkv", Size: 10, Selected: true},
			{Path: "/downloads/Single/episode-02.mkv", Size: 20, Selected: true},
			{Path: "/downloads/Single/sample.mkv", Size: 30, Selected: true},
		}},
	}
	service.SetClientFactory(func(downloader.Config) (downloader.Client, error) { return client, nil })

	if _, err := service.SyncNow(context.Background(), item.ID, true); err != nil {
		t.Fatal(err)
	}
	if client.manifestCalls != 1 {
		t.Fatalf("manifest calls = %d, want 1", client.manifestCalls)
	}
	groups, total, err := database.ListTorrentGroups(context.Background(), store.GroupFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(groups) != 1 || groups[0].Confidence != "verified" {
		t.Fatalf("singleton did not become verified: total=%d groups=%+v", total, groups)
	}
	rows, err := database.DB().Query(`SELECT source_path, canonical_path FROM torrent_files ORDER BY canonical_path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var paths [][2]string
	for rows.Next() {
		var sourcePath, canonicalPath string
		if err := rows.Scan(&sourcePath, &canonicalPath); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, [2]string{sourcePath, canonicalPath})
	}
	if len(paths) != 3 || paths[0][0] != "/downloads/Single/episode-01.mkv" || paths[0][1] != "/media/Single/episode-01.mkv" {
		t.Fatalf("file manifest was not persisted with mapped paths: %#v", paths)
	}
}

func TestWaitJoinsTriggeredSync(t *testing.T) {
	service, _, item := testService(t, downloader.Snapshot{Full: true})
	client := &fakeClient{
		full:    downloader.Snapshot{Full: true},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	service.SetClientFactory(func(downloader.Config) (downloader.Client, error) { return client, nil })
	if err := service.Trigger(item.ID, true); err != nil {
		t.Fatal(err)
	}
	<-client.started
	waited := make(chan struct{})
	go func() {
		service.Wait()
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("Wait returned while the triggered sync was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(client.release)
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after the triggered sync completed")
	}
}
