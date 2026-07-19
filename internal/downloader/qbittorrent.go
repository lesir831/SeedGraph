package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type qbittorrentClient struct {
	baseURL  *url.URL
	client   *http.Client
	username string
	password string

	authMu   sync.Mutex
	loggedIn bool
}

var _ Client = (*qbittorrentClient)(nil)

func newQBittorrent(config Config) (*qbittorrentClient, error) {
	baseURL, err := parseBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	client, err := cloneHTTPClient(config.HTTPClient, true)
	if err != nil {
		return nil, err
	}
	return &qbittorrentClient{
		baseURL:  baseURL,
		client:   client,
		username: config.Username,
		password: config.Password,
	}, nil
}

func (client *qbittorrentClient) Version(ctx context.Context) (string, error) {
	response, err := client.authenticatedRequest(ctx, http.MethodGet, "api/v2/app/version", nil, nil)
	if err != nil {
		return "", err
	}
	defer closeResponse(response)
	if response.StatusCode != http.StatusOK {
		return "", unexpectedStatus("qbittorrent app version", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTextResponse))
	if err != nil {
		return "", fmt.Errorf("qbittorrent app version: read response: %w", err)
	}
	version := strings.TrimSpace(string(body))
	if version == "" {
		return "", errors.New("qbittorrent app version: empty response")
	}
	return version, nil
}

func (client *qbittorrentClient) FullSnapshot(ctx context.Context) (Snapshot, error) {
	torrents, err := client.fetchTorrentInfo(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Full: true, Torrents: torrents}, nil
}

func (client *qbittorrentClient) Delta(ctx context.Context, cursor string) (Snapshot, error) {
	rid := strings.TrimSpace(cursor)
	if rid == "" {
		rid = "0"
	}
	if _, err := strconv.ParseUint(rid, 10, 64); err != nil {
		return Snapshot{}, fmt.Errorf("qbittorrent delta: invalid RID cursor")
	}

	query := url.Values{"rid": []string{rid}}
	response, err := client.authenticatedRequest(ctx, http.MethodGet, "api/v2/sync/maindata", query, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer closeResponse(response)
	if response.StatusCode != http.StatusOK {
		return Snapshot{}, unexpectedStatus("qbittorrent maindata", response.StatusCode)
	}

	var mainData struct {
		RID             uint64                     `json:"rid"`
		FullUpdate      bool                       `json:"full_update"`
		Torrents        map[string]json.RawMessage `json:"torrents"`
		TorrentsRemoved []string                   `json:"torrents_removed"`
	}
	if err := decodeJSONResponse(response, &mainData, "qbittorrent maindata"); err != nil {
		return Snapshot{}, err
	}

	changedSet := make(map[string]struct{}, len(mainData.Torrents))
	for hash := range mainData.Torrents {
		normalized, err := normalizeStableHash(hash)
		if err != nil {
			return Snapshot{}, errors.New("qbittorrent maindata: invalid torrent hash")
		}
		changedSet[normalized] = struct{}{}
	}
	changed := make([]string, 0, len(changedSet))
	for hash := range changedSet {
		changed = append(changed, hash)
	}
	sort.Strings(changed)

	var torrents []Torrent
	if len(changed) != 0 {
		torrents, err = client.fetchTorrentInfo(ctx, changed)
		if err != nil {
			return Snapshot{}, err
		}
	}

	removedByHash := make(map[string]struct{}, len(mainData.TorrentsRemoved))
	for _, hash := range mainData.TorrentsRemoved {
		normalized, err := normalizeStableHash(hash)
		if err != nil {
			return Snapshot{}, errors.New("qbittorrent maindata: invalid removed torrent hash")
		}
		removedByHash[normalized] = struct{}{}
	}

	// A torrent can disappear between maindata and the single filtered info
	// request. Treat an omitted changed hash as removed so it cannot stay stale.
	returned := make(map[string]struct{}, len(torrents))
	for _, torrent := range torrents {
		returned[torrent.StableHash] = struct{}{}
	}
	for _, hash := range changed {
		if _, ok := returned[hash]; !ok {
			removedByHash[hash] = struct{}{}
		}
	}

	removedHashes := make([]string, 0, len(removedByHash))
	for hash := range removedByHash {
		removedHashes = append(removedHashes, hash)
	}
	sort.Strings(removedHashes)
	removed := make([]RemovedTorrent, 0, len(removedHashes))
	for _, hash := range removedHashes {
		removed = append(removed, RemovedTorrent{StableHash: hash})
	}

	return Snapshot{
		Full:     mainData.FullUpdate,
		Cursor:   strconv.FormatUint(mainData.RID, 10),
		Torrents: torrents,
		Removed:  removed,
	}, nil
}

func (client *qbittorrentClient) Delete(ctx context.Context, torrent TorrentRef, deleteFiles bool) error {
	hash, err := normalizeStableHash(torrent.StableHash)
	if err != nil {
		return fmt.Errorf("qbittorrent delete: %w", err)
	}
	form := url.Values{
		"hashes":      []string{hash},
		"deleteFiles": []string{strconv.FormatBool(deleteFiles)},
	}
	response, err := client.authenticatedRequest(ctx, http.MethodPost, "api/v2/torrents/delete", nil, form)
	if err != nil {
		return err
	}
	defer closeResponse(response)
	if response.StatusCode != http.StatusOK {
		return unexpectedStatus("qbittorrent delete", response.StatusCode)
	}
	return nil
}

func (client *qbittorrentClient) SelectedFileSizes(ctx context.Context, torrent TorrentRef) ([]int64, error) {
	hash, err := normalizeStableHash(torrent.StableHash)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent files: %w", err)
	}
	response, err := client.authenticatedRequest(ctx, http.MethodGet, "api/v2/torrents/files", url.Values{
		"hash": []string{hash},
	}, nil)
	if err != nil {
		return nil, err
	}
	defer closeResponse(response)
	if response.StatusCode != http.StatusOK {
		return nil, unexpectedStatus("qbittorrent files", response.StatusCode)
	}
	var files []struct {
		Size     int64 `json:"size"`
		Priority int   `json:"priority"`
	}
	if err := decodeJSONResponse(response, &files, "qbittorrent files"); err != nil {
		return nil, err
	}
	sizes := make([]int64, 0, len(files))
	for _, file := range files {
		if file.Priority == 0 {
			continue
		}
		sizes = append(sizes, maxInt64(file.Size, 0))
	}
	return sizes, nil
}

