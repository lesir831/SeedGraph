package domain

import (
	"reflect"
	"testing"
	"time"
)

func deletionFixture(confidence GroupConfidence) DeletionSnapshot {
	fingerprint, err := SelectedFileSizeFingerprint([]int64{100})
	if err != nil {
		panic(err)
	}
	instances := []TorrentInstance{
		{
			ID:                  "a",
			DownloaderID:        "qb",
			DownloaderName:      "qBittorrent",
			ExternalKey:         "hash-a",
			Name:                "Film episode 01",
			StorageID:           "media",
			ContentPath:         "/downloads/Film",
			CanonicalPath:       "/movies/Film",
			WantedBytes:         100,
			SelectedFilesKnown:  true,
			SelectedFileCount:   1,
			FileSizeFingerprint: fingerprint,
			FileManifestKnown:   true,
			Files: []TorrentFile{{
				SourcePath:    "/downloads/Film/episode-01.mkv",
				CanonicalPath: "/movies/Film/episode-01.mkv",
				Size:          100,
				Selected:      true,
			}},
			ContentGroupID:   "cg",
			DataGroupID:      "dg",
			DownloaderOnline: true,
		},
		{
			ID:                  "b",
			DownloaderID:        "tr",
			DownloaderName:      "Transmission",
			ExternalKey:         "hash-b",
			Name:                "Film episode 01",
			StorageID:           "media",
			ContentPath:         "/data/Film",
			CanonicalPath:       "/movies/Film",
			WantedBytes:         100,
			SelectedFilesKnown:  true,
			SelectedFileCount:   1,
			FileSizeFingerprint: fingerprint,
			FileManifestKnown:   true,
			Files: []TorrentFile{{
				SourcePath:    "/data/Film/episode-01.mkv",
				CanonicalPath: "/movies/Film/episode-01.mkv",
				Size:          100,
				Selected:      true,
			}},
			ContentGroupID:   "cg",
			DataGroupID:      "dg",
			DownloaderOnline: true,
		},
	}
	return DeletionSnapshot{
		Instances: instances,
		ContentGroups: []ContentGroup{{
			ID:          "cg",
			Version:     4,
			Mode:        GroupModeAuto,
			Confidence:  confidence,
			MemberIDs:   []string{"a", "b"},
			TaskCount:   2,
			UniqueSites: 0, // Deliberately irrelevant to physical reference counting.
		}},
		DataGroups: []DataGroup{{
			ID:                  "dg",
			Version:             7,
			StorageID:           "media",
			CanonicalPath:       "/movies/Film",
			WantedBytes:         100,
			SelectedFileCount:   1,
			FileSizeFingerprint: fingerprint,
			Confidence:          confidence,
			PhysicalKey:         "physical-key",
			MemberIDs:           []string{"a", "b"},
		}},
		DownloaderStates: []DownloaderState{
			{ID: "qb", StorageIDs: []string{"media"}, Online: true},
			{ID: "tr", StorageIDs: []string{"media"}, Online: true},
		},
	}
}

func deleteRequest(ids ...string) DeleteRequest {
	return DeleteRequest{
		InstanceIDs:                  ids,
		ExpectedContentGroupVersions: map[string]uint64{"cg": 4},
		ExpectedDataGroupVersions:    map[string]uint64{"dg": 7},
		RequireExpectedVersions:      true,
		RequireFreshStorageSnapshot:  true,
	}
}

func hasBlocker(plan DeletePlan, code DeleteBlockerCode) bool {
	for _, blocker := range plan.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func TestPlanDeletionKeepsDataWhenPhysicalReferenceRemains(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	plan := PlanDeletion(deleteRequest("a"), snapshot)
	if !plan.Executable {
		t.Fatalf("plan unexpectedly blocked: %#v", plan.Blockers)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].DeleteData {
		t.Fatalf("remaining DataGroup reference must keep data: %#v", plan.Steps)
	}
}

func TestPlanDeletionDeletesDataExactlyOnceAndLast(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	plan := PlanDeletion(deleteRequest("b", "a"), snapshot)
	if !plan.Executable {
		t.Fatalf("plan unexpectedly blocked: %#v", plan.Blockers)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(plan.Steps))
	}
	if plan.Steps[0].DeleteData || !plan.Steps[1].DeleteData {
		t.Fatalf("file-deleting step must be last: %#v", plan.Steps)
	}
	if plan.Steps[0].Order != 1 || plan.Steps[1].Order != 2 {
		t.Fatalf("unexpected step order: %#v", plan.Steps)
	}
	if plan.Steps[1].FileManifestFingerprint == "" {
		t.Fatal("file-deleting step does not capture its file manifest")
	}
}

