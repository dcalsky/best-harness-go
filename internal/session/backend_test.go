package session_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/message"
	store "github.com/dcalsky/best-harness-go/internal/session"
)

type backendState struct {
	mu      sync.Mutex
	created map[string][]store.Entry
	closed  int
	err     error
}

type memoryBackend struct {
	state *backendState
	id    string
}

func (b *memoryBackend) Location(header store.Header) string {
	return "memory-store://" + header.ID
}
func (b *memoryBackend) Fork() store.Persistence { return &memoryBackend{state: b.state} }
func (b *memoryBackend) Create(header store.Header, entries []store.Entry) error {
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	if b.state.err != nil {
		return b.state.err
	}
	b.id = header.ID
	b.state.created[header.ID] = append([]store.Entry(nil), entries...)
	return nil
}
func (b *memoryBackend) Append(entry store.Entry) error {
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	if b.state.err != nil {
		return b.state.err
	}
	b.state.created[b.id] = append(b.state.created[b.id], entry)
	return nil
}
func (b *memoryBackend) Close() error {
	b.state.mu.Lock()
	defer b.state.mu.Unlock()
	b.state.closed++
	return nil
}

func TestBackendUsesDelayedPersistenceAndAppend(t *testing.T) {
	state := &backendState{created: make(map[string][]store.Entry)}
	manager, err := store.New(&memoryBackend{state: state}, store.Options{ID: "backend-session", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if manager.Location() != "memory-store://backend-session" {
		t.Fatalf("location=%q", manager.Location())
	}
	if _, err := manager.AppendMessage(message.User("question")); err != nil {
		t.Fatal(err)
	}
	if len(state.created) != 0 {
		t.Fatal("backend was created before an assistant message")
	}
	if _, err := manager.AppendMessage(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.Text("answer")}}); err != nil {
		t.Fatal(err)
	}
	if len(state.created["backend-session"]) != 2 {
		t.Fatalf("entries=%#v", state.created["backend-session"])
	}
	if _, err := manager.SetName("stored session"); err != nil {
		t.Fatal(err)
	}
	if len(state.created["backend-session"]) != 3 {
		t.Fatalf("entries=%#v", state.created["backend-session"])
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if state.closed != 1 {
		t.Fatalf("closed=%d", state.closed)
	}
}

func TestBackendForkUsesNewBackend(t *testing.T) {
	state := &backendState{created: make(map[string][]store.Entry)}
	manager, err := store.New(&memoryBackend{state: state}, store.Options{ID: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manager.AppendMessage(message.User("question"))
	leaf, err := manager.AppendMessage(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.Text("answer")}})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := manager.Fork(t.Context(), leaf, store.Options{ID: "child"})
	if err != nil {
		t.Fatal(err)
	}
	defer fork.Close()
	defer manager.Close()
	if fork.Location() != "memory-store://child" || fork.Header().ParentSession != manager.Location() {
		t.Fatalf("location=%q parent=%q", fork.Location(), fork.Header().ParentSession)
	}
	if len(state.created["child"]) != 2 {
		t.Fatalf("child entries=%#v", state.created["child"])
	}
}

func TestBackendErrorsAndRestoreValidation(t *testing.T) {
	want := errors.New("backend failed")
	state := &backendState{created: make(map[string][]store.Entry), err: want}
	manager, err := store.New(&memoryBackend{state: state}, store.Options{ID: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = manager.AppendMessage(message.User("question"))
	_, err = manager.AppendMessage(message.Message{Role: message.RoleAssistant, Content: []message.Content{message.Text("answer")}})
	if !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	_ = manager.Close()

	backend := &memoryBackend{state: &backendState{created: make(map[string][]store.Entry)}}
	_, err = store.Restore(store.Header{Type: "session", Version: store.Version, ID: "duplicate"}, []store.Entry{{ID: "same"}, {ID: "same"}}, backend)
	if err == nil {
		t.Fatal("duplicate entries did not fail")
	}
	if _, err := store.New(nil, store.Options{}); err == nil {
		t.Fatal("nil persistence did not fail")
	}
}
