PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS sdk_sessions (
    id             TEXT PRIMARY KEY,
    version        INTEGER NOT NULL,
    header_json    BLOB NOT NULL,
    cwd            TEXT NOT NULL,
    parent_session TEXT NOT NULL DEFAULT '',
    name           TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,
    modified_at    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS sdk_sessions_cwd_modified
    ON sdk_sessions (cwd, modified_at DESC);

CREATE TABLE IF NOT EXISTS sdk_session_entries (
    session_id TEXT NOT NULL REFERENCES sdk_sessions(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    entry_id   TEXT NOT NULL,
    entry_type TEXT NOT NULL,
    entry_json BLOB NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (session_id, position),
    UNIQUE (session_id, entry_id)
);

CREATE INDEX IF NOT EXISTS sdk_session_entries_lookup
    ON sdk_session_entries (session_id, position);