func TestPlanDeletionIgnoresLogicalSiteCountAndContentGrouping(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	snapshot.ContentGroups[0].UniqueSites = 99
	snapshot.ContentGroups = append(snapshot.ContentGroups, ContentGroup{
		ID: "manual-other", Version: 2, Mode: GroupModeManual, Confidence: ConfidenceManual,
	})
	snapshot.Instances[1].ContentGroupID = "manual-other"
	request := deleteRequest("a", "b")
	request.ExpectedContentGroupVersions["manual-other"] = 2

	plan := PlanDeletion(request, snapshot)
	if !plan.Executable {
		t.Fatalf("logical content grouping affected physical deletion: %#v", plan.Blockers)
	}
	deleteDataCount := 0
	for _, step := range plan.Steps {
		if step.DeleteData {
			deleteDataCount++
		}
	}
	if deleteDataCount != 1 {
		t.Fatalf("delete_data count = %d, want 1", deleteDataCount)
	}
}

func TestPlanDeletionDeletesEachPhysicalCopyInOneManualContentGroup(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	snapshot.DataGroups[0].MemberIDs = []string{"a"}
	snapshot.Instances[1].StorageID = "backup"
	snapshot.Instances[1].DataGroupID = "dg-backup"
	snapshot.DataGroups = append(snapshot.DataGroups, DataGroup{
		ID:                  "dg-backup",
		Version:             1,
		StorageID:           "backup",
		CanonicalPath:       "/movies/Film",
		WantedBytes:         100,
		SelectedFileCount:   snapshot.DataGroups[0].SelectedFileCount,
		FileSizeFingerprint: snapshot.DataGroups[0].FileSizeFingerprint,
		Confidence:          ConfidenceVerified,
		PhysicalKey:         "backup-key",
		MemberIDs:           []string{"b"},
	})
	snapshot.DownloaderStates[0].StorageIDs = []string{"media"}
	snapshot.DownloaderStates[1].StorageIDs = []string{"backup"}
	request := deleteRequest("a", "b")
	request.ExpectedDataGroupVersions["dg-backup"] = 1

	plan := PlanDeletion(request, snapshot)
	if !plan.Executable {
		t.Fatalf("manual logical merge blocked safe physical cleanup: %#v", plan.Blockers)
	}
	if len(plan.Steps) != 2 || !plan.Steps[0].DeleteData || !plan.Steps[1].DeleteData {
		t.Fatalf("each last physical reference should delete its own data: %#v", plan.Steps)
	}
}

func TestPlanDeletionAllowsTaskOnlyFromTentativeGroup(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceTentative)
	plan := PlanDeletion(deleteRequest("a"), snapshot)
	if !plan.Executable {
		t.Fatalf("safe task-only deletion blocked: %#v", plan.Blockers)
	}
	if plan.Steps[0].DeleteData {
		t.Fatal("tentative group unexpectedly deleted data")
	}
}

func TestPlanDeletionBlocksFinalTentativeReference(t *testing.T) {
	t.Parallel()
	plan := PlanDeletion(deleteRequest("a", "b"), deletionFixture(ConfidenceTentative))
	if plan.Executable || !hasBlocker(plan, BlockerUnverifiedDataGroup) {
		t.Fatalf("tentative final deletion was not blocked: %#v", plan)
	}
}

func TestPlanDeletionAllowsManuallyConfirmedPhysicalGroup(t *testing.T) {
	t.Parallel()
	plan := PlanDeletion(deleteRequest("a", "b"), deletionFixture(ConfidenceManual))
	if !plan.Executable {
		t.Fatalf("manual confidence should authorize deletion: %#v", plan.Blockers)
	}
}

