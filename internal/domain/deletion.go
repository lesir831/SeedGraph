package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// DeleteBlockerCode is stable API data; clients should not parse Message.
type DeleteBlockerCode string

const (
	BlockerNoSelection              DeleteBlockerCode = "no_selection"
	BlockerUnknownInstance          DeleteBlockerCode = "unknown_instance"
	BlockerInactiveInstance         DeleteBlockerCode = "inactive_instance"
	BlockerDownloaderOffline        DeleteBlockerCode = "downloader_offline"
	BlockerStaleData                DeleteBlockerCode = "stale_data"
	BlockerMissingContentGroup      DeleteBlockerCode = "missing_content_group"
	BlockerMissingDataGroup         DeleteBlockerCode = "missing_data_group"
	BlockerMissingExpectedVersion   DeleteBlockerCode = "missing_expected_version"
	BlockerVersionConflict          DeleteBlockerCode = "version_conflict"
	BlockerUnverifiedDataGroup      DeleteBlockerCode = "unverified_data_group"
	BlockerConflictingPathOccupant  DeleteBlockerCode = "conflicting_path_occupant"
	BlockerStorageSnapshotMissing   DeleteBlockerCode = "storage_snapshot_missing"
	BlockerStorageDownloaderOffline DeleteBlockerCode = "storage_downloader_offline"
	BlockerStorageSnapshotStale     DeleteBlockerCode = "storage_snapshot_stale"
	BlockerInvalidSnapshot          DeleteBlockerCode = "invalid_snapshot"
)

// DeleteBlocker explains why a preview must not be executed.
type DeleteBlocker struct {
	Code            DeleteBlockerCode `json:"code"`
	Message         string            `json:"message"`
	InstanceID      string            `json:"instance_id,omitempty"`
	DownloaderID    string            `json:"downloader_id,omitempty"`
	ContentGroupID  string            `json:"content_group_id,omitempty"`
	DataGroupID     string            `json:"data_group_id,omitempty"`
	ExpectedVersion uint64            `json:"expected_version,omitempty"`
	ActualVersion   uint64            `json:"actual_version,omitempty"`
}

// DeleteStep is ordered. A caller must stop before a delete_data step if any
// earlier task-only step failed. At most one step per DataGroup deletes data.
type DeleteStep struct {
	Order          int    `json:"order"`
	InstanceID     string `json:"instance_id"`
	DownloaderID   string `json:"downloader_id"`
	ExternalKey    string `json:"external_key"`
	ContentGroupID string `json:"content_group_id"`
	DataGroupID    string `json:"data_group_id"`
	DeleteData     bool   `json:"delete_data"`
}

// DeleteRequest contains the optimistic-lock state captured by the impact
// preview. REST handlers should set both Require* flags for execution.
type DeleteRequest struct {
	InstanceIDs                  []string          `json:"instance_ids"`
	ExpectedContentGroupVersions map[string]uint64 `json:"expected_content_group_versions,omitempty"`
	ExpectedDataGroupVersions    map[string]uint64 `json:"expected_data_group_versions,omitempty"`
	RequireExpectedVersions      bool              `json:"require_expected_versions"`
	RequireFreshStorageSnapshot  bool              `json:"require_fresh_storage_snapshot"`
}

// DeletionSnapshot must be a single coherent database view. DownloaderStates
// lists every configured downloader and the physical storages it can access.
type DeletionSnapshot struct {
	Instances        []TorrentInstance `json:"instances"`
	ContentGroups    []ContentGroup    `json:"content_groups"`
	DataGroups       []DataGroup       `json:"data_groups"`
	DownloaderStates []DownloaderState `json:"downloader_states,omitempty"`
}

// DeletePlan is an immutable impact preview. Executable is false whenever a
// blocker exists; execution must still refresh and re-plan immediately before
// invoking a downloader.
type DeletePlan struct {
	ID                  string          `json:"id"`
	SelectedInstanceIDs []string        `json:"selected_instance_ids"`
	Executable          bool            `json:"executable"`
	Steps               []DeleteStep    `json:"steps"`
	Blockers            []DeleteBlocker `json:"blockers"`
}

