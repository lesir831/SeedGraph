package iyuu

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesir831/SeedGraph/internal/store"
)

type fakeCatalogClient struct {
	sites   []Site
	err     error
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (client *fakeCatalogClient) Sites(ctx context.Context) ([]Site, error) {
	client.calls.Add(1)
	if client.started != nil {
		close(client.started)
	}
	if client.release != nil {
		select {
		case <-client.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return append([]Site(nil), client.sites...), client.err
}

func testCatalogService(t *testing.T, client CatalogClient) (*Service, *store.Store) {
	t.Helper()
	database, err := store.Open(context.Background(), t.TempDir()+"/seedgraph.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service, err := NewService(client, database, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return service, database
}

func TestServiceSyncNowPersistsCompleteCatalog(t *testing.T) {
	client := &fakeCatalogClient{sites: []Site{{
		ID: 1, Site: "alpha", Nickname: "Alpha", BaseURL: "alpha.example", IsHTTPS: 2,
	}}}
	service, database := testCatalogService(t, client)
	now := time.Unix(100, 0).UTC()
	service.now = func() time.Time { return now }

	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.SiteCount != 1 || !result.FetchedAt.Equal(now) {
		t.Fatalf("unexpected result: %+v", result)
	}
	items, state, err := database.ListIYUUSites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Slug != "alpha" || state.LastError != "" || state.SiteCount != 1 {
		t.Fatalf("unexpected stored catalog: items=%+v state=%+v", items, state)
	}
	if _, err := service.SyncNow(context.Background()); !errors.Is(err, ErrSyncCooldown) {
		t.Fatalf("immediate second sync error = %v", err)
	}
}

func TestServiceFailurePreservesCatalogAndUsesRateLimitCooldown(t *testing.T) {
	client := &fakeCatalogClient{sites: []Site{{
		ID: 1, Site: "alpha", BaseURL: "alpha.example", IsHTTPS: 2,
	}}}
	service, database := testCatalogService(t, client)
	current := time.Unix(100, 0).UTC()
	service.now = func() time.Time { return current }
	if _, err := service.SyncNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	current = current.Add(2 * time.Minute)
	client.sites = nil
	client.err = &APIError{Code: 429, Message: "访问频率过快", hint: RetryHint{After: 20 * time.Minute}}
	if _, err := service.SyncNow(context.Background()); err == nil {
		t.Fatal("rate-limited sync unexpectedly succeeded")
	}
	next := service.NextAllowedAt()
	if next == nil || !next.Equal(current.Add(20*time.Minute)) {
		t.Fatalf("next allowed = %v", next)
	}
	items, state, err := database.ListIYUUSites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || state.LastSuccessAt == nil || state.LastError == "" {
		t.Fatalf("failure replaced last catalog: items=%+v state=%+v", items, state)
	}
}

func TestServiceRejectsConcurrentSync(t *testing.T) {
	client := &fakeCatalogClient{
		sites:   []Site{{ID: 1, Site: "alpha", BaseURL: "alpha.example"}},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	service, _ := testCatalogService(t, client)
	done := make(chan error, 1)
	go func() {
		_, err := service.SyncNow(context.Background())
		done <- err
	}()
	<-client.started
	if _, err := service.SyncNow(context.Background()); !errors.Is(err, ErrSyncRunning) {
		t.Fatalf("concurrent sync error = %v", err)
	}
	close(client.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if client.calls.Load() != 1 {
		t.Fatalf("client calls = %d", client.calls.Load())
	}
}

func TestServiceCancellationDoesNotRecordFailure(t *testing.T) {
	client := &fakeCatalogClient{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	service, database := testCatalogService(t, client)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.SyncNow(ctx)
		done <- err
	}()
	<-client.started
	waited := make(chan struct{})
	go func() {
		service.Wait()
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("Wait returned while a manual sync was still running")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("sync error = %v", err)
	}
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after manual sync cancellation")
	}

	state, err := database.IYUUSyncState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.LastAttemptAt != nil || state.LastSuccessAt != nil || state.LastError != "" {
		t.Fatalf("cancellation was persisted as a failure: %+v", state)
	}
	if service.NextAllowedAt() != nil {
		t.Fatalf("cancellation unexpectedly started a cooldown: %v", service.NextAllowedAt())
	}
	events, err := database.ListAuditEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("cancellation unexpectedly emitted audit events: %+v", events)
	}
}

func TestServicePersistenceFailureUpdatesStateAndAudit(t *testing.T) {
	client := &fakeCatalogClient{sites: []Site{
		{ID: 1, Site: "duplicate", BaseURL: "one.example"},
		{ID: 2, Site: "duplicate", BaseURL: "two.example"},
	}}
	service, database := testCatalogService(t, client)
	now := time.Unix(500, 0).UTC()
	service.now = func() time.Time { return now }

	_, err := service.SyncNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "persist IYUU catalog") {
		t.Fatalf("sync error = %v", err)
	}
	state, readErr := database.IYUUSyncState(context.Background())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if state.LastAttemptAt == nil || !state.LastAttemptAt.Equal(now) ||
		state.LastSuccessAt != nil || !strings.Contains(state.LastError, "persist IYUU catalog") {
		t.Fatalf("persistence failure was not recorded: %+v", state)
	}
	events, readErr := database.ListAuditEvents(context.Background(), 10)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(events) != 1 || events[0].Action != "iyuu.sync" || events[0].Status != "failed" {
		t.Fatalf("unexpected persistence failure audit: %+v", events)
	}
}
