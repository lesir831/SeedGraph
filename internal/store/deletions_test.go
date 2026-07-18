package store

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/lesir831/SeedGraph/internal/domain"
)

func savedDeleteJobPlan(record TorrentRecord, planID string, deleteData bool) SavedDeletePlan {
	return SavedDeletePlan{
		Plan: domain.DeletePlan{
			ID: planID, SelectedInstanceIDs: []string{record.ID}, Executable: true,
			Steps: []domain.DeleteStep{{
				Order: 1, InstanceID: record.ID, DownloaderID: record.DownloaderID,
				ExternalKey: record.StableHashKey, ContentGroupID: record.ContentGroupID,
				DataGroupID: record.DataGroupID, DeleteData: deleteData,
			}},
		},
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
}

func persistDeletePlanFixture(t *testing.T, store *Store, saved SavedDeletePlan) {
	t.Helper()
	if _, err := store.db.Exec(`
		INSERT INTO delete_plans(id, selection_json, snapshot_json, blocked, created_at, expires_at)
		VALUES(?, '{}', '{}', 0, ?, ?)`, saved.Plan.ID, saved.CreatedAt.Unix(), saved.ExpiresAt.Unix()); err != nil {
		t.Fatal(err)
	}
}

func seedDeleteJobStore(t *testing.T) (*Store, TorrentRecord) {
	t.Helper()
	store := openTestStore(t)
	downloader := seedDownloader(t, store)
	record := torrentRecord(downloader, "delete-hash")
	if _, err := store.ApplySync(context.Background(), ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	return store, record
}

func TestCreateDeleteJobIdempotencyIsBoundToPlan(t *testing.T) {
	store, record := seedDeleteJobStore(t)
	firstPlan := savedDeleteJobPlan(record, "delete-plan-one", false)
	secondPlan := savedDeleteJobPlan(record, "delete-plan-two", false)
	persistDeletePlanFixture(t, store, firstPlan)
	persistDeletePlanFixture(t, store, secondPlan)

	first, created, err := store.CreateDeleteJob(context.Background(), firstPlan, "same-key")
	if err != nil || !created {
		t.Fatalf("first CreateDeleteJob() = (%+v, %t, %v)", first, created, err)
	}
	expiredReplay := firstPlan
	expiredReplay.ExpiresAt = time.Now().Add(-time.Hour)
	replayed, created, err := store.CreateDeleteJob(context.Background(), expiredReplay, "same-key")
	if err != nil || created || replayed.ID != first.ID {
		t.Fatalf("expired idempotent replay = (%+v, %t, %v), want existing job", replayed, created, err)
	}
	if _, _, err := store.CreateDeleteJob(context.Background(), secondPlan, "same-key"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same key with different plan error = %v, want ErrIdempotencyConflict", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM delete_jobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("delete job count = %d, want 1", count)
	}
}

func TestClaimDeleteJobAllowsOnlyOneExecutor(t *testing.T) {
	store, record := seedDeleteJobStore(t)
	saved := savedDeleteJobPlan(record, "delete-plan", false)
	persistDeletePlanFixture(t, store, saved)
	job, _, err := store.CreateDeleteJob(context.Background(), saved, "claim-key")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDeleteJob(context.Background(), job.ID)
	if err != nil || !claimed {
		t.Fatalf("first claim = (%t, %v)", claimed, err)
	}
	claimed, err = store.ClaimDeleteJob(context.Background(), job.ID)
	if err != nil || claimed {
		t.Fatalf("second claim = (%t, %v), want false", claimed, err)
	}
}

func TestDeletePlanDownloaderIDsIncludesEverySharedStorageObserver(t *testing.T) {
	store, record := seedDeleteJobStore(t)
	shared, err := store.CreateDownloader(context.Background(), CreateDownloaderParams{
		Name: "Shared qB", Kind: "qbittorrent", BaseURL: "http://shared:8080",
		StorageID: record.StorageID, StorageName: "media", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := store.CreateDownloader(context.Background(), CreateDownloaderParams{
		Name: "Unrelated qB", Kind: "qbittorrent", BaseURL: "http://unrelated:8080",
		StorageName: "other", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = unrelated

	withDataDelete := savedDeleteJobPlan(record, "delete-data", true)
	ids, err := store.DeletePlanDownloaderIDs(context.Background(), withDataDelete.Plan)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{record.DownloaderID, shared.ID}
	sort.Strings(want)
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("refresh downloader IDs = %v, want %v", ids, want)
	}

	taskOnly := savedDeleteJobPlan(record, "task-only", false)
	ids, err = store.DeletePlanDownloaderIDs(context.Background(), taskOnly.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{record.DownloaderID}) {
		t.Fatalf("task-only refresh downloader IDs = %v", ids)
	}
}

func TestReconcileInterruptedDeleteJobsUsesConservativeTerminalStates(t *testing.T) {
	store, record := seedDeleteJobStore(t)
	makeJob := func(planID, key string) DeleteJob {
		saved := savedDeleteJobPlan(record, planID, false)
		persistDeletePlanFixture(t, store, saved)
		job, _, err := store.CreateDeleteJob(context.Background(), saved, key)
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	pending := makeJob("pending-plan", "pending-key")
	executing := makeJob("executing-plan", "executing-key")
	verifying := makeJob("verifying-plan", "verifying-key")
	if claimed, err := store.ClaimDeleteJob(context.Background(), executing.ID); err != nil || !claimed {
		t.Fatalf("claim executing job = (%t, %v)", claimed, err)
	}
	executing, _ = store.GetDeleteJob(context.Background(), executing.ID)
	if err := store.UpdateDeleteStepStatus(context.Background(), executing.Steps[0].ID, "executing", ""); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimDeleteJob(context.Background(), verifying.ID); err != nil || !claimed {
		t.Fatalf("claim verifying job = (%t, %v)", claimed, err)
	}
	verifying, _ = store.GetDeleteJob(context.Background(), verifying.ID)
	if err := store.UpdateDeleteStepStatus(context.Background(), verifying.Steps[0].ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateDeleteJobStatus(context.Background(), verifying.ID, "verifying", "", false); err != nil {
		t.Fatal(err)
	}

	failed, uncertain, err := store.ReconcileInterruptedDeleteJobs(context.Background())
	if err != nil || failed != 1 || uncertain != 2 {
		t.Fatalf("ReconcileInterruptedDeleteJobs() = (%d, %d, %v)", failed, uncertain, err)
	}
	pending, _ = store.GetDeleteJob(context.Background(), pending.ID)
	executing, _ = store.GetDeleteJob(context.Background(), executing.ID)
	verifying, _ = store.GetDeleteJob(context.Background(), verifying.ID)
	if pending.Status != "failed" || pending.CompletedAt == nil {
		t.Fatalf("pending job after reconciliation: %+v", pending)
	}
	if executing.Status != "uncertain" || executing.Steps[0].Status != "uncertain" {
		t.Fatalf("executing job after reconciliation: %+v", executing)
	}
	if verifying.Status != "uncertain" || verifying.Steps[0].Status != "completed" {
		t.Fatalf("verifying job after reconciliation: %+v", verifying)
	}
}
