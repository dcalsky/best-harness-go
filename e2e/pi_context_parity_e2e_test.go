package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/dcalsky/best-harness-go"
)

type parityContent struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	Signature string         `json:"thinkingSignature,omitempty"`
	Data      string         `json:"data,omitempty"`
	MimeType  string         `json:"mimeType,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type parityUsage struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheWrite  int64 `json:"cacheWrite"`
	TotalTokens int64 `json:"totalTokens"`
}

type parityResponse struct {
	Content      []parityContent    `json:"content"`
	StopReason   harness.StopReason `json:"stopReason"`
	ErrorMessage string             `json:"errorMessage,omitempty"`
	Usage        parityUsage        `json:"usage"`
}

type parityTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Result      []parityContent `json:"result"`
	IsError     bool            `json:"isError"`
}

type parityFixture struct {
	Name              string            `json:"name"`
	SystemPrompt      string            `json:"systemPrompt"`
	Prompts           []string          `json:"prompts"`
	Responses         []parityResponse  `json:"responses"`
	Tools             []parityTool      `json:"tools"`
	NormalizeMessages []harness.Message `json:"normalizeMessages,omitempty"`
}

type canonicalContext struct {
	SystemPrompt string           `json:"systemPrompt"`
	Messages     []map[string]any `json:"messages"`
	Tools        []map[string]any `json:"tools"`
}

type parityProvider struct {
	fixture  parityFixture
	requests []harness.Request
	next     int
}

func (p *parityProvider) Stream(_ context.Context, request harness.Request) (harness.Stream, error) {
	p.requests = append(p.requests, request)
	if p.next >= len(p.fixture.Responses) {
		return nil, fmt.Errorf("Go requested unexpected provider turn %d", p.next+1)
	}
	response := p.fixture.Responses[p.next]
	p.next++
	events := make([]harness.StreamEvent, 0, len(response.Content)+1)
	for index, content := range response.Content {
		switch content.Type {
		case "text":
			events = append(events, harness.StreamEvent{Type: harness.EventTextDelta, Text: content.Text})
		case "thinking":
			events = append(events, harness.StreamEvent{Type: harness.EventThinkingDelta, Text: content.Thinking, Signature: content.Signature})
		case "toolCall":
			arguments, err := json.Marshal(content.Arguments)
			if err != nil {
				return nil, err
			}
			events = append(events, harness.StreamEvent{Type: harness.EventToolCallStart, Index: index, ToolCallID: content.ID, ToolName: content.Name, ArgumentsDelta: string(arguments)})
		default:
			return nil, fmt.Errorf("unsupported assistant content type %q", content.Type)
		}
	}
	events = append(events, harness.StreamEvent{
		Type:       harness.EventDone,
		StopReason: response.StopReason,
		Usage: harness.Usage{
			InputTokens:      response.Usage.Input,
			OutputTokens:     response.Usage.Output,
			CacheReadTokens:  response.Usage.CacheRead,
			CacheWriteTokens: response.Usage.CacheWrite,
			TotalTokens:      response.Usage.TotalTokens,
		},
	})
	return &harness.SliceStream{Events: events}, nil
}

func TestPiSDKProviderContextParity(t *testing.T) {
	if os.Getenv("BEST_HARNESS_PI_PARITY_E2E") != "1" {
		t.Skip("set BEST_HARNESS_PI_PARITY_E2E=1 to compare provider contexts with the local pi SDK")
	}
	fixtures := []parityFixture{
		{
			Name:         "multi_turn_text_and_thinking",
			SystemPrompt: "Keep answers deterministic.",
			Prompts:      []string{"first", "second"},
			Responses: []parityResponse{
				{Content: []parityContent{{Type: "text", Text: "one"}}, StopReason: harness.StopStop, Usage: parityUsage{Input: 3, Output: 1, TotalTokens: 4}},
				{Content: []parityContent{{Type: "thinking", Thinking: "plan", Signature: "sig"}, {Type: "text", Text: "two"}}, StopReason: harness.StopStop, Usage: parityUsage{Input: 5, Output: 2, CacheRead: 1, TotalTokens: 8}},
			},
		},
		{
			Name:         "single_tool_round_trip",
			SystemPrompt: "Use tools when requested.",
			Prompts:      []string{"echo alpha"},
			Tools: []parityTool{{
				Name: "echo", Description: "Return the supplied text.", Parameters: parityTextToolSchema(),
				Result: []parityContent{{Type: "text", Text: "alpha"}},
			}},
			Responses: []parityResponse{
				{Content: []parityContent{{Type: "toolCall", ID: "call-1", Name: "echo", Arguments: map[string]any{"text": "alpha"}}}, StopReason: harness.StopToolUse, Usage: parityUsage{Input: 4, Output: 1, TotalTokens: 5}},
				{Content: []parityContent{{Type: "text", Text: "done"}}, StopReason: harness.StopStop, Usage: parityUsage{Input: 7, Output: 1, CacheWrite: 2, TotalTokens: 10}},
			},
		},
		{
			Name:         "parallel_tool_result_order_and_error",
			SystemPrompt: "Run independent tools in order.",
			Prompts:      []string{"echo both"},
			Tools: []parityTool{
				{Name: "echo_a", Description: "Return A.", Parameters: parityTextToolSchema(), Result: []parityContent{{Type: "text", Text: "A"}}},
				{Name: "echo_b", Description: "Return B.", Parameters: parityTextToolSchema(), Result: []parityContent{{Type: "text", Text: "B"}}, IsError: true},
			},
			Responses: []parityResponse{
				{Content: []parityContent{
					{Type: "toolCall", ID: "call-a", Name: "echo_a", Arguments: map[string]any{"text": "A"}},
					{Type: "toolCall", ID: "call-b", Name: "echo_b", Arguments: map[string]any{"text": "B"}},
				}, StopReason: harness.StopToolUse, Usage: parityUsage{Input: 4, Output: 2, TotalTokens: 6}},
				{Content: []parityContent{{Type: "text", Text: "complete"}}, StopReason: harness.StopStop, Usage: parityUsage{Input: 8, Output: 1, TotalTokens: 9}},
			},
		},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			piContexts := runPiContextFixture(t, fixture)
			goContexts := runGoContextFixture(t, fixture)
			compareContexts(t, goContexts, piContexts)
		})
	}

	t.Run("provider_message_normalization", func(t *testing.T) {
		usage := harness.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
		history := []harness.Message{
			harness.User("start"),
			{Role: harness.RoleAssistant, Provider: "parity", Model: "mock", StopReason: harness.StopError, ErrorMessage: "failed", Usage: usage, Content: []harness.Content{harness.Text("partial")}},
			{Role: harness.RoleAssistant, Provider: "parity", Model: "mock", StopReason: harness.StopToolUse, Usage: usage, Content: []harness.Content{
				harness.NewToolCallContent("call-a", "echo_a", json.RawMessage(`{"text":"A"}`)),
				harness.NewToolCallContent("call-b", "echo_b", json.RawMessage(`{"text":"B"}`)),
			}},
			{Role: harness.RoleTool, ToolCallID: "call-b", ToolName: "echo_b", Content: []harness.Content{harness.Text("B")}},
			harness.User("continue"),
			{Role: harness.RoleAssistant, Provider: "parity", Model: "mock", StopReason: harness.StopToolUse, Usage: usage, Content: []harness.Content{
				harness.NewToolCallContent("call-c", "echo_c", json.RawMessage(`{"text":"C"}`)),
			}},
		}
		piContexts := runPiContextFixture(t, parityFixture{SystemPrompt: "normalize", NormalizeMessages: history})
		goContexts := []canonicalContext{canonicalizeGoContext(t, harness.Request{SystemPrompt: "normalize", Messages: harness.NormalizeMessagesForProvider(history)})}
		encoded, err := json.Marshal(goContexts)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(encoded, &goContexts); err != nil {
			t.Fatal(err)
		}
		compareContexts(t, goContexts, piContexts)
	})
}

func runGoContextFixture(t *testing.T, fixture parityFixture) []canonicalContext {
	t.Helper()
	p := &parityProvider{fixture: fixture}
	registry := harness.NewToolRegistry()
	for _, definition := range fixture.Tools {
		definition := definition
		err := registry.Register(harness.Tool[map[string]string, struct{}]{
			Name: definition.Name, Description: definition.Description, RawParameters: json.RawMessage(definition.Parameters),
			Execute: func(_ context.Context, _ harness.ToolCall, _ map[string]string, _ harness.Update[struct{}]) (harness.ToolResult[struct{}], error) {
				content := make([]harness.Content, len(definition.Result))
				for i, item := range definition.Result {
					content[i] = fixtureMessageContent(item)
				}
				return harness.ToolResult[struct{}]{Content: content, IsError: definition.IsError}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	a := harness.NewAgent(harness.AgentOptions{
		Provider:     p,
		Model:        harness.Model{Provider: "parity", ID: "mock", Name: "mock", ContextWindow: 8192, MaxOutput: 2048},
		SystemPrompt: fixture.SystemPrompt,
		Tools:        registry,
	})
	for _, text := range fixture.Prompts {
		r, err := a.Start(context.Background(), harness.AgentPrompt{Steps: harness.Sequence{harness.UserText(text)}}, harness.AgentStartOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if p.next != len(fixture.Responses) {
		t.Fatalf("Go consumed %d responses, fixture contains %d", p.next, len(fixture.Responses))
	}
	contexts := make([]canonicalContext, len(p.requests))
	for i, request := range p.requests {
		contexts[i] = canonicalizeGoContext(t, request)
	}
	// Round-trip through JSON so language-specific numeric representations do
	// not make semantically identical contexts compare unequal.
	encoded, err := json.Marshal(contexts)
	if err != nil {
		t.Fatal(err)
	}
	var normalized []canonicalContext
	if err = json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	return normalized
}

func parityTextToolSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}}`)
}

