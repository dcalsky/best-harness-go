package sqlitesession

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/dcalsky/best-harness-go"
)

func TestSQLiteBackendPersistsStateSnapshots(t *testing.T) {
	database, err := OpenDatabase(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}

	manager, err := database.NewManager(harness.PersistenceOptions{ID: "state-session", InitialState: json.RawMessage(`{"count":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendState(json.RawMessage(`{"count":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	opened, err := database.Open(context.Background(), "state-session")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if got := string(opened.State()); got != `{"count":2}` {
		t.Fatalf("state=%s", got)
	}
}
