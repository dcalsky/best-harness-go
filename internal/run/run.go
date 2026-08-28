// Package run defines identifiers and lifecycle metadata shared by agent runs
// and session-level logical runs.
package run

import (
	"errors"
	"fmt"
	"regexp"
	"time"
	"uuid"
)

type ID string
type Status string
type Cause string

// EndReason explains why an otherwise successful run loop chose to finish.
type EndReason string

const (
	EndReasonNone          EndReason = ""
	EndReasonAssistantStop EndReason = "assistant_stop"
	EndReasonPromptDone    EndReason = "prompt_complete"
	EndReasonToolTerminate EndReason = "tool_terminate"
	EndReasonLoopStopped   EndReason = "loop_stopped"
)

// Stats contains counters scoped to one logical run.
type Stats struct {
	TurnsCompleted int `json:"turnsCompleted"`
	Attempts       int `json:"attempts"`
	ToolCalls      int `json:"toolCalls"`
}

// Outcome combines lifecycle state, the successful end reason, and counters.
type Outcome struct {
	Status    Status    `json:"status"`
	Cause     Cause     `json:"cause"`
	EndReason EndReason `json:"endReason"`
	Stats     Stats     `json:"stats"`
}

// PanicError reports a panic recovered at a provider, tool, or run-loop boundary.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("agent execution panicked: %v", e.Value) }

const (
	StatusRunning    Status = "running"
	StatusCancelling Status = "cancelling"
	StatusCompleted  Status = "completed"
	StatusAborted    Status = "aborted"
	StatusFailed     Status = "failed"
)

const (
	CauseNone           Cause = ""
	CauseUserAbort      Cause = "user_abort"
	CauseParentCanceled Cause = "parent_canceled"
	CauseDeadline       Cause = "deadline_exceeded"
	CauseProviderAbort  Cause = "provider_aborted"
	CauseInterrupted    Cause = "interrupted"
	CauseInternal       Cause = "internal"
)

type Info struct {
	ID        ID
	Status    Status
	Cause     Cause
	EndReason EndReason
	Stats     Stats
	Error     string
	StartedAt time.Time
	EndedAt   time.Time
}

var (
	ErrAborted         = errors.New("run aborted")
	ErrInterrupted     = errors.New("run interrupted")
	ErrFinished        = errors.New("run is already finished")
	ErrNotFound        = errors.New("run not found")
	ErrDuplicateID     = errors.New("run id already exists")
	ErrInvalidID       = errors.New("invalid run id")
	ErrNoPendingInput  = errors.New("run has no pending input")
	ErrNextUnavailable = errors.New("next is only available inside this run's loop")
)

var validID = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

func NewID() ID { return ID(uuid.NewV7().String()) }

func ResolveID(id ID) (ID, error) {
	if id == "" {
		return NewID(), nil
	}
	if !validID.MatchString(string(id)) {
		return "", ErrInvalidID
	}
	return id, nil
}

func Terminal(status Status) bool {
	return status == StatusCompleted || status == StatusAborted || status == StatusFailed
}
