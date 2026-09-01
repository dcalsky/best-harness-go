// Package agent runs provider turns and dispatches tool calls.
package agent

import (
	"context"
	"errors"
	"fmt"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"io"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rs/xid"

	"github.com/dcalsky/best-harness-go/internal/invocation"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	"github.com/dcalsky/best-harness-go/internal/prompt"
	"github.com/dcalsky/best-harness-go/internal/provider"
	sharedrun "github.com/dcalsky/best-harness-go/internal/run"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

var (
	ErrBusy            = errors.New("agent is already running")
	ErrProviderAborted = errors.New("provider aborted run")
)

type ScriptToolError struct {
	Key, Name string
	Cause     error
}

func (e *ScriptToolError) Error() string {
	identity := e.Name
	if e.Key != "" {
		identity = e.Key + " (" + e.Name + ")"
	}
	if e.Cause == nil {
		return "scripted tool call " + identity + " failed"
	}
	return "scripted tool call " + identity + " failed: " + e.Cause.Error()
}
func (e *ScriptToolError) Unwrap() error { return e.Cause }

// ValidatorRetryLimitError reports that one configured tool validator has
// exhausted the correction retries available to the model.
type ValidatorRetryLimitError struct {
	Tool           string
	ValidatorIndex int
	RetryLimit     int
	Failures       int
	LastErr        error
}

func (e *ValidatorRetryLimitError) Error() string {
	return fmt.Sprintf(
		"tool %q validator %d exceeded its retry limit of %d after %d failed calls: %v",
		e.Tool,
		e.ValidatorIndex+1,
		e.RetryLimit,
		e.Failures,
		e.LastErr,
	)
}
func (e *ValidatorRetryLimitError) Unwrap() error { return e.LastErr }

type QueueMode string

const (
	QueueAll        QueueMode = "all"
	QueueOneAtATime QueueMode = "one-at-a-time"
)

type EventType string

const (
	EventAgentStart    EventType = "agent_start"
	EventAgentEnd      EventType = "agent_end"
	EventTurnStart     EventType = "turn_start"
	EventTurnEnd       EventType = "turn_end"
	EventMessageStart  EventType = "message_start"
	EventMessageUpdate EventType = "message_update"
	EventMessageEnd    EventType = "message_end"
	EventToolStart     EventType = "tool_execution_start"
	EventToolUpdate    EventType = "tool_execution_update"
	EventToolEnd       EventType = "tool_execution_end"
	EventError         EventType = "error"
	EventQueueUpdate   EventType = "queue_update"
)

type Event struct {
	Type     EventType
	RunID    sharedrun.ID
	Message  *message.Message
	Stream   *message.StreamEvent
	Call     *tool.ToolCall
	Result   *tool.Result
	Update   any
	Err      error
	Messages []message.Message
}

// snapshotEvent detaches event payloads from the mutable values used by the
// running agent. Subscribers are allowed to enqueue events and consume them
// asynchronously, so emitting pointers to stack values that the run mutates
// after the callback returns would otherwise create stale data and races.
func snapshotEvent(event Event) Event {
	if event.Message != nil {
		message := cloneMessage(*event.Message)
		event.Message = &message
	}
	if event.Stream != nil {
		stream := *event.Stream
		stream.ProviderMetadata = cloneRawMap(stream.ProviderMetadata)
		event.Stream = &stream
	}
	if event.Call != nil {
		call := *event.Call
		call.Arguments = call.Arguments.Clone()
		event.Call = &call
	}
	if event.Result != nil {
		result := *event.Result
		result.Content = cloneContent(event.Result.Content)
		event.Result = &result
	}
	if len(event.Messages) > 0 {
		event.Messages = make([]message.Message, len(event.Messages))
		for i := range event.Messages {
			event.Messages[i] = cloneMessage(event.Messages[i])
		}
	}
	return event
}

func cloneMessage(value message.Message) message.Message {
	value.Content = cloneContent(value.Content)
	value.ProviderMetadata = cloneRawMap(value.ProviderMetadata)
	return value
}

func cloneContent(values []message.Content) []message.Content {
	if len(values) == 0 {
		return nil
	}
	out := make([]message.Content, len(values))
	for i, value := range values {
		value.Arguments = value.Arguments.Clone()
		out[i] = value
	}
	return out
}

func cloneRawMap(values map[string]json.RawMessage) map[string]json.RawMessage {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		out[key] = value.Clone()
	}
	return out
}

type Prompt struct{ Steps prompt.Sequence }
type PrepareNextTurn func(context.Context, []message.Message) ([]message.Message, error)
type ShouldStopAfterTurn func(context.Context, []message.Message) (bool, error)
type BeforeToolCall func(context.Context, tool.ToolCall) (tool.ToolCall, error)
type ToolContext func(context.Context, tool.ToolCall) (context.Context, error)
type AfterToolCall func(context.Context, tool.ToolCall, tool.Result) (tool.Result, error)

type Options struct {
	Provider            provider.Provider
	Model               model.Model
	Tools               *tool.Registry
	SystemPrompt        string
	ExecutionMode       tool.ExecutionMode
	QueueMode           QueueMode
	ActiveTools         []string
	Generation          provider.GenerationConfig
	ReasoningEffort     string
	PrepareNextTurn     PrepareNextTurn
	ShouldStopAfterTurn ShouldStopAfterTurn
	ToolBatches         invocation.ToolBatchCoordinator
	BeforeTool          BeforeToolCall
	ToolContext         ToolContext
	AfterTool           AfterToolCall
}
type Agent struct {
	mu          sync.Mutex
	opts        Options
	messages    []message.Message
	subscribers map[uint64]func(Event)
	nextSub     uint64
	active      *Run
}

