CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS storages (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS downloaders (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('qbittorrent', 'transmission')),
    base_url TEXT NOT NULL,
    username_ciphertext TEXT NOT NULL DEFAULT '',
    password_ciphertext TEXT NOT NULL DEFAULT '',
    storage_id TEXT NOT NULL REFERENCES storages(id),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    online INTEGER NOT NULL DEFAULT 0 CHECK (online IN (0, 1)),
    version TEXT NOT NULL DEFAULT '',
    sync_cursor TEXT NOT NULL DEFAULT '',
    last_success_at INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS path_mappings (
    id TEXT PRIMARY KEY,
    downloader_id TEXT NOT NULL REFERENCES downloaders(id) ON DELETE CASCADE,
    source_prefix TEXT NOT NULL,
    target_prefix TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    UNIQUE (downloader_id, source_prefix)
);

CREATE TABLE IF NOT EXISTS content_groups (
    id TEXT PRIMARY KEY,
    auto_key TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL,
    mode TEXT NOT NULL DEFAULT 'auto' CHECK (mode IN ('auto', 'manual')),
    confidence TEXT NOT NULL DEFAULT 'tentative' CHECK (confidence IN ('verified', 'tentative', 'manual', 'conflict')),
    locked INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0, 1)),
    version INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    deleted_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_content_groups_auto_key ON content_groups(auto_key) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS data_groups (
    id TEXT PRIMARY KEY,
    version INTEGER NOT NULL DEFAULT 1,
    auto_key TEXT NOT NULL UNIQUE,
    storage_id TEXT NOT NULL REFERENCES storages(id),
    canonical_path TEXT NOT NULL,
    wanted_bytes INTEGER NOT NULL CHECK (wanted_bytes >= 0),
    manifest_fingerprint TEXT NOT NULL DEFAULT '',
    selected_file_count INTEGER NOT NULL DEFAULT 0,
    confidence TEXT NOT NULL DEFAULT 'tentative' CHECK (confidence IN ('verified', 'tentative', 'manual', 'conflict')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_data_groups_lookup
    ON data_groups(storage_id, canonical_path, wanted_bytes);

CREATE TABLE IF NOT EXISTS torrent_instances (
    id TEXT PRIMARY KEY,
    downloader_id TEXT NOT NULL REFERENCES downloaders(id) ON DELETE CASCADE,
    stable_hash_key TEXT NOT NULL,
    remote_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    source_path TEXT NOT NULL,
    canonical_path TEXT NOT NULL,
    storage_id TEXT NOT NULL REFERENCES storages(id),
    wanted_bytes INTEGER NOT NULL CHECK (wanted_bytes >= 0),
    manifest_fingerprint TEXT NOT NULL DEFAULT '',
    selected_file_count INTEGER NOT NULL DEFAULT 0,
    metadata_fingerprint TEXT NOT NULL DEFAULT '',
    suggested_content_group_id TEXT NOT NULL DEFAULT '',
    suggested_content_auto_key TEXT NOT NULL DEFAULT '',
    content_group_id TEXT REFERENCES content_groups(id),
    data_group_id TEXT REFERENCES data_groups(id),
    assignment_source TEXT NOT NULL DEFAULT 'auto' CHECK (assignment_source IN ('auto', 'manual')),
    first_seen_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    deleted_at INTEGER,
    UNIQUE (downloader_id, stable_hash_key)
);

CREATE INDEX IF NOT EXISTS idx_torrent_instances_content_group
    ON torrent_instances(content_group_id, deleted_at);
CREATE INDEX IF NOT EXISTS idx_torrent_instances_data_group
    ON torrent_instances(data_group_id, deleted_at);
CREATE INDEX IF NOT EXISTS idx_torrent_instances_path_size
    ON torrent_instances(storage_id, canonical_path, wanted_bytes, deleted_at);

CREATE TABLE IF NOT EXISTS torrent_runtime (
    instance_id TEXT PRIMARY KEY REFERENCES torrent_instances(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'unknown',
    progress REAL NOT NULL DEFAULT 0,
    ratio REAL NOT NULL DEFAULT 0,
    uploaded_bytes INTEGER NOT NULL DEFAULT 0,
    downloaded_bytes INTEGER NOT NULL DEFAULT 0,
    upload_speed INTEGER NOT NULL DEFAULT 0,
    download_speed INTEGER NOT NULL DEFAULT 0,
    runtime_fingerprint TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sites (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    base_url TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL CHECK (source IN ('custom', 'iyuu', 'inferred')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS tracker_rules (
    id TEXT PRIMARY KEY,
    host_pattern TEXT NOT NULL,
    path_prefix TEXT NOT NULL DEFAULT '',
    site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('custom', 'iyuu', 'inferred')),
    priority INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (host_pattern, path_prefix, source)
);

CREATE INDEX IF NOT EXISTS idx_tracker_rules_match
    ON tracker_rules(priority DESC, host_pattern, path_prefix);

CREATE TABLE IF NOT EXISTS iyuu_sites (
    remote_id INTEGER PRIMARY KEY CHECK (remote_id > 0),
    slug TEXT NOT NULL UNIQUE,
    nickname TEXT NOT NULL DEFAULT '',
    base_url TEXT NOT NULL,
    download_page TEXT NOT NULL DEFAULT '',
    details_page TEXT NOT NULL DEFAULT '',
    is_https INTEGER NOT NULL CHECK (is_https BETWEEN 0 AND 2),
    cookie_required INTEGER NOT NULL CHECK (cookie_required IN (0, 1)),
    first_seen_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_iyuu_sites_last_seen ON iyuu_sites(last_seen_at DESC);

CREATE TABLE IF NOT EXISTS iyuu_sync_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    last_attempt_at INTEGER,
    last_success_at INTEGER,
    last_error TEXT NOT NULL DEFAULT '',
    site_count INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS torrent_trackers (
    instance_id TEXT NOT NULL REFERENCES torrent_instances(id) ON DELETE CASCADE,
    host_identity TEXT NOT NULL,
    path_hint TEXT NOT NULL DEFAULT '',
    site_id TEXT REFERENCES sites(id) ON DELETE SET NULL,
    PRIMARY KEY (instance_id, host_identity, path_hint)
);

CREATE INDEX IF NOT EXISTS idx_torrent_trackers_site ON torrent_trackers(site_id);

CREATE TABLE IF NOT EXISTS group_operations (
    id TEXT PRIMARY KEY,
    operation_type TEXT NOT NULL,
    content_group_id TEXT NOT NULL REFERENCES content_groups(id),
    before_version INTEGER NOT NULL,
    after_version INTEGER NOT NULL,
    payload_json TEXT NOT NULL,
    undone_at INTEGER,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_runs (
    id TEXT PRIMARY KEY,
    downloader_id TEXT NOT NULL REFERENCES downloaders(id) ON DELETE CASCADE,
    mode TEXT NOT NULL CHECK (mode IN ('full', 'delta', 'manual')),
    status TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed')),
    complete INTEGER NOT NULL DEFAULT 0 CHECK (complete IN (0, 1)),
    seen_count INTEGER NOT NULL DEFAULT 0,
    changed_count INTEGER NOT NULL DEFAULT 0,
    removed_count INTEGER NOT NULL DEFAULT 0,
    cursor_before TEXT NOT NULL DEFAULT '',
    cursor_after TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL,
    finished_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_sync_runs_downloader_started
    ON sync_runs(downloader_id, started_at DESC);

CREATE TABLE IF NOT EXISTS delete_plans (
    id TEXT PRIMARY KEY,
    selection_json TEXT NOT NULL,
    snapshot_json TEXT NOT NULL,
    blocked INTEGER NOT NULL DEFAULT 0 CHECK (blocked IN (0, 1)),
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS delete_jobs (
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL REFERENCES delete_plans(id),
    idempotency_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'validating', 'executing', 'verifying', 'completed', 'failed', 'uncertain')),
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    completed_at INTEGER
);

CREATE TABLE IF NOT EXISTS delete_job_steps (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES delete_jobs(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    instance_id TEXT NOT NULL REFERENCES torrent_instances(id),
    downloader_id TEXT NOT NULL REFERENCES downloaders(id),
    delete_data INTEGER NOT NULL DEFAULT 0 CHECK (delete_data IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('pending', 'executing', 'completed', 'failed', 'uncertain')),
    error TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    UNIQUE (job_id, position)
);

CREATE TABLE IF NOT EXISTS audit_logs (
    id TEXT PRIMARY KEY,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'success' CHECK (status IN ('success', 'failed', 'warning')),
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL,
    details_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_created ON audit_logs(created_at DESC);
