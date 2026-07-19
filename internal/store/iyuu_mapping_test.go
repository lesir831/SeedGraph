package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

func TestIYUUTrackerMatchingUsesThreeUniqueLevels(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.ApplyIYUUCatalog(ctx, []IYUUSiteInput{
		{RemoteID: 1, Slug: "soulvoice", Nickname: "Soul Voice", BaseURL: "pt.soulvoice.club"},
		{RemoteID: 2, Slug: "hhanclub", Nickname: "HhanClub", BaseURL: "hhanclub.net"},
		{RemoteID: 3, Slug: "cspt", Nickname: "CSPT", BaseURL: "cspt.top"},
	}, time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	downloader := seedDownloader(t, database)
	tests := []struct {
		hash, trackerHost, wantSite, wantType string
	}{
		{hash: "iyuu-exact", trackerHost: "pt.soulvoice.club", wantSite: "soulvoice", wantType: "exact"},
		{hash: "iyuu-domain", trackerHost: "tracker.hhanclub.net", wantSite: "hhanclub", wantType: "registrable_domain"},
		{hash: "iyuu-keyword", trackerHost: "tracker.cspt.cc", wantSite: "cspt", wantType: "keyword"},
	}
	records := make([]TorrentRecord, 0, len(tests))
	for _, test := range tests {
		record := torrentRecord(downloader, test.hash)
		record.Trackers = []TrackerRecord{{HostIdentity: test.trackerHost, PathHint: "/announce"}}
		records = append(records, record)
	}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: records,
	}); err != nil {
		t.Fatal(err)
	}

	for index, test := range tests {
		var siteName, matchType string
		if err := database.db.QueryRow(`
			SELECT s.name, tt.match_type
			FROM torrent_trackers tt
			JOIN sites s ON s.id = tt.site_id
			WHERE tt.instance_id = ?`, records[index].ID,
		).Scan(&siteName, &matchType); err != nil {
			t.Fatalf("read %s mapping: %v", test.hash, err)
		}
		if siteName != test.wantSite || matchType != test.wantType {
			t.Errorf("%s mapping = site %q type %q, want %q/%q", test.hash, siteName, matchType, test.wantSite, test.wantType)
		}
	}
}

func TestIYUUTrackerMatchingLeavesAmbiguousKeywordUnmapped(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.ApplyIYUUCatalog(ctx, []IYUUSiteInput{
		{RemoteID: 1, Slug: "cspt-top", Nickname: "CSPT Top", BaseURL: "cspt.top"},
		{RemoteID: 2, Slug: "cspt-xyz", Nickname: "CSPT XYZ", BaseURL: "cspt.xyz"},
	}, time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	downloader := seedDownloader(t, database)
	record := torrentRecord(downloader, "iyuu-ambiguous")
	record.Trackers = []TrackerRecord{{HostIdentity: "tracker.cspt.cc", PathHint: "/announce"}}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}

	var siteID sql.NullString
	var matchType string
	if err := database.db.QueryRow(`
		SELECT site_id, match_type FROM torrent_trackers WHERE instance_id = ?`, record.ID,
	).Scan(&siteID, &matchType); err != nil {
		t.Fatal(err)
	}
	if siteID.Valid || matchType != "" {
		t.Fatalf("ambiguous keyword was mapped: site=%+v type=%q", siteID, matchType)
	}
}

