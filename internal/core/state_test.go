package core_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

type sharedState struct {
	Count int     `json:"count"`
	Order []int   `json:"order"`
	Score float64 `json:"score"`
}

type stateParams struct {
	Value   int  `json:"value"`
	DelayMS int  `json:"delayMs"`
	Fail    bool `json:"fail"`
}

type stateDetails struct {
	Observed int `json:"observed"`
}

type incrementInputExtension struct{}

func (incrementInputExtension) Register(r *harness.ExtensionRegistry[sharedState]) error {
	r.AddInputHook(func(ctx context.Context, c harness.Context[sharedState], m harness.Message) (harness.Message, error) {
		if err := c.UpdateState(func(state *sharedState) { state.Count++ }); err != nil {
			return m, err
		}
		return m, nil
	})
	return nil
}

func stateHarness(t *testing.T) (*harness.Harness[sharedState], harness.Model) {
	t.Helper()
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	h, err := harness.New[sharedState](harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RegisterProvider("test", &stateProvider{}); err != nil {
		t.Fatal(err)
	}
	return h, selected
}

type stateProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *stateProvider) Stream(_ context.Context, request harness.Request) (harness.Stream, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	p.mu.Unlock()
	if call == 0 && len(request.Tools) != 0 {
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventToolCallStart, Index: 0, ToolCallID: "slow-first", ToolName: "record_state", ArgumentsDelta: `{"value":1,"delayMs":40}`},
			{Type: harness.EventToolCallStart, Index: 1, ToolCallID: "fast-second", ToolName: "record_state", ArgumentsDelta: `{"value":2,"delayMs":0}`},
			{Type: harness.EventToolCallStart, Index: 2, ToolCallID: "failed-third", ToolName: "record_state", ArgumentsDelta: `{"value":3,"delayMs":0,"fail":true}`},
			{Type: harness.EventDone, StopReason: harness.StopToolUse},
		}}, nil
	}
	return &harness.SliceStream{Events: []harness.StreamEvent{{Type: harness.EventTextDelta, Text: "done"}, {Type: harness.EventDone, StopReason: harness.StopStop}}}, nil
}

