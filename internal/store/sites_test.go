package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTrackerIdentityRedactsSecrets(t *testing.T) {
	raw := "https://user:password@Tracker.Example.com:443/announce/" + strings.Repeat("a", 32) + "?passkey=secret#fragment"
	host, path, err := TrackerIdentity(raw)
	if err != nil {
		t.Fatal(err)
	}
	if host != "tracker.example.com" || strings.Contains(path, strings.Repeat("a", 32)) || strings.Contains(path, "secret") {
		t.Fatalf("unsafe tracker identity: host=%q path=%q", host, path)
	}
}

func TestTrackerIdentityRedactsArbitraryShortPathSecrets(t *testing.T) {
	host, path, err := TrackerIdentity("udp://tracker.example.com/announce/x7Kp2")
	if err != nil {
		t.Fatal(err)
	}
	if host != "tracker.example.com" || path != "/announce/*" || strings.Contains(path, "x7Kp2") {
		t.Fatalf("unsafe short tracker identity: host=%q path=%q", host, path)
	}
}

func TestTrackerIdentityRedactsHighEntropySubdomainToExactMappableIdentity(t *testing.T) {
	secret := "a91f3c7e5b2d8046a91f3c7e5b2d8046"
	host, path, err := TrackerIdentity("https://" + secret + ".tracker.example.com/announce")
	if err != nil {
		t.Fatal(err)
	}
	if host != "_redacted.tracker.example.com" || strings.Contains(host, "*") ||
		path != "/announce" || strings.Contains(host, secret) {
		t.Fatalf("unsafe dynamic tracker identity: host=%q path=%q", host, path)
	}
	idempotent, _, err := TrackerIdentity("https://" + host + "/announce")
	if err != nil {
		t.Fatal(err)
	}
	if idempotent != host {
		t.Fatalf("redacted identity is not idempotent: %q became %q", host, idempotent)
	}
	pureLetters, _, err := TrackerIdentity("https://abcdefghijklmnop.tracker.example.com/announce")
	if err != nil {
		t.Fatal(err)
	}
	if pureLetters != "_redacted.tracker.example.com" {
		t.Fatalf("16-character alphabetic token was not redacted: %q", pureLetters)
	}
	shortMixed, _, err := TrackerIdentity("https://abc123def456.tracker.example.com/announce")
	if err != nil {
		t.Fatal(err)
	}
	if shortMixed != "_redacted.tracker.example.com" {
		t.Fatalf("12-character mixed token was not redacted: %q", shortMixed)
	}
	shortBase32, _, err := TrackerIdentity("https://nqpxztrvkmwj.tracker.example.com/announce")
	if err != nil {
		t.Fatal(err)
	}
	if shortBase32 != "_redacted.tracker.example.com" {
		t.Fatalf("12-character base32-style token was not redacted: %q", shortBase32)
	}

	ordinary, _, err := TrackerIdentity("udp://tracker01.example.com:6969/announce")
	if err != nil {
		t.Fatal(err)
	}
	if ordinary != "tracker01.example.com" {
		t.Fatalf("ordinary tracker host changed to %q", ordinary)
	}
}

