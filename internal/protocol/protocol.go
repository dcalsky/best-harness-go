// Package protocol defines the provider-neutral event boundary consumed by
// optional wire-protocol adapters.
package protocol

import (
	"github.com/dcalsky/best-harness-go/internal/agent"
	sharedrun "github.com/dcalsky/best-harness-go/internal/run"
)

type AgentEvent struct {
	RunID   sharedrun.ID
	Attempt int
	Event   agent.Event
}

type RunEvent struct {
	RunID  sharedrun.ID
	Status sharedrun.Status
	Cause  sharedrun.Cause
	Err    error
}

type Adapter[Frame any] interface {
	Start(RunEvent) ([]Frame, error)
	Encode(AgentEvent) ([]Frame, error)
	Finish(RunEvent) ([]Frame, error)
}
