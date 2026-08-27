package agui

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/agent"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/protocol"
	sharedrun "github.com/dcalsky/best-harness-go/internal/run"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

func TestAdapterEncodesStreamingRun(t *testing.T) {
	runID := sharedrun.ID("run-1")
	nextID := 0
	adapter := NewAdapter(Options{
		ThreadID: "thread-1",
		GenerateID: func(prefix string) string {
			nextID++
			return prefix + "-" + string(rune('0'+nextID))
		},
		MapEvent: func(event protocol.AgentEvent) ([]Event, error) {
			if event.Event.Type != agent.EventToolEnd {
				return nil, nil
			}
			return []Event{Custom("chart.created", map[string]any{"id": "chart-1"})}, nil
		},
	})

	started, err := adapter.Start(protocol.RunEvent{RunID: runID, Status: sharedrun.StatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if got := eventKinds(started); !reflect.DeepEqual(got, []EventType{EventRunStarted}) {
		t.Fatalf("start kinds=%v", got)
	}
	runStarted := started[0].(RunStartedEvent)
	if runStarted.ThreadID != "thread-1" || runStarted.RunID != "run-1" {
		t.Fatalf("run started=%#v", runStarted)
	}

	call := tool.ToolCall{ID: "call-1", Name: "weather", Arguments: json.RawMessage(`{"city":"Paris"}`)}
	assistant := message.Message{
		Role: message.RoleAssistant, Content: []message.Content{
			message.Thinking("checking"), message.Text("sunny"),
			message.ToolCall(call.ID, call.Name, call.Arguments),
		},
		Provider: "openai", Model: "gpt-test", StopReason: message.StopToolUse,
		Usage: message.Usage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14, CacheReadTokens: 3},
	}
	result := tool.Result{Content: []message.Content{message.Text("24 C")}}
	events := []agent.Event{
		{Type: agent.EventTurnStart},
		{Type: agent.EventMessageStart, Message: &message.Message{Role: message.RoleAssistant}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventThinkingDelta, Text: "check"}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventTextDelta, Text: "sunny"}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventToolCallStart, Index: 0, ToolCallID: call.ID, ToolName: call.Name}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventToolCallDelta, Index: 0, ArgumentsDelta: `{"city":"Paris"}`}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventToolCallEnd, Index: 0}},
		{Type: agent.EventMessageEnd, Message: &assistant},
		{Type: agent.EventToolStart, Call: &call},
		{Type: agent.EventToolEnd, Call: &call, Result: &result},
		{Type: agent.EventTurnEnd},
	}
	var frames []Event
	for _, event := range events {
		encoded, encodeErr := adapter.Encode(protocol.AgentEvent{RunID: runID, Event: event})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		frames = append(frames, encoded...)
	}
	finished, err := adapter.Finish(protocol.RunEvent{RunID: runID, Status: sharedrun.StatusCompleted})
	if err != nil {
		t.Fatal(err)
	}
	frames = append(frames, finished...)

	want := []EventType{
		EventStepStarted, EventTextMessageStart,
		EventReasoningStart, EventReasoningMessageStart, EventReasoningMessageContent,
		EventReasoningMessageEnd, EventReasoningEnd, EventTextMessageContent,
		EventToolCallStart, EventToolCallArgs, EventToolCallEnd, EventTextMessageEnd,
		EventToolCallResult, EventCustom, EventStepFinished, EventRunFinished,
	}
	if got := eventKinds(frames); !reflect.DeepEqual(got, want) {
		t.Fatalf("event kinds=%v\nwant=%v", got, want)
	}
	toolStart := frames[8].(ToolCallStartEvent)
	if toolStart.ToolCallID != call.ID || toolStart.ToolCallName != call.Name || toolStart.ParentMessageID == "" {
		t.Fatalf("tool start=%#v", toolStart)
	}
	if got := frames[9].(ToolCallArgsEvent).Delta; got != `{"city":"Paris"}` {
		t.Fatalf("tool args=%q", got)
	}
	toolResult := frames[12].(ToolCallResultEvent)
	if toolResult.ToolCallID != call.ID || toolResult.Content != "24 C" || toolResult.Role != "tool" {
		t.Fatalf("tool result=%#v", toolResult)
	}
	runFinished := frames[len(frames)-1].(RunFinishedEvent)
	if runFinished.Outcome == nil || runFinished.Outcome.Type != "success" || len(runFinished.Usage) != 1 {
		t.Fatalf("run finished=%#v", runFinished)
	}
	if got := runFinished.Usage[0]; got.InputTokens != 10 || got.OutputTokens != 4 || got.TotalTokens != 14 || got.CachedInputTokens != 3 {
		t.Fatalf("usage=%#v", got)
	}
}

