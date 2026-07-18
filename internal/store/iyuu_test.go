package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestApplyIYUUCatalogIsAtomicAndRetainsMissingRowsAsStale(t *testing.T) {
	database := openTestStore(t)
	firstFetch := time.Unix(100, 0).UTC()
	if err := database.ApplyIYUUCatalog(context.Background(), []IYUUSiteInput{
		{RemoteID: 1, Slug: "alpha", Nickname: "Alpha", BaseURL: "Alpha.EXAMPLE", IsHTTPS: 2},
		{RemoteID: 2, Slug: "beta", Nickname: "Beta", BaseURL: "beta.example", IsHTTPS: 1, CookieRequired: 1},
	}, firstFetch); err != nil {
		t.Fatal(err)
	}
	secondFetch := time.Unix(200, 0).UTC()
	if err := database.ApplyIYUUCatalog(context.Background(), []IYUUSiteInput{
		{RemoteID: 1, Slug: "alpha", Nickname: "Alpha 2", BaseURL: "alpha.example", IsHTTPS: 2},
	}, secondFetch); err != nil {
		t.Fatal(err)
	}

	items, state, err := database.ListIYUUSites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || state.SiteCount != 1 || state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(secondFetch) {
		t.Fatalf("unexpected catalog state: items=%+v state=%+v", items, state)
	}
	bySlug := map[string]IYUUSite{items[0].Slug: items[0], items[1].Slug: items[1]}
	if bySlug["alpha"].Stale || bySlug["alpha"].Nickname != "Alpha 2" || bySlug["alpha"].BaseURL != "alpha.example" {
		t.Fatalf("unexpected current row: %+v", bySlug["alpha"])
	}
	if !bySlug["beta"].Stale {
		t.Fatalf("missing upstream row was not retained as stale: %+v", bySlug["beta"])
	}

	if err := database.ApplyIYUUCatalog(context.Background(), []IYUUSiteInput{
		{RemoteID: 3, Slug: "duplicate", BaseURL: "one.example"},
		{RemoteID: 3, Slug: "other", BaseURL: "two.example"},
	}, time.Unix(300, 0)); err == nil {
		t.Fatal("duplicate remote IDs unexpectedly committed")
	}
	itemsAfter, stateAfter, err := database.ListIYUUSites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(itemsAfter) != 2 || stateAfter.LastSuccessAt == nil || !stateAfter.LastSuccessAt.Equal(secondFetch) {
		t.Fatalf("invalid response changed catalog: items=%+v state=%+v", itemsAfter, stateAfter)
	}
}

func TestRecordIYUUSyncFailurePreservesLastSuccess(t *testing.T) {
	database := openTestStore(t)
	successAt := time.Unix(100, 0).UTC()
	if err := database.ApplyIYUUCatalog(context.Background(), []IYUUSiteInput{
		{RemoteID: 1, Slug: "alpha", BaseURL: "alpha.example"},
	}, successAt); err != nil {
		t.Fatal(err)
	}
	failureAt := time.Unix(200, 0).UTC()
	if err := database.RecordIYUUSyncFailure(context.Background(), failureAt, errors.New("upstream rate limited")); err != nil {
		t.Fatal(err)
	}
	state, err := database.IYUUSyncState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.LastAttemptAt == nil || !state.LastAttemptAt.Equal(failureAt) ||
		state.LastSuccessAt == nil || !state.LastSuccessAt.Equal(successAt) ||
		state.LastError != "upstream rate limited" || state.SiteCount != 1 {
		t.Fatalf("unexpected failure state: %+v", state)
	}
}
