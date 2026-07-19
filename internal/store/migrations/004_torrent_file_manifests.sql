ALTER TABLE torrent_instances
    ADD COLUMN file_manifest_known INTEGER NOT NULL DEFAULT 0 CHECK (file_manifest_known IN (0, 1));

CREATE TABLE torrent_files (
    instance_id TEXT NOT NULL REFERENCES torrent_instances(id) ON DELETE CASCADE,
    source_path TEXT NOT NULL,
    canonical_path TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    selected INTEGER NOT NULL CHECK (selected IN (0, 1)),
    PRIMARY KEY (instance_id, canonical_path)
);

CREATE INDEX idx_torrent_files_canonical_path
    ON torrent_files(canonical_path, instance_id);