func (client *qbittorrentClient) fetchTorrentInfo(ctx context.Context, hashes []string) ([]Torrent, error) {
	query := url.Values{"includeTrackers": []string{"true"}}
	if len(hashes) != 0 {
		query.Set("hashes", strings.Join(hashes, "|"))
	}
	response, err := client.authenticatedRequest(ctx, http.MethodGet, "api/v2/torrents/info", query, nil)
	if err != nil {
		return nil, err
	}
	defer closeResponse(response)
	if response.StatusCode != http.StatusOK {
		return nil, unexpectedStatus("qbittorrent torrent info", response.StatusCode)
	}

	var raw []qbittorrentTorrent
	if err := decodeJSONResponse(response, &raw, "qbittorrent torrent info"); err != nil {
		return nil, err
	}
	torrents := make([]Torrent, 0, len(raw))
	for _, item := range raw {
		torrent, err := item.toTorrent()
		if err != nil {
			return nil, err
		}
		torrents = append(torrents, torrent)
	}
	sort.Slice(torrents, func(i, j int) bool { return torrents[i].StableHash < torrents[j].StableHash })
	return torrents, nil
}

type qbittorrentTorrent struct {
	Hash          string          `json:"hash"`
	Name          string          `json:"name"`
	SavePath      string          `json:"save_path"`
	ContentPath   string          `json:"content_path"`
	Size          int64           `json:"size"`
	State         string          `json:"state"`
	Progress      float64         `json:"progress"`
	Ratio         float64         `json:"ratio"`
	Uploaded      int64           `json:"uploaded"`
	Downloaded    int64           `json:"downloaded"`
	UploadSpeed   int64           `json:"upspeed"`
	DownloadSpeed int64           `json:"dlspeed"`
	AddedOn       int64           `json:"added_on"`
	Tracker       string          `json:"tracker"`
	Trackers      json.RawMessage `json:"trackers"`
}

