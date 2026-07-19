package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/lesir831/SeedGraph/internal/auth"
	"github.com/lesir831/SeedGraph/internal/cryptox"
	"github.com/lesir831/SeedGraph/internal/deletion"
	"github.com/lesir831/SeedGraph/internal/iyuu"
	"github.com/lesir831/SeedGraph/internal/store"
	"github.com/lesir831/SeedGraph/internal/syncer"
)

const (
	sessionCookieName = "seedgraph_session"
	maxRequestBytes   = 1 << 20
)

type Server struct {
	store        *store.Store
	cipher       *cryptox.Cipher
	sessions     *auth.SessionManager
	syncer       *syncer.Service
	deletions    *deletion.Service
	iyuu         *iyuu.Service
	logger       *slog.Logger
	cookieSecure bool
	staleAfter   time.Duration
	loginLimiter *loginLimiter
}

type Options struct {
	Store        *store.Store
	Cipher       *cryptox.Cipher
	Sessions     *auth.SessionManager
	Syncer       *syncer.Service
	Deletions    *deletion.Service
	IYUU         *iyuu.Service
	Logger       *slog.Logger
	CookieSecure bool
	StaleAfter   time.Duration
}

func New(options Options) (*Server, error) {
	if options.Store == nil || options.Cipher == nil || options.Sessions == nil || options.Syncer == nil || options.Deletions == nil {
		return nil, errors.New("HTTP API dependencies are incomplete")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Server{
		store: options.Store, cipher: options.Cipher, sessions: options.Sessions,
		syncer: options.Syncer, deletions: options.Deletions, logger: options.Logger,
		iyuu:         options.IYUU,
		cookieSecure: options.CookieSecure, staleAfter: options.StaleAfter,
		loginLimiter: newLoginLimiter(5, time.Minute),
	}, nil
}

func (s *Server) Handler(frontend http.Handler) http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(60 * time.Second))
	router.Use(s.securityHeaders)
	router.Get("/healthz", s.health)
	router.Route("/api/v1", func(api chi.Router) {
		api.Post("/auth/login", s.login)
		api.Group(func(protected chi.Router) {
			protected.Use(s.requireSession)
			protected.Use(s.requireCSRF)
			protected.Get("/auth/session", s.session)
			protected.Post("/auth/logout", s.logout)
			protected.Get("/overview", s.overview)
			protected.Get("/torrent-groups", s.listGroups)
			protected.Get("/torrent-groups/{id}", s.getGroup)
			protected.Post("/torrent-groups/merge", s.mergeGroups)
			protected.Post("/torrent-groups/{id}/split", s.splitGroup)
			protected.Post("/torrent-groups/{id}/members/{instance_id}/move", s.moveGroupMember)
			protected.Patch("/torrent-groups/{id}/lock", s.lockGroup)
			protected.Post("/torrent-groups/{id}/restore-auto", s.restoreAutoGroup)
			protected.Post("/group-operations/{id}/undo", s.undoGroupOperation)
			protected.Get("/downloaders", s.listDownloaders)
			protected.Post("/downloaders", s.createDownloader)
			protected.Delete("/downloaders/{id}", s.deleteDownloader)
			protected.Post("/downloaders/{id}/test", s.testDownloader)
			protected.Post("/downloaders/{id}/sync", s.syncDownloader)
			protected.Get("/tracker-rules", s.listTrackerRules)
			protected.Get("/tracker-rules/unmapped", s.listUnmappedTrackerIdentities)
			protected.Post("/tracker-rules", s.createTrackerRule)
			protected.Delete("/tracker-rules/{id}", s.deleteTrackerRule)
			protected.Get("/sites", s.listSites)
			protected.Post("/sites/sync/iyuu", s.syncIYUUSites)
			protected.Get("/sync/status", s.syncStatus)
			protected.Post("/sync/run", s.runSync)
			protected.Get("/audit-events", s.auditEvents)
			protected.Post("/delete-plans", s.createDeletePlan)
			protected.Post("/delete-jobs", s.createDeleteJob)
			protected.Get("/delete-jobs/{id}", s.getDeleteJob)
		})
	})
	if frontend != nil {
		router.Handle("/*", frontend)
	}
	return router
}

type contextKey string

const sessionContextKey contextKey = "session"

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		session, err := s.sessions.Parse(cookie.Value)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "登录状态已失效")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey, session)))
	})
}

func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		session, ok := r.Context().Value(sessionContextKey).(auth.Session)
		provided := r.Header.Get("X-CSRF-Token")
		if !ok || len(provided) != len(session.CSRFToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(session.CSRFToken)) != 1 {
			writeAPIError(w, http.StatusForbidden, "csrf_failed", "请求校验失败，请刷新页面后重试")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.DB().PingContext(ctx); err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "database_unavailable", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func decodeBody(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func writeData(w http.ResponseWriter, status int, value any) {
	writeJSON(w, status, map[string]any{"data": value})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func (s *Server) handleError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, store.ErrVersionConflict), errors.Is(err, syncer.ErrAlreadyRunning),
		errors.Is(err, deletion.ErrPlanChanged):
		writeAPIError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, deletion.ErrPlanExpired):
		writeAPIError(w, http.StatusGone, "plan_expired", "删除预览已过期，请重新生成")
	case errors.Is(err, deletion.ErrPlanBlocked):
		writeAPIError(w, http.StatusUnprocessableEntity, "plan_blocked", "删除计划存在安全阻断")
	default:
		s.logger.Error("API request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "服务器处理请求失败")
	}
}

func intQuery(r *http.Request, name string, fallback, minimum, maximum int) (int, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return parsed, nil
}

type loginLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	attempts map[string][]time.Time
}

func newLoginLimiter(limit int, window time.Duration) *loginLimiter {
	return &loginLimiter{limit: limit, window: window, attempts: make(map[string][]time.Time)}
}

func (l *loginLimiter) Allow(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	now := time.Now()
	cutoff := now.Add(-l.window)
	l.mu.Lock()
	defer l.mu.Unlock()
	items := l.attempts[host][:0]
	for _, attempt := range l.attempts[host] {
		if attempt.After(cutoff) {
			items = append(items, attempt)
		}
	}
	if len(items) >= l.limit {
		l.attempts[host] = items
		return false
	}
	l.attempts[host] = append(items, now)
	return true
}

func (l *loginLimiter) Reset(address string) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	l.mu.Lock()
	delete(l.attempts, host)
	l.mu.Unlock()
}

func normalizeJobStatus(status string) string {
	switch status {
	case "completed":
		return "completed"
	case "failed", "uncertain":
		return "failed"
	case "pending":
		return "pending"
	default:
		return "running"
	}
}
