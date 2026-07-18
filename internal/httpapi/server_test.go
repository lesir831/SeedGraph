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
	return server.Handler(nil)
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
