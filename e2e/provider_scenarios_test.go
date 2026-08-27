package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

type streamTrace struct {
	Text           string
	Thinking       string
	Usage          harness.Usage
	StopReason     harness.StopReason
	TextDeltas     int
	ThinkingDeltas int
}

func collectTrace(ctx context.Context, p harness.Provider, request harness.Request) (streamTrace, error) {
	stream, err := p.Stream(ctx, request)
	if err != nil {
		return streamTrace{}, err
	}
	defer stream.Close()
	var trace streamTrace
	var text strings.Builder
	var thinking strings.Builder
	for {
		event, err := stream.Next()
		if err == io.EOF {
			trace.Text = text.String()
			trace.Thinking = thinking.String()
			return trace, nil
		}
		if err != nil {
			return trace, err
		}
		switch event.Type {
		case harness.EventTextDelta:
			trace.TextDeltas++
			text.WriteString(event.Text)
		case harness.EventThinkingDelta:
			trace.ThinkingDeltas++
			thinking.WriteString(event.Text)
		case harness.EventError:
			return trace, event.Err
		}
		if event.StopReason != "" {
			trace.StopReason = event.StopReason
		}
		if event.Usage.TotalTokens > 0 {
			trace.Usage = event.Usage
		}
	}
}

func TestDeepSeekMaxTokensAndLengthStop(t *testing.T) {
	p, selected := deepSeek(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	trace, err := collectTrace(ctx, p, harness.Request{
		Model:      selected,
		Messages:   []harness.Message{harness.User("Write a numbered list containing twenty different fruit names. Do not stop early.")},
		MaxTokens:  8,
		Generation: nonThinking,
	})
	if err != nil {
		t.Fatal(err)
	}
	if trace.StopReason != harness.StopLength {
		t.Fatalf("stop reason=%q text=%q usage=%#v", trace.StopReason, trace.Text, trace.Usage)
	}
	if trace.Usage.OutputTokens == 0 || trace.Usage.OutputTokens > 8 {
		t.Fatalf("usage=%#v", trace.Usage)
	}
}

func TestDeepSeekJSONOutput(t *testing.T) {
	p, selected := deepSeek(t)
	generation := harness.GenerationConfig{Thinking: harness.Ptr(false), JSONOutput: true}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	trace, err := collectTrace(ctx, p, harness.Request{
		Model:      selected,
		Messages:   []harness.Message{harness.User(`Return a JSON object with exactly two fields: "status" set to "JSON_OK" and "count" set to 3.`)},
		MaxTokens:  64,
		Generation: generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(trace.Text), &value); err != nil {
		t.Fatalf("decode %q: %v", trace.Text, err)
	}
	if value.Status != "JSON_OK" || value.Count != 3 {
		t.Fatalf("value=%#v", value)
	}
}

func TestDeepSeekThinkingStream(t *testing.T) {
	p, selected := deepSeek(t)
	selected.MaxOutput = 1_024
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	trace, err := collectTrace(ctx, p, harness.Request{
		Model:      selected,
		Messages:   []harness.Message{harness.User("What is 17 multiplied by 19? End the final answer with THINKING_OK.")},
		MaxTokens:  selected.MaxOutput,
		Generation: thinkingEnabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if trace.ThinkingDeltas == 0 || strings.TrimSpace(trace.Thinking) == "" {
		t.Fatalf("missing thinking stream: %#v", trace)
	}
	if !strings.Contains(trace.Text, "323") || !strings.Contains(trace.Text, "THINKING_OK") {
		t.Fatalf("text=%q", trace.Text)
	}
}

func TestDeepSeekUnicodeStreaming(t *testing.T) {
	p, selected := deepSeek(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	trace, err := collectTrace(ctx, p, harness.Request{
		Model:      selected,
		Messages:   []harness.Message{harness.User("Reply with exactly: 你好，世界🌍")},
		MaxTokens:  32,
		Generation: nonThinking,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(trace.Text) != "你好，世界🌍" {
		t.Fatalf("text=%q", trace.Text)
	}
}

func TestDeepSeekCancelledRequest(t *testing.T) {
	p, selected := deepSeek(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Stream(ctx, harness.Request{
		Model:      selected,
		Messages:   []harness.Message{harness.User("This request must be cancelled.")},
		MaxTokens:  32,
		Generation: nonThinking,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}
