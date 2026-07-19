package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

	for _, target := range []string{
		"/?sort_by=updated_at&sort_order=desc",
		"/?sort_by=name&sort_order=sideways",
		"/?sort_order=asc",
	} {
		response := httptest.NewRecorder()
		server.listGroups(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, body = %s", target, response.Code, response.Body.String())
		}
	}
}
