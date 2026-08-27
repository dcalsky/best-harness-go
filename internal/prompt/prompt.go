// Package prompt defines deterministic steps executed before the model agent loop.
package prompt

import (
	"errors"
	"fmt"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"

	"github.com/dcalsky/best-harness-go/internal/message"
)

// Step is a closed union of deterministic prompt step types.
type Step interface{ isPromptStep() }

type StepType string

const (
	StepUserMessage      StepType = "user_message"
	StepAssistantMessage StepType = "assistant_message"
	StepToolCalls        StepType = "tool_calls"
)

// Sequence is executed in order before the first model turn.
type Sequence []Step

type UserMessageStep struct {
	Content []message.Content `json:"content"`
}

type AssistantMessageStep struct {
	Content []message.Content `json:"content"`
}

// ToolCallsStep executes Calls sequentially. Each call becomes its own
// assistant tool-call message and tool-result message pair.
type ToolCallsStep struct {
	Calls []ToolCall `json:"calls"`
}

func (UserMessageStep) isPromptStep()      {}
func (AssistantMessageStep) isPromptStep() {}
func (ToolCallsStep) isPromptStep()        {}

type OnErrorPolicy string

const (
	// OnErrorEnterAgentLoop skips the remaining deterministic steps and starts
	// the normal model agent loop. It is the default policy.
	OnErrorEnterAgentLoop OnErrorPolicy = "enter_agent_loop"
	// OnErrorContinue continues with the next deterministic call or step.
	OnErrorContinue OnErrorPolicy = "continue"
	// OnErrorAbort ends the run without calling the model and makes Wait fail.
	OnErrorAbort OnErrorPolicy = "abort"
)

