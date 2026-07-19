package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testHashC = "cccccccccccccccccccccccccccccccccccccccc"
)

func TestQBittorrentLoginVersionAndFullSnapshot(t *testing.T) {
	t.Parallel()
	const (
		username   = "reader"
		password   = "correct horse battery staple"
		trackerURL = "https://tracker.example/announce?passkey=do-not-log"
	)
	var loginCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/auth/login":
			loginCount.Add(1)
			if request.Method != http.MethodPost {
				t.Errorf("login method = %s", request.Method)
			}
			if got := request.Header.Get("Origin"); got != serverOrigin(request) {
				t.Errorf("Origin = %q, want %q", got, serverOrigin(request))
			}
			if got := request.Header.Get("Referer"); got != serverOrigin(request)+"/" {
				t.Errorf("Referer = %q", got)
			}
			if err := request.ParseForm(); err != nil {
				t.Fatalf("parse login form: %v", err)
			}
			if request.Form.Get("username") != username || request.Form.Get("password") != password {
				t.Error("login credentials were not form encoded correctly")
			}
			http.SetCookie(writer, &http.Cookie{Name: "SID", Value: "session-one", Path: "/"})
			_, _ = writer.Write([]byte("Ok."))
		case "/api/v2/app/version":
			requireQBittorrentCookie(t, request, "session-one")
			_, _ = writer.Write([]byte("v5.1.2\n"))
		case "/api/v2/torrents/info":
			requireQBittorrentCookie(t, request, "session-one")
			if request.URL.Query().Get("includeTrackers") != "true" {
				t.Errorf("includeTrackers = %q", request.URL.Query().Get("includeTrackers"))
			}
			if request.URL.Query().Has("hashes") {
				t.Errorf("full list unexpectedly filtered by hashes: %q", request.URL.RawQuery)
			}
			_ = json.NewEncoder(writer).Encode([]map[string]any{
				{
					"hash": testHashA, "name": "Ubuntu", "save_path": "/data",
					"size": 120, "progress": 0.5, "ratio": 1.25, "uploaded": 80, "downloaded": 60,
					"upspeed": 7, "dlspeed": 9, "state": "downloading", "added_on": 1712345678,
					"trackers": []any{map[string]any{"url": trackerURL}, "udp://public.example:80/announce"},
				},
			})
		case "/api/v2/torrents/files":
			requireQBittorrentCookie(t, request, "session-one")
			if request.URL.Query().Get("hash") != testHashA {
				t.Errorf("files hash = %q", request.URL.Query().Get("hash"))
			}
			_ = json.NewEncoder(writer).Encode([]map[string]any{
				{"size": 10, "priority": 1},
				{"size": 20, "priority": 0},
				{"size": 30, "priority": 7},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(Config{
		Kind: KindQBittorrent, BaseURL: server.URL, Username: username, Password: password,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	version, err := client.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != "v5.1.2" {
		t.Fatalf("version = %q", version)
	}

	snapshot, err := client.FullSnapshot(context.Background())
	if err != nil {
		t.Fatalf("FullSnapshot: %v", err)
	}
	if loginCount.Load() != 1 {
		t.Fatalf("login count = %d, want 1", loginCount.Load())
	}
	if !snapshot.Full || len(snapshot.Torrents) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	torrent := snapshot.Torrents[0]
	if torrent.StableHash != testHashA || torrent.RemoteID != "" {
		t.Fatalf("torrent identity = %#v", torrent.Ref())
	}
	if torrent.WantedBytes != 120 || torrent.SelectedFilesKnown || torrent.SelectedFileCount != 0 {
		t.Fatalf("selection metadata = %+v", torrent)
	}
	if torrent.ContentPath != "/data/Ubuntu" || torrent.State != "downloading" || torrent.Progress != 0.5 {
		t.Fatalf("torrent fields = %+v", torrent)
	}
	if !torrent.AddedAt.Equal(time.Unix(1712345678, 0).UTC()) {
		t.Fatalf("added time = %s", torrent.AddedAt)
	}
	if !slices.Equal(torrent.TrackerURLs, []string{trackerURL, "udp://public.example:80/announce"}) {
		t.Fatalf("trackers = %#v", torrent.TrackerURLs)
	}
	sizes, err := client.(FileManifestClient).SelectedFileSizes(context.Background(), torrent.Ref())
	if err != nil {
		t.Fatalf("SelectedFileSizes: %v", err)
	}
	if !slices.Equal(sizes, []int64{10, 30}) {
		t.Fatalf("selected file sizes = %#v", sizes)
	}
}

func TestQBittorrentDeltaFetchesChangedHashesOnce(t *testing.T) {
	t.Parallel()
	var infoCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(writer, &http.Cookie{Name: "SID", Value: "delta-session", Path: "/"})
			_, _ = writer.Write([]byte("Ok."))
		case "/api/v2/sync/maindata":
			if request.URL.Query().Get("rid") != "7" {
				t.Errorf("rid = %q", request.URL.Query().Get("rid"))
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"rid": 8, "full_update": false,
				"torrents":         map[string]any{testHashB: map[string]any{"state": "pausedUP"}, testHashA: map[string]any{"state": "downloading"}},
				"torrents_removed": []string{testHashC},
			})
		case "/api/v2/torrents/info":
			infoCalls.Add(1)
			if request.URL.Query().Get("includeTrackers") != "true" {
				t.Errorf("includeTrackers = %q", request.URL.Query().Get("includeTrackers"))
			}
			if request.URL.Query().Get("hashes") != testHashA+"|"+testHashB {
				t.Errorf("hash filter = %q", request.URL.Query().Get("hashes"))
			}
			_ = json.NewEncoder(writer).Encode([]map[string]any{
				qbittorrentFixture(testHashB, "B"),
				qbittorrentFixture(strings.ToUpper(testHashA), "A"),
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindQBittorrent, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	snapshot, err := client.Delta(context.Background(), "7")
	if err != nil {
		t.Fatalf("Delta: %v", err)
	}
	if infoCalls.Load() != 1 {
		t.Fatalf("filtered info calls = %d, want 1", infoCalls.Load())
	}
	if snapshot.Full || snapshot.Cursor != "8" || len(snapshot.Torrents) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Torrents[0].StableHash != testHashA || snapshot.Torrents[1].StableHash != testHashB {
		t.Fatalf("torrent ordering/identity = %#v", snapshot.Torrents)
	}
	if len(snapshot.Removed) != 1 || snapshot.Removed[0].StableHash != testHashC || snapshot.Removed[0].RemoteID != "" {
		t.Fatalf("removed = %#v", snapshot.Removed)
	}
}

func TestQBittorrentDeleteFlags(t *testing.T) {
	t.Parallel()
	var deleteFiles []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(writer, &http.Cookie{Name: "SID", Value: "delete-session", Path: "/"})
			_, _ = writer.Write([]byte("Ok."))
		case "/api/v2/torrents/delete":
			if request.Method != http.MethodPost {
				t.Errorf("delete method = %s", request.Method)
			}
			if err := request.ParseForm(); err != nil {
				t.Fatalf("parse delete form: %v", err)
			}
			if request.Form.Get("hashes") != testHashA {
				t.Errorf("hashes = %q", request.Form.Get("hashes"))
			}
			deleteFiles = append(deleteFiles, request.Form.Get("deleteFiles"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindQBittorrent, BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, value := range []bool{false, true} {
		if err := client.Delete(context.Background(), TorrentRef{StableHash: strings.ToUpper(testHashA)}, value); err != nil {
			t.Fatalf("Delete(%t): %v", value, err)
		}
	}
	if !slices.Equal(deleteFiles, []string{"false", "true"}) {
		t.Fatalf("deleteFiles = %#v", deleteFiles)
	}
}

func TestQBittorrentErrorsRedactRemoteBodyAndCredentials(t *testing.T) {
	t.Parallel()
	const (
		password   = "password-that-must-not-leak"
		trackerURL = "https://tracker.example/announce?passkey=must-not-leak"
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("Fails. " + password + " " + trackerURL))
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindQBittorrent, BaseURL: server.URL, Username: "admin", Password: password})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Version(context.Background())
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("Version error = %v", err)
	}
	for _, secret := range []string{password, trackerURL, "passkey=must-not-leak"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked secret %q: %v", secret, err)
		}
	}
}

func TestQBittorrentRejectsNegativeAddedTime(t *testing.T) {
	t.Parallel()
	_, err := (qbittorrentTorrent{Hash: testHashA, AddedOn: -1}).toTorrent()
	if err == nil || !strings.Contains(err.Error(), "invalid added time") {
		t.Fatalf("toTorrent() error = %v", err)
	}
}

func TestQBittorrentTreatsUnencodableAddedTimeAsUnknown(t *testing.T) {
	t.Parallel()
	torrent, err := (qbittorrentTorrent{Hash: testHashA, AddedOn: maxJSONUnixTimestamp + 1}).toTorrent()
	if err != nil {
		t.Fatal(err)
	}
	if !torrent.AddedAt.IsZero() {
		t.Fatalf("added time = %s, want unknown", torrent.AddedAt)
	}
}

func qbittorrentFixture(hash, name string) map[string]any {
	return map[string]any{
		"hash": hash, "name": name, "save_path": "/data", "content_path": "/data/" + name,
		"size": 10, "progress": 1.0, "ratio": 0.0, "uploaded": 0, "downloaded": 10,
		"upspeed": 0, "dlspeed": 0, "state": "pausedUP", "trackers": []any{},
	}
}

func requireQBittorrentCookie(t *testing.T, request *http.Request, expected string) {
	t.Helper()
	cookie, err := request.Cookie("SID")
	if err != nil || cookie.Value != expected {
		t.Fatalf("SID cookie = %#v, %v", cookie, err)
	}
}

func serverOrigin(request *http.Request) string {
	return "http://" + request.Host
}
