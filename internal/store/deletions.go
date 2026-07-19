package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lesir831/SeedGraph/internal/domain"
)

type SavedDeletePlan struct {
	Plan      domain.DeletePlan    `json:"plan"`
	Request   domain.DeleteRequest `json:"request"`
	CreatedAt time.Time            `json:"created_at"`
	ExpiresAt time.Time            `json:"expires_at"`
}

func (s *Store) PrepareDeletePlan(ctx context.Context, instanceIDs []string, staleBefore time.Time, ttl time.Duration) (SavedDeletePlan, error) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if staleBefore.IsZero() {
		staleBefore = s.now().Add(-5 * time.Minute)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SavedDeletePlan{}, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := buildDeletionSnapshotTx(ctx, tx, staleBefore)
	if err != nil {
		return SavedDeletePlan{}, err
	}
	request := domain.DeleteRequest{
		InstanceIDs:                  instanceIDs,
		ExpectedContentGroupVersions: make(map[string]uint64),
		ExpectedDataGroupVersions:    make(map[string]uint64),
		RequireExpectedVersions:      true,
		RequireFreshStorageSnapshot:  true,
	}
	selected := make(map[string]struct{}, len(instanceIDs))
	for _, id := range instanceIDs {
		selected[id] = struct{}{}
	}
	contentVersions := make(map[string]uint64, len(snapshot.ContentGroups))
	for _, group := range snapshot.ContentGroups {
		contentVersions[group.ID] = group.Version
	}
	dataVersions := make(map[string]uint64, len(snapshot.DataGroups))
	for _, group := range snapshot.DataGroups {
		dataVersions[group.ID] = group.Version
	}
	for _, instance := range snapshot.Instances {
		if _, ok := selected[instance.ID]; !ok {
			continue
		}
		if version, ok := contentVersions[instance.ContentGroupID]; ok {
			request.ExpectedContentGroupVersions[instance.ContentGroupID] = version
		}
		if version, ok := dataVersions[instance.DataGroupID]; ok {
			request.ExpectedDataGroupVersions[instance.DataGroupID] = version
		}
	}
	plan := domain.PlanDeletion(request, snapshot)
	if err := tx.Commit(); err != nil {
		return SavedDeletePlan{}, err
	}
	createdAt := s.now().UTC()
	saved := SavedDeletePlan{Plan: plan, Request: request, CreatedAt: createdAt, ExpiresAt: createdAt.Add(ttl)}
	requestJSON, _ := json.Marshal(request)
	planJSON, _ := json.Marshal(plan)
	if err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
            INSERT INTO delete_plans(id, selection_json, snapshot_json, blocked, created_at, expires_at)
            VALUES(?, ?, ?, ?, ?, ?)
            ON CONFLICT(id) DO UPDATE SET
                selection_json = excluded.selection_json,
                snapshot_json = excluded.snapshot_json,
                blocked = excluded.blocked,
                created_at = excluded.created_at,
                expires_at = excluded.expires_at`,
			plan.ID, string(requestJSON), string(planJSON), boolInt(!plan.Executable), createdAt.Unix(), saved.ExpiresAt.Unix())
		return err
	}); err != nil {
		return SavedDeletePlan{}, fmt.Errorf("persist delete plan: %w", err)
	}
	return saved, nil
}

func (s *Store) GetDeletePlan(ctx context.Context, id string) (SavedDeletePlan, error) {
	var requestJSON, planJSON string
	var createdAt, expiresAt int64
	err := s.db.QueryRowContext(ctx, `
        SELECT selection_json, snapshot_json, created_at, expires_at FROM delete_plans WHERE id = ?`, id).
		Scan(&requestJSON, &planJSON, &createdAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedDeletePlan{}, ErrNotFound
	}
	if err != nil {
		return SavedDeletePlan{}, err
	}
	var saved SavedDeletePlan
	if err := json.Unmarshal([]byte(requestJSON), &saved.Request); err != nil {
		return SavedDeletePlan{}, fmt.Errorf("decode saved delete request: %w", err)
	}
	if err := json.Unmarshal([]byte(planJSON), &saved.Plan); err != nil {
		return SavedDeletePlan{}, fmt.Errorf("decode saved delete plan: %w", err)
	}
	saved.CreatedAt = time.Unix(createdAt, 0).UTC()
	saved.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	return saved, nil
}

func (s *Store) RevalidateDeletePlan(ctx context.Context, saved SavedDeletePlan, staleBefore time.Time) (domain.DeletePlan, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.DeletePlan{}, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := buildDeletionSnapshotTx(ctx, tx, staleBefore)
	if err != nil {
		return domain.DeletePlan{}, err
	}
	plan := domain.PlanDeletion(saved.Request, snapshot)
	if err := tx.Commit(); err != nil {
		return domain.DeletePlan{}, err
	}
	return plan, nil
}

func buildDeletionSnapshotTx(ctx context.Context, tx *sql.Tx, staleBefore time.Time) (domain.DeletionSnapshot, error) {
	var snapshot domain.DeletionSnapshot
	rows, err := tx.QueryContext(ctx, `
		SELECT ti.id, ti.downloader_id, d.name, ti.stable_hash_key, ti.remote_id, ti.name,
               ti.storage_id, ti.source_path, ti.canonical_path, ti.wanted_bytes,
		       ti.selected_file_count, ti.manifest_fingerprint, ti.file_manifest_known, ti.content_group_id,
               ti.data_group_id, ti.assignment_source, d.online,
               CASE WHEN COALESCE(d.last_success_at, 0) < ? THEN 1 ELSE 0 END,
               ti.metadata_fingerprint, COALESCE(tr.runtime_fingerprint, ''), ti.last_seen_at, ti.deleted_at
        FROM torrent_instances ti
        JOIN downloaders d ON d.id = ti.downloader_id
        LEFT JOIN torrent_runtime tr ON tr.instance_id = ti.id`, staleBefore.Unix())
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var instance domain.TorrentInstance
		var assignment string
		var online, stale, fileManifestKnown int
		var lastSeen int64
		var deleted sql.NullInt64
		if err := rows.Scan(
			&instance.ID, &instance.DownloaderID, &instance.DownloaderName, &instance.ExternalKey, &instance.RemoteID, &instance.Name,
			&instance.StorageID, &instance.ContentPath, &instance.CanonicalPath, &instance.WantedBytes,
			&instance.SelectedFileCount, &instance.FileSizeFingerprint, &fileManifestKnown, &instance.ContentGroupID,
			&instance.DataGroupID, &assignment, &online, &stale, &instance.MetadataFingerprint,
			&instance.RuntimeFingerprint, &lastSeen, &deleted,
		); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		instance.AssignmentSource = domain.AssignmentSource(assignment)
		instance.SelectedFilesKnown = instance.FileSizeFingerprint != ""
		instance.FileManifestKnown = fileManifestKnown != 0
		instance.DownloaderOnline = online != 0
		instance.Stale = stale != 0
		instance.LastSeenAt = time.Unix(lastSeen, 0).UTC()
		if deleted.Valid {
			value := time.Unix(deleted.Int64, 0).UTC()
			instance.DeletedAt = &value
		}
		snapshot.Instances = append(snapshot.Instances, instance)
	}
	if err := rows.Close(); err != nil {
		return snapshot, err
	}
	instanceIndexes := make(map[string]int, len(snapshot.Instances))
	for index := range snapshot.Instances {
		instanceIndexes[snapshot.Instances[index].ID] = index
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT instance_id, source_path, canonical_path, size, selected
		FROM torrent_files
		ORDER BY instance_id, canonical_path`)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var instanceID string
		var file domain.TorrentFile
		var selected int
		if err := rows.Scan(&instanceID, &file.SourcePath, &file.CanonicalPath, &file.Size, &selected); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		index, exists := instanceIndexes[instanceID]
		if !exists {
			_ = rows.Close()
			return snapshot, fmt.Errorf("torrent file references unknown instance %q", instanceID)
		}
		file.Selected = selected != 0
		snapshot.Instances[index].Files = append(snapshot.Instances[index].Files, file)
	}
	if err := rows.Close(); err != nil {
		return snapshot, err
	}

	contentByID := make(map[string]*domain.ContentGroup)
	rows, err = tx.QueryContext(ctx, `
        SELECT id, version, mode, locked, confidence, auto_key
        FROM content_groups WHERE deleted_at IS NULL`)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var group domain.ContentGroup
		var locked int
		if err := rows.Scan(&group.ID, &group.Version, &group.Mode, &locked, &group.Confidence, &group.AutoKey); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		group.Locked = locked != 0
		copy := group
		contentByID[group.ID] = &copy
	}
	_ = rows.Close()

	dataByID := make(map[string]*domain.DataGroup)
	rows, err = tx.QueryContext(ctx, `
        SELECT id, version, storage_id, canonical_path, wanted_bytes, selected_file_count,
               manifest_fingerprint, confidence, auto_key
        FROM data_groups`)
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var group domain.DataGroup
		if err := rows.Scan(&group.ID, &group.Version, &group.StorageID, &group.CanonicalPath,
			&group.WantedBytes, &group.SelectedFileCount, &group.FileSizeFingerprint,
			&group.Confidence, &group.PhysicalKey); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		copy := group
		dataByID[group.ID] = &copy
	}
	_ = rows.Close()

	for _, instance := range snapshot.Instances {
		if !instance.Active() {
			continue
		}
		if group := contentByID[instance.ContentGroupID]; group != nil {
			group.MemberIDs = append(group.MemberIDs, instance.ID)
			group.TaskCount++
			if instance.WantedBytes > group.LogicalBytes {
				group.LogicalBytes = instance.WantedBytes
			}
		}
		if group := dataByID[instance.DataGroupID]; group != nil {
			group.MemberIDs = append(group.MemberIDs, instance.ID)
		}
	}
	for _, group := range contentByID {
		seenDataGroups := make(map[string]struct{})
		for _, instance := range snapshot.Instances {
			if instance.Active() && instance.ContentGroupID == group.ID {
				seenDataGroups[instance.DataGroupID] = struct{}{}
			}
		}
		group.DataGroupCount = len(seenDataGroups)
		snapshot.ContentGroups = append(snapshot.ContentGroups, *group)
	}
	for _, group := range dataByID {
		snapshot.DataGroups = append(snapshot.DataGroups, *group)
	}

	rows, err = tx.QueryContext(ctx, `
        SELECT id, storage_id, online,
               CASE WHEN COALESCE(last_success_at, 0) < ? THEN 1 ELSE 0 END
        FROM downloaders WHERE enabled = 1`, staleBefore.Unix())
	if err != nil {
		return snapshot, err
	}
	for rows.Next() {
		var state domain.DownloaderState
		var storageID string
		var online, stale int
		if err := rows.Scan(&state.ID, &storageID, &online, &stale); err != nil {
			_ = rows.Close()
			return snapshot, err
		}
		state.StorageIDs = []string{storageID}
		state.Online = online != 0
		state.Stale = stale != 0
		snapshot.DownloaderStates = append(snapshot.DownloaderStates, state)
	}
	return snapshot, rows.Close()
}

