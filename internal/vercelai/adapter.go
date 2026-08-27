package vercelai

import (
	"errors"
	"fmt"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"strings"

	"github.com/dcalsky/best-harness-go/internal/agent"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/protocol"
	sharedrun "github.com/dcalsky/best-harness-go/internal/run"
)

// EventMapper can append application-specific chunks for an AgentEvent. It is
// useful for typed data parts such as data-a2ui without teaching the generic
// adapter about application protocols.
type EventMapper func(protocol.AgentEvent) ([]Chunk, error)

type Options struct {
	// GenerateID receives a semantic prefix such as "message" or "text".
	// The default uses harness UUIDv7 run IDs.
	GenerateID func(prefix string) string
	MapEvent   EventMapper
}

// Adapter is a stateful protocol.Adapter for one UI message stream.
type Adapter struct {
	opts             Options
	runID            sharedrun.ID
	started          bool
	finished         bool
	stepOpen         bool
	messageOpen      bool
	messageEmitted   bool
	textID           string
	reasoningID      string
	toolNames        map[string]string
	errorEmitted     bool
	lastFinishReason string
}

var _ protocol.Adapter[Chunk] = (*Adapter)(nil)

func NewAdapter(opts Options) *Adapter {
	if opts.GenerateID == nil {
		opts.GenerateID = func(prefix string) string {
			return prefix + "-" + string(sharedrun.NewID())
		}
	}
	return &Adapter{opts: opts, toolNames: make(map[string]string), lastFinishReason: "stop"}
}

func (a *Adapter) Start(event protocol.RunEvent) ([]Chunk, error) {
	if a.started {
		return nil, errors.New("Vercel AI SDK adapter already started")
	}
	if event.RunID == "" {
		return nil, errors.New("Vercel AI SDK adapter requires a run ID")
	}
	a.started = true
	a.runID = event.RunID
	return []Chunk{{Type: "start", MessageID: a.opts.GenerateID("message")}}, nil
}

func (a *Adapter) Encode(event protocol.AgentEvent) ([]Chunk, error) {
	if !a.started || a.finished {
		return nil, errors.New("Vercel AI SDK adapter is not active")
	}
	if event.RunID != a.runID {
		return nil, fmt.Errorf("Vercel AI SDK adapter run mismatch: got %q, want %q", event.RunID, a.runID)
	}

	var chunks []Chunk
	e := event.Event
	switch e.Type {
	case agent.EventTurnStart:
		if a.stepOpen {
			chunks = append(chunks, a.closeParts()...)
			chunks = append(chunks, Chunk{Type: "finish-step"})
		}
		a.stepOpen = true
		chunks = append(chunks, Chunk{Type: "start-step"})
	case agent.EventMessageStart:
		if e.Message != nil && e.Message.Role == message.RoleAssistant {
			chunks = append(chunks, a.closeParts()...)
			a.messageOpen = true
			a.messageEmitted = false
		}
	case agent.EventMessageUpdate:
		if e.Stream != nil {
			switch e.Stream.Type {
			case message.EventTextDelta:
				if e.Stream.Text != "" {
					if a.textID == "" {
						a.textID = a.opts.GenerateID("text")
						chunks = append(chunks, Chunk{Type: "text-start", ID: a.textID})
					}
					chunks = append(chunks, Chunk{Type: "text-delta", ID: a.textID, Delta: e.Stream.Text})
					a.messageEmitted = true
				}
			case message.EventThinkingDelta:
				if e.Stream.Text != "" {
					if a.reasoningID == "" {
						a.reasoningID = a.opts.GenerateID("reasoning")
						chunks = append(chunks, Chunk{Type: "reasoning-start", ID: a.reasoningID})
					}
					chunks = append(chunks, Chunk{Type: "reasoning-delta", ID: a.reasoningID, Delta: e.Stream.Text})
					a.messageEmitted = true
				}
			}
		}
	case agent.EventMessageEnd:
		if e.Message != nil && e.Message.Role == message.RoleAssistant {
			if !a.messageEmitted && e.Message.Text() != "" {
				id := a.opts.GenerateID("text")
				chunks = append(chunks,
					Chunk{Type: "text-start", ID: id},
					Chunk{Type: "text-delta", ID: id, Delta: e.Message.Text()},
					Chunk{Type: "text-end", ID: id},
				)
			} else {
				chunks = append(chunks, a.closeParts()...)
			}
			a.messageOpen = false
			a.lastFinishReason = finishReason(e.Message.StopReason)
		}
	case agent.EventToolStart:
		if e.Call != nil {
			callID := a.toolCallID(e.Call.ID)
			a.toolNames[callID] = e.Call.Name
			chunks = append(chunks, Chunk{
				Type: "tool-input-available", ToolCallID: callID, ToolName: e.Call.Name,
				Input: validJSON(e.Call.Arguments), Dynamic: true, ProviderExecuted: true,
			})
		}
	case agent.EventToolEnd:
		if e.Call != nil {
			callID := a.toolCallID(e.Call.ID)
			if _, ok := a.toolNames[callID]; !ok {
				a.toolNames[callID] = e.Call.Name
				chunks = append(chunks, Chunk{
					Type: "tool-input-available", ToolCallID: callID, ToolName: e.Call.Name,
					Input: validJSON(e.Call.Arguments), Dynamic: true, ProviderExecuted: true,
				})
			}
			if e.Err != nil || (e.Result != nil && e.Result.IsError) {
				chunks = append(chunks, Chunk{Type: "tool-output-error", ToolCallID: callID, ErrorText: toolError(e), Dynamic: true, ProviderExecuted: true})
			} else {
				chunks = append(chunks, Chunk{Type: "tool-output-available", ToolCallID: callID, Output: toolOutput(e), Dynamic: true, ProviderExecuted: true})
			}
		}
	case agent.EventTurnEnd:
		chunks = append(chunks, a.closeParts()...)
		if a.stepOpen {
			chunks = append(chunks, Chunk{Type: "finish-step"})
			a.stepOpen = false
		}
	case agent.EventError:
		if e.Err != nil {
			chunks = append(chunks, Chunk{Type: "error", ErrorText: e.Err.Error()})
			a.errorEmitted = true
		}
	}

	if a.opts.MapEvent != nil {
		extra, err := a.opts.MapEvent(event)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, extra...)
	}
	return chunks, nil
}

