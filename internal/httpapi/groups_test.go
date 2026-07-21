package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lesir831/SeedGraph/internal/store"
)

func requestWithURLParams(request *http.Request, params map[string]string) *http.Request {
	routeContext := chi.NewRouteContext()
	for key, value := range params {
		routeContext.URLParams.Add(key, value)
	}
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func TestMoveAndUndoGroupOperationHandlers(t *testing.T) {
	database, err := store.Open(context.Background(), t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	downloader, err := database.CreateDownloader(context.Background(), store.CreateDownloaderParams{
		Name: "Transmission", Kind: "transmission", BaseURL: "http://tr:9091",
		StorageName: "media", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	records := []store.TorrentRecord{
		{
			ID: "source-instance", DownloaderID: downloader.ID, StableHashKey: "source-hash",
			RemoteID: "1", Name: "Source", SourcePath: "/downloads/source",
			CanonicalPath: "/media/source", StorageID: downloader.StorageID, WantedBytes: 10,
			MetadataFingerprint: "source-metadata", ManifestFingerprint: "source-manifest",
			ContentGroupID: "source-group", ContentGroupAutoKey: "source-key",
			DataGroupID: "source-data", DataGroupAutoKey: "source-data-key", Confidence: "verified",
		},
		{
			ID: "target-instance", DownloaderID: downloader.ID, StableHashKey: "target-hash",
			RemoteID: "2", Name: "Target", SourcePath: "/downloads/target",
			CanonicalPath: "/media/target", StorageID: downloader.StorageID, WantedBytes: 20,
			MetadataFingerprint: "target-metadata", ManifestFingerprint: "target-manifest",
			ContentGroupID: "target-group", ContentGroupAutoKey: "target-key",
			DataGroupID: "target-data", DataGroupAutoKey: "target-data-key", Confidence: "verified",
		},
	}
	if _, err := database.ApplySync(context.Background(), store.ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: records,
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: database, logger: slog.Default()}

	moveRequest := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{
		"target_group_id":"target-group",
		"expected_source_version":1,
		"expected_target_version":1
	}`))
	moveRequest = requestWithURLParams(moveRequest, map[string]string{
		"id": "source-group", "instance_id": "source-instance",
	})
	moveResponse := httptest.NewRecorder()
	server.moveGroupMember(moveResponse, moveRequest)
	if moveResponse.Code != http.StatusOK {
		t.Fatalf("move status = %d, body = %s", moveResponse.Code, moveResponse.Body.String())
	}
	operationID := moveResponse.Header().Get("X-SeedGraph-Operation-ID")
	if operationID == "" {
		t.Fatal("move response omitted operation ID header")
	}
	var movePayload struct {
		Data store.TorrentGroupDetail `json:"data"`
	}
	if err := json.Unmarshal(moveResponse.Body.Bytes(), &movePayload); err != nil {
		t.Fatal(err)
	}
	if movePayload.Data.OperationID != operationID || movePayload.Data.ID != "target-group" {
		t.Fatalf("unexpected move response: %+v", movePayload.Data)
	}

	undoRequest := requestWithURLParams(
		httptest.NewRequest(http.MethodPost, "/", nil), map[string]string{"id": operationID},
	)
	undoResponse := httptest.NewRecorder()
	server.undoGroupOperation(undoResponse, undoRequest)
	if undoResponse.Code != http.StatusOK {
		t.Fatalf("undo status = %d, body = %s", undoResponse.Code, undoResponse.Body.String())
	}
	var undoPayload struct {
		Data store.UndoGroupOperationResult `json:"data"`
	}
	if err := json.Unmarshal(undoResponse.Body.Bytes(), &undoPayload); err != nil {
		t.Fatal(err)
	}
	if undoPayload.Data.OperationID != operationID || undoPayload.Data.OperationType != "move" {
		t.Fatalf("unexpected undo response: %+v", undoPayload.Data)
	}
}

func TestListGroupsValidatesSortQuery(t *testing.T) {
	database, err := store.Open(context.Background(), t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := &Server{store: database, logger: slog.Default()}

	validResponse := httptest.NewRecorder()
	server.listGroups(validResponse, httptest.NewRequest(http.MethodGet, "/?sort_by=name&sort_order=desc", nil))
	if validResponse.Code != http.StatusOK {
		t.Fatalf("valid sort status = %d, body = %s", validResponse.Code, validResponse.Body.String())
	}
	multiResponse := httptest.NewRecorder()
	server.listGroups(multiResponse, httptest.NewRequest(
		http.MethodGet,
		"/?sort=instance_count:desc&sort=oldest_added_at:desc&sort=size:asc"+
			"&name_contains=Show&site_all=site%3Asite-a&site_all=site%3Asite-b&site_none=site%3Asite-c"+
			"&size_lt=1073741824&oldest_added_gte=2026-01-01T00:00:00Z&oldest_added_lt=2026-02-01T00:00:00Z",
		nil,
	))
	if multiResponse.Code != http.StatusOK {
		t.Fatalf("valid advanced query status = %d, body = %s", multiResponse.Code, multiResponse.Body.String())
	}

	for _, target := range []string{
		"/?sort_by=updated_at&sort_order=desc",
		"/?sort_by=name&sort_order=sideways",
		"/?sort_order=asc",
		"/?sort=name:asc&sort_by=name",
		"/?sort=name",
		"/?sort=name:sideways",
		"/?sort=updated_at:desc",
		"/?sort=name:asc&sort=name:desc",
		"/?sort=name:asc&sort=size:asc&sort=oldest_added_at:asc&sort=instance_count:asc&sort=extra:asc",
		"/?site_all=site%3Aa&site_none=site%3Aa",
		"/?site_all=A",
		"/?site_all=unknown%3Aa",
		"/?site_all=site%3A",
		"/?site_all=tracker%3A",
		"/?site_all=tracker%3ATRACKER.EXAMPLE",
		"/?site_all=",
		"/?size_lt=0",
		"/?size_lt=not-a-number",
		"/?oldest_added_gte=not-a-time",
		"/?oldest_added_lt=not-a-time",
		"/?oldest_added_gte=2026-02-01T00:00:00Z&oldest_added_lt=2026-01-01T00:00:00Z",
	} {
		response := httptest.NewRecorder()
		server.listGroups(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, body = %s", target, response.Code, response.Body.String())
		}
	}

	optionsResponse := httptest.NewRecorder()
	server.listGroupSiteOptions(optionsResponse, httptest.NewRequest(http.MethodGet, "/", nil))
	if optionsResponse.Code != http.StatusOK {
		t.Fatalf("site options status = %d, body = %s", optionsResponse.Code, optionsResponse.Body.String())
	}
	var optionsPayload struct {
		Data []store.TorrentGroupSite `json:"data"`
	}
	if err := json.Unmarshal(optionsResponse.Body.Bytes(), &optionsPayload); err != nil {
		t.Fatal(err)
	}
	if optionsPayload.Data == nil || len(optionsPayload.Data) != 0 {
		t.Fatalf("empty site options = %+v, want non-nil empty list", optionsPayload.Data)
	}
}

func TestQueryGroupsAcceptsStructuredFilterSortsAndPagination(t *testing.T) {
	database, err := store.Open(context.Background(), t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := &Server{store: database, logger: slog.Default()}
	body := `{
		"filter":{
			"version":1,
			"root":{"type":"group","combinator":"and","children":[
				{"type":"condition","field":"size","operator":"gte","value":0},
				{"type":"group","scope":"instance","combinator":"or","negated":false,"children":[
					{"type":"condition","field":"state","operator":"in","value":["seeding","paused"]}
				]}
			]}
		},
		"q":"show",
		"status":"seeding",
		"downloader_id":"downloader-id",
		"sorts":[{"field":"instance_count","order":"desc"},{"field":"size","order":"asc"}],
		"limit":17,
		"offset":3
	}`
	response := httptest.NewRecorder()
	server.queryGroups(response, httptest.NewRequest(http.MethodPost, "/torrent-groups/query", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("query status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			Items  []store.TorrentGroup `json:"items"`
			Total  int                  `json:"total"`
			Limit  int                  `json:"limit"`
			Offset int                  `json:"offset"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.Items == nil || payload.Data.Total != 0 || payload.Data.Limit != 17 || payload.Data.Offset != 3 {
		t.Fatalf("unexpected query page: %+v", payload.Data)
	}
}

func TestQueryGroupsRejectsInvalidOrOversizedDocuments(t *testing.T) {
	database, err := store.Open(context.Background(), t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	server := &Server{store: database, logger: slog.Default()}

	tooDeep := `{"filter":{"version":1,"root":{"type":"group","combinator":"and","children":[` +
		`{"type":"group","combinator":"and","children":[` +
		`{"type":"group","combinator":"and","children":[` +
		`{"type":"group","combinator":"and","children":[` +
		`{"type":"condition","field":"group_name","operator":"eq","value":"x"}` +
		`]}]}]}]}}}`
	validFilter := `"filter":{"version":1,"root":{"type":"group","combinator":"and","children":[` +
		`{"type":"condition","field":"size","operator":"gte","value":0}]}}`
	tests := []string{
		``,
		`not-json`,
		`null`,
		`{}`,
		`{"filter":null}`,
		`{} {}`,
		`{"unknown":true}`,
		`{` + validFilter + `,"limit":201}`,
		`{` + validFilter + `,"offset":-1}`,
		`{` + validFilter + `,"sorts":[{"field":"private_hash","order":"asc"}]}`,
		`{` + validFilter + `,"sorts":[{"field":"name"}]}`,
		`{` + validFilter + `,"timezone":"Mars/Olympus_Mons"}`,
		`{"filter":{"version":2,"root":{"type":"group","combinator":"and","children":[]}}}`,
		`{"filter":{"version":1,"root":{"type":"group","combinator":"and","children":[],"sql":"1=1"}}}`,
		`{"filter":{"version":1,"root":{"type":"group","combinator":"and","children":[{"type":"condition","field":"private_hash","operator":"eq","value":"secret"}]}}}`,
		`{"filter":{"version":1,"root":{"type":"group","combinator":"and","children":[{"type":"condition","field":"size","operator":"gte","value":1.25}]}}}`,
		`{"filter":{"version":1,"root":{"type":"group","combinator":"and","children":[{"type":"condition","field":"locked","operator":"eq","value":"false"}]}}}`,
		tooDeep,
	}
	for _, body := range tests {
		response := httptest.NewRecorder()
		server.queryGroups(response, httptest.NewRequest(http.MethodPost, "/torrent-groups/query", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("POST body %q status = %d, body = %s", body, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "secret") {
			t.Fatalf("error response echoed filter value: %s", response.Body.String())
		}
	}

	oversized := `{"q":"` + strings.Repeat("x", maxTorrentGroupQueryRequestBytes) + `"}`
	response := httptest.NewRecorder()
	server.queryGroups(response, httptest.NewRequest(http.MethodPost, "/torrent-groups/query", strings.NewReader(oversized)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized query status = %d, body = %s", response.Code, response.Body.String())
	}
}
