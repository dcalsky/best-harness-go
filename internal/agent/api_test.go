package agent_test

import (
	"context"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/agent"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	"github.com/dcalsky/best-harness-go/internal/prompt"
	"github.com/dcalsky/best-harness-go/internal/provider"
)

func TestAssistantMessagePersistsSourceAPIAndReasoningSignature(t *testing.T) {
	faux := &provider.Faux{StreamFunc: func(context.Context, provider.Request) (provider.Stream, error) {
		return &provider.SliceStream{Events: []message.StreamEvent{
			{Type: message.EventStart},
			{Type: message.EventThinkingDelta, Text: "thinking"},
			{Type: message.EventThinkingDelta, Signature: `{"type":"reasoning","id":"rs_1"}`},
			{Type: message.EventTextDelta, Text: "answer"},
			{Type: message.EventDone, StopReason: message.StopStop},
		}}, nil
	}}
	value := agent.New(agent.Options{
		Provider: faux,
		Model:    model.Model{Provider: "openai", API: model.APIOpenAIResponses, ID: "gpt"},
	})
	run, err := value.Start(context.Background(), agent.Prompt{Steps: prompt.Sequence{prompt.UserText("question")}}, agent.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	messages := value.Messages()
	assistant := messages[len(messages)-1]
	if assistant.API != model.APIOpenAIResponses {
		t.Fatalf("API=%q", assistant.API)
	}
	if len(assistant.Content) != 2 || assistant.Content[0].Type != "thinking" || assistant.Content[0].Signature == "" || assistant.Content[1].Text != "answer" {
		t.Fatalf("assistant=%#v", assistant)
	}
}
