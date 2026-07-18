package httpapi

import (
	"net/http"
	"time"

	"github.com/lesir831/SeedGraph/internal/auth"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.loginLimiter.Allow(r.RemoteAddr) {
		w.Header().Set("Retry-After", "60")
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "登录尝试过多，请稍后再试")
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeBody(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	hash, err := s.store.AdminPasswordHash(r.Context())
	if err != nil || request.Username != "admin" || !auth.VerifyPassword(hash, request.Password) {
		// Keep the observable response identical for unknown users and bad secrets.
		time.Sleep(100 * time.Millisecond)
		writeAPIError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	cookieValue, session, err := s.sessions.Create("admin")
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: cookieValue, Path: "/", HttpOnly: true,
		Secure: s.cookieSecure, SameSite: http.SameSiteStrictMode,
		Expires: session.ExpiresAt, MaxAge: int(time.Until(session.ExpiresAt).Seconds()),
	})
	s.loginLimiter.Reset(r.RemoteAddr)
	writeData(w, http.StatusOK, map[string]any{
		"authenticated": true, "subject": session.Subject,
		"csrf_token": session.CSRFToken, "expires_at": session.ExpiresAt,
	})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	session, ok := r.Context().Value(sessionContextKey).(auth.Session)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"authenticated": true, "subject": session.Subject,
		"csrf_token": session.CSRFToken, "expires_at": session.ExpiresAt,
	})
}

func (s *Server) logout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: s.cookieSecure, SameSite: http.SameSiteStrictMode,
		Expires: time.Unix(1, 0), MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}
