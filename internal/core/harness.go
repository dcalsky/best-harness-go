// Package harness assembles providers, tools, resources, sessions, and extensions.
package core

import (
	"context"
	"errors"
	"fmt"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"reflect"
	"sync"
	"time"

	"github.com/tiendc/go-deepcopy"

	"github.com/dcalsky/best-harness-go/internal/agent"
	"github.com/dcalsky/best-harness-go/internal/builtin"
	"github.com/dcalsky/best-harness-go/internal/compact"
	"github.com/dcalsky/best-harness-go/internal/extension"
	"github.com/dcalsky/best-harness-go/internal/invocation"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/prompt"
	"github.com/dcalsky/best-harness-go/internal/protocol"
	"github.com/dcalsky/best-harness-go/internal/resource"
	sharedrun "github.com/dcalsky/best-harness-go/internal/run"
	store "github.com/dcalsky/best-harness-go/internal/session"
	"github.com/dcalsky/best-harness-go/internal/settings"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

var ErrNoModel = errors.New("no model selected")
var ErrNoProvider = errors.New("provider is not registered")
var ErrNoShell = errors.New("shell executor is not configured")

var RetryAttempts = Setting[int]{Key: "retry.attempts", Default: 2, Validate: func(v int) error {
	if v < 0 {
		return errors.New("retry attempts must not be negative")
	}
	return nil
}}
var RetryDelay = Setting[time.Duration]{Key: "retry.delay", Default: 500 * time.Millisecond, Validate: func(v time.Duration) error {
	if v < 0 {
		return errors.New("retry delay must not be negative")
	}
	return nil
}}
var QueueMode = Setting[AgentQueueMode]{Key: "queue.mode", Default: agent.QueueAll, Validate: func(v AgentQueueMode) error {
	if v != agent.QueueAll && v != agent.QueueOneAtATime {
		return errors.New("invalid queue mode")
	}
	return nil
}}
var ExecutionMode = Setting[ToolExecutionMode]{Key: "tool.execution_mode", Default: tool.Parallel, Validate: func(v ToolExecutionMode) error {
	if v != tool.Sequential && v != tool.Parallel {
		return errors.New("invalid tool execution mode")
	}
	return nil
}}
var ReserveTokens = Setting[int64]{Key: "compaction.reserve_tokens", Default: 16_384, Validate: func(v int64) error {
	if v < 0 {
		return errors.New("reserve tokens must not be negative")
	}
	return nil
}}
var KeepRecentTokens = Setting[int64]{Key: "compaction.keep_recent_tokens", Default: 20_000, Validate: func(v int64) error {
	if v < 0 {
		return errors.New("recent tokens must not be negative")
	}
	return nil
}}

type Options struct {
	Settings  *Settings
	Tools     *ToolRegistry
	Models    *ModelRegistry
	Resources *ResourceRegistry
	Shell     ShellExecutor
}
type Harness[S any] struct {
	extensions *ExtensionRegistry[S]
	settings   *Settings
	shell      ShellExecutor
}

// ToolOption configures typed behavior for a tool registered through Harness.
// Its fields are intentionally private; use the With functions below.
type ToolOption[P any] struct {
	validators []configuredValidator[P]
	err        error
}

type configuredValidator[P any] struct {
	validate      ArgumentsValidator[P]
	retryLimit    int
	hasRetryLimit bool
}

// ValidatorOption configures one validator attached through
// WithArgumentsValidator or WithStructValidator.
type ValidatorOption struct {
	retryLimit     int
	setsRetryLimit bool
	err            error
}

// WithValidatorRetryLimit limits how many correction retries the model gets
// after a validator first rejects a tool call. Zero permits no retries;
// negative values allow unlimited retries.
func WithValidatorRetryLimit(retries int) ValidatorOption {
	return ValidatorOption{retryLimit: retries, setsRetryLimit: true}
}

func configureValidator[P any](validate ArgumentsValidator[P], options []ValidatorOption) (configuredValidator[P], error) {
	configured := configuredValidator[P]{validate: validate}
	retryLimitSet := false
	for _, option := range options {
		if option.err != nil {
			return configuredValidator[P]{}, option.err
		}
		if !option.setsRetryLimit {
			continue
		}
		if retryLimitSet {
			return configuredValidator[P]{}, errors.New("validator retry limit is already configured")
		}
		retryLimitSet = true
		configured.retryLimit = option.retryLimit
		configured.hasRetryLimit = option.retryLimit >= 0
	}
	return configured, nil
}

// WithArgumentsValidator adds validation for decoded tool arguments. The
// validator may run more than once for a call and must be deterministic,
// side-effect-free, and safe for concurrent use.
func WithArgumentsValidator[P any](validate ArgumentsValidator[P], options ...ValidatorOption) ToolOption[P] {
	if validate == nil {
		return ToolOption[P]{err: errors.New("tool arguments validator is required")}
	}
	configured, err := configureValidator(validate, options)
	if err != nil {
		return ToolOption[P]{err: err}
	}
	return ToolOption[P]{validators: []configuredValidator[P]{configured}}
}

// WithStructValidator validates decoded tool arguments through a struct
// validator, such as go-playground/validator. The validator must be fully
// configured before it is used by registered tools.
func WithStructValidator[P any](validate StructValidator, options ...ValidatorOption) ToolOption[P] {
	if isNilStructValidator(validate) {
		return ToolOption[P]{err: errors.New("tool struct validator is required")}
	}
	configured, err := configureValidator(ArgumentsValidator[P](func(params P) error {
		return validate.Struct(params)
	}), options)
	if err != nil {
		return ToolOption[P]{err: err}
	}
	return ToolOption[P]{validators: []configuredValidator[P]{configured}}
}

func isNilStructValidator(validate StructValidator) bool {
	if validate == nil {
		return true
	}
	value := reflect.ValueOf(validate)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func New[S any](opts Options) (*Harness[S], error) {
	if opts.Settings == nil {
		opts.Settings = settings.New()
	}
	r := extension.NewRegistry[S](opts.Tools, opts.Models, opts.Resources)
	return &Harness[S]{extensions: r, settings: opts.Settings, shell: opts.Shell}, nil
}

type NoState struct{}

func NewStateless(opts Options) (*Harness[NoState], error) { return New[NoState](opts) }
func (h *Harness[S]) Models() *ModelRegistry               { return h.extensions.Models }
func (h *Harness[S]) Resources() *ResourceRegistry         { return h.extensions.Resources }
func (h *Harness[S]) Settings() *Settings                  { return h.settings }
func (h *Harness[S]) RegisterProvider(name string, p Provider) error {
	return h.extensions.RegisterProvider(name, p)
}
func (h *Harness[S]) RegisterBuiltinTools(config BuiltinConfig) error {
	return builtin.RegisterAll(h.extensions.Tools, config)
}
func (h *Harness[S]) RegisterExtension(ext Extension[S]) error {
	if ext == nil {
		return errors.New("extension is required")
	}
	return ext.Register(h.extensions)
}

func (h *Harness[S]) RegisterTool[P, D any](spec ToolSpec, handler func(context.Context, Context[S], P) (ToolResult[D], error), options ...ToolOption[P]) error {
	if handler == nil {
		return errors.New("tool handler is required")
	}
	var validators []configuredValidator[P]
	for _, option := range options {
		if option.err != nil {
			return option.err
		}
		validators = append(validators, option.validators...)
	}
	var validate ArgumentsValidator[P]
	if len(validators) > 0 {
		validate = func(params P) error {
			for index, validator := range validators {
				if err := validator.validate(params); err != nil {
					return &tool.ValidatorFailure{
						ValidatorIndex: index,
						RetryLimit:     validator.retryLimit,
						HasRetryLimit:  validator.hasRetryLimit,
						Err:            err,
					}
				}
			}
			return nil
		}
	}
	return h.extensions.Tools.Register(tool.Tool[P, D]{
		Name: spec.Name, Description: spec.Description, Parameters: spec.Parameters, RawParameters: spec.RawParameters,
		ExecutionMode:     spec.ExecutionMode,
		ValidateArguments: validate,
		Execute: func(ctx context.Context, _ ToolCall, params P, _ Update[D]) (ToolResult[D], error) {
			typed, err := invocation.FromContext[S](ctx)
			if err != nil {
				return ToolResult[D]{}, err
			}
			return handler(ctx, typed, params)
		},
	})
}

func marshalState[S any](state S) (json.RawMessage, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode state: %w", err)
	}
	var clone S
	if err := json.Unmarshal(raw, &clone); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	return raw, nil
}

func unmarshalState[S any](raw json.RawMessage) (S, error) {
	var state S
	if len(raw) == 0 {
		return state, errors.New("session has no initial state")
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, err
	}
	return state, nil
}

func cloneState[S any](state S) (S, error) {
	var clone S
	if err := deepcopy.Copy(&clone, &state); err != nil {
		var zero S
		return zero, fmt.Errorf("clone state: %w", err)
	}
	return clone, nil
}

type SessionOptions struct {
	ID, Cwd, ParentSession string
	Model                  *Model
	SystemPrompt           string
	ActiveTools            []string
	QueueMode              AgentQueueMode
	ExecutionMode          ToolExecutionMode
	Summarizer             Summarizer
	Compaction             CompactionOptions
	Generation             GenerationConfig
}
type Session[S any] struct {
	mu                       sync.Mutex
	harness                  *Harness[S]
	store                    *SessionManager
	opts                     SessionOptions
	agent                    *Agent
	model                    Model
	hasModel                 bool
	activeTools              []string
	snapshot                 ResourceSnapshot
	subscribers              map[uint64]func(context.Context, Context[S], any)
	nextSub                  uint64
	ctxGate                  *InvocationGate
	stateMu                  sync.RWMutex
	stateCommitMu            sync.Mutex
	state                    S
	closed                   bool
	closing                  bool
	compactionWindowExplicit bool
	active                   *Run[S]
}

type StartOptions struct{ ID ID }

type Run[S any] struct {
	mu             sync.Mutex
	session        *Session[S]
	id             ID
	ctx            context.Context
	cancel         context.CancelCauseFunc
	done           chan struct{}
	status         Status
	cause          Cause
	err            error
	started        time.Time
	ended          time.Time
	current        *AgentRun
	attempt        int
	attempts       map[ID]int
	steering       []Message
	followup       []Message
	eventErr       error
	retried        int
	overflow       bool
	pendingFailure *Message
	finalState     S
	stateFinal     bool
}

type stateTransaction[S any] struct {
	mu      sync.Mutex
	updates []func(*S)
}

func (t *stateTransaction[S]) add(update func(*S)) error {
	t.mu.Lock()
	t.updates = append(t.updates, update)
	t.mu.Unlock()
	return nil
}

func (t *stateTransaction[S]) take() []func(*S) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]func(*S){}, t.updates...)
}

