package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

type finalProvider struct{}

func (finalProvider) Stream(context.Context, harness.Request) (harness.Stream, error) {
	return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventTextDelta, Text: "answer"}, {Type: harness.EventDone, StopReason: harness.StopStop, Usage: harness.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}}}}, nil
}

type captureProvider struct {
	mu       sync.Mutex
	requests []harness.Request
}

func startSession(t *testing.T, s *harness.Session[harness.NoState], p harness.Prompt) *harness.Run[harness.NoState] {
	t.Helper()
	r, err := s.Start(context.Background(), p, harness.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (p *captureProvider) Stream(_ context.Context, request harness.Request) (harness.Stream, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
}

func TestThinkingAndExplicitActiveToolsReachProvider(t *testing.T) {
	p := &captureProvider{}
	tools := harness.NewToolRegistry()
	type params struct{}
	if err := tools.Register(harness.Tool[params, struct{}]{Name: "dummy", RawParameters: json.RawMessage(`{"type":"object","additionalProperties":false}`), Execute: func(context.Context, harness.ToolCall, params, harness.Update[struct{}]) (harness.ToolResult[struct{}], error) {
		return harness.ToolResult[struct{}]{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m", SupportsReasoning: true}
	_ = models.Register(selected)
	h, err := harness.NewStateless(harness.Options{Models: models, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("test", p); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, ActiveTools: []string{}}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SetThinkingLevel(context.Background(), "high"); err != nil {
		t.Fatal(err)
	}
	run := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("first")}})
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.SetActiveTools([]string{"dummy", "missing"}); err != nil {
		t.Fatal(err)
	}
	run = startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("second")}})
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != 2 || p.requests[0].ReasoningEffort != "high" || len(p.requests[0].Tools) != 0 || len(p.requests[1].Tools) != 1 || p.requests[1].Tools[0].Name != "dummy" {
		t.Fatalf("requests=%#v", p.requests)
	}
}

func TestSessionGenerationConfigIsClonedPerRequest(t *testing.T) {
	p := &captureProvider{}
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	_ = models.Register(selected)
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("test", p); err != nil {
		t.Fatal(err)
	}
	generation := harness.GenerationConfig{
		Temperature:   harness.Ptr(0.2),
		StopSequences: []string{"END"},
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, Generation: generation}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	run := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("first")}})
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	*p.requests[0].Generation.Temperature = 0.8
	p.requests[0].Generation.StopSequences[0] = "CHANGED"
	p.mu.Unlock()

	run = startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("second")}})
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != 2 {
		t.Fatalf("requests=%#v", p.requests)
	}
	second := p.requests[1].Generation
	if *second.Temperature != 0.2 || second.StopSequences[0] != "END" {
		t.Fatalf("second generation=%#v", second)
	}
	if *generation.Temperature != 0.2 || generation.StopSequences[0] != "END" {
		t.Fatalf("session input generation was mutated: %#v", generation)
	}
}

func TestSessionSystemPromptReplacesDefault(t *testing.T) {
	p := &captureProvider{}
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	_ = models.Register(selected)
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("test", p); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, SystemPrompt: "developer-defined prompt"}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("hello")}})
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != 1 || !strings.HasPrefix(p.requests[0].SystemPrompt, "developer-defined prompt\n") || strings.Contains(p.requests[0].SystemPrompt, "operating inside pi") {
		t.Fatalf("system prompt=%q", p.requests[0].SystemPrompt)
	}
}

type afterToolFailureExtension struct{}

func (afterToolFailureExtension) Register(registry *harness.ExtensionRegistry[harness.NoState]) error {
	registry.AddAfterToolCallHook(func(_ context.Context, _ harness.Context[harness.NoState], _ harness.ToolCall, _ harness.Result) (harness.Result, error) {
		return harness.Result{}, errors.New("extension after failed")
	})
	return nil
}

type oneToolProvider struct{ turns int }

