package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransmissionSessionNegotiationAndVersion(t *testing.T) {
	t.Parallel()
	const (
		username = "transmission-user"
		password = "transmission-password"
		session  = "csrf-session"
	)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/transmission/rpc" {
			t.Errorf("RPC path = %q", request.URL.Path)
		}
		if request.Header.Get(transmissionSessionHeader) != session {
			writer.Header().Set(transmissionSessionHeader, session)
			writer.WriteHeader(http.StatusConflict)
			return
		}
		gotUsername, gotPassword, ok := request.BasicAuth()
		if !ok || gotUsername != username || gotPassword != password {
			t.Error("missing or incorrect Basic authentication")
		}
		rpcRequest := decodeRPCRequest(t, request)
		if rpcRequest.JSONRPC != "2.0" || rpcRequest.Method != "session_get" {
			t.Fatalf("request = %+v", rpcRequest)
		}
		var fields []string
		if err := json.Unmarshal(rpcRequest.Params["fields"], &fields); err != nil {
			t.Fatalf("decode fields: %v", err)
		}
		if !slices.Equal(fields, []string{"version", "rpc_version_semver"}) {
			t.Fatalf("fields = %#v", fields)
		}
		writeRPCResult(t, writer, rpcRequest.ID, map[string]any{
			"version": "4.1.1 (38c164933e)", "rpc_version_semver": "6.0.1",
		})
	}))
	defer server.Close()

	client, err := New(Config{
		Kind: KindTransmission, BaseURL: server.URL, Username: username, Password: password,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	version, err := client.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != "4.1.1 (38c164933e)" || requests.Load() != 2 {
		t.Fatalf("version = %q, requests = %d", version, requests.Load())
	}
}

func TestTransmissionFullSnapshotParsesTableAndWantedFiles(t *testing.T) {
	t.Parallel()
	const trackerURL = "https://tracker.example/announce?passkey=return-but-never-log"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rpcRequest := decodeRPCRequest(t, request)
		if rpcRequest.Method != "torrent_get" {
			t.Fatalf("method = %q", rpcRequest.Method)
		}
		var format string
		if err := json.Unmarshal(rpcRequest.Params["format"], &format); err != nil || format != "table" {
			t.Fatalf("format = %q, %v", format, err)
		}
		if _, present := rpcRequest.Params["ids"]; present {
			t.Error("full snapshot unexpectedly sent ids")
		}
		var fields []string
		if err := json.Unmarshal(rpcRequest.Params["fields"], &fields); err != nil {
			t.Fatalf("decode fields: %v", err)
		}
		for _, required := range []string{"hash_string", "added_date", "files", "wanted", "tracker_stats"} {
			if !slices.Contains(fields, required) {
				t.Errorf("missing field %q in %#v", required, fields)
			}
		}

		header := []string{
			"tracker_stats", "wanted", "files", "status", "rate_download", "rate_upload",
			"downloaded_ever", "uploaded_ever", "upload_ratio", "percent_complete", "total_size",
			"download_dir", "name", "hash_string", "id", "added_date",
		}
		row := []any{
			[]map[string]any{{"announce": trackerURL, "host": "tracker.example"}},
			[]bool{true, false, true},
			[]map[string]any{{"name": "a", "length": 10}, {"name": "b", "length": 20}, {"name": "c", "length": 30}},
			4, 222, 111, 400, 500, 1.5, 0.75, 60, "/downloads", "Linux ISO", strings.ToUpper(testHashA), 42, 1712345678,
		}
		writeRPCResult(t, writer, rpcRequest.ID, map[string]any{"torrents": []any{header, row}})
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindTransmission, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	snapshot, err := client.FullSnapshot(context.Background())
	if err != nil {
		t.Fatalf("FullSnapshot: %v", err)
	}
	if !snapshot.Full || len(snapshot.Torrents) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	torrent := snapshot.Torrents[0]
	if torrent.StableHash != testHashA || torrent.RemoteID != "42" {
		t.Fatalf("identity = %#v", torrent.Ref())
	}
	if !torrent.SelectedFilesKnown || torrent.SelectedFileCount != 2 || torrent.WantedBytes != 40 {
		t.Fatalf("selected files = %+v", torrent)
	}
	if !slices.Equal(torrent.SelectedFileSizes, []int64{10, 30}) {
		t.Fatalf("selected sizes = %#v", torrent.SelectedFileSizes)
	}
	if torrent.ContentPath != "/downloads/Linux ISO" || torrent.State != "downloading" {
		t.Fatalf("path/state = %+v", torrent)
	}
	if !torrent.AddedAt.Equal(time.Unix(1712345678, 0).UTC()) {
		t.Fatalf("added time = %s", torrent.AddedAt)
	}
	if !slices.Equal(torrent.TrackerURLs, []string{trackerURL}) {
		t.Fatalf("trackers = %#v", torrent.TrackerURLs)
	}
}

func TestTransmissionDeltaUsesRecentlyActiveAndRemovedRemoteIDs(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rpcRequest := decodeRPCRequest(t, request)
		var ids string
		if err := json.Unmarshal(rpcRequest.Params["ids"], &ids); err != nil || ids != "recently_active" {
			t.Fatalf("ids = %q, %v", ids, err)
		}
		header, row := transmissionTestTable(testHashB, 7)
		writeRPCResult(t, writer, rpcRequest.ID, map[string]any{
			"torrents": []any{header, row},
			"removed":  []int{11, 3},
		})
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindTransmission, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	snapshot, err := client.Delta(context.Background(), "opaque-cursor")
	if err != nil {
		t.Fatalf("Delta: %v", err)
	}
	if snapshot.Full || snapshot.Cursor != "opaque-cursor" || len(snapshot.Torrents) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	wantRemoved := []RemovedTorrent{{RemoteID: "11"}, {RemoteID: "3"}}
	if !slices.Equal(snapshot.Removed, wantRemoved) {
		t.Fatalf("removed = %#v, want %#v", snapshot.Removed, wantRemoved)
	}
}

func TestTransmissionDeleteUsesStableHashAndDeletionFlag(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var observed []struct {
		IDs         []string
		DeleteLocal bool
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rpcRequest := decodeRPCRequest(t, request)
		if rpcRequest.Method != "torrent_remove" {
			t.Fatalf("method = %q", rpcRequest.Method)
		}
		var ids []string
		var deleteLocal bool
		if err := json.Unmarshal(rpcRequest.Params["ids"], &ids); err != nil {
			t.Fatalf("decode ids: %v", err)
		}
		if err := json.Unmarshal(rpcRequest.Params["delete_local_data"], &deleteLocal); err != nil {
			t.Fatalf("decode delete_local_data: %v", err)
		}
		mu.Lock()
		observed = append(observed, struct {
			IDs         []string
			DeleteLocal bool
		}{IDs: ids, DeleteLocal: deleteLocal})
		mu.Unlock()
		writeRPCResult(t, writer, rpcRequest.ID, map[string]any{})
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindTransmission, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, deleteFiles := range []bool{false, true} {
		if err := client.Delete(context.Background(), TorrentRef{
			StableHash: strings.ToUpper(testHashA), RemoteID: "999",
		}, deleteFiles); err != nil {
			t.Fatalf("Delete(%t): %v", deleteFiles, err)
		}
	}
	if len(observed) != 2 {
		t.Fatalf("observed %d deletes", len(observed))
	}
	for index, item := range observed {
		if !slices.Equal(item.IDs, []string{testHashA}) {
			t.Fatalf("delete %d used IDs %#v; numeric RemoteID must not be identity", index, item.IDs)
		}
		if item.DeleteLocal != (index == 1) {
			t.Fatalf("delete %d flag = %t", index, item.DeleteLocal)
		}
	}
}

func TestTransmissionRemoteErrorsAreRedacted(t *testing.T) {
	t.Parallel()
	const (
		password   = "rpc-password-must-not-leak"
		trackerURL = "https://tracker.example/announce?passkey=rpc-must-not-leak"
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rpcRequest := decodeRPCRequest(t, request)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      rpcRequest.ID,
			"error": map[string]any{
				"code":    7,
				"message": "backend rejected " + trackerURL,
				"data":    map[string]any{"error_string": password},
			},
		})
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindTransmission, BaseURL: server.URL, Username: "rpc", Password: password})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Version(context.Background())
	if err == nil || !strings.Contains(err.Error(), "remote error code 7") {
		t.Fatalf("Version error = %v", err)
	}
	for _, secret := range []string{password, trackerURL, "passkey=rpc-must-not-leak"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked secret %q: %v", secret, err)
		}
	}
}