type toolBatch[S any] struct {
	session  *Session[S]
	snapshot S
	runID    ID
	txs      []*stateTransaction[S]
}

func (s *Session[S]) BeginToolBatch(_ context.Context, calls []ToolCall, _ bool) (ToolBatch, error) {
	snapshot, err := s.stateSnapshot()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	runID := ID("")
	if s.active != nil {
		runID = s.active.id
	}
	s.mu.Unlock()
	return &toolBatch[S]{session: s, snapshot: snapshot, runID: runID, txs: make([]*stateTransaction[S], len(calls))}, nil
}

func (b *toolBatch[S]) Context(index int, base context.Context, call ToolCall, report func(any) error) context.Context {
	tx := &stateTransaction[S]{}
	if index >= 0 && index < len(b.txs) {
		b.txs[index] = tx
	}
	state := b.snapshot
	typed := invocation.NewContext(invocation.Config[S]{
		State: func() S {
			clone, err := cloneState(state)
			if err != nil {
				var zero S
				return zero
			}
			return clone
		},
		Update:    tx.add,
		SessionID: b.session.store.Header().ID,
		RunID:     b.runID,
		Call:      &call,
		Report:    report,
		Gate:      b.session.ctxGate,
	})
	return invocation.WithTypedContext(base, typed)
}

