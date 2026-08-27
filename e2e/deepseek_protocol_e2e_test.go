package e2e_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/dcalsky/best-harness-go"
	"github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
)

const visionModelID = "deepseek-v4-flash-vision-exp"

type deepSeekProtocolCase struct {
	name       string
	provider   harness.Provider
	api        harness.API
	generation harness.GenerationConfig
}

func deepSeekProtocolCases(t *testing.T) []deepSeekProtocolCase {
	t.Helper()
	key := deepSeekAPIKey(t)
	chatClient := openai.NewClient(
		openaioption.WithAPIKey(key),
		openaioption.WithBaseURL("https://api.deepseek.com"),
	)
	responsesClient := openai.NewClient(
		openaioption.WithAPIKey(key),
		openaioption.WithBaseURL("https://api.deepseek.com"),
	)
	anthropicClient := anthropic.NewClient(
		anthropicoption.WithAPIKey(key),
		anthropicoption.WithBaseURL("https://api.deepseek.com/anthropic"),
	)
	return []deepSeekProtocolCase{
		{
			name: "openai_chat_completions", provider: harness.NewOpenAIProvider(chatClient), api: harness.APIOpenAI,
			generation: harness.GenerationConfig{Extra: map[string]json.RawMessage{
				"thinking.type": json.RawMessage(`"disabled"`),
			}},
		},
		{
			name: "openai_responses", provider: harness.NewOpenAIResponsesProvider(responsesClient), api: harness.APIOpenAIResponses,
			generation: harness.GenerationConfig{Thinking: harness.Ptr(false)},
		},
		{
			name: "anthropic_messages", provider: harness.NewAnthropicProvider(anthropicClient), api: harness.APIAnthropic,
			generation: harness.GenerationConfig{Thinking: harness.Ptr(false)},
		},
	}
}

func deepSeekProtocolModel(protocol deepSeekProtocolCase, id string, images bool) harness.Model {
	return harness.Model{
		Provider: "deepseek", API: protocol.api, ID: id, Name: id,
		ContextWindow: 1_000_000, MaxOutput: 256,
		SupportsImages: images, SupportsReasoning: true,
	}
}

func TestDeepSeekProtocolTextE2E(t *testing.T) {
	for _, protocol := range deepSeekProtocolCases(t) {
		t.Run(protocol.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			trace, err := collectTrace(ctx, protocol.provider, harness.Request{
				Model:      deepSeekProtocolModel(protocol, modelID, false),
				Messages:   []harness.Message{harness.User("Reply with exactly THREE_PROTOCOLS_OK and nothing else.")},
				MaxTokens:  64,
				Generation: protocol.generation,
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(trace.Text) != "THREE_PROTOCOLS_OK" {
				t.Fatalf("response=%q", trace.Text)
			}
			if trace.Usage.InputTokens == 0 || trace.Usage.OutputTokens == 0 || trace.Usage.TotalTokens == 0 {
				t.Fatalf("usage=%#v", trace.Usage)
			}
		})
	}
}

type protocolEchoParams struct {
	Marker string `json:"marker"`
}

func TestDeepSeekProtocolToolReplayE2E(t *testing.T) {
	for _, protocol := range deepSeekProtocolCases(t) {
		t.Run(protocol.name, func(t *testing.T) {
			tools := harness.NewToolRegistry()
			var calls atomic.Int32
			if err := tools.Register(harness.Tool[protocolEchoParams, struct{}]{
				Name:        "protocol_echo",
				Description: "Return the marker unchanged. Always use this tool when the user asks for a protocol marker.",
				Execute: func(_ context.Context, _ harness.ToolCall, params protocolEchoParams, _ harness.Update[struct{}]) (harness.ToolResult[struct{}], error) {
					calls.Add(1)
					return harness.ToolResult[struct{}]{Content: []harness.Content{harness.Text(params.Marker)}}, nil
				},
			}); err != nil {
				t.Fatal(err)
			}
			selected := deepSeekProtocolModel(protocol, modelID, false)
			agent := harness.NewAgent(harness.AgentOptions{
				Provider: protocol.provider, Model: selected, Tools: tools,
				ActiveTools: []string{"protocol_echo"}, Generation: protocol.generation,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			run := startAgentRun(t, ctx, agent, harness.AgentPrompt{Steps: harness.Sequence{
				harness.UserText("Call protocol_echo exactly once with marker THREE_PROTOCOL_TOOL_OK. After receiving the tool result, reply with exactly THREE_PROTOCOL_TOOL_OK and do not call a tool again."),
			}})
			if err := run.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			messages := agent.Messages()
			if calls.Load() != 1 {
				t.Fatalf("tool calls=%d messages=%#v", calls.Load(), messages)
			}
			if len(messages) == 0 || strings.TrimSpace(messages[len(messages)-1].Text()) != "THREE_PROTOCOL_TOOL_OK" {
				t.Fatalf("messages=%#v", messages)
			}
		})
	}
}

func TestDeepSeekProtocolVisionE2E(t *testing.T) {
	imageBytes, err := os.ReadFile(filepath.Join("testdata", "test.png"))
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(imageBytes)
	for _, protocol := range deepSeekProtocolCases(t) {
		t.Run(protocol.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			trace, err := collectTrace(ctx, protocol.provider, harness.Request{
				Model: deepSeekProtocolModel(protocol, visionModelID, true),
				Messages: []harness.Message{{Role: harness.RoleUser, Content: []harness.Content{
					harness.Text("读取这张角色卡中下方最大字号的两个汉字姓名。只回复：VISION_OK:姓名，不要添加其他内容。"),
					harness.Image(encoded, "image/png"),
				}}},
				MaxTokens:  128,
				Generation: protocol.generation,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(trace.Text, "VISION_OK") || !strings.Contains(trace.Text, "季川") {
				t.Fatalf("vision response=%q", trace.Text)
			}
			if trace.Usage.TotalTokens == 0 {
				t.Fatalf("usage=%#v", trace.Usage)
			}
			t.Logf("%s vision response: %s", protocol.name, strings.TrimSpace(trace.Text))
		})
	}
}
