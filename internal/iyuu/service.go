package iyuu

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/lesir831/SeedGraph/internal/store"
)

var (
	ErrSyncRunning  = errors.New("IYUU catalog sync is already running")
	ErrSyncCooldown = errors.New("IYUU catalog sync is cooling down")
)

const defaultManualCooldown = time.Minute

type CatalogClient interface {
	Sites(context.Context) ([]Site, error)
}

type SyncResult struct {
	SiteCount int       `json:"site_count"`
	FetchedAt time.Time `json:"fetched_at"`
}

type Service struct {
	client   CatalogClient
	store    *store.Store
	logger   *slog.Logger
	interval time.Duration

	mu            sync.Mutex
	running       bool
	nextAllowedAt time.Time
	now           func() time.Time
	manual        sync.WaitGroup
}

func NewService(client CatalogClient, database *store.Store, logger *slog.Logger, interval time.Duration) (*Service, error) {
	if client == nil || database == nil {
		return nil, errors.New("IYUU sync dependencies are incomplete")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &Service{client: client, store: database, logger: logger, interval: interval, now: time.Now}, nil
}

// Run performs an initial refresh and then refreshes at a low-frequency,
// jittered interval. Failures keep the last successful catalog intact.
func (s *Service) Run(ctx context.Context) {
	if _, err := s.sync(ctx, true); err != nil && !errors.Is(err, context.Canceled) {
		s.logger.Warn("initial IYUU catalog sync failed", "error", err)
	}
	for {
		delay := s.interval + positiveJitter(s.interval)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
			if _, err := s.sync(ctx, true); err != nil &&
				!errors.Is(err, context.Canceled) &&
				!errors.Is(err, ErrSyncCooldown) {
				s.logger.Warn("scheduled IYUU catalog sync failed", "error", err)
			}
		}
	}
}

// SyncNow performs a manual refresh. A shared singleflight/cooldown guard
// prevents UI retries from hammering the public catalog endpoint.
func (s *Service) SyncNow(ctx context.Context) (SyncResult, error) {
	s.manual.Add(1)
	defer s.manual.Done()
	return s.sync(ctx, false)
}

// Wait blocks until accepted manual refreshes have returned. The scheduled
// Run loop is joined separately by the application lifecycle.
func (s *Service) Wait() {
	s.manual.Wait()
}

func (s *Service) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Service) NextAllowedAt() *time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextAllowedAt.IsZero() {
		return nil
	}
	value := s.nextAllowedAt
	return &value
}

func (s *Service) sync(ctx context.Context, scheduled bool) (SyncResult, error) {
	attemptedAt := s.now().UTC()
	if err := s.begin(attemptedAt); err != nil {
		return SyncResult{}, err
	}
	defer s.end()

	sites, err := s.client.Sites(ctx)
	if err != nil {
		// Caller cancellation is a lifecycle event, not an upstream failure. In
		// particular, shutdown must not manufacture a warning/cooldown or start a
		// detached database write while the store is closing.
		if ctx.Err() != nil {
			return SyncResult{}, err
		}
		s.recordFailure(ctx, attemptedAt, err, scheduled)
		return SyncResult{}, err
	}
	inputs := make([]store.IYUUSiteInput, 0, len(sites))
	for _, site := range sites {
		inputs = append(inputs, store.IYUUSiteInput{
			RemoteID: site.ID, Slug: site.Site, Nickname: site.Nickname, BaseURL: site.BaseURL,
			DownloadPage: site.DownloadPage, DetailsPage: site.DetailsPage,
			IsHTTPS: site.IsHTTPS, CookieRequired: site.CookieRequired,
		})
	}
	if err := s.store.ApplyIYUUCatalog(ctx, inputs, attemptedAt); err != nil {
		persistErr := fmt.Errorf("persist IYUU catalog: %w", err)
		if ctx.Err() != nil {
			return SyncResult{}, persistErr
		}
		s.recordFailure(ctx, attemptedAt, persistErr, scheduled)
		return SyncResult{}, persistErr
	}
	s.setSuccessCooldown(attemptedAt)
	_ = s.store.AddAuditEvent(context.WithoutCancel(ctx), store.AuditEvent{
		Actor: "system", Action: "iyuu.sync", Status: "success",
		TargetType: "site_catalog", TargetID: "iyuu",
		Details: map[string]any{"site_count": len(inputs), "scheduled": scheduled},
	})
	return SyncResult{SiteCount: len(inputs), FetchedAt: attemptedAt}, nil
}

func (s *Service) recordFailure(ctx context.Context, attemptedAt time.Time, syncErr error, scheduled bool) {
	s.setFailureCooldown(attemptedAt, syncErr)
	writeCtx := context.WithoutCancel(ctx)
	if err := s.store.RecordIYUUSyncFailure(writeCtx, attemptedAt, syncErr); err != nil {
		s.logger.Error("record IYUU sync failure", "error", err)
	}
	if err := s.store.AddAuditEvent(writeCtx, store.AuditEvent{
		Actor: "system", Action: "iyuu.sync", Status: "failed",
		TargetType: "site_catalog", TargetID: "iyuu",
		Details: map[string]any{"error": boundedError(syncErr), "scheduled": scheduled},
	}); err != nil {
		s.logger.Error("record IYUU sync failure audit", "error", err)
	}
}

func (s *Service) begin(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return ErrSyncRunning
	}
	if now.Before(s.nextAllowedAt) {
		return fmt.Errorf("%w until %s", ErrSyncCooldown, s.nextAllowedAt.Format(time.RFC3339))
	}
	s.running = true
	return nil
}

func (s *Service) end() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}

func (s *Service) setSuccessCooldown(now time.Time) {
	s.mu.Lock()
	s.nextAllowedAt = now.Add(defaultManualCooldown)
	s.mu.Unlock()
}

func (s *Service) setFailureCooldown(now time.Time, err error) {
	delay := 5 * time.Minute
	if hint, ok := RetryHintFrom(err); ok {
		if hint.After > delay {
			delay = hint.After
		}
		if !hint.ResetAt.IsZero() && hint.ResetAt.After(now.Add(delay)) {
			delay = hint.ResetAt.Sub(now)
		}
	} else {
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Code == 400 || apiErr.Code == 429) &&
			strings.Contains(apiErr.Message, "频率") {
			delay = 15 * time.Minute
		}
	}
	s.mu.Lock()
	s.nextAllowedAt = now.Add(delay)
	s.mu.Unlock()
}

func positiveJitter(interval time.Duration) time.Duration {
	maximum := interval / 20
	if maximum > 30*time.Minute {
		maximum = 30 * time.Minute
	}
	if maximum <= 0 {
		return 0
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(maximum)))
	if err != nil {
		return 0
	}
	return time.Duration(value.Int64())
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
