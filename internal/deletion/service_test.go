package deletion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lesir831/SeedGraph/internal/domain"
	"github.com/lesir831/SeedGraph/internal/downloader"
	"github.com/lesir831/SeedGraph/internal/store"
)

type deleteCall struct {
	downloaderID string
	stableHash   string
	deleteData   bool
}

type syncCall struct {
	downloaderID string
	full         bool
}

type fakeDeleteCoordinator struct {
	store           *store.Store
	mu              sync.Mutex
	deletes         []deleteCall
	syncs           []syncCall
	deleteErrorAt   int
	deleteErr       error
	fullSyncErr     error
	tombstoneOnFull bool
	deleteStarted   chan struct{}
	deleteRelease   chan struct{}
}

func (f *fakeDeleteCoordinator) SyncNow(ctx context.Context, downloaderID string, full bool) (store.ApplySyncResult, error) {
	f.mu.Lock()
	f.syncs = append(f.syncs, syncCall{downloaderID: downloaderID, full: full})
	fullSyncErr := f.fullSyncErr
	tombstone := full && f.tombstoneOnFull
	f.mu.Unlock()
	if full && fullSyncErr != nil {
		return store.ApplySyncResult{}, fullSyncErr
	}
	if tombstone {
		return f.store.ApplySync(ctx, store.ApplySyncParams{
			DownloaderID: downloaderID, Mode: "full", Complete: true,
		})
	}
	return store.ApplySyncResult{}, nil
}

func (f *fakeDeleteCoordinator) DeleteRemote(
	ctx context.Context,
	downloaderID string,
	torrent downloader.TorrentRef,
	deleteData bool,
) error {
	f.mu.Lock()
	f.deletes = append(f.deletes, deleteCall{
		downloaderID: downloaderID, stableHash: torrent.StableHash, deleteData: deleteData,
	})
	started, release := f.deleteStarted, f.deleteRelease
	var deleteErr error
	if f.deleteErr != nil && len(f.deletes) == f.deleteErrorAt {
		deleteErr = f.deleteErr
	}
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return deleteErr
}

func (f *fakeDeleteCoordinator) resetCalls() {
	f.mu.Lock()
	f.deletes = nil
	f.syncs = nil
	f.mu.Unlock()
}

