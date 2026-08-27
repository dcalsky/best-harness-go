// Package provider defines the streaming model boundary.
package provider

import (
	"context"
	"fmt"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"io"

	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	"github.com/tiendc/go-deepcopy"
)

type Tool struct {
	Name, Description string
	Parameters        json.RawMessage
}

// GenerationConfig contains provider-neutral generation parameters. Adapters
// translate supported fields to their wire protocol and return a clear error
// for unsupported combinations.
type GenerationConfig struct {
	Temperature      *float64
	TopP             *float64
	TopK             *int64
	Seed             *int64
	FrequencyPenalty *float64
	PresencePenalty  *float64
	StopSequences    []string
	// Thinking explicitly enables or disables model reasoning where supported.
	Thinking *bool
	// JSONOutput constrains the model response to a JSON object.
	JSONOutput bool
	// ParallelToolCalls controls provider-side parallel tool invocation.
	ParallelToolCalls *bool
	// ReasoningBudgetTokens sets an explicit reasoning token budget where the
	// selected protocol supports one (notably Anthropic Messages).
	ReasoningBudgetTokens *int64
	// ThinkingBudget maps to the non-standard thinking_budget field supported by
	// some OpenAI-compatible Chat APIs. Zero leaves the field unset; use
	// ExtraBody to send an explicit zero.
	ThinkingBudget int
	// PreserveThinking maps to the non-standard preserve_thinking field supported
	// by some OpenAI-compatible Chat APIs. False leaves the field unset; use
	// ExtraBody to send an explicit false.
	PreserveThinking bool
	// UseMaxCompletionTokens makes OpenAI Chat send max_completion_tokens instead
	// of the broadly compatible max_tokens field. Enable it for reasoning models
	// and endpoints that require the newer field.
	UseMaxCompletionTokens bool
	// ExtraBody contains additional request body fields using ordinary Go values,
	// matching OpenAI Agents SDK ModelSettings.extra_body semantics. Provider SDKs
	// merge these fields into the request body after normalized fields, so a key in
	// ExtraBody intentionally overrides a normalized field with the same name.
	ExtraBody map[string]any
	// Extra contains provider-native top-level generation fields. Values are raw
	// JSON so newly introduced API parameters can be used without waiting for a
	// harness release. Adapters reject keys that collide with normalized fields.
	Extra map[string]json.RawMessage
}

// Ptr makes pointer-valued configuration fields concise at call sites.
func Ptr[T any](value T) *T { return &value }

// Clone returns a request-local copy of the generation configuration.
func (c GenerationConfig) Clone() GenerationConfig {
	var clone GenerationConfig
	if err := deepcopy.Copy(&clone, &c); err != nil {
		panic(fmt.Errorf("clone generation config: %w", err))
	}
	return clone
}

type Request struct {
	Model           model.Model
	SystemPrompt    string
	Messages        []message.Message
	Tools           []Tool
	MaxTokens       int64
	ReasoningEffort string
	Generation      GenerationConfig
}

type Stream interface {
	Next() (message.StreamEvent, error)
	Close() error
}
type Provider interface {
	Stream(context.Context, Request) (Stream, error)
}

type SliceStream struct {
	Events []message.StreamEvent
	At     int
	Closed bool
}

func (s *SliceStream) Next() (message.StreamEvent, error) {
	if s.Closed || s.At >= len(s.Events) {
		return message.StreamEvent{}, io.EOF
	}
	e := s.Events[s.At]
	s.At++
	return e, nil
}
func (s *SliceStream) Close() error { s.Closed = true; return nil }

type Faux struct {
	Requests   chan Request
	StreamFunc func(context.Context, Request) (Stream, error)
}

func (f *Faux) Stream(ctx context.Context, r Request) (Stream, error) {
	if f.Requests != nil {
		select {
		case f.Requests <- r:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.StreamFunc(ctx, r)
}