func (b *toolBatch[S]) Commit(ctx context.Context, outcomes []ToolOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for i := range b.txs {
		if i >= len(outcomes) {
			break
		}
		if err := b.CommitOne(ctx, i, outcomes[i]); err != nil {
			return err
		}
	}
	return nil
}

func (b *toolBatch[S]) CommitOne(ctx context.Context, index int, outcome ToolOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if outcome.Err != nil || index < 0 || index >= len(b.txs) || b.txs[index] == nil {
		return nil
	}
	updates := b.txs[index].take()
	if len(updates) == 0 {
		return nil
	}
	return b.session.commitState(updates)
}

func (s *Session[S]) stateSnapshot() (S, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return cloneState(s.state)
}

func (s *Session[S]) State() S {
	state, err := s.stateSnapshot()
	if err != nil {
		var zero S
		return zero
	}
	return state
}

func (s *Session[S]) commitState(updates []func(*S)) error {
	if len(updates) == 0 {
		return nil
	}
	s.stateCommitMu.Lock()
	defer s.stateCommitMu.Unlock()
	current, err := s.stateSnapshot()
	if err != nil {
		return err
	}
	for _, update := range updates {
		update(&current)
	}
	raw, err := marshalState(current)
	if err != nil {
		return err
	}
	if _, err := s.store.AppendState(raw); err != nil {
		return err
	}
	s.stateMu.Lock()
	s.state = current
	s.stateMu.Unlock()
	return nil
}

func (h *Harness[S]) NewSession(ctx context.Context, persistence Persistence, opts SessionOptions, initialState S) (*Session[S], error) {
	raw, err := marshalState(initialState)
	if err != nil {
		return nil, err
	}
	m, err := store.New(persistence, store.Options{ID: opts.ID, Cwd: opts.Cwd, ParentSession: opts.ParentSession, InitialState: raw})
	if err != nil {
		return nil, err
	}
	return h.sessionFromStore(ctx, m, opts)
}
func (h *Harness[S]) NewSessionWithManager(ctx context.Context, manager *SessionManager, opts SessionOptions) (*Session[S], error) {
	if manager == nil {
		return nil, errors.New("session manager is required")
	}
	return h.sessionFromStore(ctx, manager, opts)
}
func (h *Harness[S]) OpenSession(ctx context.Context, path string) (*Session[S], error) {
	m, err := openFileManager(path)
	if err != nil {
		return nil, err
	}
	return h.sessionFromStore(ctx, m, SessionOptions{})
}
func (h *Harness[S]) ResumeLatest(ctx context.Context, directory, cwd string) (*Session[S], error) {
	m, err := resumeLatestFileManager(ctx, directory, cwd)
	if err != nil {
		return nil, err
	}
	return h.sessionFromStore(ctx, m, SessionOptions{})
}
func (h *Harness[S]) ListSessions(ctx context.Context, directory string) ([]SessionInfo, error) {
	return listFileSessions(ctx, directory)
}
func (h *Harness[S]) sessionFromStore(ctx context.Context, m *SessionManager, opts SessionOptions) (*Session[S], error) {
	compactionWindowExplicit := opts.Compaction.ContextWindow != 0
	if opts.QueueMode == "" {
		opts.QueueMode = h.settings.Get(QueueMode)
	}
	if opts.ExecutionMode == "" {
		opts.ExecutionMode = h.settings.Get(ExecutionMode)
	}
	if opts.Compaction.ReserveTokens == 0 {
		opts.Compaction.ReserveTokens = h.settings.Get(ReserveTokens)
	}
	if opts.Compaction.KeepRecentTokens == 0 {
		opts.Compaction.KeepRecentTokens = h.settings.Get(KeepRecentTokens)
	}
	snap, err := h.extensions.Resources.Load(ctx, resource.LoadRequest{Cwd: m.Header().Cwd})
	if err != nil {
		m.Close()
		return nil, err
	}
	var activeTools []string
	if opts.ActiveTools != nil {
		activeTools = make([]string, 0, len(opts.ActiveTools))
		for _, name := range opts.ActiveTools {
			if h.extensions.Tools.Has(name) {
				activeTools = append(activeTools, name)
			}
		}
	}
	state, err := unmarshalState[S](m.State())
	if err != nil {
		m.Close()
		return nil, fmt.Errorf("decode session state: %w", err)
	}
	s := &Session[S]{harness: h, store: m, opts: opts, activeTools: activeTools, snapshot: snap, subscribers: make(map[uint64]func(context.Context, Context[S], any)), ctxGate: invocation.NewGate(), state: state, compactionWindowExplicit: compactionWindowExplicit}
	if opts.Model != nil {
		s.model = *opts.Model
		s.hasModel = true
	} else {
		c := m.Context()
		if c.Provider != "" {
			if found, e := h.extensions.Models.Get(c.Provider, c.ModelID); e == nil {
				s.model = found
				s.hasModel = true
			}
		}
	}
	if s.hasModel && s.opts.Compaction.ContextWindow == 0 {
		s.opts.Compaction.ContextWindow = s.model.ContextWindow
	}
	for _, info := range m.RunHistory() {
		if info.Status == StatusRunning || info.Status == StatusCancelling {
			if _, err := m.AppendRunEnd(info.ID, StatusFailed, CauseInterrupted, sharedrun.ErrInterrupted); err != nil {
				m.Close()
				return nil, err
			}
		}
	}
	for _, hook := range h.extensions.SessionStart {
		typed, _ := s.callbackContext(ctx, "", false)
		if err := hook(ctx, typed); err != nil {
			m.Close()
			return nil, err
		}
	}
	return s, nil
}

type Prompt struct{ Steps Sequence }
type Unsubscribe func()
type Event interface{}
type AgentEvent = protocol.AgentEvent
type EntryAppendedEvent struct {
	RunID ID
	Entry SessionEntry
}
type QueueEvent struct{}
type ModelChangedEvent struct{ Model Model }
type ThinkingLevelChangedEvent struct{ Level string }
type CompactionEvent struct {
	RunID  ID
	Reason CompactionReason
	Result *CompactionResult
	Err    error
}
type RunEvent = protocol.RunEvent

