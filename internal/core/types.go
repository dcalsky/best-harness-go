package core

import (
	"github.com/dcalsky/best-harness-go/internal/agent"
	"github.com/dcalsky/best-harness-go/internal/builtin"
	"github.com/dcalsky/best-harness-go/internal/compact"
	"github.com/dcalsky/best-harness-go/internal/extension"
	"github.com/dcalsky/best-harness-go/internal/invocation"
	"github.com/dcalsky/best-harness-go/internal/message"
	"github.com/dcalsky/best-harness-go/internal/model"
	"github.com/dcalsky/best-harness-go/internal/prompt"
	"github.com/dcalsky/best-harness-go/internal/provider"
	"github.com/dcalsky/best-harness-go/internal/resource"
	sharedrun "github.com/dcalsky/best-harness-go/internal/run"
	store "github.com/dcalsky/best-harness-go/internal/session"
	"github.com/dcalsky/best-harness-go/internal/settings"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

// These aliases keep the core implementation independent from the public
// facade while ensuring every public wrapper refers to exactly the same types.
type (
	Settings                  = settings.Settings
	Setting[T any]            = settings.Setting[T]
	ToolRegistry              = tool.Registry
	ToolSpec                  = tool.Spec
	Tool[P, D any]            = tool.Tool[P, D]
	ArgumentsValidator[P any] = tool.ArgumentsValidator[P]
	StructValidator           = tool.StructValidator
	ToolCall                  = tool.ToolCall
	ToolResult[D any]         = tool.ToolResult[D]
	ToolOutcome               = invocation.ToolOutcome
	ToolBatch                 = invocation.ToolBatch
	Update[D any]             = tool.Update[D]
	Result                    = tool.Result
	ToolExecutionMode         = tool.ExecutionMode
	Model                     = model.Model
	ModelRegistry             = model.Registry
	ResourceRegistry          = resource.Registry
	ResourceSnapshot          = resource.Snapshot
	ResourceLoadRequest       = resource.LoadRequest
	ResourcePromptOptions     = resource.PromptOptions
	ShellExecutor             = builtin.ShellExecutor
	ShellResult               = builtin.ShellResult
	BuiltinConfig             = builtin.Config
	Extension[S any]          = extension.Extension[S]
	ExtensionRegistry[S any]  = extension.Registry[S]
	RequestContextHook[S any] = extension.RequestContextHook[S]
	RequestHook[S any]        = extension.RequestHook[S]
	ToolContextHook[S any]    = extension.ToolContextHook[S]
	ContextHook[S any]        = extension.ContextHook[S]
	Agent                     = agent.Agent
	AgentRun                  = agent.Run
	AgentQueueMode            = agent.QueueMode
	AgentLifecycleEvent       = agent.Event
	Summarizer                = compact.Summarizer
	CompactionOptions         = compact.Options
	CompactionReason          = compact.Reason
	CompactionResult          = compact.Result
	GenerationConfig          = provider.GenerationConfig
	Provider                  = provider.Provider
	Request                   = provider.Request
	Stream                    = provider.Stream
	Message                   = message.Message
	Sequence                  = prompt.Sequence
	Context[S any]            = invocation.Context[S]
	InvocationGate            = invocation.Gate
	ID                        = sharedrun.ID
	Status                    = sharedrun.Status
	Cause                     = sharedrun.Cause
	EndReason                 = sharedrun.EndReason
	RunStats                  = sharedrun.Stats
	RunOutcome                = sharedrun.Outcome
	PanicError                = sharedrun.PanicError
	Info                      = sharedrun.Info
	Persistence               = store.Persistence
	PersistenceOptions        = store.Options
	SessionManager            = store.Manager
	SessionHeader             = store.Header
	SessionEntry              = store.Entry
	SessionEntryID            = store.EntryID
	SessionInfo               = store.Info
	SessionSnapshot           = store.Snapshot
	SessionContext            = store.Context
	SessionTreeNode           = store.TreeNode
	SessionCustomEntry[T any] = store.CustomEntry[T]
)

const (
	RoleUser = message.RoleUser

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

	AgentEventMessageEnd = agent.EventMessageEnd
)

var (
	ErrDuplicateID     = sharedrun.ErrDuplicateID
	ErrNoPendingInput  = sharedrun.ErrNoPendingInput
	ErrNextUnavailable = sharedrun.ErrNextUnavailable
)

func NewID() ID                                   { return sharedrun.NewID() }
func UserText(text string) prompt.UserMessageStep { return prompt.UserText(text) }
