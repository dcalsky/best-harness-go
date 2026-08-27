package session_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/session"
)

type legacyConversation struct {
	ID       string
	Messages []string
}

func legacyConverter(_ context.Context, source legacyConversation) (session.Snapshot, error) {
	header := session.Header{
		Type:         "session",
		Version:      session.Version,
		ID:           source.ID,
		Timestamp:    "2026-08-25T10:00:00Z",
		Cwd:          "/legacy",
		InitialState: json.RawMessage(`{"imported":true}`),
	}
	entries := make([]session.Entry, 0, len(source.Messages))
	var parent *session.EntryID
	for i, text := range source.Messages {
		id := session.EntryID("legacy-" + strconv.Itoa(i+1))
		msg := message.User(text)
		entries = append(entries, session.Entry{
			Type:      "message",
			ID:        id,
			ParentID:  parent,
			Timestamp: "2026-08-25T10:00:00Z",
			Message:   &msg,
		})
		parent = &entries[len(entries)-1].ID
	}
	return session.Snapshot{Header: header, Entries: entries}, nil
}

func TestConvertWritesJSONLThatOpenConsumes(t *testing.T) {
	snapshot, err := session.Convert(t.Context(), legacyConversation{
		ID:       "legacy-session",
		Messages: []string{"first", "second"},
	}, session.ConverterFunc[legacyConversation](legacyConverter))
	if err != nil {
		t.Fatal(err)
	}

	var encoded bytes.Buffer
	written, err := snapshot.WriteTo(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(encoded.Len()) || !strings.HasSuffix(encoded.String(), "\n") {
		t.Fatalf("written=%d JSONL=%q", written, encoded.String())
	}

	path := filepath.Join(t.TempDir(), "converted.jsonl")
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := harness.OpenFileSession(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if got := manager.Context().Messages; len(got) != 2 || got[0].Text() != "first" || got[1].Text() != "second" {
		t.Fatalf("converted messages=%#v", got)
	}
	if got := string(manager.State()); got != `{"imported":true}` {
		t.Fatalf("converted state=%s", got)
	}
}

func TestConvertValidatesConverterAndSnapshot(t *testing.T) {
	var nilConverter session.ConverterFunc[legacyConversation]
	if _, err := session.Convert(t.Context(), legacyConversation{}, nilConverter); !errors.Is(err, session.ErrConverterRequired) {
		t.Fatalf("nil converter error=%v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.Convert(canceled, legacyConversation{}, session.ConverterFunc[legacyConversation](legacyConverter)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error=%v", err)
	}

	invalid := session.ConverterFunc[legacyConversation](func(context.Context, legacyConversation) (session.Snapshot, error) {
		missing := session.EntryID("missing")
		return session.Snapshot{
			Header:  session.Header{Type: "session", Version: session.Version, ID: "legacy", Timestamp: "2026-08-25T10:00:00Z", InitialState: json.RawMessage(`{}`)},
			Entries: []session.Entry{{Type: "message", ID: "entry", ParentID: &missing, Timestamp: "2026-08-25T10:00:01Z"}},
		}, nil
	})
	if _, err := session.Convert(t.Context(), legacyConversation{}, invalid); err == nil || !strings.Contains(err.Error(), "unknown or later parent") {
		t.Fatalf("invalid snapshot error=%v", err)
	}
}

func TestConvertedSnapshotStoresWithBackendAndContinuesAppending(t *testing.T) {
	snapshot, err := session.Convert(t.Context(), legacyConversation{ID: "restored", Messages: []string{"hello"}}, session.ConverterFunc[legacyConversation](legacyConverter))
	if err != nil {
		t.Fatal(err)
	}
	state := &backendState{created: make(map[string][]session.Entry)}
	manager, err := snapshot.Store(&memoryBackend{state: state})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.SetName("imported"); err != nil {
		t.Fatal(err)
	}
	if manager.Location() != "memory-store://restored" || len(manager.Entries()) != 2 {
		t.Fatalf("location=%q entries=%#v", manager.Location(), manager.Entries())
	}
	if got := state.created["restored"]; len(got) != 2 || got[1].Type != "session_info" {
		t.Fatalf("stored entries=%#v", got)
	}
}
