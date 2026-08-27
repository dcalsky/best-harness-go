package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

func newTypedStateHarness[S any](t *testing.T, p harness.Provider) (*harness.Harness[S], harness.Model) {
	t.Helper()
	models := harness.NewModelRegistry()
	selected := harness.Model{Provider: "state-e2e", ID: "state-model", ContextWindow: 32_000}
	if err := models.Register(selected); err != nil {
		t.Fatal(err)
	}
	h, err := harness.New[S](harness.Options{Models: models})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.RegisterProvider(selected.Provider, p); err != nil {
		t.Fatal(err)
	}
	return h, selected
}

func runTypedStateTurn[S any](t *testing.T, session *harness.Session[S], text string) *harness.Run[S] {
	t.Helper()
	run, err := session.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText(text)}}, harness.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	return run
}

type parallelStateProvider struct {
	mu    sync.Mutex
	turns int
}

func (p *parallelStateProvider) Stream(context.Context, harness.Request) (harness.Stream, error) {
	p.mu.Lock()
	p.turns++
	turn := p.turns
	p.mu.Unlock()
	if turn == 1 {
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventToolCallStart, Index: 0, ToolCallID: "parallel-alpha", ToolName: "parallel_state", ArgumentsDelta: `{"name":"alpha","delayMs":60}`},
			{Type: harness.EventToolCallStart, Index: 1, ToolCallID: "parallel-beta", ToolName: "parallel_state", ArgumentsDelta: `{"name":"beta","delayMs":30}`},
			{Type: harness.EventToolCallStart, Index: 2, ToolCallID: "parallel-gamma", ToolName: "parallel_state", ArgumentsDelta: `{"name":"gamma","delayMs":0}`},
			{Type: harness.EventDone, StopReason: harness.StopToolUse},
		}}, nil
	}
	return &harness.SliceStream{Events: []harness.StreamEvent{
		{Type: harness.EventTextDelta, Text: "parallel complete"},
		{Type: harness.EventDone, StopReason: harness.StopStop},
	}}, nil
}

type parallelScenarioState struct {
	Counter  int            `json:"counter"`
	Applied  []string       `json:"applied"`
	Observed map[string]int `json:"observed"`
}

type parallelScenarioParams struct {
	Name    string `json:"name"`
	DelayMS int    `json:"delayMs"`
}