type StartOptions struct{ ID sharedrun.ID }

type StepResult struct {
	Reason    sharedrun.EndReason
	ModelTurn bool
	ToolCalls int
}

type stepCommand struct {
	result chan stepResponse
	finish bool
}

type stepResponse struct {
	result StepResult
	err    error
}

type Run struct {
	mu                 sync.Mutex
	agent              *Agent
	id                 sharedrun.ID
	ctx                context.Context
	cancel             context.CancelCauseFunc
	done               chan struct{}
	status             sharedrun.Status
	err                error
	cause              sharedrun.Cause
	runStart           int
	steering, followup []message.Message
	validatorFailures  map[validatorFailureKey]int
	commands           chan stepCommand
	stepMu             sync.Mutex
}

type validatorFailureKey struct {
	tool           string
	validatorIndex int
}

func New(opts Options) *Agent {
	if opts.ExecutionMode == "" {
		opts.ExecutionMode = tool.Parallel
	}
	if opts.QueueMode == "" {
		opts.QueueMode = QueueAll
	}
	if opts.Tools == nil {
		opts.Tools = tool.NewRegistry()
	}
	return &Agent{opts: opts, subscribers: make(map[uint64]func(Event))}
}
func (a *Agent) On(handler func(Event)) func() {
	a.mu.Lock()
	id := a.nextSub
	a.nextSub++
	a.subscribers[id] = handler
	a.mu.Unlock()
	return func() { a.mu.Lock(); delete(a.subscribers, id); a.mu.Unlock() }
}
func (a *Agent) emit(e Event) {
	e = snapshotEvent(e)
	a.mu.Lock()
	if e.RunID == "" && a.active != nil {
		e.RunID = a.active.id
	}
	hs := make([]func(Event), 0, len(a.subscribers))
	for _, h := range a.subscribers {
		hs = append(hs, h)
	}
	a.mu.Unlock()
	for _, h := range hs {
		h(e)
	}
}
func (a *Agent) Messages() []message.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]message.Message(nil), a.messages...)
}
func (a *Agent) ActiveRun() *Run { a.mu.Lock(); defer a.mu.Unlock(); return a.active }
func (a *Agent) ReplaceMessages(ms []message.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append([]message.Message(nil), ms...)
}
func (a *Agent) Start(ctx context.Context, p Prompt, opts StartOptions) (*Run, error) {
	return a.start(ctx, p, opts, false)
}

func StartStepped(a *Agent, ctx context.Context, p Prompt, opts StartOptions) (*Run, error) {
	return a.start(ctx, p, opts, true)
}

func (a *Agent) start(ctx context.Context, p Prompt, opts StartOptions, stepped bool) (*Run, error) {
	a.mu.Lock()
	if a.active != nil {
		a.mu.Unlock()
		return nil, ErrBusy
	}
	if a.opts.Provider == nil {
		a.mu.Unlock()
		return nil, errors.New("provider is required")
	}
	steps, err := p.Steps.Normalize()
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	if err = a.validateScriptTools(steps); err != nil {
		a.mu.Unlock()
		return nil, err
	}
	id, err := sharedrun.ResolveID(opts.ID)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	r := &Run{
		agent:             a,
		id:                id,
		ctx:               runCtx,
		cancel:            cancel,
		done:              make(chan struct{}),
		status:            sharedrun.StatusRunning,
		runStart:          len(a.messages),
		validatorFailures: make(map[validatorFailureKey]int),
	}
	if stepped {
		r.commands = make(chan stepCommand)
	}
	a.active = r
	a.mu.Unlock()
	if stepped {
		go a.runStepped(r, steps)
	} else {
		go a.run(r, steps)
	}
	return r, nil
}

func SetActiveTools(a *Agent, names []string) {
	a.mu.Lock()
	if names == nil {
		a.opts.ActiveTools = nil
	} else {
		a.opts.ActiveTools = append([]string{}, names...)
	}
	a.mu.Unlock()
}

func ValidatePrompt(a *Agent, p Prompt) error {
	steps, err := p.Steps.Normalize()
	if err != nil {
		return err
	}
	return a.validateScriptTools(steps)
}