func TestCreateCustomTrackerRule(t *testing.T) {
	store := openTestStore(t)
	rule, err := store.CreateCustomTrackerRule(context.Background(), CreateTrackerRuleParams{
		HostPattern: "tracker.example.com",
		SiteName:    "example",
		DisplayName: "Example PT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rule.Source != "custom" || rule.Priority != 1000 {
		t.Fatalf("unexpected rule: %+v", rule)
	}
}

func TestCreateCustomTrackerRuleRedactsCredentialLikeHostLabel(t *testing.T) {
	database := openTestStore(t)
	secret := "a91f3c7e5b2d8046a91f3c7e5b2d8046"
	rule, err := database.CreateCustomTrackerRule(context.Background(), CreateTrackerRuleParams{
		HostPattern: secret + ".tracker.example.com",
		SiteName:    "redacted-host",
		DisplayName: "Redacted Host",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rule.HostPattern != "_redacted.tracker.example.com" || strings.Contains(rule.HostPattern, secret) ||
		strings.Contains(rule.HostPattern, "*") {
		t.Fatalf("unsafe rule host pattern: %q", rule.HostPattern)
	}
}

func TestCreateCustomTrackerRuleCanonicalizesSensitiveWildcard(t *testing.T) {
	database := openTestStore(t)
	downloader := seedDownloader(t, database)
	ctx := context.Background()
	secret := "qrstuvwxyzabcdef"
	record := torrentRecord(downloader, "sensitive-wildcard")
	record.Trackers = []TrackerRecord{{
		HostIdentity: "node." + secret + ".intentional.example.com",
		PathHint:     "/announce",
	}}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true,
		Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}

	rule, err := database.CreateCustomTrackerRule(ctx, CreateTrackerRuleParams{
		HostPattern: "*." + secret + ".intentional.example.com",
		PathPrefix:  "/announce",
		SiteName:    "sensitive-wildcard",
		DisplayName: "Sensitive Wildcard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rule.HostPattern != "_redacted.intentional.example.com" || strings.Contains(rule.HostPattern, secret) {
		t.Fatalf("sensitive wildcard was not canonicalized: %+v", rule)
	}
	if got := trackerSiteID(t, database, record.ID); !got.Valid || got.String != rule.SiteID {
		t.Fatalf("canonicalized wildcard did not reclassify observation: %+v", got)
	}

	ordinary, err := database.CreateCustomTrackerRule(ctx, CreateTrackerRuleParams{
		HostPattern: "*.ordinary.example.com",
		SiteName:    "ordinary-wildcard",
		DisplayName: "Ordinary Wildcard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.HostPattern != "*.ordinary.example.com" {
		t.Fatalf("ordinary wildcard changed: %+v", ordinary)
	}
	alreadySafe, err := database.CreateCustomTrackerRule(ctx, CreateTrackerRuleParams{
		HostPattern: "*._redacted.example.com",
		SiteName:    "already-safe-wildcard",
		DisplayName: "Already Safe Wildcard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if alreadySafe.HostPattern != "*._redacted.example.com" {
		t.Fatalf("already-safe wildcard changed: %+v", alreadySafe)
	}
}

func TestExactRedactedRuleDoesNotMatchOrdinarySuffixHost(t *testing.T) {
	database := openTestStore(t)
	downloader := seedDownloader(t, database)
	secret := "abcdefghijklmnop"
	redacted := torrentRecord(downloader, "redacted-host")
	redacted.Trackers = []TrackerRecord{{
		HostIdentity: secret + ".tracker.example.com",
		PathHint:     "/announce",
	}}
	ordinary := torrentRecord(downloader, "ordinary-host")
	ordinary.Trackers = []TrackerRecord{{HostIdentity: "other.tracker.example.com", PathHint: "/announce"}}
	ctx := context.Background()
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true,
		Torrents: []TorrentRecord{redacted, ordinary},
	}); err != nil {
		t.Fatal(err)
	}
	rule, err := database.CreateCustomTrackerRule(ctx, CreateTrackerRuleParams{
		HostPattern: secret + ".tracker.example.com",
		PathPrefix:  "/announce",
		SiteName:    "exact-redacted",
		DisplayName: "Exact Redacted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := trackerSiteID(t, database, redacted.ID); !got.Valid || got.String != rule.SiteID {
		t.Fatalf("redacted tracker site = %+v, want %q", got, rule.SiteID)
	}
	if got := trackerSiteID(t, database, ordinary.ID); got.Valid {
		t.Fatalf("exact redacted rule broadly matched ordinary host: %+v", got)
	}
}

func TestOpenNormalizesLegacyTrackerDataAndResolvesConflicts(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/seedgraph.db"
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	downloader, err := database.CreateDownloader(ctx, CreateDownloaderParams{
		Name: "Transmission", Kind: "transmission", BaseURL: "http://tr:9091",
		StorageName: "media", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped := torrentRecord(downloader, "legacy-mapped")
	conflicted := torrentRecord(downloader, "legacy-conflicted")
	ordinary := torrentRecord(downloader, "legacy-ordinary")
	wildcarded := torrentRecord(downloader, "legacy-sensitive-wildcard")
	mapped.Trackers, conflicted.Trackers, ordinary.Trackers, wildcarded.Trackers = nil, nil, nil, nil
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true,
		Torrents: []TorrentRecord{mapped, conflicted, ordinary, wildcarded},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := database.db.Exec(`
		INSERT INTO sites(id, name, display_name, source, created_at, updated_at) VALUES
			('safe-site', 'safe', 'Safe Site', 'custom', ?, ?),
			('conflict-a', 'conflict-a', 'Conflict A', 'custom', ?, ?),
			('conflict-b', 'conflict-b', 'Conflict B', 'custom', ?, ?)`,
		now, now, now, now, now, now,
	); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`
		INSERT INTO tracker_rules(
			id, host_pattern, path_prefix, site_id, source, priority, created_at, updated_at
		) VALUES
			('canonical-rule', '_redacted.tracker.example.com', '/announce', 'safe-site', 'custom', 100, ?, ?),
			('legacy-duplicate', 'abcdefghijklmnop.tracker.example.com', '/announce', 'safe-site', 'custom', 1000, ?, ?),
			('conflict-rule-a', 'abcdefghijklmnop.conflict.example.com', '/announce', 'conflict-a', 'custom', 1000, ?, ?),
			('conflict-rule-b', 'ponmlkjihgfedcba.conflict.example.com', '/announce', 'conflict-b', 'custom', 1000, ?, ?),
			('ordinary-wildcard', '*.ordinary.example.com', '', 'safe-site', 'custom', 500, ?, ?),
			('sensitive-wildcard', '*.qrstuvwxyzabcdef.intentional.example.com', '', 'safe-site', 'custom', 500, ?, ?)`,
		now-60, now-60, now-50, now-50, now-40, now-40, now-30, now-30, now-20, now-20, now-10, now-10,
	); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`
		INSERT INTO torrent_trackers(instance_id, host_identity, path_hint, site_id) VALUES
			(?, 'abcdefghijklmnop.tracker.example.com', '/announce/one', NULL),
			(?, 'ponmlkjihgfedcba.tracker.example.com', '/announce/two', NULL),
			(?, 'abcdefghijklmnop.conflict.example.com', '/announce/secret', 'conflict-a'),
			(?, 'other.tracker.example.com', '/announce', NULL),
			(?, 'node.qrstuvwxyzabcdef.intentional.example.com', '/announce', NULL)`,
		mapped.ID, mapped.ID, conflicted.ID, ordinary.ID, wildcarded.ID,
	); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	rules, err := database.ListTrackerRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ruleByID := make(map[string]TrackerRule)
	for _, rule := range rules {
		ruleByID[rule.ID] = rule
	}
	if _, ok := ruleByID["canonical-rule"]; !ok {
		t.Fatalf("canonical same-site rule was not retained: %+v", rules)
	}
	if _, ok := ruleByID["legacy-duplicate"]; ok {
		t.Fatalf("same-site duplicate rule was retained: %+v", rules)
	}
	if _, ok := ruleByID["conflict-rule-a"]; ok {
		t.Fatalf("ambiguous conflict rule A was retained: %+v", rules)
	}
	if _, ok := ruleByID["conflict-rule-b"]; ok {
		t.Fatalf("ambiguous conflict rule B was retained: %+v", rules)
	}
	if wildcard, ok := ruleByID["ordinary-wildcard"]; !ok || wildcard.HostPattern != "*.ordinary.example.com" {
		t.Fatalf("ordinary wildcard rule changed: %+v", wildcard)
	}
	if wildcard, ok := ruleByID["sensitive-wildcard"]; !ok || wildcard.HostPattern != "_redacted.intentional.example.com" {
		t.Fatalf("sensitive wildcard was not canonicalized: %+v", wildcard)
	}

	var trackerCount int
	var host, pathHint string
	var siteID sql.NullString
	if err := database.db.QueryRow(`
		SELECT COUNT(*), host_identity, path_hint, site_id
		FROM torrent_trackers WHERE instance_id = ?`, mapped.ID).
		Scan(&trackerCount, &host, &pathHint, &siteID); err != nil {
		t.Fatal(err)
	}
	if trackerCount != 1 || host != "_redacted.tracker.example.com" || pathHint != "/announce/*" ||
		!siteID.Valid || siteID.String != "safe-site" {
		t.Fatalf("normalized mapped tracker = count %d host %q path %q site %+v", trackerCount, host, pathHint, siteID)
	}
	if got := trackerSiteID(t, database, conflicted.ID); got.Valid {
		t.Fatalf("ambiguous normalized tracker remained mapped: %+v", got)
	}
	if got := trackerSiteID(t, database, ordinary.ID); got.Valid {
		t.Fatalf("exact placeholder rule matched ordinary tracker: %+v", got)
	}
	if got := trackerSiteID(t, database, wildcarded.ID); !got.Valid || got.String != "safe-site" {
		t.Fatalf("canonicalized wildcard did not map persisted tracker: %+v", got)
	}

	encodedRules, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	var persistedHosts string
	if err := database.db.QueryRow(`
		SELECT COALESCE(group_concat(host_pattern, '|'), '') || '|' ||
		       COALESCE((SELECT group_concat(host_identity, '|') FROM torrent_trackers), '')
		FROM tracker_rules`).Scan(&persistedHosts); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"abcdefghijklmnop", "ponmlkjihgfedcba", "qrstuvwxyzabcdef"} {
		if strings.Contains(string(encodedRules), secret) || strings.Contains(persistedHosts, secret) {
			t.Fatalf("startup normalization retained secret %q: rules=%s hosts=%s", secret, encodedRules, persistedHosts)
		}
	}

	// A second startup must perform no tracker rewrite. The trigger would abort
	// any reclassification UPDATE, so successful Open proves the migration is
	// idempotent once every identity is canonical.
	if _, err := database.db.Exec(`
		CREATE TRIGGER reject_idempotent_tracker_rewrite
		BEFORE UPDATE OF site_id ON torrent_trackers
		BEGIN SELECT RAISE(ABORT, 'unexpected tracker rewrite'); END`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("second idempotent Open() failed: %v", err)
	}
}

func TestRuleMutationsImmediatelyReclassifyExistingTrackers(t *testing.T) {
	database := openTestStore(t)
	downloader := seedDownloader(t, database)
	ctx := context.Background()
	record := torrentRecord(downloader, "rule-reclassification")
	record.Trackers = []TrackerRecord{{
		HostIdentity: "tracker.example.com",
		PathHint:     "/announce/*",
	}}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}

	fallback, err := database.CreateCustomTrackerRule(ctx, CreateTrackerRuleParams{
		HostPattern: "*.example.com",
		PathPrefix:  "/announce",
		SiteName:    "fallback",
		DisplayName: "Fallback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := trackerSiteID(t, database, record.ID); !got.Valid || got.String != fallback.SiteID {
		t.Fatalf("site after fallback rule = %+v, want %q", got, fallback.SiteID)
	}
	if items, err := database.ListUnmappedTrackerIdentities(ctx); err != nil || len(items) != 0 {
		t.Fatalf("unmapped after matching rule = %+v, err = %v", items, err)
	}

	// Make the wildcard rule a lower-priority fallback, then create an exact
	// rule through the public mutation path. Creating the exact rule must
	// immediately replace the persisted assignment.
	if _, err := database.db.Exec("UPDATE tracker_rules SET priority = 500 WHERE id = ?", fallback.ID); err != nil {
		t.Fatal(err)
	}
	primary, err := database.CreateCustomTrackerRule(ctx, CreateTrackerRuleParams{
		HostPattern: "tracker.example.com",
		PathPrefix:  "/announce",
		SiteName:    "primary",
		DisplayName: "Primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := trackerSiteID(t, database, record.ID); !got.Valid || got.String != primary.SiteID {
		t.Fatalf("site after primary rule = %+v, want %q", got, primary.SiteID)
	}

	// Deleting the winning rule must run the complete rule set again, not just
	// clear its site ID. The lower-priority wildcard rule should take over.
	if err := database.DeleteCustomTrackerRule(ctx, primary.ID); err != nil {
		t.Fatal(err)
	}
	if got := trackerSiteID(t, database, record.ID); !got.Valid || got.String != fallback.SiteID {
		t.Fatalf("site after deleting primary rule = %+v, want fallback %q", got, fallback.SiteID)
	}

	if err := database.DeleteCustomTrackerRule(ctx, fallback.ID); err != nil {
		t.Fatal(err)
	}
	if got := trackerSiteID(t, database, record.ID); got.Valid {
		t.Fatalf("site after deleting all matching rules = %+v, want NULL", got)
	}
	if items, err := database.ListUnmappedTrackerIdentities(ctx); err != nil || len(items) != 1 {
		t.Fatalf("unmapped after deleting all rules = %+v, err = %v", items, err)
	}
}

func TestRuleMutationAndReclassificationAreAtomic(t *testing.T) {
	database := openTestStore(t)
	downloader := seedDownloader(t, database)
	ctx := context.Background()
	record := torrentRecord(downloader, "rule-atomicity")
	record.Trackers = []TrackerRecord{{HostIdentity: "atomic.example.com", PathHint: "/announce"}}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}

	createFailureTrigger := func() {
		t.Helper()
		if _, err := database.db.Exec(`
			CREATE TRIGGER reject_tracker_reclassification
			BEFORE UPDATE OF site_id ON torrent_trackers
			BEGIN
				SELECT RAISE(ABORT, 'blocked tracker reclassification');
			END`); err != nil {
			t.Fatal(err)
		}
	}
	dropFailureTrigger := func() {
		t.Helper()
		if _, err := database.db.Exec("DROP TRIGGER reject_tracker_reclassification"); err != nil {
			t.Fatal(err)
		}
	}

	createFailureTrigger()
	if _, err := database.CreateCustomTrackerRule(ctx, CreateTrackerRuleParams{
		HostPattern: "atomic.example.com",
		SiteName:    "atomic-create",
		DisplayName: "Atomic Create",
	}); err == nil {
		t.Fatal("CreateCustomTrackerRule succeeded despite failed reclassification")
	}
	var ruleCount, siteCount int
	if err := database.db.QueryRow(
		"SELECT COUNT(*) FROM tracker_rules WHERE host_pattern = ?", "atomic.example.com",
	).Scan(&ruleCount); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(
		"SELECT COUNT(*) FROM sites WHERE name = ?", "atomic-create",
	).Scan(&siteCount); err != nil {
		t.Fatal(err)
	}
	if ruleCount != 0 || siteCount != 0 || trackerSiteID(t, database, record.ID).Valid {
		t.Fatalf("failed create was partially committed: rules=%d sites=%d", ruleCount, siteCount)
	}
	dropFailureTrigger()

	rule, err := database.CreateCustomTrackerRule(ctx, CreateTrackerRuleParams{
		HostPattern: "atomic.example.com",
		SiteName:    "atomic-delete",
		DisplayName: "Atomic Delete",
	})
	if err != nil {
		t.Fatal(err)
	}
	createFailureTrigger()
	if err := database.DeleteCustomTrackerRule(ctx, rule.ID); err == nil {
		t.Fatal("DeleteCustomTrackerRule succeeded despite failed reclassification")
	}
	if _, err := database.GetTrackerRule(ctx, rule.ID); err != nil {
		t.Fatalf("failed delete removed the rule: %v", err)
	}
	if got := trackerSiteID(t, database, record.ID); !got.Valid || got.String != rule.SiteID {
		t.Fatalf("failed delete changed tracker assignment: %+v", got)
	}
}

func trackerSiteID(t *testing.T, database *Store, instanceID string) sql.NullString {
	t.Helper()
	var siteID sql.NullString
	if err := database.db.QueryRow(
		"SELECT site_id FROM torrent_trackers WHERE instance_id = ?", instanceID,
	).Scan(&siteID); err != nil {
		t.Fatal(err)
	}
	return siteID
}

func TestListUnmappedTrackerIdentitiesAggregatesActiveInstances(t *testing.T) {
	database := openTestStore(t)
	downloader := seedDownloader(t, database)
	ctx := context.Background()

	mappedRule, err := database.CreateCustomTrackerRule(ctx, CreateTrackerRuleParams{
		HostPattern: "mapped.example.com",
		SiteName:    "mapped",
		DisplayName: "Mapped",
	})
	if err != nil {
		t.Fatal(err)
	}

	rawTracker := "https://user:password@tracker.example.com/announce/short-secret?passkey=query-secret#fragment"
	sharedHost, sharedPath, err := TrackerIdentity(rawTracker)
	if err != nil {
		t.Fatal(err)
	}
	records := []TorrentRecord{
		torrentRecord(downloader, "shared-one"),
		torrentRecord(downloader, "shared-two"),
		torrentRecord(downloader, "other"),
		torrentRecord(downloader, "mapped"),
		torrentRecord(downloader, "deleted"),
	}
	records[0].Trackers = []TrackerRecord{
		{HostIdentity: sharedHost, PathHint: sharedPath},
		{HostIdentity: sharedHost, PathHint: sharedPath},
	}
	records[1].Trackers = []TrackerRecord{{HostIdentity: sharedHost, PathHint: sharedPath}}
	records[2].Trackers = []TrackerRecord{{HostIdentity: "z.example.com", PathHint: "/announce"}}
	records[3].Trackers = []TrackerRecord{{
		HostIdentity: "mapped.example.com", PathHint: "/announce", SiteID: mappedRule.SiteID,
	}}
	records[4].Trackers = []TrackerRecord{{HostIdentity: "deleted.example.com", PathHint: "/announce"}}
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID,
		Mode:         "full",
		Complete:     true,
		Torrents:     records,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`
		UPDATE torrent_instances
		SET last_seen_at = CASE id
			WHEN ? THEN 1700
			WHEN ? THEN 1800
			WHEN ? THEN 1600
			ELSE last_seen_at
		END,
		deleted_at = CASE WHEN id = ? THEN 1900 ELSE deleted_at END`,
		records[0].ID, records[1].ID, records[2].ID, records[4].ID,
	); err != nil {
		t.Fatal(err)
	}

	items, err := database.ListUnmappedTrackerIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("unmapped identities = %+v, want two active unmapped identities", items)
	}
	if got := items[0]; got.HostIdentity != sharedHost || got.PathHint != "/announce/*" ||
		got.InstanceCount != 2 || got.GroupCount != 2 || !got.LastSeenAt.Equal(time.Unix(1800, 0).UTC()) {
		t.Fatalf("aggregated identity = %+v", got)
	}
	if got := items[1]; got.HostIdentity != "z.example.com" || got.InstanceCount != 1 || got.GroupCount != 1 {
		t.Fatalf("second identity = %+v", got)
	}

	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	response := string(encoded)
	for _, secret := range []string{rawTracker, "user", "password", "short-secret", "query-secret", "passkey"} {
		if strings.Contains(response, secret) {
			t.Fatalf("unmapped tracker response leaked %q: %s", secret, response)
		}
	}
}

func TestListUnmappedTrackerIdentitiesRedactsUnexpectedStoredValues(t *testing.T) {
	database := openTestStore(t)
	downloader := seedDownloader(t, database)
	ctx := context.Background()
	record := torrentRecord(downloader, "legacy-tracker")
	record.Trackers = nil
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true, Torrents: []TorrentRecord{record},
	}); err != nil {
		t.Fatal(err)
	}
	rawHost := "https://user:password@legacy.example.com:443/announce?passkey=host-secret"
	rawPath := "/announce/path-secret?passkey=query-secret"
	if _, err := database.db.Exec(`
		INSERT INTO torrent_trackers(instance_id, host_identity, path_hint, site_id)
		VALUES(?, ?, ?, NULL)`, record.ID, rawHost, rawPath); err != nil {
		t.Fatal(err)
	}

	items, err := database.ListUnmappedTrackerIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].HostIdentity != "legacy.example.com" || items[0].PathHint != "/announce/*" {
		t.Fatalf("legacy identity was not redacted: %+v", items)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	response := string(encoded)
	for _, secret := range []string{rawHost, rawPath, "user", "password", "host-secret", "path-secret", "query-secret", "passkey"} {
		if strings.Contains(response, secret) {
			t.Fatalf("legacy tracker response leaked %q: %s", secret, response)
		}
	}
}

