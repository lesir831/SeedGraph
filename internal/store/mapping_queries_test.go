package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestListTrackerMappingsFiltersAggregatesAndPaginates(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.ApplyIYUUCatalog(ctx, []IYUUSiteInput{
		{RemoteID: 1, Slug: "soulvoice", Nickname: "SoulVoice", BaseURL: "pt.soulvoice.club"},
		{RemoteID: 2, Slug: "hhanclub", Nickname: "HhanClub", BaseURL: "hhanclub.net"},
		{RemoteID: 3, Slug: "cspt", Nickname: "财神 PT", BaseURL: "cspt.top"},
		{RemoteID: 4, Slug: "unused", Nickname: "尚未映射", BaseURL: "unused.example"},
	}, time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	downloader := seedDownloader(t, database)
	records := []TorrentRecord{
		torrentRecord(downloader, "exact-one"),
		torrentRecord(downloader, "exact-two"),
		torrentRecord(downloader, "domain"),
		torrentRecord(downloader, "keyword"),
		torrentRecord(downloader, "unknown"),
	}
	records[0].Trackers = []TrackerRecord{{HostIdentity: "pt.soulvoice.club", PathHint: "/announce"}}
	records[1].Trackers = []TrackerRecord{{HostIdentity: "pt.soulvoice.club", PathHint: "/announce"}}
	records[2].Trackers = []TrackerRecord{{HostIdentity: "tracker.hhanclub.net", PathHint: "/announce"}}
	records[3].Trackers = []TrackerRecord{{HostIdentity: "tracker.cspt.cc", PathHint: "/announce"}}
	records[4].Trackers = []TrackerRecord{{HostIdentity: "unknown.invalid", PathHint: "/announce"}}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: records,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`
		UPDATE torrent_instances SET last_seen_at = CASE id
			WHEN ? THEN 110 WHEN ? THEN 120 WHEN ? THEN 130 WHEN ? THEN 140 ELSE 150 END`,
		records[0].ID, records[1].ID, records[2].ID, records[3].ID,
	); err != nil {
		t.Fatal(err)
	}

	all, total, err := database.ListTrackerMappings(ctx, TrackerMappingQuery{
		Status: TrackerMappingStatusAll, MatchType: TrackerMatchTypeAll, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(all) != 2 {
		t.Fatalf("mapping page = %+v total=%d, want 2 of 4", all, total)
	}
	if all[0].HostIdentity != "pt.soulvoice.club" || all[0].InstanceCount != 2 ||
		all[0].GroupCount != 2 || all[0].MatchType != TrackerMatchTypeExact ||
		all[0].DisplayName != "SoulVoice" || !all[0].LastSeenAt.Equal(time.Unix(120, 0).UTC()) {
		t.Fatalf("exact aggregate = %+v", all[0])
	}

	domain, total, err := database.ListTrackerMappings(ctx, TrackerMappingQuery{
		Query: "hhan", Status: TrackerMappingStatusMapped,
		MatchType: TrackerMatchTypeRegistrableDomain, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(domain) != 1 || domain[0].HostIdentity != "tracker.hhanclub.net" ||
		domain[0].SiteName != "hhanclub" {
		t.Fatalf("domain match filter = %+v total=%d", domain, total)
	}

	keyword, total, err := database.ListTrackerMappings(ctx, TrackerMappingQuery{
		Query: "财神", Status: TrackerMappingStatusMapped,
		MatchType: TrackerMatchTypeKeyword, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(keyword) != 1 || keyword[0].HostIdentity != "tracker.cspt.cc" {
		t.Fatalf("keyword site-name filter = %+v total=%d", keyword, total)
	}

	unmapped, total, err := database.ListTrackerMappings(ctx, TrackerMappingQuery{
		Status: TrackerMappingStatusUnmapped, MatchType: TrackerMatchTypeAll, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(unmapped) != 1 || unmapped[0].Mapped ||
		unmapped[0].HostIdentity != "unknown.invalid" || unmapped[0].MatchType != "" {
		t.Fatalf("unmapped filter = %+v total=%d", unmapped, total)
	}

	page, pageTotal, err := database.ListTrackerMappings(ctx, TrackerMappingQuery{
		Status: TrackerMappingStatusAll, MatchType: TrackerMatchTypeAll, Limit: 2, Offset: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pageTotal != 4 || len(page) != 2 {
		t.Fatalf("second page = %+v total=%d", page, pageTotal)
	}
}

func TestListTrackerMappingsRedactsUnexpectedStoredValues(t *testing.T) {
	database := openTestStore(t)
	downloader := seedDownloader(t, database)
	ctx := context.Background()
	record := torrentRecord(downloader, "legacy-mapping")
	record.Trackers = nil
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	shortHostSecret := "abc123def456"
	rawHost := "https://user:password@" + shortHostSecret + ".tracker.example.com:443/announce?passkey=host-secret"
	rawPath := "/announce/path-secret?passkey=query-secret"
	if _, err := database.db.Exec(`
		INSERT INTO torrent_trackers(instance_id, host_identity, path_hint, site_id, match_type)
		VALUES(?, ?, ?, NULL, '')`, record.ID, rawHost, rawPath); err != nil {
		t.Fatal(err)
	}

	items, total, err := database.ListTrackerMappings(ctx, TrackerMappingQuery{
		Status: TrackerMappingStatusAll, MatchType: TrackerMatchTypeAll, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].HostIdentity != "_redacted.tracker.example.com" ||
		items[0].PathHint != "/announce/*" {
		t.Fatalf("legacy mapping identity = %+v total=%d", items, total)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	response := string(encoded)
	for _, secret := range []string{rawHost, rawPath, shortHostSecret, "user", "password", "host-secret", "path-secret", "query-secret", "passkey"} {
		if strings.Contains(response, secret) {
			t.Fatalf("Tracker mapping response leaked %q: %s", secret, response)
		}
	}
}

func TestListTrackerMappingsLeavesConflictingStoredCandidatesUnmapped(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	downloader := seedDownloader(t, database)
	records := []TorrentRecord{
		torrentRecord(downloader, "conflict-one"),
		torrentRecord(downloader, "conflict-two"),
	}
	for index := range records {
		records[index].Trackers = []TrackerRecord{{HostIdentity: "tracker.conflict.example", PathHint: "/announce"}}
	}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: records,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`
		INSERT INTO sites(id, name, display_name, source, created_at, updated_at) VALUES
			('site-one', 'one', 'One', 'custom', 100, 100),
			('site-two', 'two', 'Two', 'custom', 100, 100);
		UPDATE torrent_trackers SET site_id = 'site-one', match_type = 'exact' WHERE instance_id = ?;
		UPDATE torrent_trackers SET site_id = 'site-two', match_type = 'exact' WHERE instance_id = ?;
	`, records[0].ID, records[1].ID); err != nil {
		t.Fatal(err)
	}

	items, total, err := database.ListTrackerMappings(ctx, TrackerMappingQuery{
		Status: TrackerMappingStatusUnmapped, MatchType: TrackerMatchTypeAll, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 || items[0].Mapped || items[0].SiteID != "" || items[0].MatchType != "" {
		t.Fatalf("conflicting mapping candidates were not left unmapped: %+v total=%d", items, total)
	}
}

func TestListIYUUSitesPageFiltersMappingStateAndSearch(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.ApplyIYUUCatalog(ctx, []IYUUSiteInput{
		{RemoteID: 1, Slug: "alpha", Nickname: "Alpha PT", BaseURL: "alpha.example"},
		{RemoteID: 2, Slug: "beta", Nickname: "Beta PT", BaseURL: "beta.example"},
		{RemoteID: 3, Slug: "gamma", Nickname: "Gamma PT", BaseURL: "gamma.example"},
	}, time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	downloader := seedDownloader(t, database)
	record := torrentRecord(downloader, "alpha-mapped")
	record.Trackers = []TrackerRecord{{HostIdentity: "alpha.example", PathHint: "/announce"}}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}

	mapped, state, total, err := database.ListIYUUSitesPage(ctx, IYUUSiteQuery{
		Status: TrackerMappingStatusMapped, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.SiteCount != 3 || total != 1 || len(mapped) != 1 || mapped[0].Slug != "alpha" ||
		!mapped[0].Mapped || mapped[0].MappingCount != 1 {
		t.Fatalf("mapped IYUU page = %+v state=%+v total=%d", mapped, state, total)
	}

	unmapped, _, total, err := database.ListIYUUSitesPage(ctx, IYUUSiteQuery{
		Query: "example", Status: TrackerMappingStatusUnmapped, Limit: 1, Offset: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(unmapped) != 1 || unmapped[0].Slug != "gamma" || unmapped[0].Mapped {
		t.Fatalf("unmapped IYUU page = %+v total=%d", unmapped, total)
	}

	searched, _, total, err := database.ListIYUUSitesPage(ctx, IYUUSiteQuery{
		Query: "beta pt", Status: TrackerMappingStatusAll, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(searched) != 1 || searched[0].Slug != "beta" {
		t.Fatalf("IYUU name search = %+v total=%d", searched, total)
	}
}

func TestMappingListQueriesRejectInvalidFilters(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if _, _, err := database.ListTrackerMappings(ctx, TrackerMappingQuery{
		Status: "yes", MatchType: TrackerMatchTypeAll, Limit: 20,
	}); err == nil {
		t.Fatal("invalid Tracker mapping status unexpectedly accepted")
	}
	if _, _, err := database.ListTrackerMappings(ctx, TrackerMappingQuery{
		Status: TrackerMappingStatusAll, MatchType: "fuzzy", Limit: 20,
	}); err == nil {
		t.Fatal("invalid Tracker match type unexpectedly accepted")
	}
	if _, _, _, err := database.ListIYUUSitesPage(ctx, IYUUSiteQuery{
		Status: TrackerMappingStatusAll, Limit: 201,
	}); err == nil {
		t.Fatal("oversized IYUU page unexpectedly accepted")
	}
}
