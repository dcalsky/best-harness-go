// Package agui adapts harness events to the Agent User Interaction (AG-UI)
// protocol. The event types in this file mirror the protocol's JSON shapes and
// can also be returned by Options.MapEvent for application-specific streams.
package agui

type EventType string

const (
	EventTextMessageStart           EventType = "TEXT_MESSAGE_START"
	EventTextMessageContent         EventType = "TEXT_MESSAGE_CONTENT"
	EventTextMessageEnd             EventType = "TEXT_MESSAGE_END"
	EventTextMessageChunk           EventType = "TEXT_MESSAGE_CHUNK"
	EventToolCallStart              EventType = "TOOL_CALL_START"
	EventToolCallArgs               EventType = "TOOL_CALL_ARGS"
	EventToolCallEnd                EventType = "TOOL_CALL_END"
	EventToolCallChunk              EventType = "TOOL_CALL_CHUNK"
	EventToolCallResult             EventType = "TOOL_CALL_RESULT"
	EventStateSnapshot              EventType = "STATE_SNAPSHOT"
	EventStateDelta                 EventType = "STATE_DELTA"
	EventMessagesSnapshot           EventType = "MESSAGES_SNAPSHOT"
	EventActivitySnapshot           EventType = "ACTIVITY_SNAPSHOT"
	EventActivityDelta              EventType = "ACTIVITY_DELTA"
	EventRaw                        EventType = "RAW"
	EventCustom                     EventType = "CUSTOM"
	EventRunStarted                 EventType = "RUN_STARTED"
	EventRunFinished                EventType = "RUN_FINISHED"
	EventRunError                   EventType = "RUN_ERROR"
	EventStepStarted                EventType = "STEP_STARTED"
	EventStepFinished               EventType = "STEP_FINISHED"
	EventReasoningStart             EventType = "REASONING_START"
	EventReasoningMessageStart      EventType = "REASONING_MESSAGE_START"
	EventReasoningMessageContent    EventType = "REASONING_MESSAGE_CONTENT"
	EventReasoningMessageEnd        EventType = "REASONING_MESSAGE_END"
	EventReasoningMessageChunk      EventType = "REASONING_MESSAGE_CHUNK"
	EventReasoningEnd               EventType = "REASONING_END"
	EventReasoningEncryptedValue    EventType = "REASONING_ENCRYPTED_VALUE"
	EventSubagentStarted            EventType = "SUBAGENT_STARTED"
	EventSubagentFinished           EventType = "SUBAGENT_FINISHED"
	EventSubagentError              EventType = "SUBAGENT_ERROR"
	EventThinkingStart              EventType = "THINKING_START" // Deprecated: use EventReasoningStart.
	EventThinkingEnd                EventType = "THINKING_END"   // Deprecated: use EventReasoningEnd.
	EventThinkingTextMessageStart   EventType = "THINKING_TEXT_MESSAGE_START"
	EventThinkingTextMessageContent EventType = "THINKING_TEXT_MESSAGE_CONTENT"
	EventThinkingTextMessageEnd     EventType = "THINKING_TEXT_MESSAGE_END"
)

// Event is implemented by every protocol event below. Kind reports the wire
// discriminator without requiring a type switch.
type Event interface {
	Kind() EventType
}

