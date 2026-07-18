package domain

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrInvalidTorrentInstance = errors.New("invalid torrent instance")
	ErrDuplicateTorrentID     = errors.New("duplicate torrent instance id")
)

// GroupingPlan is an automatic proposal. The persistence layer may reuse an
// existing stable group ID when reconciling the proposed AutoKey/PhysicalKey.
// Manual content memberships are preserved and are never replaced here.
type GroupingPlan struct {
	ContentGroups []ContentGroup `json:"content_groups"`
	DataGroups    []DataGroup    `json:"data_groups"`
	Memberships   []Membership   `json:"memberships"`
}

// BuildAutoKey creates the fast candidate key used for automatic grouping.
func BuildAutoKey(storageID, canonicalPath string, wantedBytes int64) (string, error) {
	if wantedBytes < 0 {
		return "", fmt.Errorf("%w: wanted bytes cannot be negative", ErrInvalidTorrentInstance)
	}
	location, err := CanonicalizeContentPath(storageID, canonicalPath)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidTorrentInstance, err)
	}
	return DeterministicID("auto", location.StorageID, location.Path, strconv.FormatInt(wantedBytes, 10)), nil
}

type normalizedInstance struct {
	instance    TorrentInstance
	autoKey     string
	known       bool
	fileCount   int
	fingerprint string
}

type groupingPartition struct {
	autoKey     string
	storageID   string
	path        string
	wantedBytes int64
	fileCount   int
	fingerprint string
	confidence  GroupConfidence
	members     []normalizedInstance
}

// PlanAutomaticGroups performs a conservative two-stage grouping:
//
//  1. storage ID + canonical content path + wanted bytes form candidates;
//  2. known selected-file-size fingerprints split conflicting candidates.
//
// Missing file evidence may join exactly one compatible known partition but
// makes it tentative. Ambiguous unknown members stay in a separate tentative
// partition. Only verified/manual DataGroups may later authorize file deletion.
func PlanAutomaticGroups(instances []TorrentInstance) (GroupingPlan, error) {
	candidates := make(map[string][]normalizedInstance)
	seenIDs := make(map[string]struct{}, len(instances))

	for _, instance := range instances {
		if !instance.Active() {
			continue
		}
		if instance.ID == "" || instance.DownloaderID == "" || instance.ExternalKey == "" {
			return GroupingPlan{}, fmt.Errorf(
				"%w %q: id, downloader_id, and external_key are required",
				ErrInvalidTorrentInstance,
				instance.ID,
			)
		}
		if _, duplicate := seenIDs[instance.ID]; duplicate {
			return GroupingPlan{}, fmt.Errorf("%w: %q", ErrDuplicateTorrentID, instance.ID)
		}
		seenIDs[instance.ID] = struct{}{}

		normalized, err := normalizeInstanceForGrouping(instance)
		if err != nil {
			return GroupingPlan{}, err
		}
		candidates[normalized.autoKey] = append(candidates[normalized.autoKey], normalized)
	}

	partitions := make([]groupingPartition, 0, len(candidates))
	for _, candidate := range candidates {
		partitions = append(partitions, partitionCandidate(candidate)...)
	}
	sort.Slice(partitions, func(i, j int) bool {
		return partitionSortKey(partitions[i]) < partitionSortKey(partitions[j])
	})

	plan := GroupingPlan{
		ContentGroups: make([]ContentGroup, 0, len(partitions)),
		DataGroups:    make([]DataGroup, 0, len(partitions)),
		Memberships:   make([]Membership, 0, len(instances)),
	}

	for _, partition := range partitions {
		sort.Slice(partition.members, func(i, j int) bool {
			return partition.members[i].instance.ID < partition.members[j].instance.ID
		})
		allMemberIDs := make([]string, 0, len(partition.members))
		autoMemberIDs := make([]string, 0, len(partition.members))
		for _, member := range partition.members {
			allMemberIDs = append(allMemberIDs, member.instance.ID)
			if member.instance.AssignmentSource != AssignmentManual {
				autoMemberIDs = append(autoMemberIDs, member.instance.ID)
			}
		}

		partitionKey := partitionSortKey(partition)
		dataGroupID := DeterministicID("dg", partitionKey)
		physicalKey := DeterministicID(
			"physical",
			partition.storageID,
			partition.path,
			strconv.FormatInt(partition.wantedBytes, 10),
			strconv.Itoa(partition.fileCount),
			partition.fingerprint,
		)
		plan.DataGroups = append(plan.DataGroups, DataGroup{
			ID:                  dataGroupID,
			Version:             1,
			StorageID:           partition.storageID,
			CanonicalPath:       partition.path,
			WantedBytes:         partition.wantedBytes,
			SelectedFileCount:   partition.fileCount,
			FileSizeFingerprint: partition.fingerprint,
			Confidence:          partition.confidence,
			PhysicalKey:         physicalKey,
			MemberIDs:           append([]string(nil), allMemberIDs...),
		})

		contentGroupID := ""
		if len(autoMemberIDs) > 0 {
			contentGroupID = DeterministicID("cg", partitionKey)
			plan.ContentGroups = append(plan.ContentGroups, ContentGroup{
				ID:             contentGroupID,
				Version:        1,
				Mode:           GroupModeAuto,
				Confidence:     partition.confidence,
				AutoKey:        partition.autoKey,
				MemberIDs:      append([]string(nil), autoMemberIDs...),
				TaskCount:      len(autoMemberIDs),
				LogicalBytes:   partition.wantedBytes,
				DataGroupCount: 1,
			})
		}

		for _, member := range partition.members {
			membershipContentGroupID := contentGroupID
			source := AssignmentAuto
			if member.instance.AssignmentSource == AssignmentManual {
				if member.instance.ContentGroupID == "" {
					return GroupingPlan{}, fmt.Errorf(
						"%w %q: manual assignment requires content_group_id",
						ErrInvalidTorrentInstance,
						member.instance.ID,
					)
				}
				membershipContentGroupID = member.instance.ContentGroupID
				source = AssignmentManual
			}
			plan.Memberships = append(plan.Memberships, Membership{
				TorrentInstanceID: member.instance.ID,
				ContentGroupID:    membershipContentGroupID,
				DataGroupID:       dataGroupID,
				AssignmentSource:  source,
				SuggestedAutoKey:  partition.autoKey,
			})
		}
	}

	sort.Slice(plan.ContentGroups, func(i, j int) bool { return plan.ContentGroups[i].ID < plan.ContentGroups[j].ID })
	sort.Slice(plan.DataGroups, func(i, j int) bool { return plan.DataGroups[i].ID < plan.DataGroups[j].ID })
	sort.Slice(plan.Memberships, func(i, j int) bool {
		return plan.Memberships[i].TorrentInstanceID < plan.Memberships[j].TorrentInstanceID
	})
	return plan, nil
}