func TestListUnmappedTrackerIdentitiesMergesAndDeduplicatesAfterRedaction(t *testing.T) {
	database := openTestStore(t)
	downloader := seedDownloader(t, database)
	ctx := context.Background()
	first := torrentRecord(downloader, "legacy-one")
	second := torrentRecord(downloader, "legacy-two")
	first.Trackers = nil
	second.Trackers = nil
	if _, err := database.ApplySync(ctx, ApplySyncParams{
		DownloaderID: downloader.ID, Mode: "full", Complete: true,
		Torrents: []TorrentRecord{first, second},
	}); err != nil {
		t.Fatal(err)
	}
	secretOne := "a91f3c7e5b2d8046a91f3c7e5b2d8046"
	secretTwo := "b82e4d6f7c1a9053b82e4d6f7c1a9053"
	legacyRows := []struct {
		instanceID string
		host       string
		path       string
	}{
		{first.ID, "https://user:password@" + secretOne + ".legacy.example.com:443", "/announce/one-secret"},
		{first.ID, secretTwo + ".legacy.example.com", "/announce/two-secret"},
		{second.ID, secretOne + ".legacy.example.com", "/announce/three-secret"},
	}
	for _, row := range legacyRows {
		if _, err := database.db.Exec(`
			INSERT INTO torrent_trackers(instance_id, host_identity, path_hint, site_id)
			VALUES(?, ?, ?, NULL)`, row.instanceID, row.host, row.path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.db.Exec(`
		UPDATE torrent_instances SET last_seen_at = CASE id WHEN ? THEN 1700 WHEN ? THEN 1800 END
		WHERE id IN (?, ?)`, first.ID, second.ID, first.ID, second.ID); err != nil {
		t.Fatal(err)
	}

	items, err := database.ListUnmappedTrackerIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("redacted identities = %+v, want one merged identity", items)
	}
	got := items[0]
	if got.HostIdentity != "_redacted.legacy.example.com" || got.PathHint != "/announce/*" ||
		got.InstanceCount != 2 || got.GroupCount != 2 || !got.LastSeenAt.Equal(time.Unix(1800, 0).UTC()) {
		t.Fatalf("merged redacted identity = %+v", got)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{secretOne, secretTwo, "user", "password", "one-secret", "two-secret", "three-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("merged identity leaked %q: %s", secret, encoded)
		}
	}
}
