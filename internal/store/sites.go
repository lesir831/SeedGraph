package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/publicsuffix"
)

type TrackerRule struct {
	ID          string    `json:"id"`
	HostPattern string    `json:"host_pattern"`
	PathPrefix  string    `json:"path_prefix"`
	SiteID      string    `json:"site_id"`
	SiteName    string    `json:"site_name"`
	DisplayName string    `json:"display_name"`
	Source      string    `json:"source"`
	Priority    int       `json:"priority"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// UnmappedTrackerIdentity is the privacy-preserving tracker identity shown to
// administrators when no tracker rule currently matches it. It intentionally
// contains neither an announce URL nor any credentials or query parameters.
type UnmappedTrackerIdentity struct {
	HostIdentity  string    `json:"host_identity"`
	PathHint      string    `json:"path_hint"`
	InstanceCount int       `json:"instance_count"`
	GroupCount    int       `json:"group_count"`
	LastSeenAt    time.Time `json:"last_seen_at"`
}

type CreateTrackerRuleParams struct {
	HostPattern string
	PathPrefix  string
	SiteName    string
	DisplayName string
}

func (s *Store) CreateCustomTrackerRule(ctx context.Context, params CreateTrackerRuleParams) (TrackerRule, error) {
	host, err := canonicalizeTrackerRuleHost(params.HostPattern)
	if err != nil {
		return TrackerRule{}, err
	}
	params.PathPrefix = sanitizePathHint(params.PathPrefix)
	params.SiteName = strings.TrimSpace(params.SiteName)
	params.DisplayName = strings.TrimSpace(params.DisplayName)
	if params.SiteName == "" {
		return TrackerRule{}, errors.New("site name is required")
	}

	var siteID string
	ruleID := uuid.NewString()
	now := s.now().Unix()
	err = s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT id FROM sites WHERE name = ?", params.SiteName).Scan(&siteID)
		switch {
		case err == nil:
			// Reuse the canonical site identity exactly as maintained by IYUU (or
			// by an earlier custom rule). Creating a Tracker rule must not turn an
			// IYUU site into a custom site or overwrite catalog-managed metadata.
		case errors.Is(err, sql.ErrNoRows):
			if params.DisplayName == "" {
				return errors.New("display name is required for a new site")
			}
			siteID = uuid.NewString()
			if _, err := tx.ExecContext(ctx, `
                INSERT INTO sites(id, name, display_name, source, created_at, updated_at)
                VALUES(?, ?, ?, 'custom', ?, ?)`,
				siteID, params.SiteName, params.DisplayName, now, now); err != nil {
				return fmt.Errorf("create custom site: %w", err)
			}
		default:
			return fmt.Errorf("look up site: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO tracker_rules(
                id, host_pattern, path_prefix, site_id, source, priority, created_at, updated_at
            ) VALUES(?, ?, ?, ?, 'custom', 1000, ?, ?)`,
			ruleID, host, params.PathPrefix, siteID, now, now); err != nil {
			return fmt.Errorf("insert tracker rule: %w", err)
		}
		return reclassifyTorrentTrackers(ctx, tx)
	})
	if err != nil {
		return TrackerRule{}, err
	}
	return s.GetTrackerRule(ctx, ruleID)
}

func (s *Store) GetTrackerRule(ctx context.Context, id string) (TrackerRule, error) {
	row := s.db.QueryRowContext(ctx, `
        SELECT r.id, r.host_pattern, r.path_prefix, r.site_id, s.name, s.display_name,
               r.source, r.priority, r.created_at, r.updated_at
        FROM tracker_rules r JOIN sites s ON s.id = r.site_id WHERE r.id = ?`, id)
	rule, err := scanTrackerRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TrackerRule{}, ErrNotFound
	}
	return rule, err
}

