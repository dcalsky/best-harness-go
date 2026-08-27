package core_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go"
)

func TestBuiltInPersistenceSelectionIsExplicit(t *testing.T) {
	if _, err := harness.NewFilePersistence(""); err == nil {
		t.Fatal("empty persistence directory did not fail")
	}
	if _, err := harness.NewSessionManager(nil, harness.PersistenceOptions{}); err == nil {
		t.Fatal("nil persistence did not fail")
	}

	memory, err := harness.NewSessionManager(harness.NewMemoryPersistence(), harness.PersistenceOptions{ID: "memory"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if memory.Location() != "memory-session://memory" {
		t.Fatalf("memory location=%q", memory.Location())
	}
}

func TestFilePersistenceGeneratesOneFilePerSessionInDirectory(t *testing.T) {
	directory := t.TempDir()
	locations := make(map[string]bool)
	for _, id := range []string{"first", "second"} {
		persistence, err := harness.NewFilePersistence(directory)
		if err != nil {
			t.Fatal(err)
		}
		manager, err := harness.NewSessionManager(persistence, harness.PersistenceOptions{ID: id})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.AppendRunStart(harness.ID("run-" + id)); err != nil {
			t.Fatal(err)
		}
		location := manager.Location()
		if filepath.Dir(location) != directory || !strings.HasSuffix(location, "_"+id+".jsonl") {
			t.Fatalf("location=%q", location)
		}
		if locations[location] {
			t.Fatalf("duplicate location=%q", location)
		}
		locations[location] = true
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
	}

	items, err := harness.ListFileSessions(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("sessions=%#v", items)
	}
}
