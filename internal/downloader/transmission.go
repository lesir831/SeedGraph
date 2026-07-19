package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const transmissionSessionHeader = "X-Transmission-Session-Id"

var transmissionTorrentFields = []string{
	"id",
	"hash_string",
	"name",
	"download_dir",
	"total_size",
	"percent_complete",
	"upload_ratio",
	"uploaded_ever",
	"downloaded_ever",
	"rate_upload",
	"rate_download",
	"status",
	"added_date",
	"files",
	"wanted",
	"tracker_stats",
}

type transmissionClient struct {
	rpcURL   string
	client   *http.Client
	username string
	password string

	nextID  atomic.Uint64
	session struct {
		sync.RWMutex
		id string
	}
}

var _ Client = (*transmissionClient)(nil)

func newTransmission(config Config) (*transmissionClient, error) {
	baseURL, err := parseBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	client, err := cloneHTTPClient(config.HTTPClient, false)
	if err != nil {
		return nil, err
	}
	return &transmissionClient{
		rpcURL:   transmissionEndpoint(baseURL),
		client:   client,
		username: config.Username,
		password: config.Password,
	}, nil
}

func transmissionEndpoint(base *url.URL) string {
	path := strings.TrimRight(base.Path, "/")
	if !strings.HasSuffix(path, "/transmission/rpc") {
		path += "/transmission/rpc"
	}
	copy := *base
	copy.Path = path
	copy.RawPath = ""
	return copy.String()
}

func (client *transmissionClient) Version(ctx context.Context) (string, error) {
	var result struct {
		Version          string `json:"version"`
		RPCVersionSemver string `json:"rpc_version_semver"`
	}
	if err := client.rpc(ctx, "session_get", map[string]any{
		"fields": []string{"version", "rpc_version_semver"},
	}, &result); err != nil {
		return "", err
	}
	if strings.TrimSpace(result.Version) == "" {
		return "", errors.New("transmission session_get: empty version")
	}
	return result.Version, nil
}

func (client *transmissionClient) FullSnapshot(ctx context.Context) (Snapshot, error) {
	result, err := client.getTorrents(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Full: true, Torrents: result.torrents}, nil
}

func (client *transmissionClient) Delta(ctx context.Context, cursor string) (Snapshot, error) {
	result, err := client.getTorrents(ctx, "recently_active")
	if err != nil {
		return Snapshot{}, err
	}
	removed := make([]RemovedTorrent, 0, len(result.removedIDs))
	for _, remoteID := range result.removedIDs {
		removed = append(removed, RemovedTorrent{RemoteID: remoteID})
	}
	return Snapshot{
		Full:     false,
		Cursor:   cursor,
		Torrents: result.torrents,
		Removed:  removed,
	}, nil
}

func (client *transmissionClient) Delete(ctx context.Context, torrent TorrentRef, deleteFiles bool) error {
	hash, err := normalizeStableHash(torrent.StableHash)
	if err != nil {
		return fmt.Errorf("transmission torrent_remove: %w", err)
	}
	return client.rpc(ctx, "torrent_remove", map[string]any{
		"ids":               []string{hash},
		"delete_local_data": deleteFiles,
	}, nil)
}

type transmissionTorrentResult struct {
	torrents   []Torrent
	removedIDs []string
}

func (client *transmissionClient) getTorrents(ctx context.Context, ids any) (transmissionTorrentResult, error) {
	params := map[string]any{
		"fields": transmissionTorrentFields,
		"format": "table",
	}
	if ids != nil {
		params["ids"] = ids
	}
	var result struct {
		Torrents json.RawMessage   `json:"torrents"`
		Removed  []json.RawMessage `json:"removed"`
	}
	if err := client.rpc(ctx, "torrent_get", params, &result); err != nil {
		return transmissionTorrentResult{}, err
	}
	torrents, err := decodeTransmissionTorrents(result.Torrents)
	if err != nil {
		return transmissionTorrentResult{}, err
	}
	removedIDs := make([]string, 0, len(result.Removed))
	for _, rawID := range result.Removed {
		id, err := rawRemoteID(rawID)
		if err != nil {
			return transmissionTorrentResult{}, errors.New("transmission torrent_get: invalid removed torrent ID")
		}
		removedIDs = append(removedIDs, id)
	}
	sort.Strings(removedIDs)
	return transmissionTorrentResult{torrents: torrents, removedIDs: removedIDs}, nil
}

type transmissionRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
	ID      uint64 `json:"id"`
}

type transmissionRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code int `json:"code"`
	} `json:"error"`
	ID uint64 `json:"id"`
}

