package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go/internal/agent"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	"github.com/dcalsky/best-harness-go/internal/prompt"
	"github.com/dcalsky/best-harness-go/internal/provider"
	sharedrun "github.com/dcalsky/best-harness-go/internal/run"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

type scripted struct {
	mu   sync.Mutex
	turn int
}

type validatorRetryProvider struct {
	values []string
	turn   int
}

func (p *validatorRetryProvider) Stream(context.Context, provider.Request) (provider.Stream, error) {
	if p.turn >= len(p.values) {
		return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventDone, StopReason: message.StopStop}}}, nil
	}
	value := p.values[p.turn]
	p.turn++
	return &provider.SliceStream{Events: []message.StreamEvent{
		{Type: message.EventToolCallStart, ToolCallID: fmt.Sprintf("call-%d", p.turn), ToolName: "validate", ArgumentsDelta: fmt.Sprintf(`{"text":%q}`, value)},
		{Type: message.EventDone, StopReason: message.StopToolUse},
	}}, nil
}

func (p *scripted) Stream(context.Context, provider.Request) (provider.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turn++
	if p.turn == 1 {
		return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventToolCallStart, Index: 0, ToolCallID: "c1", ToolName: "echo", ArgumentsDelta: `{"text":"ok"}`}, {Type: message.EventDone, StopReason: message.StopToolUse}}}, nil
	}
	return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventTextDelta, Text: "done"}, {Type: message.EventDone, StopReason: message.StopStop}}}, nil
}

type echoParams struct {
	Text string `json:"text"`
}

