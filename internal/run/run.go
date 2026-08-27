// Package run defines identifiers and lifecycle metadata shared by agent runs
// and session-level logical runs.
package run

import (
	"errors"
	"regexp"
	"time"
	"uuid"
)

type ID string
type Status string
type Cause string

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
	Error     string
	StartedAt time.Time
	EndedAt   time.Time
}

var (
	ErrAborted     = errors.New("run aborted")
	ErrInterrupted = errors.New("run interrupted")
	ErrFinished    = errors.New("run is already finished")
	ErrNotFound    = errors.New("run not found")
	ErrDuplicateID = errors.New("run id already exists")
	ErrInvalidID   = errors.New("invalid run id")
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
