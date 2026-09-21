CREATE TABLE group_search_presets (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    filter_json TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
