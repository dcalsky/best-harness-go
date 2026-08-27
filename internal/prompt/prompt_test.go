package prompt_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/prompt"
)

func TestSequenceNormalizeDefaultsClonesAndRoundTrips(t *testing.T) {
	arguments := json.RawMessage(`{"path":"go.mod"}`)
	sequence := prompt.Sequence{
		prompt.UserText("inspect"),
		&prompt.AssistantMessageStep{Content: []message.Content{message.Text("reading")}},
		prompt.Tools(prompt.ToolCall{Key: "read", Name: "read_file", Arguments: arguments}),
	}
	normalized, err := sequence.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	call := normalized[2].(prompt.ToolCallsStep).Calls[0]
	if call.OnError != prompt.OnErrorEnterAgentLoop {
		t.Fatalf("default on error=%q", call.OnError)
	}
	arguments[9] = 'X'
	if got := string(call.Arguments); got != `{"path":"go.mod"}` {
		t.Fatalf("arguments were not cloned: %s", got)
	}

	data, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"assistant_message"`) {
		t.Fatalf("encoded sequence=%s", data)
	}
	var decoded prompt.Sequence
	if err = json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 3 || decoded[0].(prompt.UserMessageStep).Content[0].Text != "inspect" {
		t.Fatalf("decoded=%#v", decoded)
	}
}

func TestSequenceValidation(t *testing.T) {
	tests := []struct {
		name string
		seq  prompt.Sequence
		want string
	}{
		{"assistant last", prompt.Sequence{prompt.UserText("x"), prompt.AssistantText("y")}, "cannot end"},
		{"assistant image", prompt.Sequence{prompt.AssistantMessageStep{Content: []message.Content{message.Image("x", "image/png")}}, prompt.UserText("x")}, "not allowed"},
		{"user thinking", prompt.Sequence{prompt.UserMessageStep{Content: []message.Content{message.Thinking("x")}}}, "not allowed"},
		{"empty tools", prompt.Sequence{prompt.ToolCallsStep{}}, "tool calls are required"},
		{"missing name", prompt.Sequence{prompt.Tools(prompt.ToolCall{})}, "name is required"},
		{"invalid json", prompt.Sequence{prompt.Tools(prompt.ToolCall{Name: "x", Arguments: json.RawMessage(`{`)})}, "invalid JSON"},
		{"invalid policy", prompt.Sequence{prompt.Tools(prompt.ToolCall{Name: "x", OnError: "maybe"})}, "invalid on-error"},
		{"duplicate key", prompt.Sequence{prompt.Tools(prompt.ToolCall{Key: "x", Name: "a"}), prompt.Tools(prompt.ToolCall{Key: "x", Name: "b"})}, "duplicated"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.seq.Normalize()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want substring %q", err, tc.want)
			}
		})
	}
}

func TestSequenceUnmarshalRejectsUnknownStep(t *testing.T) {
	var sequence prompt.Sequence
	err := json.Unmarshal([]byte(`[{"type":"mystery"}]`), &sequence)
	if err == nil || !strings.Contains(err.Error(), "invalid type") {
		t.Fatalf("error=%v", err)
	}
}
