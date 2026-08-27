package e2e_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

type gatedProvider struct {
	base     harness.Provider
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
	calls    atomic.Int32
	mu       sync.Mutex
	requests []harness.Request
}

func (p *gatedProvider) Stream(ctx context.Context, request harness.Request) (harness.Stream, error) {
	call := p.calls.Add(1)
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	if call == 1 {
		p.once.Do(func() { close(p.started) })
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.base.Stream(ctx, request)
}

func TestDeepSeekSteerFollowUpBusyAndQueueDrain(t *testing.T) {
	p, selected := deepSeek(t)
	gate := &gatedProvider{base: p, started: make(chan struct{}), release: make(chan struct{})}
	a := harness.NewAgent(harness.AgentOptions{Provider: gate, Model: selected, QueueMode: harness.QueueAll, Generation: nonThinking})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := startAgentRun(t, ctx, a, harness.AgentPrompt{Steps: harness.Sequence{harness.UserText("Reply with INITIAL_OK only.")}})
	select {
	case <-gate.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := a.Start(ctx, harness.AgentPrompt{}, harness.AgentStartOptions{}); !errors.Is(err, harness.ErrAgentBusy) {
		t.Fatalf("busy error=%v", err)
	}
	_ = run.Steer(harness.User("This is steering. Reply with STEER_OK only."))
	_ = run.FollowUp(harness.User("This is follow-up. Reply with FOLLOWUP_OK only."))
	close(gate.release)
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if gate.calls.Load() != 3 {
		t.Fatalf("provider calls=%d messages=%#v", gate.calls.Load(), a.Messages())
	}
	gate.mu.Lock()
	requests := append([]harness.Request(nil), gate.requests...)
	gate.mu.Unlock()
	assertContextRoles(t, requests[0], harness.RoleUser)
	assertContextRoles(t, requests[1], harness.RoleUser, harness.RoleAssistant, harness.RoleUser)
	assertContextRoles(t, requests[2], harness.RoleUser, harness.RoleAssistant, harness.RoleUser, harness.RoleAssistant, harness.RoleUser)
	if got := requests[1].Messages[len(requests[1].Messages)-1].Text(); got != "This is steering. Reply with STEER_OK only." {
		t.Fatalf("steer request=%q", got)
	}
	if got := requests[2].Messages[len(requests[2].Messages)-1].Text(); got != "This is follow-up. Reply with FOLLOWUP_OK only." {
		t.Fatalf("follow-up request=%q", got)
	}
	messages := a.Messages()
	if !stringsContainAssistant(messages, "INITIAL_OK") || !stringsContainAssistant(messages, "STEER_OK") || !stringsContainAssistant(messages, "FOLLOWUP_OK") {
		t.Fatalf("messages=%#v", messages)
	}
}

func stringsContainAssistant(messages []harness.Message, text string) bool {
	for _, m := range messages {
		if m.Role == harness.RoleAssistant && contains(m.Text(), text) {
			return true
		}
	}
	return false
}

func contains(s, part string) bool {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return true
		}
	}
	return false
}