func normalizeInstanceForGrouping(instance TorrentInstance) (normalizedInstance, error) {
	canonicalPath := instance.CanonicalPath
	if canonicalPath == "" {
		return normalizedInstance{}, fmt.Errorf(
			"%w %q: canonical_path is required after storage mapping",
			ErrInvalidTorrentInstance,
			instance.ID,
		)
	}
	location, err := CanonicalizeContentPath(instance.StorageID, canonicalPath)
	if err != nil {
		return normalizedInstance{}, fmt.Errorf("%w %q: %v", ErrInvalidTorrentInstance, instance.ID, err)
	}
	instance.CanonicalPath = location.Path
	if instance.WantedBytes < 0 {
		return normalizedInstance{}, fmt.Errorf(
			"%w %q: wanted_bytes cannot be negative",
			ErrInvalidTorrentInstance,
			instance.ID,
		)
	}

	known := instance.SelectedFilesKnown || instance.FileSizeFingerprint != "" || len(instance.SelectedFileSizes) > 0
	fileCount := instance.SelectedFileCount
	fingerprint := strings.ToLower(instance.FileSizeFingerprint)

	if len(instance.SelectedFileSizes) > 0 || (instance.SelectedFilesKnown && fingerprint == "" && fileCount == 0) {
		if fileCount != 0 && fileCount != len(instance.SelectedFileSizes) {
			return normalizedInstance{}, fmt.Errorf(
				"%w %q: selected_file_count=%d, size list has %d entries",
				ErrInvalidTorrentInstance,
				instance.ID,
				fileCount,
				len(instance.SelectedFileSizes),
			)
		}
		fileCount = len(instance.SelectedFileSizes)
		computed, fingerprintErr := SelectedFileSizeFingerprint(instance.SelectedFileSizes)
		if fingerprintErr != nil {
			return normalizedInstance{}, fmt.Errorf("%w %q: %v", ErrInvalidTorrentInstance, instance.ID, fingerprintErr)
		}
		if fingerprint != "" && fingerprint != computed {
			return normalizedInstance{}, fmt.Errorf(
				"%w %q: supplied file-size fingerprint does not match selected sizes",
				ErrInvalidTorrentInstance,
				instance.ID,
			)
		}
		fingerprint = computed
		known = true
	}
	if known && fingerprint == "" {
		return normalizedInstance{}, fmt.Errorf(
			"%w %q: selected files are marked known but no fingerprint or sizes were supplied",
			ErrInvalidTorrentInstance,
			instance.ID,
		)
	}
	if fingerprint != "" && !validSHA256Hex(fingerprint) {
		return normalizedInstance{}, fmt.Errorf(
			"%w %q: file_size_fingerprint must be a SHA-256 hex digest",
			ErrInvalidTorrentInstance,
			instance.ID,
		)
	}
	instance.SelectedFilesKnown = known
	instance.SelectedFileCount = fileCount
	instance.FileSizeFingerprint = fingerprint

	autoKey, err := BuildAutoKey(location.StorageID, location.Path, instance.WantedBytes)
	if err != nil {
		return normalizedInstance{}, err
	}
	return normalizedInstance{
		instance:    instance,
		autoKey:     autoKey,
		known:       known,
		fileCount:   fileCount,
		fingerprint: fingerprint,
	}, nil
}

func validSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func partitionCandidate(candidate []normalizedInstance) []groupingPartition {
	if len(candidate) == 0 {
		return nil
	}

	type knownBucket struct {
		fileCount   int
		fingerprint string
		members     []normalizedInstance
	}
	known := make(map[string]*knownBucket)
	unknown := make([]normalizedInstance, 0)
	for _, member := range candidate {
		if !member.known {
			unknown = append(unknown, member)
			continue
		}
		key := strconv.Itoa(member.fileCount) + ":" + member.fingerprint
		bucket := known[key]
		if bucket == nil {
			bucket = &knownBucket{fileCount: member.fileCount, fingerprint: member.fingerprint}
			known[key] = bucket
		}
		bucket.members = append(bucket.members, member)
	}

	knownKeys := make([]string, 0, len(known))
	for key := range known {
		knownKeys = append(knownKeys, key)
	}
	sort.Strings(knownKeys)

	base := candidate[0]
	partitions := make([]groupingPartition, 0, len(known)+1)
	knownPartitionIndex := make(map[string]int, len(known))
	for _, key := range knownKeys {
		bucket := known[key]
		knownPartitionIndex[key] = len(partitions)
		partitions = append(partitions, groupingPartition{
			autoKey:     base.autoKey,
			storageID:   base.instance.StorageID,
			path:        base.instance.CanonicalPath,
			wantedBytes: base.instance.WantedBytes,
			fileCount:   bucket.fileCount,
			fingerprint: bucket.fingerprint,
			confidence:  ConfidenceVerified,
			members:     append([]normalizedInstance(nil), bucket.members...),
		})
	}

	standaloneUnknown := make(map[int][]normalizedInstance)
	for _, member := range unknown {
		matchingKnownKey := ""
		for _, key := range knownKeys {
			bucket := known[key]
			if member.fileCount != 0 && member.fileCount != bucket.fileCount {
				continue
			}
			if matchingKnownKey != "" {
				matchingKnownKey = ""
				break
			}
			matchingKnownKey = key
		}
		if matchingKnownKey != "" {
			index := knownPartitionIndex[matchingKnownKey]
			partitions[index].members = append(partitions[index].members, member)
			partitions[index].confidence = ConfidenceTentative
			continue
		}
		standaloneUnknown[member.fileCount] = append(standaloneUnknown[member.fileCount], member)
	}

	unknownCounts := make([]int, 0, len(standaloneUnknown))
	for count := range standaloneUnknown {
		unknownCounts = append(unknownCounts, count)
	}
	sort.Ints(unknownCounts)
	for _, count := range unknownCounts {
		partitions = append(partitions, groupingPartition{
			autoKey:     base.autoKey,
			storageID:   base.instance.StorageID,
			path:        base.instance.CanonicalPath,
			wantedBytes: base.instance.WantedBytes,
			fileCount:   count,
			confidence:  ConfidenceTentative,
			members:     append([]normalizedInstance(nil), standaloneUnknown[count]...),
		})
	}
	return partitions
}

func partitionSortKey(partition groupingPartition) string {
	return strings.Join([]string{
		partition.autoKey,
		strconv.Itoa(partition.fileCount),
		partition.fingerprint,
	}, "|")
}
