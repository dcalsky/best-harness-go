package e2e_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
	sqliteresources "github.com/dcalsky/best-harness-go/examples/sqlite_resources"
)

func prepareSQLiteResources(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	root := t.TempDir()
	db, err := sqliteresources.Open(filepath.Join(root, "resources.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliteresources.Initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "skill-cache")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	return db, cache, project
}

func insertSQLiteResource(t *testing.T, db *sql.DB, rule, marker string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO agent_rules(project_key,name,content,priority)
VALUES('sqlite-e2e','verification',?,10)`, rule); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: sqlite-proof\ndescription: Read the SQLite verification marker\n---\nVerification marker: " + marker + "\n"
	if _, err := db.Exec(`
INSERT INTO agent_skills(project_key,name,description,content,priority)
VALUES('sqlite-e2e','sqlite-proof','Read the SQLite verification marker',?,10)`, content); err != nil {
		t.Fatal(err)
	}
}

func sqliteResourceRegistry(db *sql.DB, cache string) *harness.ResourceRegistry {
	resources := harness.NewResourceRegistry()
	resources.Register(sqliteresources.Loader{
		Store:      sqliteresources.SQLiteStore{DB: db},
		ProjectKey: "sqlite-e2e",
		CacheDir:   cache,
	})
	return resources
}

type sqliteScenarioProvider struct {
	skillPath string
	calls     atomic.Int32
	mu        sync.Mutex
	requests  []harness.Request
}

func (p *sqliteScenarioProvider) Stream(_ context.Context, request harness.Request) (harness.Stream, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	switch p.calls.Add(1) {
	case 1:
		arguments := json.RawMessage(fmt.Sprintf(`{"path":%q}`, p.skillPath))
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventToolCallStart, Index: 0, ToolCallID: "read-sqlite-skill", ToolName: "read", ArgumentsDelta: string(arguments)},
			{Type: harness.EventDone, StopReason: harness.StopToolUse},
		}}, nil
	case 2:
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventTextDelta, Text: "SQLITE_LOCAL_E2E_OK"},
			{Type: harness.EventDone, StopReason: harness.StopStop},
		}}, nil
	default:
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventTextDelta, Text: "SQLITE_RELOAD_OK"},
			{Type: harness.EventDone, StopReason: harness.StopStop},
		}}, nil
	}
}

func (p *sqliteScenarioProvider) snapshot() []harness.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]harness.Request(nil), p.requests...)
}

func TestSQLiteResourceHarnessE2E(t *testing.T) {
	db, cache, project := prepareSQLiteResources(t)
	insertSQLiteResource(t, db, "SQLite rule version: SQLITE_RULE_V1", "SQLITE_SKILL_V1")

	loader := sqliteresources.Loader{Store: sqliteresources.SQLiteStore{DB: db}, ProjectKey: "sqlite-e2e", CacheDir: cache}
	snapshot, err := loader.Load(context.Background(), harness.ResourceLoadRequest{Cwd: project})
	if err != nil {
		t.Fatal(err)
	}
	skillPath := snapshot.Skills["sqlite-proof"].Location

	selected := harness.Model{Provider: "sqlite-faux", ID: "resource-e2e", ContextWindow: 32_000, MaxOutput: 512}
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	tools := harness.NewToolRegistry()
	if err := tools.Register(harness.ReadTool(harness.BuiltinConfig{Cwd: project})); err != nil {
		t.Fatal(err)
	}
	p := &sqliteScenarioProvider{skillPath: skillPath}
	h, err := harness.NewStateless(harness.Options{Models: models, Tools: tools, Resources: sqliteResourceRegistry(db, cache)})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RegisterProvider("sqlite-faux", p); err != nil {
		t.Fatal(err)
	}
	session, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, Cwd: project, ActiveTools: []string{"read"}}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := startSessionRun(t, ctx, session, harness.Prompt{Steps: harness.Sequence{harness.UserText("Read the SQLite skill.")}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	messages := session.Conversation().Messages
	if !stringsContainAssistant(messages, "SQLITE_LOCAL_E2E_OK") {
		t.Fatalf("messages=%#v", messages)
	}
	var toolText string
	for _, current := range messages {
		if current.Role == harness.RoleTool && current.ToolName == "read" {
			toolText = current.Text()
		}
	}
	if !strings.Contains(toolText, "SQLITE_SKILL_V1") {
		t.Fatalf("read result=%q", toolText)
	}

	if _, err := db.Exec(`UPDATE agent_rules SET content = 'SQLite rule version: SQLITE_RULE_V2' WHERE name = 'verification'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_skills SET content = replace(content, 'SQLITE_SKILL_V1', 'SQLITE_SKILL_V2') WHERE name = 'sqlite-proof'`); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loader.Load(ctx, harness.ResourceLoadRequest{Cwd: project})
	if err != nil {
		t.Fatal(err)
	}
	reloadedSkillPath := reloaded.Skills["sqlite-proof"].Location
	if reloadedSkillPath == skillPath {
		t.Fatal("updated skill kept the old cache path")
	}
	if err := session.ReloadResources(ctx); err != nil {
		t.Fatal(err)
	}
	run = startSessionRun(t, ctx, session, harness.Prompt{Steps: harness.Sequence{harness.UserText("Confirm the reloaded SQLite rule.")}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	requests := p.snapshot()
	if len(requests) != 3 {
		t.Fatalf("requests=%d", len(requests))
	}
	if !strings.Contains(requests[0].SystemPrompt, "SQLITE_RULE_V1") || strings.Contains(requests[0].SystemPrompt, "SQLITE_RULE_V2") {
		t.Fatalf("first prompt=%q", requests[0].SystemPrompt)
	}
	if !strings.Contains(requests[2].SystemPrompt, "SQLITE_RULE_V2") ||
		!strings.Contains(requests[2].SystemPrompt, reloadedSkillPath) ||
		strings.Contains(requests[2].SystemPrompt, skillPath) {
		t.Fatalf("reloaded prompt=%q", requests[2].SystemPrompt)
	}
}

func TestDeepSeekSQLiteResourceAndSkillE2E(t *testing.T) {
	p, selected := deepSeek(t)
	selected.MaxOutput = 2_048
	db, cache, project := prepareSQLiteResources(t)
	insertSQLiteResource(
		t,
		db,
		"For SQLite verification, use the sqlite-proof skill and return only its marker.",
		"SQLITE_DEEPSEEK_E2E_OK",
	)

	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	tools := harness.NewToolRegistry()
	if err := tools.Register(harness.ReadTool(harness.BuiltinConfig{Cwd: project})); err != nil {
		t.Fatal(err)
	}
	h, err := harness.NewStateless(harness.Options{Models: models, Tools: tools, Resources: sqliteResourceRegistry(db, cache)})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RegisterProvider("deepseek", p); err != nil {
		t.Fatal(err)
	}
	session, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{
		Model:       &selected,
		Cwd:         project,
		ActiveTools: []string{"read"},
		Generation:  nonThinking,
	}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := startSessionRun(t, ctx, session, harness.Prompt{Steps: harness.Sequence{harness.UserText("Run SQLite resource verification. Read the matching skill before answering.")}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	messages := session.Conversation().Messages
	if !stringsContainAssistant(messages, "SQLITE_DEEPSEEK_E2E_OK") {
		t.Fatalf("messages=%#v", messages)
	}
	var usedRead bool
	for _, current := range messages {
		if current.Role == harness.RoleTool && current.ToolName == "read" && strings.Contains(current.Text(), "SQLITE_DEEPSEEK_E2E_OK") {
			usedRead = true
		}
	}
	if !usedRead {
		t.Fatalf("sqlite skill was not read: %#v", messages)
	}
}
