CREATE TABLE IF NOT EXISTS agent_rules (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_key TEXT,
    name        TEXT NOT NULL,
    content     TEXT NOT NULL,
    priority    INTEGER NOT NULL DEFAULT 100,
    enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS agent_rules_scope_name
    ON agent_rules (COALESCE(project_key, ''), name);

CREATE INDEX IF NOT EXISTS agent_rules_lookup
    ON agent_rules (project_key, enabled, priority, id);

CREATE TABLE IF NOT EXISTS agent_skills (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_key TEXT,
    name        TEXT NOT NULL,
    description TEXT NOT NULL,
    content     TEXT NOT NULL,
    priority    INTEGER NOT NULL DEFAULT 100,
    enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS agent_skills_scope_name
    ON agent_skills (COALESCE(project_key, ''), name);

CREATE INDEX IF NOT EXISTS agent_skills_lookup
    ON agent_skills (project_key, enabled, priority, id);