type DeleteJob struct {
	ID             string          `json:"id"`
	PlanID         string          `json:"plan_id"`
	IdempotencyKey string          `json:"-"`
	Status         string          `json:"status"`
	Error          string          `json:"error"`
	Steps          []DeleteJobStep `json:"steps"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	CompletedAt    *time.Time      `json:"completed_at"`
}

type DeleteJobStep struct {
	ID           string    `json:"id"`
	Position     int       `json:"position"`
	InstanceID   string    `json:"instance_id"`
	DownloaderID string    `json:"downloader_id"`
	DeleteData   bool      `json:"delete_data"`
	Status       string    `json:"status"`
	Error        string    `json:"error"`
	UpdatedAt    time.Time `json:"updated_at"`
}

var ErrIdempotencyConflict = errors.New("idempotency key was already used for a different delete plan")

func (s *Store) CreateDeleteJob(ctx context.Context, saved SavedDeletePlan, idempotencyKey string) (DeleteJob, bool, error) {
	if strings.TrimSpace(idempotencyKey) == "" {
		return DeleteJob{}, false, errors.New("idempotency key is required")
	}
	if existing, err := s.GetDeleteJobByIdempotencyKey(ctx, idempotencyKey); err == nil {
		if existing.PlanID != saved.Plan.ID {
			return DeleteJob{}, false, ErrIdempotencyConflict
		}
		return existing, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return DeleteJob{}, false, err
	}
	if !saved.Plan.Executable {
		return DeleteJob{}, false, errors.New("delete plan is blocked")
	}
	if !saved.ExpiresAt.After(s.now()) {
		return DeleteJob{}, false, errors.New("delete plan has expired")
	}
	jobID := uuid.NewString()
	now := s.now().Unix()
	created := false
	err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		var existingID, existingPlanID string
		err := tx.QueryRowContext(ctx, `
			SELECT id, plan_id FROM delete_jobs WHERE idempotency_key = ?`, idempotencyKey).
			Scan(&existingID, &existingPlanID)
		if err == nil {
			if existingPlanID != saved.Plan.ID {
				return ErrIdempotencyConflict
			}
			jobID = existingID
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO delete_jobs(id, plan_id, idempotency_key, status, created_at, updated_at)
            VALUES(?, ?, ?, 'pending', ?, ?)`, jobID, saved.Plan.ID, idempotencyKey, now, now); err != nil {
			return err
		}
		for _, step := range saved.Plan.Steps {
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO delete_job_steps(
                    id, job_id, position, instance_id, downloader_id, delete_data, status, updated_at
                ) VALUES(?, ?, ?, ?, ?, ?, 'pending', ?)`, uuid.NewString(), jobID, step.Order,
				step.InstanceID, step.DownloaderID, boolInt(step.DeleteData), now); err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	if err != nil {
		return DeleteJob{}, false, err
	}
	job, err := s.GetDeleteJob(ctx, jobID)
	return job, created, err
}

func (s *Store) GetDeleteJobByIdempotencyKey(ctx context.Context, idempotencyKey string) (DeleteJob, error) {
	var jobID string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM delete_jobs WHERE idempotency_key = ?`, idempotencyKey).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteJob{}, ErrNotFound
	}
	if err != nil {
		return DeleteJob{}, err
	}
	return s.GetDeleteJob(ctx, jobID)
}

