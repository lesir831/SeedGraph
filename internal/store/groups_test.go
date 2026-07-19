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
}

func TestListTorrentGroupsRejectsInvalidSort(t *testing.T) {
	store := openTestStore(t)
	for _, filters := range []GroupFilters{
		{SortBy: "updated_at", SortOrder: "desc"},
		{SortBy: "name", SortOrder: "sideways"},
		{SortOrder: "asc"},
	} {
		if _, _, err := store.ListTorrentGroups(context.Background(), filters); !errors.Is(err, ErrInvalidGroupSort) {
			t.Fatalf("ListTorrentGroups(%+v) error = %v, want ErrInvalidGroupSort", filters, err)
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