func (s *Store) ListTrackerRules(ctx context.Context) ([]TrackerRule, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT r.id, r.host_pattern, r.path_prefix, r.site_id, s.name, s.display_name,
               r.source, r.priority, r.created_at, r.updated_at
        FROM tracker_rules r JOIN sites s ON s.id = r.site_id
        ORDER BY r.priority DESC, s.display_name COLLATE NOCASE,
                 r.host_pattern, r.path_prefix, r.id`)
	if err != nil {
		return nil, fmt.Errorf("list tracker rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]TrackerRule, 0)
	for rows.Next() {
		rule, err := scanTrackerRule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, rule)
	}
	return result, rows.Err()
}

func (s *Store) ListUnmappedTrackerIdentities(ctx context.Context) ([]UnmappedTrackerIdentity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tt.host_identity, tt.path_hint, ti.id, COALESCE(ti.content_group_id, ''), ti.last_seen_at
        FROM torrent_trackers tt
        JOIN torrent_instances ti ON ti.id = tt.instance_id
        WHERE tt.site_id IS NULL AND ti.deleted_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("list unmapped tracker identities: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type identityAggregate struct {
		item      UnmappedTrackerIdentity
		instances map[string]struct{}
		groups    map[string]struct{}
	}
	aggregates := make(map[string]*identityAggregate)
	for rows.Next() {
		var rawHost, rawPath, instanceID, groupID string
		var lastSeenAt int64
		if err := rows.Scan(&rawHost, &rawPath, &instanceID, &groupID, &lastSeenAt); err != nil {
			return nil, fmt.Errorf("scan unmapped tracker identity: %w", err)
		}
		host, err := privacySafeTrackerHost(rawHost)
		if err != nil {
			// Legacy or externally modified rows must not make the entire endpoint
			// unavailable, and must never be reflected back to the caller.
			continue
		}
		pathHint := sanitizeObservedTrackerPath(rawPath)
		key := host + "\x00" + pathHint
		aggregate := aggregates[key]
		if aggregate == nil {
			aggregate = &identityAggregate{
				item:      UnmappedTrackerIdentity{HostIdentity: host, PathHint: pathHint},
				instances: make(map[string]struct{}),
				groups:    make(map[string]struct{}),
			}
			aggregates[key] = aggregate
		}
		aggregate.instances[instanceID] = struct{}{}
		if groupID != "" {
			aggregate.groups[groupID] = struct{}{}
		}
		if seenAt := time.Unix(lastSeenAt, 0).UTC(); seenAt.After(aggregate.item.LastSeenAt) {
			aggregate.item.LastSeenAt = seenAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unmapped tracker identities: %w", err)
	}

	result := make([]UnmappedTrackerIdentity, 0, len(aggregates))
	for _, aggregate := range aggregates {
		aggregate.item.InstanceCount = len(aggregate.instances)
		aggregate.item.GroupCount = len(aggregate.groups)
		result = append(result, aggregate.item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].InstanceCount != result[j].InstanceCount {
			return result[i].InstanceCount > result[j].InstanceCount
		}
		leftHost, rightHost := strings.ToLower(result[i].HostIdentity), strings.ToLower(result[j].HostIdentity)
		if leftHost != rightHost {
			return leftHost < rightHost
		}
		if result[i].PathHint != result[j].PathHint {
			return strings.ToLower(result[i].PathHint) < strings.ToLower(result[j].PathHint)
		}
		return result[i].HostIdentity < result[j].HostIdentity
	})
	return result, nil
}

func (s *Store) DeleteCustomTrackerRule(ctx context.Context, id string) error {
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM tracker_rules WHERE id = ? AND source = 'custom'", id)
		if err != nil {
			return fmt.Errorf("delete tracker rule: %w", err)
		}
		if err := requireAffected(result); err != nil {
			return err
		}
		return reclassifyTorrentTrackers(ctx, tx)
	})
}

// normalizePersistedTrackerData is an idempotent startup data migration for
// tracker identities written by older versions. Rules and observations are
// normalized in one transaction, uniqueness collisions are resolved before
// keys are rewritten, and assignments are recalculated only when data changed.
func (s *Store) normalizePersistedTrackerData(ctx context.Context) error {
	return s.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := normalizePersistedTrackerRules(ctx, tx)
		if err != nil {
			return err
		}
		_, err = normalizePersistedTorrentTrackers(ctx, tx)
		if err != nil {
			return err
		}
		// Always finish startup with a classifier pass. Migrations commit before
		// this transaction, so relying on an in-memory "migration applied" flag
		// would leave a crash window where backfilled catalog identities never
		// reach existing Tracker rows on the next launch.
		return reclassifyTorrentTrackers(ctx, tx)
	})
}

type persistedTrackerRule struct {
	id, host, path, source, siteID, displayName string
	priority                                    int
	createdAt, updatedAt                        int64
	safeHost, safePath                          string
}

func normalizePersistedTrackerRules(ctx context.Context, tx *sql.Tx) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT r.id, r.host_pattern, r.path_prefix, r.source, r.site_id,
		       r.priority, r.created_at, r.updated_at, s.display_name
		FROM tracker_rules r JOIN sites s ON s.id = r.site_id`)
	if err != nil {
		return false, fmt.Errorf("read persisted tracker rules: %w", err)
	}
	var rules []*persistedTrackerRule
	for rows.Next() {
		rule := &persistedTrackerRule{}
		if err := rows.Scan(
			&rule.id, &rule.host, &rule.path, &rule.source, &rule.siteID,
			&rule.priority, &rule.createdAt, &rule.updatedAt, &rule.displayName,
		); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("scan persisted tracker rule: %w", err)
		}
		rule.safeHost, err = canonicalizeTrackerRuleHost(rule.host)
		if err == nil {
			rule.safePath = sanitizePathHint(rule.path)
		}
		rules = append(rules, rule)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close persisted tracker rules: %w", err)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate persisted tracker rules: %w", err)
	}

	changed := false
	deleteIDs := make(map[string]struct{})
	buckets := make(map[string][]*persistedTrackerRule)
	for _, rule := range rules {
		if rule.safeHost == "" {
			deleteIDs[rule.id] = struct{}{}
			changed = true
			continue
		}
		key := rule.safeHost + "\x00" + rule.safePath + "\x00" + rule.source
		buckets[key] = append(buckets[key], rule)
	}
	for _, bucket := range buckets {
		if len(bucket) < 2 {
			continue
		}
		siteIDs := make(map[string]struct{})
		for _, rule := range bucket {
			siteIDs[rule.siteID] = struct{}{}
		}
		if len(siteIDs) > 1 {
			// Collapsing credential-bearing hosts made the rules ambiguous. Do
			// not silently map a tracker to an arbitrary site; remove every
			// conflicting rule so the administrator can confirm the mapping.
			for _, rule := range bucket {
				deleteIDs[rule.id] = struct{}{}
			}
			changed = true
			continue
		}
		sort.Slice(bucket, func(i, j int) bool {
			iUnchanged := bucket[i].host == bucket[i].safeHost && bucket[i].path == bucket[i].safePath
			jUnchanged := bucket[j].host == bucket[j].safeHost && bucket[j].path == bucket[j].safePath
			if iUnchanged != jUnchanged {
				return iUnchanged
			}
			if bucket[i].priority != bucket[j].priority {
				return bucket[i].priority > bucket[j].priority
			}
			if bucket[i].updatedAt != bucket[j].updatedAt {
				return bucket[i].updatedAt > bucket[j].updatedAt
			}
			if bucket[i].createdAt != bucket[j].createdAt {
				return bucket[i].createdAt > bucket[j].createdAt
			}
			left, right := strings.ToLower(bucket[i].displayName), strings.ToLower(bucket[j].displayName)
			if left != right {
				return left < right
			}
			return bucket[i].id < bucket[j].id
		})
		for _, duplicate := range bucket[1:] {
			deleteIDs[duplicate.id] = struct{}{}
			changed = true
		}
	}

	for id := range deleteIDs {
		if _, err := tx.ExecContext(ctx, "DELETE FROM tracker_rules WHERE id = ?", id); err != nil {
			return false, fmt.Errorf("remove conflicting persisted tracker rule: %w", err)
		}
	}
	for _, rule := range rules {
		if _, deleted := deleteIDs[rule.id]; deleted || rule.safeHost == "" {
			continue
		}
		if rule.host == rule.safeHost && rule.path == rule.safePath {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE tracker_rules SET host_pattern = ?, path_prefix = ? WHERE id = ?`,
			rule.safeHost, rule.safePath, rule.id,
		); err != nil {
			return false, fmt.Errorf("normalize persisted tracker rule: %w", err)
		}
		changed = true
	}
	return changed, nil
}

type persistedTorrentTracker struct {
	instanceID, host, path string
	safeHost, safePath     string
}

func normalizePersistedTorrentTrackers(ctx context.Context, tx *sql.Tx) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT instance_id, host_identity, path_hint FROM torrent_trackers`)
	if err != nil {
		return false, fmt.Errorf("read persisted torrent trackers: %w", err)
	}
	var trackers []*persistedTorrentTracker
	for rows.Next() {
		tracker := &persistedTorrentTracker{}
		if err := rows.Scan(&tracker.instanceID, &tracker.host, &tracker.path); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("scan persisted torrent tracker: %w", err)
		}
		tracker.safeHost, err = privacySafeTrackerHost(tracker.host)
		if err == nil {
			tracker.safePath = sanitizeObservedTrackerPath(tracker.path)
		}
		trackers = append(trackers, tracker)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close persisted torrent trackers: %w", err)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate persisted torrent trackers: %w", err)
	}

	changed := false
	deleteKeys := make(map[string]*persistedTorrentTracker)
	buckets := make(map[string][]*persistedTorrentTracker)
	for _, tracker := range trackers {
		rawKey := tracker.instanceID + "\x00" + tracker.host + "\x00" + tracker.path
		if tracker.safeHost == "" {
			deleteKeys[rawKey] = tracker
			changed = true
			continue
		}
		key := tracker.instanceID + "\x00" + tracker.safeHost + "\x00" + tracker.safePath
		buckets[key] = append(buckets[key], tracker)
	}
	for _, bucket := range buckets {
		if len(bucket) < 2 {
			continue
		}
		sort.Slice(bucket, func(i, j int) bool {
			iUnchanged := bucket[i].host == bucket[i].safeHost && bucket[i].path == bucket[i].safePath
			jUnchanged := bucket[j].host == bucket[j].safeHost && bucket[j].path == bucket[j].safePath
			if iUnchanged != jUnchanged {
				return iUnchanged
			}
			if bucket[i].host != bucket[j].host {
				return bucket[i].host < bucket[j].host
			}
			return bucket[i].path < bucket[j].path
		})
		for _, duplicate := range bucket[1:] {
			rawKey := duplicate.instanceID + "\x00" + duplicate.host + "\x00" + duplicate.path
			deleteKeys[rawKey] = duplicate
			changed = true
		}
	}
	for _, tracker := range deleteKeys {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM torrent_trackers
			WHERE instance_id = ? AND host_identity = ? AND path_hint = ?`,
			tracker.instanceID, tracker.host, tracker.path,
		); err != nil {
			return false, fmt.Errorf("remove duplicate persisted torrent tracker: %w", err)
		}
	}
	for _, bucket := range buckets {
		tracker := bucket[0]
		rawKey := tracker.instanceID + "\x00" + tracker.host + "\x00" + tracker.path
		if _, deleted := deleteKeys[rawKey]; deleted ||
			(tracker.host == tracker.safeHost && tracker.path == tracker.safePath) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE torrent_trackers SET host_identity = ?, path_hint = ?
			WHERE instance_id = ? AND host_identity = ? AND path_hint = ?`,
			tracker.safeHost, tracker.safePath, tracker.instanceID, tracker.host, tracker.path,
		); err != nil {
			return false, fmt.Errorf("normalize persisted torrent tracker: %w", err)
		}
		changed = true
	}
	return changed, nil
}

