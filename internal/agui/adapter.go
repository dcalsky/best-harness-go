package agui

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

type EventMapper func(protocol.AgentEvent) ([]Event, error)
type ToolResultMapper func(agent.Event) (string, error)

type Options struct {
	// ThreadID is the AG-UI conversation thread containing this run. AG-UI
	// requires it on both lifecycle boundary events.
	ThreadID    string
	ParentRunID string
	Input       any

	// GenerateID receives a semantic prefix such as "message" or "reasoning".
	// The default uses harness UUIDv7 run IDs.
	GenerateID func(prefix string) string
	// StepName maps the one-based harness turn number to an AG-UI step name.
	StepName func(turn int) string
	// Timestamp optionally supplies Unix milliseconds for generated events.
	// AG-UI timestamps are optional, so the default omits them.
	Timestamp     func() int64
	MapEvent      EventMapper
	MapToolResult ToolResultMapper
	MapRunResult  func(protocol.RunEvent) any
}

type toolState struct {
	name        string
	parentID    string
	argsEmitted bool
	ended       bool
	resulted    bool
}

// Adapter is a stateful protocol.Adapter for one AG-UI run stream.
type Adapter struct {
	opts Options

	runID             sharedrun.ID
	started           bool
	finished          bool
	stepOpen          bool
	stepName          string
	turn              int
	messageOpen       bool
	messageID         string
	textEmitted       bool
	reasoningID       string
	reasoningStreamed bool

	streamToolIDs map[int]string
	tools         map[string]*toolState
	toolOrder     []string
	fallbackTools map[string][]string
	pendingErr    error
	usage         map[string]*TokenUsage
	usageOrder    []string
}

var _ protocol.Adapter[Event] = (*Adapter)(nil)

func NewAdapter(opts Options) *Adapter {
	if opts.GenerateID == nil {
		opts.GenerateID = func(prefix string) string {
			return prefix + "-" + string(sharedrun.NewID())
		}
	}
	if opts.StepName == nil {
		opts.StepName = func(turn int) string { return fmt.Sprintf("turn-%d", turn) }
	}
	if opts.MapToolResult == nil {
		opts.MapToolResult = defaultToolResult
	}
	return &Adapter{
		opts: opts, streamToolIDs: make(map[int]string), tools: make(map[string]*toolState),
		fallbackTools: make(map[string][]string), usage: make(map[string]*TokenUsage),
	}
}

func (a *Adapter) Start(event protocol.RunEvent) ([]Event, error) {
	if a.started {
		return nil, errors.New("AG-UI adapter already started")
	}
	if event.RunID == "" {
		return nil, errors.New("AG-UI adapter requires a run ID")
	}
	if a.opts.ThreadID == "" {
		return nil, errors.New("AG-UI adapter requires a thread ID")
	}
	a.started = true
	a.runID = event.RunID
	return []Event{RunStartedEvent{
		BaseEvent: a.base(EventRunStarted), ThreadID: a.opts.ThreadID,
		RunID: string(event.RunID), ParentRunID: a.opts.ParentRunID, Input: a.opts.Input,
	}}, nil
}

