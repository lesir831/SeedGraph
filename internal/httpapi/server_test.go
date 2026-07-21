package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lesir831/SeedGraph/internal/auth"
	"github.com/lesir831/SeedGraph/internal/cryptox"
	"github.com/lesir831/SeedGraph/internal/deletion"
	"github.com/lesir831/SeedGraph/internal/store"
	"github.com/lesir831/SeedGraph/internal/syncer"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, _ := testHandlerWithStore(t)
	return handler
}

func testHandlerWithStore(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	hash, _ := auth.HashPassword("correct-horse")
	if err := database.EnsureAdmin(ctx, hash); err != nil {
		t.Fatal(err)
	}
	cipher, _ := cryptox.New([]byte(strings.Repeat("s", 32)))
	syncService := syncer.New(database, cipher, nil, time.Minute, time.Hour)
	deleteService := deletion.New(database, syncService, nil, 5*time.Minute)
	server, err := New(Options{
		Store: database, Cipher: cipher, Sessions: auth.NewSessionManager(cipher, time.Hour),
		Syncer: syncService, Deletions: deleteService, StaleAfter: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler(nil), database
}

func TestHealthAndSessionProtection(t *testing.T) {
	handler := testHandler(t)
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", health.Code, health.Body.String())
	}

	protected := httptest.NewRecorder()
	handler.ServeHTTP(protected, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if protected.Code != http.StatusUnauthorized {
		t.Fatalf("protected status = %d, want 401", protected.Code)
	}
}

func TestLoginCSRFAndCredentialRedaction(t *testing.T) {
	handler := testHandler(t)
	login := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin","password":"correct-horse"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(login, request)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	var payload struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.CSRFToken == "" || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login did not return session material: %s", login.Body.String())
	}
	cookie := login.Result().Cookies()[0]

	withoutCSRF := httptest.NewRecorder()
	createBody := `{"name":"qB","kind":"qbittorrent","base_url":"http://qb:8080","username":"user","password":"top-secret-password","storage_name":"media","path_mappings":[],"enabled":true}`
	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/downloaders", bytes.NewBufferString(createBody))
	createRequest.AddCookie(cookie)
	createRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(withoutCSRF, createRequest)
	if withoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("request without CSRF status = %d, want 403", withoutCSRF.Code)
	}

	created := httptest.NewRecorder()
	createRequest = httptest.NewRequest(http.MethodPost, "/api/v1/downloaders", bytes.NewBufferString(createBody))
	createRequest.AddCookie(cookie)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("X-CSRF-Token", payload.Data.CSRFToken)
	handler.ServeHTTP(created, createRequest)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "top-secret-password") || strings.Contains(created.Body.String(), "ciphertext") {
		t.Fatalf("downloader response leaked credentials: %s", created.Body.String())
	}
}

