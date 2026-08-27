# SQLite resource loader

This example stores global and project-specific RULES and SKILLS in one SQLite file.

Open the file and create the tables:

```go
db, err := sqliteresources.Open("./agent-resources.db")
if err != nil {
    return err
}
defer db.Close()

if err := sqliteresources.Initialize(ctx, db); err != nil {
    return err
}
```

Insert resources:

```sql
INSERT INTO agent_rules (project_key, name, content, priority)
VALUES
  (NULL, 'format', 'Run gofmt on changed Go files.', 10),
  ('billing-api', 'billing-tests', 'Run go test ./billing/... after changes.', 20);

INSERT INTO agent_skills (project_key, name, description, content, priority)
VALUES
  (
    NULL,
    'review',
    'Review Go changes',
    '---
name: review
description: Review Go changes
---
Check errors, cancellation, and tests.
',
    10
  );
```

Register the loader:

```go
resources := harness.NewResourceRegistry()
resources.Register(sqliteresources.Loader{
    Store:      sqliteresources.SQLiteStore{DB: db},
    ProjectKey: "billing-api",
    CacheDir:   "/var/run/my-app/skills",
})
```

Rows with a null `project_key` apply to every project. Project rows load after global rows. A project SKILL with the same name replaces the global SKILL.

RULES become `ProjectInstructions`. Each SKILL is copied to `CacheDir/<id>-<content-hash>/SKILL.md`, and that path becomes its `Location`. The content hash keeps an older session pinned to the version it loaded. The cache directory should be private to the application, and the registered `read` tool must be allowed to read it.