// PlanDeletion applies physical DataGroup reference counting. Site counts and
// logical ContentGroups never decide whether local data is deleted.
func PlanDeletion(request DeleteRequest, snapshot DeletionSnapshot) DeletePlan {
	selectedIDs := uniqueSortedStrings(request.InstanceIDs)
	plan := DeletePlan{SelectedInstanceIDs: selectedIDs}
	blockerKeys := make(map[string]struct{})
	addBlocker := func(blocker DeleteBlocker) {
		key := strings.Join([]string{
			string(blocker.Code),
			blocker.InstanceID,
			blocker.DownloaderID,
			blocker.ContentGroupID,
			blocker.DataGroupID,
			strconv.FormatUint(blocker.ExpectedVersion, 10),
			strconv.FormatUint(blocker.ActualVersion, 10),
		}, "\x00")
		if _, exists := blockerKeys[key]; exists {
			return
		}
		blockerKeys[key] = struct{}{}
		plan.Blockers = append(plan.Blockers, blocker)
	}

	if len(selectedIDs) == 0 {
		addBlocker(DeleteBlocker{Code: BlockerNoSelection, Message: "at least one torrent instance must be selected"})
		plan.ID = deletionPlanID(plan, request)
		return plan
	}

	instances := make(map[string]TorrentInstance, len(snapshot.Instances))
	for _, instance := range snapshot.Instances {
		if instance.ID == "" {
			addBlocker(DeleteBlocker{Code: BlockerInvalidSnapshot, Message: "snapshot contains an instance without an id"})
			continue
		}
		if _, duplicate := instances[instance.ID]; duplicate {
			addBlocker(DeleteBlocker{
				Code:       BlockerInvalidSnapshot,
				Message:    "snapshot contains duplicate torrent instance ids",
				InstanceID: instance.ID,
			})
			continue
		}
		instances[instance.ID] = instance
	}
	contentGroups := make(map[string]ContentGroup, len(snapshot.ContentGroups))
	for _, group := range snapshot.ContentGroups {
		if _, duplicate := contentGroups[group.ID]; duplicate || group.ID == "" {
			addBlocker(DeleteBlocker{
				Code:           BlockerInvalidSnapshot,
				Message:        "snapshot contains an invalid or duplicate content group id",
				ContentGroupID: group.ID,
			})
			continue
		}
		contentGroups[group.ID] = group
	}
	dataGroups := make(map[string]DataGroup, len(snapshot.DataGroups))
	for _, group := range snapshot.DataGroups {
		if _, duplicate := dataGroups[group.ID]; duplicate || group.ID == "" {
			addBlocker(DeleteBlocker{
				Code:        BlockerInvalidSnapshot,
				Message:     "snapshot contains an invalid or duplicate data group id",
				DataGroupID: group.ID,
			})
			continue
		}
		dataGroups[group.ID] = group
	}

	selected := make(map[string]TorrentInstance, len(selectedIDs))
	affectedContentGroups := make(map[string]struct{})
	affectedDataGroups := make(map[string]struct{})
	for _, id := range selectedIDs {
		instance, exists := instances[id]
		if !exists {
			addBlocker(DeleteBlocker{
				Code:       BlockerUnknownInstance,
				Message:    "selected torrent instance does not exist",
				InstanceID: id,
			})
			continue
		}
		if !instance.Active() {
			addBlocker(DeleteBlocker{
				Code:       BlockerInactiveInstance,
				Message:    "selected torrent instance is already absent from its downloader",
				InstanceID: id,
			})
			continue
		}
		selected[id] = instance
		if !instance.DownloaderOnline {
			addBlocker(DeleteBlocker{
				Code:         BlockerDownloaderOffline,
				Message:      "selected torrent's downloader is offline",
				InstanceID:   id,
				DownloaderID: instance.DownloaderID,
			})
		}
		if instance.Stale {
			addBlocker(DeleteBlocker{
				Code:       BlockerStaleData,
				Message:    "selected torrent data is stale and must be refreshed",
				InstanceID: id,
			})
		}
		if instance.ContentGroupID == "" {
			addBlocker(DeleteBlocker{
				Code:       BlockerMissingContentGroup,
				Message:    "selected torrent has no content group",
				InstanceID: id,
			})
		} else {
			affectedContentGroups[instance.ContentGroupID] = struct{}{}
		}
		if instance.DataGroupID == "" {
			addBlocker(DeleteBlocker{
				Code:       BlockerMissingDataGroup,
				Message:    "selected torrent has no physical data group",
				InstanceID: id,
			})
		} else {
			affectedDataGroups[instance.DataGroupID] = struct{}{}
		}
	}

	validateExpectedVersions(
		"content_group",
		affectedContentGroups,
		request.ExpectedContentGroupVersions,
		request.RequireExpectedVersions,
		func(id string) (uint64, bool) {
			group, ok := contentGroups[id]
			return group.Version, ok
		},
		addBlocker,
	)
	validateExpectedVersions(
		"data_group",
		affectedDataGroups,
		request.ExpectedDataGroupVersions,
		request.RequireExpectedVersions,
		func(id string) (uint64, bool) {
			group, ok := dataGroups[id]
			return group.Version, ok
		},
		addBlocker,
	)

	activeByDataGroup := make(map[string][]TorrentInstance)
	for _, instance := range instances {
		if instance.Active() && instance.DataGroupID != "" {
			activeByDataGroup[instance.DataGroupID] = append(activeByDataGroup[instance.DataGroupID], instance)
		}
	}
	selectedByDataGroup := make(map[string][]TorrentInstance)
	for _, instance := range selected {
		if instance.DataGroupID != "" {
			selectedByDataGroup[instance.DataGroupID] = append(selectedByDataGroup[instance.DataGroupID], instance)
		}
	}

	var taskOnlySteps, dataDeletingSteps []DeleteStep
	groupIDs := sortedMapKeys(selectedByDataGroup)
	for _, groupID := range groupIDs {
		group, groupExists := dataGroups[groupID]
		selectedMembers := selectedByDataGroup[groupID]
		sort.Slice(selectedMembers, func(i, j int) bool { return selectedMembers[i].ID < selectedMembers[j].ID })
		if !groupExists {
			addBlocker(DeleteBlocker{
				Code:        BlockerMissingDataGroup,
				Message:     "selected torrent references a data group that does not exist",
				DataGroupID: groupID,
			})
			continue
		}
		if group.Confidence == ConfidenceVerified &&
			(group.FileSizeFingerprint == "" || !validSHA256Hex(group.FileSizeFingerprint)) {
			addBlocker(DeleteBlocker{
				Code:        BlockerInvalidSnapshot,
				Message:     "verified data group is missing a valid file-size fingerprint",
				DataGroupID: groupID,
			})
		}

		activeMembers := activeByDataGroup[groupID]
		for _, instance := range activeMembers {
			if instance.StorageID != group.StorageID || instance.CanonicalPath != group.CanonicalPath || instance.WantedBytes != group.WantedBytes {
				addBlocker(DeleteBlocker{
					Code:        BlockerInvalidSnapshot,
					Message:     "data group metadata does not match one of its active torrent instances",
					InstanceID:  instance.ID,
					DataGroupID: groupID,
				})
			}
			if group.Confidence == ConfidenceVerified &&
				(instance.FileSizeFingerprint != group.FileSizeFingerprint || instance.SelectedFileCount != group.SelectedFileCount) {
				addBlocker(DeleteBlocker{
					Code:        BlockerInvalidSnapshot,
					Message:     "verified file evidence does not match one of the data group's active torrents",
					InstanceID:  instance.ID,
					DataGroupID: groupID,
				})
			}
		}

		deleteData := len(selectedMembers) == len(activeMembers)
		if deleteData {
			if group.Confidence != ConfidenceVerified && group.Confidence != ConfidenceManual {
				addBlocker(DeleteBlocker{
					Code:        BlockerUnverifiedDataGroup,
					Message:     "the final physical reference is not verified; file deletion is unsafe",
					DataGroupID: groupID,
				})
			}
			for _, occupant := range instances {
				if !occupant.Active() || occupant.DataGroupID == groupID {
					continue
				}
				if occupant.StorageID == group.StorageID && pathsOverlap(occupant.CanonicalPath, group.CanonicalPath) {
					addBlocker(DeleteBlocker{
						Code:        BlockerConflictingPathOccupant,
						Message:     "another data group occupies the same physical path",
						InstanceID:  occupant.ID,
						DataGroupID: groupID,
					})
				}
			}
			if request.RequireFreshStorageSnapshot {
				validateStorageFreshness(group, snapshot.DownloaderStates, addBlocker)
			}
		}

		lastIndex := len(selectedMembers) - 1
		for index, instance := range selectedMembers {
			step := DeleteStep{
				InstanceID:     instance.ID,
				DownloaderID:   instance.DownloaderID,
				ExternalKey:    instance.ExternalKey,
				ContentGroupID: instance.ContentGroupID,
				DataGroupID:    instance.DataGroupID,
				DeleteData:     deleteData && index == lastIndex,
			}
			if step.DeleteData {
				dataDeletingSteps = append(dataDeletingSteps, step)
			} else {
				taskOnlySteps = append(taskOnlySteps, step)
			}
		}
	}

	sortDeleteSteps(taskOnlySteps)
	sortDeleteSteps(dataDeletingSteps)
	plan.Steps = append(taskOnlySteps, dataDeletingSteps...)
	for index := range plan.Steps {
		plan.Steps[index].Order = index + 1
	}
	sort.Slice(plan.Blockers, func(i, j int) bool {
		return blockerSortKey(plan.Blockers[i]) < blockerSortKey(plan.Blockers[j])
	})
	plan.Executable = len(plan.Blockers) == 0 && len(plan.Steps) > 0
	plan.ID = deletionPlanID(plan, request)
	return plan
}