func (a *Agent) validateScriptTools(steps prompt.Sequence) error {
	active := make(map[string]struct{}, len(a.opts.ActiveTools))
	for _, name := range a.opts.ActiveTools {
		active[name] = struct{}{}
	}
	for stepIndex, raw := range steps {
		step, ok := raw.(prompt.ToolCallsStep)
		if !ok {
			continue
		}
		for callIndex, call := range step.Calls {
			if a.opts.ActiveTools != nil {
				if _, ok := active[call.Name]; !ok {
					return fmt.Errorf("prompt step %d tool call %d: tool %q is not active", stepIndex, callIndex, call.Name)
				}
			}
			if err := a.opts.Tools.Validate(tool.ToolCall{Name: call.Name, Arguments: call.Arguments}); err != nil {
				return fmt.Errorf("prompt step %d tool call %d: %w", stepIndex, callIndex, err)
			}
		}
	}
	return nil
}
func (r *Run) ID() sharedrun.ID         { return r.id }
func (r *Run) Status() sharedrun.Status { r.mu.Lock(); defer r.mu.Unlock(); return r.status }
func (r *Run) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !sharedrun.Terminal(r.status) {
		return nil
	}
	return r.err
}
func (r *Run) Done() <-chan struct{} { return r.done }
func (r *Run) Wait(ctx context.Context) error {
	select {
	case <-r.done:
		return r.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *Run) Abort() bool {
	r.mu.Lock()
	if r.status != sharedrun.StatusRunning {
		r.mu.Unlock()
		return false
	}
	r.status = sharedrun.StatusCancelling
	r.steering = nil
	r.followup = nil
	cancel := r.cancel
	r.mu.Unlock()
	cancel(sharedrun.ErrAborted)
	r.agent.emit(Event{Type: EventQueueUpdate, RunID: r.id})
	return true
}
func (r *Run) Steer(m message.Message) error {
	r.mu.Lock()
	if r.status != sharedrun.StatusRunning {
		r.mu.Unlock()
		return sharedrun.ErrFinished
	}
	r.steering = append(r.steering, m)
	r.mu.Unlock()
	r.agent.emit(Event{Type: EventQueueUpdate, RunID: r.id})
	return nil
}
func (r *Run) FollowUp(m message.Message) error {
	r.mu.Lock()
	if r.status != sharedrun.StatusRunning {
		r.mu.Unlock()
		return sharedrun.ErrFinished
	}
	r.followup = append(r.followup, m)
	r.mu.Unlock()
	r.agent.emit(Event{Type: EventQueueUpdate, RunID: r.id})
	return nil
}

func Next(r *Run, ctx context.Context) (StepResult, error) {
	if r.commands == nil {
		return StepResult{}, errors.New("agent run is not stepped")
	}
	if !r.stepMu.TryLock() {
		return StepResult{}, ErrBusy
	}
	defer r.stepMu.Unlock()
	response := make(chan stepResponse, 1)
	select {
	case r.commands <- stepCommand{result: response}:
	case <-r.done:
		return StepResult{}, sharedrun.ErrFinished
	case <-ctx.Done():
		return StepResult{}, ctx.Err()
	}
	select {
	case got := <-response:
		return got.result, got.err
	case <-r.done:
		select {
		case got := <-response:
			return got.result, got.err
		default:
		}
		return StepResult{}, r.Err()
	case <-ctx.Done():
		r.cancel(ctx.Err())
		return StepResult{}, ctx.Err()
	}
}

func Complete(r *Run) error {
	if r.commands == nil {
		return errors.New("agent run is not stepped")
	}
	select {
	case r.commands <- stepCommand{finish: true}:
	case <-r.done:
		return r.Err()
	}
	return r.Wait(context.Background())
}

func (r *Run) observeValidatorResult(toolName string, validationErr error) (error, error) {
	if validationErr == nil {
		for key := range r.validatorFailures {
			if key.tool == toolName {
				delete(r.validatorFailures, key)
			}
		}
		return nil, nil
	}

	var failure *tool.ValidatorFailure
	if !errors.As(validationErr, &failure) {
		return validationErr, nil
	}
	for key := range r.validatorFailures {
		if key.tool == toolName && key.validatorIndex < failure.ValidatorIndex {
			delete(r.validatorFailures, key)
		}
	}
	if !failure.HasRetryLimit {
		return validationErr, nil
	}

	key := validatorFailureKey{tool: toolName, validatorIndex: failure.ValidatorIndex}
	failures := r.validatorFailures[key] + 1
	r.validatorFailures[key] = failures
	if failures <= failure.RetryLimit {
		remaining := failure.RetryLimit - failures + 1
		return fmt.Errorf(
			"%w (validator retry limit: %d; %d retries remaining)",
			validationErr,
			failure.RetryLimit,
			remaining,
		), nil
	}

	limitErr := &ValidatorRetryLimitError{
		Tool:           toolName,
		ValidatorIndex: failure.ValidatorIndex,
		RetryLimit:     failure.RetryLimit,
		Failures:       failures,
		LastErr:        validationErr,
	}
	return limitErr, limitErr
}
func (a *Agent) finish(r *Run, err error) {
	status, cause, finalErr := classify(r.ctx, err)
	r.mu.Lock()
	r.err = finalErr
	r.status = status
	r.cause = cause
	r.steering = nil
	r.followup = nil
	r.mu.Unlock()
	a.mu.Lock()
	ms := append([]message.Message(nil), a.messages[r.runStart:]...)
	a.mu.Unlock()
	if finalErr != nil {
		a.emit(Event{Type: EventError, RunID: r.id, Err: finalErr})
	}
	a.emit(Event{Type: EventAgentEnd, RunID: r.id, Messages: ms})
	a.mu.Lock()
	if a.active == r {
		a.active = nil
	}
	close(r.done)
	a.mu.Unlock()
}

func classify(ctx context.Context, err error) (sharedrun.Status, sharedrun.Cause, error) {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, sharedrun.ErrAborted):
		return sharedrun.StatusAborted, sharedrun.CauseUserAbort, sharedrun.ErrAborted
	case errors.Is(cause, context.DeadlineExceeded):
		return sharedrun.StatusAborted, sharedrun.CauseDeadline, context.DeadlineExceeded
	case errors.Is(cause, context.Canceled):
		return sharedrun.StatusAborted, sharedrun.CauseParentCanceled, context.Canceled
	case cause != nil:
		return sharedrun.StatusFailed, sharedrun.CauseInternal, cause
	case err == nil:
		return sharedrun.StatusCompleted, sharedrun.CauseNone, nil
	case errors.Is(err, ErrProviderAborted):
		return sharedrun.StatusAborted, sharedrun.CauseProviderAbort, err
	default:
		return sharedrun.StatusFailed, sharedrun.CauseInternal, err
	}
}

