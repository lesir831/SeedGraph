package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lesir831/SeedGraph/internal/iyuu"
)

func (s *Server) listSites(w http.ResponseWriter, r *http.Request) {
	items, state, err := s.store.ListIYUUSites(r.Context())
	if err != nil {
		s.handleError(w, r, err)
		return
	}
	response := map[string]any{
		"items":   items,
		"state":   state,
		"running": s.iyuu != nil && s.iyuu.Running(),
	}
	if s.iyuu != nil {
		response["next_allowed_at"] = s.iyuu.NextAllowedAt()
	}
	writeData(w, http.StatusOK, response)
}

func (s *Server) syncIYUUSites(w http.ResponseWriter, r *http.Request) {
	if s.iyuu == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "iyuu_unavailable", "IYUU 目录同步服务未启用")
		return
	}
	result, err := s.iyuu.SyncNow(r.Context())
	if err != nil {
		switch {
		case errors.Is(err, iyuu.ErrSyncRunning):
			writeAPIError(w, http.StatusConflict, "iyuu_sync_running", "IYUU 目录同步正在运行")
		case errors.Is(err, iyuu.ErrSyncCooldown):
			if next := s.iyuu.NextAllowedAt(); next != nil {
				seconds := max(int(time.Until(*next).Seconds()), 1)
				w.Header().Set("Retry-After", strconv.Itoa(seconds))
			}
			writeAPIError(w, http.StatusTooManyRequests, "iyuu_sync_cooldown", "IYUU 目录同步过于频繁，请稍后再试")
		case writeIYUUUpstreamError(w, err, s.iyuu.NextAllowedAt()):
		default:
			s.handleError(w, r, err)
		}
		return
	}
	writeData(w, http.StatusOK, result)
}

func writeIYUUUpstreamError(w http.ResponseWriter, err error, nextAllowedAt *time.Time) bool {
	var httpErr *iyuu.HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.StatusCode == http.StatusTooManyRequests:
			setIYUURetryAfter(w, err, nextAllowedAt)
			writeAPIError(w, http.StatusTooManyRequests, "iyuu_rate_limited", "IYUU 上游限制了同步频率，请稍后再试")
		case httpErr.StatusCode == http.StatusRequestTimeout || httpErr.StatusCode >= http.StatusInternalServerError:
			setIYUURetryAfterIfPresent(w, err)
			writeAPIError(w, http.StatusServiceUnavailable, "iyuu_upstream_unavailable", "IYUU 上游服务暂时不可用，请稍后再试")
		default:
			writeAPIError(w, http.StatusBadGateway, "iyuu_upstream_error", "IYUU 上游返回了无效响应")
		}
		return true
	}

	var apiErr *iyuu.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch {
	case isIYUURateLimitError(apiErr):
		setIYUURetryAfter(w, err, nextAllowedAt)
		writeAPIError(w, http.StatusTooManyRequests, "iyuu_rate_limited", "IYUU 上游限制了同步频率，请稍后再试")
	case apiErr.Code == http.StatusRequestTimeout ||
		(apiErr.Code >= http.StatusInternalServerError && apiErr.Code <= 599):
		setIYUURetryAfterIfPresent(w, err)
		writeAPIError(w, http.StatusServiceUnavailable, "iyuu_upstream_unavailable", "IYUU 上游服务暂时不可用，请稍后再试")
	default:
		writeAPIError(w, http.StatusBadGateway, "iyuu_upstream_error", "IYUU 上游返回了无效响应")
	}
	return true
}

func isIYUURateLimitError(err *iyuu.APIError) bool {
	if err.Code == http.StatusTooManyRequests {
		return true
	}
	if err.Code != http.StatusBadRequest {
		return false
	}
	message := strings.ToLower(err.Message)
	return strings.Contains(message, "频率") ||
		strings.Contains(message, "rate limit") ||
		strings.Contains(message, "too many requests")
}

func setIYUURetryAfter(w http.ResponseWriter, err error, nextAllowedAt *time.Time) {
	now := time.Now()
	delay := retryDelay(err, now)
	if nextAllowedAt != nil && nextAllowedAt.After(now) {
		delay = max(delay, nextAllowedAt.Sub(now))
	}
	if delay <= 0 {
		delay = time.Minute
	}
	seconds := max(int((delay+time.Second-1)/time.Second), 1)
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}

func setIYUURetryAfterIfPresent(w http.ResponseWriter, err error) {
	if delay := retryDelay(err, time.Now()); delay > 0 {
		seconds := max(int((delay+time.Second-1)/time.Second), 1)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
}

func retryDelay(err error, now time.Time) time.Duration {
	hint, ok := iyuu.RetryHintFrom(err)
	if !ok {
		return 0
	}
	delay := hint.After
	if hint.ResetAt.After(now) {
		delay = max(delay, hint.ResetAt.Sub(now))
	}
	return delay
}
