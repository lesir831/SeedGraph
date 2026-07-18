package deletion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lesir831/SeedGraph/internal/domain"
	"github.com/lesir831/SeedGraph/internal/downloader"
	"github.com/lesir831/SeedGraph/internal/store"
)

type Coordinator interface {
	SyncNow(context.Context, string, bool) (store.ApplySyncResult, error)
	DeleteRemote(context.Context, string, downloader.TorrentRef, bool) error
}

type Service struct {
	store       *store.Store
	coordinator Coordinator
	logger      *slog.Logger
	staleAfter  time.Duration
	planTTL     time.Duration

	mu      sync.Mutex
	rootCtx context.Context
	workers sync.WaitGroup
}

func New(store *store.Store, coordinator Coordinator, logger *slog.Logger, staleAfter time.Duration) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store: store, coordinator: coordinator, logger: logger,
		staleAfter: staleAfter, planTTL: 5 * time.Minute,
	}
}

func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	s.rootCtx = ctx
	s.mu.Unlock()
	failed, uncertain, err := s.store.ReconcileInterruptedDeleteJobs(ctx)
	if err != nil {
		s.logger.Error("reconcile interrupted delete jobs", "error", err)
		return
	}
	if failed != 0 || uncertain != 0 {
		s.logger.Warn("reconciled interrupted delete jobs", "failed", failed, "uncertain", uncertain)
	}
}

func (s *Service) CreatePlan(ctx context.Context, instanceIDs []string) (store.SavedDeletePlan, error) {
	return s.store.PrepareDeletePlan(ctx, instanceIDs, time.Now().Add(-s.staleAfter), s.planTTL)
}

var (
	ErrPlanExpired = errors.New("delete plan has expired")
	ErrPlanChanged = errors.New("delete plan no longer matches current state")
	ErrPlanBlocked = errors.New("delete plan is blocked")
)

// CreateJob refreshes all affected downloaders, re-plans against the current
// state, persists an idempotent job, and starts ordered execution.
func (s *Service) CreateJob(ctx context.Context, planID, idempotencyKey string) (store.DeleteJob, error) {
	if existing, err := s.store.GetDeleteJobByIdempotencyKey(ctx, idempotencyKey); err == nil {
		if existing.PlanID != planID {
			return store.DeleteJob{}, fmt.Errorf("%w: %v", ErrPlanChanged, store.ErrIdempotencyConflict)
		}
		return existing, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.DeleteJob{}, err
	}
	saved, err := s.store.GetDeletePlan(ctx, planID)
	if err != nil {
		return store.DeleteJob{}, err
	}
	if !saved.ExpiresAt.After(time.Now()) {
		return store.DeleteJob{}, ErrPlanExpired
	}
	if !saved.Plan.Executable {
		return store.DeleteJob{}, ErrPlanBlocked
	}
	if s.coordinator == nil {
		return store.DeleteJob{}, errors.New("delete coordinator is unavailable")
	}
	ids, err := s.store.DeletePlanDownloaderIDs(ctx, saved.Plan)
	if err != nil {
		return store.DeleteJob{}, fmt.Errorf("resolve delete-plan downloaders: %w", err)
	}
	for _, id := range ids {
		if _, err := s.coordinator.SyncNow(ctx, id, false); err != nil {
			return store.DeleteJob{}, fmt.Errorf("refresh downloader %s before deletion: %w", id, err)
		}
	}
	current, err := s.store.RevalidateDeletePlan(ctx, saved, time.Now().Add(-s.staleAfter))
	if err != nil {
		return store.DeleteJob{}, err
	}
	if !current.Executable || current.ID != saved.Plan.ID {
		return store.DeleteJob{}, ErrPlanChanged
	}
	job, created, err := s.store.CreateDeleteJob(ctx, saved, idempotencyKey)
	if err != nil {
		if errors.Is(err, store.ErrIdempotencyConflict) {
			return store.DeleteJob{}, fmt.Errorf("%w: %v", ErrPlanChanged, err)
		}
		return store.DeleteJob{}, err
	}
	if created {
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			s.execute(s.context(), job.ID, saved)
		}()
	}
	return job, nil
}

// Wait blocks until every accepted deletion job has persisted its terminal or
// uncertain state. Call it after the HTTP server has stopped accepting work.
func (s *Service) Wait() {
	s.workers.Wait()
}

func (s *Service) GetJob(ctx context.Context, id string) (store.DeleteJob, error) {
	return s.store.GetDeleteJob(ctx, id)
}