func (raw qbittorrentTorrent) toTorrent() (Torrent, error) {
	hash, err := normalizeStableHash(raw.Hash)
	if err != nil {
		return Torrent{}, errors.New("qbittorrent torrent info: missing stable torrent hash")
	}
	trackers, err := decodeQBittorrentTrackers(raw.Trackers)
	if err != nil {
		return Torrent{}, err
	}
	trackers = appendUniqueNonEmpty(trackers, raw.Tracker)
	addedAt, err := parseTorrentAddedAt(raw.AddedOn)
	if err != nil {
		return Torrent{}, errors.New("qbittorrent torrent info: invalid added time")
	}
	contentPath := raw.ContentPath
	if contentPath == "" {
		contentPath = joinRemotePath(raw.SavePath, raw.Name)
	}
	return Torrent{
		StableHash:      hash,
		Name:            raw.Name,
		SavePath:        raw.SavePath,
		ContentPath:     contentPath,
		WantedBytes:     maxInt64(raw.Size, 0),
		State:           raw.State,
		Progress:        raw.Progress,
		Ratio:           raw.Ratio,
		UploadedBytes:   maxInt64(raw.Uploaded, 0),
		DownloadedBytes: maxInt64(raw.Downloaded, 0),
		UploadSpeed:     maxInt64(raw.UploadSpeed, 0),
		DownloadSpeed:   maxInt64(raw.DownloadSpeed, 0),
		AddedAt:         addedAt,
		TrackerURLs:     trackers,
	}, nil
}

func decodeQBittorrentTrackers(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values []any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("qbittorrent torrent info: invalid tracker list")
	}
	var trackers []string
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			trackers = appendUniqueNonEmpty(trackers, typed)
		case map[string]any:
			if trackerURL, ok := typed["url"].(string); ok {
				trackers = appendUniqueNonEmpty(trackers, trackerURL)
			}
		}
	}
	return trackers, nil
}

func appendUniqueNonEmpty(values []string, candidates ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(candidates))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		values = append(values, candidate)
	}
	return values
}

func (client *qbittorrentClient) authenticatedRequest(
	ctx context.Context,
	method string,
	endpoint string,
	query url.Values,
	form url.Values,
) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := client.login(ctx); err != nil {
			return nil, err
		}
		response, err := client.request(ctx, method, endpoint, query, form)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusForbidden {
			return response, nil
		}
		closeResponse(response)
		client.authMu.Lock()
		client.loggedIn = false
		client.authMu.Unlock()
	}
	return nil, fmt.Errorf("qbittorrent request: %w", ErrAuthentication)
}

func (client *qbittorrentClient) login(ctx context.Context) error {
	client.authMu.Lock()
	defer client.authMu.Unlock()
	if client.loggedIn {
		return nil
	}

	form := url.Values{"username": []string{client.username}, "password": []string{client.password}}
	response, err := client.request(ctx, http.MethodPost, "api/v2/auth/login", nil, form)
	if err != nil {
		return err
	}
	defer closeResponse(response)
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("qbittorrent login: %w", ErrAuthentication)
	}
	if response.StatusCode != http.StatusOK {
		return unexpectedStatus("qbittorrent login", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return fmt.Errorf("qbittorrent login: read response: %w", err)
	}
	if strings.TrimSpace(string(body)) != "Ok." {
		return fmt.Errorf("qbittorrent login: %w", ErrAuthentication)
	}
	client.loggedIn = true
	return nil
}

func (client *qbittorrentClient) request(
	ctx context.Context,
	method string,
	endpoint string,
	query url.Values,
	form url.Values,
) (*http.Response, error) {
	requestURL, err := url.Parse(appendURLPath(client.baseURL, endpoint))
	if err != nil {
		return nil, fmt.Errorf("qbittorrent request: build endpoint: %w", err)
	}
	if len(query) != 0 {
		requestURL.RawQuery = query.Encode()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent request: create request: %w", err)
	}
	request.Header.Set("Accept", "application/json, text/plain")
	request.Header.Set("Origin", originFor(client.baseURL))
	request.Header.Set("Referer", originFor(client.baseURL)+"/")
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := client.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("qbittorrent request failed: %w", err)
	}
	return response, nil
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}