func (p *oneToolProvider) Stream(context.Context, harness.Request) (harness.Stream, error) {
	p.turns++
	if p.turns == 1 {
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventToolCallStart, ToolCallID: "call", ToolName: "work", ArgumentsDelta: `{}`},
			{Type: harness.EventDone, StopReason: harness.StopToolUse},
		}}, nil
	}
	return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
}

func TestParallelExtensionAfterToolErrorBecomesToolError(t *testing.T) {
	p := &oneToolProvider{}
	tools := harness.NewToolRegistry()
	if err := tools.Register(harness.Tool[struct{}, struct{}]{Name: "work", Execute: func(context.Context, harness.ToolCall, struct{}, harness.Update[struct{}]) (harness.ToolResult[struct{}], error) {
		return harness.ToolResult[struct{}]{Content: []harness.Content{harness.Text("worked")}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	_ = models.Register(selected)
	h, err := harness.NewStateless(harness.Options{Models: models, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterExtension(afterToolFailureExtension{}); err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("test", p); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("go")}})
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	messages := s.Conversation().Messages
	if p.turns != 2 || len(messages) != 4 || !messages[2].IsError || messages[2].Text() != "extension after failed" {
		t.Fatalf("turns=%d messages=%#v", p.turns, messages)
	}
}

func TestFinalProviderFailurePersistsAssistantError(t *testing.T) {
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	_ = models.Register(selected)
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("test", &harness.Faux{StreamFunc: func(context.Context, harness.Request) (harness.Stream, error) {
		return nil, errors.New("final provider failure")
	}}); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("go")}})
	if err = run.Wait(context.Background()); err == nil || err.Error() != "final provider failure" {
		t.Fatalf("wait error=%v", err)
	}
	messages := s.Conversation().Messages
	if len(messages) != 2 || messages[1].StopReason != harness.StopError || messages[1].ErrorMessage != "final provider failure" {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestExtensionForkReplacesSessionAndStalesContext(t *testing.T) {
	h := newHarness(t, finalProvider{})
	selected, _ := h.Models().Get("test", "m")
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("question")}})
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries := s.Entries()
	var firstMessage harness.SessionEntryID
	for _, entry := range entries {
		if entry.Type == "message" && entry.Message != nil {
			firstMessage = entry.ID
			break
		}
	}
	oldContext := s.Context()
	if err = oldContext.Fork(context.Background(), string(firstMessage)); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(oldContext.Check(), harness.ErrStaleContext) {
		t.Fatalf("old context is not stale: %v", oldContext.Check())
	}
	if got := s.Conversation().Messages; len(got) != 1 || got[0].Text() != "question" {
		t.Fatalf("fork context=%#v", got)
	}
}
func newHarness(t *testing.T, p harness.Provider) *harness.Harness[harness.NoState] {
	t.Helper()
	models := harness.NewModelRegistry()
	m := harness.Model{Provider: "test", ID: "m", ContextWindow: 1000, MaxOutput: 100}
	if err := models.Register(m); err != nil {
		t.Fatal(err)
	}
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("test", p); err != nil {
		t.Fatal(err)
	}
	return h
}
func TestHarnessSessionAndTypedEvent(t *testing.T) {
	h := newHarness(t, finalProvider{})
	m, _ := h.Models().Get("test", "m")
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &m}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var messages int
	s.On(func(_ context.Context, _ harness.Context[harness.NoState], e harness.EntryAppendedEvent) {
		if e.Entry.Type == "message" {
			messages++
		}
	})
	run := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("question")}})
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := s.Conversation()
	if len(ctx.Messages) != 2 || ctx.Messages[1].Text() != "answer" {
		t.Fatalf("context=%#v", ctx.Messages)
	}
	if messages != 2 {
		t.Fatalf("message events=%d", messages)
	}
	stats := s.Stats()
	if stats.TotalTokens != 3 {
		t.Fatalf("stats=%#v", stats)
	}
	id, err := s.AppendCustom(context.Background(), "state", struct {
		N int `json:"n"`
	}{N: 4})
	if err != nil || id == "" {
		t.Fatal(err)
	}
	values, err := s.CustomEntries[struct {
		N int `json:"n"`
	}]("state")
	if err != nil || values[0].Data.N != 4 {
		t.Fatalf("values=%#v err=%v", values, err)
	}
}

