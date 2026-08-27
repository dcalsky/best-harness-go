package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

type fixedEstimator struct{}

func (fixedEstimator) Estimate(harness.Message) int64 { return 1 }

func newDeepSeekHarness(t *testing.T, p harness.Provider, selected harness.Model) *harness.Harness[harness.NoState] {
	t.Helper()
	models := harness.NewModelRegistry()
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.RegisterProvider("deepseek", p); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestDeepSeekAutomaticThresholdCompaction(t *testing.T) {
	p, selected := deepSeek(t)
	h := newDeepSeekHarness(t, p, selected)
	summarizer := deepSeekSummarizer{p: p, model: selected}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, Generation: nonThinking, Summarizer: summarizer, Compaction: harness.CompactionOptions{ContextWindow: 10, ReserveTokens: 1, KeepRecentTokens: 1, Estimator: fixedEstimator{}}}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var events atomic.Int32
	s.On(func(_ context.Context, _ harness.Context[harness.NoState], e harness.CompactionEvent) {
		if e.Reason == harness.CompactionThreshold && e.Err == nil {
			events.Add(1)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("Reply THRESHOLD_OK only.")}})
	if err = run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	entries := s.Entries()
	foundCompaction := false
	for _, entry := range entries {
		foundCompaction = foundCompaction || entry.Type == "compaction"
	}
	if events.Load() != 1 || !foundCompaction {
		t.Fatalf("events=%d entries=%#v", events.Load(), entries)
	}
	if len(s.Conversation().Messages) < 2 || !contains(s.Conversation().Messages[0].Text(), "COMPACT_OK") {
		t.Fatalf("context=%#v", s.Conversation().Messages)
	}
	if text := s.Conversation().Messages[0].Text(); !contains(text, harness.CompactionSummaryPrefix) || !contains(text, harness.CompactionSummarySuffix) {
		t.Fatalf("pi-incompatible compaction summary=%q", text)
	}
}