func (a *Adapter) Finish(event protocol.RunEvent) ([]Chunk, error) {
	if !a.started || a.finished {
		return nil, errors.New("Vercel AI SDK adapter is not active")
	}
	if event.RunID != a.runID {
		return nil, fmt.Errorf("Vercel AI SDK adapter run mismatch: got %q, want %q", event.RunID, a.runID)
	}
	a.finished = true
	chunks := a.closeParts()
	if a.stepOpen {
		chunks = append(chunks, Chunk{Type: "finish-step"})
		a.stepOpen = false
	}
	reason := a.lastFinishReason
	if event.Status == sharedrun.StatusAborted {
		text := "run aborted"
		if event.Err != nil {
			text = event.Err.Error()
		}
		chunks = append(chunks, Chunk{Type: "abort", Reason: text})
		reason = "error"
	} else if event.Status == sharedrun.StatusFailed || event.Err != nil {
		if !a.errorEmitted {
			text := "run failed"
			if event.Err != nil {
				text = event.Err.Error()
			}
			chunks = append(chunks, Chunk{Type: "error", ErrorText: text})
		}
		reason = "error"
	}
	chunks = append(chunks, Chunk{Type: "finish", FinishReason: reason})
	return chunks, nil
}

func (a *Adapter) closeParts() []Chunk {
	var chunks []Chunk
	if a.reasoningID != "" {
		chunks = append(chunks, Chunk{Type: "reasoning-end", ID: a.reasoningID})
		a.reasoningID = ""
	}
	if a.textID != "" {
		chunks = append(chunks, Chunk{Type: "text-end", ID: a.textID})
		a.textID = ""
	}
	return chunks
}

func (a *Adapter) toolCallID(id string) string {
	if id != "" {
		return id
	}
	return a.opts.GenerateID("tool")
}

func validJSON(value []byte) json.RawMessage {
	raw := json.RawMessage(value)
	if len(raw) > 0 && raw.IsValid() {
		return raw.Clone()
	}
	return json.RawMessage(`{}`)
}

func finishReason(reason message.StopReason) string {
	switch reason {
	case message.StopLength:
		return "length"
	case message.StopError, message.StopAborted:
		return "error"
	case message.StopToolUse:
		return "tool-calls"
	default:
		return "stop"
	}
}

func toolError(event agent.Event) string {
	if event.Err != nil {
		return event.Err.Error()
	}
	if event.Result != nil {
		var texts []string
		for _, content := range event.Result.Content {
			if content.Type == "text" && content.Text != "" {
				texts = append(texts, content.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return "tool execution failed"
}

func toolOutput(event agent.Event) any {
	if event.Result == nil {
		return map[string]any{"status": "completed"}
	}
	return struct {
		Content   []message.Content `json:"content,omitempty"`
		Details   any               `json:"details,omitempty"`
		Terminate bool              `json:"terminate,omitempty"`
	}{Content: event.Result.Content, Details: event.Result.Details, Terminate: event.Result.Terminate}
}
