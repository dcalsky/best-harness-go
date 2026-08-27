package extension_test

import (
	"errors"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/invocation"
)

func TestStaleContext(t *testing.T) {
	gate := invocation.NewGate()
	called := false
	c := invocation.NewContext(invocation.Config[struct{}]{Gate: gate, Actions: invocation.Actions{Abort: func() error { called = true; return nil }}})
	if err := c.Abort(); err != nil || !called {
		t.Fatalf("abort err=%v called=%v", err, called)
	}
	gate.Invalidate()
	if err := c.Abort(); !errors.Is(err, invocation.ErrStaleContext) {
		t.Fatalf("error=%v", err)
	}
}