// reclassifyTorrentTrackers gives explicit tracker rules first refusal, then
// applies the three privacy-safe IYUU catalog match levels. Every automatic
// level must identify exactly one site; ambiguity is deliberately left
// unmapped for an administrator to resolve.
func reclassifyTorrentTrackers(ctx context.Context, tx *sql.Tx) error {
	rules, err := trackerClassificationRules(ctx, tx)
	if err != nil {
		return err
	}
	iyuuSites, err := iyuuTrackerMatchSites(ctx, tx)
	if err != nil {
		return err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT instance_id, host_identity, path_hint, site_id, match_type
		FROM torrent_trackers`)
	if err != nil {
		return fmt.Errorf("read torrent trackers for reclassification: %w", err)
	}
	type persistedClassification struct {
		instanceID, host, path, matchType string
		siteID                            sql.NullString
	}
	trackers := make([]persistedClassification, 0)
	for rows.Next() {
		var tracker persistedClassification
		if err := rows.Scan(
			&tracker.instanceID, &tracker.host, &tracker.path,
			&tracker.siteID, &tracker.matchType,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan torrent tracker for reclassification: %w", err)
		}
		trackers = append(trackers, tracker)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate torrent trackers for reclassification: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close torrent trackers for reclassification: %w", err)
	}

	for _, tracker := range trackers {
		siteID, matchType := classifyPersistedTracker(tracker.host, tracker.path, rules, iyuuSites)
		if tracker.siteID.Valid == (siteID != "") && tracker.siteID.String == siteID && tracker.matchType == matchType {
			continue
		}
		var nullableSiteID any
		if siteID != "" {
			nullableSiteID = siteID
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE torrent_trackers
			SET site_id = ?, match_type = ?
			WHERE instance_id = ? AND host_identity = ? AND path_hint = ?`,
			nullableSiteID, matchType, tracker.instanceID, tracker.host, tracker.path,
		); err != nil {
			return fmt.Errorf("reclassify torrent tracker: %w", err)
		}
	}
	return nil
}