// DeletePlanDownloaderIDs returns every downloader that must participate in
// the revalidation barrier. Selected task owners are always included. When a
// step can delete physical data, every enabled downloader that observes the
// same storage is included so a recent conflicting reference cannot be missed.
func (s *Store) DeletePlanDownloaderIDs(ctx context.Context, plan domain.DeletePlan) ([]string, error) {
	ids := make(map[string]struct{})
	dataGroupIDs := make(map[string]struct{})
	for _, step := range plan.Steps {
		if step.DownloaderID != "" {
			ids[step.DownloaderID] = struct{}{}
		}
		if step.DeleteData && step.DataGroupID != "" {
			dataGroupIDs[step.DataGroupID] = struct{}{}
		}
	}
	if len(dataGroupIDs) > 0 {
		groupIDs := make([]string, 0, len(dataGroupIDs))
		for groupID := range dataGroupIDs {
			groupIDs = append(groupIDs, groupID)
		}
		sort.Strings(groupIDs)
		placeholders := strings.TrimRight(strings.Repeat("?,", len(groupIDs)), ",")
		args := make([]any, 0, len(groupIDs))
		for _, groupID := range groupIDs {
			args = append(args, groupID)
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT DISTINCT d.id
			FROM downloaders d
			JOIN data_groups dg ON dg.storage_id = d.storage_id
			WHERE d.enabled = 1 AND dg.id IN (`+placeholders+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var downloaderID string
			if err := rows.Scan(&downloaderID); err != nil {
				_ = rows.Close()
				return nil, err
			}
			ids[downloaderID] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

// ReconcileInterruptedDeleteJobs converts states left behind by a process
// interruption into explicit terminal outcomes. Pending jobs are known not to
// have started and fail safely; executing/verifying jobs are ambiguous and
// therefore become uncertain without retrying any remote mutation.
func (s *Store) ReconcileInterruptedDeleteJobs(ctx context.Context) (failed, uncertain int64, err error) {
	now := s.now().Unix()
	const interruptedMessage = "SeedGraph restarted before the delete job completed"
	err = s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		result, execErr := tx.ExecContext(ctx, `
			UPDATE delete_job_steps
			SET status = 'uncertain', error = ?, updated_at = ?
			WHERE status = 'executing' AND job_id IN (
				SELECT id FROM delete_jobs WHERE status IN ('executing', 'verifying')
			)`, interruptedMessage, now)
		if execErr != nil {
			return execErr
		}
		if _, execErr = result.RowsAffected(); execErr != nil {
			return execErr
		}
		result, execErr = tx.ExecContext(ctx, `
			UPDATE delete_jobs
			SET status = 'uncertain', error = ?, updated_at = ?, completed_at = ?
			WHERE status IN ('executing', 'verifying')`, interruptedMessage, now, now)
		if execErr != nil {
			return execErr
		}
		uncertain, execErr = result.RowsAffected()
		if execErr != nil {
			return execErr
		}
		result, execErr = tx.ExecContext(ctx, `
			UPDATE delete_jobs
			SET status = 'failed', error = ?, updated_at = ?, completed_at = ?
			WHERE status = 'pending'`, interruptedMessage, now, now)
		if execErr != nil {
			return execErr
		}
		failed, execErr = result.RowsAffected()
		return execErr
	})
	return failed, uncertain, err
}

func (s *Store) GetDeleteJob(ctx context.Context, id string) (DeleteJob, error) {
	var job DeleteJob
	var createdAt, updatedAt int64
	var completed sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
        SELECT id, plan_id, idempotency_key, status, error, created_at, updated_at, completed_at
        FROM delete_jobs WHERE id = ?`, id).Scan(&job.ID, &job.PlanID, &job.IdempotencyKey,
		&job.Status, &job.Error, &createdAt, &updatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteJob{}, ErrNotFound
	}
	if err != nil {
		return DeleteJob{}, err
	}
	job.CreatedAt = time.Unix(createdAt, 0).UTC()
	job.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	job.CompletedAt = scanNullableTime(completed)
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, position, instance_id, downloader_id, delete_data, status, error, updated_at
        FROM delete_job_steps WHERE job_id = ? ORDER BY position`, id)
	if err != nil {
		return DeleteJob{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var step DeleteJobStep
		var deleteData int
		var stepUpdated int64
		if err := rows.Scan(&step.ID, &step.Position, &step.InstanceID, &step.DownloaderID,
			&deleteData, &step.Status, &step.Error, &stepUpdated); err != nil {
			return DeleteJob{}, err
		}
		step.DeleteData = deleteData != 0
		step.UpdatedAt = time.Unix(stepUpdated, 0).UTC()
		job.Steps = append(job.Steps, step)
	}
	return job, rows.Err()
}

func (s *Store) UpdateDeleteJobStatus(ctx context.Context, id, status, message string, terminal bool) error {
	now := s.now().Unix()
	var completed any
	if terminal {
		completed = now
	}
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
            UPDATE delete_jobs SET status = ?, error = ?, updated_at = ?, completed_at = ? WHERE id = ?`,
			status, message, now, completed, id)
		if err != nil {
			return err
		}
		return requireAffected(result)
	})
}