func TestParallelToolsReadOneSnapshotAndWriteStateInCallOrderE2E(t *testing.T) {
	provider := &parallelStateProvider{}
	h, selected := newTypedStateHarness[parallelScenarioState](t, provider)
	started := make(chan string, 3)
	release := make(chan struct{})
	var completedMu sync.Mutex
	var completed []string
	err := h.RegisterTool(harness.ToolSpec{Name: "parallel_state", Description: "update shared state", ExecutionMode: harness.Parallel}, func(ctx context.Context, c harness.Context[parallelScenarioState], params parallelScenarioParams) (harness.ToolResult[struct{}], error) {
		observed := c.State().Counter
		started <- params.Name
		<-release
		if params.DelayMS > 0 {
			time.Sleep(time.Duration(params.DelayMS) * time.Millisecond)
		}
		completedMu.Lock()
		completed = append(completed, params.Name)
		completedMu.Unlock()
		if err := c.UpdateState(func(state *parallelScenarioState) {
			state.Counter++
			state.Applied = append(state.Applied, params.Name)
			state.Observed[params.Name] = observed
		}); err != nil {
			return harness.ToolResult[struct{}]{}, err
		}
		return harness.ToolResult[struct{}]{Content: []harness.Content{harness.Text(params.Name)}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	session, err := h.NewSession(context.Background(), harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, parallelScenarioState{Counter: 4, Observed: map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	run, err := session.Start(context.Background(), harness.Prompt{Steps: harness.Sequence{harness.UserText("run parallel state tools")}}, harness.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	startedSet := map[string]bool{}
	for range 3 {
		select {
		case name := <-started:
			startedSet[name] = true
		case <-time.After(time.Second):
			t.Fatal("three tool handlers did not enter concurrently")
		}
	}
	close(release)
	if err := run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(startedSet) != 3 {
		t.Fatalf("started=%v", startedSet)
	}
	completedMu.Lock()
	gotCompleted := append([]string(nil), completed...)
	completedMu.Unlock()
	if !reflect.DeepEqual(gotCompleted, []string{"gamma", "beta", "alpha"}) {
		t.Fatalf("completion order=%v", gotCompleted)
	}
	want := parallelScenarioState{
		Counter:  7,
		Applied:  []string{"alpha", "beta", "gamma"},
		Observed: map[string]int{"alpha": 4, "beta": 4, "gamma": 4},
	}
	if got := session.State(); !reflect.DeepEqual(got, want) {
		t.Fatalf("state:\n got=%#v\nwant=%#v", got, want)
	}
}

type perUserToolProvider struct {
	mu       sync.Mutex
	toolName string
	nextID   int
}

func (p *perUserToolProvider) Stream(_ context.Context, request harness.Request) (harness.Stream, error) {
	if len(request.Messages) == 0 {
		return nil, fmt.Errorf("provider request has no messages")
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role == harness.RoleUser {
		p.mu.Lock()
		p.nextID++
		id := p.nextID
		p.mu.Unlock()
		raw, err := json.Marshal(struct {
			Note string `json:"note"`
		}{Note: last.Text()})
		if err != nil {
			return nil, err
		}
		return &harness.SliceStream{Events: []harness.StreamEvent{
			{Type: harness.EventToolCallStart, Index: 0, ToolCallID: fmt.Sprintf("state-call-%d", id), ToolName: p.toolName, ArgumentsDelta: string(raw)},
			{Type: harness.EventDone, StopReason: harness.StopToolUse},
		}}, nil
	}
	return &harness.SliceStream{Events: []harness.StreamEvent{
		{Type: harness.EventTextDelta, Text: "state saved"},
		{Type: harness.EventDone, StopReason: harness.StopStop},
	}}, nil
}

type complexScenarioTask struct {
	Title    string            `json:"title"`
	Tags     []string          `json:"tags"`
	Metadata map[string]string `json:"metadata"`
}

type complexScenarioBoard struct {
	Tasks  map[string]*complexScenarioTask `json:"tasks"`
	Matrix [][]int                         `json:"matrix"`
}

type complexScenarioCursor struct {
	Board string `json:"board"`
	Task  string `json:"task"`
}

type complexScenarioState struct {
	Boards map[string]*complexScenarioBoard `json:"boards"`
	Audit  []map[string][]string            `json:"audit"`
	Cursor *complexScenarioCursor           `json:"cursor"`
}

type noteParams struct {
	Note string `json:"note"`
}

func TestComplexStateSurvivesToolExecutionAndSessionReopenE2E(t *testing.T) {
	provider := &perUserToolProvider{toolName: "enrich_complex_state"}
	h, selected := newTypedStateHarness[complexScenarioState](t, provider)
	err := h.RegisterTool(harness.ToolSpec{Name: "enrich_complex_state", Description: "update nested state"}, func(ctx context.Context, c harness.Context[complexScenarioState], params noteParams) (harness.ToolResult[struct{}], error) {
		if err := c.UpdateState(func(state *complexScenarioState) {
			board := state.Boards["main"]
			board.Tasks["generated"] = &complexScenarioTask{
				Title:    params.Note,
				Tags:     []string{"tool", "persisted"},
				Metadata: map[string]string{"origin": "agent", "length": fmt.Sprint(len(params.Note))},
			}
			board.Matrix[0] = append(board.Matrix[0], 3)
			state.Audit = append(state.Audit, map[string][]string{"created": {"generated"}, "tags": {"tool", "persisted"}})
			state.Cursor = &complexScenarioCursor{Board: "main", Task: "generated"}
		}); err != nil {
			return harness.ToolResult[struct{}]{}, err
		}
		return harness.ToolResult[struct{}]{Content: []harness.Content{harness.Text("complex state updated")}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := complexScenarioState{
		Boards: map[string]*complexScenarioBoard{"main": {Tasks: map[string]*complexScenarioTask{}, Matrix: [][]int{{1, 2}, {4, 5}}}},
		Audit:  []map[string][]string{},
		Cursor: &complexScenarioCursor{Board: "main"},
	}
	persistence, err := harness.NewFilePersistence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.NewSession(context.Background(), persistence, harness.SessionOptions{Model: &selected}, initial)
	if err != nil {
		t.Fatal(err)
	}
	path := session.Location()
	runTypedStateTurn(t, session, "create nested task")
	want := complexScenarioState{
		Boards: map[string]*complexScenarioBoard{"main": {
			Tasks:  map[string]*complexScenarioTask{"generated": {Title: "create nested task", Tags: []string{"tool", "persisted"}, Metadata: map[string]string{"origin": "agent", "length": "18"}}},
			Matrix: [][]int{{1, 2, 3}, {4, 5}},
		}},
		Audit:  []map[string][]string{{"created": {"generated"}, "tags": {"tool", "persisted"}}},
		Cursor: &complexScenarioCursor{Board: "main", Task: "generated"},
	}
	if got := session.State(); !reflect.DeepEqual(got, want) {
		t.Fatalf("state after tool:\n got=%#v\nwant=%#v", got, want)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := h.OpenSession(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := restored.State(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored state:\n got=%#v\nwant=%#v", got, want)
	}
}

type conversationScenarioState struct {
	Turn   int            `json:"turn"`
	Notes  []string       `json:"notes"`
	Counts map[string]int `json:"counts"`
	Last   *struct {
		Number int    `json:"number"`
		Note   string `json:"note"`
	} `json:"last"`
}

func registerRememberTool(t *testing.T, h *harness.Harness[conversationScenarioState]) {
	t.Helper()
	err := h.RegisterTool(harness.ToolSpec{Name: "remember_turn", Description: "persist one conversation turn"}, func(ctx context.Context, c harness.Context[conversationScenarioState], params noteParams) (harness.ToolResult[struct{}], error) {
		if err := c.UpdateState(func(state *conversationScenarioState) {
			state.Turn++
			state.Notes = append(state.Notes, params.Note)
			state.Counts[params.Note]++
			state.Last = &struct {
				Number int    `json:"number"`
				Note   string `json:"note"`
			}{Number: state.Turn, Note: params.Note}
		}); err != nil {
			return harness.ToolResult[struct{}]{}, err
		}
		return harness.ToolResult[struct{}]{Content: []harness.Content{harness.Text("remembered " + params.Note)}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newConversationHarness(t *testing.T) (*harness.Harness[conversationScenarioState], harness.Model) {
	t.Helper()
	h, selected := newTypedStateHarness[conversationScenarioState](t, &perUserToolProvider{toolName: "remember_turn"})
	registerRememberTool(t, h)
	return h, selected
}

func TestMultiTurnStatePersistsAcrossCloseOpenAndContinuedConversationE2E(t *testing.T) {
	ctx := context.Background()
	persistence, err := harness.NewFilePersistence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h1, selected := newConversationHarness(t)
	session, err := h1.NewSession(ctx, persistence, harness.SessionOptions{Model: &selected}, conversationScenarioState{Counts: map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	path := session.Location()
	runTypedStateTurn(t, session, "first")
	runTypedStateTurn(t, session, "second")
	wantAfterTwo := conversationScenarioState{
		Turn: 2, Notes: []string{"first", "second"}, Counts: map[string]int{"first": 1, "second": 1},
		Last: &struct {
			Number int    `json:"number"`
			Note   string `json:"note"`
		}{Number: 2, Note: "second"},
	}
	if got := session.State(); !reflect.DeepEqual(got, wantAfterTwo) {
		t.Fatalf("state after two turns:\n got=%#v\nwant=%#v", got, wantAfterTwo)
	}
	messagesAfterTwo := len(session.Conversation().Messages)
	if messagesAfterTwo != 8 {
		t.Fatalf("messages after two turns=%d", messagesAfterTwo)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	// Create a new Harness and Provider to simulate a process restart rather
	// than reopening through the original in-memory objects.
	h2, _ := newConversationHarness(t)
	restored, err := h2.OpenSession(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.State(); !reflect.DeepEqual(got, wantAfterTwo) {
		t.Fatalf("state after first reopen:\n got=%#v\nwant=%#v", got, wantAfterTwo)
	}
	if got := len(restored.Conversation().Messages); got != messagesAfterTwo {
		t.Fatalf("restored messages=%d want=%d", got, messagesAfterTwo)
	}
	thirdRun := runTypedStateTurn(t, restored, "third")
	wantAfterThree := conversationScenarioState{
		Turn: 3, Notes: []string{"first", "second", "third"}, Counts: map[string]int{"first": 1, "second": 1, "third": 1},
		Last: &struct {
			Number int    `json:"number"`
			Note   string `json:"note"`
		}{Number: 3, Note: "third"},
	}
	if got := thirdRun.State(); !reflect.DeepEqual(got, wantAfterThree) {
		t.Fatalf("third run state:\n got=%#v\nwant=%#v", got, wantAfterThree)
	}
	if got := len(restored.Conversation().Messages); got != 12 {
		t.Fatalf("messages after continued turn=%d", got)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}

	h3, _ := newConversationHarness(t)
	reopenedAgain, err := h3.OpenSession(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedAgain.Close()
	if got := reopenedAgain.State(); !reflect.DeepEqual(got, wantAfterThree) {
		t.Fatalf("state after second reopen:\n got=%#v\nwant=%#v", got, wantAfterThree)
	}
	if got := len(reopenedAgain.Conversation().Messages); got != 12 {
		t.Fatalf("messages after second reopen=%d", got)
	}
}
