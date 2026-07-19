ALTER TABLE sites ADD COLUMN iyuu_remote_id INTEGER;

-- Upgrade installations which already have a cached IYUU catalog without
-- waiting for another successful upstream request. Reuse a site created by a
-- custom rule when its stable slug matches, then create the remaining catalog
-- identities in the application-wide sites table.
UPDATE sites
SET display_name = COALESCE(NULLIF((
        SELECT iy.nickname FROM iyuu_sites iy WHERE iy.slug = sites.name
    ), ''), sites.display_name),
    base_url = (
        SELECT iy.base_url FROM iyuu_sites iy WHERE iy.slug = sites.name
    ),
    source = 'iyuu',
    iyuu_remote_id = (
        SELECT iy.remote_id FROM iyuu_sites iy WHERE iy.slug = sites.name
    ),
    updated_at = MAX(updated_at, (
        SELECT iy.last_seen_at FROM iyuu_sites iy WHERE iy.slug = sites.name
    ))
WHERE EXISTS (
    SELECT 1 FROM iyuu_sites iy WHERE iy.slug = sites.name
);

INSERT INTO sites(
    id, name, display_name, base_url, source, iyuu_remote_id, created_at, updated_at
)
SELECT 'iyuu:' || iy.remote_id,
       iy.slug,
       COALESCE(NULLIF(iy.nickname, ''), iy.slug),
       iy.base_url,
       'iyuu',
       iy.remote_id,
       iy.first_seen_at,
       iy.last_seen_at
FROM iyuu_sites iy
WHERE NOT EXISTS (
    SELECT 1 FROM sites s WHERE s.name = iy.slug
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_sites_iyuu_remote_id
    ON sites(iyuu_remote_id)
    WHERE iyuu_remote_id IS NOT NULL;

ALTER TABLE torrent_trackers ADD COLUMN match_type TEXT NOT NULL DEFAULT ''
    CHECK (match_type IN ('', 'exact', 'registrable_domain', 'keyword', 'custom'));

-- Existing assignments predate match provenance. Preserve them as mapped and
-- recover the strongest type that can be proven from an exact, pathless rule;
-- the startup reclassifier refines every row after this migration commits.
UPDATE torrent_trackers
SET match_type = CASE
    WHEN EXISTS (
        SELECT 1
        FROM tracker_rules r
        WHERE r.site_id = torrent_trackers.site_id
          AND lower(r.host_pattern) = lower(torrent_trackers.host_identity)
          AND r.path_prefix = ''
    ) THEN 'exact'
    ELSE 'custom'
END
WHERE site_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_torrent_trackers_match_type
    ON torrent_trackers(match_type);