func TestLogicalRunEventPrecedesPhysicalAttemptEvents(t *testing.T) {
	h := newHarness(t, finalProvider{})
	m, _ := h.Models().Get("test", "m")
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &m}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mu sync.Mutex
	var timeline []string
	s.On(func(_ context.Context, _ harness.Context[harness.NoState], e harness.RunEvent) {
		mu.Lock()
		timeline = append(timeline, "run:"+string(e.Status))
		mu.Unlock()
	})
	s.On(func(context.Context, harness.Context[harness.NoState], harness.AgentEvent) {
		mu.Lock()
		timeline = append(timeline, "agent")
		mu.Unlock()
	})
	run := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("question")}})
	if err := run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(timeline) < 3 || timeline[0] != "run:running" || timeline[len(timeline)-1] != "run:completed" {
		t.Fatalf("event timeline=%v", timeline)
	}
}

func TestLogicalRunCancellationCauses(t *testing.T) {
	tests := []struct {
		name     string
		provider harness.Provider
		context  func() (context.Context, context.CancelFunc)
		status   harness.Status
		cause    harness.Cause
		wantErr  error
	}{
		{
			name:     "parent canceled",
			provider: finalProvider{},
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			status:  harness.StatusAborted,
			cause:   harness.CauseParentCanceled,
			wantErr: context.Canceled,
		},
		{
			name:     "deadline",
			provider: finalProvider{},
			context: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			status:  harness.StatusAborted,
			cause:   harness.CauseDeadline,
			wantErr: context.DeadlineExceeded,
		},
		{
			name: "provider aborted",
			provider: &harness.Faux{StreamFunc: func(context.Context, harness.Request) (harness.Stream, error) {
				return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventDone, StopReason: harness.StopAborted}}}, nil
			}},
			context: func() (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			},
			status:  harness.StatusAborted,
			cause:   harness.CauseProviderAbort,
			wantErr: harness.ErrProviderAborted,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.provider)
			m, _ := h.Models().Get("test", "m")
			s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &m}, harness.NoState{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx, cancel := tc.context()
			defer cancel()
			run, err := s.Start(ctx, harness.Prompt{Steps: harness.Sequence{harness.UserText("question")}}, harness.StartOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err = run.Wait(context.Background()); !errors.Is(err, tc.wantErr) {
				t.Fatalf("wait error=%v want %v", err, tc.wantErr)
			}
			info, err := s.RunInfo(run.ID())
			if err != nil || info.Status != tc.status || info.Cause != tc.cause {
				t.Fatalf("info=%#v error=%v", info, err)
			}
		})
	}
}

type promptHookExtension struct {
	inputs, responses int
}

func (e *promptHookExtension) Register(registry *harness.ExtensionRegistry[harness.NoState]) error {
	registry.AddInputHook(func(_ context.Context, _ harness.Context[harness.NoState], m harness.Message) (harness.Message, error) {
		e.inputs++
		m.Content = append(m.Content, harness.Text("-hooked"))
		return m, nil
	})
	registry.AddResponseHook(func(_ context.Context, _ harness.Context[harness.NoState], _ harness.Message) error {
		e.responses++
		return nil
	})
	return nil
}

