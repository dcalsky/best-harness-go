package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/run"
	"github.com/dcalsky/best-harness-go/internal/session"
)

func findNode(nodes []*session.TreeNode, id session.EntryID) *session.TreeNode {
	for _, node := range nodes {
		if node.Entry.ID == id {
			return node
		}
		if found := findNode(node.Children, id); found != nil {
			return found
		}
	}
	return nil
}

func TestDelayedPersistenceOpenAndWriterLock(t *testing.T) {
	directory := t.TempDir()
	persistence, err := harness.NewFilePersistence(directory)
	if err != nil {
		t.Fatal(err)
	}
	m, err := session.New(persistence, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	userID, err := m.AppendMessage(message.User("hello"))
	if err != nil {
		t.Fatal(err)
	}
	path := m.Location()
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file exists before assistant: %v", err)
	}
	assistant := message.Message{Role: message.RoleAssistant, Content: []message.Content{message.Text("hi")}, Provider: "p", Model: "m"}
	if _, err = m.AppendMessage(assistant); err != nil {
		t.Fatal(err)
	}
	if _, err = harness.OpenFileSession(path); !errors.Is(err, session.ErrWriterActive) {
		t.Fatalf("writer error=%v", err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := harness.OpenFileSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if got := opened.Context().Messages; len(got) != 2 || got[0].Text() != "hello" {
		t.Fatalf("messages=%#v", got)
	}
	branch, err := opened.Branch(userID)
	if err != nil || len(branch) != 1 {
		t.Fatalf("branch=%#v err=%v", branch, err)
	}
}

func TestRunEntriesPersistImmediatelyAndStayOutOfContext(t *testing.T) {
	persistence, err := harness.NewFilePersistence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := session.New(persistence, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AppendRunStart("run-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(m.Location()); err != nil {
		t.Fatalf("run start did not create session: %v", err)
	}
	_, _ = m.AppendMessage(message.User("hello"))
	if _, err = m.AppendRunEnd("run-1", run.StatusCompleted, run.CauseNone, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = m.AppendRunStart("run-1"); !errors.Is(err, run.ErrDuplicateID) {
		t.Fatalf("duplicate error=%v", err)
	}
	info, err := m.RunInfo("run-1")
	if err != nil || info.Status != run.StatusCompleted || info.StartedAt.IsZero() || info.EndedAt.IsZero() {
		t.Fatalf("info=%#v err=%v", info, err)
	}
	if got := m.Context().Messages; len(got) != 1 || got[0].Text() != "hello" {
		t.Fatalf("context=%#v", got)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := harness.OpenFileSession(m.Location())
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	info, err = opened.RunInfo("run-1")
	if err != nil || info.Status != run.StatusCompleted {
		t.Fatalf("reopened info=%#v err=%v", info, err)
	}
}
func TestIncompleteTailAndMalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	header := session.Header{Type: "session", Version: session.Version, ID: "id", Timestamp: "2026-01-01T00:00:00.000Z", Cwd: dir, InitialState: json.RawMessage(`{}`)}
	hb, _ := json.Marshal(header)
	good := `{"type":"custom","id":"abcd1234","parentId":null,"timestamp":"2026-01-01T00:00:00.000Z","customType":"x","data":{"n":1},"future":{"keep":true}}`
	content := string(hb) + "\nnot-json\n" + good + "\n" + `{"type":"message"`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := harness.OpenFileSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries()) != 1 {
		t.Fatalf("entries=%#v", m.Entries())
	}
	values, err := m.CustomEntries[struct {
		N int `json:"n"`
	}]("x")
	if err != nil || len(values) != 1 || values[0].Data.N != 1 {
		t.Fatalf("custom=%#v err=%v", values, err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `{"type":"message"`) {
		t.Fatal("unfinished tail was not removed")
	}
}

func TestOpenRejectsV3Session(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.jsonl")
	header := session.Header{Type: "session", Version: 3, ID: "old", Timestamp: "2026-01-01T00:00:00.000Z", Cwd: t.TempDir()}
	raw, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.OpenFileSession(path); err == nil || !strings.Contains(err.Error(), "unsupported session version 3") {
		t.Fatalf("error=%v", err)
	}
}
func TestCompactionContextAndTree(t *testing.T) {
	m, _ := session.New(harness.NewMemoryPersistence(), session.Options{})
	id1, _ := m.AppendMessage(message.User("old"))
	id2, _ := m.AppendMessage(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.Text("answer")}})
	id3, _ := m.AppendMessage(message.User("keep"))
	_, _ = m.AppendCompaction("summary", id3, 100, nil, nil, false)
	_, _ = m.AppendMessage(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.Text("new")}})
	ctx := m.Context()
	wantSummary := session.CompactionSummaryPrefix + "summary" + session.CompactionSummarySuffix
	if len(ctx.Messages) != 3 || ctx.Messages[0].Text() != wantSummary || ctx.Messages[1].Text() != "keep" {
		t.Fatalf("context=%#v", ctx.Messages)
	}
	if err := m.Navigate(&id2); err != nil {
		t.Fatal(err)
	}
	_, _ = m.AppendMessage(message.User("branch"))
	tree := m.Tree()
	if len(tree) != 1 || len(tree[0].Children) != 1 {
		t.Fatalf("tree=%#v id1=%s", tree, id1)
	}
}

func TestBranchSummaryUsesPiContextEnvelope(t *testing.T) {
	m, _ := session.New(harness.NewMemoryPersistence(), session.Options{})
	id, _ := m.AppendMessage(message.User("root"))
	_, _ = m.AppendBranchSummary(&id, "abandoned work", nil, nil, false)
	ctx := m.Context()
	want := session.BranchSummaryPrefix + "abandoned work" + session.BranchSummarySuffix
	if len(ctx.Messages) != 2 || ctx.Messages[1].Role != message.RoleUser || ctx.Messages[1].Text() != want {
		t.Fatalf("context=%#v want summary=%q", ctx.Messages, want)
	}
}

func TestOpenRejectsNonV3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session","version":2,"id":"old","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/tmp"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.OpenFileSession(path); err == nil || !strings.Contains(err.Error(), "unsupported session version") {
		t.Fatalf("error=%v", err)
	}
}

func TestForkPreservesResolvedLabelsAndPersistsImmediately(t *testing.T) {
	persistence, err := harness.NewFilePersistence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := session.New(persistence, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := source.AppendMessage(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.Text("answer")}, Timestamp: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.SetLabel(id, "bookmark"); err != nil {
		t.Fatal(err)
	}
	fork, err := source.Fork(context.Background(), id, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fork.Close()
	if _, err = os.Stat(fork.Location()); err != nil {
		t.Fatalf("fork was not persisted: %v", err)
	}
	node := findNode(fork.Tree(), id)
	if node == nil || node.Label != "bookmark" {
		t.Fatalf("tree=%#v", fork.Tree())
	}
}