func (s *Session[S]) On[E Event](handler func(context.Context, Context[S], E)) Unsubscribe {
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subscribers[id] = func(ctx context.Context, typed Context[S], v any) {
		if e, ok := v.(E); ok {
			handler(ctx, typed, e)
		}
	}
	s.mu.Unlock()
	return func() { s.mu.Lock(); delete(s.subscribers, id); s.mu.Unlock() }
}
func (s *Session[S]) emit(ctx context.Context, event any) {
	s.mu.Lock()
	handlers := make([]func(context.Context, Context[S], any), 0, len(s.subscribers))
	for _, h := range s.subscribers {
		handlers = append(handlers, h)
	}
	s.mu.Unlock()
	typed, _ := s.callbackContext(ctx, "", false)
	for _, h := range handlers {
		h(ctx, typed, event)
	}
}

func (s *Session[S]) callbackContext(ctx context.Context, runID ID, writable bool) (Context[S], *stateTransaction[S]) {
	return s.callbackContextForTool(ctx, runID, writable, nil)
}

func (s *Session[S]) callbackContextForTool(ctx context.Context, runID ID, writable bool, call *ToolCall) (Context[S], *stateTransaction[S]) {
	snapshot, err := s.stateSnapshot()
	if err != nil {
		var zero S
		snapshot = zero
	}
	tx := &stateTransaction[S]{}
	var update func(func(*S)) error
	if writable {
		update = tx.add
	}
	typed := invocation.NewContext(invocation.Config[S]{State: func() S {
		clone, err := cloneState(snapshot)
		if err != nil {
			var zero S
			return zero
		}
		return clone
	}, Update: update, SessionID: s.store.Header().ID, RunID: runID, Call: call, Gate: s.ctxGate})
	return typed, tx
}

func (s *Session[S]) beforeTool(ctx context.Context, call ToolCall) (ToolCall, error) {
	var err error
	for _, hook := range s.harness.extensions.BeforeTool {
		typed, tx := s.callbackContextForTool(ctx, s.activeRunID(), true, &call)
		call, err = hook(ctx, typed, call)
		if err != nil {
			return call, err
		}
		if err = s.finishCallback(tx, nil); err != nil {
			return call, err
		}
	}
	return call, nil
}

func (s *Session[S]) afterTool(ctx context.Context, call ToolCall, result Result) (Result, error) {
	if typed, err := invocation.FromContext[S](ctx); err == nil {
		for _, hook := range s.harness.extensions.AfterTool {
			result, err = hook(ctx, typed, call, result)
			if err != nil {
				return result, err
			}
		}
		return result, nil
	}
	var err error
	for _, hook := range s.harness.extensions.AfterTool {
		typed, tx := s.callbackContextForTool(ctx, s.activeRunID(), true, &call)
		result, err = hook(ctx, typed, call, result)
		if err != nil {
			return result, err
		}
		if err = s.finishCallback(tx, nil); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *Session[S]) finishCallback(tx *stateTransaction[S], callbackErr error) error {
	if callbackErr != nil {
		return callbackErr
	}
	updates := tx.take()
	if len(updates) == 0 {
		return nil
	}
	return s.commitState(updates)
}
func (s *Session[S]) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil
}
func (s *Session[S]) ensureAgent(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing {
		return store.ErrClosed
	}
	if !s.hasModel {
		return ErrNoModel
	}
	if s.agent != nil {
		return nil
	}
	// State is user-defined: prove it can be deep-copied before the agent
	// starts so a non-copyable state fails here instead of mid-run.
	if _, err := s.stateSnapshot(); err != nil {
		return err
	}
	p, ok := s.harness.extensions.Provider(s.model.Provider)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoProvider, s.model.Provider)
	}
	definitions := s.harness.extensions.Tools.Definitions(s.activeTools)
	effectiveTools := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		effectiveTools = append(effectiveTools, definition.Name)
	}
	promptSnapshot := s.snapshot
	if s.opts.SystemPrompt != "" {
		promptSnapshot.SystemPrompt = s.opts.SystemPrompt
	}
	system := resource.BuildSystemPrompt(resource.PromptOptions{Cwd: s.store.Header().Cwd, Tools: effectiveTools, Snapshot: promptSnapshot})
	wrapped := hookedProvider[S]{session: s, base: p, hooks: s.harness.extensions.Request, contextHooks: s.harness.extensions.Context}
	reasoning := s.store.Context().ThinkingLevel
	if reasoning == "off" {
		reasoning = ""
	}
	a := agent.New(agent.Options{Provider: wrapped, Model: s.model, Tools: s.harness.extensions.Tools, SystemPrompt: system, ExecutionMode: s.opts.ExecutionMode, QueueMode: s.opts.QueueMode, ActiveTools: s.activeTools, Generation: s.opts.Generation.Clone(), ReasoningEffort: reasoning, ToolBatches: s, BeforeTool: s.beforeTool, AfterTool: s.afterTool})
	a.ReplaceMessages(s.store.Context().Messages)
	a.On(func(e AgentLifecycleEvent) { s.handleAgentEvent(context.Background(), e) })
	s.agent = a
	return nil
}

type hookedProvider[S any] struct {
	session      *Session[S]
	base         Provider
	hooks        []RequestHook[S]
	contextHooks []ContextHook[S]
}

func (p hookedProvider[S]) Stream(ctx context.Context, r Request) (Stream, error) {
	for _, h := range p.contextHooks {
		typed, tx := p.session.callbackContext(ctx, p.session.activeRunID(), true)
		messages, err := h(ctx, typed, r.Messages)
		if err != nil {
			return nil, err
		}
		if err := p.session.finishCallback(tx, nil); err != nil {
			return nil, err
		}
		r.Messages = messages
	}
	for _, h := range p.hooks {
		typed, tx := p.session.callbackContext(ctx, p.session.activeRunID(), true)
		if err := h(ctx, typed, &r); err != nil {
			return nil, err
		}
		if err := p.session.finishCallback(tx, nil); err != nil {
			return nil, err
		}
	}
	return p.base.Stream(ctx, r)
}