func TestPromptStepHooksDistinguishScriptFromModelResponses(t *testing.T) {
	exts := &promptHookExtension{}
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	_ = models.Register(selected)
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterExtension(exts); err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("test", finalProvider{}); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	steps := harness.Sequence{harness.UserText("one"), harness.AssistantText("scripted"), harness.UserText("two")}
	run := startSession(t, s, harness.Prompt{Steps: steps})
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	messages := s.Conversation().Messages
	if exts.inputs != 2 || exts.responses != 1 {
		t.Fatalf("input hooks=%d response hooks=%d", exts.inputs, exts.responses)
	}
	if messages[0].Text() != "one-hooked" || messages[1].Text() != "scripted" || messages[2].Text() != "two-hooked" {
		t.Fatalf("messages=%#v", messages)
	}
}
func TestNoModel(t *testing.T) {
	h, _ := harness.NewStateless(harness.Options{})
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("x")}}, harness.StartOptions{}); !errors.Is(err, harness.ErrNoModel) {
		t.Fatalf("error=%v", err)
	}
}

type gatedProvider struct {
	once     sync.Once
	started  chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	requests []harness.Request
}

func (g *gatedProvider) Stream(ctx context.Context, r harness.Request) (harness.Stream, error) {
	g.mu.Lock()
	g.requests = append(g.requests, r)
	n := len(g.requests)
	g.mu.Unlock()
	if n == 1 {
		g.once.Do(func() { close(g.started) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventTextDelta, Text: "turn"}, {Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
}
func TestSteerAndBusy(t *testing.T) {
	p := &gatedProvider{started: make(chan struct{}), release: make(chan struct{})}
	h := newHarness(t, p)
	m, _ := h.Models().Get("test", "m")
	s, _ := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &m}, harness.NoState{})
	defer s.Close()
	run, err := s.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("first")}}, harness.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	if _, err := s.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("second")}}, harness.StartOptions{}); !errors.Is(err, harness.ErrAgentBusy) {
		t.Fatalf("busy error=%v", err)
	}
	if err := run.Steer(context.Background(), harness.User("steer")); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	if err := run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != 2 || p.requests[1].Messages[len(p.requests[1].Messages)-1].Text() != "steer" {
		t.Fatalf("requests=%#v", p.requests)
	}
}

type retryProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *retryProvider) Stream(context.Context, harness.Request) (harness.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return nil, &harness.ProviderError{Provider: "test", Message: "temporary", Retryable: true}
	}
	return (&finalProvider{}).Stream(context.Background(), harness.Request{})
}

type overflowCompactionProvider struct {
	mu       sync.Mutex
	calls    int
	requests []harness.Request
}

func (p *overflowCompactionProvider) Stream(_ context.Context, request harness.Request) (harness.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.requests = append(p.requests, request)
	if p.calls == 2 {
		return nil, &harness.ProviderError{
			Provider: "test", StatusCode: 400,
			Code: "context_length_exceeded", Message: "maximum context length exceeded",
			Cause: harness.ErrContextOverflow,
		}
	}
	text := "first"
	if p.calls == 3 {
		text = "after compaction"
	}
	return &harness.SliceStream{Events: []harness.StreamEvent{
		{Type: harness.EventTextDelta, Text: text},
		{Type: harness.EventDone, StopReason: harness.StopStop},
	}}, nil
}

type fixedCompactionSummarizer struct{}

func (fixedCompactionSummarizer) Summarize(context.Context, []harness.Message, string) (harness.CompactionSummary, error) {
	return harness.CompactionSummary{Text: "earlier context summary"}, nil
}

func TestContextOverflowAutomaticallyCompactsAndRetries(t *testing.T) {
	p := &overflowCompactionProvider{}
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RegisterProvider("test", p); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{
		Model:      &selected,
		Summarizer: fixedCompactionSummarizer{},
		Compaction: harness.CompactionOptions{
			KeepRecentTokens: 1,
			Estimator:        harness.TokenEstimatorFunc(func(harness.Message) int64 { return 1 }),
		},
	}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("first request")}}).Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("overflow request")}}).Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	p.mu.Lock()
	calls := p.calls
	requests := append([]harness.Request(nil), p.requests...)
	p.mu.Unlock()
	if calls != 3 || len(requests) != 3 {
		t.Fatalf("calls=%d requests=%d", calls, len(requests))
	}
	if len(requests[2].Messages) != 2 || !strings.Contains(requests[2].Messages[0].Text(), harness.CompactionSummaryPrefix) {
		t.Fatalf("retry context=%#v", requests[2].Messages)
	}
	if got := s.Conversation().Messages[len(s.Conversation().Messages)-1].Text(); got != "after compaction" {
		t.Fatalf("final response=%q", got)
	}
}

