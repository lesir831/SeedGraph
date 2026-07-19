package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lesir831/SeedGraph/internal/cryptox"
	"github.com/lesir831/SeedGraph/internal/domain"
	"github.com/lesir831/SeedGraph/internal/downloader"
	"github.com/lesir831/SeedGraph/internal/store"
)

type ClientFactory func(downloader.Config) (downloader.Client, error)

type Service struct {
	store        *store.Store
	cipher       *cryptox.Cipher
	logger       *slog.Logger
	factory      ClientFactory
	interval     time.Duration
	fullInterval time.Duration

	mu       sync.Mutex
	running  map[string]bool
	lastFull map[string]time.Time
	rootCtx  context.Context
	workers  sync.WaitGroup
}

func New(store *store.Store, cipher *cryptox.Cipher, logger *slog.Logger, interval, fullInterval time.Duration) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store: store, cipher: cipher, logger: logger, factory: downloader.New,
		interval: interval, fullInterval: fullInterval,
		running: make(map[string]bool), lastFull: make(map[string]time.Time),
	}
}

func (s *Service) SetClientFactory(factory ClientFactory) {
	s.factory = factory
}

// Run starts periodic synchronization and blocks until ctx is canceled.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.rootCtx = ctx
	s.mu.Unlock()
	s.triggerAll(true)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.triggerAll(false)
		}
	}
}

func (s *Service) triggerAll(forceFull bool) {
	ctx := s.context()
	downloaders, err := s.store.ListDownloaders(ctx)
	if err != nil {
		s.logger.Error("list downloaders for scheduled sync", "error", err)
		return
	}
	for _, item := range downloaders {
		if !item.Enabled {
			continue
		}
		full := forceFull || s.fullSyncDue(item.ID)
		if err := s.Trigger(item.ID, full); err != nil && !errors.Is(err, ErrAlreadyRunning) {
			s.logger.Warn("schedule downloader sync", "downloader_id", item.ID, "error", err)
		}
	}
}

var ErrAlreadyRunning = errors.New("downloader sync is already running")

// Trigger queues a sync and returns immediately.
func (s *Service) Trigger(downloaderID string, full bool) error {
	if !s.begin(downloaderID) {
		return ErrAlreadyRunning
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer s.end(downloaderID)
		if _, err := s.sync(s.context(), downloaderID, full); err != nil {
			s.logger.Warn("downloader sync failed", "downloader_id", downloaderID, "error", err)
		}
	}()
	return nil
}

// TriggerAll queues enabled downloaders and returns the number accepted.
func (s *Service) TriggerAll(full bool) (int, error) {
	items, err := s.store.ListDownloaders(s.context())
	if err != nil {
		return 0, err
	}
	accepted := 0
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		if err := s.Trigger(item.ID, full); err == nil {
			accepted++
		} else if !errors.Is(err, ErrAlreadyRunning) {
			return accepted, err
		}
	}
	return accepted, nil
}

// SyncNow performs a blocking sync. It is used for connection tests and the
// destructive-operation revalidation barrier.
func (s *Service) SyncNow(ctx context.Context, downloaderID string, full bool) (store.ApplySyncResult, error) {
	s.workers.Add(1)
	defer s.workers.Done()
	if !s.begin(downloaderID) {
		return store.ApplySyncResult{}, ErrAlreadyRunning
	}
	defer s.end(downloaderID)
	return s.sync(ctx, downloaderID, full)
}

func (s *Service) TestConnection(ctx context.Context, downloaderID string) (string, error) {
	s.workers.Add(1)
	defer s.workers.Done()
	item, client, err := s.client(ctx, downloaderID)
	if err != nil {
		return "", err
	}
	version, err := client.Version(ctx)
	if err != nil {
		_ = s.store.UpdateDownloaderConnectionState(ctx, item.ID, false, "", safeError(err), false)
		return "", err
	}
	if err := s.store.UpdateDownloaderConnectionState(ctx, item.ID, true, version, "", true); err != nil {
		return "", err
	}
	return version, nil
}

