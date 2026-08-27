package e2e_test

import (
	"context"
	"testing"

	"github.com/dcalsky/best-harness-go"
)

func startSessionRun(t *testing.T, ctx context.Context, s *harness.Session[harness.NoState], p harness.Prompt) *harness.Run[harness.NoState] {
	t.Helper()
	r, err := s.Start(ctx, p, harness.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func startAgentRun(t *testing.T, ctx context.Context, a *harness.Agent, p harness.AgentPrompt) *harness.AgentRun {
	t.Helper()
	r, err := a.Start(ctx, p, harness.AgentStartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