func (a *Agent) run(r *Run, steps prompt.Sequence) {
	ctx := r.ctx
	a.emit(Event{Type: EventAgentStart})
	enterAgentLoop, _, _, err := a.executePromptSteps(r, ctx, steps)
	if err != nil {
		a.finish(r, err)
		return
	}
	if !enterAgentLoop {
		a.finish(r, nil)
		return
	}
	// Steering queued while the request was being started belongs in the first
	// provider request, not after the first assistant response.
	a.drain(r, true)
	for {
		result, err := a.executeModelStep(r, ctx)
		if err != nil {
			a.finish(r, err)
			return
		}
		if result.Reason != sharedrun.EndReasonNone {
			a.finish(r, nil)
			return
		}
	}
}

func (a *Agent) runStepped(r *Run, steps prompt.Sequence) {
	ctx := r.ctx
	a.emit(Event{Type: EventAgentStart})
	first := true
	previousReason := sharedrun.EndReasonNone
	for {
		select {
		case <-ctx.Done():
			a.finish(r, context.Cause(ctx))
			return
		case command := <-r.commands:
			if command.finish {
				a.finish(r, nil)
				return
			}
			result := StepResult{}
			var err error
			if first {
				first = false
				var enter bool
				var promptReason sharedrun.EndReason
				var promptTools int
				enter, promptReason, promptTools, err = a.executePromptSteps(r, ctx, steps)
				result.ToolCalls += promptTools
				if err == nil && !enter {
					result.Reason = promptReason
				}
				if err != nil || !enter {
					previousReason = result.Reason
					command.result <- stepResponse{result: result, err: err}
					if err != nil {
						a.finish(r, err)
						return
					}
					continue
				}
				a.drain(r, true)
			} else if previousReason != sharedrun.EndReasonNone {
				if !a.drain(r, true) {
					a.drain(r, false)
				}
			}
			step, stepErr := a.executeModelStep(r, ctx)
			result.Reason = step.Reason
			result.ModelTurn = step.ModelTurn
			result.ToolCalls += step.ToolCalls
			previousReason = result.Reason
			command.result <- stepResponse{result: result, err: stepErr}
			if stepErr != nil {
				a.finish(r, stepErr)
				return
			}
		}
	}
}

func (a *Agent) executeModelStep(r *Run, ctx context.Context) (result StepResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &sharedrun.PanicError{Value: recovered, Stack: debug.Stack()}
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	a.emit(Event{Type: EventTurnStart})
	assistant, calls, err := a.safeModelTurn(ctx)
	if err != nil {
		a.appendFailureMessage(assistant, ctx, err)
		return result, err
	}
	result.ModelTurn = true
	result.ToolCalls = len(calls)
	if err := ctx.Err(); err != nil {
		a.appendFailureMessage(assistant, ctx, err)
		return result, err
	}
	if assistant.StopReason == message.StopError && assistant.ErrorMessage == "" {
		assistant.ErrorMessage = "provider returned an error stop reason"
	}
	if assistant.StopReason == message.StopAborted && assistant.ErrorMessage == "" {
		assistant.ErrorMessage = "provider aborted run"
	}
	a.appendMessage(assistant)
	if assistant.StopReason == message.StopError || assistant.StopReason == message.StopAborted {
		a.emit(Event{Type: EventTurnEnd, Message: &assistant})
		err := errors.New("provider returned an error stop reason")
		if assistant.StopReason == message.StopAborted {
			err = ErrProviderAborted
		}
		return result, err
	}
	terminate := false
	if len(calls) > 0 {
		var results []message.Message
		if assistant.StopReason == message.StopLength {
			results = a.failTruncatedCalls(calls)
		} else {
			results, terminate, _, err = a.executeCalls(r, ctx, calls)
		}
		for _, m := range results {
			a.appendMessage(m)
		}
		if err != nil {
			return result, err
		}
	}
	a.emit(Event{Type: EventTurnEnd, Message: &assistant})
	preparedMore, stop, err := a.afterTurn(ctx)
	if err != nil {
		return result, err
	}
	if stop {
		result.Reason = sharedrun.EndReasonLoopStopped
		return result, nil
	}
	if a.drain(r, true) {
		return result, nil
	}
	if preparedMore || (len(calls) > 0 && !terminate) {
		return result, nil
	}
	if a.drain(r, false) {
		return result, nil
	}
	if terminate {
		result.Reason = sharedrun.EndReasonToolTerminate
	} else {
		result.Reason = sharedrun.EndReasonAssistantStop
	}
	return result, nil
}

func (a *Agent) safeModelTurn(ctx context.Context) (assistant message.Message, calls []tool.ToolCall, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &sharedrun.PanicError{Value: recovered, Stack: debug.Stack()}
		}
	}()
	return a.modelTurn(ctx)
}