func (s *Service) Running() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]string, 0, len(s.running))
	for id, running := range s.running {
		if running {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

// Wait blocks until accepted background and blocking downloader operations
// have returned. Call it only after schedulers and HTTP handlers are stopped.
func (s *Service) Wait() {
	s.workers.Wait()
}

func (s *Service) sync(ctx context.Context, downloaderID string, full bool) (store.ApplySyncResult, error) {
	item, client, err := s.client(ctx, downloaderID)
	if err != nil {
		return store.ApplySyncResult{}, err
	}
	mode := "delta"
	var snapshot downloader.Snapshot
	if full {
		mode = "full"
		snapshot, err = client.FullSnapshot(ctx)
	} else {
		snapshot, err = client.Delta(ctx, item.SyncCursor)
	}
	if err != nil {
		_ = s.store.RecordFailedSync(ctx, item.ID, mode, item.SyncCursor, errors.New(safeError(err)))
		return store.ApplySyncResult{}, err
	}

	records, err := s.normalizeSnapshot(ctx, item, snapshot, client)
	if err != nil {
		_ = s.store.RecordFailedSync(ctx, item.ID, mode, item.SyncCursor, errors.New(safeError(err)))
		return store.ApplySyncResult{}, err
	}
	removedHashes := make([]string, 0, len(snapshot.Removed))
	removedRemoteIDs := make([]string, 0, len(snapshot.Removed))
	for _, removed := range snapshot.Removed {
		if removed.StableHash != "" {
			removedHashes = append(removedHashes, removed.StableHash)
		}
		if removed.RemoteID != "" {
			removedRemoteIDs = append(removedRemoteIDs, removed.RemoteID)
		}
	}
	cursorAfter := snapshot.Cursor
	if cursorAfter == "" {
		cursorAfter = item.SyncCursor
	}
	result, err := s.store.ApplySync(ctx, store.ApplySyncParams{
		DownloaderID:        item.ID,
		Mode:                mode,
		Complete:            snapshot.Full,
		CursorBefore:        item.SyncCursor,
		CursorAfter:         cursorAfter,
		Torrents:            records,
		RemovedStableHashes: removedHashes,
		RemovedRemoteIDs:    removedRemoteIDs,
	})
	if err != nil {
		_ = s.store.RecordFailedSync(ctx, item.ID, mode, item.SyncCursor, errors.New(safeError(err)))
		return store.ApplySyncResult{}, err
	}
	if full || snapshot.Full {
		s.mu.Lock()
		s.lastFull[item.ID] = time.Now()
		s.mu.Unlock()
	}
	return result, nil
}

func (s *Service) normalizeSnapshot(ctx context.Context, item store.Downloader, snapshot downloader.Snapshot, client downloader.Client) ([]store.TorrentRecord, error) {
	rules, err := s.store.ListTrackerRules(ctx)
	if err != nil {
		return nil, err
	}
	domainInstances := make([]domain.TorrentInstance, 0, len(snapshot.Torrents))
	byID := make(map[string]downloader.Torrent, len(snapshot.Torrents))
	candidates := make(map[string][]int)
	for _, raw := range snapshot.Torrents {
		instance, err := normalizeTorrent(item, raw)
		if err != nil {
			return nil, fmt.Errorf("normalize torrent %q: %w", raw.Name, err)
		}
		domainInstances = append(domainInstances, instance)
		byID[instance.ID] = raw
		autoKey, _ := domain.BuildAutoKey(instance.StorageID, instance.CanonicalPath, instance.WantedBytes)
		candidates[autoKey] = append(candidates[autoKey], len(domainInstances)-1)
	}
	if manifestClient, ok := client.(downloader.FileManifestClient); ok {
		// qBittorrent exposes files only through a per-torrent endpoint. Fetch
		// evidence for every member returned by this snapshot, including
		// singletons: grouping needs sizes and safe deletion needs exact paths.
		// Normal cycles are deltas, so unchanged tasks do not incur repeated
		// requests.
		for _, indexes := range candidates {
			for _, index := range indexes {
				if domainInstances[index].FileManifestKnown {
					continue
				}
				raw := byID[domainInstances[index].ID]
				files, err := manifestClient.FileManifest(ctx, raw)
				if err != nil {
					s.logger.Warn("file manifest unavailable; group remains tentative",
						"downloader_id", item.ID, "torrent_hash", raw.StableHash, "error", safeError(err))
					continue
				}
				sizes := selectedFileSizes(files)
				raw.SelectedFilesKnown = true
				raw.SelectedFileCount = len(sizes)
				raw.SelectedFileSizes = sizes
				raw.FileManifestKnown = true
				raw.Files = append([]downloader.TorrentFile(nil), files...)
				byID[domainInstances[index].ID] = raw
				normalized, err := normalizeTorrent(item, raw)
				if err != nil {
					return nil, err
				}
				domainInstances[index] = normalized
			}
		}
	}
	plan, err := domain.PlanAutomaticGroups(domainInstances)
	if err != nil {
		return nil, err
	}
	memberships := make(map[string]domain.Membership, len(plan.Memberships))
	for _, membership := range plan.Memberships {
		memberships[membership.TorrentInstanceID] = membership
	}
	contentGroups := make(map[string]domain.ContentGroup, len(plan.ContentGroups))
	for _, group := range plan.ContentGroups {
		contentGroups[group.ID] = group
	}
	dataGroups := make(map[string]domain.DataGroup, len(plan.DataGroups))
	for _, group := range plan.DataGroups {
		dataGroups[group.ID] = group
	}

	records := make([]store.TorrentRecord, 0, len(domainInstances))
	for _, instance := range domainInstances {
		raw := byID[instance.ID]
		membership := memberships[instance.ID]
		contentGroup := contentGroups[membership.ContentGroupID]
		dataGroup := dataGroups[membership.DataGroupID]
		trackers := classifyTrackers(raw.TrackerURLs, rules)
		metadataFingerprint := fingerprint(struct {
			Hash, Name, Path, Storage, Manifest string
			Bytes                               int64
			Trackers                            []store.TrackerRecord
			Files                               []domain.TorrentFile
		}{raw.StableHash, raw.Name, instance.CanonicalPath, instance.StorageID, instance.FileSizeFingerprint, instance.WantedBytes, trackers, instance.Files})
		runtime := store.RuntimeRecord{
			Status: raw.State, Progress: raw.Progress, Ratio: raw.Ratio,
			UploadedBytes: raw.UploadedBytes, DownloadedBytes: raw.DownloadedBytes,
			UploadSpeed: raw.UploadSpeed, DownloadSpeed: raw.DownloadSpeed,
		}
		runtime.RuntimeFingerprint = fingerprint(runtime)
		records = append(records, store.TorrentRecord{
			ID: instance.ID, DownloaderID: item.ID, StableHashKey: raw.StableHash,
			RemoteID: raw.RemoteID, Name: raw.Name, SourcePath: raw.ContentPath,
			CanonicalPath: instance.CanonicalPath, StorageID: instance.StorageID,
			AddedAt:     raw.AddedAt,
			WantedBytes: instance.WantedBytes, ManifestFingerprint: instance.FileSizeFingerprint,
			SelectedFileCount:   instance.SelectedFileCount,
			FileManifestKnown:   instance.FileManifestKnown,
			Files:               storeFileRecords(instance.Files),
			MetadataFingerprint: metadataFingerprint,
			ContentGroupID:      membership.ContentGroupID, ContentGroupAutoKey: contentGroup.AutoKey,
			DataGroupID: membership.DataGroupID, DataGroupAutoKey: dataGroup.PhysicalKey,
			Confidence: string(dataGroup.Confidence), Runtime: runtime, Trackers: trackers,
		})
	}
	return records, nil
}

// DeleteRemote executes one already-planned remote mutation. Callers must use
// the domain deletion planner before invoking this method.
func (s *Service) DeleteRemote(ctx context.Context, downloaderID string, ref downloader.TorrentRef, deleteData bool) error {
	s.workers.Add(1)
	defer s.workers.Done()
	_, client, err := s.client(ctx, downloaderID)
	if err != nil {
		return err
	}
	return client.Delete(ctx, ref, deleteData)
}

func normalizeTorrent(item store.Downloader, raw downloader.Torrent) (domain.TorrentInstance, error) {
	location, err := canonicalizeDownloaderPath(item, raw.ContentPath)
	if err != nil {
		return domain.TorrentInstance{}, err
	}
	files := make([]domain.TorrentFile, 0, len(raw.Files))
	if raw.FileManifestKnown {
		for _, file := range raw.Files {
			if file.Size < 0 {
				return domain.TorrentInstance{}, fmt.Errorf("invalid file size %d", file.Size)
			}
			fileLocation, err := canonicalizeDownloaderPath(item, file.Path)
			if err != nil {
				return domain.TorrentInstance{}, fmt.Errorf("normalize file %q: %w", file.Path, err)
			}
			if fileLocation.StorageID != location.StorageID {
				return domain.TorrentInstance{}, fmt.Errorf("file %q maps to storage %q instead of %q", file.Path, fileLocation.StorageID, location.StorageID)
			}
			files = append(files, domain.TorrentFile{
				SourcePath: file.Path, CanonicalPath: fileLocation.Path,
				Size: file.Size, Selected: file.Selected,
			})
		}
		sort.Slice(files, func(i, j int) bool { return files[i].CanonicalPath < files[j].CanonicalPath })
	}
	selectedSizes := append([]int64(nil), raw.SelectedFileSizes...)
	if raw.FileManifestKnown {
		selectedSizes = selectedDomainFileSizes(files)
	}
	manifest := ""
	if raw.SelectedFilesKnown || raw.FileManifestKnown {
		manifest, err = domain.SelectedFileSizeFingerprint(selectedSizes)
		if err != nil {
			return domain.TorrentInstance{}, err
		}
	}
	return domain.TorrentInstance{
		ID:           domain.DeterministicID("ti", item.ID, raw.StableHash),
		DownloaderID: item.ID, ExternalKey: raw.StableHash, RemoteID: raw.RemoteID,
		Name: raw.Name, StorageID: location.StorageID, ContentPath: raw.ContentPath,
		CanonicalPath: location.Path, WantedBytes: raw.WantedBytes,
		SelectedFilesKnown: raw.SelectedFilesKnown || raw.FileManifestKnown, SelectedFileCount: len(selectedSizes),
		SelectedFileSizes: selectedSizes, FileSizeFingerprint: manifest,
		FileManifestKnown: raw.FileManifestKnown, Files: files,
		AssignmentSource: domain.AssignmentAuto, DownloaderOnline: true,
	}, nil
}

func canonicalizeDownloaderPath(item store.Downloader, rawPath string) (domain.CanonicalLocation, error) {
	if len(item.PathMappings) == 0 {
		return domain.CanonicalizeContentPath(item.StorageID, rawPath)
	}
	mappings := make([]domain.PathMapping, 0, len(item.PathMappings))
	for _, mapping := range item.PathMappings {
		mappings = append(mappings, domain.PathMapping{
			ID: mapping.ID, DownloaderID: item.ID, StorageID: item.StorageID,
			SourcePrefix: mapping.SourcePrefix, TargetPrefix: mapping.TargetPrefix,
		})
	}
	return domain.ApplyPathMappings(item.ID, rawPath, mappings)
}

func selectedFileSizes(files []downloader.TorrentFile) []int64 {
	result := make([]int64, 0, len(files))
	for _, file := range files {
		if file.Selected {
			result = append(result, file.Size)
		}
	}
	return result
}

func selectedDomainFileSizes(files []domain.TorrentFile) []int64 {
	result := make([]int64, 0, len(files))
	for _, file := range files {
		if file.Selected {
			result = append(result, file.Size)
		}
	}
	return result
}

func storeFileRecords(files []domain.TorrentFile) []store.TorrentFileRecord {
	result := make([]store.TorrentFileRecord, 0, len(files))
	for _, file := range files {
		result = append(result, store.TorrentFileRecord{
			SourcePath: file.SourcePath, CanonicalPath: file.CanonicalPath,
			Size: file.Size, Selected: file.Selected,
		})
	}
	return result
}

func classifyTrackers(rawURLs []string, rules []store.TrackerRule) []store.TrackerRecord {
	var result []store.TrackerRecord
	seen := make(map[string]struct{})
	for _, rawURL := range rawURLs {
		host, pathHint, err := store.TrackerIdentity(rawURL)
		if err != nil {
			continue
		}
		siteID := resolveSite(host, pathHint, rules)
		key := host + "\x00" + pathHint + "\x00" + siteID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, store.TrackerRecord{HostIdentity: host, PathHint: pathHint, SiteID: siteID})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].HostIdentity != result[j].HostIdentity {
			return result[i].HostIdentity < result[j].HostIdentity
		}
		return result[i].PathHint < result[j].PathHint
	})
	return result
}