func (client *transmissionClient) rpc(ctx context.Context, method string, params any, destination any) error {
	requestID := client.nextID.Add(1)
	payload, err := json.Marshal(transmissionRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      requestID,
	})
	if err != nil {
		return fmt.Errorf("transmission %s: encode request: %w", method, err)
	}

	for attempt := 0; attempt < 3; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.rpcURL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("transmission %s: create request: %w", method, err)
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/json")
		client.session.RLock()
		sessionID := client.session.id
		client.session.RUnlock()
		if sessionID != "" {
			request.Header.Set(transmissionSessionHeader, sessionID)
		}
		if client.username != "" || client.password != "" {
			request.SetBasicAuth(client.username, client.password)
		}

		response, err := client.client.Do(request)
		if err != nil {
			return fmt.Errorf("transmission %s request failed: %w", method, err)
		}
		if response.StatusCode == http.StatusConflict {
			newSessionID := strings.TrimSpace(response.Header.Get(transmissionSessionHeader))
			closeResponse(response)
			if newSessionID == "" {
				return fmt.Errorf("transmission %s: 409 response missing session ID", method)
			}
			client.session.Lock()
			client.session.id = newSessionID
			client.session.Unlock()
			continue
		}
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			closeResponse(response)
			return fmt.Errorf("transmission %s: %w", method, ErrAuthentication)
		}
		if response.StatusCode != http.StatusOK {
			status := response.StatusCode
			closeResponse(response)
			return unexpectedStatus("transmission "+method, status)
		}

		var envelope transmissionRPCResponse
		decodeErr := decodeJSONResponse(response, &envelope, "transmission "+method)
		closeResponse(response)
		if decodeErr != nil {
			return decodeErr
		}
		if envelope.JSONRPC != "2.0" || envelope.ID != requestID {
			return fmt.Errorf("transmission %s: invalid JSON-RPC response envelope", method)
		}
		if envelope.Error != nil {
			// Remote messages/data can contain tracker URLs or paths. Keep only the
			// protocol error code in the returned error.
			return fmt.Errorf("transmission %s: remote error code %d", method, envelope.Error.Code)
		}
		if destination == nil || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(envelope.Result, destination); err != nil {
			return fmt.Errorf("transmission %s: invalid result shape", method)
		}
		return nil
	}
	return fmt.Errorf("transmission %s: session negotiation did not converge", method)
}

func decodeTransmissionTorrents(raw json.RawMessage) ([]Torrent, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, errors.New("transmission torrent_get: invalid torrent table")
	}
	if len(rows) == 0 {
		return nil, nil
	}

	var records []map[string]json.RawMessage
	first := strings.TrimSpace(string(rows[0]))
	if strings.HasPrefix(first, "{") {
		records = make([]map[string]json.RawMessage, 0, len(rows))
		for _, row := range rows {
			var record map[string]json.RawMessage
			if err := json.Unmarshal(row, &record); err != nil {
				return nil, errors.New("transmission torrent_get: invalid torrent object")
			}
			records = append(records, record)
		}
	} else {
		var columns []string
		if err := json.Unmarshal(rows[0], &columns); err != nil || len(columns) == 0 {
			return nil, errors.New("transmission torrent_get: invalid table header")
		}
		seenColumns := make(map[string]struct{}, len(columns))
		for _, column := range columns {
			if _, exists := seenColumns[column]; exists {
				return nil, errors.New("transmission torrent_get: duplicate table column")
			}
			seenColumns[column] = struct{}{}
		}
		records = make([]map[string]json.RawMessage, 0, len(rows)-1)
		for _, row := range rows[1:] {
			var values []json.RawMessage
			if err := json.Unmarshal(row, &values); err != nil || len(values) != len(columns) {
				return nil, errors.New("transmission torrent_get: invalid table row")
			}
			record := make(map[string]json.RawMessage, len(columns))
			for index, column := range columns {
				record[column] = values[index]
			}
			records = append(records, record)
		}
	}

	torrents := make([]Torrent, 0, len(records))
	for _, record := range records {
		torrent, err := transmissionRecordToTorrent(record)
		if err != nil {
			return nil, err
		}
		torrents = append(torrents, torrent)
	}
	sort.Slice(torrents, func(i, j int) bool { return torrents[i].StableHash < torrents[j].StableHash })
	return torrents, nil
}

