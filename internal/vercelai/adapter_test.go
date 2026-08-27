package vercelai

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/agent"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/protocol"
	sharedrun "github.com/dcalsky/best-harness-go/internal/run"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

func TestAdapterEncodesTextToolAndCustomData(t *testing.T) {
	runID := sharedrun.ID("run-1")
	nextID := 0
	adapter := NewAdapter(Options{
		GenerateID: func(prefix string) string {
			nextID++
			return prefix + "-" + string(rune('0'+nextID))
		},
		MapEvent: func(event protocol.AgentEvent) ([]Chunk, error) {
			if event.Event.Type != agent.EventToolEnd {
				return nil, nil
			}
			chunk, err := Data("a2ui", map[string]any{"chart": "created"}, true)
			return []Chunk{chunk}, err
		},
	})

	start, err := adapter.Start(protocol.RunEvent{RunID: runID, Status: sharedrun.StatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	if len(start) != 1 || start[0].Type != "start" || start[0].MessageID == "" {
		t.Fatalf("start=%#v", start)
	}

	call := tool.ToolCall{ID: "call-1", Name: "render_chart", Arguments: []byte(`{"title":"Sales"}`)}
	result := tool.Result{Content: []message.Content{message.Text("created")}, Details: map[string]any{"id": "chart-1"}}
	events := []agent.Event{
		{Type: agent.EventTurnStart},
		{Type: agent.EventMessageStart, Message: &message.Message{Role: message.RoleAssistant}},
		{Type: agent.EventMessageUpdate, Stream: &message.StreamEvent{Type: message.EventTextDelta, Text: "hello"}},
		{Type: agent.EventMessageEnd, Message: &message.Message{Role: message.RoleAssistant, StopReason: message.StopToolUse}},
		{Type: agent.EventToolStart, Call: &call},
		{Type: agent.EventToolEnd, Call: &call, Result: &result},
		{Type: agent.EventTurnEnd},
	}
	var chunks []Chunk
	for _, event := range events {
		encoded, err := adapter.Encode(protocol.AgentEvent{RunID: runID, Event: event})
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, encoded...)
	}
	finish, err := adapter.Finish(protocol.RunEvent{RunID: runID, Status: sharedrun.StatusCompleted})
	if err != nil {
		t.Fatal(err)
	}
	chunks = append(chunks, finish...)

	wantTypes := []string{
		"start-step", "text-start", "text-delta", "text-end",
		"tool-input-available", "tool-output-available", "data-a2ui",
		"finish-step", "finish",
	}
	if len(chunks) != len(wantTypes) {
		t.Fatalf("chunk types=%v", chunkTypes(chunks))
	}
	for i, want := range wantTypes {
		if chunks[i].Type != want {
			t.Fatalf("chunk[%d].type=%q want %q; all=%v", i, chunks[i].Type, want, chunkTypes(chunks))
		}
	}
	if got := string(chunks[4].Input); got != `{"title":"Sales"}` {
		t.Fatalf("tool input=%s", got)
	}
	if !chunks[4].Dynamic || !chunks[4].ProviderExecuted {
		t.Fatalf("tool input flags=%#v", chunks[4])
	}
	if !chunks[6].Transient {
		t.Fatalf("data chunk=%#v", chunks[6])
	}
	if chunks[len(chunks)-1].FinishReason != "tool-calls" {
		t.Fatalf("finish=%#v", chunks[len(chunks)-1])
	}
}

func TestAdapterEncodesTerminalFailureOnce(t *testing.T) {
	runID := sharedrun.ID("run-failed")
	adapter := NewAdapter(Options{})
	if _, err := adapter.Start(protocol.RunEvent{RunID: runID}); err != nil {
		t.Fatal(err)
	}
	frames, err := adapter.Encode(protocol.AgentEvent{RunID: runID, Event: agent.Event{Type: agent.EventError, Err: errors.New("provider failed")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].Type != "error" {
		t.Fatalf("error frames=%#v", frames)
	}
	frames, err = adapter.Finish(protocol.RunEvent{RunID: runID, Status: sharedrun.StatusFailed, Err: errors.New("provider failed")})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || frames[0].Type != "finish" || frames[0].FinishReason != "error" {
		t.Fatalf("finish frames=%#v", frames)
	}
}

func TestSSEEncoderAndHeaders(t *testing.T) {
	var output bytes.Buffer
	encoder := NewSSEEncoder(&output)
	if err := encoder.Encode(Chunk{Type: "text-delta", ID: "text-1", Delta: "hello\nworld"}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(Chunk{Type: "finish"}); err == nil {
		t.Fatal("encode after close succeeded")
	}
	want := "data: {\"type\":\"text-delta\",\"id\":\"text-1\",\"delta\":\"hello\\nworld\"}\n\ndata: [DONE]\n\n"
	if got := output.String(); !strings.Contains(got, want) {
		t.Fatalf("SSE output=%q", got)
	}

	header := make(http.Header)
	SetHeaders(header)
	if header.Get("Content-Type") != "text/event-stream" || header.Get("X-Vercel-Ai-Ui-Message-Stream") != "v1" {
		t.Fatalf("headers=%v", header)
	}
}

func TestDataRejectsInvalidName(t *testing.T) {
	if _, err := Data("a2ui message", map[string]any{}, true); err == nil {
		t.Fatal("invalid data part name was accepted")
	}
}

func chunkTypes(chunks []Chunk) []string {
	types := make([]string, len(chunks))
	for i := range chunks {
		types[i] = chunks[i].Type
	}
	return types
}