func TestAdapterSynthesizesNonStreamingMessageAndTool(t *testing.T) {
	runID := sharedrun.ID("scripted-run")
	adapter := NewAdapter(Options{ThreadID: "thread", GenerateID: sequenceIDs()})
	if _, err := adapter.Start(protocol.RunEvent{RunID: runID}); err != nil {
		t.Fatal(err)
	}
	call := tool.ToolCall{ID: "call-script", Name: "lookup", Arguments: json.RawMessage(`{"q":"go"}`)}
	assistantMessage := message.Message{Role: message.RoleAssistant, Content: []message.Content{
		message.Thinking("brief reasoning"),
		message.Text("I will look it up."),
		message.ToolCall(call.ID, call.Name, call.Arguments),
	}}
	var frames []Event
	for _, event := range []agent.Event{
		{Type: agent.EventTurnStart},
		{Type: agent.EventMessageStart, Message: &message.Message{Role: message.RoleAssistant}},
		{Type: agent.EventMessageEnd, Message: &assistantMessage},
		{Type: agent.EventToolStart, Call: &call},
		{Type: agent.EventToolEnd, Call: &call, Result: &tool.Result{Content: []message.Content{message.Text("found")}}},
		{Type: agent.EventTurnEnd},
	} {
		got, err := adapter.Encode(protocol.AgentEvent{RunID: runID, Event: event})
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, got...)
	}
	want := []EventType{
		EventStepStarted, EventTextMessageStart,
		EventReasoningStart, EventReasoningMessageStart, EventReasoningMessageContent, EventReasoningMessageEnd, EventReasoningEnd,
		EventTextMessageContent, EventToolCallStart, EventToolCallArgs, EventToolCallEnd, EventTextMessageEnd,
		EventToolCallResult, EventStepFinished,
	}
	if got := eventKinds(frames); !reflect.DeepEqual(got, want) {
		t.Fatalf("event kinds=%v\nwant=%v", got, want)
	}
}

func TestAdapterKeepsParallelToolLifecyclesIndependent(t *testing.T) {
	runID := sharedrun.ID("parallel-run")
	adapter := NewAdapter(Options{ThreadID: "thread", GenerateID: sequenceIDs()})
	if _, err := adapter.Start(protocol.RunEvent{RunID: runID}); err != nil {
		t.Fatal(err)
	}
	assistantMessage := message.Message{Role: message.RoleAssistant, Content: []message.Content{
		message.ToolCall("call-a", "search", json.RawMessage(`{"q":"a"}`)),
		message.ToolCall("call-b", "search", json.RawMessage(`{"q":"b"}`)),
	}}
	calls := []tool.ToolCall{
		{ID: "call-a", Name: "search", Arguments: json.RawMessage(`{"q":"a"}`)},
		{ID: "call-b", Name: "search", Arguments: json.RawMessage(`{"q":"b"}`)},
	}
	events := []agent.Event{
		{Type: agent.EventTurnStart},
		{Type: agent.EventMessageStart, Message: &message.Message{Role: message.RoleAssistant}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventToolCallStart, Index: 0, ToolCallID: "call-a", ToolName: "search"}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventToolCallDelta, Index: 0, ArgumentsDelta: `{"q":"a"}`}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventToolCallStart, Index: 1, ToolCallID: "call-b", ToolName: "search"}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventToolCallDelta, Index: 1, ArgumentsDelta: `{"q":"b"}`}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventToolCallEnd, Index: 0}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventToolCallEnd, Index: 1}},
		{Type: agent.EventMessageEnd, Message: &assistantMessage},
		{Type: agent.EventToolStart, Call: &calls[0]},
		{Type: agent.EventToolStart, Call: &calls[1]},
		// Parallel execution may finish in a different order than calls started.
		{Type: agent.EventToolEnd, Call: &calls[1], Result: &tool.Result{Content: []message.Content{message.Text("b")}}},
		{Type: agent.EventToolEnd, Call: &calls[0], Result: &tool.Result{Content: []message.Content{message.Text("a")}}},
		{Type: agent.EventTurnEnd},
	}
	var frames []Event
	for _, event := range events {
		got, err := adapter.Encode(protocol.AgentEvent{RunID: runID, Event: event})
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, got...)
	}

	var starts, ends, results []string
	for _, frame := range frames {
		switch event := frame.(type) {
		case ToolCallStartEvent:
			starts = append(starts, event.ToolCallID)
		case ToolCallEndEvent:
			ends = append(ends, event.ToolCallID)
		case ToolCallResultEvent:
			results = append(results, event.ToolCallID)
		}
	}
	if !reflect.DeepEqual(starts, []string{"call-a", "call-b"}) || !reflect.DeepEqual(ends, []string{"call-a", "call-b"}) {
		t.Fatalf("starts=%v ends=%v", starts, ends)
	}
	if !reflect.DeepEqual(results, []string{"call-b", "call-a"}) {
		t.Fatalf("results=%v", results)
	}
}

