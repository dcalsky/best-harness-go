package run_test

import (
	"errors"
	"testing"
	"uuid"

	"github.com/dcalsky/best-harness-go/internal/run"
)

func TestResolveID(t *testing.T) {
	id, err := run.ResolveID("")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := uuid.Parse(string(id))
	if err != nil || parsed[6]>>4 != 7 {
		t.Fatalf("id=%q parsed=%v err=%v", id, parsed, err)
	}
	custom, err := run.ResolveID("web-run_123")
	if err != nil || custom != "web-run_123" {
		t.Fatalf("custom=%q err=%v", custom, err)
	}
	if _, err := run.ResolveID("bad/id"); !errors.Is(err, run.ErrInvalidID) {
		t.Fatalf("invalid error=%v", err)
	}
}

func TestTerminal(t *testing.T) {
	if run.Terminal(run.StatusRunning) || run.Terminal(run.StatusCancelling) {
		t.Fatal("non-terminal status reported terminal")
	}
	for _, status := range []run.Status{run.StatusCompleted, run.StatusAborted, run.StatusFailed} {
		if !run.Terminal(status) {
			t.Fatalf("status %q is not terminal", status)
		}
	}
}
