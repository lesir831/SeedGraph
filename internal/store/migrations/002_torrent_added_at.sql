ALTER TABLE torrent_instances ADD COLUMN added_at INTEGER;

UPDATE torrent_instances
SET added_at = first_seen_at
WHERE added_at IS NULL;