func TestParallelToolsShareSnapshotAndCommitInCallOrder(t *testing.T) {
	h, selected := stateHarness(t)
	var mu sync.Mutex
	observed := map[int]int{}
	started := make(chan int, 3)
	release := make(chan struct{})
	err := h.RegisterTool(harness.ToolSpec{Name: "record_state", Description: "record state", ExecutionMode: harness.Parallel}, func(ctx context.Context, c harness.Context[sharedState], p stateParams) (harness.ToolResult[stateDetails], error) {
		before := c.State().Count
		mu.Lock()
		observed[p.Value] = before
		mu.Unlock()
		started <- p.Value
		<-release
		if p.DelayMS > 0 {
			time.Sleep(time.Duration(p.DelayMS) * time.Millisecond)
		}
		if err := c.UpdateState(func(state *sharedState) {
			state.Count++
			state.Order = append(state.Order, p.Value)
		}); err != nil {
			return harness.ToolResult[stateDetails]{}, err
		}
		if p.Fail {
			return harness.ToolResult[stateDetails]{}, errors.New("failed after staging")
		}
		return harness.ToolResult[stateDetails]{Content: []harness.Content{harness.Text("ok")}, Details: stateDetails{Observed: before}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	persistence, err := harness.NewFilePersistence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), persistence, harness.SessionOptions{Model: &selected}, sharedState{Count: 10})
	if err != nil {
		t.Fatal(err)
	}
	steps := harness.Sequence{harness.UserText("run tools")}
	run, err := s.Start(context.Background(), harness.Prompt{Steps: steps}, harness.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	seenStarted := map[int]bool{}
	for range 3 {
		select {
		case value := <-started:
			seenStarted[value] = true
		case <-time.After(time.Second):
			t.Fatal("tool handlers did not start in parallel")
		}
	}
	close(release)
	if err := run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seenStarted) != 3 {
		t.Fatalf("started=%v", seenStarted)
	}

	mu.Lock()
	if observed[1] != 10 || observed[2] != 10 || observed[3] != 10 {
		t.Fatalf("observed=%v", observed)
	}
	mu.Unlock()
	want := sharedState{Count: 12, Order: []int{1, 2}}
	if got := s.State(); got.Count != want.Count || len(got.Order) != 2 || got.Order[0] != 1 || got.Order[1] != 2 {
		t.Fatalf("state=%#v", got)
	}
	copy := s.State()
	copy.Count = 99
	copy.Order[0] = 99
	if got := s.State(); got.Count != 12 || got.Order[0] != 1 {
		t.Fatalf("state leaked through copy: %#v", got)
	}
	if got := run.State(); got.Count != 12 {
		t.Fatalf("run state=%#v", got)
	}

	var firstState harness.SessionEntryID
	for _, entry := range s.Entries() {
		if entry.Type == "state" {
			firstState = entry.ID
			break
		}
	}
	if firstState == "" {
		t.Fatal("state entry was not persisted")
	}
	if err := s.Navigate(context.Background(), &firstState); err != nil {
		t.Fatal(err)
	}
	if got := s.State(); got.Count != 11 || len(got.Order) != 1 || got.Order[0] != 1 {
		t.Fatalf("navigated state=%#v", got)
	}
	if got := run.State(); got.Count != 12 {
		t.Fatalf("terminal run state changed after navigate: %#v", got)
	}
	child, err := s.Fork(context.Background(), firstState, harness.SessionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := child.State(); got.Count != 11 || len(got.Order) != 1 {
		t.Fatalf("fork state=%#v", got)
	}
	if err := child.Context().UpdateState(func(state *sharedState) { state.Count = 20 }); err != nil {
		t.Fatal(err)
	}
	if s.State().Count != 11 {
		t.Fatal("fork update changed parent state")
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Context().UpdateState(func(*sharedState) {}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	path := s.Location()
	opened, err := h.OpenSession(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if got := opened.State(); got.Count != 11 || len(got.Order) != 1 {
		t.Fatalf("opened state=%#v", got)
	}
}

func TestSessionContextStatePermissionsAndHookCommit(t *testing.T) {
	h, selected := stateHarness(t)
	if err := h.RegisterExtension(incrementInputExtension{}); err != nil {
		t.Fatal(err)
	}

	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, sharedState{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sc := s.Context()
	if err := sc.UpdateState(func(state *sharedState) { state.Count = 4 }); err != nil {
		t.Fatal(err)
	}
	if err := sc.UpdateState(func(state *sharedState) { state.Score = math.NaN() }); err == nil {
		t.Fatal("invalid JSON state was accepted")
	}
	if got := s.State(); got.Count != 4 {
		t.Fatalf("state=%#v", got)
	}

	called := false
	s.On(func(_ context.Context, eventCtx harness.Context[sharedState], _ harness.RunEvent) {
		called = true
		if err := eventCtx.UpdateState(func(state *sharedState) { state.Count++ }); !errors.Is(err, harness.ErrStateReadOnly) {
			t.Errorf("observer update error=%v", err)
		}
	})
	run, err := s.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("done")}}, harness.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("run event was not observed")
	}
	if got := s.State(); got.Count != 5 {
		t.Fatalf("hook or observer state=%#v", got)
	}
}

// nonCopyableState persists fine (the channel is hidden from JSON) but cannot
// be deep-copied, so Start must reject it before the agent runs.
type nonCopyableState struct {
	Count int      `json:"count"`
	Ready chan int `json:"-"`
}

func TestStartRejectsNonDeepCopyableState(t *testing.T) {
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "test", ID: "m"}
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	h, err := harness.New[nonCopyableState](harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RegisterProvider("test", &stateProvider{}); err != nil {
		t.Fatal(err)
	}
	s, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, nonCopyableState{Ready: make(chan int)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run, err := s.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("go")}}, harness.StartOptions{})
	if err == nil {
		t.Fatal("Start accepted a state that cannot be deep-copied")
	}
	if run != nil {
		t.Fatal("Start returned a run for a rejected state")
	}
}

var _ harness.Provider = finalProvider{}