func runPiContextFixture(t *testing.T, fixture parityFixture) []canonicalContext {
	t.Helper()
	piRoot := os.Getenv("PI_REPO")
	if piRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		piRoot = filepath.Join(home, "www", "pi")
	}
	tsx := filepath.Join(piRoot, "node_modules", ".bin", "tsx")
	if _, err := os.Stat(tsx); err != nil {
		t.Fatalf("pi SDK runner not found at %s (set PI_REPO to the pi checkout with npm dependencies installed): %v", tsx, err)
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve parity runner path")
	}
	runner := filepath.Join(filepath.Dir(currentFile), "testdata", "pi_context_runner.ts")
	input, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(tsx, runner)
	cmd.Dir = piRoot
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run pi SDK fixture: %v\n%s", err, output)
	}
	var contexts []canonicalContext
	if err = json.Unmarshal(output, &contexts); err != nil {
		t.Fatalf("decode pi contexts: %v\n%s", err, output)
	}
	return contexts
}

func canonicalizeGoContext(t *testing.T, request harness.Request) canonicalContext {
	t.Helper()
	canonical := canonicalContext{SystemPrompt: request.SystemPrompt, Messages: make([]map[string]any, len(request.Messages)), Tools: make([]map[string]any, len(request.Tools))}
	for i, msg := range request.Messages {
		content := make([]map[string]any, len(msg.Content))
		for j, item := range msg.Content {
			content[j] = canonicalizeGoContent(t, item)
		}
		entry := map[string]any{"role": string(msg.Role), "content": content}
		if msg.Role == harness.RoleAssistant {
			entry["provider"] = msg.Provider
			entry["model"] = msg.Model
			entry["stopReason"] = string(msg.StopReason)
			if msg.ErrorMessage != "" {
				entry["errorMessage"] = msg.ErrorMessage
			}
			entry["usage"] = map[string]any{
				"input": msg.Usage.InputTokens, "output": msg.Usage.OutputTokens,
				"cacheRead": msg.Usage.CacheReadTokens, "cacheWrite": msg.Usage.CacheWriteTokens,
				"totalTokens": msg.Usage.TotalTokens,
			}
		}
		if msg.Role == harness.RoleTool {
			entry["toolCallId"] = msg.ToolCallID
			entry["toolName"] = msg.ToolName
			entry["isError"] = msg.IsError
		}
		canonical.Messages[i] = entry
	}
	for i, definition := range request.Tools {
		var parameters any
		if err := json.Unmarshal(definition.Parameters, &parameters); err != nil {
			t.Fatal(err)
		}
		canonical.Tools[i] = map[string]any{"name": definition.Name, "description": definition.Description, "parameters": parameters}
	}
	return canonical
}