func (s *Session[S]) activeRunID() ID {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return ""
	}
	return s.active.id
}
func (s *Session[S]) handleAgentEvent(ctx context.Context, e AgentLifecycleEvent) {
	s.mu.Lock()
	r := s.active
	s.mu.Unlock()
	logicalID := ID("")
	attempt := 0
	if r != nil {
		r.mu.Lock()
		logicalID = r.id
		attempt = r.attempts[e.RunID]
		r.mu.Unlock()
	}
	s.emit(ctx, AgentEvent{RunID: logicalID, Attempt: attempt, Event: e})
	if e.Type == AgentEventMessageEnd && e.Message != nil {
		if e.Message.Role == message.RoleAssistant && e.Message.Origin == message.OriginModel {
			for _, hook := range s.harness.extensions.Response {
				typed, tx := s.callbackContext(ctx, logicalID, true)
				if err := hook(ctx, typed, *e.Message); err != nil {
					if r != nil {
						r.fail(err)
					}
					return
				}
				if err := s.finishCallback(tx, nil); err != nil {
					if r != nil {
						r.fail(err)
					}
					return
				}
			}
			if e.Message.StopReason == message.StopError || e.Message.StopReason == message.StopAborted {
				if r != nil {
					failure := *e.Message
					r.mu.Lock()
					r.pendingFailure = &failure
					r.mu.Unlock()
				}
				return
			}
		}
		entryID, err := s.store.AppendMessage(*e.Message)
		if err != nil {
			if r != nil {
				r.fail(err)
			}
		} else {
			s.emitEntry(ctx, logicalID, entryID)
		}
	}
}
func (s *Session[S]) emitEntry(ctx context.Context, runID ID, entryID SessionEntryID) {
	entries := s.store.Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].ID == entryID {
			s.emit(ctx, EntryAppendedEvent{RunID: runID, Entry: entries[i]})
			return
		}
	}
}

