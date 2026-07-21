package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func groupQueryCondition(t *testing.T, field, operator string, value any) TorrentGroupQueryNode {
	t.Helper()
	var raw json.RawMessage
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		raw = encoded
	}
	return TorrentGroupQueryNode{Type: "condition", Field: field, Operator: operator, Value: raw}
}

func groupQueryDocument(children ...TorrentGroupQueryNode) *TorrentGroupQuery {
	return &TorrentGroupQuery{
		Version: 1,
		Root:    &TorrentGroupQueryNode{Type: "group", Combinator: "and", Children: children},
	}
}

func operationTestRecord(downloader Downloader, hash, groupID string) TorrentRecord {
	record := torrentRecord(downloader, hash)
	record.Name = groupID + "-" + hash
	record.ContentGroupID = groupID
	record.ContentGroupAutoKey = "key-" + groupID
	return record
}

func groupOperationState(t *testing.T, store *Store, groupID string) (version int, deleted bool) {
	t.Helper()
	var deletedAt sql.NullInt64
	if err := store.db.QueryRow(`SELECT version, deleted_at FROM content_groups WHERE id = ?`, groupID).
		Scan(&version, &deletedAt); err != nil {
		t.Fatal(err)
	}
	return version, deletedAt.Valid
}

func instanceMembership(t *testing.T, store *Store, instanceID string) (groupID, source string) {
	t.Helper()
	if err := store.db.QueryRow(`
		SELECT content_group_id, assignment_source FROM torrent_instances WHERE id = ?`, instanceID).
		Scan(&groupID, &source); err != nil {
		t.Fatal(err)
	}
	return groupID, source
}