func canonicalizeGoContent(t *testing.T, content harness.Content) map[string]any {
	t.Helper()
	canonical := map[string]any{"type": content.Type}
	switch content.Type {
	case "text":
		canonical["text"] = content.Text
	case "thinking":
		canonical["thinking"] = content.Thinking
		if content.Signature != "" {
			canonical["thinkingSignature"] = content.Signature
		}
	case "image":
		canonical["data"] = content.Data
		canonical["mimeType"] = content.MimeType
	case "toolCall":
		canonical["id"] = content.ID
		canonical["name"] = content.Name
		var arguments any
		if err := json.Unmarshal(content.Arguments, &arguments); err != nil {
			t.Fatal(err)
		}
		canonical["arguments"] = arguments
	default:
		t.Fatalf("unsupported Go context content type %q", content.Type)
	}
	return canonical
}

func fixtureMessageContent(content parityContent) harness.Content {
	switch content.Type {
	case "text":
		return harness.Text(content.Text)
	case "image":
		return harness.Image(content.Data, content.MimeType)
	default:
		panic("unsupported tool result content type " + content.Type)
	}
}

func compareContexts(t *testing.T, got, want []canonicalContext) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("provider context count: Go=%d pi=%d", len(got), len(want))
	}
	for i := range want {
		if got[i].SystemPrompt != want[i].SystemPrompt {
			t.Errorf("context %d system prompt: Go=%q pi=%q", i, got[i].SystemPrompt, want[i].SystemPrompt)
		}
		if !reflect.DeepEqual(got[i].Tools, want[i].Tools) {
			t.Errorf("context %d tools differ:\nGo: %s\npi: %s", i, prettyJSON(got[i].Tools), prettyJSON(want[i].Tools))
		}
		if len(got[i].Messages) != len(want[i].Messages) {
			t.Errorf("context %d message count: Go=%d pi=%d\nGo: %s\npi: %s", i, len(got[i].Messages), len(want[i].Messages), prettyJSON(got[i].Messages), prettyJSON(want[i].Messages))
			continue
		}
		for j := range want[i].Messages {
			if !reflect.DeepEqual(got[i].Messages[j], want[i].Messages[j]) {
				t.Errorf("context %d message %d differs:\nGo: %s\npi: %s", i, j, prettyJSON(got[i].Messages[j]), prettyJSON(want[i].Messages[j]))
			}
		}
	}
}

func prettyJSON(value any) string {
	data, _ := json.MarshalIndent(value, "", "  ")
	return string(data)
}