func (a *Agent) executePromptSteps(r *Run, ctx context.Context, steps prompt.Sequence) (bool, sharedrun.EndReason, int, error) {
	toolCalls := 0
	for _, raw := range steps {
		if err := ctx.Err(); err != nil {
			return false, sharedrun.EndReasonNone, toolCalls, err
		}
		switch step := raw.(type) {
		case prompt.UserMessageStep:
			m := message.Message{Role: message.RoleUser, Origin: message.OriginUser, Content: step.Content, Timestamp: time.Now().UnixMilli()}
			a.emit(Event{Type: EventMessageStart, Message: &m})
			a.appendMessage(m)
		case prompt.AssistantMessageStep:
			m := message.Message{Role: message.RoleAssistant, Origin: message.OriginScript, Content: step.Content, Timestamp: time.Now().UnixMilli(), StopReason: message.StopStop}
			a.emit(Event{Type: EventTurnStart})
			a.emit(Event{Type: EventMessageStart, Message: &m})
			a.appendMessage(m)
			if err := ctx.Err(); err != nil {
				return false, sharedrun.EndReasonNone, toolCalls, err
			}
			a.emit(Event{Type: EventTurnEnd, Message: &m})
			_, stop, err := a.afterTurn(ctx)
			if err != nil || stop {
				return false, sharedrun.EndReasonPromptDone, toolCalls, err
			}
		case prompt.ToolCallsStep:
			for _, planned := range step.Calls {
				toolCalls++
				call := tool.ToolCall{ID: "call_" + xid.New().String(), Key: planned.Key, Name: planned.Name, Arguments: planned.Arguments.Clone()}
				content := message.ToolCall(call.ID, call.Name, call.Arguments)
				content.Key = call.Key
				assistant := message.Message{Role: message.RoleAssistant, Origin: message.OriginScript, Content: []message.Content{content}, Timestamp: time.Now().UnixMilli(), StopReason: message.StopToolUse}
				a.emit(Event{Type: EventTurnStart})
				a.emit(Event{Type: EventMessageStart, Message: &assistant})
				a.appendMessage(assistant)
				if err := ctx.Err(); err != nil {
					return false, sharedrun.EndReasonNone, toolCalls, err
				}
				results, terminate, callErrors, err := a.executeCalls(r, ctx, []tool.ToolCall{call})
				for _, result := range results {
					a.appendMessage(result)
				}
				if err != nil {
					return false, sharedrun.EndReasonNone, toolCalls, err
				}
				a.emit(Event{Type: EventTurnEnd, Message: &assistant})
				failed := len(results) > 0 && results[0].IsError
				if failed && planned.OnError == prompt.OnErrorAbort {
					cause := errors.New("tool returned an error result")
					if len(callErrors) > 0 && callErrors[0] != nil {
						cause = callErrors[0]
					} else if text := results[0].Text(); text != "" {
						cause = errors.New(text)
					}
					return false, sharedrun.EndReasonNone, toolCalls, &ScriptToolError{Key: planned.Key, Name: planned.Name, Cause: cause}
				}
				_, stop, err := a.afterTurn(ctx)
				if err != nil || stop || terminate {
					reason := sharedrun.EndReasonPromptDone
					if terminate {
						reason = sharedrun.EndReasonToolTerminate
					}
					return false, reason, toolCalls, err
				}
				if !failed {
					continue
				}
				switch planned.OnError {
				case prompt.OnErrorContinue:
					continue
				case prompt.OnErrorEnterAgentLoop:
					return true, sharedrun.EndReasonNone, toolCalls, nil
				}
			}
		}
	}
	return true, sharedrun.EndReasonNone, toolCalls, nil
}

func (a *Agent) afterTurn(ctx context.Context) (preparedMore, stop bool, err error) {
	if a.opts.PrepareNextTurn != nil {
		ms, prepareErr := a.opts.PrepareNextTurn(ctx, a.Messages())
		if prepareErr != nil {
			return false, false, prepareErr
		}
		for _, m := range ms {
			a.appendMessage(m)
		}
		preparedMore = len(ms) > 0
	}
	if a.opts.ShouldStopAfterTurn != nil {
		stop, err = a.opts.ShouldStopAfterTurn(ctx, a.Messages())
	}
	return preparedMore, stop, err
}
func (a *Agent) appendMessage(m message.Message) {
	a.mu.Lock()
	a.messages = append(a.messages, m)
	a.mu.Unlock()
	a.emit(Event{Type: EventMessageEnd, Message: &m})
}
func (a *Agent) drain(r *Run, steering bool) bool {
	r.mu.Lock()
	queue := &r.followup
	if steering {
		queue = &r.steering
	}
	if len(*queue) == 0 {
		r.mu.Unlock()
		return false
	}
	n := len(*queue)
	if a.opts.QueueMode == QueueOneAtATime {
		n = 1
	}
	ms := append([]message.Message(nil), (*queue)[:n]...)
	*queue = (*queue)[n:]
	r.mu.Unlock()
	a.mu.Lock()
	a.messages = append(a.messages, ms...)
	a.mu.Unlock()
	for i := range ms {
		a.emit(Event{Type: EventMessageStart, Message: &ms[i]})
		a.emit(Event{Type: EventMessageEnd, Message: &ms[i]})
	}
	a.emit(Event{Type: EventQueueUpdate})
	return true
}

