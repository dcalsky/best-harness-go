// Package vercelai adapts harness AgentEvents to the Vercel AI SDK UI Message
// Stream protocol. It does not depend on a JavaScript runtime and can be used
// by any Go HTTP server.
package vercelai

import (
	"errors"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"regexp"
)

// Chunk is the JSON shape consumed by the AI SDK UI message stream protocol.
// Fields are shared by the protocol's discriminated union and are interpreted
// according to Type.
type Chunk struct {
	Type             string          `json:"type"`
	MessageID        string          `json:"messageId,omitempty"`
	ID               string          `json:"id,omitempty"`
	Delta            string          `json:"delta,omitempty"`
	ToolCallID       string          `json:"toolCallId,omitempty"`
	ToolName         string          `json:"toolName,omitempty"`
	Input            json.RawMessage `json:"input,omitempty"`
	Output           any             `json:"output,omitempty"`
	Data             any             `json:"data,omitempty"`
	ErrorText        string          `json:"errorText,omitempty"`
	FinishReason     string          `json:"finishReason,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	Dynamic          bool            `json:"dynamic,omitempty"`
	ProviderExecuted bool            `json:"providerExecuted,omitempty"`
	Transient        bool            `json:"transient,omitempty"`
}

var dataNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Data creates a custom data-* chunk. Transient chunks invoke useChat's
// onData callback but are not retained in UIMessage history.
func Data(name string, value any, transient bool) (Chunk, error) {
	if !dataNamePattern.MatchString(name) {
		return Chunk{}, errors.New("Vercel AI SDK data part name must contain only letters, numbers, dot, underscore, or hyphen")
	}
	return Chunk{Type: "data-" + name, Data: value, Transient: transient}, nil
}
