package e2e_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
	sqlitesession "github.com/dcalsky/best-harness-go/examples/sqlite_session"
)

type sqliteSessionProvider struct {
	mu       sync.Mutex
	requests []harness.Request
}

func (p *sqliteSessionProvider) Stream(_ context.Context, request harness.Request) (harness.Stream, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	call := len(p.requests)
	p.mu.Unlock()
	text := "SQLITE_SESSION_FIRST_OK"
	if call > 1 {
		text = "SQLITE_SESSION_RESTORED_OK"
	}
	return &harness.SliceStream{Events: []harness.StreamEvent{
		{Type: harness.EventTextDelta, Text: text},
		{Type: harness.EventDone, StopReason: harness.StopStop},
	}}, nil
}

func (p *sqliteSessionProvider) snapshot() []harness.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]harness.Request(nil), p.requests...)
}

type sqliteSessionSummarizer struct{}

func (sqliteSessionSummarizer) Summarize(context.Context, []harness.Message, string) (harness.CompactionSummary, error) {
	return harness.CompactionSummary{Text: "SQLite stored the earlier turn."}, nil
}

func TestSQLiteSessionHarnessPersistenceE2E(t *testing.T) {
	root := t.TempDir()
	database, err := sqlitesession.OpenDatabase(filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}

	selected := harness.Model{Provider: "sqlite-session-faux", ID: "stored-model", ContextWindow: 32_000, MaxOutput: 512}
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	p := &sqliteSessionProvider{}
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RegisterProvider("sqlite-session-faux", p); err != nil {
		t.Fatal(err)
	}

	manager, err := database.NewManager(harness.PersistenceOptions{ID: "sqlite-harness-e2e", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	first, err := h.NewSessionWithManager(context.Background(), manager, harness.SessionOptions{Model: &selected})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := startSessionRun(t, ctx, first, harness.Prompt{Steps: harness.Sequence{harness.UserText("Remember SQLITE_SESSION_CONTEXT_MARKER.")}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restoredManager, err := database.Open(ctx, "sqlite-harness-e2e")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := h.NewSessionWithManager(ctx, restoredManager, harness.SessionOptions{
		Summarizer: sqliteSessionSummarizer{},
		Compaction: harness.CompactionOptions{KeepRecentTokens: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	run = startSessionRun(t, ctx, restored, harness.Prompt{Steps: harness.Sequence{harness.UserText("Continue from the stored session.")}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.AppendCustom(ctx, "sqlite-e2e", map[string]any{"stored": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.SetName("sqlite harness e2e"); err != nil {
		t.Fatal(err)
	}
	result, err := restored.Compact(ctx, harness.CompactOptions{Reason: harness.CompactionManual})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Summary, "SQLite stored") {
		t.Fatalf("summary=%q", result.Summary)
	}
	leaf := restored.Entries()[len(restored.Entries())-1].ID
	fork, err := restored.Fork(ctx, leaf, harness.SessionOptions{ID: "sqlite-harness-child"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fork.Conversation().Messages[0].Text(), harness.CompactionSummaryPrefix) {
		t.Fatalf("fork context=%#v", fork.Conversation().Messages)
	}
	if err := fork.Close(); err != nil {
		t.Fatal(err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}

	requests := p.snapshot()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	assertContextRoles(t, requests[0], harness.RoleUser)
	assertContextRoles(t, requests[1], harness.RoleUser, harness.RoleAssistant, harness.RoleUser)
	if requests[1].Messages[0].Text() != "Remember SQLITE_SESSION_CONTEXT_MARKER." {
		t.Fatalf("restored request=%#v", requests[1].Messages)
	}

	opened, err := database.Open(ctx, "sqlite-harness-e2e")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	entries := opened.Entries()
	if len(entries) == 0 || entries[len(entries)-1].Type != "compaction" || opened.Name() != "sqlite harness e2e" {
		t.Fatalf("entries=%#v name=%q", entries, opened.Name())
	}
	custom, err := opened.CustomEntries[map[string]any]("sqlite-e2e")
	if err != nil || len(custom) != 1 || custom[0].Data["stored"] != true {
		t.Fatalf("custom=%#v error=%v", custom, err)
	}
	items, err := database.List(ctx, root)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v error=%v", items, err)
	}
	jsonl, err := filepath.Glob(filepath.Join(root, "*.jsonl"))
	if err != nil || len(jsonl) != 0 {
		t.Fatalf("jsonl=%#v error=%v", jsonl, err)
	}
}

func TestDeepSeekSQLiteSessionRestoreE2E(t *testing.T) {
	p, selected := deepSeek(t)
	recorded := &providerRecorder{base: p}
	selected.MaxOutput = 512
	root := t.TempDir()
	database, err := sqlitesession.OpenDatabase(filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RegisterProvider("deepseek", recorded); err != nil {
		t.Fatal(err)
	}

	manager, err := database.NewManager(harness.PersistenceOptions{ID: "deepseek-sqlite-session", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	first, err := h.NewSessionWithManager(context.Background(), manager, harness.SessionOptions{Model: &selected, Generation: nonThinking})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := startSessionRun(t, ctx, first, harness.Prompt{Steps: harness.Sequence{harness.UserText("Remember the exact marker SQLITE_SESSION_DEEPSEEK_MEMORY. Reply FIRST_STORED_OK only.")}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restoredManager, err := database.Open(ctx, "deepseek-sqlite-session")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := h.NewSessionWithManager(ctx, restoredManager, harness.SessionOptions{Generation: nonThinking})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	run = startSessionRun(t, ctx, restored, harness.Prompt{Steps: harness.Sequence{harness.UserText("Reply SQLITE_SESSION_RESTORE_OK only.")}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if !stringsContainAssistant(restored.Conversation().Messages, "SQLITE_SESSION_RESTORE_OK") {
		t.Fatalf("messages=%#v", restored.Conversation().Messages)
	}
	requests := recorded.snapshot()
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	assertContextRoles(t, requests[1], harness.RoleUser, harness.RoleAssistant, harness.RoleUser)
	if !strings.Contains(requests[1].Messages[0].Text(), "SQLITE_SESSION_DEEPSEEK_MEMORY") ||
		!strings.Contains(requests[1].Messages[1].Text(), "FIRST_STORED_OK") {
		t.Fatalf("restored request=%#v", requests[1].Messages)
	}
	items, err := database.List(ctx, root)
	if err != nil || len(items) != 1 || items[0].MessageCount != 4 {
		t.Fatalf("items=%#v error=%v", items, err)
	}
	jsonl, err := filepath.Glob(filepath.Join(root, "*.jsonl"))
	if err != nil || len(jsonl) != 0 {
		t.Fatalf("jsonl=%#v error=%v", jsonl, err)
	}
}
