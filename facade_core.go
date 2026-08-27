package harness

import (
	"context"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	openaisdk "github.com/openai/openai-go/v3"

	"github.com/dcalsky/best-harness-go/internal/agent"
	"github.com/dcalsky/best-harness-go/internal/builtin"
	"github.com/dcalsky/best-harness-go/internal/compact"
	"github.com/dcalsky/best-harness-go/internal/extension"
	"github.com/dcalsky/best-harness-go/internal/invocation"
	"github.com/dcalsky/best-harness-go/internal/jsonschema"
	"github.com/dcalsky/best-harness-go/internal/provider/anthropic"
	openaiadapter "github.com/dcalsky/best-harness-go/internal/provider/openai"
	"github.com/dcalsky/best-harness-go/internal/resource"
	"github.com/dcalsky/best-harness-go/internal/resource/fsloader"
	store "github.com/dcalsky/best-harness-go/internal/session"
	"github.com/dcalsky/best-harness-go/internal/settings"
)

// Agent lifecycle contracts are exposed for event handling and for advanced
// callers that need to drive the provider-neutral agent loop directly.
type (
	AgentLifecycleEvent      = agent.Event
	AgentEventType           = agent.EventType
	AgentQueueMode           = agent.QueueMode
	AgentOptions             = agent.Options
	AgentPrompt              = agent.Prompt
	Agent                    = agent.Agent
	AgentRun                 = agent.Run
	AgentStartOptions        = agent.StartOptions
	ScriptToolError          = agent.ScriptToolError
	ValidatorRetryLimitError = agent.ValidatorRetryLimitError
	PrepareNextTurn          = agent.PrepareNextTurn
	ShouldStopAfterTurn      = agent.ShouldStopAfterTurn
	BeforeAgentToolCall      = agent.BeforeToolCall
	AfterAgentToolCall       = agent.AfterToolCall
	InvocationActions        = invocation.Actions
	InvocationConfig[S any]  = invocation.Config[S]
	InvocationGate           = invocation.Gate
	ToolBatch                = invocation.ToolBatch
	ToolBatchCoordinator     = invocation.ToolBatchCoordinator
	ToolOutcome              = invocation.ToolOutcome
	SchemaDataType           = jsonschema.DataType
	SchemaDefinition         = jsonschema.Definition
)

const (
	QueueAll        = agent.QueueAll
	QueueOneAtATime = agent.QueueOneAtATime

	AgentEventAgentStart    = agent.EventAgentStart
	AgentEventAgentEnd      = agent.EventAgentEnd
	AgentEventTurnStart     = agent.EventTurnStart
	AgentEventTurnEnd       = agent.EventTurnEnd
	AgentEventMessageStart  = agent.EventMessageStart
	AgentEventMessageUpdate = agent.EventMessageUpdate
	AgentEventMessageEnd    = agent.EventMessageEnd
	AgentEventToolStart     = agent.EventToolStart
	AgentEventToolUpdate    = agent.EventToolUpdate
	AgentEventToolEnd       = agent.EventToolEnd
	AgentEventError         = agent.EventError
	AgentEventQueueUpdate   = agent.EventQueueUpdate

	SchemaObject  = jsonschema.Object
	SchemaNumber  = jsonschema.Number
	SchemaInteger = jsonschema.Integer
	SchemaString  = jsonschema.String
	SchemaArray   = jsonschema.Array
	SchemaNull    = jsonschema.Null
	SchemaBoolean = jsonschema.Boolean
)

var (
	ErrAgentBusy            = agent.ErrBusy
	ErrProviderAborted      = agent.ErrProviderAborted
	ErrContextUnavailable   = invocation.ErrContextUnavailable
	ErrCompactionCancelled  = compact.ErrCancelled
	ErrNothingToCompact     = compact.ErrNothingToCompact
	ErrSessionClosed        = store.ErrClosed
	ErrSessionEntryNotFound = store.ErrEntryNotFound
	ErrSessionWriterActive  = store.ErrWriterActive
	ErrSessionConverter     = store.ErrConverterRequired
)

