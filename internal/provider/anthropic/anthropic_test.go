package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"

	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	base "github.com/dcalsky/best-harness-go/internal/provider"
	adapter "github.com/dcalsky/best-harness-go/internal/provider/anthropic"
)

func anthropicClient(server *httptest.Server) anthropicsdk.Client {
	return anthropicsdk.NewClient(
		anthropicoption.WithBaseURL(server.URL),
		anthropicoption.WithAPIKey("secret"),
		anthropicoption.WithMaxRetries(0),
	)
}

func collect(t *testing.T, stream base.Stream) []message.StreamEvent {
	t.Helper()
	defer stream.Close()
	var events []message.StreamEvent
	for {
		event, err := stream.Next()
		if err == io.EOF {
			return events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
}

func TestMessagesRequestThinkingToolAndUsage(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("headers=%v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"served-claude\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0,\"cache_read_input_tokens\":2,\"cache_creation_input_tokens\":1}}}\n\n")
		io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"think\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"opaque\"}}\n\n")
		io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_1\",\"name\":\"lookup\",\"input\":{}}}\n\n")
		io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":2,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\\\"x\\\"}\"}}\n\n")
		io.WriteString(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":2}\n\n")
		io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	stream, err := adapter.New(anthropicClient(server)).Stream(context.Background(), base.Request{
		Model:        model.Model{Provider: "anthropic", API: model.APIAnthropic, ID: "claude", SupportsImages: true, SupportsReasoning: true, InputPrice: 1, OutputPrice: 2},
		SystemPrompt: "system", Messages: []message.Message{
			message.User("question"),
			{Role: message.RoleAssistant, API: model.APIAnthropic, Content: []message.Content{
				message.ToolCall("prior_tool", "lookup", []byte(`{"q":"old"}`)),
			}},
			{Role: message.RoleTool, ToolCallID: "prior_tool", Content: []message.Content{
				message.Text("old result"), message.Image("DDDD", "image/png"),
			}},
		},
		Tools:     []base.Tool{{Name: "lookup", Description: "look up", Parameters: []byte(`{"type":"object","properties":{}}`)}},
		MaxTokens: 4_096, ReasoningEffort: "high",
		Generation: base.GenerationConfig{
			Temperature: base.Ptr(0.3), TopP: base.Ptr(0.95), TopK: base.Ptr(int64(40)),
			ReasoningBudgetTokens: base.Ptr(int64(2_048)), JSONOutput: true,
			ExtraBody: map[string]any{"provider_feature": map[string]any{"mode": "anthropic"}},
			Extra:     map[string]json.RawMessage{"metadata": json.RawMessage(`{"user_id":"u-1"}`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	if len(events) != 8 || events[1].Text != "think" || events[2].Signature != "opaque" || events[3].Text != "hello" {
		t.Fatalf("events=%#v", events)
	}
	if events[4].ToolCallID != "tool_1" || events[4].ToolName != "lookup" || events[5].ArgumentsDelta != `{"q":"x"}` || events[6].Type != message.EventToolCallEnd {
		t.Fatalf("tool events=%#v", events)
	}
	done := events[7]
	if done.StopReason != message.StopToolUse || done.Usage.InputTokens != 10 || done.Usage.OutputTokens != 5 || done.Usage.CacheReadTokens != 2 || done.Usage.CacheWriteTokens != 1 || done.Usage.TotalTokens != 18 {
		t.Fatalf("done=%#v", done)
	}
	if body["model"] != "claude" || body["max_tokens"].(float64) != 4096 || body["system"].([]any)[0].(map[string]any)["text"] != "system" {
		t.Fatalf("body=%#v", body)
	}
	thinking := body["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"].(float64) != 2048 {
		t.Fatalf("thinking=%#v", thinking)
	}
	if body["output_config"].(map[string]any)["effort"] != "high" || body["metadata"].(map[string]any)["user_id"] != "u-1" {
		t.Fatalf("output/extra=%#v", body)
	}
	if body["provider_feature"].(map[string]any)["mode"] != "anthropic" {
		t.Fatalf("provider_feature=%#v", body["provider_feature"])
	}
	messages := body["messages"].([]any)
	toolResults := messages[2].(map[string]any)["content"].([]any)
	toolResultContent := toolResults[0].(map[string]any)["content"].([]any)
	if len(toolResultContent) != 2 || toolResultContent[0].(map[string]any)["type"] != "text" ||
		toolResultContent[1].(map[string]any)["type"] != "image" ||
		toolResultContent[1].(map[string]any)["source"].(map[string]any)["data"] != "DDDD" {
		t.Fatalf("anthropic tool output=%#v", messages[2])
	}
}

func TestAnthropicValidationAndStreamError(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer server.Close()
	p := adapter.New(anthropicClient(server))
	_, err := p.Stream(context.Background(), base.Request{
		Model:      model.Model{API: model.APIAnthropic, ID: "m"},
		Generation: base.GenerationConfig{ReasoningBudgetTokens: base.Ptr(int64(512))},
	})
	if err == nil || !strings.Contains(err.Error(), "at least 1024") || called {
		t.Fatalf("error=%v called=%v", err, called)
	}
	_, err = p.Stream(context.Background(), base.Request{
		Model:      model.Model{API: model.APIAnthropic, ID: "m"},
		Generation: base.GenerationConfig{PreserveThinking: true},
	})
	if err == nil || !strings.Contains(err.Error(), "only supported") || called {
		t.Fatalf("error=%v called=%v", err, called)
	}

	stream, err := p.Stream(context.Background(), base.Request{Model: model.Model{Provider: "anthropic", API: model.APIAnthropic, ID: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	start, err := stream.Next()
	if err != nil || start.Type != message.EventStart {
		t.Fatalf("start=%#v err=%v", start, err)
	}
	event, err := stream.Next()
	if err != nil || event.Type != message.EventError {
		t.Fatalf("event=%#v err=%v", event, err)
	}
	var providerError *message.ProviderError
	if !errors.As(event.Err, &providerError) || providerError.StatusCode != 429 || providerError.Code != "rate_limit_error" || !providerError.Retryable {
		t.Fatalf("provider error=%#v", event.Err)
	}
}

func TestAnthropicHTTPContextOverflowIsClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long for the model context window"}}`)
	}))
	defer server.Close()
	stream, err := adapter.New(anthropicClient(server)).Stream(context.Background(), base.Request{
		Model: model.Model{Provider: "anthropic", API: model.APIAnthropic, ID: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if event, err := stream.Next(); err != nil || event.Type != message.EventStart {
		t.Fatalf("start=%#v err=%v", event, err)
	}
	event, err := stream.Next()
	if err != nil || event.Type != message.EventError || !errors.Is(event.Err, message.ErrContextOverflow) {
		t.Fatalf("event=%#v err=%v", event, err)
	}
}

func TestForeignReasoningSignatureIsNotReplayedAsThinking(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude\",\"content\":[],\"usage\":{}}}\n\n")
		io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{}}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	stream, err := adapter.New(anthropicClient(server)).Stream(context.Background(), base.Request{
		Model: model.Model{Provider: "anthropic", API: model.APIAnthropic, ID: "claude"},
		Messages: []message.Message{
			message.User("question"),
			{Role: message.RoleAssistant, API: model.APIOpenAIResponses, Content: []message.Content{{Type: "thinking", Thinking: "foreign", Signature: `{"type":"reasoning","id":"rs_1"}`}, message.Text("answer")}},
			message.User("continue"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	collect(t, stream)
	messages := body["messages"].([]any)
	assistantContent := messages[1].(map[string]any)["content"].([]any)
	for _, raw := range assistantContent {
		if raw.(map[string]any)["type"] == "thinking" {
			t.Fatalf("foreign signature leaked: %#v", assistantContent)
		}
	}
}
