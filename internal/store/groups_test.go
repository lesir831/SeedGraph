package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
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