func TestPlanDeletionBlocksConflictingPhysicalPath(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	snapshot.Instances = append(snapshot.Instances, TorrentInstance{
		ID:                "occupant",
		DownloaderID:      "qb",
		DownloaderName:    "NAS qBittorrent",
		ExternalKey:       "hash-occupant",
		Name:              "Film extras",
		StorageID:         "media",
		ContentPath:       "/downloads/movies/Film",
		CanonicalPath:     "/movies/Film",
		WantedBytes:       101,
		FileManifestKnown: true,
		Files: []TorrentFile{{
			SourcePath:    "/downloads/movies/Film/episode-01.mkv",
			CanonicalPath: "/movies/Film/episode-01.mkv",
			Size:          100,
			Selected:      true,
		}},
		ContentGroupID:   "cg-other",
		DataGroupID:      "dg-other",
		DownloaderOnline: true,
	})
	snapshot.ContentGroups = append(snapshot.ContentGroups, ContentGroup{ID: "cg-other", Version: 1})
	snapshot.DataGroups = append(snapshot.DataGroups, DataGroup{
		ID: "dg-other", Version: 1, StorageID: "media", CanonicalPath: "/movies/Film", WantedBytes: 101,
		Confidence: ConfidenceVerified,
	})

	plan := PlanDeletion(deleteRequest("a", "b"), snapshot)
	if plan.Executable || !hasBlocker(plan, BlockerConflictingPathOccupant) {
		t.Fatalf("same-path occupant was not blocked: %#v", plan)
	}
	var conflict DeleteBlocker
	for _, blocker := range plan.Blockers {
		if blocker.Code == BlockerConflictingPathOccupant {
			conflict = blocker
			break
		}
	}
	if conflict.InstanceName != "Film extras" || conflict.DownloaderName != "NAS qBittorrent" || conflict.Path != "/downloads/movies/Film/episode-01.mkv" {
		t.Fatalf("conflicting task details missing: %#v", conflict)
	}
}

func TestPlanDeletionAllowsDifferentEpisodeFilesInSharedDirectory(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	snapshot.Instances = append(snapshot.Instances, TorrentInstance{
		ID: "episode-02", DownloaderID: "tr", ExternalKey: "hash-episode-02", Name: "Film episode 02",
		StorageID: "media", ContentPath: "/data/Film", CanonicalPath: "/movies/Film", WantedBytes: 101,
		FileManifestKnown: true,
		Files: []TorrentFile{{
			SourcePath:    "/data/Film/episode-02.mkv",
			CanonicalPath: "/movies/Film/episode-02.mkv",
			Size:          101,
			Selected:      true,
		}},
		ContentGroupID: "cg-episode-02", DataGroupID: "dg-episode-02", DownloaderOnline: true,
	})

	plan := PlanDeletion(deleteRequest("a", "b"), snapshot)
	if !plan.Executable {
		t.Fatalf("different episode in the same directory blocked deletion: %#v", plan.Blockers)
	}
}

func TestPlanDeletionBlocksWhenDeletingTorrentFileManifestIsMissing(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	snapshot.Instances[1].FileManifestKnown = false
	snapshot.Instances[1].Files = nil

	plan := PlanDeletion(deleteRequest("a", "b"), snapshot)
	if plan.Executable || !hasBlocker(plan, BlockerFileManifestMissing) {
		t.Fatalf("missing delete-owner file manifest was not blocked: %#v", plan)
	}
}

func TestPlanDeletionAllTaskOnlyStepsPrecedeEveryFileDelete(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	fingerprint, err := SelectedFileSizeFingerprint([]int64{1})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Instances = append(snapshot.Instances, TorrentInstance{
		ID: "c", DownloaderID: "qb", ExternalKey: "hash-c", StorageID: "other", CanonicalPath: "/c", WantedBytes: 1,
		SelectedFilesKnown: true, SelectedFileCount: 1, FileSizeFingerprint: fingerprint,
		FileManifestKnown: true, Files: []TorrentFile{{SourcePath: "/c", CanonicalPath: "/c", Size: 1, Selected: true}},
		ContentGroupID: "cg2", DataGroupID: "dg2", DownloaderOnline: true,
	})
	snapshot.ContentGroups = append(snapshot.ContentGroups, ContentGroup{ID: "cg2", Version: 1})
	snapshot.DataGroups = append(snapshot.DataGroups, DataGroup{
		ID: "dg2", Version: 1, StorageID: "other", CanonicalPath: "/c", WantedBytes: 1,
		SelectedFileCount: 1, FileSizeFingerprint: fingerprint, Confidence: ConfidenceVerified,
	})
	snapshot.DownloaderStates[0].StorageIDs = append(snapshot.DownloaderStates[0].StorageIDs, "other")
	request := deleteRequest("a", "b", "c")
	request.ExpectedContentGroupVersions["cg2"] = 1
	request.ExpectedDataGroupVersions["dg2"] = 1

	plan := PlanDeletion(request, snapshot)
	if !plan.Executable {
		t.Fatalf("plan unexpectedly blocked: %#v", plan.Blockers)
	}
	seenDeleteData := false
	for _, step := range plan.Steps {
		if step.DeleteData {
			seenDeleteData = true
			continue
		}
		if seenDeleteData {
			t.Fatalf("task-only step appears after file deletion: %#v", plan.Steps)
		}
	}
}

