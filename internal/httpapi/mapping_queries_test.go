package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lesir831/SeedGraph/internal/store"
)

func TestTrackerMappingsAndIYUUSitesHandlersPageAndFilter(t *testing.T) {
	database, err := store.Open(context.Background(), t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if err := database.ApplyIYUUCatalog(ctx, []store.IYUUSiteInput{
		{RemoteID: 1, Slug: "alpha", Nickname: "Alpha PT", BaseURL: "alpha.example"},
		{RemoteID: 2, Slug: "beta", Nickname: "Beta PT", BaseURL: "beta.example"},
		{RemoteID: 3, Slug: "gamma", Nickname: "Gamma PT", BaseURL: "gamma.example"},
	}, time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	downloader, err := database.CreateDownloader(ctx, store.CreateDownloaderParams{
		Name: "Transmission", Kind: "transmission", BaseURL: "http://tr:9091",
		StorageName: "media", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	records := []store.TorrentRecord{
		httpTrackerRecord(downloader, "mapped", "alpha.example"),
		httpTrackerRecord(downloader, "unmapped", "unknown.invalid"),
	}
	if _, err := database.ApplySync(ctx, store.ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: records,
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: database}

	mappingsResponse := httptest.NewRecorder()
	mappingsRequest := httptest.NewRequest(
		http.MethodGet,
		"/?q=alpha&status=mapped&match_type=exact&limit=1&offset=0",
		nil,
	)
	server.listTrackerMappings(mappingsResponse, mappingsRequest)
	if mappingsResponse.Code != http.StatusOK {
		t.Fatalf("mapping list status = %d, body = %s", mappingsResponse.Code, mappingsResponse.Body.String())
	}
	var mappingsPayload struct {
		Data struct {
			Items  []store.TrackerMapping `json:"items"`
			Total  int                    `json:"total"`
			Limit  int                    `json:"limit"`
			Offset int                    `json:"offset"`
		} `json:"data"`
	}
	if err := json.Unmarshal(mappingsResponse.Body.Bytes(), &mappingsPayload); err != nil {
		t.Fatal(err)
	}
	if mappingsPayload.Data.Total != 1 || mappingsPayload.Data.Limit != 1 ||
		mappingsPayload.Data.Offset != 0 || len(mappingsPayload.Data.Items) != 1 ||
		mappingsPayload.Data.Items[0].HostIdentity != "alpha.example" {
		t.Fatalf("mapping list payload = %+v", mappingsPayload.Data)
	}

	sitesResponse := httptest.NewRecorder()
	sitesRequest := httptest.NewRequest(
		http.MethodGet,
		"/?q=example&status=unmapped&limit=1&offset=1",
		nil,
	)
	server.listSites(sitesResponse, sitesRequest)
	if sitesResponse.Code != http.StatusOK {
		t.Fatalf("IYUU list status = %d, body = %s", sitesResponse.Code, sitesResponse.Body.String())
	}
	var sitesPayload struct {
		Data struct {
			Items   []store.IYUUSite    `json:"items"`
			State   store.IYUUSyncState `json:"state"`
			Total   int                 `json:"total"`
			Limit   int                 `json:"limit"`
			Offset  int                 `json:"offset"`
			Running bool                `json:"running"`
		} `json:"data"`
	}
	if err := json.Unmarshal(sitesResponse.Body.Bytes(), &sitesPayload); err != nil {
		t.Fatal(err)
	}
	if sitesPayload.Data.Total != 2 || sitesPayload.Data.Limit != 1 ||
		sitesPayload.Data.Offset != 1 || len(sitesPayload.Data.Items) != 1 ||
		sitesPayload.Data.Items[0].Slug != "gamma" || sitesPayload.Data.Items[0].Mapped ||
		sitesPayload.Data.State.SiteCount != 3 || sitesPayload.Data.Running {
		t.Fatalf("IYUU list payload = %+v", sitesPayload.Data)
	}
}

func TestMappingHandlersRejectInvalidQueries(t *testing.T) {
	server := &Server{}
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		target  string
	}{
		{name: "bad mapping status", handler: server.listTrackerMappings, target: "/?status=yes"},
		{name: "bad match type", handler: server.listTrackerMappings, target: "/?match_type=fuzzy"},
		{name: "bad mapping limit", handler: server.listTrackerMappings, target: "/?limit=0"},
		{name: "bad IYUU status", handler: server.listSites, target: "/?status=yes"},
		{name: "bad IYUU offset", handler: server.listSites, target: "/?offset=-1"},
		{name: "oversized search", handler: server.listSites, target: "/?q=" + strings.Repeat("a", 201)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, httptest.NewRequest(http.MethodGet, test.target, nil))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_query") {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func httpTrackerRecord(downloader store.Downloader, hash, trackerHost string) store.TorrentRecord {
	return store.TorrentRecord{
		ID: hash + "-instance", DownloaderID: downloader.ID, StableHashKey: hash,
		RemoteID: hash, Name: hash, SourcePath: "/downloads/" + hash,
		CanonicalPath: "/media/" + hash, StorageID: downloader.StorageID, WantedBytes: 1,
		MetadataFingerprint: "metadata-" + hash, ManifestFingerprint: "manifest-" + hash,
		ContentGroupID: "content-" + hash, ContentGroupAutoKey: "content-key-" + hash,
		DataGroupID: "data-" + hash, DataGroupAutoKey: "data-key-" + hash,
		Confidence: "verified", Runtime: store.RuntimeRecord{Status: "seeding", Progress: 1},
		Trackers: []store.TrackerRecord{{HostIdentity: trackerHost, PathHint: "/announce"}},
	}
}