// ClaimDeleteJob atomically transitions a newly-created job to executing.
// Exactly one executor may observe claimed=true, which prevents duplicate
// goroutines or idempotent retries from replaying remote deletions.
func (s *Store) ClaimDeleteJob(ctx context.Context, id string) (bool, error) {
	claimed := false
	err := s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE delete_jobs SET status = 'executing', error = '', updated_at = ?
			WHERE id = ? AND status = 'pending'`, s.now().Unix(), id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		claimed = count == 1
		return nil
	})
	return claimed, err
}

func (s *Store) UpdateDeleteStepStatus(ctx context.Context, id, status, message string) error {
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
            UPDATE delete_job_steps SET status = ?, error = ?, updated_at = ? WHERE id = ?`,
			status, message, s.now().Unix(), id)
		if err != nil {
			return err
		}
		return requireAffected(result)
	})
}

func (s *Store) TorrentRef(ctx context.Context, instanceID string) (downloaderID, stableHash, remoteID string, err error) {
	err = s.db.QueryRowContext(ctx, `
        SELECT downloader_id, stable_hash_key, remote_id FROM torrent_instances WHERE id = ?`, instanceID).
		Scan(&downloaderID, &stableHash, &remoteID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func (s *Store) InstancesDeleted(ctx context.Context, instanceIDs []string) (bool, error) {
	if len(instanceIDs) == 0 {
		return false, nil
	}
	for _, id := range instanceIDs {
		var deleted sql.NullInt64
		if err := s.db.QueryRowContext(ctx, "SELECT deleted_at FROM torrent_instances WHERE id = ?", id).Scan(&deleted); err != nil {
			return false, err
		}
		if !deleted.Valid {
			return false, nil
		}
	}
	return true, nil
}
