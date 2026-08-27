package harness

import (
	"context"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"

	"github.com/dcalsky/best-harness-go/internal/core"
)

// Root runtime types are aliases to internal/core so their complete method
// sets, including Go 1.27 generic methods, remain available through harness.
type (
	Options                   = core.Options
	Harness[S any]            = core.Harness[S]
	ToolOption[P any]         = core.ToolOption[P]
	ValidatorOption           = core.ValidatorOption
	NoState                   = core.NoState
	SessionOptions            = core.SessionOptions
	Session[S any]            = core.Session[S]
	StartOptions              = core.StartOptions
	Run[S any]                = core.Run[S]
	Prompt                    = core.Prompt
	Unsubscribe               = core.Unsubscribe
	Event                     = core.Event
	AgentEvent                = core.AgentEvent
	EntryAppendedEvent        = core.EntryAppendedEvent
	QueueEvent                = core.QueueEvent
	ModelChangedEvent         = core.ModelChangedEvent
	ThinkingLevelChangedEvent = core.ThinkingLevelChangedEvent
	CompactionEvent           = core.CompactionEvent
	RunEvent                  = core.RunEvent
	CompactOptions            = core.CompactOptions
	NavigateOptions           = core.NavigateOptions
	Stats                     = core.Stats
)

var (
	ErrNoModel       = core.ErrNoModel
	ErrNoProvider    = core.ErrNoProvider
	ErrNoShell       = core.ErrNoShell
	RetryAttempts    = core.RetryAttempts
	RetryDelay       = core.RetryDelay
	QueueMode        = core.QueueMode
	ExecutionMode    = core.ExecutionMode
	ReserveTokens    = core.ReserveTokens
	KeepRecentTokens = core.KeepRecentTokens
)

func New[S any](opts Options) (*Harness[S], error)         { return core.New[S](opts) }
func NewStateless(opts Options) (*Harness[NoState], error) { return core.NewStateless(opts) }
func WithValidatorRetryLimit(retries int) ValidatorOption {
	return core.WithValidatorRetryLimit(retries)
}
func WithArgumentsValidator[P any](validate ArgumentsValidator[P], options ...ValidatorOption) ToolOption[P] {
	return core.WithArgumentsValidator(validate, options...)
}
func WithStructValidator[P any](validate StructValidator, options ...ValidatorOption) ToolOption[P] {
	return core.WithStructValidator[P](validate, options...)
}

func NewMemoryPersistence() Persistence { return core.NewMemoryPersistence() }
func NewFilePersistence(directory string) (Persistence, error) {
	return core.NewFilePersistence(directory)
}
func OpenFileSession(path string) (*SessionManager, error) { return core.OpenFileSession(path) }
func ListFileSessions(ctx context.Context, directory string) ([]SessionInfo, error) {
	return core.ListFileSessions(ctx, directory)
}
func ResumeLatestFileSession(ctx context.Context, directory, cwd string) (*SessionManager, error) {
	return core.ResumeLatestFileSession(ctx, directory, cwd)
}

func OverflowOptions() CompactOptions               { return core.OverflowOptions() }
func RawJSON(value json.RawMessage) json.RawMessage { return core.RawJSON(value) }