func (a *Agent) appendFailureMessage(m message.Message, ctx context.Context, err error) {
	started := m.Role != ""
	if !started {
		m = message.Message{
			Role:      message.RoleAssistant,
			Origin:    message.OriginModel,
			Content:   []message.Content{message.Text("")},
			Timestamp: time.Now().UnixMilli(),
			API:       a.opts.Model.API,
			Provider:  a.opts.Model.Provider,
			Model:     a.opts.Model.ID,
		}
	}
	if len(m.Content) == 0 {
		m.Content = []message.Content{message.Text("")}
	}
	m.StopReason = message.StopError
	if context.Cause(ctx) != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrProviderAborted) {
		m.StopReason = message.StopAborted
	}
	if err != nil {
		m.ErrorMessage = err.Error()
	}
	if !started {
		a.emit(Event{Type: EventMessageStart, Message: &m})
	}
	a.appendMessage(m)
	a.emit(Event{Type: EventTurnEnd, Message: &m})
}

type assembledCall struct {
	call tool.ToolCall
	args string
}

func (a *Agent) modelTurn(ctx context.Context) (message.Message, []tool.ToolCall, error) {
	a.mu.Lock()
	activeTools := append([]string(nil), a.opts.ActiveTools...)
	if a.opts.ActiveTools != nil && len(a.opts.ActiveTools) == 0 {
		activeTools = []string{}
	}
	a.mu.Unlock()
	req := provider.Request{Model: a.opts.Model, SystemPrompt: a.opts.SystemPrompt, Messages: message.ExpandLargeText(a.Messages()), Tools: a.opts.Tools.Definitions(activeTools), MaxTokens: a.opts.Model.MaxOutput, ReasoningEffort: a.opts.ReasoningEffort, Generation: a.opts.Generation.Clone()}
	stream, err := a.opts.Provider.Stream(ctx, req)
	if err != nil {
		return message.Message{}, nil, err
	}
	defer stream.Close()
	m := message.Message{Role: message.RoleAssistant, Origin: message.OriginModel, Timestamp: time.Now().UnixMilli(), API: a.opts.Model.API, Provider: a.opts.Model.Provider, Model: a.opts.Model.ID}
	a.emit(Event{Type: EventMessageStart, Message: &m})
	calls := map[int]*assembledCall{}
	order := []int{}
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, nil, err
		}
		a.emit(Event{Type: EventMessageUpdate, Message: &m, Stream: &ev})
		switch ev.Type {
		case message.EventTextDelta:
			if len(m.Content) == 0 || m.Content[len(m.Content)-1].Type != "text" {
				m.Content = append(m.Content, message.Text(""))
			}
			m.Content[len(m.Content)-1].Text += ev.Text
		case message.EventThinkingDelta:
			if len(m.Content) == 0 || m.Content[len(m.Content)-1].Type != "thinking" {
				m.Content = append(m.Content, message.Thinking(""))
			}
			m.Content[len(m.Content)-1].Thinking += ev.Text
			if m.Content[len(m.Content)-1].Signature == "" {
				m.Content[len(m.Content)-1].Signature = ev.Signature
			}
		case message.EventToolCallStart, message.EventToolCallDelta:
			c := calls[ev.Index]
			if c == nil {
				c = &assembledCall{call: tool.ToolCall{ID: ev.ToolCallID, Name: ev.ToolName}}
				calls[ev.Index] = c
				order = append(order, ev.Index)
			}
			if ev.ToolCallID != "" {
				c.call.ID = ev.ToolCallID
			}
			if ev.ToolName != "" {
				c.call.Name = ev.ToolName
			}
			c.args += ev.ArgumentsDelta
		case message.EventDone:
			if ev.StopReason != "" {
				m.StopReason = ev.StopReason
			}
			if ev.Usage.TotalTokens > 0 || ev.Usage.InputTokens > 0 {
				m.Usage = ev.Usage
			}
			if len(ev.ProviderMetadata) > 0 {
				if m.ProviderMetadata == nil {
					m.ProviderMetadata = make(map[string]json.RawMessage)
				}
				for key, value := range ev.ProviderMetadata {
					m.ProviderMetadata[key] = value.Clone()
				}
			}
		case message.EventError:
			return m, nil, ev.Err
		}
	}
	out := make([]tool.ToolCall, 0, len(order))
	for _, i := range order {
		c := calls[i]
		c.call.Arguments = normalizeArguments(c.args)
		out = append(out, c.call)
		m.Content = append(m.Content, message.ToolCall(c.call.ID, c.call.Name, c.call.Arguments))
	}
	return m, out, nil
}

func normalizeArguments(raw string) json.RawMessage {
	if raw == "" {
		return json.RawMessage(`{}`)
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) == nil {
		return json.RawMessage(raw)
	}
	// Provider replay must always contain syntactically valid JSON. Argument
	// validation will turn this fallback into a tool error rather than sending a
	// malformed tool_calls payload on the next turn.
	return json.RawMessage(`{}`)
}

