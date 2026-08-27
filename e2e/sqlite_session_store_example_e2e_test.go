package e2e_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dcalsky/best-harness-go"
	. "github.com/dcalsky/best-harness-go/examples/sqlite_session"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.db")
	database, err := OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return database, path
}

func assistant(text string) harness.Message {
	return harness.Message{
		Role:     harness.RoleAssistant,
		Content:  []harness.Content{harness.Text(text)},
		Provider: "test",
		Model:    "stored-model",
	}
}

func TestInitializeAndValidation(t *testing.T) {
	database, _ := testStore(t)
	if err := database.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDatabase(""); err == nil {
		t.Fatal("empty path did not fail")
	}
	if err := (*Store)(nil).Initialize(context.Background()); err == nil {
		t.Fatal("nil store did not fail")
	}
	if _, err := (&Store{DB: database.DB}).NewManager(harness.PersistenceOptions{}); err == nil {
		t.Fatal("missing location did not fail")
	}
}

func TestDelayedPersistenceListAndRestore(t *testing.T) {
	database, path := testStore(t)
	manager, err := database.NewManager(harness.PersistenceOptions{ID: "sqlite-session", Cwd: "/work/project"})
	if err != nil {
		t.Fatal(err)
	}
	userID, err := manager.AppendMessage(harness.User("first question"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := database.List(context.Background(), "")
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%#v error=%v", items, err)
	}
	assistantID, err := manager.AppendMessage(assistant("first answer"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetName("sqlite task"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetLabel(userID, "question"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendCustom(context.Background(), "state", struct {
		Count int `json:"count"`
	}{Count: 3}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	items, err = database.List(context.Background(), "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "sqlite-session" || items[0].Name != "sqlite task" || items[0].MessageCount != 2 || items[0].FirstMessage != "first question" {
		t.Fatalf("items=%#v", items)
	}
	if !strings.HasPrefix(items[0].Location, "sqlite-session://") {
		t.Fatalf("location=%q", items[0].Location)
	}
	opened, err := database.Open(context.Background(), "sqlite-session")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if opened.Location() != items[0].Location || len(opened.Context().Messages) != 2 || opened.LeafID() == nil {
		t.Fatalf("location=%q context=%#v leaf=%v", opened.Location(), opened.Context(), opened.LeafID())
	}
	states, err := opened.CustomEntries[struct {
		Count int `json:"count"`
	}]("state")
	if err != nil || len(states) != 1 || states[0].Data.Count != 3 {
		t.Fatalf("states=%#v error=%v", states, err)
	}
	if len(opened.Tree()) == 0 || assistantID == "" {
		t.Fatal("restored tree is empty")
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.jsonl"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("jsonl files=%#v error=%v", matches, err)
	}
}

func TestWriterLockResumeAndFork(t *testing.T) {
	database, _ := testStore(t)
	parent, err := database.NewManager(harness.PersistenceOptions{ID: "parent", Cwd: "/work/a"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = parent.AppendMessage(harness.User("question"))
	leaf, err := parent.AppendMessage(assistant("answer"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Open(context.Background(), "parent"); !errors.Is(err, harness.ErrSessionWriterActive) {
		t.Fatalf("writer error=%v", err)
	}
	fork, err := parent.Fork(context.Background(), leaf, harness.PersistenceOptions{ID: "child"})
	if err != nil {
		t.Fatal(err)
	}
	if fork.Header().ParentSession != parent.Location() {
		t.Fatalf("header=%#v location=%q", fork.Header(), fork.Location())
	}
	if err := fork.Close(); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := database.ResumeLatest(context.Background(), "/work/a")
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.Header().ID != "child" && resumed.Header().ID != "parent" {
		t.Fatalf("resumed=%q", resumed.Header().ID)
	}
	child, err := database.Open(context.Background(), "child")
	if resumed.Header().ID == "child" {
		if !errors.Is(err, harness.ErrSessionWriterActive) {
			t.Fatalf("child writer error=%v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	} else {
		_ = child.Close()
	}
}

func TestConcurrentAppendsAndCorruptRows(t *testing.T) {
	database, _ := testStore(t)
	manager, err := database.NewManager(harness.PersistenceOptions{ID: "concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manager.AppendMessage(harness.User("question"))
	_, _ = manager.AppendMessage(assistant("answer"))
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.AppendCustom(context.Background(), "counter", i)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := database.Open(context.Background(), "concurrent")
	if err != nil {
		t.Fatal(err)
	}
	values, err := opened.CustomEntries[int]("counter")
	if err != nil || len(values) != 20 {
		t.Fatalf("values=%d error=%v", len(values), err)
	}
	_ = opened.Close()
	if _, err := database.DB.Exec(`UPDATE sdk_sessions SET header_json = x'00' WHERE id = 'concurrent'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Open(context.Background(), "concurrent"); err == nil {
		t.Fatal("corrupt header did not fail")
	}
}

func TestClosedDatabaseReturnsAppendError(t *testing.T) {
	database, _ := testStore(t)
	manager, err := database.NewManager(harness.PersistenceOptions{ID: "closed-db"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manager.AppendMessage(harness.User("question"))
	_, _ = manager.AppendMessage(assistant("answer"))
	if err := database.DB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetName("fails"); !errors.Is(err, sql.ErrConnDone) && err == nil {
		t.Fatal("append on closed database did not fail")
	}
	_ = manager.Close()
}

func TestOpenHonorsCancellationAndMissingSession(t *testing.T) {
	database, _ := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := database.Open(ctx, "missing"); err == nil {
		t.Fatal("cancelled open did not fail")
	}
	if _, err := database.Open(context.Background(), "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(database.Location); err != nil {
		t.Fatal(err)
	}
}