func (a *Adapter) Encode(event protocol.AgentEvent) ([]Event, error) {
	if !a.started || a.finished {
		return nil, errors.New("AG-UI adapter is not active")
	}
	if event.RunID != a.runID {
		return nil, fmt.Errorf("AG-UI adapter run mismatch: got %q, want %q", event.RunID, a.runID)
	}

	var out []Event
	e := event.Event
	switch e.Type {
	case agent.EventTurnStart:
		out = append(out, a.closeMessage()...)
		if a.stepOpen {
			out = append(out, StepFinishedEvent{BaseEvent: a.base(EventStepFinished), StepName: a.stepName})
		}
		a.turn++
		a.stepName = a.opts.StepName(a.turn)
		if a.stepName == "" {
			return nil, errors.New("AG-UI step name must not be empty")
		}
		a.stepOpen = true
		out = append(out, StepStartedEvent{BaseEvent: a.base(EventStepStarted), StepName: a.stepName})

	case agent.EventMessageStart:
		if e.Message != nil && e.Message.Role == message.RoleAssistant {
			out = append(out, a.closeMessage()...)
			a.messageOpen = true
			a.messageID = a.opts.GenerateID("message")
			a.textEmitted = false
			a.reasoningStreamed = false
			out = append(out, TextMessageStartEvent{
				BaseEvent: a.base(EventTextMessageStart), MessageID: a.messageID, Role: "assistant",
			})
		}

	case agent.EventMessageUpdate:
		if e.Stream != nil {
			out = append(out, a.encodeStream(*e.Stream)...)
		}

	case agent.EventMessageEnd:
		if e.Message != nil && e.Message.Role == message.RoleAssistant {
			out = append(out, a.finishMessage(*e.Message)...)
			a.addUsage(*e.Message)
		}

	case agent.EventToolStart:
		if e.Call != nil {
			id := a.resolveToolID(e.Call.ID, e.Call.Name, true)
			out = append(out, a.ensureTool(id, e.Call.Name, a.messageID, string(e.Call.Arguments))...)
		}

	case agent.EventToolEnd:
		if e.Call != nil {
			id := a.resolveToolID(e.Call.ID, e.Call.Name, false)
			out = append(out, a.ensureTool(id, e.Call.Name, a.messageID, string(e.Call.Arguments))...)
			out = append(out, a.endTool(id)...)
			state := a.tools[id]
			if !state.resulted {
				content, err := a.opts.MapToolResult(e)
				if err != nil {
					return nil, fmt.Errorf("map AG-UI tool result: %w", err)
				}
				state.resulted = true
				out = append(out, ToolCallResultEvent{
					BaseEvent: a.base(EventToolCallResult), MessageID: a.opts.GenerateID("tool-result"),
					ToolCallID: id, Content: content, Role: "tool",
				})
			}
		}

	case agent.EventTurnEnd:
		out = append(out, a.closeMessage()...)
		if a.stepOpen {
			out = append(out, StepFinishedEvent{BaseEvent: a.base(EventStepFinished), StepName: a.stepName})
			a.stepOpen = false
		}

	case agent.EventError:
		if e.Err != nil {
			a.pendingErr = e.Err
		}
	}

	if a.opts.MapEvent != nil {
		extra, err := a.opts.MapEvent(event)
		if err != nil {
			return nil, err
		}
		out = append(out, extra...)
	}
	return out, nil
}

func (a *Adapter) Finish(event protocol.RunEvent) ([]Event, error) {
	if !a.started || a.finished {
		return nil, errors.New("AG-UI adapter is not active")
	}
	if event.RunID != a.runID {
		return nil, fmt.Errorf("AG-UI adapter run mismatch: got %q, want %q", event.RunID, a.runID)
	}
	a.finished = true
	out := a.closeMessage()
	for _, id := range a.toolOrder {
		out = append(out, a.endTool(id)...)
	}
	if a.stepOpen {
		out = append(out, StepFinishedEvent{BaseEvent: a.base(EventStepFinished), StepName: a.stepName})
		a.stepOpen = false
	}

	terminalErr := event.Err
	if terminalErr == nil {
		terminalErr = a.pendingErr
	}
	if event.Status == sharedrun.StatusFailed || event.Status == sharedrun.StatusAborted || terminalErr != nil {
		message := "run failed"
		if event.Status == sharedrun.StatusAborted {
			message = "run aborted"
		}
		if terminalErr != nil {
			message = terminalErr.Error()
		}
		out = append(out, RunErrorEvent{
			BaseEvent: a.base(EventRunError), Message: message,
			Code: string(event.Cause), Usage: a.usages(),
		})
		return out, nil
	}

	var result any
	if a.opts.MapRunResult != nil {
		result = a.opts.MapRunResult(event)
	}
	out = append(out, RunFinishedEvent{
		BaseEvent: a.base(EventRunFinished), ThreadID: a.opts.ThreadID, RunID: string(event.RunID),
		Result: result, Outcome: &RunFinishedOutcome{Type: "success"}, Usage: a.usages(),
	})
	return out, nil
}