func resolveSite(host, pathHint string, rules []store.TrackerRule) string {
	for _, rule := range rules {
		pattern := strings.ToLower(strings.TrimSpace(rule.HostPattern))
		hostMatches := host == pattern
		if strings.HasPrefix(pattern, "*.") {
			suffix := strings.TrimPrefix(pattern, "*")
			hostMatches = strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".")
		}
		if !hostMatches || (rule.PathPrefix != "" && !strings.HasPrefix(pathHint, rule.PathPrefix)) {
			continue
		}
		return rule.SiteID
	}
	return ""
}

func (s *Service) client(ctx context.Context, downloaderID string) (store.Downloader, downloader.Client, error) {
	item, err := s.store.GetDownloader(ctx, downloaderID)
	if err != nil {
		return store.Downloader{}, nil, err
	}
	username, err := s.decrypt(item.UsernameCiphertext)
	if err != nil {
		return store.Downloader{}, nil, fmt.Errorf("decrypt downloader username: %w", err)
	}
	password, err := s.decrypt(item.PasswordCiphertext)
	if err != nil {
		return store.Downloader{}, nil, fmt.Errorf("decrypt downloader password: %w", err)
	}
	client, err := s.factory(downloader.Config{
		Kind: downloader.Kind(item.Kind), BaseURL: item.BaseURL, Username: username, Password: password,
	})
	if err != nil {
		return store.Downloader{}, nil, err
	}
	return item, client, nil
}

func (s *Service) decrypt(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	plaintext, err := s.cipher.Decrypt(value)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (s *Service) begin(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Service) end(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

func (s *Service) fullSyncDue(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.lastFull[id]
	return last.IsZero() || time.Since(last) >= s.fullInterval
}

func (s *Service) context() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rootCtx != nil {
		return s.rootCtx
	}
	return context.Background()
}

func fingerprint(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
