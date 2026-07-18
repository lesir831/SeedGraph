package domain

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

func groupingTorrent(id, downloader, storage, canonicalPath string, wanted int64, sizes ...int64) TorrentInstance {
	return TorrentInstance{
		ID:                 id,
		DownloaderID:       downloader,
		ExternalKey:        "hash-" + id,
		StorageID:          storage,
		ContentPath:        canonicalPath,
		CanonicalPath:      canonicalPath,
		WantedBytes:        wanted,
		SelectedFilesKnown: true,
		SelectedFileSizes:  append([]int64(nil), sizes...),
		DownloaderOnline:   true,
	}
}

func TestPlanAutomaticGroupsVerifiedAcrossDownloaders(t *testing.T) {
	t.Parallel()
	instances := []TorrentInstance{
		groupingTorrent("qb-a", "qb", "media", "/movies/Film", 600, 100, 200, 300),
		groupingTorrent("tr-a", "tr", "media", "/movies/Film", 600, 300, 100, 200),
	}

	plan, err := PlanAutomaticGroups(instances)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ContentGroups) != 1 || len(plan.DataGroups) != 1 || len(plan.Memberships) != 2 {
		t.Fatalf("unexpected plan sizes: content=%d data=%d memberships=%d", len(plan.ContentGroups), len(plan.DataGroups), len(plan.Memberships))
	}
	if plan.ContentGroups[0].Confidence != ConfidenceVerified || plan.DataGroups[0].Confidence != ConfidenceVerified {
		t.Fatalf("confidence = content:%q data:%q, want verified", plan.ContentGroups[0].Confidence, plan.DataGroups[0].Confidence)
	}
	if plan.ContentGroups[0].ID == plan.DataGroups[0].ID {
		t.Fatal("logical and physical groups share an ID")
	}
	if plan.Memberships[0].ContentGroupID != plan.Memberships[1].ContentGroupID ||
		plan.Memberships[0].DataGroupID != plan.Memberships[1].DataGroupID {
		t.Fatalf("matching instances were not grouped together: %#v", plan.Memberships)
	}
}

func TestPlanAutomaticGroupsSeparatesFingerprintConflicts(t *testing.T) {
	t.Parallel()
	instances := []TorrentInstance{
		groupingTorrent("a", "qb", "media", "/shows/Season", 300, 100, 200),
		groupingTorrent("b", "tr", "media", "/shows/Season", 300, 50, 250),
	}
	plan, err := PlanAutomaticGroups(instances)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ContentGroups) != 2 || len(plan.DataGroups) != 2 {
		t.Fatalf("conflicting file layouts must split: content=%d data=%d", len(plan.ContentGroups), len(plan.DataGroups))
	}
	if plan.Memberships[0].DataGroupID == plan.Memberships[1].DataGroupID {
		t.Fatal("different file-size fingerprints share a DataGroup")
	}
}

func TestPlanAutomaticGroupsSeparatesIdenticalPathsOnDifferentStorages(t *testing.T) {
	t.Parallel()
	instances := []TorrentInstance{
		groupingTorrent("a", "qb", "storage-a", "/downloads/Film.mkv", 100, 100),
		groupingTorrent("b", "tr", "storage-b", "/downloads/Film.mkv", 100, 100),
	}
	plan, err := PlanAutomaticGroups(instances)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ContentGroups) != 2 || len(plan.DataGroups) != 2 {
		t.Fatalf("same textual path on separate storage was grouped: %#v", plan)
	}
}

func TestPlanAutomaticGroupsUsesCanonicalRatherThanDownloaderPath(t *testing.T) {
	t.Parallel()
	a := groupingTorrent("a", "qb", "media", "/movies/Film.mkv", 100, 100)
	b := groupingTorrent("b", "tr", "media", "/movies/Film.mkv", 100, 100)
	a.ContentPath = "/downloads/movies/Film.mkv"
	b.ContentPath = "/data/media/movies/Film.mkv"

	plan, err := PlanAutomaticGroups([]TorrentInstance{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.DataGroups) != 1 {
		t.Fatalf("mapped canonical paths did not group: %#v", plan.DataGroups)
	}
}

func TestPlanAutomaticGroupsUnknownEvidenceIsTentative(t *testing.T) {
	t.Parallel()
	known := groupingTorrent("known", "qb", "media", "/movies/Film", 300, 100, 200)
	unknown := groupingTorrent("unknown", "tr", "media", "/movies/Film", 300)
	unknown.SelectedFilesKnown = false
	unknown.SelectedFileSizes = nil
	unknown.SelectedFileCount = 2

	plan, err := PlanAutomaticGroups([]TorrentInstance{known, unknown})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.DataGroups) != 1 {
		t.Fatalf("one compatible unknown should join the candidate: got %d groups", len(plan.DataGroups))
	}
	if plan.DataGroups[0].Confidence != ConfidenceTentative {
		t.Fatalf("confidence = %q, want tentative", plan.DataGroups[0].Confidence)
	}
}