func startAgent(t *testing.T, a *agent.Agent, p agent.Prompt) *agent.Run {
	t.Helper()
	r, err := a.Start(context.Background(), p, agent.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestToolLoopAndEventOrder(t *testing.T) {
	tools := tool.NewRegistry()
	if err := tools.Register(tool.Tool[echoParams, struct{}]{Name: "echo", Execute: func(_ context.Context, _ tool.ToolCall, p echoParams, _ tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		return tool.ToolResult[struct{}]{Content: []message.Content{message.Text(p.Text)}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.Options{Provider: &scripted{}, Model: model.Model{Provider: "p", ID: "m"}, Tools: tools})
	var types []agent.EventType
	a.On(func(e agent.Event) { types = append(types, e.Type) })
	r := startAgent(t, a, agent.Prompt{Steps: prompt.Sequence{prompt.UserText("go")}})
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ms := a.Messages()
	if len(ms) != 4 || ms[2].Role != message.RoleTool || ms[3].Text() != "done" {
		t.Fatalf("messages=%#v", ms)
	}
	want := []agent.EventType{agent.EventAgentStart, agent.EventMessageStart, agent.EventMessageEnd, agent.EventTurnStart}
	for i, v := range want {
		if types[i] != v {
			t.Fatalf("events=%v", types)
		}
	}
}

func TestValidatorRetryLimitIsPerValidatorAndResetsAfterSuccess(t *testing.T) {
	errRejected := errors.New("text is invalid")
	executed := 0
	registry := tool.NewRegistry()
	if err := registry.Register(tool.Tool[echoParams, struct{}]{
		Name: "validate",
		ValidateArguments: func(params echoParams) error {
			if params.Text != "bad" {
				return nil
			}
			return &tool.ValidatorFailure{
				ValidatorIndex: 0,
				RetryLimit:     1,
				HasRetryLimit:  true,
				Err:            errRejected,
			}
		},
		Execute: func(context.Context, tool.ToolCall, echoParams, tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
			executed++
			return tool.ToolResult[struct{}]{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	p := &validatorRetryProvider{values: []string{"bad", "good", "bad", "bad"}}
	a := agent.New(agent.Options{Provider: p, Model: model.Model{ID: "m"}, Tools: registry})
	run := startAgent(t, a, agent.Prompt{Steps: prompt.Sequence{prompt.UserText("validate")}})
	err := run.Wait(context.Background())
	var limitErr *agent.ValidatorRetryLimitError
	if !errors.As(err, &limitErr) || !errors.Is(err, errRejected) {
		t.Fatalf("run error=%v", err)
	}
	if limitErr.Tool != "validate" || limitErr.ValidatorIndex != 0 || limitErr.RetryLimit != 1 || limitErr.Failures != 2 {
		t.Fatalf("limit error=%#v", limitErr)
	}
	if run.Status() != sharedrun.StatusFailed || p.turn != 4 || executed != 1 {
		t.Fatalf("status=%s provider turns=%d executions=%d", run.Status(), p.turn, executed)
	}
	messages := a.Messages()
	if len(messages) != 9 || !strings.Contains(messages[2].Text(), "1 retries remaining") || !strings.Contains(messages[8].Text(), "exceeded its retry limit") {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestValidatorRetryLimitCountsHookRevalidationOncePerToolCall(t *testing.T) {
	validatorCalls := 0
	registry := tool.NewRegistry()
	if err := registry.Register(tool.Tool[echoParams, struct{}]{
		Name: "validate",
		ValidateArguments: func(params echoParams) error {
			validatorCalls++
			if params.Text != "changed" {
				return nil
			}
			return &tool.ValidatorFailure{
				ValidatorIndex: 0,
				RetryLimit:     1,
				HasRetryLimit:  true,
				Err:            errors.New("changed text is invalid"),
			}
		},
		Execute: func(context.Context, tool.ToolCall, echoParams, tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
			t.Fatal("invalid tool executed")
			return tool.ToolResult[struct{}]{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	registry.AddBeforeHook(func(_ context.Context, call tool.ToolCall) (tool.ToolCall, error) {
		call.Arguments = json.RawMessage(`{"text":"changed"}`)
		return call, nil
	})
	p := &validatorRetryProvider{values: []string{"original", "original"}}
	a := agent.New(agent.Options{Provider: p, Model: model.Model{ID: "m"}, Tools: registry})
	run := startAgent(t, a, agent.Prompt{Steps: prompt.Sequence{prompt.UserText("validate")}})
	err := run.Wait(context.Background())
	var limitErr *agent.ValidatorRetryLimitError
	if !errors.As(err, &limitErr) || limitErr.Failures != 2 {
		t.Fatalf("run error=%v limit=%#v", err, limitErr)
	}
	if p.turn != 2 || validatorCalls != 4 {
		t.Fatalf("provider turns=%d validator calls=%d", p.turn, validatorCalls)
	}
}

func TestValidatorRetryLimitsAreIndependent(t *testing.T) {
	registry := tool.NewRegistry()
	if err := registry.Register(tool.Tool[echoParams, struct{}]{
		Name: "validate",
		ValidateArguments: func(params echoParams) error {
			validatorIndex := 0
			if params.Text == "second" {
				validatorIndex = 1
			}
			return &tool.ValidatorFailure{
				ValidatorIndex: validatorIndex,
				RetryLimit:     1,
				HasRetryLimit:  true,
				Err:            fmt.Errorf("validator %d rejected text", validatorIndex),
			}
		},
		Execute: func(context.Context, tool.ToolCall, echoParams, tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
			t.Fatal("invalid tool executed")
			return tool.ToolResult[struct{}]{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	p := &validatorRetryProvider{values: []string{"first", "second", "first", "first"}}
	a := agent.New(agent.Options{Provider: p, Model: model.Model{ID: "m"}, Tools: registry})
	run := startAgent(t, a, agent.Prompt{Steps: prompt.Sequence{prompt.UserText("validate")}})
	err := run.Wait(context.Background())
	var limitErr *agent.ValidatorRetryLimitError
	if !errors.As(err, &limitErr) || limitErr.ValidatorIndex != 0 || limitErr.Failures != 2 {
		t.Fatalf("run error=%v limit=%#v", err, limitErr)
	}
	if p.turn != 4 {
		t.Fatalf("provider turns=%d", p.turn)
	}
}

type requestCaptureProvider struct{ request provider.Request }

func (p *requestCaptureProvider) Stream(_ context.Context, r provider.Request) (provider.Stream, error) {
	p.request = r
	return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventDone, StopReason: message.StopStop}}}, nil
}

func TestLargeTextExpandsOnlyAtProviderBoundary(t *testing.T) {
	p := &requestCaptureProvider{}
	a := agent.New(agent.Options{Provider: p, Model: model.Model{ID: "m"}})
	original := message.Message{Role: message.RoleUser, Content: []message.Content{
		message.Text("inspect this data"),
		message.LargeText("甲乙丙丁戊己", 4),
	}}
	r := startAgent(t, a, agent.Prompt{Steps: prompt.Sequence{prompt.UserMessageStep{Content: original.Content}}})
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.request.Messages) != 2 || p.request.Messages[0].Text() != "inspect this data" {
		t.Fatalf("provider messages=%#v", p.request.Messages)
	}
	if got := p.request.Messages[1]; got.Role != message.RoleUser || got.Content[0].Type != "text" || !strings.Contains(got.Text(), "kept head and tail from 6 chars") {
		t.Fatalf("large provider message=%#v", got)
	}
	history := a.Messages()
	if len(history) < 1 || history[0].Content[1].Type != "largeText" || history[0].Content[1].Text != "甲乙丙丁戊己" {
		t.Fatalf("history=%#v", history)
	}
}

type blockingStream struct{ ctx context.Context }

func (s blockingStream) Next() (message.StreamEvent, error) {
	<-s.ctx.Done()
	return message.StreamEvent{}, s.ctx.Err()
}
func (s blockingStream) Close() error { return nil }

type blockingProvider struct{}

func (blockingProvider) Stream(ctx context.Context, _ provider.Request) (provider.Stream, error) {
	return blockingStream{ctx}, nil
}
func TestBusyAndAbort(t *testing.T) {
	a := agent.New(agent.Options{Provider: blockingProvider{}, Model: model.Model{ID: "m"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := a.Start(ctx, agent.Prompt{}, agent.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Start(ctx, agent.Prompt{}, agent.StartOptions{}); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("busy error=%v", err)
	}
	r.Abort()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := r.Wait(waitCtx); !errors.Is(err, sharedrun.ErrAborted) {
		t.Fatalf("wait error=%v", err)
	}
}

type partialBlockingStream struct {
	ctx     context.Context
	started chan struct{}
	sent    bool
}

func (s *partialBlockingStream) Next() (message.StreamEvent, error) {
	if !s.sent {
		s.sent = true
		close(s.started)
		return message.StreamEvent{Type: message.EventTextDelta, Text: "partial"}, nil
	}
	<-s.ctx.Done()
	return message.StreamEvent{}, s.ctx.Err()
}
func (s *partialBlockingStream) Close() error { return nil }

func TestRunIdentityWaitAndPartialAbort(t *testing.T) {
	started := make(chan struct{})
	a := agent.New(agent.Options{Provider: &provider.Faux{StreamFunc: func(ctx context.Context, _ provider.Request) (provider.Stream, error) {
		return &partialBlockingStream{ctx: ctx, started: started}, nil
	}}, Model: model.Model{ID: "m"}})
	var eventMu sync.Mutex
	var eventIDs []sharedrun.ID
	a.On(func(e agent.Event) {
		eventMu.Lock()
		eventIDs = append(eventIDs, e.RunID)
		eventMu.Unlock()
	})
	r, err := a.Start(context.Background(), agent.Prompt{}, agent.StartOptions{ID: "physical-1"})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err = r.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) || r.Status() != sharedrun.StatusRunning {
		t.Fatalf("wait error=%v status=%q", err, r.Status())
	}
	if !r.Abort() || r.Abort() {
		t.Fatal("abort was not idempotent")
	}
	if err = r.Wait(context.Background()); !errors.Is(err, sharedrun.ErrAborted) || r.Status() != sharedrun.StatusAborted {
		t.Fatalf("wait error=%v status=%q", err, r.Status())
	}
	messages := a.Messages()
	if len(messages) != 1 || messages[0].Text() != "partial" || messages[0].StopReason != message.StopAborted {
		t.Fatalf("messages=%#v", messages)
	}
	eventMu.Lock()
	ids := append([]sharedrun.ID(nil), eventIDs...)
	eventMu.Unlock()
	for _, id := range ids {
		if id != "physical-1" {
			t.Fatalf("event run id=%q", id)
		}
	}
}

type stagedProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *stagedProvider) Stream(ctx context.Context, _ provider.Request) (provider.Stream, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventDone, StopReason: message.StopStop}}}, nil
	}
	return blockingStream{ctx: ctx}, nil
}

func TestStaleRunCannotAbortNextRun(t *testing.T) {
	a := agent.New(agent.Options{Provider: &stagedProvider{}, Model: model.Model{ID: "m"}})
	first := startAgent(t, a, agent.Prompt{})
	if err := first.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := startAgent(t, a, agent.Prompt{})
	if first.Abort() || second.Status() != sharedrun.StatusRunning {
		t.Fatalf("stale abort affected second run: %q", second.Status())
	}
	second.Abort()
	_ = second.Wait(context.Background())
}

func TestToolCanTerminateLoop(t *testing.T) {
	p := &scripted{}
	tools := tool.NewRegistry()
	if err := tools.Register(tool.Tool[echoParams, struct{}]{Name: "echo", Execute: func(_ context.Context, _ tool.ToolCall, _ echoParams, _ tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		return tool.ToolResult[struct{}]{Content: []message.Content{message.Text("stop")}, Terminate: true}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.Options{Provider: p, Model: model.Model{Provider: "p", ID: "m"}, Tools: tools})
	r := startAgent(t, a, agent.Prompt{})
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	turns := p.turn
	p.mu.Unlock()
	if turns != 1 {
		t.Fatalf("provider turns=%d", turns)
	}
}

type countingProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *countingProvider) Stream(context.Context, provider.Request) (provider.Stream, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventDone, StopReason: message.StopStop}}}, nil
}
func TestOneAtATimeQueue(t *testing.T) {
	p := &countingProvider{}
	a := agent.New(agent.Options{Provider: p, Model: model.Model{ID: "m"}, QueueMode: agent.QueueOneAtATime})
	r := startAgent(t, a, agent.Prompt{})
	_ = r.Steer(message.User("one"))
	_ = r.Steer(message.User("two"))
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	calls := p.calls
	p.mu.Unlock()
	if calls != 2 {
		t.Fatalf("provider calls=%d", calls)
	}
}

func TestTurnEndFollowsToolResultsAndHooksRunOnToolTurns(t *testing.T) {
	tools := tool.NewRegistry()
	if err := tools.Register(tool.Tool[echoParams, struct{}]{Name: "echo", Execute: func(_ context.Context, _ tool.ToolCall, p echoParams, _ tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		return tool.ToolResult[struct{}]{Content: []message.Content{message.Text(p.Text)}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	prepared, stopped := 0, 0
	a := agent.New(agent.Options{
		Provider: &scripted{}, Model: model.Model{Provider: "p", ID: "m"}, Tools: tools,
		PrepareNextTurn:     func(context.Context, []message.Message) ([]message.Message, error) { prepared++; return nil, nil },
		ShouldStopAfterTurn: func(context.Context, []message.Message) (bool, error) { stopped++; return false, nil },
	})
	var events []agent.EventType
	a.On(func(e agent.Event) { events = append(events, e.Type) })
	r := startAgent(t, a, agent.Prompt{Steps: prompt.Sequence{prompt.UserText("go")}})
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prepared != 2 || stopped != 2 {
		t.Fatalf("prepare=%d stop=%d", prepared, stopped)
	}
	toolMessage, firstTurnEnd := -1, -1
	for i, typ := range events {
		if typ == agent.EventMessageEnd && i > 0 && events[i-1] == agent.EventToolEnd {
			toolMessage = i
		}
		if typ == agent.EventTurnEnd {
			firstTurnEnd = i
			break
		}
	}
	if toolMessage < 0 || firstTurnEnd < toolMessage {
		t.Fatalf("events=%v", events)
	}
}

func TestLengthStopDoesNotExecuteTool(t *testing.T) {
	executed := false
	tools := tool.NewRegistry()
	if err := tools.Register(tool.Tool[echoParams, struct{}]{Name: "echo", Execute: func(context.Context, tool.ToolCall, echoParams, tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		executed = true
		return tool.ToolResult[struct{}]{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	p := &provider.Faux{StreamFunc: func(context.Context, provider.Request) (provider.Stream, error) {
		return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventToolCallStart, ToolCallID: "c", ToolName: "echo", ArgumentsDelta: `{"text":"partial"}`}, {Type: message.EventDone, StopReason: message.StopLength}}}, nil
	}}
	a := agent.New(agent.Options{Provider: p, Model: model.Model{ID: "m"}, Tools: tools, ShouldStopAfterTurn: func(_ context.Context, ms []message.Message) (bool, error) {
		return len(ms) > 0 && ms[len(ms)-1].Role == message.RoleTool, nil
	}})
	r := startAgent(t, a, agent.Prompt{})
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if executed || !a.Messages()[1].IsError {
		t.Fatalf("executed=%v messages=%#v", executed, a.Messages())
	}
}

type mixedTerminateProvider struct{ turns int }

func (p *mixedTerminateProvider) Stream(context.Context, provider.Request) (provider.Stream, error) {
	p.turns++
	if p.turns == 1 {
		return &provider.SliceStream{Events: []message.StreamEvent{
			{Type: message.EventToolCallStart, Index: 0, ToolCallID: "a", ToolName: "echo", ArgumentsDelta: `{"text":"stop"}`},
			{Type: message.EventToolCallStart, Index: 1, ToolCallID: "b", ToolName: "echo", ArgumentsDelta: `{"text":"continue"}`},
			{Type: message.EventDone, StopReason: message.StopToolUse},
		}}, nil
	}
	return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventDone, StopReason: message.StopStop}}}, nil
}

func TestEveryTerminateResultStopsBatchLoop(t *testing.T) {
	p := &mixedTerminateProvider{}
	tools := tool.NewRegistry()
	if err := tools.Register(tool.Tool[echoParams, struct{}]{Name: "echo", Execute: func(_ context.Context, _ tool.ToolCall, params echoParams, _ tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		return tool.ToolResult[struct{}]{Content: []message.Content{message.Text(params.Text)}, Terminate: params.Text == "stop"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.Options{Provider: p, Model: model.Model{ID: "m"}, Tools: tools})
	r := startAgent(t, a, agent.Prompt{})
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.turns != 2 {
		t.Fatalf("provider turns=%d", p.turns)
	}
}

func TestParallelAfterToolRunsWithoutBatchCoordinator(t *testing.T) {
	p := &scripted{}
	tools := tool.NewRegistry()
	if err := tools.Register(tool.Tool[echoParams, struct{}]{Name: "echo", Execute: func(_ context.Context, _ tool.ToolCall, params echoParams, _ tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		return tool.ToolResult[struct{}]{Content: []message.Content{message.Text(params.Text)}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	afterCalls := 0
	toolEnds := 0
	a := agent.New(agent.Options{
		Provider: p,
		Model:    model.Model{ID: "m"},
		Tools:    tools,
		AfterTool: func(_ context.Context, _ tool.ToolCall, result tool.Result) (tool.Result, error) {
			afterCalls++
			result.Content = []message.Content{message.Text("after")}
			return result, nil
		},
	})
	a.On(func(event agent.Event) {
		if event.Type == agent.EventToolEnd {
			toolEnds++
		}
	})
	r := startAgent(t, a, agent.Prompt{})
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if afterCalls != 1 || toolEnds != 1 || a.Messages()[1].Text() != "after" {
		t.Fatalf("after calls=%d tool ends=%d messages=%#v", afterCalls, toolEnds, a.Messages())
	}
}

func TestParallelAfterToolErrorBecomesToolError(t *testing.T) {
	p := &scripted{}
	tools := tool.NewRegistry()
	if err := tools.Register(tool.Tool[echoParams, struct{}]{Name: "echo", Execute: func(_ context.Context, _ tool.ToolCall, params echoParams, _ tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		return tool.ToolResult[struct{}]{Content: []message.Content{message.Text(params.Text)}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.Options{
		Provider: p,
		Model:    model.Model{ID: "m"},
		Tools:    tools,
		AfterTool: func(context.Context, tool.ToolCall, tool.Result) (tool.Result, error) {
			return tool.Result{}, errors.New("after failed")
		},
	})
	r := startAgent(t, a, agent.Prompt{})
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	messages := a.Messages()
	if p.turn != 2 || len(messages) != 3 || !messages[1].IsError || messages[1].Text() != "after failed" {
		t.Fatalf("turns=%d messages=%#v", p.turn, messages)
	}
}

func TestParallelAfterToolFollowsCompletionOrder(t *testing.T) {
	type emptyParams struct{}
	type twoCallsProvider struct{ turns int }
	providerImpl := &twoCallsProvider{}
	streamProvider := &provider.Faux{StreamFunc: func(context.Context, provider.Request) (provider.Stream, error) {
		providerImpl.turns++
		if providerImpl.turns == 1 {
			return &provider.SliceStream{Events: []message.StreamEvent{
				{Type: message.EventToolCallStart, Index: 0, ToolCallID: "slow-call", ToolName: "slow", ArgumentsDelta: `{}`},
				{Type: message.EventToolCallStart, Index: 1, ToolCallID: "fast-call", ToolName: "fast", ArgumentsDelta: `{}`},
				{Type: message.EventDone, StopReason: message.StopToolUse},
			}}, nil
		}
		return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventDone, StopReason: message.StopStop}}}, nil
	}}
	tools := tool.NewRegistry()
	fastAfterDone := make(chan struct{})
	if err := tools.Register(tool.Tool[emptyParams, struct{}]{Name: "slow", Execute: func(context.Context, tool.ToolCall, emptyParams, tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		<-fastAfterDone
		return tool.ToolResult[struct{}]{Content: []message.Content{message.Text("slow")}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := tools.Register(tool.Tool[emptyParams, struct{}]{Name: "fast", Execute: func(context.Context, tool.ToolCall, emptyParams, tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		return tool.ToolResult[struct{}]{Content: []message.Content{message.Text("fast")}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var afterOrder []string
	a := agent.New(agent.Options{
		Provider: streamProvider,
		Model:    model.Model{ID: "m"},
		Tools:    tools,
		AfterTool: func(_ context.Context, call tool.ToolCall, result tool.Result) (tool.Result, error) {
			mu.Lock()
			afterOrder = append(afterOrder, call.Name)
			mu.Unlock()
			if call.Name == "fast" {
				close(fastAfterDone)
			}
			return result, nil
		},
	})
	r := startAgent(t, a, agent.Prompt{})
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotOrder := strings.Join(afterOrder, ",")
	mu.Unlock()
	if gotOrder != "fast,slow" {
		t.Fatalf("after order=%s", gotOrder)
	}
	messages := a.Messages()
	if messages[1].ToolName != "slow" || messages[2].ToolName != "fast" {
		t.Fatalf("tool result source order=%#v", messages)
	}
}

type partialErrorStream struct{ sent bool }

func (s *partialErrorStream) Next() (message.StreamEvent, error) {
	if !s.sent {
		s.sent = true
		return message.StreamEvent{Type: message.EventTextDelta, Text: "partial"}, nil
	}
	return message.StreamEvent{}, errors.New("stream failed")
}

func (*partialErrorStream) Close() error { return nil }

func TestPartialProviderFailureFinalizesStartedAssistant(t *testing.T) {
	a := agent.New(agent.Options{
		Provider: &provider.Faux{StreamFunc: func(context.Context, provider.Request) (provider.Stream, error) {
			return &partialErrorStream{}, nil
		}},
		Model: model.Model{Provider: "test", ID: "m"},
	})
	var starts, ends, turns int
	a.On(func(event agent.Event) {
		switch event.Type {
		case agent.EventMessageStart:
			starts++
		case agent.EventMessageEnd:
			ends++
		case agent.EventTurnEnd:
			turns++
		}
	})
	r := startAgent(t, a, agent.Prompt{})
	if err := r.Wait(context.Background()); err == nil || err.Error() != "stream failed" {
		t.Fatalf("wait error=%v", err)
	}
	messages := a.Messages()
	if len(messages) != 1 || messages[0].Text() != "partial" || messages[0].StopReason != message.StopError || messages[0].ErrorMessage != "stream failed" {
		t.Fatalf("messages=%#v", messages)
	}
	if starts != 1 || ends != 1 || turns != 1 {
		t.Fatalf("starts=%d ends=%d turns=%d", starts, ends, turns)
	}
}

func TestProviderFailureProducesAssistantLifecycle(t *testing.T) {
	a := agent.New(agent.Options{
		Provider: &provider.Faux{StreamFunc: func(context.Context, provider.Request) (provider.Stream, error) {
			return nil, errors.New("provider failed")
		}},
		Model: model.Model{Provider: "test", ID: "m"},
	})
	var events []agent.EventType
	a.On(func(event agent.Event) { events = append(events, event.Type) })
	r := startAgent(t, a, agent.Prompt{})
	if err := r.Wait(context.Background()); err == nil || err.Error() != "provider failed" {
		t.Fatalf("wait error=%v", err)
	}
	messages := a.Messages()
	if len(messages) != 1 || messages[0].StopReason != message.StopError || messages[0].ErrorMessage != "provider failed" {
		t.Fatalf("messages=%#v", messages)
	}
	want := []agent.EventType{
		agent.EventAgentStart,
		agent.EventTurnStart,
		agent.EventMessageStart,
		agent.EventMessageEnd,
		agent.EventTurnEnd,
		agent.EventError,
		agent.EventAgentEnd,
	}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
}

func TestBlockedBatchCanTerminate(t *testing.T) {
	p := &scripted{}
	tools := tool.NewRegistry()
	if err := tools.Register(tool.Tool[echoParams, struct{}]{Name: "echo", Execute: func(context.Context, tool.ToolCall, echoParams, tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		t.Fatal("blocked tool executed")
		return tool.ToolResult[struct{}]{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	tools.AddBeforeHook(func(_ context.Context, call tool.ToolCall) (tool.ToolCall, error) {
		return call, tool.Block("blocked by policy", true)
	})
	a := agent.New(agent.Options{Provider: p, Model: model.Model{ID: "m"}, Tools: tools})
	r := startAgent(t, a, agent.Prompt{})
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.turn != 1 || !a.Messages()[1].IsError {
		t.Fatalf("turns=%d messages=%#v", p.turn, a.Messages())
	}
}

type scriptedStepParams struct {
	Label     string `json:"label"`
	Fail      bool   `json:"fail,omitempty"`
	Terminate bool   `json:"terminate,omitempty"`
}

func scriptedStepTools(t *testing.T, executed *[]string) *tool.Registry {
	t.Helper()
	registry := tool.NewRegistry()
	if err := registry.Register(tool.Tool[scriptedStepParams, struct{}]{
		Name: "script_step",
		Execute: func(_ context.Context, _ tool.ToolCall, params scriptedStepParams, _ tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
			*executed = append(*executed, params.Label)
			if params.Fail {
				return tool.ToolResult[struct{}]{}, errors.New("script step failed")
			}
			return tool.ToolResult[struct{}]{Content: []message.Content{message.Text(params.Label)}, Terminate: params.Terminate}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	return registry
}

func scriptedCall(key, label string, fail bool, policy prompt.OnErrorPolicy) prompt.ToolCall {
	arguments := fmt.Sprintf(`{"label":%q,"fail":%t}`, label, fail)
	return prompt.ToolCall{Key: key, Name: "script_step", Arguments: json.RawMessage(arguments), OnError: policy}
}

func TestPromptStepsExecuteBeforeFirstModelTurn(t *testing.T) {
	var executed []string
	registry := scriptedStepTools(t, &executed)
	providerCalls := 0
	var request provider.Request
	p := &provider.Faux{StreamFunc: func(_ context.Context, r provider.Request) (provider.Stream, error) {
		providerCalls++
		request = r
		if strings.Join(executed, ",") != "one,two" {
			t.Fatalf("provider called before script completed: %v", executed)
		}
		return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventTextDelta, Text: "done"}, {Type: message.EventDone, StopReason: message.StopStop}}}, nil
	}}
	a := agent.New(agent.Options{Provider: p, Model: model.Model{Provider: "test", ID: "m"}, Tools: registry})
	var eventKeys []string
	a.On(func(event agent.Event) {
		if event.Type == agent.EventToolStart && event.Call != nil {
			eventKeys = append(eventKeys, event.Call.Key)
		}
	})
	steps := prompt.Sequence{
		prompt.UserText("inspect"),
		prompt.AssistantText("I will inspect first."),
		prompt.Tools(
			scriptedCall("first", "one", false, ""),
			scriptedCall("second", "two", false, ""),
		),
		prompt.UserText("summarize"),
	}
	run := startAgent(t, a, agent.Prompt{Steps: steps})
	if err := run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls=%d", providerCalls)
	}
	if strings.Join(eventKeys, ",") != "first,second" {
		t.Fatalf("tool event keys=%v", eventKeys)
	}
	wantRoles := []message.Role{message.RoleUser, message.RoleAssistant, message.RoleAssistant, message.RoleTool, message.RoleAssistant, message.RoleTool, message.RoleUser, message.RoleAssistant}
	messages := a.Messages()
	if len(messages) != len(wantRoles) {
		t.Fatalf("messages=%#v", messages)
	}
	for i, role := range wantRoles {
		if messages[i].Role != role {
			t.Fatalf("message %d role=%q want %q", i, messages[i].Role, role)
		}
	}
	if messages[1].Origin != message.OriginScript || messages[2].Origin != message.OriginScript || messages[3].Origin != message.OriginTool || messages[7].Origin != message.OriginModel {
		t.Fatalf("origins=%#v", messages)
	}
	if messages[2].Content[0].Key != "first" || messages[3].ToolCallKey != "first" || messages[4].Content[0].Key != "second" || messages[5].ToolCallKey != "second" {
		t.Fatalf("script keys were not preserved: %#v", messages)
	}
	if messages[2].Content[0].ID == messages[4].Content[0].ID || messages[2].Content[0].ID == "" {
		t.Fatalf("tool call IDs are not unique: %#v", messages)
	}
	if len(request.Messages) != 7 || request.Messages[6].Text() != "summarize" {
		t.Fatalf("provider request=%#v", request.Messages)
	}
}

func TestScriptedToolErrorResultUsesDefaultOnErrorPolicy(t *testing.T) {
	registry := tool.NewRegistry()
	var executed []string
	type params struct {
		Label string `json:"label"`
		Fail  bool   `json:"fail,omitempty"`
	}
	if err := registry.Register(tool.Tool[params, struct{}]{Name: "soft", Execute: func(_ context.Context, _ tool.ToolCall, p params, _ tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		executed = append(executed, p.Label)
		return tool.ToolResult[struct{}]{Content: []message.Content{message.Text("soft failure")}, IsError: p.Fail}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	a := agent.New(agent.Options{Provider: &provider.Faux{StreamFunc: func(context.Context, provider.Request) (provider.Stream, error) {
		providerCalls++
		return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventDone, StopReason: message.StopStop}}}, nil
	}}, Model: model.Model{ID: "m"}, Tools: registry})
	steps := prompt.Sequence{
		prompt.UserText("start"),
		prompt.Tools(
			prompt.ToolCall{Name: "soft", Arguments: json.RawMessage(`{"label":"bad","fail":true}`)},
			prompt.ToolCall{Name: "soft", Arguments: json.RawMessage(`{"label":"skipped"}`)},
		),
		prompt.UserText("also skipped"),
	}
	run := startAgent(t, a, agent.Prompt{Steps: steps})
	if err := run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(executed, ",") != "bad" || providerCalls != 1 {
		t.Fatalf("executed=%v provider calls=%d", executed, providerCalls)
	}
	messages := a.Messages()
	if len(messages) != 4 || !messages[2].IsError || messages[3].Origin != message.OriginModel {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestScriptedToolOnErrorPolicies(t *testing.T) {
	tests := []struct {
		name          string
		policy        prompt.OnErrorPolicy
		wantExecuted  string
		wantProviders int
		wantAbort     bool
		wantMessages  int
	}{
		{"continue", prompt.OnErrorContinue, "bad,good", 1, false, 7},
		{"enter agent loop", prompt.OnErrorEnterAgentLoop, "bad", 1, false, 4},
		{"abort", prompt.OnErrorAbort, "bad", 0, true, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var executed []string
			registry := scriptedStepTools(t, &executed)
			providerCalls := 0
			p := &provider.Faux{StreamFunc: func(context.Context, provider.Request) (provider.Stream, error) {
				providerCalls++
				return &provider.SliceStream{Events: []message.StreamEvent{{Type: message.EventDone, StopReason: message.StopStop}}}, nil
			}}
			a := agent.New(agent.Options{Provider: p, Model: model.Model{ID: "m"}, Tools: registry})
			steps := prompt.Sequence{
				prompt.UserText("start"),
				prompt.Tools(scriptedCall("bad", "bad", true, tc.policy), scriptedCall("good", "good", false, "")),
				prompt.UserText("finish"),
			}
			run := startAgent(t, a, agent.Prompt{Steps: steps})
			err := run.Wait(context.Background())
			var scriptErr *agent.ScriptToolError
			if tc.wantAbort != errors.As(err, &scriptErr) {
				t.Fatalf("wait error=%v", err)
			}
			if tc.wantAbort && (scriptErr.Key != "bad" || scriptErr.Name != "script_step") {
				t.Fatalf("script error=%#v", scriptErr)
			}
			if got := strings.Join(executed, ","); got != tc.wantExecuted {
				t.Fatalf("executed=%q want %q", got, tc.wantExecuted)
			}
			if providerCalls != tc.wantProviders || len(a.Messages()) != tc.wantMessages {
				t.Fatalf("provider calls=%d messages=%#v", providerCalls, a.Messages())
			}
		})
	}
}

func TestPromptStepsPreflightAllToolsBeforeExecuting(t *testing.T) {
	var executed []string
	registry := scriptedStepTools(t, &executed)
	providerCalls := 0
	a := agent.New(agent.Options{Provider: &provider.Faux{StreamFunc: func(context.Context, provider.Request) (provider.Stream, error) {
		providerCalls++
		return &provider.SliceStream{}, nil
	}}, Model: model.Model{ID: "m"}, Tools: registry})
	steps := prompt.Sequence{prompt.Tools(
		scriptedCall("valid", "valid", false, ""),
		prompt.ToolCall{Name: "missing", Arguments: json.RawMessage(`{}`)},
	)}
	_, err := a.Start(context.Background(), agent.Prompt{Steps: steps}, agent.StartOptions{})
	if !errors.Is(err, tool.ErrNotFound) || len(executed) != 0 || providerCalls != 0 || a.ActiveRun() != nil {
		t.Fatalf("error=%v executed=%v provider calls=%d active=%v", err, executed, providerCalls, a.ActiveRun())
	}
	steps = prompt.Sequence{prompt.Tools(
		scriptedCall("valid-again", "valid", false, ""),
		prompt.ToolCall{Name: "script_step", Arguments: json.RawMessage(`{"value":`)},
	)}
	_, err = a.Start(context.Background(), agent.Prompt{Steps: steps}, agent.StartOptions{})
	if err == nil || len(executed) != 0 || providerCalls != 0 {
		t.Fatalf("argument error=%v executed=%v provider calls=%d", err, executed, providerCalls)
	}

	a = agent.New(agent.Options{Provider: &provider.Faux{}, Model: model.Model{ID: "m"}, Tools: registry, ActiveTools: []string{}})
	_, err = a.Start(context.Background(), agent.Prompt{Steps: prompt.Sequence{prompt.Tools(scriptedCall("inactive", "inactive", false, ""))}}, agent.StartOptions{})
	if err == nil || !strings.Contains(err.Error(), "not active") {
		t.Fatalf("inactive tool error=%v", err)
	}
}

func TestScriptedToolTerminateAndAbortSkipModel(t *testing.T) {
	t.Run("terminate", func(t *testing.T) {
		var executed []string
		registry := scriptedStepTools(t, &executed)
		providerCalls := 0
		p := &provider.Faux{StreamFunc: func(context.Context, provider.Request) (provider.Stream, error) {
			providerCalls++
			return &provider.SliceStream{}, nil
		}}
		a := agent.New(agent.Options{Provider: p, Model: model.Model{ID: "m"}, Tools: registry})
		call := prompt.ToolCall{Name: "script_step", Arguments: json.RawMessage(`{"label":"stop","terminate":true}`)}
		run := startAgent(t, a, agent.Prompt{Steps: prompt.Sequence{prompt.Tools(call)}})
		if err := run.Wait(context.Background()); err != nil || providerCalls != 0 {
			t.Fatalf("wait error=%v provider calls=%d", err, providerCalls)
		}
	})

	t.Run("abort context", func(t *testing.T) {
		started := make(chan struct{})
		registry := tool.NewRegistry()
		if err := registry.Register(tool.Tool[struct{}, struct{}]{Name: "block", Execute: func(ctx context.Context, _ tool.ToolCall, _ struct{}, _ tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
			close(started)
			<-ctx.Done()
			return tool.ToolResult[struct{}]{}, ctx.Err()
		}}); err != nil {
			t.Fatal(err)
		}
		providerCalls := 0
		a := agent.New(agent.Options{Provider: &provider.Faux{StreamFunc: func(context.Context, provider.Request) (provider.Stream, error) {
			providerCalls++
			return &provider.SliceStream{}, nil
		}}, Model: model.Model{ID: "m"}, Tools: registry})
		run := startAgent(t, a, agent.Prompt{Steps: prompt.Sequence{prompt.Tools(prompt.ToolCall{Name: "block"})}})
		<-started
		run.Abort()
		if err := run.Wait(context.Background()); !errors.Is(err, sharedrun.ErrAborted) || providerCalls != 0 {
			t.Fatalf("wait error=%v provider calls=%d", err, providerCalls)
		}
		messages := a.Messages()
		if len(messages) != 2 || messages[1].Role != message.RoleTool || !messages[1].IsError {
			t.Fatalf("cancelled tool history=%#v", messages)
		}
	})
}

type eofStream struct{}

func (eofStream) Next() (message.StreamEvent, error) { return message.StreamEvent{}, io.EOF }
func (eofStream) Close() error                       { return nil }

type canceledEOFStream struct {
	ctx     context.Context
	started chan struct{}
}

func (s canceledEOFStream) Next() (message.StreamEvent, error) {
	close(s.started)
	<-s.ctx.Done()
	return message.StreamEvent{}, io.EOF
}
func (s canceledEOFStream) Close() error { return nil }

func TestAbortWinsWhenProviderClosesWithEOF(t *testing.T) {
	started := make(chan struct{})
	a := agent.New(agent.Options{Provider: &provider.Faux{StreamFunc: func(ctx context.Context, _ provider.Request) (provider.Stream, error) {
		return canceledEOFStream{ctx: ctx, started: started}, nil
	}}, Model: model.Model{ID: "m"}})
	run := startAgent(t, a, agent.Prompt{})
	<-started
	if !run.Abort() {
		t.Fatal("abort was not accepted")
	}
	if err := run.Wait(context.Background()); !errors.Is(err, sharedrun.ErrAborted) || run.Status() != sharedrun.StatusAborted {
		t.Fatalf("wait error=%v status=%q", err, run.Status())
	}
	if messages := a.Messages(); len(messages) != 1 || messages[0].StopReason != message.StopAborted || messages[0].ErrorMessage == "" {
		t.Fatalf("aborted assistant=%#v", messages)
	}
}

type twoToolProvider struct{}

func (twoToolProvider) Stream(context.Context, provider.Request) (provider.Stream, error) {
	return &provider.SliceStream{Events: []message.StreamEvent{
		{Type: message.EventToolCallStart, Index: 0, ToolCallID: "first-call", ToolName: "first", ArgumentsDelta: `{}`},
		{Type: message.EventToolCallStart, Index: 1, ToolCallID: "second-call", ToolName: "second", ArgumentsDelta: `{}`},
		{Type: message.EventDone, StopReason: message.StopToolUse},
	}}, nil
}

func TestAbortSequentialToolsCompletesEveryToolCall(t *testing.T) {
	started := make(chan struct{})
	tools := tool.NewRegistry()
	if err := tools.Register(tool.Tool[struct{}, struct{}]{Name: "first", ExecutionMode: tool.Sequential, Execute: func(ctx context.Context, _ tool.ToolCall, _ struct{}, _ tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		close(started)
		<-ctx.Done()
		return tool.ToolResult[struct{}]{}, ctx.Err()
	}}); err != nil {
		t.Fatal(err)
	}
	secondExecuted := false
	if err := tools.Register(tool.Tool[struct{}, struct{}]{Name: "second", Execute: func(context.Context, tool.ToolCall, struct{}, tool.Update[struct{}]) (tool.ToolResult[struct{}], error) {
		secondExecuted = true
		return tool.ToolResult[struct{}]{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	a := agent.New(agent.Options{Provider: twoToolProvider{}, Model: model.Model{ID: "m"}, Tools: tools, ExecutionMode: tool.Sequential})
	run := startAgent(t, a, agent.Prompt{Steps: prompt.Sequence{prompt.UserText("go")}})
	<-started
	run.Abort()
	if err := run.Wait(context.Background()); !errors.Is(err, sharedrun.ErrAborted) {
		t.Fatalf("wait error=%v", err)
	}
	if secondExecuted {
		t.Fatal("second sequential tool executed after abort")
	}
	messages := a.Messages()
	if len(messages) != 4 || messages[2].ToolCallID != "first-call" || !messages[2].IsError || messages[3].ToolCallID != "second-call" || !messages[3].IsError {
		t.Fatalf("tool call history is incomplete: %#v", messages)
	}
}

var _ = json.RawMessage{}
