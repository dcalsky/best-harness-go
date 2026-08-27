package main

import (
	"context"

	"github.com/dcalsky/best-harness-go"
)

func enqueue(r *harness.AgentRun) {
	_ = r.Steer(harness.User("Use this correction after the current turn."))
	_ = r.FollowUp(harness.User("Run the final check before stopping."))
}
func main() { _ = context.Background(); _ = enqueue }
