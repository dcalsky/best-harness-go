package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

type providerRecorder struct {
	base     harness.Provider
	calls    atomic.Int32
	mu       sync.Mutex
	requests []harness.Request
}

func (p *providerRecorder) Stream(ctx context.Context, request harness.Request) (harness.Stream, error) {
	p.calls.Add(1)
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	return p.base.Stream(ctx, request)
}

func (p *providerRecorder) snapshot() []harness.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]harness.Request(nil), p.requests...)
}

type delayedParams struct {
	Value int `json:"value"`
}
type delayedDetails struct {
	Value  int `json:"value"`
	Update int `json:"update,omitempty"`
}

func updateMax(target *atomic.Int32, value int32) {
	for {
		current := target.Load()
		if current >= value || target.CompareAndSwap(current, value) {
			return
		}
	}
}

func runMultipleToolCalls(t *testing.T, mode harness.ToolExecutionMode) {
	t.Helper()
	p, selected := deepSeek(t)
	recorded := &providerRecorder{base: p}
	tools := harness.NewToolRegistry()
	var active atomic.Int32
	var maximum atomic.Int32
	var calls atomic.Int32
	var updates atomic.Int32
	if err := tools.Register(harness.Tool[delayedParams, delayedDetails]{
		Name:          "record_value",
		Description:   "Record one integer. Each requested integer needs a separate tool call.",
		ExecutionMode: mode,
		Execute: func(ctx context.Context, _ harness.ToolCall, p delayedParams, update harness.Update[delayedDetails]) (harness.ToolResult[delayedDetails], error) {
			calls.Add(1)
			now := active.Add(1)
			updateMax(&maximum, now)
			defer active.Add(-1)
			update(delayedDetails{Value: p.Value, Update: 1})
			updates.Add(1)
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				return harness.ToolResult[delayedDetails]{}, ctx.Err()
			}
			return harness.ToolResult[delayedDetails]{Content: []harness.Content{harness.Text(strconv.Itoa(p.Value))}, Details: delayedDetails{Value: p.Value}}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	generation := harness.GenerationConfig{Thinking: harness.Ptr(false), ParallelToolCalls: harness.Ptr(true)}
	a := harness.NewAgent(harness.AgentOptions{Provider: recorded, Model: selected, Tools: tools, ActiveTools: []string{"record_value"}, Generation: generation})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	requestText := "In one assistant turn, issue exactly three record_value tool calls with values 11, 22, and 33 in that order. Do not wait between calls. After all three results arrive, reply MULTI_TOOL_OK without calling any tool again."
	run := startAgentRun(t, ctx, a, harness.AgentPrompt{Steps: harness.Sequence{harness.UserText(requestText)}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || updates.Load() != 3 {
		t.Fatalf("calls=%d updates=%d messages=%#v", calls.Load(), updates.Load(), a.Messages())
	}
	requests := recorded.snapshot()
	if len(requests) != 2 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	assertContextRoles(t, requests[0], harness.RoleUser)
	assertContextRoles(t, requests[1], harness.RoleUser, harness.RoleAssistant, harness.RoleTool, harness.RoleTool, harness.RoleTool)
	if mode == harness.Parallel && maximum.Load() < 2 {
		t.Fatalf("parallel maximum=%d", maximum.Load())
	}
	if mode == harness.Sequential && maximum.Load() != 1 {
		t.Fatalf("sequential maximum=%d", maximum.Load())
	}
	assertToolResultOrder(t, a.Messages())
}

func assertToolResultOrder(t *testing.T, messages []harness.Message) {
	t.Helper()
	var expected []string
	for _, m := range messages {
		if m.Role != harness.RoleAssistant {
			continue
		}
		for _, content := range m.Content {
			if content.Type != "toolCall" || content.Name != "record_value" {
				continue
			}
			var p delayedParams
			if err := json.Unmarshal(content.Arguments, &p); err != nil {
				t.Fatal(err)
			}
			expected = append(expected, strconv.Itoa(p.Value))
		}
		if len(expected) > 0 {
			break
		}
	}
	var actual []string
	for _, m := range messages {
		if m.Role == harness.RoleTool && m.ToolName == "record_value" {
			actual = append(actual, m.Text())
		}
	}
	if fmt.Sprint(actual) != fmt.Sprint(expected) {
		t.Fatalf("tool result order=%v call order=%v", actual, expected)
	}
}

func TestDeepSeekParallelToolCalls(t *testing.T)   { runMultipleToolCalls(t, harness.Parallel) }
func TestDeepSeekSequentialToolCalls(t *testing.T) { runMultipleToolCalls(t, harness.Sequential) }

type finishParams struct {
	Marker string `json:"marker"`
}

func TestDeepSeekToolTerminationStopsNextModelTurn(t *testing.T) {
	p, selected := deepSeek(t)
	recorded := &providerRecorder{base: p}
	tools := harness.NewToolRegistry()
	if err := tools.Register(harness.Tool[finishParams, struct{}]{
		Name:        "finish_now",
		Description: "Finish the agent run immediately.",
		Execute: func(_ context.Context, _ harness.ToolCall, p finishParams, _ harness.Update[struct{}]) (harness.ToolResult[struct{}], error) {
			return harness.ToolResult[struct{}]{Content: []harness.Content{harness.Text(p.Marker)}, Terminate: true}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	a := harness.NewAgent(harness.AgentOptions{Provider: recorded, Model: selected, Tools: tools, ActiveTools: []string{"finish_now"}, Generation: nonThinking})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	run := startAgentRun(t, ctx, a, harness.AgentPrompt{Steps: harness.Sequence{harness.UserText("Call finish_now exactly once with marker TERMINATED. Do not answer directly.")}})
	if err := run.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if recorded.calls.Load() != 1 {
		t.Fatalf("provider calls=%d", recorded.calls.Load())
	}
	requests := recorded.snapshot()
	if len(requests) != 1 {
		t.Fatalf("provider requests=%d", len(requests))
	}
	assertContextRoles(t, requests[0], harness.RoleUser)
	messages := a.Messages()
	if len(messages) < 3 || messages[len(messages)-1].Role != harness.RoleTool || messages[len(messages)-1].Text() != "TERMINATED" {
		t.Fatalf("messages=%#v", messages)
	}
}