func TestUnmappedTrackerIdentitiesRouteIsProtectedAndRedacted(t *testing.T) {
	handler, database := testHandlerWithStore(t)
	ctx := context.Background()
	downloader, err := database.CreateDownloader(ctx, store.CreateDownloaderParams{
		Name: "Transmission", Kind: "transmission", BaseURL: "http://tr:9091",
		StorageName: "media", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostSecret := "a91f3c7e5b2d8046a91f3c7e5b2d8046"
	rawTracker := "https://user:password@" + hostSecret + ".tracker.example.com/announce/path-secret?passkey=query-secret"
	host, pathHint, err := store.TrackerIdentity(rawTracker)
	if err != nil {
		t.Fatal(err)
	}
	record := store.TorrentRecord{
		ID: "tracker-instance", DownloaderID: downloader.ID, StableHashKey: "tracker-hash",
		RemoteID: "1", Name: "Tracker test", SourcePath: "/downloads/test",
		CanonicalPath: "/media/test", StorageID: downloader.StorageID, WantedBytes: 1,
		MetadataFingerprint: "tracker-metadata", ManifestFingerprint: "tracker-manifest",
		ContentGroupID: "tracker-group", ContentGroupAutoKey: "tracker-group-key",
		DataGroupID: "tracker-data", DataGroupAutoKey: "tracker-data-key", Confidence: "verified",
		Trackers: []store.TrackerRecord{{HostIdentity: host, PathHint: pathHint}},
	}
	if _, err := database.ApplySync(ctx, store.ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []store.TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate a row written by an older version, before host/path redaction was
	// enforced at the store boundary. Both tracker APIs must still redact it.
	if _, err := database.DB().Exec(`
		UPDATE torrent_trackers SET host_identity = ?, path_hint = ? WHERE instance_id = ?`,
		hostSecret+".tracker.example.com", "/announce/path-secret?passkey=query-secret", record.ID,
	); err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/tracker-rules/unmapped", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unmapped route without session status = %d, want 401", unauthorized.Code)
	}

	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		bytes.NewBufferString(`{"username":"admin","password":"correct-horse"}`),
	)
	loginRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	var sessionPayload struct {
		Data struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &sessionPayload); err != nil {
		t.Fatal(err)
	}
	if sessionPayload.Data.CSRFToken == "" {
		t.Fatalf("login did not return CSRF token: %s", login.Body.String())
	}
	cookie := login.Result().Cookies()[0]

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/tracker-rules/unmapped", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unmapped route status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []store.UnmappedTrackerIdentity `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].HostIdentity != "_redacted.tracker.example.com" ||
		payload.Data[0].PathHint != "/announce/*" || payload.Data[0].InstanceCount != 1 ||
		payload.Data[0].GroupCount != 1 || payload.Data[0].LastSeenAt.IsZero() {
		t.Fatalf("unexpected unmapped response: %+v", payload.Data)
	}
	body := response.Body.String()
	for _, secret := range []string{rawTracker, hostSecret, "user", "password", "path-secret", "query-secret", "passkey"} {
		if strings.Contains(body, secret) {
			t.Fatalf("unmapped route leaked %q: %s", secret, body)
		}
	}

	groupResponse := httptest.NewRecorder()
	groupRequest := httptest.NewRequest(http.MethodGet, "/api/v1/torrent-groups/tracker-group", nil)
	groupRequest.AddCookie(cookie)
	handler.ServeHTTP(groupResponse, groupRequest)
	if groupResponse.Code != http.StatusOK {
		t.Fatalf("torrent group status = %d, body = %s", groupResponse.Code, groupResponse.Body.String())
	}
	if !strings.Contains(groupResponse.Body.String(), "Unknown · _redacted.tracker.example.com") {
		t.Fatalf("torrent group omitted redacted tracker identity: %s", groupResponse.Body.String())
	}
	for _, secret := range []string{rawTracker, hostSecret, "path-secret", "query-secret", "passkey"} {
		if strings.Contains(groupResponse.Body.String(), secret) {
			t.Fatalf("torrent group route leaked %q: %s", secret, groupResponse.Body.String())
		}
	}
	siteOptionsResponse := httptest.NewRecorder()
	siteOptionsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/torrent-groups/site-options", nil)
	siteOptionsRequest.AddCookie(cookie)
	handler.ServeHTTP(siteOptionsResponse, siteOptionsRequest)
	if siteOptionsResponse.Code != http.StatusOK {
		t.Fatalf("torrent group site options status = %d, body = %s", siteOptionsResponse.Code, siteOptionsResponse.Body.String())
	}
	if !strings.Contains(siteOptionsResponse.Body.String(), `"key":"tracker:_redacted.tracker.example.com"`) ||
		!strings.Contains(siteOptionsResponse.Body.String(), `"label":"Unknown · _redacted.tracker.example.com"`) {
		t.Fatalf("torrent group site options omitted stable redacted tracker option: %s", siteOptionsResponse.Body.String())
	}
	for _, secret := range []string{rawTracker, hostSecret, "path-secret", "query-secret", "passkey"} {
		if strings.Contains(siteOptionsResponse.Body.String(), secret) {
			t.Fatalf("torrent group site options leaked %q: %s", secret, siteOptionsResponse.Body.String())
		}
	}
	// Startup migration canonicalizes old rows before rule mutations occur in a
	// real process. Restore that canonical state after the read-time legacy leak
	// checks so the remainder exercises exact placeholder reclassification.
	if _, err := database.DB().Exec(`
		UPDATE torrent_trackers SET host_identity = ?, path_hint = ? WHERE instance_id = ?`,
		payload.Data[0].HostIdentity, payload.Data[0].PathHint, record.ID,
	); err != nil {
		t.Fatal(err)
	}

	created := httptest.NewRecorder()
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/tracker-rules",
		bytes.NewBufferString(`{"host_pattern":"_redacted.tracker.example.com","path_prefix":"/announce","site_name":"tracker-test","display_name":"Tracker Test"}`),
	)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("X-CSRF-Token", sessionPayload.Data.CSRFToken)
	createRequest.AddCookie(cookie)
	handler.ServeHTTP(created, createRequest)
	if created.Code != http.StatusCreated {
		t.Fatalf("create tracker rule status = %d, body = %s", created.Code, created.Body.String())
	}
	var createdPayload struct {
		Data store.TrackerRule `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdPayload); err != nil {
		t.Fatal(err)
	}
	if createdPayload.Data.ID == "" {
		t.Fatalf("create tracker rule returned no ID: %s", created.Body.String())
	}

	afterCreate := httptest.NewRecorder()
	afterCreateRequest := httptest.NewRequest(http.MethodGet, "/api/v1/tracker-rules/unmapped", nil)
	afterCreateRequest.AddCookie(cookie)
	handler.ServeHTTP(afterCreate, afterCreateRequest)
	if afterCreate.Code != http.StatusOK {
		t.Fatalf("unmapped after create status = %d, body = %s", afterCreate.Code, afterCreate.Body.String())
	}
	var afterCreatePayload struct {
		Data []store.UnmappedTrackerIdentity `json:"data"`
	}
	if err := json.Unmarshal(afterCreate.Body.Bytes(), &afterCreatePayload); err != nil {
		t.Fatal(err)
	}
	if len(afterCreatePayload.Data) != 0 {
		t.Fatalf("new rule did not immediately remove unmapped tracker: %+v", afterCreatePayload.Data)
	}

	deleted := httptest.NewRecorder()
	deleteRequest := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/tracker-rules/"+createdPayload.Data.ID,
		nil,
	)
	deleteRequest.Header.Set("X-CSRF-Token", sessionPayload.Data.CSRFToken)
	deleteRequest.AddCookie(cookie)
	handler.ServeHTTP(deleted, deleteRequest)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete tracker rule status = %d, body = %s", deleted.Code, deleted.Body.String())
	}

	afterDelete := httptest.NewRecorder()
	afterDeleteRequest := httptest.NewRequest(http.MethodGet, "/api/v1/tracker-rules/unmapped", nil)
	afterDeleteRequest.AddCookie(cookie)
	handler.ServeHTTP(afterDelete, afterDeleteRequest)
	if afterDelete.Code != http.StatusOK {
		t.Fatalf("unmapped after delete status = %d, body = %s", afterDelete.Code, afterDelete.Body.String())
	}
	var afterDeletePayload struct {
		Data []store.UnmappedTrackerIdentity `json:"data"`
	}
	if err := json.Unmarshal(afterDelete.Body.Bytes(), &afterDeletePayload); err != nil {
		t.Fatal(err)
	}
	if len(afterDeletePayload.Data) != 1 || afterDeletePayload.Data[0].HostIdentity != "_redacted.tracker.example.com" {
		t.Fatalf("deleting rule did not immediately restore unmapped tracker: %+v", afterDeletePayload.Data)
	}
}
