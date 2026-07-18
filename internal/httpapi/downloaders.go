package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lesir831/SeedGraph/internal/domain"
	"github.com/lesir831/SeedGraph/internal/store"
)

func (s *Server) listDownloaders(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDownloaders(r.Context())
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, items)
}

func (s *Server) createDownloader(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name         string `json:"name"`
		Kind         string `json:"kind"`
		BaseURL      string `json:"base_url"`
		Username     string `json:"username"`
		Password     string `json:"password"`
		StorageID    string `json:"storage_id"`
		StorageName  string `json:"storage_name"`
		PathMappings []struct {
			SourcePrefix string `json:"source_prefix"`
			TargetPrefix string `json:"target_prefix"`
		} `json:"path_mappings"`
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	request.BaseURL = strings.TrimSpace(request.BaseURL)
	parsedURL, err := url.Parse(request.BaseURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.User != nil || parsedURL.Fragment != "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_base_url", "下载器地址必须是不含凭据和片段的 HTTP(S) URL")
		return
	}
	usernameCiphertext, err := s.cipher.Encrypt([]byte(request.Username))
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	passwordCiphertext, err := s.cipher.Encrypt([]byte(request.Password))
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	pathMappings := make([]store.PathMapping, 0, len(request.PathMappings))
	for _, mapping := range request.PathMappings {
		if _, err := domain.CanonicalizeContentPath("validate", mapping.SourcePrefix); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_path_mapping", "路径映射源必须是绝对路径")
			return
		}
		if _, err := domain.CanonicalizeContentPath("validate", mapping.TargetPrefix); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_path_mapping", "路径映射目标必须是绝对路径")
			return
		}
		pathMappings = append(pathMappings, store.PathMapping{
			SourcePrefix: mapping.SourcePrefix, TargetPrefix: mapping.TargetPrefix,
		})
	}
	item, err := s.store.CreateDownloader(r.Context(), store.CreateDownloaderParams{
		Name: request.Name, Kind: request.Kind, BaseURL: strings.TrimRight(request.BaseURL, "/"),
		UsernameCiphertext: usernameCiphertext, PasswordCiphertext: passwordCiphertext,
		StorageID: request.StorageID, StorageName: request.StorageName,
		PathMappings: pathMappings, Enabled: request.Enabled,
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_downloader", err.Error())
		return
	}
	_ = s.store.AddAuditEvent(r.Context(), store.AuditEvent{
		Actor: "admin", Action: "downloader.create", TargetType: "downloader", TargetID: item.ID,
		Details: map[string]any{"kind": item.Kind, "name": item.Name},
	})
	writeData(w, http.StatusCreated, item)
}

func (s *Server) deleteDownloader(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteDownloader(r.Context(), id); err != nil {
		s.handleError(w, r, err)
		return
	}
	_ = s.store.AddAuditEvent(r.Context(), store.AuditEvent{
		Actor: "admin", Action: "downloader.delete", TargetType: "downloader", TargetID: id,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testDownloader(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	version, err := s.syncer.TestConnection(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeData(w, http.StatusOK, map[string]any{
			"ok": false, "latency_ms": time.Since(started).Milliseconds(), "message": "连接失败",
		})
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"ok": true, "latency_ms": time.Since(started).Milliseconds(), "version": version, "message": "连接成功",
	})
}

func (s *Server) syncDownloader(w http.ResponseWriter, r *http.Request) {
	if err := s.syncer.Trigger(chi.URLParam(r, "id"), false); err != nil {
		s.handleError(w, r, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"status": "running", "running": true})
}