func TestTransmissionNeverFallsBackToNumericIDForIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rpcRequest := decodeRPCRequest(t, request)
		header, row := transmissionTestTable("", 91)
		writeRPCResult(t, writer, rpcRequest.ID, map[string]any{"torrents": []any{header, row}})
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindTransmission, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.FullSnapshot(context.Background())
	if !errors.Is(err, ErrInvalidStableHash) && (err == nil || !strings.Contains(err.Error(), "stable torrent hash")) {
		t.Fatalf("FullSnapshot error = %v", err)
	}
}

func TestTransmissionRejectsNegativeAddedTime(t *testing.T) {
	t.Parallel()
	header, row := transmissionTestTable(testHashA, 1)
	for index, field := range header {
		if field == "added_date" {
			row[index] = -1
		}
	}
	raw, err := json.Marshal([]any{header, row})
	if err != nil {
		t.Fatal(err)
	}
	_, err = decodeTransmissionTorrents(raw)
	if err == nil || !strings.Contains(err.Error(), "invalid added time") {
		t.Fatalf("decodeTransmissionTorrents() error = %v", err)
	}
}

func TestTransmissionTreatsUnencodableAddedTimeAsUnknown(t *testing.T) {
	t.Parallel()
	header, row := transmissionTestTable(testHashA, 1)
	for index, field := range header {
		if field == "added_date" {
			row[index] = maxJSONUnixTimestamp + 1
		}
	}
	raw, err := json.Marshal([]any{header, row})
	if err != nil {
		t.Fatal(err)
	}
	torrents, err := decodeTransmissionTorrents(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(torrents) != 1 || !torrents[0].AddedAt.IsZero() {
		t.Fatalf("torrents = %+v, want one torrent with unknown added time", torrents)
	}
}

type testRPCRequest struct {
	JSONRPC string
	Method  string
	Params  map[string]json.RawMessage
	ID      uint64
}

func decodeRPCRequest(t *testing.T, request *http.Request) testRPCRequest {
	t.Helper()
	if request.Method != http.MethodPost {
		t.Fatalf("RPC method = %s", request.Method)
	}
	if request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", request.Header.Get("Content-Type"))
	}
	var value struct {
		JSONRPC string                     `json:"jsonrpc"`
		Method  string                     `json:"method"`
		Params  map[string]json.RawMessage `json:"params"`
		ID      uint64                     `json:"id"`
	}
	if err := json.NewDecoder(request.Body).Decode(&value); err != nil {
		t.Fatalf("decode RPC request: %v", err)
	}
	return testRPCRequest(value)
}

func writeRPCResult(t *testing.T, writer http.ResponseWriter, id uint64, result any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"jsonrpc": "2.0", "id": id, "result": result,
	}); err != nil {
		t.Fatalf("encode RPC response: %v", err)
	}
}

func transmissionTestTable(hash string, id int) ([]string, []any) {
	header := []string{
		"id", "hash_string", "name", "download_dir", "total_size", "percent_complete", "upload_ratio",
		"uploaded_ever", "downloaded_ever", "rate_upload", "rate_download", "status", "added_date", "files", "wanted", "tracker_stats",
	}
	row := []any{
		id, hash, "Torrent", "/data", 5, 1.0, 2.0,
		2, 5, 0, 0, 6, 1712345678, []map[string]any{{"length": 5}}, []bool{true}, []map[string]any{},
	}
	return header, row
}