func TestRetryableProviderError(t *testing.T) {
	p := &retryProvider{}
	cfg := harness.NewSettings()
	_ = cfg.Set(harness.RetryAttempts, 1)
	_ = cfg.Set(harness.RetryDelay, time.Duration(0))
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	_ = models.Register(selected)
	h, _ := harness.NewStateless(harness.Options{Models: models, Settings: cfg})
	_ = h.RegisterProvider("test", p)
	s, _ := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, harness.NoState{})
	defer s.Close()
	seenAttempts := map[int]bool{}
	var logicalID harness.ID
	s.On(func(_ context.Context, _ harness.Context[harness.NoState], e harness.AgentEvent) {
		seenAttempts[e.Attempt] = true
		logicalID = e.RunID
	})
	run := startSession(t, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("retry")}})
	if err := run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	calls := p.calls
	p.mu.Unlock()
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if logicalID != run.ID() || !seenAttempts[1] || !seenAttempts[2] {
		t.Fatalf("logical id=%q run=%q attempts=%v", logicalID, run.ID(), seenAttempts)
	}
	messages := s.Conversation().Messages
	if len(messages) != 2 || messages[0].Role != harness.RoleUser || messages[1].StopReason != harness.StopStop {
		t.Fatalf("retry persisted intermediate failure: %#v", messages)
	}
}