func TestDeepSeekOneAtATimeSteerQueue(t *testing.T) {
	p, selected := deepSeek(t)
	gate := &gatedProvider{base: p, started: make(chan struct{}), release: make(chan struct{})}
	a := harness.NewAgent(harness.AgentOptions{Provider: gate, Model: selected, QueueMode: harness.QueueOneAtATime, Generation: nonThinking})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := startAgentRun(t, ctx, a, harness.AgentPrompt{Steps: harness.Sequence{harness.UserText("Reply QUEUE_START.")}})
	select {
	case <-gate.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_ = run.Steer(harness.User("Reply QUEUE_ONE."))
	_ = run.Steer(harness.User("Reply QUEUE_TWO."))
	close(gate.release)
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if gate.calls.Load() != 3 {
		t.Fatalf("provider calls=%d", gate.calls.Load())
	}
	gate.mu.Lock()
	requests := append([]harness.Request(nil), gate.requests...)
	gate.mu.Unlock()
	assertContextRoles(t, requests[0], harness.RoleUser)
	assertContextRoles(t, requests[1], harness.RoleUser, harness.RoleAssistant, harness.RoleUser)
	assertContextRoles(t, requests[2], harness.RoleUser, harness.RoleAssistant, harness.RoleUser, harness.RoleAssistant, harness.RoleUser)
	if requests[1].Messages[len(requests[1].Messages)-1].Text() != "Reply QUEUE_ONE." || requests[2].Messages[len(requests[2].Messages)-1].Text() != "Reply QUEUE_TWO." {
		t.Fatalf("requests=%#v", requests)
	}
}

type retryOnceProvider struct {
	base     harness.Provider
	calls    atomic.Int32
	mu       sync.Mutex
	requests []harness.Request
}

func (p *retryOnceProvider) Stream(ctx context.Context, request harness.Request) (harness.Stream, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	if p.calls.Add(1) == 1 {
		return nil, &harness.ProviderError{Provider: "deepseek", StatusCode: 503, Message: "injected retry", Retryable: true}
	}
	return p.base.Stream(ctx, request)
}

func TestDeepSeekSessionRetryThenRealResponse(t *testing.T) {
	p, selected := deepSeek(t)
	retry := &retryOnceProvider{base: p}
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	config := harness.NewSettings()
	if err := config.Set(harness.RetryAttempts, 1); err != nil {
		t.Fatal(err)
	}
	if err := config.Set(harness.RetryDelay, time.Duration(0)); err != nil {
		t.Fatal(err)
	}
	h, err := harness.NewStateless(harness.Options{Models: models, Settings: config})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("deepseek", retry); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, Generation: nonThinking}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("Reply RETRY_OK only.")}})
	if err = run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if retry.calls.Load() != 2 {
		t.Fatalf("provider calls=%d", retry.calls.Load())
	}
	if !stringsContainAssistant(s.Conversation().Messages, "RETRY_OK") {
		t.Fatalf("messages=%#v", s.Conversation().Messages)
	}
	retry.mu.Lock()
	requests := append([]harness.Request(nil), retry.requests...)
	retry.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	assertContextRoles(t, requests[0], harness.RoleUser)
	assertContextRoles(t, requests[1], harness.RoleUser)
	if requests[0].SystemPrompt != requests[1].SystemPrompt || requests[0].Messages[0].Text() != requests[1].Messages[0].Text() {
		t.Fatalf("retry changed provider context: first=%#v second=%#v", requests[0], requests[1])
	}
}

type abortAfterFirstProviderEvent struct {
	base  harness.Provider
	first chan struct{}
}

func (p *abortAfterFirstProviderEvent) Stream(ctx context.Context, request harness.Request) (harness.Stream, error) {
	stream, err := p.base.Stream(ctx, request)
	if err != nil {
		return nil, err
	}
	return &abortAfterFirstStream{ctx: ctx, base: stream, first: p.first}, nil
}

type abortAfterFirstStream struct {
	ctx       context.Context
	base      harness.Stream
	first     chan struct{}
	delivered bool
}

func (s *abortAfterFirstStream) Next() (harness.StreamEvent, error) {
	if !s.delivered {
		s.delivered = true
		event, err := s.base.Next()
		close(s.first)
		return event, err
	}
	<-s.ctx.Done()
	return harness.StreamEvent{}, s.ctx.Err()
}

func (s *abortAfterFirstStream) Close() error { return s.base.Close() }

func TestDeepSeekSessionRuntimeAbort(t *testing.T) {
	p, selected := deepSeek(t)
	first := make(chan struct{})
	abortable := &abortAfterFirstProviderEvent{base: p, first: first}
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("deepseek", abortable); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, Generation: nonThinking}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{
		harness.UserText("Write a long explanation of Go context cancellation."),
	}})
	select {
	case <-first:
	case <-run.Done():
		t.Fatalf("run ended before abort: %v", run.Err())
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !run.Abort() {
		t.Fatal("runtime abort was not accepted")
	}
	if err = run.Wait(ctx); !errors.Is(err, harness.ErrAborted) {
		t.Fatalf("wait error=%v", err)
	}
	info, err := s.RunInfo(run.ID())
	if err != nil || info.Status != harness.StatusAborted || info.Cause != harness.CauseUserAbort {
		t.Fatalf("run info=%#v error=%v", info, err)
	}
}