func newDeleteExecutionFixture(t *testing.T, count int) (*store.Store, store.SavedDeletePlan, store.DeleteJob) {
	t.Helper()
	database, err := store.Open(context.Background(), t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	downloaderRecord, err := database.CreateDownloader(context.Background(), store.CreateDownloaderParams{
		Name: "Transmission", Kind: "transmission", BaseURL: "http://tr:9091",
		StorageName: "media", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := domain.SelectedFileSizeFingerprint([]int64{100})
	if err != nil {
		t.Fatal(err)
	}
	records := make([]store.TorrentRecord, 0, count)
	instanceIDs := make([]string, 0, count)
	for index := 0; index < count; index++ {
		suffix := fmt.Sprintf("%02d", index)
		record := store.TorrentRecord{
			ID: "instance-" + suffix, DownloaderID: downloaderRecord.ID,
			StableHashKey: "hash-" + suffix, RemoteID: suffix, Name: "Example",
			SourcePath: "/downloads/Example", CanonicalPath: "/media/Example",
			StorageID: downloaderRecord.StorageID, WantedBytes: 100,
			ManifestFingerprint: fingerprint, SelectedFileCount: 1,
			MetadataFingerprint: "metadata-" + suffix,
			ContentGroupID:      "content-group", ContentGroupAutoKey: "content-key",
			DataGroupID: "data-group", DataGroupAutoKey: "data-key", Confidence: "verified",
			Runtime: store.RuntimeRecord{Status: "seeding", Progress: 1, RuntimeFingerprint: "runtime-" + suffix},
		}
		records = append(records, record)
		instanceIDs = append(instanceIDs, record.ID)
	}
	if _, err := database.ApplySync(context.Background(), store.ApplySyncParams{
		DownloaderID: downloaderRecord.ID, Mode: "full", Complete: true, Torrents: records,
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := database.PrepareDeletePlan(
		context.Background(), instanceIDs, time.Now().Add(-5*time.Minute), 5*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Plan.Executable {
		t.Fatalf("fixture delete plan blocked: %+v", saved.Plan.Blockers)
	}
	job, created, err := database.CreateDeleteJob(context.Background(), saved, "fixture-key")
	if err != nil || !created {
		t.Fatalf("CreateDeleteJob() = (%+v, %t, %v)", job, created, err)
	}
	return database, saved, job
}

func TestExecuteRunsTaskOnlyStepsBeforeSingleDataDeleteAndVerifies(t *testing.T) {
	database, saved, job := newDeleteExecutionFixture(t, 2)
	coordinator := &fakeDeleteCoordinator{store: database, tombstoneOnFull: true}
	service := New(database, coordinator, nil, 5*time.Minute)

	service.execute(context.Background(), job.ID, saved)
	completed, err := database.GetDeleteJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" || completed.CompletedAt == nil {
		t.Fatalf("completed job = %+v", completed)
	}
	if len(coordinator.deletes) != 2 || coordinator.deletes[0].deleteData || !coordinator.deletes[1].deleteData {
		t.Fatalf("remote delete ordering = %+v", coordinator.deletes)
	}
	for _, step := range completed.Steps {
		if step.Status != "completed" {
			t.Fatalf("step not completed: %+v", step)
		}
	}
	if len(coordinator.syncs) != 1 || !coordinator.syncs[0].full {
		t.Fatalf("verification syncs = %+v", coordinator.syncs)
	}

	// A duplicate executor cannot claim the terminal job and therefore cannot
	// replay either the task-only deletion or the physical-data deletion.
	service.execute(context.Background(), job.ID, saved)
	if len(coordinator.deletes) != 2 {
		t.Fatalf("duplicate execution replayed remote deletes: %+v", coordinator.deletes)
	}
}

func TestExecuteMarksRemoteTimeoutUncertainWithoutContinuing(t *testing.T) {
	database, saved, job := newDeleteExecutionFixture(t, 2)
	coordinator := &fakeDeleteCoordinator{
		store: database, deleteErrorAt: 1, deleteErr: context.DeadlineExceeded,
	}
	service := New(database, coordinator, nil, 5*time.Minute)

	service.execute(context.Background(), job.ID, saved)
	uncertain, err := database.GetDeleteJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if uncertain.Status != "uncertain" || uncertain.CompletedAt == nil || uncertain.Error == "" {
		t.Fatalf("job after timeout = %+v", uncertain)
	}
	if len(coordinator.deletes) != 1 || len(coordinator.syncs) != 0 {
		t.Fatalf("executor continued after ambiguous timeout: deletes=%+v syncs=%+v", coordinator.deletes, coordinator.syncs)
	}
	if uncertain.Steps[0].Status != "uncertain" || uncertain.Steps[1].Status != "pending" {
		t.Fatalf("step states after timeout = %+v", uncertain.Steps)
	}
}

func TestExecuteMarksMissingPostDeleteEvidenceUncertain(t *testing.T) {
	database, saved, job := newDeleteExecutionFixture(t, 1)
	coordinator := &fakeDeleteCoordinator{store: database, tombstoneOnFull: false}
	service := New(database, coordinator, nil, 5*time.Minute)

	service.execute(context.Background(), job.ID, saved)
	uncertain, err := database.GetDeleteJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if uncertain.Status != "uncertain" || uncertain.Steps[0].Status != "completed" {
		t.Fatalf("job without post-delete evidence = %+v", uncertain)
	}
	if len(coordinator.syncs) != 1 || !coordinator.syncs[0].full {
		t.Fatalf("post-delete full sync was not attempted: %+v", coordinator.syncs)
	}
}

func TestCreateJobIdempotentReplayReturnsTerminalJobBeforeRevalidation(t *testing.T) {
	database, saved, job := newDeleteExecutionFixture(t, 1)
	coordinator := &fakeDeleteCoordinator{store: database, tombstoneOnFull: true}
	service := New(database, coordinator, nil, 5*time.Minute)
	service.execute(context.Background(), job.ID, saved)
	coordinator.resetCalls()

	// The original torrent is now tombstoned, so revalidation would reject the
	// old plan. An idempotent retry must still return the original terminal job.
	replayed, err := service.CreateJob(context.Background(), saved.Plan.ID, "fixture-key")
	if err != nil || replayed.ID != job.ID || replayed.Status != "completed" {
		t.Fatalf("idempotent replay = (%+v, %v)", replayed, err)
	}
	if len(coordinator.syncs) != 0 || len(coordinator.deletes) != 0 {
		t.Fatalf("idempotent replay performed work: syncs=%+v deletes=%+v", coordinator.syncs, coordinator.deletes)
	}
	if _, err := service.CreateJob(context.Background(), "different-plan", "fixture-key"); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("same key with a different plan error = %v, want ErrPlanChanged", err)
	}
}

func TestWaitJoinsAcceptedDeleteJob(t *testing.T) {
	database, saved, _ := newDeleteExecutionFixture(t, 1)
	coordinator := &fakeDeleteCoordinator{
		store: database, tombstoneOnFull: true,
		deleteStarted: make(chan struct{}), deleteRelease: make(chan struct{}),
	}
	service := New(database, coordinator, nil, 5*time.Minute)
	job, err := service.CreateJob(context.Background(), saved.Plan.ID, "async-key")
	if err != nil {
		t.Fatal(err)
	}
	<-coordinator.deleteStarted
	waited := make(chan struct{})
	go func() {
		service.Wait()
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("Wait returned while the delete job was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(coordinator.deleteRelease)
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after the delete job completed")
	}
	completed, err := database.GetDeleteJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" {
		t.Fatalf("delete job status after Wait = %q", completed.Status)
	}
}