func validateExpectedVersions(
	resourceType string,
	affected map[string]struct{},
	expected map[string]uint64,
	required bool,
	actualVersion func(string) (uint64, bool),
	addBlocker func(DeleteBlocker),
) {
	ids := sortedSetKeys(affected)
	for _, id := range ids {
		actual, exists := actualVersion(id)
		if !exists {
			blocker := DeleteBlocker{Message: "selected torrent references a group that does not exist"}
			if resourceType == "content_group" {
				blocker.Code = BlockerMissingContentGroup
				blocker.ContentGroupID = id
			} else {
				blocker.Code = BlockerMissingDataGroup
				blocker.DataGroupID = id
			}
			addBlocker(blocker)
			continue
		}
		wanted, supplied := expected[id]
		if !supplied {
			if required {
				blocker := DeleteBlocker{
					Code:    BlockerMissingExpectedVersion,
					Message: "an expected group version is required before deletion",
				}
				if resourceType == "content_group" {
					blocker.ContentGroupID = id
				} else {
					blocker.DataGroupID = id
				}
				addBlocker(blocker)
			}
			continue
		}
		if err := CheckExpectedVersion(resourceType, id, wanted, actual); err != nil {
			blocker := DeleteBlocker{
				Code:            BlockerVersionConflict,
				Message:         err.Error(),
				ExpectedVersion: wanted,
				ActualVersion:   actual,
			}
			if resourceType == "content_group" {
				blocker.ContentGroupID = id
			} else {
				blocker.DataGroupID = id
			}
			addBlocker(blocker)
		}
	}
}