func TestScriptedPromptPersistsAndProviderRetryDoesNotReplayTools(t *testing.T) {
	p := &retryProvider{}
	cfg := harness.NewSettings()
	_ = cfg.Set(harness.RetryAttempts, 1)
	_ = cfg.Set(harness.RetryDelay, time.Duration(0))
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	_ = models.Register(selected)
	tools := harness.NewToolRegistry()
	executions := 0
	type params struct {
		Value string `json:"value"`
	}
	if err := tools.Register(harness.Tool[params, struct{}]{Name: "record", Execute: func(_ context.Context, _ harness.ToolCall, p params, _ harness.Update[struct{}]) (harness.ToolResult[struct{}], error) {
		executions++
		return harness.ToolResult[struct{}]{Content: []harness.Content{harness.Text(p.Value)}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	h, err := harness.NewStateless(harness.Options{Models: models, Tools: tools, Settings: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("test", p); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	steps := harness.Sequence{
		harness.UserText("start"),
		harness.AssistantText("recording"),
		harness.Tools(harness.PromptToolCall{Key: "record-once", Name: "record", Arguments: json.RawMessage(`{"value":"stored"}`)}),
	}
	run := startSession(t, s, harness.Prompt{Steps: steps})
	if err = run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	providerCalls := p.calls
	p.mu.Unlock()
	if executions != 1 || providerCalls != 2 {
		t.Fatalf("tool executions=%d provider calls=%d", executions, providerCalls)
	}
	messages := s.Conversation().Messages
	if len(messages) != 5 {
		t.Fatalf("messages=%#v", messages)
	}
	wantOrigins := []harness.Origin{harness.OriginUser, harness.OriginScript, harness.OriginScript, harness.OriginTool, harness.OriginModel}
	for i, origin := range wantOrigins {
		if messages[i].Origin != origin {
			t.Fatalf("message %d origin=%q want %q", i, messages[i].Origin, origin)
		}
	}
	if messages[2].Content[0].Key != "record-once" || messages[3].ToolCallKey != "record-once" {
		t.Fatalf("script key was not persisted: %#v", messages)
	}
}

type partialSessionStream struct {
	ctx     context.Context
	started chan struct{}
	sent    bool
}

func (s *partialSessionStream) Next() (harness.StreamEvent, error) {
	if !s.sent {
		s.sent = true
		close(s.started)
		return harness.StreamEvent{Type: harness.EventTextDelta, Text: "partial session answer"}, nil
	}
	<-s.ctx.Done()
	return harness.StreamEvent{}, s.ctx.Err()
}
func (s *partialSessionStream) Close() error { return nil }

func TestLogicalRunPersistsIdentityAndPartialAbort(t *testing.T) {
	started := make(chan struct{})
	p := &harness.Faux{StreamFunc: func(ctx context.Context, _ harness.Request) (harness.Stream, error) {
		return &partialSessionStream{ctx: ctx, started: started}, nil
	}}
	h := newHarness(t, p)
	m, _ := h.Models().Get("test", "m")
	directory := t.TempDir()
	persistence, err := harness.NewFilePersistence(directory)
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), persistence, harness.SessionOptions{Model: &m}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	var statuses []harness.Status
	var logicalIDs []harness.ID
	var eventsMu sync.Mutex
	s.On(func(_ context.Context, _ harness.Context[harness.NoState], e harness.RunEvent) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		statuses = append(statuses, e.Status)
	})
	s.On(func(_ context.Context, _ harness.Context[harness.NoState], e harness.AgentEvent) {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		logicalIDs = append(logicalIDs, e.RunID)
	})
	r, err := s.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("question")}}, harness.StartOptions{ID: "logical-1"})
	if err != nil {
		t.Fatal(err)
	}
	path := s.Location()
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("run start did not persist session: %v", err)
	}
	<-started
	if !r.Abort() || r.Abort() {
		t.Fatal("abort was not idempotent")
	}
	if err = r.Wait(context.Background()); !errors.Is(err, harness.ErrAborted) {
		t.Fatalf("wait error=%v", err)
	}
	info, err := s.RunInfo("logical-1")
	if err != nil || info.Status != harness.StatusAborted || info.Cause != harness.CauseUserAbort {
		t.Fatalf("info=%#v err=%v", info, err)
	}
	messages := s.Conversation().Messages
	if len(messages) != 2 || messages[1].Text() != "partial session answer" || messages[1].StopReason != harness.StopAborted {
		t.Fatalf("messages=%#v", messages)
	}
	eventsMu.Lock()
	gotStatuses := append([]harness.Status(nil), statuses...)
	gotLogicalIDs := append([]harness.ID(nil), logicalIDs...)
	eventsMu.Unlock()
	if len(gotStatuses) != 3 || gotStatuses[0] != harness.StatusRunning || gotStatuses[1] != harness.StatusCancelling || gotStatuses[2] != harness.StatusAborted {
		t.Fatalf("statuses=%v", gotStatuses)
	}
	for _, id := range gotLogicalIDs {
		if id != "logical-1" {
			t.Fatalf("logical event id=%q", id)
		}
	}
	if _, err = s.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("again")}}, harness.StartOptions{ID: "logical-1"}); !errors.Is(err, harness.ErrDuplicateID) {
		t.Fatalf("duplicate error=%v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := h.OpenSession(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	info, err = reopened.RunInfo("logical-1")
	if err != nil || info.Status != harness.StatusAborted {
		t.Fatalf("reopened info=%#v err=%v", info, err)
	}
}

func TestOpenSessionMarksDanglingRunInterrupted(t *testing.T) {
	persistence, err := harness.NewFilePersistence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := harness.NewSessionManager(persistence, harness.PersistenceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AppendRunStart("dangling"); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	h, _ := harness.NewStateless(harness.Options{})
	path := m.Location()
	s, err := h.OpenSession(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	info, err := s.RunInfo("dangling")
	if err != nil || info.Status != harness.StatusFailed || info.Cause != harness.CauseInterrupted || info.Error != harness.ErrInterrupted.Error() {
		t.Fatalf("info=%#v err=%v", info, err)
	}
}