func TestMoveInstanceAndUndoRestoresExactMembership(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	source := operationTestRecord(downloader, "source-one", "source-group")
	target := operationTestRecord(downloader, "target-one", "target-group")
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{source, target},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE torrent_instances SET assignment_source = 'manual' WHERE id = ?`, source.ID); err != nil {
		t.Fatal(err)
	}

	detail, err := store.MoveInstance(context.Background(), MoveInstanceParams{
		InstanceID: source.ID, SourceGroupID: source.ContentGroupID, TargetGroupID: target.ContentGroupID,
		ExpectedSourceVersion: 1, ExpectedTargetVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.OperationID == "" || detail.ID != target.ContentGroupID || detail.Version != 2 || detail.TaskCount != 2 {
		t.Fatalf("unexpected move result: %+v", detail)
	}
	if groupID, assignment := instanceMembership(t, store, source.ID); groupID != target.ContentGroupID || assignment != "manual" {
		t.Fatalf("moved membership = (%q, %q)", groupID, assignment)
	}

	undo, err := store.UndoGroupOperation(context.Background(), detail.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if undo.OperationType != "move" || len(undo.RetiredGroupIDs) != 0 ||
		undo.Versions[source.ContentGroupID] != 3 || undo.Versions[target.ContentGroupID] != 3 {
		t.Fatalf("unexpected undo result: %+v", undo)
	}
	if groupID, assignment := instanceMembership(t, store, source.ID); groupID != source.ContentGroupID || assignment != "manual" {
		t.Fatalf("restored membership = (%q, %q), want exact original manual membership", groupID, assignment)
	}
	if _, err := store.UndoGroupOperation(context.Background(), detail.OperationID); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("second undo error = %v, want version conflict", err)
	}
}

func TestListTorrentGroupsSortsByWhitelistedFields(t *testing.T) {
	store := openTestStore(t)
	store.now = func() time.Time { return time.Unix(4000, 0).UTC() }
	downloader := seedDownloader(t, store)

	alphaOne := operationTestRecord(downloader, "alpha-one", "group-alpha")
	alphaOne.Name = "Alpha"
	alphaOne.WantedBytes = 50
	alphaOne.AddedAt = time.Unix(100, 0).UTC()
	alphaTwo := operationTestRecord(downloader, "alpha-two", "group-alpha")
	alphaTwo.Name = "Alpha"
	alphaTwo.WantedBytes = 60
	alphaTwo.AddedAt = time.Unix(200, 0).UTC()
	beta := operationTestRecord(downloader, "beta", "group-beta")
	beta.Name = "Beta"
	beta.WantedBytes = 100
	beta.AddedAt = time.Unix(300, 0).UTC()
	gamma := operationTestRecord(downloader, "gamma", "group-gamma")
	gamma.Name = "Gamma"
	gamma.WantedBytes = 200
	gamma.AddedAt = time.Unix(200, 0).UTC()

	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true,
		Torrents: []TorrentRecord{alphaOne, alphaTwo, beta, gamma},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		sortBy    string
		sortOrder string
		want      []string
	}{
		{name: "default remains updated descending with stable ID fallback", want: []string{"group-alpha", "group-beta", "group-gamma"}},
		{name: "oldest ascending", sortBy: "oldest_added_at", sortOrder: "asc", want: []string{"group-alpha", "group-gamma", "group-beta"}},
		{name: "oldest descending", sortBy: "oldest_added_at", sortOrder: "desc", want: []string{"group-beta", "group-gamma", "group-alpha"}},
		{name: "instance count ascending", sortBy: "instance_count", sortOrder: "asc", want: []string{"group-beta", "group-gamma", "group-alpha"}},
		{name: "instance count descending", sortBy: "instance_count", sortOrder: "desc", want: []string{"group-alpha", "group-beta", "group-gamma"}},
		{name: "size ascending", sortBy: "size", sortOrder: "asc", want: []string{"group-alpha", "group-beta", "group-gamma"}},
		{name: "size descending", sortBy: "size", sortOrder: "desc", want: []string{"group-gamma", "group-beta", "group-alpha"}},
		{name: "name ascending", sortBy: "name", sortOrder: "asc", want: []string{"group-alpha", "group-beta", "group-gamma"}},
		{name: "name descending", sortBy: "name", sortOrder: "desc", want: []string{"group-gamma", "group-beta", "group-alpha"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			groups, total, err := store.ListTorrentGroups(context.Background(), GroupFilters{
				SortBy: test.sortBy, SortOrder: test.sortOrder, Limit: 20,
			})
			if err != nil {
				t.Fatal(err)
			}
			if total != 3 {
				t.Fatalf("total = %d, want 3", total)
			}
			got := make([]string, 0, len(groups))
			for _, group := range groups {
				got = append(got, group.ID)
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("group IDs = %v, want %v", got, test.want)
			}
		})
	}

	detail, err := store.GetTorrentGroup(context.Background(), "group-alpha", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !detail.OldestAddedAt.Equal(time.Unix(100, 0).UTC()) {
		t.Fatalf("oldest_added_at = %s", detail.OldestAddedAt)
	}
	gotAddedAt := make([]int64, 0, len(detail.Instances))
	for _, instance := range detail.Instances {
		gotAddedAt = append(gotAddedAt, instance.AddedAt.Unix())
	}
	slices.Sort(gotAddedAt)
	if !slices.Equal(gotAddedAt, []int64{100, 200}) {
		t.Fatalf("instance added_at values = %v", gotAddedAt)
	}

	delta := operationTestRecord(downloader, "delta", "group-delta")
	delta.Name = "Delta"
	delta.WantedBytes = 75
	delta.AddedAt = time.Unix(300, 0).UTC()
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "delta", Torrents: []TorrentRecord{delta},
	}); err != nil {
		t.Fatal(err)
	}
	multiSorted, _, err := store.ListTorrentGroups(context.Background(), GroupFilters{
		Sorts: []GroupSort{
			{Field: "instance_count", Order: "desc"},
			{Field: "oldest_added_at", Order: "desc"},
			{Field: "size", Order: "asc"},
		},
		Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	multiIDs := make([]string, 0, len(multiSorted))
	for _, group := range multiSorted {
		multiIDs = append(multiIDs, group.ID)
	}
	if want := []string{"group-alpha", "group-delta", "group-beta", "group-gamma"}; !slices.Equal(multiIDs, want) {
		t.Fatalf("multi-sort group IDs = %v, want %v", multiIDs, want)
	}
}

func TestListTorrentGroupsRejectsInvalidSort(t *testing.T) {
	store := openTestStore(t)
	for _, filters := range []GroupFilters{
		{SortBy: "updated_at", SortOrder: "desc"},
		{SortBy: "name", SortOrder: "sideways"},
		{SortOrder: "asc"},
		{Sorts: []GroupSort{{Field: "name", Order: "asc"}, {Field: "name", Order: "desc"}}},
		{Sorts: []GroupSort{{Field: "name", Order: "asc"}}, SortBy: "size"},
		{Sorts: []GroupSort{
			{Field: "name", Order: "asc"},
			{Field: "size", Order: "asc"},
			{Field: "oldest_added_at", Order: "asc"},
			{Field: "instance_count", Order: "asc"},
			{Field: "extra", Order: "asc"},
		}},
	} {
		if _, _, err := store.ListTorrentGroups(context.Background(), filters); !errors.Is(err, ErrInvalidGroupSort) {
			t.Fatalf("ListTorrentGroups(%+v) error = %v, want ErrInvalidGroupSort", filters, err)
		}
	}
}

func TestListTorrentGroupsAdvancedFiltersAndSiteSummaries(t *testing.T) {
	store := openTestStore(t)
	store.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	downloader := seedDownloader(t, store)

	record := func(hash, groupID, name string, size, addedAt int64, trackers ...string) TorrentRecord {
		item := operationTestRecord(downloader, hash, groupID)
		item.Name = name
		item.WantedBytes = size
		item.AddedAt = time.Unix(addedAt, 0).UTC()
		item.Trackers = make([]TrackerRecord, 0, len(trackers))
		for _, host := range trackers {
			item.Trackers = append(item.Trackers, TrackerRecord{HostIdentity: host})
		}
		return item
	}
	records := []TorrentRecord{
		record("target-one", "group-target", "Target Show", 900, 100, "tracker.a.example", "tracker.unknown.example"),
		record("target-two", "group-target", "Target Show", 700, 200, "tracker.b.example"),
		record("only-a", "group-only-a", "Target A Only", 100, 100, "tracker.a.example"),
		record("with-c", "group-with-c", "Target With C", 100, 100, "tracker.a.example", "tracker.b.example", "tracker.c.example"),
		record("large", "group-large", "Target Large", 2000, 100, "tracker.a.example", "tracker.b.example"),
		record("late", "group-late", "Target Late", 100, 200, "tracker.a.example", "tracker.b.example"),
	}
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: records,
	}); err != nil {
		t.Fatal(err)
	}
	now := store.now().Unix()
	if _, err := store.db.Exec(`
		INSERT INTO sites(id, name, display_name, source, created_at, updated_at) VALUES
			('site-a', 'site-a', 'A站', 'custom', ?, ?),
			('site-b', 'site-b', 'B站', 'custom', ?, ?),
			('site-c', 'site-c', 'C站', 'custom', ?, ?);
		UPDATE torrent_trackers SET site_id = CASE host_identity
			WHEN 'tracker.a.example' THEN 'site-a'
			WHEN 'tracker.b.example' THEN 'site-b'
			WHEN 'tracker.c.example' THEN 'site-c'
			ELSE NULL
		END`, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	sizeLT := int64(1000)
	oldestGTE := time.Unix(90, 0).UTC()
	oldestLT := time.Unix(150, 0).UTC()
	groups, total, err := store.ListTorrentGroups(context.Background(), GroupFilters{
		NameContains: "Target", SiteAll: []string{"site:site-a", "site:site-b"}, SiteNone: []string{"site:site-c"},
		SizeLT: &sizeLT, OldestAddedGTE: &oldestGTE, OldestAddedLT: &oldestLT, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(groups) != 1 || groups[0].ID != "group-target" {
		t.Fatalf("advanced groups = %+v, total = %d", groups, total)
	}
	group := groups[0]
	if group.TaskCount != 2 || group.SizeBytes != 900 || group.OldestAddedAt.Unix() != 100 {
		t.Fatalf("target metrics = %+v", group)
	}
	wantSites := []TorrentGroupSite{
		{Key: "site:site-a", Label: "A站", Mapped: true},
		{Key: "site:site-b", Label: "B站", Mapped: true},
		{Key: "tracker:tracker.unknown.example", Label: "Unknown · tracker.unknown.example"},
	}
	if !slices.Equal(group.Sites, wantSites) {
		t.Fatalf("target sites = %v, want %v", group.Sites, wantSites)
	}

	unknown, total, err := store.ListTorrentGroups(context.Background(), GroupFilters{
		SiteAll: []string{"tracker:tracker.unknown.example"}, Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(unknown) != 1 || unknown[0].ID != "group-target" {
		t.Fatalf("unknown-site groups = %+v, total = %d", unknown, total)
	}
	options, err := store.ListTorrentGroupSiteOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantOptions := []TorrentGroupSite{
		{Key: "site:site-a", Label: "A站", Mapped: true},
		{Key: "site:site-b", Label: "B站", Mapped: true},
		{Key: "site:site-c", Label: "C站", Mapped: true},
		{Key: "tracker:tracker.unknown.example", Label: "Unknown · tracker.unknown.example"},
	}
	if !slices.Equal(options, wantOptions) {
		t.Fatalf("site options = %+v, want %+v", options, wantOptions)
	}
}

func TestListTorrentGroupsKeepsCompleteMetricsWhenFilteringInstances(t *testing.T) {
	store := openTestStore(t)
	store.now = func() time.Time { return time.Unix(1000, 0).UTC() }
	transmission := seedDownloader(t, store)
	qbittorrent, err := store.CreateDownloader(context.Background(), CreateDownloaderParams{
		Name: "qBittorrent", Kind: "qbittorrent", BaseURL: "http://qb:8080",
		StorageID: transmission.StorageID, StorageName: "media", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := operationTestRecord(transmission, "first", "shared-group")
	first.Name = "Shared"
	first.WantedBytes = 10
	first.AddedAt = time.Unix(100, 0).UTC()
	first.Runtime.Status = "seeding"
	first.Trackers = []TrackerRecord{{HostIdentity: "tracker.first.example"}}
	second := operationTestRecord(qbittorrent, "second", "shared-group")
	second.Name = "Shared"
	second.WantedBytes = 500
	second.AddedAt = time.Unix(200, 0).UTC()
	second.Runtime.Status = "paused"
	second.Trackers = []TrackerRecord{{HostIdentity: "tracker.second.example"}}
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: transmission.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{first},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: qbittorrent.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{second},
	}); err != nil {
		t.Fatal(err)
	}

	groups, total, err := store.ListTorrentGroups(context.Background(), GroupFilters{
		DownloaderID: transmission.ID, Status: "seeding", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(groups) != 1 {
		t.Fatalf("filtered groups = %+v, total = %d", groups, total)
	}
	group := groups[0]
	if group.TaskCount != 2 || group.DownloaderCount != 2 || group.DataCopyCount != 2 ||
		group.SizeBytes != 500 || group.OldestAddedAt.Unix() != 100 || group.SiteCount != 2 {
		t.Fatalf("filtered group metrics shrank: %+v", group)
	}

	if _, err := store.db.Exec(`UPDATE downloaders SET last_success_at = CASE id WHEN ? THEN 900 ELSE 0 END`, transmission.ID); err != nil {
		t.Fatal(err)
	}
	fresh := false
	freshGroups, freshTotal, err := store.ListTorrentGroups(context.Background(), GroupFilters{Stale: &fresh, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if freshTotal != 0 || len(freshGroups) != 0 {
		t.Fatalf("fresh filter returned group containing a stale downloader: %+v", freshGroups)
	}
	stale := true
	staleGroups, staleTotal, err := store.ListTorrentGroups(context.Background(), GroupFilters{Stale: &stale, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if staleTotal != 1 || len(staleGroups) != 1 || !staleGroups[0].Stale {
		t.Fatalf("stale filter groups = %+v, total = %d", staleGroups, staleTotal)
	}
}

func TestListTorrentGroupsRejectsInvalidAdvancedFilters(t *testing.T) {
	store := openTestStore(t)
	nonPositive := int64(0)
	start := time.Unix(200, 0).UTC()
	end := time.Unix(100, 0).UTC()
	for _, filters := range []GroupFilters{
		{SizeLT: &nonPositive},
		{OldestAddedGTE: &start, OldestAddedLT: &end},
		{SiteAll: []string{"site:a"}, SiteNone: []string{"site:a"}},
		{SiteAll: []string{"site:"}},
		{SiteAll: []string{"tracker:"}},
		{SiteAll: []string{"unknown:value"}},
		{SiteAll: []string{"tracker:TRACKER.EXAMPLE"}},
	} {
		if _, _, err := store.ListTorrentGroups(context.Background(), filters); !errors.Is(err, ErrInvalidGroupFilter) {
			t.Fatalf("ListTorrentGroups(%+v) error = %v, want ErrInvalidGroupFilter", filters, err)
		}
	}
}

func TestListTorrentGroupsStructuredQueryUsesBoundParametersAndCorrectMultiInstanceSemantics(t *testing.T) {
	store := openTestStore(t)
	store.now = func() time.Time { return time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC) }
	transmission := seedDownloader(t, store)
	qbittorrent, err := store.CreateDownloader(context.Background(), CreateDownloaderParams{
		Name: "qBittorrent", Kind: "qbittorrent", BaseURL: "http://qb:8080",
		StorageID: transmission.StorageID, StorageName: "media", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record := func(downloader Downloader, hash, groupID, name, path, state, tracker string, size, addedAt int64) TorrentRecord {
		item := operationTestRecord(downloader, hash, groupID)
		item.Name = name
		item.CanonicalPath = path
		item.WantedBytes = size
		item.AddedAt = time.Unix(addedAt, 0).UTC()
		item.Runtime.Status = state
		item.Trackers = []TrackerRecord{{HostIdentity: tracker}}
		return item
	}
	first := record(
		transmission, "episode-one", "group-series", "Series Episode 01", "/shows/series/episode01",
		"seeding", "tracker.alpha.example", 100, 100,
	)
	second := record(
		qbittorrent, "episode-two", "group-series", "Series Episode 02", "/shows/series/episode02",
		"paused", "tracker.beta.example", 200, 200,
	)
	injectionText := `%\' OR 1=1 --`
	injection := record(
		transmission, "injection", "group-injection", "Literal "+injectionText,
		"/shows/literal", "seeding", "tracker.literal.example", 50, 300,
	)
	other := record(
		transmission, "other", "group-other", "Other", "/movies/other",
		"seeding", "tracker.other.example", 500, 400,
	)
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: transmission.ID, Mode: "full", Complete: true,
		Torrents: []TorrentRecord{first, injection, other},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: qbittorrent.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{second},
	}); err != nil {
		t.Fatal(err)
	}

	assertIDs := func(t *testing.T, query *TorrentGroupQuery, expected ...string) {
		t.Helper()
		groups, total, err := store.ListTorrentGroups(context.Background(), GroupFilters{Query: query, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		actual := make([]string, 0, len(groups))
		for _, group := range groups {
			actual = append(actual, group.ID)
		}
		slices.Sort(actual)
		slices.Sort(expected)
		if total != len(expected) || !slices.Equal(actual, expected) {
			t.Fatalf("query returned IDs %v (total %d), want %v", actual, total, expected)
		}
	}

	// LIKE metacharacters, quotes, and SQL-looking text remain a single bound
	// value. They cannot turn the predicate into an always-true SQL fragment.
	assertIDs(t, groupQueryDocument(groupQueryCondition(t, "group_name", "contains", injectionText)), "group-injection")

	// Independent instance conditions may be satisfied by different members.
	assertIDs(t, groupQueryDocument(
		groupQueryCondition(t, "path", "contains", "episode01"),
		groupQueryCondition(t, "state", "in", []string{"paused"}),
	), "group-series")

	// An instance-scoped group requires all of its conditions to match the same
	// torrent instance, so the split Episode 01/paused match is rejected.
	assertIDs(t, groupQueryDocument(TorrentGroupQueryNode{
		Type: "group", Combinator: "and", Scope: "instance", Children: []TorrentGroupQueryNode{
			groupQueryCondition(t, "path", "contains", "episode01"),
			groupQueryCondition(t, "state", "in", []string{"paused"}),
		},
	}))
	assertIDs(t, groupQueryDocument(TorrentGroupQueryNode{
		Type: "group", Combinator: "and", Scope: "instance", Children: []TorrentGroupQueryNode{
			groupQueryCondition(t, "instance_name", "ends_with", "02"),
			groupQueryCondition(t, "state", "in", []string{"paused"}),
			groupQueryCondition(t, "site", "in", []string{"tracker:tracker.beta.example"}),
		},
	}), "group-series")
	assertIDs(t, groupQueryDocument(
		groupQueryCondition(t, "site", "contains_all", []string{
			"tracker:tracker.alpha.example", "tracker:tracker.beta.example",
		}),
	), "group-series")
	assertIDs(t, groupQueryDocument(TorrentGroupQueryNode{
		Type: "group", Combinator: "and", Scope: "instance", Children: []TorrentGroupQueryNode{
			groupQueryCondition(t, "site", "contains_all", []string{
				"tracker:tracker.alpha.example", "tracker:tracker.beta.example",
			}),
		},
	}))

	// Negative operators mean no member may match the positive predicate. A
	// second non-matching instance must not make the group pass.
	assertIDs(t, groupQueryDocument(
		groupQueryCondition(t, "path", "not_contains", "episode01"),
		groupQueryCondition(t, "state", "not_in", []string{"paused"}),
		groupQueryCondition(t, "site", "not_in", []string{"tracker:tracker.beta.example"}),
	), "group-injection", "group-other")
}

func TestListTorrentGroupsStructuredQuerySupportsMetricsDatesEnumsAndNegatedGroups(t *testing.T) {
	store := openTestStore(t)
	store.now = func() time.Time { return time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC) }
	downloader := seedDownloader(t, store)
	record := operationTestRecord(downloader, "one", "group-one")
	record.Name = "One"
	record.WantedBytes = 1024
	record.AddedAt = time.Date(2026, time.July, 20, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	record.Runtime.Status = "seeding"
	record.Trackers = []TrackerRecord{{HostIdentity: "tracker.unmapped.example"}}
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	negated := true
	query := groupQueryDocument(
		groupQueryCondition(t, "size", "between", []int64{1000, 2000}),
		groupQueryCondition(t, "instance_count", "eq", int64(1)),
		groupQueryCondition(t, "site_count", "gte", int64(1)),
		groupQueryCondition(t, "downloader_count", "lte", int64(1)),
		groupQueryCondition(t, "data_copy_count", "ne", int64(2)),
		groupQueryCondition(t, "oldest_added_at", "on", "2026-07-20T00:00:00+08:00"),
		groupQueryCondition(t, "updated_at", "between", []string{"2026-07-21T00:00:00Z", "2026-07-21T00:00:00Z"}),
		groupQueryCondition(t, "locked", "eq", false),
		groupQueryCondition(t, "grouping_method", "eq", "auto"),
		groupQueryCondition(t, "confidence", "eq", "verified"),
		groupQueryCondition(t, "stale", "eq", false),
		groupQueryCondition(t, "has_unmapped_tracker", "eq", true),
		TorrentGroupQueryNode{
			Type: "group", Combinator: "or", Negated: &negated,
			Children: []TorrentGroupQueryNode{groupQueryCondition(t, "group_name", "eq", "Never")},
		},
	)
	groups, total, err := store.ListTorrentGroups(context.Background(), GroupFilters{
		Query: query, StaleBefore: time.Unix(0, 0), Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(groups) != 1 || groups[0].ID != "group-one" {
		t.Fatalf("structured metrics query groups = %+v, total = %d", groups, total)
	}
}

func TestListTorrentGroupsRejectsUnsafeStructuredQueries(t *testing.T) {
	store := openTestStore(t)
	condition := func(field, operator string, raw string) TorrentGroupQueryNode {
		return TorrentGroupQueryNode{Type: "condition", Field: field, Operator: operator, Value: json.RawMessage(raw)}
	}
	tooDeep := groupQueryDocument(TorrentGroupQueryNode{
		Type: "group", Combinator: "and", Children: []TorrentGroupQueryNode{{
			Type: "group", Combinator: "and", Children: []TorrentGroupQueryNode{{
				Type: "group", Combinator: "and", Children: []TorrentGroupQueryNode{
					condition("group_name", "eq", `"name"`),
				},
			}},
		}},
	})
	tooMany := make([]TorrentGroupQueryNode, 31)
	for index := range tooMany {
		tooMany[index] = condition("size", "gte", "0")
	}
	invalid := []*TorrentGroupQuery{
		{Version: 2, Root: groupQueryDocument(condition("group_name", "eq", `"name"`)).Root},
		{Version: 1},
		tooDeep,
		groupQueryDocument(TorrentGroupQueryNode{Type: "group", Combinator: "and", Children: tooMany}),
		groupQueryDocument(condition("private_hash", "eq", `"secret"`)),
		groupQueryDocument(condition("size", "gte", `1.5`)),
		groupQueryDocument(condition("instance_count", "gt", `1000000001`)),
		groupQueryDocument(condition("oldest_added_at", "before", `"not-a-date"`)),
		groupQueryDocument(condition("site", "in", `["tracker:TRACKER.EXAMPLE"]`)),
		groupQueryDocument(condition("locked", "eq", `"true"`)),
		groupQueryDocument(condition("grouping_method", "eq", `"sql"`)),
		groupQueryDocument(condition("confidence", "eq", `"certain"`)),
	}
	for _, query := range invalid {
		if _, _, err := store.ListTorrentGroups(context.Background(), GroupFilters{Query: query, Limit: 20}); !errors.Is(err, ErrInvalidGroupFilter) {
			t.Fatalf("ListTorrentGroups(%+v) error = %v, want ErrInvalidGroupFilter", query, err)
		}
	}
}

func TestStructuredQueryDateBoundariesHonorIANATimezones(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		date     string
		duration time.Duration
	}{
		{date: "2026-03-08T00:00:00-05:00", duration: 23 * time.Hour},
		{date: "2026-11-01T00:00:00-04:00", duration: 25 * time.Hour},
	} {
		_, args, err := compileGroupTimeCondition(
			"gm.oldest_added_at", "on", json.RawMessage(`"`+test.date+`"`), location,
		)
		if err != nil {
			t.Fatal(err)
		}
		start, startOK := args[0].(int64)
		end, endOK := args[1].(int64)
		if !startOK || !endOK {
			t.Fatalf("date boundary args = %#v, want Unix seconds", args)
		}
		if got := time.Duration(end-start) * time.Second; got != test.duration {
			t.Fatalf("date %s duration = %s, want %s", test.date, got, test.duration)
		}
	}
}

func TestUndoRefusesWhenAffectedGroupVersionChanged(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	source := operationTestRecord(downloader, "source-one", "source-group")
	target := operationTestRecord(downloader, "target-one", "target-group")
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{source, target},
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := store.MoveInstance(context.Background(), MoveInstanceParams{
		InstanceID: source.ID, SourceGroupID: source.ContentGroupID, TargetGroupID: target.ContentGroupID,
		ExpectedSourceVersion: 1, ExpectedTargetVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetGroupLock(context.Background(), target.ContentGroupID, 2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UndoGroupOperation(context.Background(), detail.OperationID); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("UndoGroupOperation() error = %v, want version conflict", err)
	}
	if groupID, assignment := instanceMembership(t, store, source.ID); groupID != target.ContentGroupID || assignment != "manual" {
		t.Fatalf("failed undo partially changed membership to (%q, %q)", groupID, assignment)
	}
	var undoneAt sql.NullInt64
	if err := store.db.QueryRow(`SELECT undone_at FROM group_operations WHERE id = ?`, detail.OperationID).Scan(&undoneAt); err != nil {
		t.Fatal(err)
	}
	if undoneAt.Valid {
		t.Fatal("conflicting undo marked operation as undone")
	}
}

func TestUndoRefusesWhenAffectedMemberWasTombstoned(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	source := operationTestRecord(downloader, "source-one", "source-group")
	target := operationTestRecord(downloader, "target-one", "target-group")
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{source, target},
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := store.MoveInstance(context.Background(), MoveInstanceParams{
		InstanceID: source.ID, SourceGroupID: source.ContentGroupID, TargetGroupID: target.ContentGroupID,
		ExpectedSourceVersion: 1, ExpectedTargetVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{target},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UndoGroupOperation(context.Background(), detail.OperationID); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("UndoGroupOperation() error = %v, want version conflict", err)
	}
}

func TestSplitUndoRetiresCreatedGroupAndRestoresSource(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	first := operationTestRecord(downloader, "first", "source-group")
	second := operationTestRecord(downloader, "second", "source-group")
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{first, second},
	}); err != nil {
		t.Fatal(err)
	}

	created, err := store.SplitGroup(context.Background(), SplitGroupParams{
		GroupID: first.ContentGroupID, ExpectedVersion: 1, InstanceIDs: []string{first.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.OperationID == "" || created.ID == first.ContentGroupID {
		t.Fatalf("unexpected split result: %+v", created)
	}
	if _, err := store.UndoGroupOperation(context.Background(), created.OperationID); err != nil {
		t.Fatal(err)
	}
	if version, deleted := groupOperationState(t, store, first.ContentGroupID); version != 3 || deleted {
		t.Fatalf("source state after undo = (version %d, deleted %t)", version, deleted)
	}
	if version, deleted := groupOperationState(t, store, created.ID); version != 2 || !deleted {
		t.Fatalf("created group state after undo = (version %d, deleted %t)", version, deleted)
	}
	if groupID, assignment := instanceMembership(t, store, first.ID); groupID != first.ContentGroupID || assignment != "auto" {
		t.Fatalf("split member after undo = (%q, %q)", groupID, assignment)
	}
}

func TestMergeUndoRestoresGroupsAndMixedAssignmentSources(t *testing.T) {
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	first := operationTestRecord(downloader, "first", "first-group")
	second := operationTestRecord(downloader, "second", "second-group")
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{first, second},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE torrent_instances SET assignment_source = 'manual' WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE content_groups SET mode = 'manual', confidence = 'manual' WHERE id = ?`, first.ContentGroupID); err != nil {
		t.Fatal(err)
	}

	merged, err := store.MergeGroups(context.Background(), MergeGroupsParams{
		GroupIDs: []string{second.ContentGroupID, first.ContentGroupID},
		ExpectedVersions: map[string]int{
			first.ContentGroupID:  1,
			second.ContentGroupID: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UndoGroupOperation(context.Background(), merged.OperationID); err != nil {
		t.Fatal(err)
	}
	for _, groupID := range []string{first.ContentGroupID, second.ContentGroupID} {
		if version, deleted := groupOperationState(t, store, groupID); version != 3 || deleted {
			t.Fatalf("restored group %s state = (version %d, deleted %t)", groupID, version, deleted)
		}
	}
	if version, deleted := groupOperationState(t, store, merged.ID); version != 2 || !deleted {
		t.Fatalf("merged group state after undo = (version %d, deleted %t)", version, deleted)
	}
	if groupID, assignment := instanceMembership(t, store, first.ID); groupID != first.ContentGroupID || assignment != "manual" {
		t.Fatalf("first membership after undo = (%q, %q)", groupID, assignment)
	}
	if groupID, assignment := instanceMembership(t, store, second.ID); groupID != second.ContentGroupID || assignment != "auto" {
		t.Fatalf("second membership after undo = (%q, %q)", groupID, assignment)
	}
	var mode, confidence string
	if err := store.db.QueryRow(`SELECT mode, confidence FROM content_groups WHERE id = ?`, first.ContentGroupID).
		Scan(&mode, &confidence); err != nil {
		t.Fatal(err)
	}
	if mode != "manual" || confidence != "manual" {
		t.Fatalf("restored first group metadata = (%q, %q)", mode, confidence)
	}
}
