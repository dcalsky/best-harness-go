// Package invocation defines the typed context shared by a session and its
// callbacks. It intentionally sits below harness, extension, and tool so those
// packages can use the same context without import cycles.
package invocation

import (
	"context"
	"errors"
	"sync"

	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"github.com/dcalsky/best-harness-go/internal/message"
	sharedrun "github.com/dcalsky/best-harness-go/internal/run"
)

var ErrStaleContext = errors.New("invocation context belongs to an inactive session")
var ErrActionUnavailable = errors.New("invocation action is not available")
var ErrStateReadOnly = errors.New("state is read-only in this context")
var ErrStateBusy = errors.New("state cannot be changed while the session is running")
var ErrContextUnavailable = errors.New("typed invocation context is not available")

// ToolCall contains provider-neutral metadata for one tool invocation.
type ToolCall struct {
	ID, Key, Name string
	Arguments     json.RawMessage
}

// Gate invalidates contexts created before a session changes branch or closes.
type Gate struct {
	mu         sync.RWMutex
	generation uint64
}

func NewGate() *Gate { return &Gate{generation: 1} }
func (g *Gate) Generation() uint64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.generation
}
func (g *Gate) Invalidate() {
	g.mu.Lock()
	g.generation++
	g.mu.Unlock()
}
func (g *Gate) Check(generation uint64) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if generation != g.generation {
		return ErrStaleContext
	}
	return nil
}

// Actions are session operations exposed through Context. Nil functions mean
// that an action is unavailable in the current phase.
type Actions struct {
	SendUser        func(context.Context, message.Message) error
	SendCustom      func(context.Context, string, any) error
	Abort           func() error
	Compact         func(context.Context) error
	Fork            func(context.Context, string) error
	Switch          func(context.Context, string) error
	ReloadResources func(context.Context) error
}

// Config is used by harness to create a Context. SDK users normally receive a
// Context from a Session, Tool handler, Hook, or event callback.
type Config[S any] struct {
	State     func() S
	Update    func(func(*S)) error
	SessionID string
	RunID     sharedrun.ID
	Call      *ToolCall
	Report    func(any) error
	Gate      *Gate
	Actions   Actions
}

// Context is the typed agent/session context. It carries session state and
// actions only; cancellation, deadlines, and values stay on the standard
// context.Context passed alongside it.
type Context[S any] struct {
	state      func() S
	update     func(func(*S)) error
	sessionID  string
	runID      sharedrun.ID
	call       *ToolCall
	report     func(any) error
	gate       *Gate
	generation uint64
	actions    Actions
}

func NewContext[S any](cfg Config[S]) Context[S] {
	generation := uint64(0)
	if cfg.Gate != nil {
		generation = cfg.Gate.Generation()
	}
	return Context[S]{state: cfg.State, update: cfg.Update, sessionID: cfg.SessionID, runID: cfg.RunID, call: cfg.Call, report: cfg.Report, gate: cfg.Gate, generation: generation, actions: cfg.Actions}
}

func (c Context[S]) Check() error {
	if c.gate == nil {
		return nil
	}
	return c.gate.Check(c.generation)
}

func (c Context[S]) State() S {
	if c.state == nil {
		var zero S
		return zero
	}
	return c.state()
}

func (c Context[S]) UpdateState(update func(*S)) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.update == nil {
		return ErrStateReadOnly
	}
	if update == nil {
		return errors.New("state update is required")
	}
	return c.update(update)
}

func (c Context[S]) SessionID() string   { return c.sessionID }
func (c Context[S]) RunID() sharedrun.ID { return c.runID }
func (c Context[S]) ToolCall() (ToolCall, bool) {
	if c.call == nil {
		return ToolCall{}, false
	}
	call := *c.call
	call.Arguments = call.Arguments.Clone()
	return call, true
}

func (c Context[S]) Report[T any](details T) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.report == nil {
		return ErrActionUnavailable
	}
	return c.report(details)
}

func (c Context[S]) SendUser(ctx context.Context, m message.Message) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.actions.SendUser == nil {
		return ErrActionUnavailable
	}
	return c.actions.SendUser(ctx, m)
}
func (c Context[S]) SendCustom(ctx context.Context, customType string, value any) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.actions.SendCustom == nil {
		return ErrActionUnavailable
	}
	return c.actions.SendCustom(ctx, customType, value)
}
func (c Context[S]) Abort() error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.actions.Abort == nil {
		return ErrActionUnavailable
	}
	return c.actions.Abort()
}
func (c Context[S]) Compact(ctx context.Context) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.actions.Compact == nil {
		return ErrActionUnavailable
	}
	return c.actions.Compact(ctx)
}
func (c Context[S]) Fork(ctx context.Context, entryID string) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.actions.Fork == nil {
		return ErrActionUnavailable
	}
	return c.actions.Fork(ctx, entryID)
}
func (c Context[S]) Switch(ctx context.Context, entryID string) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.actions.Switch == nil {
		return ErrActionUnavailable
	}
	return c.actions.Switch(ctx, entryID)
}
func (c Context[S]) ReloadResources(ctx context.Context) error {
	if err := c.Check(); err != nil {
		return err
	}
	if c.actions.ReloadResources == nil {
		return ErrActionUnavailable
	}
	return c.actions.ReloadResources(ctx)
}

type typedContextKey struct{}

func WithTypedContext[S any](ctx context.Context, typed Context[S]) context.Context {
	return context.WithValue(ctx, typedContextKey{}, typed)
}

func FromContext[S any](ctx context.Context) (Context[S], error) {
	typed, ok := ctx.Value(typedContextKey{}).(Context[S])
	if !ok {
		return Context[S]{}, ErrContextUnavailable
	}
	return typed, typed.Check()
}

// ToolOutcome tells a batch coordinator whether one staged update may commit.
type ToolOutcome struct {
	Err       error
	IsError   bool
	Terminate bool
}

// ToolBatch is deliberately non-generic. A Session[S] implements its state
// work behind this interface while agent remains independent of S.
type ToolBatch interface {
	Context(index int, base context.Context, call ToolCall, report func(any) error) context.Context
	CommitOne(context.Context, int, ToolOutcome) error
	Commit(context.Context, []ToolOutcome) error
}

type ToolBatchCoordinator interface {
	BeginToolBatch(context.Context, []ToolCall, bool) (ToolBatch, error)
}
