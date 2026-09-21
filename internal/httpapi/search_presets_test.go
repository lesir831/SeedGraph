package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lesir831/SeedGraph/internal/store"
)

func TestSearchPresetRoutes(t *testing.T) {
	handler := testHandler(t)
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"correct-horse"}`)))
	var session struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login: %d", login.Code)
	}
	call := func(method, path, body string, authenticated, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/search-presets"+path, strings.NewReader(body))
		if authenticated {
			r.AddCookie(login.Result().Cookies()[0])
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", session.Data.CSRF)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "", "", false, false); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	body := `{"name":"Movies","filter":{"version":1,"root":{"type":"group","combinator":"and","children":[{"type":"condition","field":"path","operator":"contains","value":"/Movies/"}]}}}`
	if w := call("POST", "", body, true, false); w.Code != 403 {
		t.Fatalf("CSRF: %d", w.Code)
	}
	created := call("POST", "", body, true, true)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var item struct {
		Data store.GroupSearchPreset `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "", body, true, true); w.Code != 409 {
		t.Fatalf("duplicate: %d", w.Code)
	}
	for _, invalid := range []string{`{"name":"empty"}`, strings.Replace(body, `"path"`, `"raw_sql"`, 1)} {
		if w := call("POST", "", invalid, true, true); w.Code != 400 {
			t.Fatalf("invalid: %d %s", w.Code, w.Body.String())
		}
	}
	listed := call("GET", "", "", true, false)
	var list struct {
		Data []store.GroupSearchPreset `json:"data"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if listed.Code != 200 || len(list.Data) != 1 || list.Data[0].ID != item.Data.ID {
		t.Fatalf("list: %s", listed.Body.String())
	}
	if w := call("DELETE", "/"+item.Data.ID, "", true, false); w.Code != 403 {
		t.Fatalf("delete CSRF: %d", w.Code)
	}
	if w := call("DELETE", "/"+item.Data.ID, "", true, true); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := call("DELETE", "/"+item.Data.ID, "", true, true); w.Code != 404 {
		t.Fatalf("missing: %d", w.Code)
	}
}
