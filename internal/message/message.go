// Package message defines the provider-neutral conversation protocol.
package message

import (
	"bytes"
	"errors"
	"fmt"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"time"

	"github.com/dcalsky/best-harness-go/internal/model"
)

const DefaultLargeTextMaxChars = 4_000

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "toolResult"
)

type Origin string

const (
	OriginUser   Origin = "user"
	OriginModel  Origin = "model"
	OriginScript Origin = "script"
	OriginTool   Origin = "tool"
)

type StopReason string

const (
	StopStop    StopReason = "stop"
	StopLength  StopReason = "length"
	StopToolUse StopReason = "toolUse"
	StopError   StopReason = "error"
	StopAborted StopReason = "aborted"
)

type Usage struct {
	InputTokens      int64 `json:"input,omitempty"`
	OutputTokens     int64 `json:"output,omitempty"`
	CacheReadTokens  int64 `json:"cacheRead,omitempty"`
	CacheWriteTokens int64 `json:"cacheWrite,omitempty"`
	TotalTokens      int64 `json:"totalTokens,omitempty"`
	Cost             *Cost `json:"cost,omitempty"`
}

type Cost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
	Total      float64 `json:"total,omitempty"`
}

type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type ThinkingContent struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"thinkingSignature,omitempty"`
}
type ImageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}
type ToolCallContent struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Content struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	MaxChars  int             `json:"maxChars,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"thinkingSignature,omitempty"`
	Data      string          `json:"data,omitempty"`
	MimeType  string          `json:"mimeType,omitempty"`
	ID        string          `json:"id,omitempty"`
	Key       string          `json:"key,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// UnmarshalJSON restores tool-call arguments that omitempty drops: jsonv2
// omits any field encoding as an empty JSON object, so a persisted `{}` (or
// an already-corrupted `null`) would otherwise reload as invalid arguments.
func (c *Content) UnmarshalJSON(data []byte) error {
	type contentAlias Content
	var decoded contentAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = Content(decoded)
	if c.Type == "toolCall" {
		if args := bytes.TrimSpace(c.Arguments); len(args) == 0 || bytes.Equal(args, []byte("null")) {
			c.Arguments = json.RawMessage(`{}`)
		}
	}
	return nil
}

func Text(s string) Content { return Content{Type: "text", Text: s} }

// LargeText marks text that must be sent to the LLM as its own user message.
// The optional limit counts Unicode code points and defaults to
// DefaultLargeTextMaxChars. Non-positive limits use the default.
func LargeText(s string, maxChars ...int) Content {
	limit := DefaultLargeTextMaxChars
	if len(maxChars) > 0 && maxChars[0] > 0 {
		limit = maxChars[0]
	}
	return Content{Type: "largeText", Text: s, MaxChars: limit}
}
func Thinking(s string) Content       { return Content{Type: "thinking", Thinking: s} }
func Image(data, mime string) Content { return Content{Type: "image", Data: data, MimeType: mime} }
func ToolCall(id, name string, args json.RawMessage) Content {
	return Content{Type: "toolCall", ID: id, Name: name, Arguments: args.Clone()}
}

type Message struct {
	Role             Role                       `json:"role"`
	Origin           Origin                     `json:"origin,omitempty"`
	Content          []Content                  `json:"content"`
	Timestamp        int64                      `json:"timestamp"`
	API              model.API                  `json:"api,omitempty"`
	Provider         string                     `json:"provider,omitempty"`
	Model            string                     `json:"model,omitempty"`
	StopReason       StopReason                 `json:"stopReason,omitempty"`
	ErrorMessage     string                     `json:"errorMessage,omitempty"`
	Usage            Usage                      `json:"usage,omitempty"`
	ToolCallID       string                     `json:"toolCallId,omitempty"`
	ToolCallKey      string                     `json:"toolCallKey,omitempty"`
	ToolName         string                     `json:"toolName,omitempty"`
	IsError          bool                       `json:"isError,omitempty"`
	ProviderMetadata map[string]json.RawMessage `json:"providerMetadata,omitempty"`
}

func User(text string) Message {
	return Message{Role: RoleUser, Origin: OriginUser, Content: []Content{Text(text)}, Timestamp: time.Now().UnixMilli()}
}

// ExpandLargeText converts every largeText content item into a separate user
// message containing ordinary text. Other content from the source message is
// kept together before the extracted large-text messages.
func ExpandLargeText(messages []Message) []Message {
	var out []Message
	for _, m := range messages {
		regular := make([]Content, 0, len(m.Content))
		large := make([]Content, 0, len(m.Content))
		for _, c := range m.Content {
			if c.Type == "largeText" {
				large = append(large, c)
			} else {
				regular = append(regular, c)
			}
		}
		if len(large) == 0 {
			out = append(out, m)
			continue
		}
		if len(regular) > 0 {
			m.Content = regular
			out = append(out, m)
		}
		for _, c := range large {
			out = append(out, Message{
				Role:      RoleUser,
				Origin:    m.Origin,
				Content:   []Content{Text(c.LLMText())},
				Timestamp: m.Timestamp,
			})
		}
	}
	return out
}

// NormalizeForProvider returns a provider-safe replay of messages without
// modifying the stored conversation. Failed assistant turns are excluded, and
// every assistant tool call is resolved before the next assistant/user message
// or the end of the context, matching pi-ai's provider transformation.
func NormalizeForProvider(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	type pendingCall struct{ id, name string }
	out := make([]Message, 0, len(messages))
	var pending []pendingCall
	results := make(map[string]struct{})
	flush := func() {
		for _, call := range pending {
			if _, ok := results[call.id]; ok {
				continue
			}
			out = append(out, Message{
				Role:       RoleTool,
				Origin:     OriginTool,
				Content:    []Content{Text("No result provided")},
				Timestamp:  time.Now().UnixMilli(),
				ToolCallID: call.id,
				ToolName:   call.name,
				IsError:    true,
			})
		}
		pending = nil
		clear(results)
	}

	for _, msg := range messages {
		switch msg.Role {
		case RoleAssistant:
			flush()
			if msg.StopReason == StopError || msg.StopReason == StopAborted {
				continue
			}
			for _, content := range msg.Content {
				if content.Type == "toolCall" {
					pending = append(pending, pendingCall{id: content.ID, name: content.Name})
				}
			}
			out = append(out, msg)
		case RoleTool:
			results[msg.ToolCallID] = struct{}{}
			out = append(out, msg)
		case RoleUser:
			flush()
			out = append(out, msg)
		default:
			out = append(out, msg)
		}
	}
	flush()
	return out
}

// LLMText returns the text representation sent to the LLM. For largeText it
// applies its configured Unicode-character limit while preserving both ends.
func (c Content) LLMText() string {
	if c.Type != "largeText" {
		return c.Text
	}
	limit := c.MaxChars
	if limit <= 0 {
		limit = DefaultLargeTextMaxChars
	}
	runes := []rune(c.Text)
	if len(runes) <= limit {
		return c.Text
	}
	half := limit / 2
	marker := fmt.Sprintf("\n\n[truncated: text exceeded %d chars; kept head and tail from %d chars]\n\n", limit, len(runes))
	return string(runes[:half]) + marker + string(runes[len(runes)-half:])
}

func (m Message) Text() string {
	var out string
	for _, c := range m.Content {
		if c.Type == "text" || c.Type == "largeText" {
			out += c.Text
		}
	}
	return out
}

type StreamEventType string

const (
	EventStart         StreamEventType = "start"
	EventTextDelta     StreamEventType = "text_delta"
	EventThinkingDelta StreamEventType = "thinking_delta"
	EventToolCallStart StreamEventType = "tool_call_start"
	EventToolCallDelta StreamEventType = "tool_call_delta"
	EventToolCallEnd   StreamEventType = "tool_call_end"
	EventDone          StreamEventType = "done"
	EventError         StreamEventType = "error"
)

type StreamEvent struct {
	Type             StreamEventType
	Index            int
	Text             string
	ToolCallID       string
	ToolName         string
	ArgumentsDelta   string
	Signature        string
	ProviderMetadata map[string]json.RawMessage
	Usage            Usage
	StopReason       StopReason
	Err              error
}

type ProviderError struct {
	Provider      string
	StatusCode    int
	Code, Message string
	Retryable     bool
	Cause         error
}

func (e *ProviderError) Error() string { return fmt.Sprintf("%s: %s", e.Provider, e.Message) }
func (e *ProviderError) Unwrap() error { return e.Cause }

var ErrContextOverflow = errors.New("model context window exceeded")