func TestPlanAutomaticGroupsKeepsAmbiguousUnknownSeparate(t *testing.T) {
	t.Parallel()
	first := groupingTorrent("first", "qb", "media", "/shows/Season", 300, 100, 200)
	second := groupingTorrent("second", "tr", "media", "/shows/Season", 300, 50, 250)
	unknown := groupingTorrent("unknown", "tr2", "media", "/shows/Season", 300)
	unknown.SelectedFilesKnown = false
	unknown.SelectedFileSizes = nil
	unknown.SelectedFileCount = 2

	plan, err := PlanAutomaticGroups([]TorrentInstance{first, second, unknown})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.DataGroups) != 3 {
		t.Fatalf("ambiguous unknown should have its own tentative group: got %d", len(plan.DataGroups))
	}
	foundTentativeSingleton := false
	for _, group := range plan.DataGroups {
		if group.Confidence == ConfidenceTentative && reflect.DeepEqual(group.MemberIDs, []string{"unknown"}) {
			foundTentativeSingleton = true
		}
	}
	if !foundTentativeSingleton {
		t.Fatalf("tentative singleton not found: %#v", plan.DataGroups)
	}
}

func TestPlanAutomaticGroupsPreservesManualContentAssignmentButPlansPhysicalData(t *testing.T) {
	t.Parallel()
	auto := groupingTorrent("auto", "qb", "media", "/movies/Film", 100, 100)
	manual := groupingTorrent("manual", "tr", "media", "/movies/Film", 100, 100)
	manual.AssignmentSource = AssignmentManual
	manual.ContentGroupID = "cg_user_choice"

	plan, err := PlanAutomaticGroups([]TorrentInstance{auto, manual})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ContentGroups) != 1 || !reflect.DeepEqual(plan.ContentGroups[0].MemberIDs, []string{"auto"}) {
		t.Fatalf("manual member was overwritten by auto content grouping: %#v", plan.ContentGroups)
	}
	if len(plan.DataGroups) != 1 || !reflect.DeepEqual(plan.DataGroups[0].MemberIDs, []string{"auto", "manual"}) {
		t.Fatalf("physical membership should remain independent: %#v", plan.DataGroups)
	}
	for _, membership := range plan.Memberships {
		if membership.TorrentInstanceID == "manual" {
			if membership.ContentGroupID != "cg_user_choice" || membership.AssignmentSource != AssignmentManual {
				t.Fatalf("manual membership changed: %#v", membership)
			}
			if membership.DataGroupID != plan.DataGroups[0].ID {
				t.Fatalf("manual content member did not receive physical assignment: %#v", membership)
			}
		}
	}
}

func TestPlanAutomaticGroupsManualContentMergeDoesNotMergeDifferentStorages(t *testing.T) {
	t.Parallel()
	first := groupingTorrent("first", "qb", "disk-a", "/movies/Film", 100, 100)
	second := groupingTorrent("second", "tr", "disk-b", "/movies/Film", 100, 100)
	first.AssignmentSource = AssignmentManual
	first.ContentGroupID = "cg-manual"
	second.AssignmentSource = AssignmentManual
	second.ContentGroupID = "cg-manual"

	plan, err := PlanAutomaticGroups([]TorrentInstance{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ContentGroups) != 0 {
		t.Fatalf("automatic planner recreated a manual content group: %#v", plan.ContentGroups)
	}
	if len(plan.DataGroups) != 2 {
		t.Fatalf("two physical storages were merged: %#v", plan.DataGroups)
	}
	if len(plan.Memberships) != 2 ||
		plan.Memberships[0].ContentGroupID != "cg-manual" ||
		plan.Memberships[1].ContentGroupID != "cg-manual" ||
		plan.Memberships[0].DataGroupID == plan.Memberships[1].DataGroupID {
		t.Fatalf("logical/physical assignments are not independent: %#v", plan.Memberships)
	}
}

func TestPlanAutomaticGroupsDeterministicAcrossInputOrder(t *testing.T) {
	t.Parallel()
	instances := []TorrentInstance{
		groupingTorrent("c", "qb", "media", "/c", 30, 30),
		groupingTorrent("a", "qb", "media", "/a", 10, 10),
		groupingTorrent("b", "tr", "media", "/a", 10, 10),
	}
	first, err := PlanAutomaticGroups(instances)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].ID < instances[j].ID })
	second, err := PlanAutomaticGroups(instances)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plan changed with input order:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func TestPlanAutomaticGroupsRejectsDuplicateAndInvalidEvidence(t *testing.T) {
	t.Parallel()
	instance := groupingTorrent("same", "qb", "media", "/file", 1, 1)
	if _, err := PlanAutomaticGroups([]TorrentInstance{instance, instance}); !errors.Is(err, ErrDuplicateTorrentID) {
		t.Fatalf("duplicate error = %v", err)
	}

	bad := groupingTorrent("bad", "qb", "media", "/file", 3, 1, 2)
	bad.FileSizeFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := PlanAutomaticGroups([]TorrentInstance{bad}); !errors.Is(err, ErrInvalidTorrentInstance) {
		t.Fatalf("fingerprint mismatch error = %v", err)
	}
}

func TestBuildAutoKeyNormalizesPathAndIncludesStorageAndSize(t *testing.T) {
	t.Parallel()
	base, err := BuildAutoKey("media", "/movies/../films/a", 100)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := BuildAutoKey("media", "/films/a", 100)
	if err != nil {
		t.Fatal(err)
	}
	if base != normalized {
		t.Fatalf("normalized paths produced different keys: %s != %s", base, normalized)
	}
	otherStorage, _ := BuildAutoKey("backup", "/films/a", 100)
	otherSize, _ := BuildAutoKey("media", "/films/a", 101)
	if base == otherStorage || base == otherSize {
		t.Fatal("auto key omitted storage or exact byte size")
	}
}