func TestPlanDeletionBlocksVersionDriftAndMissingPreconditions(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	request := deleteRequest("a")
	request.ExpectedContentGroupVersions["cg"] = 3
	plan := PlanDeletion(request, snapshot)
	if plan.Executable || !hasBlocker(plan, BlockerVersionConflict) {
		t.Fatalf("version drift was not blocked: %#v", plan)
	}

	request = deleteRequest("a")
	delete(request.ExpectedDataGroupVersions, "dg")
	plan = PlanDeletion(request, snapshot)
	if plan.Executable || !hasBlocker(plan, BlockerMissingExpectedVersion) {
		t.Fatalf("missing expected version was not blocked: %#v", plan)
	}
}

func TestPlanDeletionBlocksOfflineStaleAndIncompleteStorageViews(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*DeletionSnapshot)
		code   DeleteBlockerCode
	}{
		{
			name: "selected downloader offline",
			mutate: func(snapshot *DeletionSnapshot) {
				snapshot.Instances[0].DownloaderOnline = false
			},
			code: BlockerDownloaderOffline,
		},
		{
			name: "selected data stale",
			mutate: func(snapshot *DeletionSnapshot) {
				snapshot.Instances[0].Stale = true
			},
			code: BlockerStaleData,
		},
		{
			name: "storage coverage missing",
			mutate: func(snapshot *DeletionSnapshot) {
				snapshot.DownloaderStates = nil
			},
			code: BlockerStorageSnapshotMissing,
		},
		{
			name: "shared downloader offline",
			mutate: func(snapshot *DeletionSnapshot) {
				snapshot.DownloaderStates[1].Online = false
			},
			code: BlockerStorageDownloaderOffline,
		},
		{
			name: "shared downloader stale",
			mutate: func(snapshot *DeletionSnapshot) {
				snapshot.DownloaderStates[1].Stale = true
			},
			code: BlockerStorageSnapshotStale,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot := deletionFixture(ConfidenceVerified)
			test.mutate(&snapshot)
			plan := PlanDeletion(deleteRequest("a", "b"), snapshot)
			if plan.Executable || !hasBlocker(plan, test.code) {
				t.Fatalf("blocker %q missing: %#v", test.code, plan)
			}
		})
	}
}

func TestPlanDeletionDoesNotCountTombstonesAsReferences(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	now := time.Now()
	snapshot.Instances[1].DeletedAt = &now
	plan := PlanDeletion(deleteRequest("a"), snapshot)
	if !plan.Executable {
		t.Fatalf("plan unexpectedly blocked: %#v", plan.Blockers)
	}
	if len(plan.Steps) != 1 || !plan.Steps[0].DeleteData {
		t.Fatalf("tombstone counted as an active reference: %#v", plan.Steps)
	}
}

func TestPlanDeletionDeterministicAndDeduplicatesSelection(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	first := PlanDeletion(deleteRequest("b", "a", "a"), snapshot)
	second := PlanDeletion(deleteRequest("a", "b"), snapshot)
	if first.ID != second.ID || !reflect.DeepEqual(first.Steps, second.Steps) {
		t.Fatalf("plan changed with duplicate/order: first=%#v second=%#v", first, second)
	}
}

func TestPlanDeletionIDChangesWhenDeletingFileManifestChanges(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	first := PlanDeletion(deleteRequest("a", "b"), snapshot)
	snapshot.Instances[1].Files[0].SourcePath = "/data/Film/episode-02.mkv"
	snapshot.Instances[1].Files[0].CanonicalPath = "/movies/Film/episode-02.mkv"
	second := PlanDeletion(deleteRequest("a", "b"), snapshot)
	if !first.Executable || !second.Executable {
		t.Fatalf("test plans unexpectedly blocked: first=%#v second=%#v", first.Blockers, second.Blockers)
	}
	if first.ID == second.ID {
		t.Fatal("delete plan ID ignored a changed file manifest")
	}
}

func TestPlanDeletionUnknownInstanceIsExplicitBlocker(t *testing.T) {
	t.Parallel()
	plan := PlanDeletion(deleteRequest("missing"), deletionFixture(ConfidenceVerified))
	if plan.Executable || !hasBlocker(plan, BlockerUnknownInstance) {
		t.Fatalf("unknown selection was not blocked: %#v", plan)
	}
}

func TestPlanDeletionDetectsDataGroupMetadataDrift(t *testing.T) {
	t.Parallel()
	snapshot := deletionFixture(ConfidenceVerified)
	snapshot.DataGroups[0].CanonicalPath = "/different"
	plan := PlanDeletion(deleteRequest("a"), snapshot)
	if plan.Executable || !hasBlocker(plan, BlockerInvalidSnapshot) {
		t.Fatalf("incoherent snapshot was not blocked: %#v", plan)
	}
}
