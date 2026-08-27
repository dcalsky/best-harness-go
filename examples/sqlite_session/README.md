# Application-defined SQLite session persistence

This example shows how application code can implement `harness.Persistence` with SQLite. SQLite session persistence is not built into the SDK; the complete implementation lives in this example and does not create JSONL files.

```go
database, err := sqlitesession.OpenDatabase("./sessions.db")
if err != nil {
    return err
}
defer database.Close()

if err := database.Initialize(ctx); err != nil {
    return err
}

manager, err := database.NewManager(harness.PersistenceOptions{
    ID:  "task-42",
    Cwd: "/work/project",
})
if err != nil {
    return err
}

running, err := harness.NewSessionWithManager(ctx, manager, harness.SessionOptions{
    Model: &selected,
})
if err != nil {
    return err
}
defer running.Close()
```

Restore by session ID:

```go
manager, err := database.Open(ctx, "task-42")
if err != nil {
    return err
}
running, err := harness.NewSessionWithManager(ctx, manager, harness.SessionOptions{})
```

List and resume:

```go
items, err := database.List(ctx, "/work/project")
manager, err := database.ResumeLatest(ctx, "/work/project")
```

When used through `Session.Start`, the SQLite row is created with the `run_start` entry so interrupted runs remain auditable. A Manager used directly without Run entries still delays creation until the first assistant message. One process may have only one active writer for a session ID. Forks use the same database through `Persistence.Fork()`.
