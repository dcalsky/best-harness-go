package message_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/message"
)

func TestLargeTextUsesDefaultAndCustomLimits(t *testing.T) {
	defaultContent := message.LargeText(strings.Repeat("界", message.DefaultLargeTextMaxChars+1))
	if defaultContent.Type != "largeText" || defaultContent.MaxChars != message.DefaultLargeTextMaxChars {
		t.Fatalf("default content=%#v", defaultContent)
	}
	if !strings.Contains(defaultContent.LLMText(), "text exceeded 4000 chars") {
		t.Fatalf("default rendered text=%q", defaultContent.LLMText())
	}

	custom := message.LargeText("甲乙丙丁戊己", 4)
	want := "甲乙\n\n[truncated: text exceeded 4 chars; kept head and tail from 6 chars]\n\n戊己"
	if got := custom.LLMText(); got != want {
		t.Fatalf("custom rendered text=%q want=%q", got, want)
	}
	if got := message.LargeText("甲乙丙丁", 4).LLMText(); got != "甲乙丙丁" {
		t.Fatalf("untruncated text=%q", got)
	}
}

func TestExpandLargeTextCreatesSeparateUserMessages(t *testing.T) {
	original := message.Message{
		Role: message.RoleUser,
		Content: []message.Content{
			message.Text("compare the files"),
			message.LargeText("first"),
			message.LargeText("甲乙丙丁戊己", 4),
		},
		Timestamp: 123,
	}
	got := message.ExpandLargeText([]message.Message{original})
	if len(got) != 3 {
		t.Fatalf("messages=%#v", got)
	}
	if got[0].Text() != "compare the files" || got[1].Role != message.RoleUser || got[1].Text() != "first" {
		t.Fatalf("messages=%#v", got)
	}
	if got[2].Content[0].Type != "text" || got[2].Content[0].Text != message.LargeText("甲乙丙丁戊己", 4).LLMText() {
		t.Fatalf("large message=%#v", got[2])
	}
	if original.Content[1].Type != "largeText" {
		t.Fatalf("original was mutated: %#v", original)
	}
}

func TestLargeTextWithoutStoredLimitUsesDefault(t *testing.T) {
	c := message.Content{Type: "largeText", Text: strings.Repeat("x", message.DefaultLargeTextMaxChars+1)}
	if !strings.Contains(c.LLMText(), "text exceeded 4000 chars") {
		t.Fatalf("rendered text=%q", c.LLMText())
	}
}

func TestNormalizeForProviderMatchesPiToolFlow(t *testing.T) {
	failed := message.Message{Role: message.RoleAssistant, StopReason: message.StopError, Content: []message.Content{message.Text("partial")}}
	aborted := message.Message{Role: message.RoleAssistant, StopReason: message.StopAborted, Content: []message.Content{message.Text("cancelled")}}
	toolCalls := message.Message{Role: message.RoleAssistant, StopReason: message.StopToolUse, Content: []message.Content{
		message.ToolCall("call-a", "echo_a", json.RawMessage(`{"text":"A"}`)),
		message.ToolCall("call-b", "echo_b", json.RawMessage(`{"text":"B"}`)),
	}}
	toolB := message.Message{Role: message.RoleTool, ToolCallID: "call-b", ToolName: "echo_b", Content: []message.Content{message.Text("B")}}
	trailingCall := message.Message{Role: message.RoleAssistant, StopReason: message.StopToolUse, Content: []message.Content{
		message.ToolCall("call-c", "echo_c", json.RawMessage(`{}`)),
	}}
	input := []message.Message{message.User("start"), failed, aborted, toolCalls, toolB, message.User("continue"), trailingCall}

	got := message.NormalizeForProvider(input)
	if len(input) != 7 || input[1].Text() != "partial" || input[2].Text() != "cancelled" {
		t.Fatalf("input was mutated: %#v", input)
	}
	if len(got) != 7 {
		t.Fatalf("messages=%#v", got)
	}
	wantRoles := []message.Role{message.RoleUser, message.RoleAssistant, message.RoleTool, message.RoleTool, message.RoleUser, message.RoleAssistant, message.RoleTool}
	for i, role := range wantRoles {
		if got[i].Role != role {
			t.Fatalf("message %d role=%q want=%q messages=%#v", i, got[i].Role, role, got)
		}
	}
	if got[2].ToolCallID != "call-b" || got[2].IsError {
		t.Fatalf("existing result=%#v", got[2])
	}
	if got[3].ToolCallID != "call-a" || got[3].ToolName != "echo_a" || !got[3].IsError || got[3].Text() != "No result provided" {
		t.Fatalf("synthetic partial result=%#v", got[3])
	}
	if got[6].ToolCallID != "call-c" || !got[6].IsError || got[6].Text() != "No result provided" {
		t.Fatalf("synthetic trailing result=%#v", got[6])
	}
}

func TestNormalizeForProviderClosesCallsBeforeAssistant(t *testing.T) {
	toolCall := message.Message{Role: message.RoleAssistant, StopReason: message.StopToolUse, Content: []message.Content{
		message.ToolCall("call-1", "echo", json.RawMessage(`{}`)),
	}}
	failed := message.Message{Role: message.RoleAssistant, StopReason: message.StopError, Content: []message.Content{message.Text("partial")}}
	next := message.Message{Role: message.RoleAssistant, StopReason: message.StopStop, Content: []message.Content{message.Text("next")}}

	got := message.NormalizeForProvider([]message.Message{toolCall, failed, next})
	if len(got) != 3 || got[0].Role != message.RoleAssistant || got[1].Role != message.RoleTool || got[1].ToolCallID != "call-1" || got[2].Text() != "next" {
		t.Fatalf("messages=%#v", got)
	}
}

func TestToolCallEmptyArgumentsSurviveRoundTrip(t *testing.T) {
	for _, args := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`null`), nil} {
		raw, err := json.Marshal(message.ToolCall("id", "name", args))
		if err != nil {
			t.Fatal(err)
		}
		var got message.Content
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if string(got.Arguments) != `{}` {
			t.Fatalf("args=%q marshaled=%s roundtrip=%q", string(args), raw, string(got.Arguments))
		}
	}
	raw, err := json.Marshal(message.Text("hi"))
	if err != nil {
		t.Fatal(err)
	}
	var text message.Content
	if err := json.Unmarshal(raw, &text); err != nil {
		t.Fatal(err)
	}
	if len(text.Arguments) != 0 || text.Text != "hi" {
		t.Fatalf("text content=%#v", text)
	}
}