func NewAgent(opts AgentOptions) *Agent  { return agent.New(opts) }
func NewInvocationGate() *InvocationGate { return invocation.NewGate() }
func NewInvocationContext[S any](cfg InvocationConfig[S]) Context[S] {
	return invocation.NewContext(cfg)
}
func WithInvocationContext[S any](ctx context.Context, typed Context[S]) context.Context {
	return invocation.WithTypedContext(ctx, typed)
}
func InvocationContextFrom[S any](ctx context.Context) (Context[S], error) {
	return invocation.FromContext[S](ctx)
}

// Built-in tool contracts and constructors.
type (
	BuiltinConfig   = builtin.Config
	FileSystem      = builtin.FileSystem
	OSFileSystem    = builtin.OSFileSystem
	OutputStore     = builtin.OutputStore
	MutationQueue   = builtin.MutationQueue
	ShellExecutor   = builtin.ShellExecutor
	OSShellExecutor = builtin.OSShellExecutor
	ShellResult     = builtin.ShellResult
	Truncation      = builtin.Truncation
	ReadParams      = builtin.ReadParams
	ReadDetails     = builtin.ReadDetails
	WriteParams     = builtin.WriteParams
	WriteDetails    = builtin.WriteDetails
	EditParams      = builtin.EditParams
	EditDetails     = builtin.EditDetails
	BashParams      = builtin.BashParams
	BashDetails     = builtin.BashDetails
	GrepParams      = builtin.GrepParams
	GrepDetails     = builtin.GrepDetails
	FindParams      = builtin.FindParams
	FindDetails     = builtin.FindDetails
	LSParams        = builtin.LSParams
	LSDetails       = builtin.LSDetails
)

func NewMutationQueue() *MutationQueue                               { return builtin.NewMutationQueue() }
func ReadTool(config BuiltinConfig) Tool[ReadParams, ReadDetails]    { return builtin.Read(config) }
func WriteTool(config BuiltinConfig) Tool[WriteParams, WriteDetails] { return builtin.Write(config) }
func EditTool(config BuiltinConfig) Tool[EditParams, EditDetails]    { return builtin.Edit(config) }
func BashTool(config BuiltinConfig) Tool[BashParams, BashDetails]    { return builtin.Bash(config) }
func GrepTool(config BuiltinConfig) Tool[GrepParams, GrepDetails]    { return builtin.Grep(config) }
func FindTool(config BuiltinConfig) Tool[FindParams, FindDetails]    { return builtin.Find(config) }
func LSTool(config BuiltinConfig) Tool[LSParams, LSDetails]          { return builtin.LS(config) }
func RegisterBuiltinTools(registry *ToolRegistry, config BuiltinConfig) error {
	return builtin.RegisterAll(registry, config)
}

// Compaction contracts.
type (
	CompactionReason      = compact.Reason
	TokenEstimator        = compact.Estimator
	TokenEstimatorFunc    = compact.EstimatorFunc
	ApproximateEstimator  = compact.ApproximateEstimator
	Summarizer            = compact.Summarizer
	CompactionSummary     = compact.Summary
	CompactionPreparation = compact.Preparation
	CompactionPrepareHook = compact.PrepareHook
	CompactionOptions     = compact.Options
	CompactionResult      = compact.Result
)

const (
	CompactionManual    = compact.Manual
	CompactionThreshold = compact.Threshold
	CompactionOverflow  = compact.Overflow
)

func EstimateTokens(messages []Message, estimator TokenEstimator) int64 {
	return compact.Tokens(messages, estimator)
}
func ShouldCompact(messages []Message, opts CompactionOptions) bool {
	return compact.ShouldCompact(messages, opts)
}
func PrepareCompaction(entries []SessionEntry, reason CompactionReason, opts CompactionOptions) (CompactionPreparation, error) {
	return compact.Prepare(entries, reason, opts)
}
func RunCompaction(ctx context.Context, manager *SessionManager, reason CompactionReason, opts CompactionOptions, summarizer Summarizer) (CompactionResult, error) {
	return compact.Run(ctx, manager, reason, opts, summarizer)
}