func (s *Session[S]) Start(ctx context.Context, p Prompt, opts StartOptions) (*Run[S], error) {
	if len(p.Steps) == 0 {
		return nil, errors.New("prompt has no steps")
	}
	steps, err := p.Steps.Normalize()
	if err != nil {
		return nil, err
	}
	id, err := sharedrun.ResolveID(opts.ID)
	if err != nil {
		return nil, err
	}
	if _, err = s.store.RunInfo(id); err == nil {
		return nil, sharedrun.ErrDuplicateID
	} else if !errors.Is(err, sharedrun.ErrNotFound) {
		return nil, err
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	r := &Run[S]{session: s, id: id, ctx: runCtx, cancel: cancel, done: make(chan struct{}), status: StatusRunning, started: time.Now(), attempts: make(map[ID]int)}
	s.mu.Lock()
	if s.closed || s.closing {
		s.mu.Unlock()
		cancel(store.ErrClosed)
		return nil, store.ErrClosed
	}
	if s.active != nil {
		s.mu.Unlock()
		cancel(agent.ErrBusy)
		return nil, agent.ErrBusy
	}
	s.active = r
	s.mu.Unlock()
	cleanup := func(startErr error) (*Run[S], error) {
		cancel(startErr)
		s.mu.Lock()
		if s.active == r {
			s.active = nil
		}
		s.mu.Unlock()
		return nil, startErr
	}
	for i, raw := range steps {
		step, ok := raw.(prompt.UserMessageStep)
		if !ok {
			continue
		}
		m := Message{Role: message.RoleUser, Origin: message.OriginUser, Content: step.Content, Timestamp: time.Now().UnixMilli()}
		for _, hook := range s.harness.extensions.Input {
			typed, tx := s.callbackContext(ctx, id, true)
			m, err = hook(ctx, typed, m)
			if err != nil {
				return cleanup(err)
			}
			if err = s.finishCallback(tx, nil); err != nil {
				return cleanup(err)
			}
		}
		if m.Role != message.RoleUser {
			return cleanup(fmt.Errorf("input hook changed prompt step %d role to %q", i, m.Role))
		}
		step.Content = m.Content
		steps[i] = step
	}
	steps, err = steps.Normalize()
	if err != nil {
		return cleanup(err)
	}
	if err := s.ensureAgent(ctx); err != nil {
		return cleanup(err)
	}
	for _, hook := range s.harness.extensions.BeforeAgent {
		typed, tx := s.callbackContext(ctx, id, true)
		if err := hook(ctx, typed); err != nil {
			return cleanup(err)
		}
		if err := s.finishCallback(tx, nil); err != nil {
			return cleanup(err)
		}
	}
	entryID, err := s.store.AppendRunStart(id)
	if err != nil {
		return cleanup(err)
	}
	s.emitEntry(context.Background(), id, entryID)
	s.agent.ReplaceMessages(s.store.Context().Messages)
	s.emit(context.Background(), RunEvent{RunID: id, Status: StatusRunning})
	attempt, err := s.startAttempt(r, agent.Prompt{Steps: steps})
	if err != nil {
		s.finalizeRun(r, err)
		return nil, err
	}
	go s.coordinate(r, attempt)
	return r, nil
}

func (s *Session[S]) startAttempt(r *Run[S], p agent.Prompt) (*AgentRun, error) {
	attemptID := sharedrun.NewID()
	r.mu.Lock()
	r.attempt++
	n := r.attempt
	r.attempts[attemptID] = n
	r.mu.Unlock()
	s.agent.ReplaceMessages(s.store.Context().Messages)
	ar, err := s.agent.Start(r.ctx, p, agent.StartOptions{ID: attemptID})
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.current = ar
	steering := append([]Message(nil), r.steering...)
	followup := append([]Message(nil), r.followup...)
	r.steering = nil
	r.followup = nil
	r.mu.Unlock()
	var failedSteering, failedFollowup []Message
	for _, m := range steering {
		if err := ar.Steer(m); err != nil {
			failedSteering = append(failedSteering, m)
		}
	}
	for _, m := range followup {
		if err := ar.FollowUp(m); err != nil {
			failedFollowup = append(failedFollowup, m)
		}
	}
	if len(failedSteering)+len(failedFollowup) > 0 {
		r.mu.Lock()
		if r.status == StatusRunning {
			r.steering = append(failedSteering, r.steering...)
			r.followup = append(failedFollowup, r.followup...)
		}
		r.mu.Unlock()
	}
	return ar, nil
}

func (s *Session[S]) coordinate(r *Run[S], attempt *AgentRun) {
	err := attempt.Wait(context.Background())
	for {
		r.mu.Lock()
		if r.current == attempt {
			r.current = nil
		}
		if r.eventErr != nil {
			err = r.eventErr
			r.eventErr = nil
		}
		r.mu.Unlock()
		if cause := context.Cause(r.ctx); cause != nil {
			err = cause
			break
		}
		var providerErr *message.ProviderError
		if errors.As(err, &providerErr) && providerErr.Retryable && r.retried < s.harness.settings.Get(RetryAttempts) {
			r.retried++
			r.mu.Lock()
			r.pendingFailure = nil
			r.mu.Unlock()
			timer := time.NewTimer(s.harness.settings.Get(RetryDelay))
			select {
			case <-timer.C:
			case <-r.ctx.Done():
				timer.Stop()
				err = context.Cause(r.ctx)
				break
			}
			if context.Cause(r.ctx) != nil {
				break
			}
			attempt, err = s.startAttempt(r, agent.Prompt{})
			if err != nil {
				break
			}
			err = attempt.Wait(context.Background())
			continue
		}
		if errors.Is(err, message.ErrContextOverflow) && !r.overflow {
			r.overflow = true
			r.mu.Lock()
			r.pendingFailure = nil
			r.mu.Unlock()
			if _, compactErr := s.compactForRun(r.ctx, OverflowOptions(), r.id); compactErr != nil {
				err = errors.Join(err, compactErr)
				break
			}
			attempt, err = s.startAttempt(r, agent.Prompt{})
			if err != nil {
				break
			}
			err = attempt.Wait(context.Background())
			continue
		}
		break
	}
	if err == nil && compact.ShouldCompact(s.store.Context().Messages, s.opts.Compaction) && s.opts.Summarizer != nil {
		_, err = s.compactForRun(r.ctx, CompactOptions{Reason: compact.Threshold}, r.id)
	}
	s.finalizeRun(r, err)
}

func (s *Session[S]) ActiveRun() *Run[S]          { s.mu.Lock(); defer s.mu.Unlock(); return s.active }
func (s *Session[S]) RunInfo(id ID) (Info, error) { return s.store.RunInfo(id) }
func (s *Session[S]) RunHistory() []Info          { return s.store.RunHistory() }

func (r *Run[S]) ID() ID         { return r.id }
func (r *Run[S]) Status() Status { r.mu.Lock(); defer r.mu.Unlock(); return r.status }
func (r *Run[S]) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !sharedrun.Terminal(r.status) {
		return nil
	}
	return r.err
}
func (r *Run[S]) Done() <-chan struct{} { return r.done }
func (r *Run[S]) Wait(ctx context.Context) error {
	select {
	case <-r.done:
		return r.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *Run[S]) State() S {
	r.mu.Lock()
	if r.stateFinal {
		state := r.finalState
		r.mu.Unlock()
		clone, err := cloneState(state)
		if err != nil {
			var zero S
			return zero
		}
		return clone
	}
	r.mu.Unlock()
	return r.session.State()
}
func (r *Run[S]) Abort() bool {
	r.mu.Lock()
	if r.status != StatusRunning {
		r.mu.Unlock()
		return false
	}
	r.status = StatusCancelling
	r.steering = nil
	r.followup = nil
	current := r.current
	cancel := r.cancel
	r.mu.Unlock()
	r.session.emit(context.Background(), RunEvent{RunID: r.id, Status: StatusCancelling, Cause: CauseUserAbort})
	r.session.emit(context.Background(), QueueEvent{})
	cancel(sharedrun.ErrAborted)
	if current != nil {
		current.Abort()
	}
	return true
}
func (r *Run[S]) Steer(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.status != StatusRunning {
		r.mu.Unlock()
		return sharedrun.ErrFinished
	}
	current := r.current
	if current == nil {
		r.steering = append(r.steering, m)
		r.mu.Unlock()
	} else {
		r.mu.Unlock()
		if err := current.Steer(m); err != nil {
			r.mu.Lock()
			if r.status == StatusRunning {
				r.steering = append(r.steering, m)
				err = nil
			}
			r.mu.Unlock()
			if err != nil {
				return err
			}
		}
	}
	r.session.emit(ctx, QueueEvent{})
	return nil
}
func (r *Run[S]) FollowUp(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.status != StatusRunning {
		r.mu.Unlock()
		return sharedrun.ErrFinished
	}
	current := r.current
	if current == nil {
		r.followup = append(r.followup, m)
		r.mu.Unlock()
	} else {
		r.mu.Unlock()
		if err := current.FollowUp(m); err != nil {
			r.mu.Lock()
			if r.status == StatusRunning {
				r.followup = append(r.followup, m)
				err = nil
			}
			r.mu.Unlock()
			if err != nil {
				return err
			}
		}
	}
	r.session.emit(ctx, QueueEvent{})
	return nil
}
func (r *Run[S]) fail(err error) {
	r.mu.Lock()
	if sharedrun.Terminal(r.status) {
		r.mu.Unlock()
		return
	}
	if r.eventErr == nil {
		r.eventErr = err
	}
	current := r.current
	cancel := r.cancel
	r.mu.Unlock()
	cancel(err)
	if current != nil {
		current.Abort()
	}
}

func (s *Session[S]) finalizeRun(r *Run[S], err error) {
	r.mu.Lock()
	pendingFailure := r.pendingFailure
	r.pendingFailure = nil
	r.mu.Unlock()
	if pendingFailure != nil {
		entryID, persistFailureErr := s.store.AppendMessage(*pendingFailure)
		if persistFailureErr != nil {
			err = errors.Join(err, persistFailureErr)
		} else {
			s.emitEntry(context.Background(), r.id, entryID)
		}
	}
	status, cause, finalErr := classifyRun(r.ctx, err)
	entryID, persistErr := s.store.AppendRunEnd(r.id, status, cause, finalErr)
	if persistErr != nil {
		status = StatusFailed
		cause = CauseInternal
		finalErr = errors.Join(finalErr, persistErr)
	} else {
		s.emitEntry(context.Background(), r.id, entryID)
	}
	finalState, stateErr := s.stateSnapshot()
	if stateErr != nil {
		status = StatusFailed
		cause = CauseInternal
		finalErr = errors.Join(finalErr, stateErr)
	}
	r.mu.Lock()
	r.status = status
	r.cause = cause
	r.err = finalErr
	r.ended = time.Now()
	r.current = nil
	r.steering = nil
	r.followup = nil
	r.finalState = finalState
	r.stateFinal = true
	r.mu.Unlock()
	s.emit(context.Background(), RunEvent{RunID: r.id, Status: status, Cause: cause, Err: finalErr})
	s.mu.Lock()
	if s.active == r {
		s.active = nil
	}
	close(r.done)
	s.mu.Unlock()
}

func classifyRun(ctx context.Context, err error) (Status, Cause, error) {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, sharedrun.ErrAborted):
		return StatusAborted, CauseUserAbort, sharedrun.ErrAborted
	case errors.Is(cause, context.DeadlineExceeded):
		return StatusAborted, CauseDeadline, context.DeadlineExceeded
	case errors.Is(cause, context.Canceled):
		return StatusAborted, CauseParentCanceled, context.Canceled
	case cause != nil:
		return StatusFailed, CauseInternal, cause
	case err == nil:
		return StatusCompleted, CauseNone, nil
	case errors.Is(err, agent.ErrProviderAborted):
		return StatusAborted, CauseProviderAbort, err
	default:
		return StatusFailed, CauseInternal, err
	}
}

