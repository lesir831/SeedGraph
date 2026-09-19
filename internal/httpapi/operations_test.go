package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lesir831/SeedGraph/internal/store"
)

func TestAuditHistoryPaginationAndFilters(t *testing.T) {
	handler, database := testHandlerWithStore(t)
	for index := 0; index < 205; index++ {
		status := "success"
		if index%2 == 0 {
			status = "warning"
		}
		if err := database.AddAuditEvent(context.Background(), store.AuditEvent{
			ID: fmt.Sprintf("event-%03d", index), Action: "delete.uncertain", Status: status,
			TargetType: "delete_job", TargetID: "job", Details: map[string]any{"message": "complete details"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"admin","password":"correct-horse"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d", login.Code)
	}
	for _, tc := range []struct {
		query string
		total int
		count int
		first string
	}{
		{"limit=20&offset=200", 205, 5, "event-004"},
		{"action=delete.uncertain&status=warning&limit=20&offset=100", 103, 3, "event-004"},
		{"action=delete.completed&limit=20", 0, 0, ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events?"+tc.query, nil)
			request.AddCookie(login.Result().Cookies()[0])
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			var payload struct {
				Data struct {
					Items []store.AuditEvent `json:"items"`
					Total int                `json:"total"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || payload.Data.Total != tc.total || len(payload.Data.Items) != tc.count {
				t.Fatalf("unexpected page: %d %s", response.Code, response.Body.String())
			}
			if tc.count > 0 && (payload.Data.Items[0].ID != tc.first || payload.Data.Items[0].Details["message"] != "complete details") {
				t.Fatalf("wrong order or missing details: %+v", payload.Data.Items[0])
			}
		})
	}
}