func (a *Adapter) encodeStream(stream message.StreamEvent) []Event {
	var out []Event
	switch stream.Type {
	case message.EventTextDelta:
		if stream.Text != "" && a.messageOpen {
			out = append(out, a.closeReasoning()...)
			out = append(out, TextMessageContentEvent{
				BaseEvent: a.base(EventTextMessageContent), MessageID: a.messageID, Delta: stream.Text,
			})
			a.textEmitted = true
		}
	case message.EventThinkingDelta:
		if stream.Text != "" {
			a.reasoningStreamed = true
			if a.reasoningID == "" {
				a.reasoningID = a.opts.GenerateID("reasoning")
				out = append(out,
					ReasoningStartEvent{BaseEvent: a.base(EventReasoningStart), MessageID: a.reasoningID},
					ReasoningMessageStartEvent{BaseEvent: a.base(EventReasoningMessageStart), MessageID: a.reasoningID, Role: "reasoning"},
				)
			}
			out = append(out, ReasoningMessageContentEvent{
				BaseEvent: a.base(EventReasoningMessageContent), MessageID: a.reasoningID, Delta: stream.Text,
			})
		}
	case message.EventToolCallStart, message.EventToolCallDelta:
		out = append(out, a.closeReasoning()...)
		id := stream.ToolCallID
		if id == "" {
			id = a.streamToolIDs[stream.Index]
		}
		if id != "" && stream.ToolName != "" {
			a.streamToolIDs[stream.Index] = id
			out = append(out, a.ensureTool(id, stream.ToolName, a.messageID, "")...)
		}
		if id != "" && stream.ArgumentsDelta != "" {
			if state := a.tools[id]; state != nil {
				state.argsEmitted = true
				out = append(out, ToolCallArgsEvent{BaseEvent: a.base(EventToolCallArgs), ToolCallID: id, Delta: stream.ArgumentsDelta})
			}
		}
	case message.EventToolCallEnd:
		id := stream.ToolCallID
		if id == "" {
			id = a.streamToolIDs[stream.Index]
		}
		out = append(out, a.endTool(id)...)
	}
	return out
}

func (a *Adapter) finishMessage(message message.Message) []Event {
	if !a.messageOpen {
		return nil
	}
	var out []Event
	for _, content := range message.Content {
		switch content.Type {
		case "text", "largeText":
			if !a.textEmitted && content.Text != "" {
				out = append(out, TextMessageContentEvent{
					BaseEvent: a.base(EventTextMessageContent), MessageID: a.messageID, Delta: content.Text,
				})
			}
		case "thinking":
			if !a.reasoningStreamed && content.Thinking != "" {
				id := a.opts.GenerateID("reasoning")
				out = append(out,
					ReasoningStartEvent{BaseEvent: a.base(EventReasoningStart), MessageID: id},
					ReasoningMessageStartEvent{BaseEvent: a.base(EventReasoningMessageStart), MessageID: id, Role: "reasoning"},
					ReasoningMessageContentEvent{BaseEvent: a.base(EventReasoningMessageContent), MessageID: id, Delta: content.Thinking},
					ReasoningMessageEndEvent{BaseEvent: a.base(EventReasoningMessageEnd), MessageID: id},
					ReasoningEndEvent{BaseEvent: a.base(EventReasoningEnd), MessageID: id},
				)
			}
		case "toolCall":
			id := a.resolveToolID(content.ID, content.Name, true)
			out = append(out, a.ensureTool(id, content.Name, a.messageID, string(content.Arguments))...)
			out = append(out, a.endTool(id)...)
		}
	}
	a.textEmitted = a.textEmitted || message.Text() != ""
	out = append(out, a.closeMessage()...)
	return out
}

func (a *Adapter) closeMessage() []Event {
	var out []Event
	out = append(out, a.closeReasoning()...)
	if !a.messageOpen {
		return out
	}
	for _, id := range a.toolOrder {
		state := a.tools[id]
		if state.parentID == a.messageID {
			out = append(out, a.endTool(id)...)
		}
	}
	out = append(out, TextMessageEndEvent{BaseEvent: a.base(EventTextMessageEnd), MessageID: a.messageID})
	a.messageOpen = false
	a.textEmitted = false
	a.reasoningStreamed = false
	return out
}