type CompactOptions struct {
	Reason       CompactionReason
	Instructions string
}

func OverflowOptions() CompactOptions { return CompactOptions{Reason: compact.Overflow} }
func (s *Session[S]) Compact(ctx context.Context, o CompactOptions) (CompactionResult, error) {
	if s.busy() {
		return CompactionResult{}, agent.ErrBusy
	}
	return s.compactForRun(ctx, o, "")
}
func (s *Session[S]) compactForRun(ctx context.Context, o CompactOptions, runID ID) (CompactionResult, error) {
	if o.Reason == "" {
		o.Reason = compact.Manual
	}
	opts := s.opts.Compaction
	if o.Instructions != "" {
		opts.Instructions = o.Instructions
	}
	for _, hook := range s.harness.extensions.Compact {
		typed, _ := s.callbackContext(ctx, runID, false)
		if err := hook(ctx, typed); err != nil {
			return CompactionResult{}, err
		}
	}
	result, err := compact.Run(ctx, s.store, o.Reason, opts, s.opts.Summarizer)
	if err == nil {
		s.mu.Lock()
		if s.agent != nil {
			s.agent.ReplaceMessages(s.store.Context().Messages)
		}
		s.mu.Unlock()
	}
	s.emit(ctx, CompactionEvent{RunID: runID, Reason: o.Reason, Result: &result, Err: err})
	return result, err
}

type NavigateOptions struct {
	Summary string
	Details json.RawMessage
}

func (s *Session[S]) Navigate(ctx context.Context, id *SessionEntryID, options ...NavigateOptions) error {
	if s.busy() {
		return agent.ErrBusy
	}
	if err := s.store.Navigate(id); err != nil {
		return err
	}
	if len(options) > 0 && options[0].Summary != "" {
		if _, err := s.store.AppendBranchSummary(id, options[0].Summary, options[0].Details, nil, false); err != nil {
			return err
		}
	}
	restored, err := unmarshalState[S](s.store.State())
	if err != nil {
		return fmt.Errorf("decode branch state: %w", err)
	}
	s.stateMu.Lock()
	s.state = restored
	s.stateMu.Unlock()
	s.ctxGate.Invalidate()
	s.mu.Lock()
	if s.agent != nil {
		s.agent.ReplaceMessages(s.store.Context().Messages)
	}
	s.mu.Unlock()
	for _, hook := range s.harness.extensions.Tree {
		target := "root"
		if id != nil {
			target = string(*id)
		}
		typed, _ := s.callbackContext(ctx, "", false)
		if err := hook(ctx, typed, target); err != nil {
			return err
		}
	}
	return nil
}
func (s *Session[S]) Fork(ctx context.Context, id SessionEntryID, opts SessionOptions) (*Session[S], error) {
	if s.busy() {
		return nil, agent.ErrBusy
	}
	fork, err := s.store.Fork(ctx, id, store.Options{ID: opts.ID, Cwd: opts.Cwd})
	if err != nil {
		return nil, err
	}
	s.ctxGate.Invalidate()
	return s.harness.sessionFromStore(ctx, fork, opts)
}
func (s *Session[S]) SetModel(ctx context.Context, m Model) error {
	if s.busy() {
		return agent.ErrBusy
	}
	if _, err := s.harness.extensions.Models.Get(m.Provider, m.ID); err != nil {
		return err
	}
	if _, err := s.store.AppendModel(m.Provider, m.ID); err != nil {
		return err
	}
	s.mu.Lock()
	s.model = m
	s.hasModel = true
	if !s.compactionWindowExplicit {
		s.opts.Compaction.ContextWindow = m.ContextWindow
	}
	s.agent = nil
	s.mu.Unlock()
	s.emit(ctx, ModelChangedEvent{Model: m})
	return nil
}
func (s *Session[S]) SetThinkingLevel(ctx context.Context, level string) error {
	if s.busy() {
		return agent.ErrBusy
	}
	switch level {
	case "off", "minimal", "low", "medium", "high", "xhigh", "max":
	default:
		return fmt.Errorf("invalid thinking level %q", level)
	}
	s.mu.Lock()
	if s.hasModel && !s.model.SupportsReasoning {
		level = "off"
	}
	s.mu.Unlock()
	if _, err := s.store.AppendThinkingLevel(level); err != nil {
		return err
	}
	s.mu.Lock()
	s.agent = nil
	s.mu.Unlock()
	s.emit(ctx, ThinkingLevelChangedEvent{Level: level})
	return nil
}
func (s *Session[S]) SetActiveTools(names []string) error {
	if s.busy() {
		return agent.ErrBusy
	}
	valid := make([]string, 0, len(names))
	for _, name := range names {
		if s.harness.extensions.Tools.Has(name) {
			valid = append(valid, name)
		}
	}
	s.mu.Lock()
	s.activeTools = valid
	s.agent = nil
	s.mu.Unlock()
	return nil
}
func (s *Session[S]) AppendCustom[T any](ctx context.Context, customType string, value T) (SessionEntryID, error) {
	return s.store.AppendCustom(ctx, customType, value)
}
func (s *Session[S]) CustomEntries[T any](customType string) ([]SessionCustomEntry[T], error) {
	return s.store.CustomEntries[T](customType)
}