func TestDeepSeekCompactionKeepsToolCallAndResultTogether(t *testing.T) {
	p, selected := deepSeek(t)
	m, err := harness.NewSessionManager(harness.NewMemoryPersistence(), harness.PersistenceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = m.AppendMessage(harness.User("old context to summarize"))
	call := harness.Message{Role: harness.RoleAssistant, Content: []harness.Content{harness.NewToolCallContent("call-boundary", "lookup", json.RawMessage(`{"key":"x"}`))}, Timestamp: time.Now().UnixMilli()}
	callID, err := m.AppendMessage(call)
	if err != nil {
		t.Fatal(err)
	}
	result := harness.Message{Role: harness.RoleTool, ToolCallID: "call-boundary", ToolName: "lookup", Content: []harness.Content{harness.Text("tool-value")}, Timestamp: time.Now().UnixMilli()}
	if _, err = m.AppendMessage(result); err != nil {
		t.Fatal(err)
	}
	_, _ = m.AppendMessage(harness.User("recent question"))
	_, _ = m.AppendMessage(harness.Message{Role: harness.RoleAssistant, Content: []harness.Content{harness.Text("recent answer")}, Timestamp: time.Now().UnixMilli()})
	preparation, err := harness.PrepareCompaction(m.Entries(), harness.CompactionManual, harness.CompactionOptions{KeepRecentTokens: 3, Estimator: fixedEstimator{}})
	if err != nil {
		t.Fatal(err)
	}
	if preparation.FirstKeptEntryID != callID {
		t.Fatalf("first kept=%s want=%s", preparation.FirstKeptEntryID, callID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err = harness.RunCompaction(ctx, m, harness.CompactionManual, harness.CompactionOptions{KeepRecentTokens: 3, Estimator: fixedEstimator{}}, deepSeekSummarizer{p: p, model: selected}); err != nil {
		t.Fatal(err)
	}
	messages := m.Context().Messages
	var sawCall, sawResult bool
	for _, msg := range messages {
		for _, content := range msg.Content {
			if content.Type == "toolCall" && content.ID == "call-boundary" {
				sawCall = true
			}
		}
		if msg.Role == harness.RoleTool && msg.ToolCallID == "call-boundary" {
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("call=%v result=%v context=%#v", sawCall, sawResult, messages)
	}
	if messages[0].Text() == "" || !contains(messages[0].Text(), harness.CompactionSummaryPrefix) {
		t.Fatalf("pi-incompatible compaction context=%#v", messages)
	}
}

type injectedOverflowProvider struct {
	base        harness.Provider
	calls       atomic.Int32
	overflowAt  int32
	alwaysAfter bool
	mu          sync.Mutex
	requests    []harness.Request
}

func (p *injectedOverflowProvider) Stream(ctx context.Context, request harness.Request) (harness.Stream, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	call := p.calls.Add(1)
	if call == p.overflowAt || (p.alwaysAfter && call >= p.overflowAt) {
		return nil, &harness.ProviderError{
			Provider: "injected", StatusCode: 400,
			Code: "context_length_exceeded", Message: "maximum context length exceeded",
			Cause: harness.ErrContextOverflow,
		}
	}
	return p.base.Stream(ctx, request)
}

func (p *injectedOverflowProvider) snapshot() []harness.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]harness.Request(nil), p.requests...)
}

func TestDeepSeekOverflowCompactsAndRetriesOnce(t *testing.T) {
	base, selected := deepSeek(t)
	wrapped := &injectedOverflowProvider{base: base, overflowAt: 2}
	h := newDeepSeekHarness(t, wrapped, selected)
	summarizer := deepSeekSummarizer{p: base, model: selected}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, Generation: nonThinking, Summarizer: summarizer, Compaction: harness.CompactionOptions{KeepRecentTokens: 1, Estimator: fixedEstimator{}}}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run := startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("Reply FIRST_OK only.")}})
	if err = run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	run = startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("The compacted summary is historical context. Follow this newest instruction: reply exactly AFTER_OVERFLOW_OK and do not repeat FIRST_OK.")}})
	if err = run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if wrapped.calls.Load() != 3 {
		t.Fatalf("provider calls=%d", wrapped.calls.Load())
	}
	requests := wrapped.snapshot()
	if len(requests) != 3 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	assertContextRoles(t, requests[0], harness.RoleUser)
	assertContextRoles(t, requests[1], harness.RoleUser, harness.RoleAssistant, harness.RoleUser)
	assertContextRoles(t, requests[2], harness.RoleUser, harness.RoleUser)
	if !contains(requests[2].Messages[0].Text(), harness.CompactionSummaryPrefix) {
		t.Fatalf("overflow retry context=%#v", requests[2].Messages)
	}
	var compacted bool
	for _, entry := range s.Entries() {
		if entry.Type == "compaction" {
			compacted = true
		}
	}
	if !compacted || !stringsContainAssistant(s.Conversation().Messages, "AFTER_OVERFLOW_OK") {
		t.Fatalf("entries=%#v context=%#v", s.Entries(), s.Conversation().Messages)
	}
}

func TestDeepSeekOverflowDoesNotRetryTwice(t *testing.T) {
	base, selected := deepSeek(t)
	wrapped := &injectedOverflowProvider{base: base, overflowAt: 2, alwaysAfter: true}
	h := newDeepSeekHarness(t, wrapped, selected)
	summarizer := deepSeekSummarizer{p: base, model: selected}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected, Generation: nonThinking, Summarizer: summarizer, Compaction: harness.CompactionOptions{KeepRecentTokens: 1, Estimator: fixedEstimator{}}}, harness.NoState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run := startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("Reply BASE_OK only.")}})
	if err = run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	run = startSessionRun(t, ctx, s, harness.Prompt{Steps: harness.Sequence{harness.UserText("This request receives injected overflow.")}})
	err = run.Wait(ctx)
	if !errors.Is(err, harness.ErrContextOverflow) {
		t.Fatalf("error=%v", err)
	}
	if wrapped.calls.Load() != 3 {
		t.Fatalf("provider calls=%d", wrapped.calls.Load())
	}
	requests := wrapped.snapshot()
	if len(requests) != 3 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	assertContextRoles(t, requests[2], harness.RoleUser, harness.RoleUser)
	if !contains(requests[2].Messages[0].Text(), harness.CompactionSummaryPrefix) {
		t.Fatalf("second overflow context=%#v", requests[2].Messages)
	}
}
