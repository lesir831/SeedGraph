package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/lesir831/SeedGraph/internal/iyuu"
	"github.com/lesir831/SeedGraph/internal/store"
)

type catalogStub struct {
	sites []iyuu.Site
	err   error
}

func (stub catalogStub) Sites(context.Context) ([]iyuu.Site, error) {
	return append([]iyuu.Site(nil), stub.sites...), stub.err
}

func TestIYUUSyncAndListHandlers(t *testing.T) {
	database, err := store.Open(context.Background(), t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service, err := iyuu.NewService(catalogStub{sites: []iyuu.Site{{
		ID: 1, Site: "alpha", Nickname: "Alpha", BaseURL: "alpha.example", IsHTTPS: 2,
	}}}, database, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{store: database, iyuu: service}

	syncResponse := httptest.NewRecorder()
	server.syncIYUUSites(syncResponse, httptest.NewRequest(http.MethodPost, "/", nil))
	if syncResponse.Code != http.StatusOK {
		t.Fatalf("sync status = %d, body = %s", syncResponse.Code, syncResponse.Body.String())
	}

	listResponse := httptest.NewRecorder()
	server.listSites(listResponse, httptest.NewRequest(http.MethodGet, "/", nil))
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listResponse.Code, listResponse.Body.String())
	}
	var payload struct {
		Data struct {
			Items []store.IYUUSite    `json:"items"`
			State store.IYUUSyncState `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Items) != 1 || payload.Data.Items[0].Slug != "alpha" || payload.Data.State.SiteCount != 1 {
		t.Fatalf("unexpected list payload: %+v", payload.Data)
	}

	secondResponse := httptest.NewRecorder()
	server.syncIYUUSites(secondResponse, httptest.NewRequest(http.MethodPost, "/", nil))
	if secondResponse.Code != http.StatusTooManyRequests || secondResponse.Header().Get("Retry-After") == "" {
		t.Fatalf("cooldown status = %d, headers = %v", secondResponse.Code, secondResponse.Header())
	}
}

func TestIYUUSyncMapsUpstreamErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		status     int
		code       string
		retryAfter bool
	}{
		{
			name:       "HTTP rate limit",
			err:        &iyuu.HTTPError{StatusCode: http.StatusTooManyRequests},
			status:     http.StatusTooManyRequests,
			code:       "iyuu_rate_limited",
			retryAfter: true,
		},
		{
			name:       "application rate limit",
			err:        &iyuu.APIError{Code: http.StatusTooManyRequests, Message: "slow down"},
			status:     http.StatusTooManyRequests,
			code:       "iyuu_rate_limited",
			retryAfter: true,
		},
		{
			name:       "live style application rate limit",
			err:        &iyuu.APIError{Code: http.StatusBadRequest, Message: "访问频率过快"},
			status:     http.StatusTooManyRequests,
			code:       "iyuu_rate_limited",
			retryAfter: true,
		},
		{
			name:   "HTTP server error",
			err:    &iyuu.HTTPError{StatusCode: http.StatusBadGateway},
			status: http.StatusServiceUnavailable,
			code:   "iyuu_upstream_unavailable",
		},
		{
			name:   "application server error",
			err:    &iyuu.APIError{Code: http.StatusInternalServerError, Message: "failed"},
			status: http.StatusServiceUnavailable,
			code:   "iyuu_upstream_unavailable",
		},
		{
			name:   "application timeout",
			err:    &iyuu.APIError{Code: http.StatusRequestTimeout, Message: "timeout"},
			status: http.StatusServiceUnavailable,
			code:   "iyuu_upstream_unavailable",
		},
		{
			name:   "HTTP client error",
			err:    &iyuu.HTTPError{StatusCode: http.StatusNotFound},
			status: http.StatusBadGateway,
			code:   "iyuu_upstream_error",
		},
		{
			name:   "application error",
			err:    &iyuu.APIError{Code: http.StatusUnauthorized, Message: "denied"},
			status: http.StatusBadGateway,
			code:   "iyuu_upstream_error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, err := store.Open(context.Background(), t.TempDir()+"/seedgraph.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			service, err := iyuu.NewService(catalogStub{err: test.err}, database, nil, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			server := &Server{store: database, iyuu: service}

			response := httptest.NewRecorder()
			server.syncIYUUSites(response, httptest.NewRequest(http.MethodPost, "/", nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.status, response.Body.String())
			}
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Error.Code != test.code {
				t.Fatalf("error code = %q, want %q", payload.Error.Code, test.code)
			}
			gotRetryAfter := response.Header().Get("Retry-After")
			if test.retryAfter {
				seconds, err := strconv.Atoi(gotRetryAfter)
				if err != nil || seconds <= 0 {
					t.Fatalf("Retry-After = %q, want positive delta seconds", gotRetryAfter)
				}
			} else if gotRetryAfter != "" {
				t.Fatalf("Retry-After = %q, want empty", gotRetryAfter)
			}
		})
	}
}