type BaseEvent struct {
	Type      EventType      `json:"type"`
	Timestamp int64          `json:"timestamp,omitempty"`
	RawEvent  any            `json:"rawEvent,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

func (e BaseEvent) Kind() EventType { return e.Type }

type RunStartedEvent struct {
	BaseEvent
	ThreadID    string `json:"threadId"`
	RunID       string `json:"runId"`
	ParentRunID string `json:"parentRunId,omitempty"`
	Input       any    `json:"input,omitempty"`
}

type TokenUsage struct {
	Provider          string `json:"provider,omitempty"`
	Model             string `json:"model,omitempty"`
	InputTokens       int64  `json:"inputTokens,omitempty"`
	OutputTokens      int64  `json:"outputTokens,omitempty"`
	TotalTokens       int64  `json:"totalTokens,omitempty"`
	ReasoningTokens   int64  `json:"reasoningTokens,omitempty"`
	CachedInputTokens int64  `json:"cachedInputTokens,omitempty"`
}

type RunFinishedOutcome struct {
	Type       string `json:"type"`
	Interrupts []any  `json:"interrupts,omitempty"`
}

type RunFinishedEvent struct {
	BaseEvent
	ThreadID string              `json:"threadId"`
	RunID    string              `json:"runId"`
	Result   any                 `json:"result,omitempty"`
	Outcome  *RunFinishedOutcome `json:"outcome,omitempty"`
	Usage    []TokenUsage        `json:"usage,omitempty"`
}

type RunErrorEvent struct {
	BaseEvent
	Message string       `json:"message"`
	Code    string       `json:"code,omitempty"`
	Usage   []TokenUsage `json:"usage,omitempty"`
}

type StepStartedEvent struct {
	BaseEvent
	StepName      string `json:"stepName"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type StepFinishedEvent struct {
	BaseEvent
	StepName      string `json:"stepName"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type TextMessageStartEvent struct {
	BaseEvent
	MessageID     string `json:"messageId"`
	Role          string `json:"role"`
	Name          string `json:"name,omitempty"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type TextMessageContentEvent struct {
	BaseEvent
	MessageID     string `json:"messageId"`
	Delta         string `json:"delta"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type TextMessageEndEvent struct {
	BaseEvent
	MessageID     string `json:"messageId"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type TextMessageChunkEvent struct {
	BaseEvent
	MessageID     string `json:"messageId,omitempty"`
	Role          string `json:"role,omitempty"`
	Delta         string `json:"delta,omitempty"`
	Name          string `json:"name,omitempty"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type ToolCallStartEvent struct {
	BaseEvent
	ToolCallID      string `json:"toolCallId"`
	ToolCallName    string `json:"toolCallName"`
	ParentMessageID string `json:"parentMessageId,omitempty"`
	SubagentRunID   string `json:"subagentRunId,omitempty"`
}

type ToolCallArgsEvent struct {
	BaseEvent
	ToolCallID    string `json:"toolCallId"`
	Delta         string `json:"delta"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type ToolCallEndEvent struct {
	BaseEvent
	ToolCallID    string `json:"toolCallId"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type ToolCallResultEvent struct {
	BaseEvent
	MessageID     string `json:"messageId"`
	ToolCallID    string `json:"toolCallId"`
	Content       string `json:"content"`
	Role          string `json:"role,omitempty"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type ToolCallChunkEvent struct {
	BaseEvent
	ToolCallID      string `json:"toolCallId,omitempty"`
	ToolCallName    string `json:"toolCallName,omitempty"`
	ParentMessageID string `json:"parentMessageId,omitempty"`
	Delta           string `json:"delta,omitempty"`
	SubagentRunID   string `json:"subagentRunId,omitempty"`
}

type ReasoningStartEvent struct {
	BaseEvent
	MessageID     string `json:"messageId"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type ReasoningMessageStartEvent struct {
	BaseEvent
	MessageID     string `json:"messageId"`
	Role          string `json:"role"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type ReasoningMessageContentEvent struct {
	BaseEvent
	MessageID     string `json:"messageId"`
	Delta         string `json:"delta"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type ReasoningMessageEndEvent struct {
	BaseEvent
	MessageID     string `json:"messageId"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type ReasoningEndEvent struct {
	BaseEvent
	MessageID     string `json:"messageId"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type ReasoningMessageChunkEvent struct {
	BaseEvent
	MessageID     string `json:"messageId,omitempty"`
	Delta         string `json:"delta,omitempty"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type ReasoningEncryptedValueEvent struct {
	BaseEvent
	Subtype        string `json:"subtype"`
	EntityID       string `json:"entityId"`
	EncryptedValue string `json:"encryptedValue"`
	SubagentRunID  string `json:"subagentRunId,omitempty"`
}

// JSONPatchOperation is an RFC 6902 operation used by state and activity
// deltas. Value is omitted for operations such as remove.
type JSONPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	From  string `json:"from,omitempty"`
	Value any    `json:"value,omitempty"`
}

type StateSnapshotEvent struct {
	BaseEvent
	Snapshot      any    `json:"snapshot"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type StateDeltaEvent struct {
	BaseEvent
	Delta         []JSONPatchOperation `json:"delta"`
	SubagentRunID string               `json:"subagentRunId,omitempty"`
}

type MessagesSnapshotEvent struct {
	BaseEvent
	Messages []any `json:"messages"`
}

type ActivitySnapshotEvent struct {
	BaseEvent
	MessageID     string         `json:"messageId"`
	ActivityType  string         `json:"activityType"`
	Content       map[string]any `json:"content"`
	Replace       *bool          `json:"replace,omitempty"`
	SubagentRunID string         `json:"subagentRunId,omitempty"`
}

type ActivityDeltaEvent struct {
	BaseEvent
	MessageID     string               `json:"messageId"`
	ActivityType  string               `json:"activityType"`
	Patch         []JSONPatchOperation `json:"patch"`
	SubagentRunID string               `json:"subagentRunId,omitempty"`
}

type RawEvent struct {
	BaseEvent
	Event         any    `json:"event"`
	Source        string `json:"source,omitempty"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type CustomEvent struct {
	BaseEvent
	Name          string `json:"name"`
	Value         any    `json:"value"`
	SubagentRunID string `json:"subagentRunId,omitempty"`
}

type SubagentStartedEvent struct {
	BaseEvent
	SubagentRunID       string `json:"subagentRunId"`
	Name                string `json:"name"`
	Description         string `json:"description,omitempty"`
	ParentSubagentRunID string `json:"parentSubagentRunId,omitempty"`
	ParentToolCallID    string `json:"parentToolCallId,omitempty"`
	ParentMessageID     string `json:"parentMessageId,omitempty"`
}

type SubagentFinishedOutcome struct {
	Type         string   `json:"type"`
	InterruptIDs []string `json:"interruptIds,omitempty"`
}

type SubagentFinishedEvent struct {
	BaseEvent
	SubagentRunID string                   `json:"subagentRunId"`
	Result        any                      `json:"result,omitempty"`
	Outcome       *SubagentFinishedOutcome `json:"outcome,omitempty"`
}

type SubagentErrorEvent struct {
	BaseEvent
	SubagentRunID string `json:"subagentRunId"`
	Message       string `json:"message"`
	Code          string `json:"code,omitempty"`
}

func Custom(name string, value any) CustomEvent {
	return CustomEvent{BaseEvent: BaseEvent{Type: EventCustom}, Name: name, Value: value}
}

func StateSnapshot(snapshot any) StateSnapshotEvent {
	return StateSnapshotEvent{BaseEvent: BaseEvent{Type: EventStateSnapshot}, Snapshot: snapshot}
}

func StateDelta(delta ...JSONPatchOperation) StateDeltaEvent {
	return StateDeltaEvent{BaseEvent: BaseEvent{Type: EventStateDelta}, Delta: delta}
}