func (a *Adapter) closeReasoning() []Event {
	if a.reasoningID == "" {
		return nil
	}
	id := a.reasoningID
	a.reasoningID = ""
	return []Event{
		ReasoningMessageEndEvent{BaseEvent: a.base(EventReasoningMessageEnd), MessageID: id},
		ReasoningEndEvent{BaseEvent: a.base(EventReasoningEnd), MessageID: id},
	}
}

func (a *Adapter) ensureTool(id, name, parentID, args string) []Event {
	if id == "" {
		id = a.opts.GenerateID("tool")
	}
	state := a.tools[id]
	if state == nil {
		state = &toolState{name: name, parentID: parentID}
		a.tools[id] = state
		a.toolOrder = append(a.toolOrder, id)
		out := []Event{ToolCallStartEvent{
			BaseEvent: a.base(EventToolCallStart), ToolCallID: id, ToolCallName: name, ParentMessageID: parentID,
		}}
		if args != "" {
			state.argsEmitted = true
			out = append(out, ToolCallArgsEvent{BaseEvent: a.base(EventToolCallArgs), ToolCallID: id, Delta: args})
		}
		return out
	}
	if !state.argsEmitted && args != "" {
		state.argsEmitted = true
		return []Event{ToolCallArgsEvent{BaseEvent: a.base(EventToolCallArgs), ToolCallID: id, Delta: args}}
	}
	return nil
}

func (a *Adapter) endTool(id string) []Event {
	if id == "" {
		return nil
	}
	state := a.tools[id]
	if state == nil || state.ended {
		return nil
	}
	state.ended = true
	return []Event{ToolCallEndEvent{BaseEvent: a.base(EventToolCallEnd), ToolCallID: id}}
}

func (a *Adapter) resolveToolID(id, name string, start bool) string {
	if id != "" {
		return id
	}
	if len(a.fallbackTools[name]) > 0 {
		id = a.fallbackTools[name][0]
		if !start {
			a.fallbackTools[name] = a.fallbackTools[name][1:]
		}
		return id
	}
	id = a.opts.GenerateID("tool")
	a.fallbackTools[name] = append(a.fallbackTools[name], id)
	return id
}

func (a *Adapter) addUsage(message message.Message) {
	u := message.Usage
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 && u.CacheReadTokens == 0 {
		return
	}
	key := message.Provider + "\x00" + message.Model
	usage := a.usage[key]
	if usage == nil {
		usage = &TokenUsage{Provider: message.Provider, Model: message.Model}
		a.usage[key] = usage
		a.usageOrder = append(a.usageOrder, key)
	}
	usage.InputTokens += u.InputTokens
	usage.OutputTokens += u.OutputTokens
	usage.TotalTokens += u.TotalTokens
	usage.CachedInputTokens += u.CacheReadTokens
}

func (a *Adapter) usages() []TokenUsage {
	if len(a.usageOrder) == 0 {
		return nil
	}
	out := make([]TokenUsage, 0, len(a.usageOrder))
	for _, key := range a.usageOrder {
		out = append(out, *a.usage[key])
	}
	return out
}

func (a *Adapter) base(kind EventType) BaseEvent {
	base := BaseEvent{Type: kind}
	if a.opts.Timestamp != nil {
		base.Timestamp = a.opts.Timestamp()
	}
	return base
}

func defaultToolResult(event agent.Event) (string, error) {
	if event.Err != nil {
		return event.Err.Error(), nil
	}
	if event.Result == nil {
		return "completed", nil
	}
	if event.Result.IsError {
		var text []string
		for _, content := range event.Result.Content {
			if content.Type == "text" && content.Text != "" {
				text = append(text, content.Text)
			}
		}
		if len(text) > 0 {
			return strings.Join(text, "\n"), nil
		}
		return "tool execution failed", nil
	}
	if len(event.Result.Content) == 1 && event.Result.Content[0].Type == "text" && event.Result.Details == nil && !event.Result.Terminate {
		return event.Result.Content[0].Text, nil
	}
	payload := struct {
		Content   []message.Content `json:"content,omitempty"`
		Details   any               `json:"details,omitempty"`
		Terminate bool              `json:"terminate,omitempty"`
	}{Content: event.Result.Content, Details: event.Result.Details, Terminate: event.Result.Terminate}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