type Stats struct {
	Entries, Messages                      int
	InputTokens, OutputTokens, TotalTokens int64
	Cost                                   float64
	Started                                time.Time
}

func (s *Session[S]) Stats() Stats {
	entries := s.store.Entries()
	st := Stats{Entries: len(entries)}
	st.Started, _ = time.Parse(time.RFC3339Nano, s.store.Header().Timestamp)
	for _, e := range entries {
		var usage *message.Usage
		if e.Message != nil {
			st.Messages++
			usage = &e.Message.Usage
		} else if (e.Type == "compaction" || e.Type == "branch_summary") && e.Usage != nil {
			usage = e.Usage
		}
		if usage != nil {
			st.InputTokens += usage.InputTokens
			st.OutputTokens += usage.OutputTokens
			st.TotalTokens += usage.TotalTokens
			if usage.Cost != nil {
				st.Cost += usage.Cost.Total
			}
		}
	}
	return st
}
func (s *Session[S]) RunBash(ctx context.Context, command string) (ShellResult, error) {
	if s.harness.shell == nil {
		return ShellResult{}, ErrNoShell
	}
	var err error
	for _, hook := range s.harness.extensions.UserBash {
		typed, _ := s.callbackContext(ctx, s.activeRunID(), false)
		command, err = hook(ctx, typed, command)
		if err != nil {
			return ShellResult{}, err
		}
	}
	return s.harness.shell.Execute(ctx, command, s.store.Header().Cwd, nil)
}
func (s *Session[S]) ReloadResources(ctx context.Context) error {
	if s.busy() {
		return agent.ErrBusy
	}
	snap, err := s.harness.extensions.Resources.Load(ctx, resource.LoadRequest{Cwd: s.store.Header().Cwd})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.snapshot = snap
	s.agent = nil
	s.mu.Unlock()
	return nil
}
func (s *Session[S]) forkInPlace(ctx context.Context, entryID string) error {
	if s.busy() {
		return agent.ErrBusy
	}
	s.mu.Lock()
	old := s.store
	s.mu.Unlock()
	fork, err := old.Fork(ctx, SessionEntryID(entryID), store.Options{})
	if err != nil {
		return err
	}
	if err = old.Close(); err != nil {
		fork.Close()
		return err
	}
	s.mu.Lock()
	s.store = fork
	s.agent = nil
	s.mu.Unlock()
	restored, decodeErr := unmarshalState[S](fork.State())
	if decodeErr != nil {
		return decodeErr
	}
	s.stateMu.Lock()
	s.state = restored
	s.stateMu.Unlock()
	s.ctxGate.Invalidate()
	return nil
}
func (s *Session[S]) Context() Context[S] {
	return invocation.NewContext(invocation.Config[S]{State: s.State, Update: func(update func(*S)) error {
		if s.busy() {
			return invocation.ErrStateBusy
		}
		return s.commitState([]func(*S){update})
	}, SessionID: s.store.Header().ID, Gate: s.ctxGate, Actions: invocation.Actions{SendUser: func(ctx context.Context, m Message) error {
		r := s.ActiveRun()
		if r == nil {
			return errors.New("session is not running")
		}
		return r.Steer(ctx, m)
	}, SendCustom: func(ctx context.Context, customType string, value any) error {
		_, err := s.store.AppendCustom(ctx, customType, value)
		return err
	}, Abort: func() error {
		if r := s.ActiveRun(); r != nil {
			r.Abort()
			return nil
		}
		return errors.New("session is not running")
	}, Compact: func(ctx context.Context) error { _, err := s.Compact(ctx, CompactOptions{}); return err }, Fork: s.forkInPlace, Switch: func(ctx context.Context, entryID string) error {
		id := SessionEntryID(entryID)
		return s.Navigate(ctx, &id)
	}, ReloadResources: s.ReloadResources}})
}
func (s *Session[S]) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	r := s.active
	s.mu.Unlock()
	if r != nil {
		r.Abort()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := r.Wait(ctx)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	s.mu.Lock()
	s.closed = true
	s.closing = false
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, hook := range s.harness.extensions.Shutdown {
		typed, _ := s.callbackContext(ctx, "", false)
		_ = hook(ctx, typed)
	}
	s.ctxGate.Invalidate()
	return s.store.Close()
}
func (s *Session[S]) Entries() []SessionEntry                     { return s.store.Entries() }
func (s *Session[S]) Tree() []*SessionTreeNode                    { return s.store.Tree() }
func (s *Session[S]) Conversation() SessionContext                { return s.store.Context() }
func (s *Session[S]) Location() string                            { return s.store.Location() }
func (s *Session[S]) SetName(name string) (SessionEntryID, error) { return s.store.SetName(name) }
func (s *Session[S]) SetLabel(id SessionEntryID, label string) (SessionEntryID, error) {
	return s.store.SetLabel(id, label)
}

// RawJSON copies a JSON value for callers that need to attach extension details.
func RawJSON(v json.RawMessage) json.RawMessage { return v.Clone() }