func TestAdapterEmitsSingleTerminalRunError(t *testing.T) {
	runID := sharedrun.ID("failed-run")
	adapter := NewAdapter(Options{ThreadID: "thread"})
	if _, err := adapter.Start(protocol.RunEvent{RunID: runID}); err != nil {
		t.Fatal(err)
	}
	frames, err := adapter.Encode(protocol.AgentEvent{
		RunID: runID, Event: agent.Event{Type: agent.EventError, Err: errors.New("provider failed")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 0 {
		t.Fatalf("error should be deferred to terminal boundary: %#v", frames)
	}
	frames, err = adapter.Finish(protocol.RunEvent{RunID: runID, Status: sharedrun.StatusFailed, Cause: sharedrun.CauseInternal})
	if err != nil {
		t.Fatal(err)
	}
	if got := eventKinds(frames); !reflect.DeepEqual(got, []EventType{EventRunError}) {
		t.Fatalf("terminal kinds=%v", got)
	}
	runError := frames[0].(RunErrorEvent)
	if runError.Message != "provider failed" || runError.Code != string(sharedrun.CauseInternal) {
		t.Fatalf("run error=%#v", runError)
	}
	if _, err := adapter.Finish(protocol.RunEvent{RunID: runID}); err == nil {
		t.Fatal("second finish succeeded")
	}
}

func TestAdapterValidatesLifecycleIdentity(t *testing.T) {
	if _, err := NewAdapter(Options{}).Start(protocol.RunEvent{RunID: "run"}); err == nil || !strings.Contains(err.Error(), "thread ID") {
		t.Fatalf("missing thread error=%v", err)
	}
	adapter := NewAdapter(Options{ThreadID: "thread"})
	if _, err := adapter.Start(protocol.RunEvent{}); err == nil || !strings.Contains(err.Error(), "run ID") {
		t.Fatalf("missing run error=%v", err)
	}
	adapter = NewAdapter(Options{ThreadID: "thread"})
	if _, err := adapter.Start(protocol.RunEvent{RunID: "run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Encode(protocol.AgentEvent{RunID: "other"}); err == nil || !strings.Contains(err.Error(), "run mismatch") {
		t.Fatalf("mismatch error=%v", err)
	}
}

func TestSSEEncoderAndHeaders(t *testing.T) {
	var output bytes.Buffer
	encoder := NewSSEEncoder(&output)
	event := TextMessageContentEvent{
		BaseEvent: BaseEvent{Type: EventTextMessageContent}, MessageID: "message-1", Delta: "hello\nworld",
	}
	if err := encoder.Encode(event); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(event); err == nil {
		t.Fatal("encode after close succeeded")
	}
	want := "data: {\"type\":\"TEXT_MESSAGE_CONTENT\",\"messageId\":\"message-1\",\"delta\":\"hello\\nworld\"}\n\n"
	if got := output.String(); got != want || strings.Contains(got, "[DONE]") {
		t.Fatalf("SSE output=%q", got)
	}

	header := make(http.Header)
	SetHeaders(header)
	if header.Get("Content-Type") != "text/event-stream" || header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers=%v", header)
	}
}

func TestExtensionEventsMarshalProtocolShapes(t *testing.T) {
	events := []Event{
		Custom("progress", map[string]any{"percent": 50}),
		StateSnapshot(map[string]any{"count": 1}),
		StateDelta(JSONPatchOperation{Op: "replace", Path: "/count", Value: 2}),
	}
	var got []map[string]any
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		got = append(got, value)
	}
	if got[0]["type"] != string(EventCustom) || got[0]["name"] != "progress" {
		t.Fatalf("custom=%v", got[0])
	}
	if got[1]["type"] != string(EventStateSnapshot) || got[2]["type"] != string(EventStateDelta) {
		t.Fatalf("state events=%v", got[1:])
	}
}

func eventKinds(events []Event) []EventType {
	kinds := make([]EventType, len(events))
	for i, event := range events {
		kinds[i] = event.Kind()
	}
	return kinds
}

func sequenceIDs() func(string) string {
	next := 0
	return func(prefix string) string {
		next++
		return prefix + "-" + string(rune('0'+next))
	}
}
