package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"
)

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