func (a *Agent) executeCalls(r *Run, ctx context.Context, calls []tool.ToolCall) (results []message.Message, terminate bool, errs []error, err error) {
	results = make([]message.Message, len(calls))
	errs = make([]error, len(calls))
	terminated := make([]bool, len(calls))
	started := make([]bool, len(calls))
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr := &sharedrun.PanicError{Value: recovered, Stack: debug.Stack()}
			for i, call := range calls {
				if results[i].Role != "" {
					continue
				}
				if !started[i] {
					a.emit(Event{Type: EventToolStart, Call: &call})
				}
				res := tool.Result{Content: []message.Content{message.Text(panicErr.Error())}, IsError: true}
				errs[i] = panicErr
				results[i] = message.Message{Role: message.RoleTool, Origin: message.OriginTool, Content: res.Content, ToolCallID: call.ID, ToolCallKey: call.Key, ToolName: call.Name, IsError: true, Timestamp: time.Now().UnixMilli()}
				a.emit(Event{Type: EventToolEnd, Call: &call, Result: &res, Err: panicErr})
			}
			terminate = false
			err = panicErr
		}
	}()
	var wg sync.WaitGroup
	var parallelBatch invocation.ToolBatch
	var commitErr error
	var validatorLimitErr error
	mode := a.opts.ExecutionMode
	for _, c := range calls {
		if m, ok := a.opts.Tools.Mode(c.Name); ok && m == tool.Sequential {
			mode = tool.Sequential
			break
		}
	}
	recordCanceled := func(i int, call tool.ToolCall, err error) {
		res := tool.Result{Content: []message.Content{message.Text(err.Error())}, IsError: true}
		errs[i] = err
		results[i] = message.Message{Role: message.RoleTool, Origin: message.OriginTool, Content: res.Content, ToolCallID: call.ID, ToolCallKey: call.Key, ToolName: call.Name, IsError: true, Timestamp: time.Now().UnixMilli()}
		a.emit(Event{Type: EventToolEnd, Call: &call, Result: &res, Err: err})
	}
	recordPrepareError := func(i int, call tool.ToolCall, validationErr error) {
		displayErr, limitErr := r.observeValidatorResult(call.Name, validationErr)
		if limitErr != nil && validatorLimitErr == nil {
			validatorLimitErr = limitErr
		}
		res := tool.Result{Content: []message.Content{message.Text(displayErr.Error())}, IsError: true}
		var blocked *tool.BlockedError
		if errors.As(displayErr, &blocked) {
			res.Terminate = blocked.Terminate
			terminated[i] = blocked.Terminate
		}
		errs[i] = displayErr
		a.emit(Event{Type: EventToolEnd, Call: &call, Result: &res, Err: displayErr})
		results[i] = message.Message{Role: message.RoleTool, Origin: message.OriginTool, Content: res.Content, ToolCallID: call.ID, ToolCallKey: call.Key, ToolName: call.Name, IsError: true, Timestamp: time.Now().UnixMilli()}
	}
	run := func(i int, prepared *tool.Prepared, call tool.ToolCall, baseCtx context.Context) {
		finished := false
		defer func() {
			if recovered := recover(); recovered != nil && !finished {
				panicErr := &sharedrun.PanicError{Value: recovered, Stack: debug.Stack()}
				res := tool.Result{Content: []message.Content{message.Text(panicErr.Error())}, IsError: true}
				errs[i] = panicErr
				terminated[i] = false
				results[i] = message.Message{Role: message.RoleTool, Origin: message.OriginTool, Content: res.Content, ToolCallID: call.ID, ToolCallKey: call.Key, ToolName: call.Name, IsError: true, Timestamp: time.Now().UnixMilli()}
				a.emit(Event{Type: EventToolEnd, Call: &call, Result: &res, Err: panicErr})
			}
			wg.Done()
		}()
		execCtx := baseCtx
		report := func(v any) error {
			a.emit(Event{Type: EventToolUpdate, Call: &call, Update: v})
			return nil
		}
		var sequentialBatch invocation.ToolBatch
		if a.opts.ToolBatches != nil {
			if mode == tool.Parallel {
				execCtx = parallelBatch.Context(i, baseCtx, call, report)
			} else {
				sequentialBatch, commitErr = a.opts.ToolBatches.BeginToolBatch(baseCtx, []tool.ToolCall{call}, false)
				if commitErr != nil {
					err := commitErr
					errs[i] = err
					results[i] = message.Message{Role: message.RoleTool, Origin: message.OriginTool, Content: []message.Content{message.Text(err.Error())}, ToolCallID: call.ID, ToolCallKey: call.Key, ToolName: call.Name, IsError: true, Timestamp: time.Now().UnixMilli()}
					return
				}
				execCtx = sequentialBatch.Context(0, baseCtx, call, report)
			}
		}
		res, err := prepared.Execute(execCtx, func(v any) { _ = report(v) })
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		if err != nil {
			res.IsError = true
			if len(res.Content) == 0 {
				res.Content = []message.Content{message.Text(err.Error())}
			}
		}
		if a.opts.AfterTool != nil {
			updated, hookErr := a.opts.AfterTool(execCtx, call, res)
			if hookErr != nil {
				res = tool.Result{Content: []message.Content{message.Text(hookErr.Error())}, IsError: true}
				err = hookErr
			} else {
				res = updated
			}
		}
		errs[i] = err
		terminated[i] = res.Terminate
		if sequentialBatch != nil && ctx.Err() == nil {
			outcome := invocation.ToolOutcome{Err: err, IsError: res.IsError, Terminate: res.Terminate}
			if stateErr := sequentialBatch.Commit(ctx, []invocation.ToolOutcome{outcome}); stateErr != nil {
				err = stateErr
				commitErr = stateErr
				errs[i] = stateErr
				res = tool.Result{Content: []message.Content{message.Text(stateErr.Error())}, IsError: true}
				terminated[i] = false
			}
		}
		results[i] = message.Message{Role: message.RoleTool, Origin: message.OriginTool, Content: res.Content, ToolCallID: call.ID, ToolCallKey: call.Key, ToolName: call.Name, IsError: res.IsError, Timestamp: time.Now().UnixMilli()}
		a.emit(Event{Type: EventToolEnd, Call: &call, Result: &res, Err: err})
		finished = true
	}
	preparedCalls := make([]*tool.Prepared, len(calls))
	preparedValues := make([]tool.ToolCall, len(calls))
	preparedContexts := make([]context.Context, len(calls))
	for i, original := range calls {
		call := original
		a.emit(Event{Type: EventToolStart, Call: &call})
		started[i] = true
		if err := ctx.Err(); err != nil {
			recordCanceled(i, call, err)
			continue
		}
		prepared, err := a.opts.Tools.Prepare(ctx, call)
		if err != nil {
			recordPrepareError(i, call, err)
			continue
		}
		call = prepared.Call()
		if a.opts.BeforeTool != nil {
			before := call.Arguments.Clone()
			call, err = a.opts.BeforeTool(ctx, call)
			if err != nil {
				_, _ = r.observeValidatorResult(call.Name, nil)
				res := tool.Result{Content: []message.Content{message.Text(err.Error())}, IsError: true}
				errs[i] = err
				a.emit(Event{Type: EventToolEnd, Call: &call, Result: &res, Err: err})
				results[i] = message.Message{Role: message.RoleTool, Origin: message.OriginTool, Content: res.Content, ToolCallID: call.ID, ToolCallKey: call.Key, ToolName: call.Name, IsError: true, Timestamp: time.Now().UnixMilli()}
				continue
			}
			if string(before) != string(call.Arguments) {
				prepared, err = a.opts.Tools.Prepare(ctx, call)
				if err != nil {
					recordPrepareError(i, call, err)
					continue
				}
				call = prepared.Call()
			}
		}
		toolCtx := ctx
		if a.opts.ToolContext != nil {
			toolCtx, err = a.opts.ToolContext(ctx, call)
			if err != nil {
				res := tool.Result{Content: []message.Content{message.Text(err.Error())}, IsError: true}
				errs[i] = err
				a.emit(Event{Type: EventToolEnd, Call: &call, Result: &res, Err: err})
				results[i] = message.Message{Role: message.RoleTool, Origin: message.OriginTool, Content: res.Content, ToolCallID: call.ID, ToolCallKey: call.Key, ToolName: call.Name, IsError: true, Timestamp: time.Now().UnixMilli()}
				continue
			}
			if toolCtx == nil {
				err = errors.New("tool context hook returned a nil context")
				res := tool.Result{Content: []message.Content{message.Text(err.Error())}, IsError: true}
				errs[i] = err
				a.emit(Event{Type: EventToolEnd, Call: &call, Result: &res, Err: err})
				results[i] = message.Message{Role: message.RoleTool, Origin: message.OriginTool, Content: res.Content, ToolCallID: call.ID, ToolCallKey: call.Key, ToolName: call.Name, IsError: true, Timestamp: time.Now().UnixMilli()}
				continue
			}
		}
		_, _ = r.observeValidatorResult(call.Name, nil)
		if mode == tool.Sequential {
			wg.Add(1)
			run(i, prepared, call, toolCtx)
			if commitErr != nil {
				for j := i + 1; j < len(calls); j++ {
					recordCanceled(j, calls[j], commitErr)
				}
				break
			}
		} else {
			preparedCalls[i] = prepared
			preparedValues[i] = call
			preparedContexts[i] = toolCtx
		}
	}
	if mode == tool.Parallel {
		// All argument validation and before hooks complete in source order before
		// any tool body starts.
		if a.opts.ToolBatches != nil {
			var batchErr error
			parallelBatch, batchErr = a.opts.ToolBatches.BeginToolBatch(ctx, preparedValues, true)
			if batchErr != nil {
				return results, false, errs, batchErr
			}
		}
		for i, prepared := range preparedCalls {
			if prepared == nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				recordCanceled(i, preparedValues[i], err)
				continue
			}
			wg.Add(1)
			go run(i, prepared, preparedValues[i], preparedContexts[i])
		}
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return results, false, errs, err
	}
	if mode == tool.Parallel && parallelBatch != nil {
		for i, prepared := range preparedCalls {
			if prepared == nil {
				continue
			}
			outcome := invocation.ToolOutcome{Err: errs[i], IsError: results[i].IsError, Terminate: terminated[i]}
			if err := parallelBatch.CommitOne(ctx, i, outcome); err != nil {
				return results, false, errs, err
			}
		}
	}
	if commitErr != nil {
		return results, false, errs, commitErr
	}
	if validatorLimitErr != nil {
		return results, false, errs, validatorLimitErr
	}
	for _, callErr := range errs {
		var panicErr *sharedrun.PanicError
		if errors.As(callErr, &panicErr) {
			return results, false, errs, panicErr
		}
	}
	terminate = len(terminated) > 0
	for _, stop := range terminated {
		terminate = terminate && stop
	}
	return results, terminate, errs, nil
}

func (a *Agent) failTruncatedCalls(calls []tool.ToolCall) []message.Message {
	results := make([]message.Message, 0, len(calls))
	for i := range calls {
		call := calls[i]
		a.emit(Event{Type: EventToolStart, Call: &call})
		res := tool.Result{Content: []message.Content{message.Text("Tool call \"" + call.Name + "\" was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.")}, IsError: true}
		a.emit(Event{Type: EventToolEnd, Call: &call, Result: &res})
		results = append(results, message.Message{Role: message.RoleTool, Origin: message.OriginTool, Content: res.Content, ToolCallID: call.ID, ToolCallKey: call.Key, ToolName: call.Name, IsError: true, Timestamp: time.Now().UnixMilli()})
	}
	return results
}
