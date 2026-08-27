package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"

	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	base "github.com/dcalsky/best-harness-go/internal/provider"
	adapter "github.com/dcalsky/best-harness-go/internal/provider/openai"
)

func openAIClient(server *httptest.Server) openaisdk.Client {
	return openaisdk.NewClient(
		openaioption.WithBaseURL(server.URL+"/v1"),
		openaioption.WithAPIKey("secret"),
		openaioption.WithMaxRetries(0),
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

func TestChatCompletionsRequestAndStream(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("authorization=%q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"chat-1\",\"model\":\"served\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\",\"content\":\"hello\"}}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"chat-1\",\"model\":\"served\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\"}}]}}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"chat-1\",\"model\":\"served\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"x\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		io.WriteString(w, "data: {\"id\":\"chat-1\",\"model\":\"served\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":4,\"total_tokens\":16,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	stream, err := adapter.New(openAIClient(server)).Stream(context.Background(), base.Request{
		Model:        model.Model{Provider: "openai", API: model.APIOpenAI, ID: "gpt", SupportsImages: true, InputPrice: 1, OutputPrice: 2},
		SystemPrompt: "system",
		Messages: []message.Message{
			{Role: message.RoleUser, Content: []message.Content{message.Text("question"), message.Image("AAAA", "image/png")}},
			{Role: message.RoleAssistant, API: model.APIOpenAI, Content: []message.Content{
				message.Thinking("prior reasoning"),
				message.ToolCall("previous_call", "lookup", []byte(`{"q":"old"}`)),
			}},
			{Role: message.RoleTool, ToolCallID: "previous_call", Content: []message.Content{
				message.Text("old result"), message.Image("BBBB", "image/png"),
			}},
		},
		Tools:     []base.Tool{{Name: "lookup", Description: "look up", Parameters: []byte(`{"type":"object","properties":{"q":{"type":"string"}}}`)}},
		MaxTokens: 123, ReasoningEffort: "high",
		Generation: base.GenerationConfig{
			Temperature: base.Ptr(0.2), JSONOutput: true, ParallelToolCalls: base.Ptr(true),
			ThinkingBudget: 2_048, PreserveThinking: true, UseMaxCompletionTokens: true,
			ExtraBody: map[string]any{
				"temperature":      0.75,
				"provider_feature": map[string]any{"mode": "chat"},
			},
			Extra: map[string]json.RawMessage{"service_tier": json.RawMessage(`"flex"`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	if len(events) != 7 {
		t.Fatalf("events=%#v", events)
	}
	if events[0].Type != message.EventStart || events[1].Text != "hello" || events[2].Text != "think" {
		t.Fatalf("content events=%#v", events)
	}
	if events[3].ToolCallID != "call_1" || events[3].ToolName != "lookup" || events[3].ArgumentsDelta != `{"q":` || events[4].ArgumentsDelta != `"x"}` || events[5].Type != message.EventToolCallEnd {
		t.Fatalf("tool events=%#v", events)
	}
	done := events[6]
	if done.Type != message.EventDone || done.StopReason != message.StopToolUse || done.Usage.InputTokens != 10 || done.Usage.CacheReadTokens != 2 || done.Usage.TotalTokens != 16 {
		t.Fatalf("done=%#v", done)
	}
	if string(done.ProviderMetadata["responseId"]) != `"chat-1"` || string(done.ProviderMetadata["responseModel"]) != `"served"` {
		t.Fatalf("metadata=%v", done.ProviderMetadata)
	}
	if body["model"] != "gpt" || body["max_completion_tokens"].(float64) != 123 || body["service_tier"] != "flex" {
		t.Fatalf("body=%#v", body)
	}
	if body["thinking_budget"].(float64) != 2048 || body["preserve_thinking"] != true || body["temperature"].(float64) != 0.75 {
		t.Fatalf("thinking/extra_body=%#v", body)
	}
	if body["provider_feature"].(map[string]any)["mode"] != "chat" {
		t.Fatalf("provider_feature=%#v", body["provider_feature"])
	}
	if body["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("response_format=%#v", body["response_format"])
	}
	if len(body["tools"].([]any)) != 1 || len(body["messages"].([]any)) != 5 {
		t.Fatalf("tools/messages=%#v", body)
	}
	messages := body["messages"].([]any)
	assistant := messages[2].(map[string]any)
	if assistant["reasoning_content"] != "prior reasoning" {
		t.Fatalf("assistant replay=%#v", assistant)
	}
	imageParts := messages[4].(map[string]any)["content"].([]any)
	if len(imageParts) != 2 || imageParts[1].(map[string]any)["type"] != "image_url" ||
		imageParts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,BBBB" {
		t.Fatalf("tool image replay=%#v", messages[4])
	}
}

func TestResponsesRequestReasoningAndStream(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"sequence_number\":1,\"output_index\":0,\"item_id\":\"rs_1\",\"summary_index\":0,\"delta\":\"think\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"rs_1\",\"status\":\"completed\",\"encrypted_content\":\"opaque\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"think\"}]}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"output_index\":1,\"content_index\":0,\"item_id\":\"msg_1\",\"delta\":\"hello\",\"logprobs\":[]}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"sequence_number\":4,\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"\",\"status\":\"in_progress\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":5,\"output_index\":2,\"item_id\":\"fc_1\",\"delta\":\"{\\\"q\\\":\\\"x\\\"}\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"sequence_number\":6,\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"x\\\"}\",\"status\":\"completed\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"sequence_number\":7,\"response\":{\"id\":\"resp_1\",\"model\":\"served\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":12,\"output_tokens\":5,\"total_tokens\":17,\"input_tokens_details\":{\"cached_tokens\":3,\"cache_write_tokens\":1},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n")
	}))
	defer server.Close()

	stream, err := adapter.New(openAIClient(server)).Stream(context.Background(), base.Request{
		Model: model.Model{Provider: "openai", API: model.APIOpenAIResponses, ID: "gpt", SupportsImages: true, InputPrice: 1, OutputPrice: 2},
		Messages: []message.Message{
			message.User("question"),
			{Role: message.RoleAssistant, API: model.APIOpenAIResponses, Content: []message.Content{
				message.ToolCall("prior_call|prior_item", "lookup", []byte(`{"q":"old"}`)),
			}},
			{Role: message.RoleTool, ToolCallID: "prior_call|prior_item", Content: []message.Content{
				message.Text("old result"), message.Image("CCCC", "image/png"),
			}},
		},
		Tools:     []base.Tool{{Name: "lookup", Parameters: []byte(`{"type":"object"}`)}},
		MaxTokens: 222, ReasoningEffort: "high",
		Generation: base.GenerationConfig{
			ExtraBody: map[string]any{"provider_feature": map[string]any{"mode": "responses"}},
			Extra: map[string]json.RawMessage{
				"service_tier":            json.RawMessage(`"flex"`),
				"reasoning.budget_tokens": json.RawMessage(`2048`),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, stream)
	if len(events) != 8 || events[1].Text != "think" || events[2].Signature == "" || events[3].Text != "hello" {
		t.Fatalf("events=%#v", events)
	}
	if events[4].ToolCallID != "call_1|fc_1" || events[5].ArgumentsDelta != `{"q":"x"}` || events[6].Type != message.EventToolCallEnd {
		t.Fatalf("tool events=%#v", events)
	}
	done := events[7]
	if done.StopReason != message.StopToolUse || done.Usage.InputTokens != 8 || done.Usage.CacheReadTokens != 3 || done.Usage.CacheWriteTokens != 1 {
		t.Fatalf("done=%#v", done)
	}
	if body["store"] != false || body["max_output_tokens"].(float64) != 222 || body["service_tier"] != "flex" {
		t.Fatalf("body=%#v", body)
	}
	if body["reasoning"].(map[string]any)["budget_tokens"].(float64) != 2048 {
		t.Fatalf("reasoning=%#v", body["reasoning"])
	}
	if body["provider_feature"].(map[string]any)["mode"] != "responses" {
		t.Fatalf("provider_feature=%#v", body["provider_feature"])
	}
	include := body["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include=%#v", include)
	}
	requestInput := body["input"].([]any)
	toolOutput := requestInput[2].(map[string]any)["output"].([]any)
	if len(toolOutput) != 2 || toolOutput[0].(map[string]any)["type"] != "input_text" ||
		toolOutput[1].(map[string]any)["type"] != "input_image" ||
		toolOutput[1].(map[string]any)["image_url"] != "data:image/png;base64,CCCC" {
		t.Fatalf("responses tool output=%#v", requestInput[2])
	}
}

func TestChatTokenLimitFieldSelection(t *testing.T) {
	tests := []struct {
		name            string
		useCompletion   bool
		expectedField   string
		unexpectedField string
	}{
		{name: "compatible max_tokens", expectedField: "max_tokens", unexpectedField: "max_completion_tokens"},
		{name: "new max_completion_tokens", useCompletion: true, expectedField: "max_completion_tokens", unexpectedField: "max_tokens"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			stream, err := adapter.New(openAIClient(server)).Stream(context.Background(), base.Request{
				Model: model.Model{API: model.APIOpenAI, ID: "m"}, Messages: []message.Message{message.User("hi")},
				MaxTokens: 123, Generation: base.GenerationConfig{UseMaxCompletionTokens: test.useCompletion},
			})
			if err != nil {
				t.Fatal(err)
			}
			collect(t, stream)
			if body[test.expectedField] != float64(123) {
				t.Fatalf("%s=%#v body=%#v", test.expectedField, body[test.expectedField], body)
			}
			if _, exists := body[test.unexpectedField]; exists {
				t.Fatalf("unexpected %s in body=%#v", test.unexpectedField, body)
			}
		})
	}
}

func TestOpenAIHTTPContextOverflowIsClassified(t *testing.T) {
	for _, api := range []model.API{model.APIOpenAI, model.APIOpenAIResponses} {
		t.Run(string(api), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"error":{"code":"context_length_exceeded","message":"maximum context length exceeded"}}`)
			}))
			defer server.Close()
			stream, err := adapter.New(openAIClient(server)).Stream(context.Background(), base.Request{Model: model.Model{Provider: "openai", API: api, ID: "m"}})
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
			stream.Close()
		})
	}
}

func TestResponsesStreamContextOverflowIsClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.failed\",\"sequence_number\":1,\"response\":{\"id\":\"resp\",\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"prompt is too long\"},\"output\":[]}}\n\n")
	}))
	defer server.Close()
	stream, err := adapter.New(openAIClient(server)).Stream(context.Background(), base.Request{Model: model.Model{Provider: "openai", API: model.APIOpenAIResponses, ID: "m"}})
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

func TestOpenAIUnsupportedAndConflictingExtraFailBeforeHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer server.Close()
	p := adapter.New(openAIClient(server))
	_, err := p.Stream(context.Background(), base.Request{
		Model:      model.Model{API: model.APIOpenAIResponses, ID: "m"},
		Generation: base.GenerationConfig{TopK: base.Ptr(int64(40))},
	})
	if err == nil || !strings.Contains(err.Error(), "top_k") || called {
		t.Fatalf("error=%v called=%v", err, called)
	}
	_, err = p.Stream(context.Background(), base.Request{
		Model:      model.Model{API: model.APIOpenAI, ID: "m"},
		Generation: base.GenerationConfig{Extra: map[string]json.RawMessage{"model": json.RawMessage(`"other"`)}},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts") || called {
		t.Fatalf("error=%v called=%v", err, called)
	}
	_, err = p.Stream(context.Background(), base.Request{
		Model:      model.Model{API: model.APIOpenAIResponses, ID: "m"},
		Generation: base.GenerationConfig{ThinkingBudget: 2_048},
	})
	if err == nil || !strings.Contains(err.Error(), "only supported") || called {
		t.Fatalf("error=%v called=%v", err, called)
	}
}
