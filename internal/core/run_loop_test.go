package core_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

type loopProvider struct {
	mu       sync.Mutex
	turn     int
	requests []harness.Request
	stream   func(int, harness.Request) (harness.Stream, error)
}

type providerFunc func(context.Context, harness.Request) (harness.Stream, error)

func (f providerFunc) Stream(ctx context.Context, request harness.Request) (harness.Stream, error) {
	return f(ctx, request)
}

func (p *loopProvider) Stream(_ context.Context, request harness.Request) (harness.Stream, error) {
	p.mu.Lock()
	p.turn++
	turn := p.turn
	p.requests = append(p.requests, request)
	stream := p.stream
	p.mu.Unlock()
	return stream(turn, request)
}

func newLoopSession(t *testing.T, provider harness.Provider, tools *harness.ToolRegistry, activeTools []string) *harness.Session[harness.NoState] {
	t.Helper()
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	h, err := harness.NewStateless(harness.Options{Models: models, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("test", provider); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, ActiveTools: activeTools}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStartWithLoopDrivesOneLogicalRun(t *testing.T) {
	type echoParams struct {
		Text string `json:"text"`
	}
	tools := harness.NewToolRegistry()
	if err := tools.Register(harness.Tool[echoParams, struct{}]{
		Name: "echo",
		Execute: func(_ context.Context, _ harness.ToolCall, p echoParams, _ harness.Update[struct{}]) (harness.ToolResult[struct{}], error) {
			return harness.ToolResult[struct{}]{Content: []harness.Content{harness.Text(p.Text)}}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	provider := &loopProvider{stream: func(turn int, _ harness.Request) (harness.Stream, error) {
		if turn == 1 {
			return &harness.SliceStream{Events: []harness.StreamEvent{
				{Type: harness.EventToolCallStart, ToolCallID: "call-1", ToolName: "echo", ArgumentsDelta: `{"text":"ok"}`},
				{Type: harness.EventDone, StopReason: harness.StopToolUse},
			}}, nil
		}
		return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventTextDelta, Text: "done"}, {Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
	}}
	s := newLoopSession(t, provider, tools, []string{"echo"})
	var seenID harness.ID
	run, err := s.StartWithLoop(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("go")}}, harness.StartOptions{}, func(ctx context.Context, r *harness.Run[harness.NoState]) (harness.EndReason, error) {
		seenID = r.ID()
		reason, err := r.Next(ctx)
		if err != nil || reason != harness.EndReasonNone {
			return "", errors.New("first turn did not request continuation")
		}
		return r.Next(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seenID != run.ID() {
		t.Fatalf("loop run id=%q returned run id=%q", seenID, run.ID())
	}
	outcome := run.Outcome()
	if outcome.Status != harness.StatusCompleted || outcome.EndReason != harness.EndReasonAssistantStop || outcome.Stats.TurnsCompleted != 2 || outcome.Stats.Attempts != 1 || outcome.Stats.ToolCalls != 1 {
		t.Fatalf("outcome=%#v", outcome)
	}
	info, err := s.RunInfo(run.ID())
	if err != nil {
		t.Fatal(err)
	}
	if info.EndReason != outcome.EndReason || info.Stats != outcome.Stats {
		t.Fatalf("persisted info=%#v outcome=%#v", info, outcome)
	}
	starts, ends := 0, 0
	for _, entry := range s.Entries() {
		if entry.RunID != run.ID() {
			continue
		}
		switch entry.Type {
		case "run_start":
			starts++
		case "run_end":
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("run entries: starts=%d ends=%d", starts, ends)
	}
}

func TestRunLoopCanContinueAfterCandidateEnd(t *testing.T) {
	provider := &loopProvider{stream: func(_ int, _ harness.Request) (harness.Stream, error) {
		return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
	}}
	s := newLoopSession(t, provider, nil, nil)
	run, err := s.StartWithLoop(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("first")}}, harness.StartOptions{}, func(ctx context.Context, r *harness.Run[harness.NoState]) (harness.EndReason, error) {
		reason, err := r.Next(ctx)
		if err != nil || reason != harness.EndReasonAssistantStop {
			return "", errors.New("first candidate end was not assistant_stop")
		}
		if _, err = r.Next(ctx); !errors.Is(err, harness.ErrNoPendingInput) {
			return "", errors.New("next without input did not fail")
		}
		if err = r.FollowUp(ctx, harness.User("continue")); err != nil {
			return "", err
		}
		if _, err = r.Next(ctx); err != nil {
			return "", err
		}
		return "continued_once", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := run.Outcome(); got.EndReason != "continued_once" || got.Stats.TurnsCompleted != 2 || got.Stats.Attempts != 1 {
		t.Fatalf("outcome=%#v", got)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.requests) != 2 || provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Text() != "continue" {
		t.Fatalf("requests=%#v", provider.requests)
	}
}

func TestRunActiveToolsAreScopedToRun(t *testing.T) {
	type params struct{}
	tools := harness.NewToolRegistry()
	if err := tools.Register(harness.Tool[params, struct{}]{Name: "dummy", Execute: func(context.Context, harness.ToolCall, params, harness.Update[struct{}]) (harness.ToolResult[struct{}], error) {
		return harness.ToolResult[struct{}]{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	provider := &loopProvider{stream: func(_ int, _ harness.Request) (harness.Stream, error) {
		return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
	}}
	s := newLoopSession(t, provider, tools, []string{"dummy"})
	run, err := s.StartWithLoop(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("custom")}}, harness.StartOptions{}, func(ctx context.Context, r *harness.Run[harness.NoState]) (harness.EndReason, error) {
		if err := r.SetActiveTools(nil); err != nil {
			return "", err
		}
		return r.Next(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("default")}})
	if err = second.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.requests) != 2 || len(provider.requests[0].Tools) != 0 || len(provider.requests[1].Tools) != 1 {
		t.Fatalf("requests=%#v", provider.requests)
	}
}

func TestRunLoopImplementsMaxTurnsWithoutSDKPolicy(t *testing.T) {
	type params struct{}
	tools := harness.NewToolRegistry()
	if err := tools.Register(harness.Tool[params, struct{}]{Name: "again", Execute: func(context.Context, harness.ToolCall, params, harness.Update[struct{}]) (harness.ToolResult[struct{}], error) {
		return harness.ToolResult[struct{}]{Content: []harness.Content{harness.Text("again")}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	provider := &loopProvider{stream: func(turn int, request harness.Request) (harness.Stream, error) {
		if len(request.Tools) > 0 {
			return &harness.SliceStream{Events: []harness.StreamEvent{
				{Type: harness.EventToolCallStart, ToolCallID: "call-" + string(rune('0'+turn)), ToolName: "again", ArgumentsDelta: `{}`},
				{Type: harness.EventDone, StopReason: harness.StopToolUse},
			}}, nil
		}
		return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventTextDelta, Text: "wrapped"}, {Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
	}}
	s := newLoopSession(t, provider, tools, []string{"again"})
	wrapUp := harness.User("wrap up now")
	run, err := s.StartWithLoop(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("start")}}, harness.StartOptions{}, func(ctx context.Context, r *harness.Run[harness.NoState]) (harness.EndReason, error) {
		wrappingUp := false
		for {
			reason, err := r.Next(ctx)
			if err != nil {
				return "", err
			}
			if reason != "" {
				if wrappingUp {
					return "max_turns", nil
				}
				return reason, nil
			}
			if r.Stats().TurnsCompleted < 2 {
				continue
			}
			if wrappingUp {
				return "max_turns", nil
			}
			if err = r.SetActiveTools(nil); err != nil {
				return "", err
			}
			if err = r.FollowUp(ctx, wrapUp); err != nil {
				return "", err
			}
			wrappingUp = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := run.Outcome(); got.EndReason != "max_turns" || got.Stats.TurnsCompleted != 3 || got.Stats.ToolCalls != 2 {
		t.Fatalf("outcome=%#v", got)
	}
	wrapUps := 0
	for _, message := range s.Conversation().Messages {
		if message.Text() == "wrap up now" {
			wrapUps++
		}
	}
	if wrapUps != 1 {
		t.Fatalf("wrap-up messages=%d", wrapUps)
	}
}

func TestRunLoopCanContinueAfterProviderPanic(t *testing.T) {
	provider := &loopProvider{stream: func(turn int, _ harness.Request) (harness.Stream, error) {
		if turn == 1 {
			panic("provider panic")
		}
		return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
	}}
	s := newLoopSession(t, provider, nil, nil)
	run, err := s.StartWithLoop(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("panic")}}, harness.StartOptions{}, func(ctx context.Context, r *harness.Run[harness.NoState]) (harness.EndReason, error) {
		_, err := r.Next(ctx)
		var panicErr *harness.PanicError
		if !errors.As(err, &panicErr) || len(panicErr.Stack) == 0 {
			return "", errors.New("provider panic was not returned as PanicError")
		}
		return r.Next(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := run.Outcome(); got.EndReason != harness.EndReasonAssistantStop || got.Stats.Attempts != 2 || got.Stats.TurnsCompleted != 1 {
		t.Fatalf("outcome=%#v", got)
	}
}

func TestRunLoopCanContinueAfterToolPanic(t *testing.T) {
	type params struct{}
	tools := harness.NewToolRegistry()
	if err := tools.Register(harness.Tool[params, struct{}]{Name: "panic_tool", Execute: func(context.Context, harness.ToolCall, params, harness.Update[struct{}]) (harness.ToolResult[struct{}], error) {
		panic("tool panic")
	}}); err != nil {
		t.Fatal(err)
	}
	provider := &loopProvider{stream: func(turn int, _ harness.Request) (harness.Stream, error) {
		if turn == 1 {
			return &harness.SliceStream{Events: []harness.StreamEvent{
				{Type: harness.EventToolCallStart, ToolCallID: "panic-call", ToolName: "panic_tool", ArgumentsDelta: `{}`},
				{Type: harness.EventDone, StopReason: harness.StopToolUse},
			}}, nil
		}
		return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
	}}
	s := newLoopSession(t, provider, tools, []string{"panic_tool"})
	run, err := s.StartWithLoop(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("panic")}}, harness.StartOptions{}, func(ctx context.Context, r *harness.Run[harness.NoState]) (harness.EndReason, error) {
		_, err := r.Next(ctx)
		var panicErr *harness.PanicError
		if !errors.As(err, &panicErr) {
			return "", errors.New("tool panic was not returned as PanicError")
		}
		return r.Next(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	messages := s.Conversation().Messages
	if len(messages) != 4 || messages[2].Role != harness.RoleTool || !messages[2].IsError || messages[2].ToolCallID != "panic-call" {
		t.Fatalf("messages=%#v", messages)
	}
	if got := run.Outcome(); got.Stats.Attempts != 2 || got.Stats.TurnsCompleted != 2 || got.Stats.ToolCalls != 1 {
		t.Fatalf("outcome=%#v", got)
	}
}

func TestRunLoopRejectsConcurrentAndExternalNext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	provider := providerFunc(func(context.Context, harness.Request) (harness.Stream, error) {
		close(started)
		<-release
		return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
	})
	s := newLoopSession(t, provider, nil, nil)
	loopReady := make(chan struct{})
	run, err := s.StartWithLoop(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("concurrent")}}, harness.StartOptions{}, func(ctx context.Context, r *harness.Run[harness.NoState]) (harness.EndReason, error) {
		close(loopReady)
		type nextResult struct {
			reason harness.EndReason
			err    error
		}
		result := make(chan nextResult, 1)
		go func() {
			reason, err := r.Next(ctx)
			result <- nextResult{reason: reason, err: err}
		}()
		<-started
		if _, err := r.Next(ctx); !errors.Is(err, harness.ErrAgentBusy) {
			close(release)
			return "", errors.New("concurrent Next did not return ErrAgentBusy")
		}
		close(release)
		got := <-result
		return got.reason, got.err
	})
	if err != nil {
		t.Fatal(err)
	}
	<-loopReady
	if _, err = run.Next(context.Background()); !errors.Is(err, harness.ErrNextUnavailable) {
		t.Fatalf("external Next error=%v", err)
	}
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultRunLoopPreservesOneAtATimeQueue(t *testing.T) {
	provider := &gatedProvider{started: make(chan struct{}), release: make(chan struct{})}
	h := newHarness(t, provider)
	model, err := h.Models().Get("test", "m")
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &model, QueueMode: harness.QueueOneAtATime}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("first")}})
	<-provider.started
	if err = run.FollowUp(context.Background(), harness.User("second")); err != nil {
		t.Fatal(err)
	}
	if err = run.FollowUp(context.Background(), harness.User("third")); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.requests) != 3 {
		t.Fatalf("requests=%d", len(provider.requests))
	}
	second := provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Text()
	third := provider.requests[2].Messages[len(provider.requests[2].Messages)-1].Text()
	if second != "second" || third != "third" {
		t.Fatalf("queued messages: second=%q third=%q", second, third)
	}
}

func TestRunLoopPanicFailsRunOnce(t *testing.T) {
	provider := &loopProvider{stream: func(_ int, _ harness.Request) (harness.Stream, error) {
		return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
	}}
	s := newLoopSession(t, provider, nil, nil)
	run, err := s.StartWithLoop(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("panic")}}, harness.StartOptions{}, func(context.Context, *harness.Run[harness.NoState]) (harness.EndReason, error) {
		panic("loop panic")
	})
	if err != nil {
		t.Fatal(err)
	}
	err = run.Wait(context.Background())
	var panicErr *harness.PanicError
	if !errors.As(err, &panicErr) {
		t.Fatalf("wait error=%v", err)
	}
	if got := run.Outcome(); got.Status != harness.StatusFailed || got.EndReason != harness.EndReasonNone {
		t.Fatalf("outcome=%#v", got)
	}
	ends := 0
	for _, entry := range s.Entries() {
		if entry.RunID == run.ID() && entry.Type == "run_end" {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("run_end entries=%d", ends)
	}
}

func TestRunLoopNextDeadlineIsTerminal(t *testing.T) {
	provider := providerFunc(func(ctx context.Context, _ harness.Request) (harness.Stream, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	s := newLoopSession(t, provider, nil, nil)
	run, err := s.StartWithLoop(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("timeout")}}, harness.StartOptions{}, func(ctx context.Context, r *harness.Run[harness.NoState]) (harness.EndReason, error) {
		stepCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()
		_, err := r.Next(stepCtx)
		return "", err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Wait(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error=%v", err)
	}
	if got := run.Outcome(); got.Status != harness.StatusAborted || got.Cause != harness.CauseDeadline {
		t.Fatalf("outcome=%#v", got)
	}
}