type trackerClassificationRule struct {
	hostPattern, pathPrefix, siteID string
}

func trackerClassificationRules(ctx context.Context, tx *sql.Tx) ([]trackerClassificationRule, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT r.host_pattern, r.path_prefix, r.site_id
		FROM tracker_rules r
		JOIN sites s ON s.id = r.site_id
		ORDER BY r.priority DESC, s.display_name COLLATE NOCASE,
		         r.host_pattern, r.path_prefix, r.id`)
	if err != nil {
		return nil, fmt.Errorf("read tracker rules for reclassification: %w", err)
	}
	defer func() { _ = rows.Close() }()
	rules := make([]trackerClassificationRule, 0)
	for rows.Next() {
		var rule trackerClassificationRule
		if err := rows.Scan(&rule.hostPattern, &rule.pathPrefix, &rule.siteID); err != nil {
			return nil, fmt.Errorf("scan tracker rule for reclassification: %w", err)
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tracker rules for reclassification: %w", err)
	}
	return rules, nil
}

type iyuuTrackerMatchSite struct {
	siteID, host, registrableDomain, keyword string
}

func iyuuTrackerMatchSites(ctx context.Context, tx *sql.Tx) ([]iyuuTrackerMatchSite, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, base_url
		FROM sites
		WHERE source = 'iyuu' AND iyuu_remote_id IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("read IYUU sites for tracker matching: %w", err)
	}
	defer func() { _ = rows.Close() }()
	sites := make([]iyuuTrackerMatchSite, 0)
	for rows.Next() {
		var site iyuuTrackerMatchSite
		var rawHost string
		if err := rows.Scan(&site.siteID, &rawHost); err != nil {
			return nil, fmt.Errorf("scan IYUU site for tracker matching: %w", err)
		}
		host, err := normalizeTrackerHost(rawHost)
		if err != nil || strings.HasPrefix(host, "*.") || net.ParseIP(host) != nil {
			continue
		}
		registrableDomain, err := publicsuffix.EffectiveTLDPlusOne(host)
		if err != nil {
			continue
		}
		site.host = host
		site.registrableDomain = strings.ToLower(registrableDomain)
		site.keyword = trackerDomainKeyword(site.registrableDomain)
		sites = append(sites, site)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate IYUU sites for tracker matching: %w", err)
	}
	return sites, nil
}

func classifyPersistedTracker(
	host, pathHint string,
	rules []trackerClassificationRule,
	iyuuSites []iyuuTrackerMatchSite,
) (siteID, matchType string) {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, rule := range rules {
		pattern := strings.ToLower(strings.TrimSpace(rule.hostPattern))
		hostMatches := host == pattern
		if strings.HasPrefix(pattern, "*.") {
			suffix := strings.TrimPrefix(pattern, "*")
			hostMatches = strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".")
		}
		if !hostMatches || (rule.pathPrefix != "" && !strings.HasPrefix(pathHint, rule.pathPrefix)) {
			continue
		}
		if !strings.HasPrefix(pattern, "*.") && rule.pathPrefix == "" {
			return rule.siteID, "exact"
		}
		return rule.siteID, "custom"
	}

	if siteID, state := uniqueIYUUSiteMatch(iyuuSites, func(site iyuuTrackerMatchSite) bool {
		return site.host == host
	}); state == iyuuMatchUnique {
		return siteID, "exact"
	} else if state == iyuuMatchAmbiguous {
		return "", ""
	}
	registrableDomain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", ""
	}
	registrableDomain = strings.ToLower(registrableDomain)
	if siteID, state := uniqueIYUUSiteMatch(iyuuSites, func(site iyuuTrackerMatchSite) bool {
		return site.registrableDomain == registrableDomain
	}); state == iyuuMatchUnique {
		return siteID, "registrable_domain"
	} else if state == iyuuMatchAmbiguous {
		return "", ""
	}
	keyword := trackerDomainKeyword(registrableDomain)
	if len(keyword) < 3 {
		return "", ""
	}
	if siteID, state := uniqueIYUUSiteMatch(iyuuSites, func(site iyuuTrackerMatchSite) bool {
		return len(site.keyword) >= 3 && site.keyword == keyword
	}); state == iyuuMatchUnique {
		return siteID, "keyword"
	}
	return "", ""
}

type iyuuMatchState uint8

const (
	iyuuMatchNone iyuuMatchState = iota
	iyuuMatchUnique
	iyuuMatchAmbiguous
)

func uniqueIYUUSiteMatch(sites []iyuuTrackerMatchSite, matches func(iyuuTrackerMatchSite) bool) (string, iyuuMatchState) {
	matchedSite := ""
	for _, site := range sites {
		if !matches(site) {
			continue
		}
		if matchedSite != "" && matchedSite != site.siteID {
			return "", iyuuMatchAmbiguous
		}
		matchedSite = site.siteID
	}
	if matchedSite == "" {
		return "", iyuuMatchNone
	}
	return matchedSite, iyuuMatchUnique
}

// trackerDomainKeyword strips punctuation from the registrable domain's first
// label. Keeping the input at the eTLD+1 level prevents generic subdomains such
// as tracker, announce, or pt from becoming the match key.
func trackerDomainKeyword(registrableDomain string) string {
	label, _, _ := strings.Cut(strings.ToLower(registrableDomain), ".")
	var result strings.Builder
	for _, character := range label {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			result.WriteRune(character)
		}
	}
	return result.String()
}

func scanTrackerRule(row scanner) (TrackerRule, error) {
	var rule TrackerRule
	var createdAt, updatedAt int64
	if err := row.Scan(
		&rule.ID, &rule.HostPattern, &rule.PathPrefix, &rule.SiteID,
		&rule.SiteName, &rule.DisplayName, &rule.Source, &rule.Priority,
		&createdAt, &updatedAt,
	); err != nil {
		return TrackerRule{}, err
	}
	rule.CreatedAt = time.Unix(createdAt, 0).UTC()
	rule.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return rule, nil
}

// TrackerIdentity strips credentials, query parameters, fragments, and ports.
// Only the normalized host and a static path hint are suitable for persistence.
func TrackerIdentity(raw string) (host, pathHint string, err error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "", "", errors.New("invalid tracker URL")
	}
	host, err = privacySafeTrackerHost(parsed.Hostname())
	if err != nil {
		return "", "", err
	}
	return host, sanitizeObservedTrackerPath(parsed.EscapedPath()), nil
}

// canonicalizeTrackerRuleHost projects a rule into the same privacy-safe host
// identity used for observations. Ordinary wildcard rules remain wildcards.
// When a wildcard suffix itself contains a credential-like label, its observed
// hosts collapse to one exact _redacted identity, so the rule must do the same
// or it could never match during sync or persisted reclassification.
func canonicalizeTrackerRuleHost(value string) (string, error) {
	host, err := normalizeTrackerHost(value)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(host, "*.") {
		return privacySafeTrackerHost(host)
	}
	suffix := strings.TrimPrefix(host, "*.")
	safeSuffix, err := privacySafeTrackerHost(suffix)
	if err != nil {
		return "", err
	}
	if safeSuffix != suffix {
		return safeSuffix, nil
	}
	return host, nil
}

// privacySafeTrackerHost retains ordinary tracker hosts while collapsing a
// likely credential-bearing host label into an exact synthetic identity. For
// example, a per-user announce host such as "<passkey>.tracker.example.com"
// becomes "_redacted.tracker.example.com". The placeholder is deliberately
// not a wildcard: a rule created from it can classify equivalent redacted
// identities without matching unrelated real hosts under the same suffix.
func privacySafeTrackerHost(value string) (string, error) {
	host, err := normalizeTrackerHost(value)
	if err != nil {
		return "", err
	}
	if net.ParseIP(host) != nil {
		return host, nil
	}
	// Explicit wildcard rules are administrator-authored matching policy, not
	// observed identities. Preserve them byte-for-byte after normalization even
	// when a long suffix label happens to resemble a credential.
	if strings.HasPrefix(host, "*.") {
		return host, nil
	}

	labels := strings.Split(host, ".")
	lastSensitive := -1
	for index, label := range labels {
		if isSensitiveTrackerHostLabel(label) || isShortTokenBeforeTrackerRole(labels, index) {
			lastSensitive = index
		}
	}
	if lastSensitive < 0 {
		return host, nil
	}
	suffix := labels[lastSensitive+1:]
	if len(suffix) == 0 {
		return "_redacted", nil
	}
	return "_redacted." + strings.Join(suffix, "."), nil
}

func isSensitiveTrackerHostLabel(label string) bool {
	lower := strings.ToLower(strings.TrimSpace(label))
	if lower == "" || lower == "*" {
		return false
	}
	for _, marker := range []string{"passkey", "authkey", "apikey", "token", "secret"} {
		if strings.Contains(lower, marker) && len(lower) >= len(marker)+6 {
			return true
		}
	}

	compact := strings.NewReplacer("-", "", "_", "").Replace(lower)
	if len(compact) < 12 {
		return false
	}
	unique := make(map[rune]struct{})
	digits := 0
	hexOnly := true
	alphaNumeric := true
	for _, character := range compact {
		unique[character] = struct{}{}
		switch {
		case character >= 'a' && character <= 'z':
		case character >= '0' && character <= '9':
			digits++
		default:
			alphaNumeric = false
		}
		if (character < 'a' || character > 'f') && (character < '0' || character > '9') {
			hexOnly = false
		}
	}
	if len(compact) < 16 {
		return alphaNumeric && (digits == len(compact) ||
			digits >= 5 && digits < len(compact) && len(unique) >= 8)
	}
	if !alphaNumeric {
		return len(compact) >= 24
	}
	if digits == len(compact) || (hexOnly && len(compact) >= 20) {
		return true
	}
	if len(compact) >= 16 && digits >= 4 && len(unique) >= 8 {
		return true
	}
	if len(compact) >= 16 && len(compact) < 24 && len(unique)*4 >= len(compact)*3 {
		return true
	}
	return len(compact) >= 24 && len(unique) >= 8
}

func isShortTokenBeforeTrackerRole(labels []string, index int) bool {
	compact := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(strings.TrimSpace(labels[index])))
	if len(compact) < 12 || len(compact) >= 16 {
		return false
	}
	unique := make(map[rune]struct{})
	for _, character := range compact {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
		unique[character] = struct{}{}
	}
	if len(unique) < 8 {
		return false
	}
	for _, label := range labels[index+1:] {
		lower := strings.ToLower(label)
		if strings.HasPrefix(lower, "tracker") || strings.HasPrefix(lower, "announce") {
			return true
		}
	}
	return false
}

func normalizeTrackerHost(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if parsed, err := url.Parse(value); err == nil && parsed.Hostname() != "" {
		value = parsed.Hostname()
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.TrimSuffix(value, ".")
	if value == "" || strings.ContainsAny(value, "/?#@") {
		return "", errors.New("invalid tracker host pattern")
	}
	return value, nil
}

func sanitizePathHint(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return ""
	}
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		value = value[:index]
	}
	segments := strings.Split(value, "/")
	for i, segment := range segments {
		lower := strings.ToLower(segment)
		if len(segment) >= 24 || strings.Contains(lower, "passkey") || strings.Contains(lower, "token") || strings.Contains(lower, "auth") {
			segments[i] = "*"
		}
	}
	return strings.Join(segments, "/")
}

// sanitizeObservedTrackerPath uses an allowlist because an arbitrary path
// segment may itself be a passkey even when it is short or has no revealing
// name. User-authored rules still accept explicit static prefixes through
// sanitizePathHint; remotely observed values retain only common endpoint
// structure.
func sanitizeObservedTrackerPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return ""
	}
	allowed := map[string]struct{}{
		"announce": {}, "announce.php": {}, "tracker": {}, "tracker.php": {},
		"api": {}, "v1": {}, "v2": {}, "v3": {}, "bt": {}, "torrent": {},
	}
	segments := strings.Split(value, "/")
	lastRedacted := false
	for index, segment := range segments {
		if segment == "" {
			continue
		}
		lower := strings.ToLower(segment)
		if _, ok := allowed[lower]; ok {
			lastRedacted = false
			continue
		}
		if lastRedacted {
			segments[index] = ""
			continue
		}
		segments[index] = "*"
		lastRedacted = true
	}
	result := strings.Join(segments, "/")
	for strings.Contains(result, "//") {
		result = strings.ReplaceAll(result, "//", "/")
	}
	return strings.TrimSuffix(result, "/")
}