type ToolCall struct {
	// Key is an optional stable orchestration identifier. The agent generates a
	// separate protocol tool-call ID for every execution.
	Key       string          `json:"key,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	OnError   OnErrorPolicy   `json:"onError,omitempty"`
}

type encodedStep struct {
	Type    StepType          `json:"type"`
	Content []message.Content `json:"content,omitempty"`
	Calls   []ToolCall        `json:"calls,omitempty"`
}

func (s Sequence) MarshalJSON() ([]byte, error) {
	encoded := make([]encodedStep, len(s))
	for i, raw := range s {
		switch step := raw.(type) {
		case UserMessageStep:
			encoded[i] = encodedStep{Type: StepUserMessage, Content: step.Content}
		case *UserMessageStep:
			if step == nil {
				return nil, fmt.Errorf("prompt step %d is nil", i)
			}
			encoded[i] = encodedStep{Type: StepUserMessage, Content: step.Content}
		case AssistantMessageStep:
			encoded[i] = encodedStep{Type: StepAssistantMessage, Content: step.Content}
		case *AssistantMessageStep:
			if step == nil {
				return nil, fmt.Errorf("prompt step %d is nil", i)
			}
			encoded[i] = encodedStep{Type: StepAssistantMessage, Content: step.Content}
		case ToolCallsStep:
			encoded[i] = encodedStep{Type: StepToolCalls, Calls: step.Calls}
		case *ToolCallsStep:
			if step == nil {
				return nil, fmt.Errorf("prompt step %d is nil", i)
			}
			encoded[i] = encodedStep{Type: StepToolCalls, Calls: step.Calls}
		default:
			return nil, fmt.Errorf("prompt step %d has unsupported type %T", i, raw)
		}
	}
	return json.Marshal(encoded)
}

func (s *Sequence) UnmarshalJSON(data []byte) error {
	var encoded []encodedStep
	if err := json.Unmarshal(data, &encoded); err != nil {
		return err
	}
	decoded := make(Sequence, len(encoded))
	for i, step := range encoded {
		switch step.Type {
		case StepUserMessage:
			decoded[i] = UserMessageStep{Content: step.Content}
		case StepAssistantMessage:
			decoded[i] = AssistantMessageStep{Content: step.Content}
		case StepToolCalls:
			decoded[i] = ToolCallsStep{Calls: step.Calls}
		default:
			return fmt.Errorf("prompt step %d has invalid type %q", i, step.Type)
		}
	}
	normalized, err := decoded.Normalize()
	if err != nil {
		return err
	}
	*s = normalized
	return nil
}

func UserText(text string) UserMessageStep {
	return UserMessageStep{Content: []message.Content{message.Text(text)}}
}

func AssistantText(text string) AssistantMessageStep {
	return AssistantMessageStep{Content: []message.Content{message.Text(text)}}
}

func Tools(calls ...ToolCall) ToolCallsStep {
	return ToolCallsStep{Calls: append([]ToolCall(nil), calls...)}
}

// Normalize validates a sequence, applies safe defaults, and returns a deep
// copy that the caller may execute asynchronously.
func (s Sequence) Normalize() (Sequence, error) {
	if len(s) == 0 {
		return nil, nil
	}
	out := make(Sequence, len(s))
	keys := make(map[string]struct{})
	for i, raw := range s {
		switch step := raw.(type) {
		case UserMessageStep:
			content, err := normalizeContent(step.Content, true)
			if err != nil {
				return nil, fmt.Errorf("prompt step %d: %w", i, err)
			}
			out[i] = UserMessageStep{Content: content}
		case *UserMessageStep:
			if step == nil {
				return nil, fmt.Errorf("prompt step %d is nil", i)
			}
			content, err := normalizeContent(step.Content, true)
			if err != nil {
				return nil, fmt.Errorf("prompt step %d: %w", i, err)
			}
			out[i] = UserMessageStep{Content: content}
		case AssistantMessageStep:
			content, err := normalizeContent(step.Content, false)
			if err != nil {
				return nil, fmt.Errorf("prompt step %d: %w", i, err)
			}
			out[i] = AssistantMessageStep{Content: content}
		case *AssistantMessageStep:
			if step == nil {
				return nil, fmt.Errorf("prompt step %d is nil", i)
			}
			content, err := normalizeContent(step.Content, false)
			if err != nil {
				return nil, fmt.Errorf("prompt step %d: %w", i, err)
			}
			out[i] = AssistantMessageStep{Content: content}
		case ToolCallsStep:
			calls, err := normalizeCalls(step.Calls, keys)
			if err != nil {
				return nil, fmt.Errorf("prompt step %d: %w", i, err)
			}
			out[i] = ToolCallsStep{Calls: calls}
		case *ToolCallsStep:
			if step == nil {
				return nil, fmt.Errorf("prompt step %d is nil", i)
			}
			calls, err := normalizeCalls(step.Calls, keys)
			if err != nil {
				return nil, fmt.Errorf("prompt step %d: %w", i, err)
			}
			out[i] = ToolCallsStep{Calls: calls}
		case nil:
			return nil, fmt.Errorf("prompt step %d is nil", i)
		default:
			return nil, fmt.Errorf("prompt step %d has unsupported type %T", i, raw)
		}
	}
	if _, ok := out[len(out)-1].(AssistantMessageStep); ok {
		return nil, errors.New("prompt sequence cannot end with an assistant message step")
	}
	return out, nil
}

func normalizeContent(in []message.Content, user bool) ([]message.Content, error) {
	if len(in) == 0 {
		return nil, errors.New("message content is required")
	}
	out := make([]message.Content, len(in))
	for i, content := range in {
		allowed := content.Type == "text"
		if user {
			allowed = allowed || content.Type == "image" || content.Type == "largeText"
		}
		if !allowed {
			role := "assistant"
			if user {
				role = "user"
			}
			return nil, fmt.Errorf("content %d type %q is not allowed in a %s message step", i, content.Type, role)
		}
		out[i] = cloneContent(content)
	}
	return out, nil
}

func normalizeCalls(in []ToolCall, keys map[string]struct{}) ([]ToolCall, error) {
	if len(in) == 0 {
		return nil, errors.New("tool calls are required")
	}
	out := make([]ToolCall, len(in))
	for i, call := range in {
		if call.Name == "" {
			return nil, fmt.Errorf("tool call %d name is required", i)
		}
		if call.Key != "" {
			if _, exists := keys[call.Key]; exists {
				return nil, fmt.Errorf("tool call key %q is duplicated", call.Key)
			}
			keys[call.Key] = struct{}{}
		}
		if len(call.Arguments) == 0 {
			call.Arguments = json.RawMessage(`{}`)
		}
		var value any
		if err := json.Unmarshal(call.Arguments, &value); err != nil {
			return nil, fmt.Errorf("tool call %q arguments are invalid JSON: %w", call.Name, err)
		}
		switch call.OnError {
		case "":
			call.OnError = OnErrorEnterAgentLoop
		case OnErrorEnterAgentLoop, OnErrorContinue, OnErrorAbort:
		default:
			return nil, fmt.Errorf("tool call %q has invalid on-error policy %q", call.Name, call.OnError)
		}
		call.Arguments = call.Arguments.Clone()
		out[i] = call
	}
	return out, nil
}

func cloneContent(content message.Content) message.Content {
	content.Arguments = content.Arguments.Clone()
	return content
}