func validateStorageFreshness(group DataGroup, states []DownloaderState, addBlocker func(DeleteBlocker)) {
	found := false
	for _, state := range states {
		if !containsString(state.StorageIDs, group.StorageID) {
			continue
		}
		found = true
		if !state.Online {
			addBlocker(DeleteBlocker{
				Code:         BlockerStorageDownloaderOffline,
				Message:      "a downloader sharing this storage is offline",
				DownloaderID: state.ID,
				DataGroupID:  group.ID,
			})
		}
		if state.Stale {
			addBlocker(DeleteBlocker{
				Code:         BlockerStorageSnapshotStale,
				Message:      "a downloader sharing this storage has stale data",
				DownloaderID: state.ID,
				DataGroupID:  group.ID,
			})
		}
	}
	if !found {
		addBlocker(DeleteBlocker{
			Code:        BlockerStorageSnapshotMissing,
			Message:     "no complete downloader snapshot covers this storage",
			DataGroupID: group.ID,
		})
	}
}

func uniqueSortedStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	return sortedSetKeys(set)
}

func sortedSetKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func pathsOverlap(first, second string) bool {
	return directoryPrefix(first, second, false) || directoryPrefix(second, first, false)
}

func sortDeleteSteps(steps []DeleteStep) {
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].DataGroupID != steps[j].DataGroupID {
			return steps[i].DataGroupID < steps[j].DataGroupID
		}
		return steps[i].InstanceID < steps[j].InstanceID
	})
}

func blockerSortKey(blocker DeleteBlocker) string {
	return strings.Join([]string{
		string(blocker.Code),
		blocker.DataGroupID,
		blocker.ContentGroupID,
		blocker.DownloaderID,
		blocker.InstanceID,
	}, "\x00")
}

func deletionPlanID(plan DeletePlan, request DeleteRequest) string {
	parts := append([]string(nil), plan.SelectedInstanceIDs...)
	for _, step := range plan.Steps {
		parts = append(parts, fmt.Sprintf("step:%s:%t", step.InstanceID, step.DeleteData))
	}
	for _, blocker := range plan.Blockers {
		parts = append(parts, fmt.Sprintf(
			"block:%s:%s:%s:%s:%d:%d",
			blocker.Code,
			blocker.InstanceID,
			blocker.ContentGroupID,
			blocker.DataGroupID,
			blocker.ExpectedVersion,
			blocker.ActualVersion,
		))
	}
	for _, id := range sortedMapKeys(request.ExpectedContentGroupVersions) {
		parts = append(parts, fmt.Sprintf("cg:%s:%d", id, request.ExpectedContentGroupVersions[id]))
	}
	for _, id := range sortedMapKeys(request.ExpectedDataGroupVersions) {
		parts = append(parts, fmt.Sprintf("dg:%s:%d", id, request.ExpectedDataGroupVersions[id]))
	}
	return DeterministicID("delete-plan", parts...)
}