func TestIYUUTrackerMatchingStopsAtAmbiguousStrongerTier(t *testing.T) {
	tests := []struct {
		name  string
		host  string
		sites []iyuuTrackerMatchSite
	}{
		{
			name: "ambiguous exact does not fall through to unique registrable domain",
			host: "tracker.example.com",
			sites: []iyuuTrackerMatchSite{
				{siteID: "one", host: "tracker.example.com", registrableDomain: "example.com", keyword: "example"},
				{siteID: "two", host: "tracker.example.com", registrableDomain: "other.net", keyword: "other"},
			},
		},
		{
			name: "ambiguous registrable domain does not fall through to unique keyword",
			host: "tracker.example.com",
			sites: []iyuuTrackerMatchSite{
				{siteID: "one", host: "one.example.com", registrableDomain: "example.com", keyword: "example"},
				{siteID: "two", host: "two.example.com", registrableDomain: "example.com", keyword: "other"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			siteID, matchType := classifyPersistedTracker(test.host, "/announce", nil, test.sites)
			if siteID != "" || matchType != "" {
				t.Fatalf("ambiguous stronger tier fell through: site=%q type=%q", siteID, matchType)
			}
		})
	}
}

func TestIYUUCatalogReclassifiesExistingTrackerAndMaintainsSiteIdentity(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	downloader := seedDownloader(t, database)
	record := torrentRecord(downloader, "catalog-reclassification")
	record.Trackers = []TrackerRecord{{HostIdentity: "tracker.hhanclub.net", PathHint: "/announce"}}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	if got := trackerSiteID(t, database, record.ID); got.Valid {
		t.Fatalf("tracker unexpectedly mapped before catalog sync: %+v", got)
	}

	if err := database.ApplyIYUUCatalog(ctx, []IYUUSiteInput{
		{RemoteID: 42, Slug: "hhanclub", Nickname: "Hhan Club", BaseURL: "hhanclub.net"},
	}, time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	var remoteID int64
	var source, matchType string
	if err := database.db.QueryRow(`
		SELECT s.iyuu_remote_id, s.source, tt.match_type
		FROM torrent_trackers tt
		JOIN sites s ON s.id = tt.site_id
		WHERE tt.instance_id = ?`, record.ID,
	).Scan(&remoteID, &source, &matchType); err != nil {
		t.Fatal(err)
	}
	if remoteID != 42 || source != "iyuu" || matchType != "registrable_domain" {
		t.Fatalf("catalog mapping = remote %d source %q type %q", remoteID, source, matchType)
	}
}

func TestExplicitTrackerRuleWinsAndRecordsItsMatchType(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if err := database.ApplyIYUUCatalog(ctx, []IYUUSiteInput{
		{RemoteID: 1, Slug: "catalog", Nickname: "Catalog", BaseURL: "example.com"},
	}, time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	downloader := seedDownloader(t, database)
	record := torrentRecord(downloader, "explicit-rule-priority")
	record.Trackers = []TrackerRecord{{HostIdentity: "tracker.example.com", PathHint: "/announce"}}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	rule, err := database.CreateCustomTrackerRule(ctx, CreateTrackerRuleParams{
		HostPattern: "*.example.com", SiteName: "explicit", DisplayName: "Explicit",
	})
	if err != nil {
		t.Fatal(err)
	}
	var siteID, matchType string
	if err := database.db.QueryRow(`
		SELECT site_id, match_type FROM torrent_trackers WHERE instance_id = ?`, record.ID,
	).Scan(&siteID, &matchType); err != nil {
		t.Fatal(err)
	}
	if siteID != rule.SiteID || matchType != "custom" {
		t.Fatalf("explicit rule mapping = site %q type %q, want %q/custom", siteID, matchType, rule.SiteID)
	}
}

func TestIYUUCatalogHandlesRemoteIDSwapWithoutLosingSlugIdentity(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	initial := []IYUUSiteInput{
		{RemoteID: 1, Slug: "alpha", Nickname: "Alpha", BaseURL: "alpha.example"},
		{RemoteID: 2, Slug: "beta", Nickname: "Beta", BaseURL: "beta.example"},
	}
	if err := database.ApplyIYUUCatalog(ctx, initial, time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	siteIDs := make(map[string]string)
	rows, err := database.db.Query("SELECT name, id FROM sites WHERE name IN ('alpha', 'beta')")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, id string
		if err := rows.Scan(&name, &id); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		siteIDs[name] = id
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}

	swapped := []IYUUSiteInput{
		{RemoteID: 1, Slug: "beta", Nickname: "Beta", BaseURL: "beta.example"},
		{RemoteID: 2, Slug: "alpha", Nickname: "Alpha", BaseURL: "alpha.example"},
	}
	if err := database.ApplyIYUUCatalog(ctx, swapped, time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	for name, wantRemoteID := range map[string]int64{"alpha": 2, "beta": 1} {
		var id string
		var remoteID int64
		if err := database.db.QueryRow(
			"SELECT id, iyuu_remote_id FROM sites WHERE name = ?", name,
		).Scan(&id, &remoteID); err != nil {
			t.Fatal(err)
		}
		if id != siteIDs[name] || remoteID != wantRemoteID {
			t.Fatalf("site %q after ID swap = id %q remote %d, want id %q remote %d", name, id, remoteID, siteIDs[name], wantRemoteID)
		}
	}
}

func TestIYUUCatalogResolvesSlugsBeforeRemoteIDsRegardlessOfInputOrder(t *testing.T) {
	orders := [][]IYUUSiteInput{
		{
			{RemoteID: 1, Slug: "beta", Nickname: "Beta", BaseURL: "beta.example"},
			{RemoteID: 2, Slug: "alpha", Nickname: "Alpha", BaseURL: "alpha.example"},
		},
		{
			{RemoteID: 2, Slug: "alpha", Nickname: "Alpha", BaseURL: "alpha.example"},
			{RemoteID: 1, Slug: "beta", Nickname: "Beta", BaseURL: "beta.example"},
		},
	}
	for index, snapshot := range orders {
		t.Run(fmt.Sprintf("order-%d", index), func(t *testing.T) {
			database := openTestStore(t)
			ctx := context.Background()
			if err := database.ApplyIYUUCatalog(ctx, []IYUUSiteInput{
				{RemoteID: 1, Slug: "alpha", Nickname: "Alpha", BaseURL: "alpha.example"},
			}, time.Unix(100, 0).UTC()); err != nil {
				t.Fatal(err)
			}
			var originalAlphaID string
			if err := database.db.QueryRow("SELECT id FROM sites WHERE name = 'alpha'").Scan(&originalAlphaID); err != nil {
				t.Fatal(err)
			}
			if err := database.ApplyIYUUCatalog(ctx, snapshot, time.Unix(200, 0).UTC()); err != nil {
				t.Fatal(err)
			}
			var alphaID string
			var alphaRemoteID int64
			if err := database.db.QueryRow(
				"SELECT id, iyuu_remote_id FROM sites WHERE name = 'alpha'",
			).Scan(&alphaID, &alphaRemoteID); err != nil {
				t.Fatal(err)
			}
			if alphaID != originalAlphaID || alphaRemoteID != 2 {
				t.Fatalf("alpha identity = id %q remote %d, want id %q remote 2", alphaID, alphaRemoteID, originalAlphaID)
			}
		})
	}
}