func (s *Service) execute(ctx context.Context, jobID string, saved store.SavedDeletePlan) {
	fail := func(status string, err error) {
		message := safeMessage(err)
		_ = s.store.UpdateDeleteJobStatus(context.Background(), jobID, status, message, true)
		s.logger.Warn("delete job stopped", "job_id", jobID, "status", status, "error", message)
	}
	claimed, err := s.store.ClaimDeleteJob(ctx, jobID)
	if err != nil {
		fail("failed", err)
		return
	}
	if !claimed {
		return
	}
	job, err := s.store.GetDeleteJob(ctx, jobID)
	if err != nil {
		fail("failed", err)
		return
	}
	if err := validatePersistedJob(job, saved); err != nil {
		fail("failed", err)
		return
	}
	if s.coordinator == nil {
		fail("failed", errors.New("delete coordinator is unavailable"))
		return
	}
	for _, step := range job.Steps {
		if err := s.store.UpdateDeleteStepStatus(ctx, step.ID, "executing", ""); err != nil {
			fail("failed", err)
			return
		}
		downloaderID, stableHash, remoteID, err := s.store.TorrentRef(ctx, step.InstanceID)
		if err != nil {
			_ = s.store.UpdateDeleteStepStatus(context.Background(), step.ID, "failed", safeMessage(err))
			fail("failed", err)
			return
		}
		if downloaderID != step.DownloaderID {
			err = fmt.Errorf("torrent instance %s moved to downloader %s", step.InstanceID, downloaderID)
			_ = s.store.UpdateDeleteStepStatus(context.Background(), step.ID, "failed", safeMessage(err))
			fail("failed", err)
			return
		}
		mutationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = s.coordinator.DeleteRemote(mutationCtx, downloaderID, downloader.TorrentRef{
			StableHash: stableHash, RemoteID: remoteID,
		}, step.DeleteData)
		cancel()
		if err != nil {
			// A failed HTTP response cannot prove that the downloader did not apply
			// the mutation. Never retry a potentially destructive step blindly.
			_ = s.store.UpdateDeleteStepStatus(context.Background(), step.ID, "uncertain", safeMessage(err))
			fail("uncertain", err)
			return
		}
		if err := s.store.UpdateDeleteStepStatus(ctx, step.ID, "completed", ""); err != nil {
			fail("uncertain", err)
			return
		}
	}
	if err := s.store.UpdateDeleteJobStatus(ctx, jobID, "verifying", "", false); err != nil {
		fail("uncertain", err)
		return
	}
	ids, err := s.store.DeletePlanDownloaderIDs(ctx, saved.Plan)
	if err != nil {
		fail("uncertain", fmt.Errorf("resolve verification downloaders: %w", err))
		return
	}
	for _, id := range ids {
		if _, err := s.coordinator.SyncNow(ctx, id, true); err != nil {
			fail("uncertain", fmt.Errorf("verify downloader %s: %w", id, err))
			return
		}
	}
	deleted, err := s.store.InstancesDeleted(ctx, saved.Plan.SelectedInstanceIDs)
	if err != nil || !deleted {
		if err == nil {
			err = errors.New("one or more torrent tasks are still present after deletion")
		}
		fail("uncertain", err)
		return
	}
	if err := s.store.UpdateDeleteJobStatus(ctx, jobID, "completed", "", true); err != nil {
		fail("uncertain", err)
		return
	}
	_ = s.store.AddAuditEvent(context.Background(), store.AuditEvent{
		Actor: "admin", Action: "delete.completed", TargetType: "delete_job", TargetID: jobID,
		Details: map[string]any{"instances": saved.Plan.SelectedInstanceIDs},
	})
}

func validatePersistedJob(job store.DeleteJob, saved store.SavedDeletePlan) error {
	if job.PlanID != saved.Plan.ID {
		return errors.New("persisted delete job references a different plan")
	}
	if len(job.Steps) != len(saved.Plan.Steps) {
		return errors.New("persisted delete job steps do not match its plan")
	}
	for index, step := range job.Steps {
		planned := saved.Plan.Steps[index]
		if step.Position != planned.Order || step.InstanceID != planned.InstanceID ||
			step.DownloaderID != planned.DownloaderID || step.DeleteData != planned.DeleteData {
			return fmt.Errorf("persisted delete step %d does not match its plan", index+1)
		}
	}
	return nil
}

func (s *Service) context() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rootCtx != nil {
		return s.rootCtx
	}
	return context.Background()
}

func safeMessage(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

// DomainPlan is exported for API documentation without coupling callers to
// store persistence details.
type DomainPlan = domain.DeletePlan
