package harness

import (
	"github.com/dcalsky/best-harness-go/internal/invocation"
	"github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	"github.com/dcalsky/best-harness-go/internal/prompt"
	"github.com/dcalsky/best-harness-go/internal/provider"
	sharedrun "github.com/dcalsky/best-harness-go/internal/run"
	store "github.com/dcalsky/best-harness-go/internal/session"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

// Message protocol types are aliases so values remain directly assignable to
// the internal protocol packages used by providers, sessions, and tools.
type (
	JSONCodec            = jsoncodec.Codec
	JSONCodecFuncs       = jsoncodec.Funcs
	Content              = message.Content
	Cost                 = message.Cost
	ImageContent         = message.ImageContent
	Message              = message.Message
	Origin               = message.Origin
	ProviderError        = message.ProviderError
	Role                 = message.Role
	StopReason           = message.StopReason
	StreamEvent          = message.StreamEvent
	StreamEventType      = message.StreamEventType
	TextContent          = message.TextContent
	ThinkingContent      = message.ThinkingContent
	ToolCallContent      = message.ToolCallContent
	Usage                = message.Usage
	API                  = model.API
	Model                = model.Model
	ModelRegistry        = model.Registry
	AssistantMessageStep = prompt.AssistantMessageStep
	OnErrorPolicy        = prompt.OnErrorPolicy
	Sequence             = prompt.Sequence
	Step                 = prompt.Step
	StepType             = prompt.StepType
	PromptToolCall       = prompt.ToolCall
	ToolCallsStep        = prompt.ToolCallsStep
	UserMessageStep      = prompt.UserMessageStep
)

// Run lifecycle types describe IDs, terminal states, and persisted metadata.
type (
	ID         = sharedrun.ID
	Status     = sharedrun.Status
	Cause      = sharedrun.Cause
	EndReason  = sharedrun.EndReason
	RunStats   = sharedrun.Stats
	RunOutcome = sharedrun.Outcome
	PanicError = sharedrun.PanicError
	Info       = sharedrun.Info
)

// Tool types retain their original identity, including the Go 1.27 generic
// Register method on ToolRegistry.
type (
	AfterHook                 = tool.AfterHook
	BeforeHook                = tool.BeforeHook
	BlockedError              = tool.BlockedError
	ToolExecutionMode         = tool.ExecutionMode
	Prepared                  = tool.Prepared
	ToolRegistry              = tool.Registry
	ToolSpec                  = tool.Spec
	Result                    = tool.Result
	Tool[P, D any]            = tool.Tool[P, D]
	ArgumentsValidator[P any] = tool.ArgumentsValidator[P]
	StructValidator           = tool.StructValidator
	ToolCall                  = tool.ToolCall
	ToolResult[D any]         = tool.ToolResult[D]
	Update[D any]             = tool.Update[D]
	Context[S any]            = invocation.Context[S]
	Persistence               = store.Persistence
	SessionInfo               = store.Info
)

// Provider types define the provider-neutral streaming boundary. ProviderTool
// is named explicitly to distinguish it from the generic Tool definition.
type (
	Faux             = provider.Faux
	Provider         = provider.Provider
	Request          = provider.Request
	GenerationConfig = provider.GenerationConfig
	SliceStream      = provider.SliceStream
	Stream           = provider.Stream
	ProviderTool     = provider.Tool
)

const (
	DefaultLargeTextMaxChars = message.DefaultLargeTextMaxChars

	OriginUser   = message.OriginUser
	OriginModel  = message.OriginModel
	OriginScript = message.OriginScript
	OriginTool   = message.OriginTool

	RoleUser      = message.RoleUser
	RoleAssistant = message.RoleAssistant
	RoleTool      = message.RoleTool

	APIOpenAI          = model.APIOpenAI
	APIOpenAIResponses = model.APIOpenAIResponses
	APIAnthropic       = model.APIAnthropic

	StopStop    = message.StopStop
	StopLength  = message.StopLength
	StopToolUse = message.StopToolUse
	StopError   = message.StopError
	StopAborted = message.StopAborted

	EventStart         = message.EventStart
	EventTextDelta     = message.EventTextDelta
	EventThinkingDelta = message.EventThinkingDelta
	EventToolCallStart = message.EventToolCallStart
	EventToolCallDelta = message.EventToolCallDelta
	EventToolCallEnd   = message.EventToolCallEnd
	EventDone          = message.EventDone
	EventError         = message.EventError

	OnErrorEnterAgentLoop = prompt.OnErrorEnterAgentLoop
	OnErrorContinue       = prompt.OnErrorContinue
	OnErrorAbort          = prompt.OnErrorAbort

	StepUserMessage      = prompt.StepUserMessage
	StepAssistantMessage = prompt.StepAssistantMessage
	StepToolCalls        = prompt.StepToolCalls

	StatusRunning    = sharedrun.StatusRunning
	StatusCancelling = sharedrun.StatusCancelling
	StatusCompleted  = sharedrun.StatusCompleted
	StatusAborted    = sharedrun.StatusAborted
	StatusFailed     = sharedrun.StatusFailed

	CauseNone           = sharedrun.CauseNone
	CauseUserAbort      = sharedrun.CauseUserAbort
	CauseParentCanceled = sharedrun.CauseParentCanceled
	CauseDeadline       = sharedrun.CauseDeadline
	CauseProviderAbort  = sharedrun.CauseProviderAbort
	CauseInterrupted    = sharedrun.CauseInterrupted
	CauseInternal       = sharedrun.CauseInternal

	EndReasonNone          = sharedrun.EndReasonNone
	EndReasonAssistantStop = sharedrun.EndReasonAssistantStop
	EndReasonPromptDone    = sharedrun.EndReasonPromptDone
	EndReasonToolTerminate = sharedrun.EndReasonToolTerminate
	EndReasonLoopStopped   = sharedrun.EndReasonLoopStopped

	Sequential = tool.Sequential
	Parallel   = tool.Parallel
)

var (
	ErrInvalidJSONCodec = jsoncodec.ErrInvalid
	ErrJSONCodecFrozen  = jsoncodec.ErrFrozen

	ErrContextOverflow = message.ErrContextOverflow
	ErrModelNotFound   = model.ErrModelNotFound

	ErrAborted         = sharedrun.ErrAborted
	ErrInterrupted     = sharedrun.ErrInterrupted
	ErrFinished        = sharedrun.ErrFinished
	ErrNotFound        = sharedrun.ErrNotFound
	ErrDuplicateID     = sharedrun.ErrDuplicateID
	ErrInvalidID       = sharedrun.ErrInvalidID
	ErrNoPendingInput  = sharedrun.ErrNoPendingInput
	ErrNextUnavailable = sharedrun.ErrNextUnavailable

	ErrToolNotFound      = tool.ErrNotFound
	ErrStaleContext      = invocation.ErrStaleContext
	ErrActionUnavailable = invocation.ErrActionUnavailable
	ErrStateReadOnly     = invocation.ErrStateReadOnly
	ErrStateBusy         = invocation.ErrStateBusy
)

// SetJSONCodec replaces the process-wide JSON implementation. It must be
// called before the first SDK operation that encodes or decodes JSON.
func SetJSONCodec(codec JSONCodec) error { return jsoncodec.Set(codec) }

// StandardJSONCodec returns the default encoding/json implementation.
func StandardJSONCodec() JSONCodec { return jsoncodec.Standard() }

func Text(text string) Content                       { return message.Text(text) }
func LargeText(text string, maxChars ...int) Content { return message.LargeText(text, maxChars...) }
func Thinking(text string) Content                   { return message.Thinking(text) }
func Image(data, mimeType string) Content            { return message.Image(data, mimeType) }
func NewToolCallContent(id, name string, args jsoncodec.RawMessage) Content {
	return message.ToolCall(id, name, args)
}
func User(text string) Message                     { return message.User(text) }
func ExpandLargeText(messages []Message) []Message { return message.ExpandLargeText(messages) }
func NormalizeMessagesForProvider(messages []Message) []Message {
	return message.NormalizeForProvider(messages)
}

func NewModelRegistry() *ModelRegistry { return model.NewRegistry() }

func Ptr[T any](value T) *T { return provider.Ptr(value) }

func UserText(text string) UserMessageStep           { return prompt.UserText(text) }
func AssistantText(text string) AssistantMessageStep { return prompt.AssistantText(text) }
func Tools(calls ...PromptToolCall) ToolCallsStep    { return prompt.Tools(calls...) }

func NewID() ID                   { return sharedrun.NewID() }
func ResolveID(id ID) (ID, error) { return sharedrun.ResolveID(id) }
func Terminal(status Status) bool { return sharedrun.Terminal(status) }

func NewToolRegistry() *ToolRegistry             { return tool.NewRegistry() }
func Block(reason string, terminate bool) error  { return tool.Block(reason, terminate) }
func SchemaOf[T any]() (SchemaDefinition, error) { return tool.SchemaOf[T]() }