// Extension contracts.
type (
	Extension[S any]          = extension.Extension[S]
	ExtensionRegistry[S any]  = extension.Registry[S]
	InputHook[S any]          = extension.InputHook[S]
	ContextHook[S any]        = extension.ContextHook[S]
	BeforeAgentHook[S any]    = extension.BeforeAgentHook[S]
	RequestHook[S any]        = extension.RequestHook[S]
	ResponseHook[S any]       = extension.ResponseHook[S]
	LifecycleHook[S any]      = extension.LifecycleHook[S]
	TreeHook[S any]           = extension.TreeHook[S]
	UserBashHook[S any]       = extension.UserBashHook[S]
	BeforeToolCallHook[S any] = extension.BeforeToolCallHook[S]
	AfterToolCallHook[S any]  = extension.AfterToolCallHook[S]
)

func NewExtensionRegistry[S any](tools *ToolRegistry, models *ModelRegistry, resources *ResourceRegistry) *ExtensionRegistry[S] {
	return extension.NewRegistry[S](tools, models, resources)
}

// Resource loading contracts.
type (
	ResourceLoadRequest      = resource.LoadRequest
	ResourceSource           = resource.Source
	ResourceSkill            = resource.Skill
	PromptTemplate           = resource.PromptTemplate
	ResourceDiagnostic       = resource.Diagnostic
	ResourceSnapshot         = resource.Snapshot
	ResourceLoader           = resource.Loader
	ProgramResourceLoader    = resource.ProgramLoader
	ResourceRegistry         = resource.Registry
	ResourcePromptOptions    = resource.PromptOptions
	FileSystemResourceLoader = fsloader.Loader
)

func NewResourceRegistry() *ResourceRegistry                            { return resource.NewRegistry() }
func NewFileSystemResourceLoader(root string) *FileSystemResourceLoader { return fsloader.New(root) }
func BuildSystemPrompt(opts ResourcePromptOptions) string               { return resource.BuildSystemPrompt(opts) }
func ExpandPromptTemplate(template PromptTemplate, values map[string]string) (string, error) {
	return resource.Expand(template, values)
}
func ParsePromptCommandArgs(input string) []string { return resource.ParseCommandArgs(input) }
func ExpandPromptArgs(template PromptTemplate, args []string) string {
	return resource.ExpandArgs(template, args)
}

// Session storage contracts. SessionOptions remains the runtime Session
// configuration; PersistenceOptions configures the lower-level append-only
// session manager.
type (
	SessionEntryID              = store.EntryID
	SessionHeader               = store.Header
	SessionEntry                = store.Entry
	SessionCustomEntry[T any]   = store.CustomEntry[T]
	SessionContext              = store.Context
	SessionTreeNode             = store.TreeNode
	PersistenceOptions          = store.Options
	SessionManager              = store.Manager
	SessionSnapshot             = store.Snapshot
	SessionConverter[T any]     = store.Converter[T]
	SessionConverterFunc[T any] = store.ConverterFunc[T]
)

const (
	SessionFormatVersion    = store.Version
	CompactionSummaryPrefix = store.CompactionSummaryPrefix
	CompactionSummarySuffix = store.CompactionSummarySuffix
	BranchSummaryPrefix     = store.BranchSummaryPrefix
	BranchSummarySuffix     = store.BranchSummarySuffix
)

func NewSessionManager(persistence Persistence, opts PersistenceOptions) (*SessionManager, error) {
	return store.New(persistence, opts)
}
func RestoreSessionManager(header SessionHeader, entries []SessionEntry, persistence Persistence) (*SessionManager, error) {
	return store.Restore(header, entries, persistence)
}
func ConvertSession[T any](ctx context.Context, source T, converter SessionConverter[T]) (SessionSnapshot, error) {
	return store.Convert(ctx, source, converter)
}

// Settings contracts.
type (
	Setting[T any] = settings.Setting[T]
	Settings       = settings.Settings
)

func NewSettings() *Settings { return settings.New() }

// Official SDK provider constructors. Configure credentials, base URLs,
// transports, retries, and headers on the SDK client before passing it in.
type (
	OpenAIClient      = openaisdk.Client
	OpenAIProvider    = openaiadapter.Provider
	AnthropicClient   = anthropicsdk.Client
	AnthropicProvider = anthropic.Provider
)

func NewOpenAIProvider(client OpenAIClient) *OpenAIProvider { return openaiadapter.New(client) }
func NewOpenAIResponsesProvider(client OpenAIClient) *OpenAIProvider {
	return openaiadapter.NewResponses(client)
}
func NewAnthropicProvider(client AnthropicClient) *AnthropicProvider {
	return anthropic.New(client)
}