func transmissionRecordToTorrent(record map[string]json.RawMessage) (Torrent, error) {
	hashValue, err := rawString(record["hash_string"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid stable torrent hash")
	}
	hash, err := normalizeStableHash(hashValue)
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: missing stable torrent hash")
	}
	remoteID, err := rawRemoteID(record["id"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid remote torrent ID")
	}
	name, err := rawString(record["name"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid torrent name")
	}
	downloadDir, err := rawString(record["download_dir"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid download directory")
	}
	totalSize, err := rawInt64(record["total_size"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid total size")
	}
	progress, err := rawFloat64(record["percent_complete"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid progress")
	}
	ratio, err := rawFloat64(record["upload_ratio"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid ratio")
	}
	uploaded, err := rawInt64(record["uploaded_ever"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid uploaded bytes")
	}
	downloaded, err := rawInt64(record["downloaded_ever"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid downloaded bytes")
	}
	uploadSpeed, err := rawInt64(record["rate_upload"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid upload speed")
	}
	downloadSpeed, err := rawInt64(record["rate_download"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid download speed")
	}
	status, err := rawInt64(record["status"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid status")
	}
	addedDate, err := rawInt64(record["added_date"])
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid added time")
	}
	addedAt, err := parseTorrentAddedAt(addedDate)
	if err != nil {
		return Torrent{}, errors.New("transmission torrent_get: invalid added time")
	}

	selectedSizes, selectedKnown, err := transmissionSelectedFileSizes(record["files"], record["wanted"])
	if err != nil {
		return Torrent{}, err
	}
	wantedBytes := maxInt64(totalSize, 0)
	if selectedKnown {
		wantedBytes = 0
		for _, size := range selectedSizes {
			if size > math.MaxInt64-wantedBytes {
				return Torrent{}, errors.New("transmission torrent_get: selected file sizes overflow")
			}
			wantedBytes += size
		}
	}
	trackerURLs, err := transmissionTrackerURLs(record["tracker_stats"])
	if err != nil {
		return Torrent{}, err
	}

	return Torrent{
		StableHash:         hash,
		RemoteID:           remoteID,
		Name:               name,
		SavePath:           downloadDir,
		ContentPath:        joinRemotePath(downloadDir, name),
		WantedBytes:        wantedBytes,
		SelectedFilesKnown: selectedKnown,
		SelectedFileCount:  len(selectedSizes),
		SelectedFileSizes:  selectedSizes,
		State:              transmissionStatus(status),
		Progress:           progress,
		Ratio:              ratio,
		UploadedBytes:      maxInt64(uploaded, 0),
		DownloadedBytes:    maxInt64(downloaded, 0),
		UploadSpeed:        maxInt64(uploadSpeed, 0),
		DownloadSpeed:      maxInt64(downloadSpeed, 0),
		AddedAt:            addedAt,
		TrackerURLs:        trackerURLs,
	}, nil
}

func transmissionSelectedFileSizes(filesRaw, wantedRaw json.RawMessage) ([]int64, bool, error) {
	var files []struct {
		Length int64 `json:"length"`
	}
	if err := json.Unmarshal(filesRaw, &files); err != nil {
		return nil, false, errors.New("transmission torrent_get: invalid files list")
	}
	var wanted []bool
	if err := json.Unmarshal(wantedRaw, &wanted); err != nil {
		return nil, false, errors.New("transmission torrent_get: invalid wanted list")
	}
	if len(files) != len(wanted) {
		return nil, false, errors.New("transmission torrent_get: files and wanted lengths differ")
	}
	selected := make([]int64, 0, len(files))
	for index, file := range files {
		if file.Length < 0 {
			return nil, false, errors.New("transmission torrent_get: negative file size")
		}
		if wanted[index] {
			selected = append(selected, file.Length)
		}
	}
	return selected, true, nil
}

func transmissionTrackerURLs(raw json.RawMessage) ([]string, error) {
	var trackers []struct {
		Announce string `json:"announce"`
	}
	if err := json.Unmarshal(raw, &trackers); err != nil {
		return nil, errors.New("transmission torrent_get: invalid tracker_stats")
	}
	result := make([]string, 0, len(trackers))
	for _, tracker := range trackers {
		result = appendUniqueNonEmpty(result, tracker.Announce)
	}
	return result, nil
}

func rawString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("missing string")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func rawRemoteID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("missing ID")
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		value, err := strconv.ParseInt(number.String(), 10, 64)
		if err == nil && value >= 0 {
			return strconv.FormatInt(value, 10), nil
		}
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		parsed, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr == nil && parsed >= 0 {
			return strconv.FormatInt(parsed, 10), nil
		}
	}
	return "", errors.New("invalid ID")
}

func rawInt64(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing integer")
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}
	return strconv.ParseInt(number.String(), 10, 64)
}

func rawFloat64(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing number")
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(number.String(), 64)
}

func transmissionStatus(value int64) string {
	switch value {
	case 0:
		return "stopped"
	case 1:
		return "check_wait"
	case 2:
		return "checking"
	case 3:
		return "download_wait"
	case 4:
		return "downloading"
	case 5:
		return "seed_wait"
	case 6:
		return "seeding"
	default:
		return "unknown"
	}
}

func joinRemotePath(directory, name string) string {
	if directory == "" {
		return name
	}
	if name == "" || strings.HasSuffix(directory, "/") || strings.HasSuffix(directory, "\\") {
		return directory + name
	}
	separator := "/"
	if strings.Contains(directory, "\\") && !strings.Contains(directory, "/") {
		separator = "\\"
	}
	return directory + separator + name
}
