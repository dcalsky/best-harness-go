# best-harness-go

`best-harness-go` is a Go 1.27 SDK for running non-interactive coding-agent sessions. It provides typed shared session state, parallel tools with ordered state commits, a provider-neutral message protocol, a unified streaming LLM API for OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages, append-only JSONL v4 sessions, context compaction, explicit resource loading, and compile-time extensions.

The module does not include a CLI, TUI, RPC server, JavaScript runtime, automatic project scanning, or automatic shell and file access.

完整中文文档见 [`docs/sdk.md`](docs/sdk.md)。

Install the Harness package:

```text
go get github.com/dcalsky/best-harness-go
```

Application code imports only the root `harness` package for the core SDK, concrete providers, resources, persistence, tools, and protocol adapters. Implementations live under Go's `internal` boundary and cannot be imported by downstream modules.


## Minimal session

```go
import "github.com/dcalsky/best-harness-go"

models := harness.NewModelRegistry()
selected := harness.Model{Provider: "test", ID: "small", ContextWindow: 128_000}
_ = models.Register(selected)

h, _ := harness.NewStateless(harness.Options{Models: models})
_ = h.RegisterProvider("test", fauxProvider)

s, _ := h.NewSession(ctx, harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, harness.NoState{})
defer s.Close()

r, _ := s.Start(ctx, harness.Prompt{Steps: harness.Sequence{
	harness.UserText("Inspect the package and explain its public API."),
}}, harness.StartOptions{})
_ = r.Wait(ctx)
```

No provider, model, tool, resource loader, or shell is installed by default. Every session requires an explicit Persistence; use `harness.NewMemoryPersistence()` when no durable storage is wanted.

## Model providers via official SDKs

The application owns the official OpenAI or Anthropic SDK client and passes it to the harness adapter. The OpenAI adapter supports both Chat Completions and Responses, selected by `Model.API`:

```go
client := openai.NewClient(
    option.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
)

selected := harness.Model{
    Provider:  "openai",
    API:       harness.APIOpenAIResponses,
    ID:        "gpt-5",
    MaxOutput: 8_192,
}
_ = h.RegisterProvider("openai", harness.NewOpenAIProvider(client))
```

Use `harness.APIOpenAI` for Chat Completions, `harness.APIOpenAIResponses` for Responses, and `harness.APIAnthropic` with `harness.NewAnthropicProvider`. Credentials, base URLs, HTTP transports, headers, and retries are configured with each official SDK's `option` package. OpenAI-compatible endpoints use the same OpenAI client with `option.WithBaseURL`. Requests, messages, tool definitions, stream events, usage, costs, and provider errors remain provider-neutral.

## Typed shared state and tools

The state type is selected once when the Harness is created. Every Session owns one value of that type.

```go
type AgentState struct {
    Searches int      `json:"searches"`
    Facts    []string `json:"facts"`
}

type SearchParams struct {
    Query string `json:"query"`
}

type SearchDetails struct { Fact string `json:"fact"` }

h, _ := harness.New[AgentState](harness.Options{Models: models})

err := h.RegisterTool(
	harness.ToolSpec{Name: "search", Description: "Search for one fact.", ExecutionMode: harness.Parallel},
	func(ctx context.Context, c harness.Context[AgentState], p SearchParams) (harness.ToolResult[SearchDetails], error) {
        details := SearchDetails{Fact: search(ctx, p.Query)}
        if err := c.UpdateState(func(state *AgentState) {
            state.Searches++
            state.Facts = append(state.Facts, details.Fact)
        }); err != nil {
			return harness.ToolResult[SearchDetails]{}, err
        }
		return harness.ToolResult[SearchDetails]{Details: details}, nil
    },
)

s, _ := h.NewSession(ctx, harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, AgentState{})
r, _ := s.Start(ctx, prompt, harness.StartOptions{})
_ = r.Wait(ctx)
finalState := r.State()
```

Go 1.27 infers the Tool parameter and detail types from the handler. Generated schemas describe the accepted arguments; ordinary decoding ignores unknown fields. A low-level `Tool.PrepareArguments` can enforce stricter behavior when needed. Parallel Tool bodies read the same state snapshot; their reducers commit in the model's Tool Call order.

## Persistence and resources

Pass `harness.NewFilePersistence(directory)` to store each session as a generated JSONL v4 file in that directory. Pass `harness.NewMemoryPersistence()` for an ephemeral session, or implement `harness.Persistence` for a database or another store. Initial state is stored in the header and each successful update appends a full state snapshot. `OpenSession`, `ResumeLatest`, `Navigate`, and `Fork` restore both conversation history and state from the selected branch. v3 files are intentionally rejected.

Resources are loaded only from registered `harness.ResourceLoader` values. `harness.ProgramResourceLoader` supplies in-memory content. `harness.NewFileSystemResourceLoader` reads the selected project tree only when the application registers it.

The `examples` directory contains:

- a faux provider session;
- OpenAI-compatible setup;
- unified OpenAI Responses and Anthropic setup;
- a custom typed tool;
- a production-shaped Hertz Web setup with an application-owned adapter;
- a single-page AI SDK dashboard where an Agent generates ECharts visualizations;
- an `execute_sql` tool that separates the model-facing Tool Message from structured frontend events;
- persistence and recovery;
- an application-defined SQLite `Persistence` example without JSONL files;
- steer and follow-up queues;
- an in-memory resource loader;
- a SQLite RULES and SKILLS loader;
- a compile-time extension;
- history navigation, fork, and compaction setup.

`examples/` contains runnable or directly reusable application code only. Assertions, fixtures, concurrency cases, and black-box process checks live under `e2e/`; example-related E2E files use the `*_example_e2e_test.go` suffix.

The SDK, examples, and E2E suite are separate workspace modules. Hertz and
SQLite are dependencies of application examples and E2E coverage, not of the
root SDK module. Run the complete offline suite from the repository root with
`go test ./... ./examples/... ./e2e/...`.

## JSON codec

The SDK supports replacing its process-wide JSON codec. The default is
`encoding/json`. To use another JSON implementation, configure it once at
process startup:

```go
err := harness.SetJSONCodec(harness.JSONCodecFuncs{
    MarshalFunc:   sonic.Marshal,
    UnmarshalFunc: sonic.Unmarshal,
})
```
