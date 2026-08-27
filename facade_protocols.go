package harness

import (
	"io"
	"net/http"

	"github.com/dcalsky/best-harness-go/internal/agui"
	"github.com/dcalsky/best-harness-go/internal/vercelai"
)

type (
	AGUIAdapter                      = agui.Adapter
	AGUIOptions                      = agui.Options
	AGUIEvent                        = agui.Event
	AGUIEventType                    = agui.EventType
	AGUIEventMapper                  = agui.EventMapper
	AGUIToolResultMapper             = agui.ToolResultMapper
	AGUIBaseEvent                    = agui.BaseEvent
	AGUIRunStartedEvent              = agui.RunStartedEvent
	AGUITokenUsage                   = agui.TokenUsage
	AGUIRunFinishedOutcome           = agui.RunFinishedOutcome
	AGUIRunFinishedEvent             = agui.RunFinishedEvent
	AGUIRunErrorEvent                = agui.RunErrorEvent
	AGUIStepStartedEvent             = agui.StepStartedEvent
	AGUIStepFinishedEvent            = agui.StepFinishedEvent
	AGUITextMessageStartEvent        = agui.TextMessageStartEvent
	AGUITextMessageContentEvent      = agui.TextMessageContentEvent
	AGUITextMessageEndEvent          = agui.TextMessageEndEvent
	AGUITextMessageChunkEvent        = agui.TextMessageChunkEvent
	AGUIToolCallStartEvent           = agui.ToolCallStartEvent
	AGUIToolCallArgsEvent            = agui.ToolCallArgsEvent
	AGUIToolCallEndEvent             = agui.ToolCallEndEvent
	AGUIToolCallResultEvent          = agui.ToolCallResultEvent
	AGUIToolCallChunkEvent           = agui.ToolCallChunkEvent
	AGUIReasoningStartEvent          = agui.ReasoningStartEvent
	AGUIReasoningMessageStartEvent   = agui.ReasoningMessageStartEvent
	AGUIReasoningMessageContentEvent = agui.ReasoningMessageContentEvent
	AGUIReasoningMessageEndEvent     = agui.ReasoningMessageEndEvent
	AGUIReasoningEndEvent            = agui.ReasoningEndEvent
	AGUIReasoningMessageChunkEvent   = agui.ReasoningMessageChunkEvent
	AGUIReasoningEncryptedValueEvent = agui.ReasoningEncryptedValueEvent
	AGUIJSONPatchOperation           = agui.JSONPatchOperation
	AGUIStateSnapshotEvent           = agui.StateSnapshotEvent
	AGUIStateDeltaEvent              = agui.StateDeltaEvent
	AGUIMessagesSnapshotEvent        = agui.MessagesSnapshotEvent
	AGUIActivitySnapshotEvent        = agui.ActivitySnapshotEvent
	AGUIActivityDeltaEvent           = agui.ActivityDeltaEvent
	AGUIRawEvent                     = agui.RawEvent
	AGUICustomEvent                  = agui.CustomEvent
	AGUISubagentStartedEvent         = agui.SubagentStartedEvent
	AGUISubagentFinishedOutcome      = agui.SubagentFinishedOutcome
	AGUISubagentFinishedEvent        = agui.SubagentFinishedEvent
	AGUISubagentErrorEvent           = agui.SubagentErrorEvent
	AGUISSEEncoder                   = agui.SSEEncoder

	VercelAIAdapter     = vercelai.Adapter
	VercelAIOptions     = vercelai.Options
	VercelAIEventMapper = vercelai.EventMapper
	VercelAIChunk       = vercelai.Chunk
	VercelAISSEEncoder  = vercelai.SSEEncoder
)

const (
	AGUIEventTextMessageStart           = agui.EventTextMessageStart
	AGUIEventTextMessageContent         = agui.EventTextMessageContent
	AGUIEventTextMessageEnd             = agui.EventTextMessageEnd
	AGUIEventTextMessageChunk           = agui.EventTextMessageChunk
	AGUIEventToolCallStart              = agui.EventToolCallStart
	AGUIEventToolCallArgs               = agui.EventToolCallArgs
	AGUIEventToolCallEnd                = agui.EventToolCallEnd
	AGUIEventToolCallChunk              = agui.EventToolCallChunk
	AGUIEventToolCallResult             = agui.EventToolCallResult
	AGUIEventStateSnapshot              = agui.EventStateSnapshot
	AGUIEventStateDelta                 = agui.EventStateDelta
	AGUIEventMessagesSnapshot           = agui.EventMessagesSnapshot
	AGUIEventActivitySnapshot           = agui.EventActivitySnapshot
	AGUIEventActivityDelta              = agui.EventActivityDelta
	AGUIEventRaw                        = agui.EventRaw
	AGUIEventCustom                     = agui.EventCustom
	AGUIEventRunStarted                 = agui.EventRunStarted
	AGUIEventRunFinished                = agui.EventRunFinished
	AGUIEventRunError                   = agui.EventRunError
	AGUIEventStepStarted                = agui.EventStepStarted
	AGUIEventStepFinished               = agui.EventStepFinished
	AGUIEventReasoningStart             = agui.EventReasoningStart
	AGUIEventReasoningMessageStart      = agui.EventReasoningMessageStart
	AGUIEventReasoningMessageContent    = agui.EventReasoningMessageContent
	AGUIEventReasoningMessageEnd        = agui.EventReasoningMessageEnd
	AGUIEventReasoningMessageChunk      = agui.EventReasoningMessageChunk
	AGUIEventReasoningEnd               = agui.EventReasoningEnd
	AGUIEventReasoningEncryptedValue    = agui.EventReasoningEncryptedValue
	AGUIEventSubagentStarted            = agui.EventSubagentStarted
	AGUIEventSubagentFinished           = agui.EventSubagentFinished
	AGUIEventSubagentError              = agui.EventSubagentError
	AGUIEventThinkingStart              = agui.EventThinkingStart
	AGUIEventThinkingEnd                = agui.EventThinkingEnd
	AGUIEventThinkingTextMessageStart   = agui.EventThinkingTextMessageStart
	AGUIEventThinkingTextMessageContent = agui.EventThinkingTextMessageContent
	AGUIEventThinkingTextMessageEnd     = agui.EventThinkingTextMessageEnd
)

func NewAGUIAdapter(opts AGUIOptions) *AGUIAdapter          { return agui.NewAdapter(opts) }
func SetAGUIHeaders(header http.Header)                     { agui.SetHeaders(header) }
func NewAGUISSEEncoder(w io.Writer) *AGUISSEEncoder         { return agui.NewSSEEncoder(w) }
func AGUICustom(name string, value any) AGUICustomEvent     { return agui.Custom(name, value) }
func AGUIStateSnapshot(snapshot any) AGUIStateSnapshotEvent { return agui.StateSnapshot(snapshot) }
func AGUIStateDelta(delta ...AGUIJSONPatchOperation) AGUIStateDeltaEvent {
	return agui.StateDelta(delta...)
}

func NewVercelAIAdapter(opts VercelAIOptions) *VercelAIAdapter {
	return vercelai.NewAdapter(opts)
}
func SetVercelAIHeaders(header http.Header)                 { vercelai.SetHeaders(header) }
func NewVercelAISSEEncoder(w io.Writer) *VercelAISSEEncoder { return vercelai.NewSSEEncoder(w) }
func VercelAIData(name string, value any, transient bool) (VercelAIChunk, error) {
	return vercelai.Data(name, value, transient)
}
