package e2e_test

import (
	"fmt"
	"testing"

	"github.com/dcalsky/best-harness-go"
)

func assertContextRoles(t *testing.T, request harness.Request, want ...harness.Role) {
	t.Helper()
	got := make([]harness.Role, len(request.Messages))
	for i, msg := range request.Messages {
		got[i] = msg.Role
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("provider context roles=%v want=%v messages=%#v", got, want, request.Messages)
	}
	assertToolContextIntegrity(t, request.Messages)
}

// pi-ai normalizes every provider context so tool results refer to a preceding
// tool call and every call is resolved before the next user turn.
func assertToolContextIntegrity(t *testing.T, messages []harness.Message) {
	t.Helper()
	pending := make(map[string]string)
	for _, msg := range messages {
		if msg.Role == harness.RoleUser && len(pending) != 0 {
			t.Fatalf("user message encountered with unresolved tool calls: %v", pending)
		}
		if msg.Role == harness.RoleAssistant {
			if msg.StopReason == harness.StopError || msg.StopReason == harness.StopAborted {
				t.Fatalf("pi excludes %s assistant messages from provider context", msg.StopReason)
			}
			for _, content := range msg.Content {
				if content.Type == "toolCall" {
					pending[content.ID] = content.Name
				}
			}
		}
		if msg.Role == harness.RoleTool {
			name, ok := pending[msg.ToolCallID]
			if !ok || (msg.ToolName != "" && name != msg.ToolName) {
				t.Fatalf("orphan or mismatched tool result id=%q name=%q pending=%v", msg.ToolCallID, msg.ToolName, pending)
			}
			delete(pending, msg.ToolCallID)
		}
	}
	if len(pending) != 0 {
		t.Fatalf("provider context has unresolved tool calls: %v", pending)
	}
}
