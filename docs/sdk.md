# best-harness-go SDK

`best-harness-go` 用于在 Go 程序中运行带工具的模型会话。它适合后台任务、服务端接口、自定义界面和自动化测试。

SDK 不会自动读取项目文件，也不会自动执行 Shell 命令。模型、工具、文件访问和持久化都需要由应用显式配置。

本文档从一个无状态会话开始，然后逐步加入工具、状态、事件和持久化。文末提供常用 API 速查。

## 快速开始

### 环境要求

- Go 1.27
- 一个可用的模型 API Key

安装：

```bash
go get github.com/dcalsky/best-harness-go
```

### 运行第一个会话

下面的程序通过 OpenAI 官方 Go SDK 的 Responses API 发送一条消息，并打印最终回复：

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/dcalsky/best-harness-go"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func main() {
	ctx := context.Background()

	models := harness.NewModelRegistry()
	selected := harness.Model{
		Provider:      "openai",
		API:           harness.APIOpenAIResponses,
		ID:            "gpt-5",
		ContextWindow: 128_000,
		MaxOutput:     8_192,
	}
	if err := models.Register(selected); err != nil {
		log.Fatal(err)
	}

	client := openai.NewClient(option.WithAPIKey(os.Getenv("OPENAI_API_KEY")))

	h, err := harness.NewStateless(harness.Options{Models: models})
	if err != nil {
		log.Fatal(err)
	}
	if err := h.RegisterProvider("openai", harness.NewOpenAIProvider(client)); err != nil {
		log.Fatal(err)
	}

	session, err := h.NewSession(
		ctx,
		harness.NewMemoryPersistence(),
		harness.SessionOptions{Model: &selected},
		harness.NoState{},
	)
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close()

	run, err := session.Start(ctx, harness.Prompt{Steps: harness.Sequence{
		harness.UserText("Explain what this Go package does in two sentences."),
	}}, harness.StartOptions{})
	if err != nil {
		log.Fatal(err)
	}
	if err := run.Wait(ctx); err != nil {
		log.Fatal(err)
	}

	messages := session.Conversation().Messages
	fmt.Println(messages[len(messages)-1].Text())
}
```

运行：

```bash
export OPENAI_API_KEY="your-key"
go run .
```

这段代码完成四件事：注册模型、配置模型服务、使用内存 Persistence 创建会话、等待本次运行结束。工具和持久化存储尚未启用。

## 核心对象

| 对象 | 用途 | 通常使用范围 |
| --- | --- | --- |
| `Harness[S]` | 保存应用允许使用的模型、模型服务、工具和资源 | 整个进程 |
| `Session[S]` | 保存一段对话、当前模型和可选状态 | 一段用户会话 |
| `Run[S]` | 表示一次 `Start` 调用 | 一次用户输入 |
| `harness.Provider` | 把模型请求发送到远端或本地服务 | 整个进程 |

`S` 是应用定义的会话状态类型。不需要状态时使用 `NewStateless` 和 `NoState`。

一个 `Harness` 可以创建多个 `Session`。每个 `Session` 的对话和状态互不共享。

## 配置模型

模型注册和模型服务注册是两步：

1. `Model` 描述模型名称和能力。
2. `RegisterProvider` 注册负责发送请求的客户端。

`Model.Provider` 必须与 `RegisterProvider` 的名称一致；`Model.API` 指定具体协议：

```go
selected := harness.Model{
	Provider:          "anthropic",
	API:               harness.APIAnthropic,
	ID:                "claude-sonnet-4-6",
	ContextWindow:     200_000,
	MaxOutput:         16_384,
	SupportsImages:    true,
	SupportsReasoning: true,
}

if err := models.Register(selected); err != nil {
	return err
}
if err := h.RegisterProvider("anthropic", provider); err != nil {
	return err
}
```

### 官方 SDK 客户端

OpenAI Chat Completions 和 Responses 共用 OpenAI 官方 Client。适配器根据 `Model.API` 分派：

```go
client := openai.NewClient(option.WithAPIKey(os.Getenv("OPENAI_API_KEY")))
provider := harness.NewOpenAIProvider(client)

chatModel.API = harness.APIOpenAI
responsesModel.API = harness.APIOpenAIResponses
```

Anthropic Messages 使用 Anthropic 官方 Client：

```go
client := anthropic.NewClient(option.WithAPIKey(os.Getenv("ANTHROPIC_API_KEY")))
provider := harness.NewAnthropicProvider(client)
model.API = harness.APIAnthropic
```

API Key、BaseURL、自定义 HTTP Client、请求头和重试策略由官方 SDK 的 `option` 包配置。Harness 不读取或保存 API Key。

### OpenAI 兼容服务

本地或第三方 OpenAI 兼容服务通过 OpenAI SDK 的 `option.WithBaseURL` 接入：

```go
client := openai.NewClient(
	option.WithAPIKey(os.Getenv("MODEL_API_KEY")),
	option.WithBaseURL("http://127.0.0.1:8000/v1"),
)
provider := harness.NewOpenAIProvider(client)
```

Chat Completions 默认使用兼容面更广的 `max_tokens`。对于要求新字段的 OpenAI 推理模型，在会话的生成配置中启用：

```go
Generation: harness.GenerationConfig{
	UseMaxCompletionTokens: true,
}
```

### API Key

最简单的配置是把环境变量传给官方 SDK：

```go
client := openai.NewClient(option.WithAPIKey(os.Getenv("OPENAI_API_KEY")))
```

需要短期令牌、代理或自定义认证时，可以向官方 SDK 注入自定义 HTTP Client 或 Middleware。

## 发送输入并读取结果

`Start` 立即返回一个 `Run`。使用 `Wait` 等待完成：

```go
run, err := session.Start(ctx, harness.Prompt{Steps: harness.Sequence{
	harness.UserText("Review this change."),
}}, harness.StartOptions{})
if err != nil {
	return err
}
if err := run.Wait(ctx); err != nil {
	return err
}
```

读取完整对话：

```go
messages := session.Conversation().Messages
lastReply := messages[len(messages)-1].Text()
```

`Run.Status()` 返回当前状态，`Run.Err()` 返回结束错误，`Run.Abort()` 请求终止运行。

### 文本和图片

一条输入可以包含多种内容：

```go
step := harness.UserMessageStep{Content: []harness.Content{
	harness.Text("Describe this image."),
	harness.Image(base64Image, "image/png"),
}}

run, err := session.Start(ctx, harness.Prompt{Steps: harness.Sequence{step}}, harness.StartOptions{})
```

图片数据使用 Base64 编码。对应模型需要设置 `SupportsImages: true`，远端服务也必须支持图片输入。

### 预先执行多个步骤

大多数请求只需要一个 `UserText`。如果应用需要先写入已有消息或先执行确定的工具调用，可以在 `Sequence` 中加入 `AssistantText` 或 `Tools`。这些步骤按顺序执行，然后模型继续处理剩余任务。

## 添加工具

工具让模型调用应用代码。参数类型和返回详情都是 Go 类型：

```go
type CountParams struct {
	Text string `json:"text"`
}

type CountDetails struct {
	Runes int `json:"runes"`
}

err := h.RegisterTool(
	harness.ToolSpec{
		Name:        "count_runes",
		Description: "Count Unicode characters in text.",
	},
	func(
		ctx context.Context,
		c harness.Context[harness.NoState],
		params CountParams,
	) (harness.ToolResult[CountDetails], error) {
		details := CountDetails{Runes: len([]rune(params.Text))}
		return harness.ToolResult[CountDetails]{
			Content: []harness.Content{harness.Text(strconv.Itoa(details.Runes))},
			Details: details,
		}, nil
	},
)
```

当 `harness.ToolSpec.Parameters` 为空时，SDK 根据参数结构体生成 JSON Schema。默认反序列化会忽略参数结构体中不存在的字段；如果某个工具需要严格拒绝未知字段，可以通过低层 `Tool.PrepareArguments` 提供自己的解码逻辑。可选字段使用 `omitempty` 标记。

使用 `WithArgumentsValidator` 可以在处理函数运行前校验已经解码的强类型参数：

```go
err := h.RegisterTool(
	harness.ToolSpec{Name: "count_runes"},
	handler,
	harness.WithArgumentsValidator(func(params CountParams) error {
		if strings.TrimSpace(params.Text) == "" {
			return errors.New("text must not be empty")
		}
		return nil
	}),
)
```

validator 可能因脚本预检或 hook 修改参数后的复检而执行多次，因此应保持无副作用、幂等、并发安全，并且不应修改传入参数。需要访问运行状态或执行外部 I/O 的检查应放在处理函数或 before-tool hook 中。

#### Validator 返回 error 后的行为

对于模型发起的普通 Tool Call，任一 validator 返回非 `nil` error 后：

1. SDK 立即停止执行后续 validator，并且不会执行工具 handler。
2. SDK 生成一条 `role=tool`、`IsError=true` 的 Tool Output，内容包含 `invalid <tool_name> arguments: <validator error>`。
3. 未达到 retry limit（或者未限制重试次数）时，该 Tool Output 会加入对话并发送给下一轮模型。模型可以据此修正参数并重新调用工具；这是模型自主发起的新 Tool Call，SDK 不会自动重放原调用。
4. 达到 retry limit 时，SDK 仍会保存最后一条错误 Tool Output，但不会再请求下一轮模型。Run 状态变为 `Failed`，`Run.Wait` 和 `Run.Err` 返回 `*harness.ValidatorRetryLimitError`。

负数或未配置 retry limit 时，validator error 可以持续作为 Tool Output 交给模型，直到模型停止调用、Run 被取消或发生其他终止错误。一次 Tool Call 即使触发多次复检，也只消耗一次重试计数。

直接调用 `ToolRegistry.Validate`、`Prepare` 或 `Execute` 时没有模型循环，validator error 会作为 Go error 直接返回。Prompt 中预先编排的 Tool Call 会在 Run 启动前预检；预检失败时 `Start` 直接返回 error，也不会生成 Tool Output。

参数结构体也可以使用兼容 `Struct(any) error` 接口的 tag validator，例如 [go-playground/validator](https://github.com/go-playground/validator)：

```go
import validator "github.com/go-playground/validator/v10"

type CreateUserParams struct {
	Name  string `json:"name" validate:"required,min=2"`
	Email string `json:"email" validate:"required,email"`
	Age   int    `json:"age" validate:"gte=0,lte=150"`
}

structValidator := validator.New(
	validator.WithRequiredStructEnabled(),
)

err := h.RegisterTool(
	harness.ToolSpec{Name: "create_user"},
	createUser,
	harness.WithStructValidator[CreateUserParams](
		structValidator,
		harness.WithValidatorRetryLimit(2),
	),
)
```

`WithStructValidator` 不让 SDK 直接依赖某个 validator 实现；`*validator.Validate` 会通过其 `Struct` 方法满足该接口。由于参数类型无法从 `StructValidator` 接口反推，调用时需要显式写出 `[CreateUserParams]`。validator 实例应在注册工具前完成自定义规则等配置，并在多个工具间复用。

`WithValidatorRetryLimit(2)` 表示第一次校验失败后，允许模型额外修正两次；第三次仍被同一个 validator 拒绝时结束 Run。`0` 表示不允许修正重试。传入负数或不传该 option 时，都不限制该 validator 的重试次数。

tag 校验可以和业务校验组合，并为每个 validator 设置不同的限制。SDK 按 option 的声明顺序执行，并在第一个错误处停止：

```go
err := h.RegisterTool(
	harness.ToolSpec{Name: "create_user"},
	createUser,
	harness.WithStructValidator[CreateUserParams](
		structValidator,
		harness.WithValidatorRetryLimit(2),
	),
	harness.WithArgumentsValidator(func(params CreateUserParams) error {
		if strings.HasSuffix(params.Email, "@example.invalid") {
			return errors.New("reserved email domain")
		}
		return nil
	}, harness.WithValidatorRetryLimit(1)),
)
```

重试计数在每个 Run 内按工具和 validator 分开保存。一次 Tool Call 即使因 hook 改写参数而多次执行 validator，也只计一次失败；validator 后续成功时会清空自己的连续失败计数。达到上限时，SDK 保留最后一条 `IsError` Tool Output，不执行 handler，不再请求下一轮模型，并让 `Run.Wait` 返回 `*harness.ValidatorRetryLimitError`：

```go
if err := run.Wait(ctx); err != nil {
	var limitErr *harness.ValidatorRetryLimitError
	if errors.As(err, &limitErr) {
		log.Printf(
			"tool %s validator %d exceeded retry limit %d: %v",
			limitErr.Tool,
			limitErr.ValidatorIndex+1,
			limitErr.RetryLimit,
			limitErr.LastErr,
		)
	}
	return err
}
```

`ToolResult` 的常用字段：

| 字段 | 用途 |
| --- | --- |
| `Content` | 返回给模型的内容 |
| `Details` | 提供给事件订阅者或应用界面的结构化数据 |
| `IsError` | 表示工具已执行，但业务结果失败 |
| `Terminate` | 返回结果后结束本次运行 |

处理函数返回非 `nil` error 时，该次工具调用失败。

### 限制会话可用的工具

默认情况下，会话可以使用已注册的全部工具。通过 `ActiveTools` 只开放指定工具：

```go
session, err := h.NewSession(ctx, harness.NewMemoryPersistence(), harness.SessionOptions{
	Model:       &selected,
	ActiveTools: []string{"read", "grep"},
}, harness.NoState{})
```

运行期间不能修改工具列表。运行结束后可以调用：

```go
err := session.SetActiveTools([]string{"read", "grep", "count_runes"})
```

未注册的工具名称会被忽略。

### 内置文件和 Shell 工具

`RegisterBuiltinTools` 注册 `read`、`bash`、`edit`、`write`、`grep`、`find` 和 `ls`：

```go
err := h.RegisterBuiltinTools(harness.BuiltinConfig{
	Cwd:        projectDir,
	FileSystem: harness.OSFileSystem{},
})
```

这会让模型访问 `FileSystem`，并通过 `Shell` 执行命令。没有传入 `Shell` 时使用本机 Shell。不要把超出任务范围的目录或执行器传给不受信任的请求。

## 添加会话状态

状态用于保存应用数据，例如计数、审批结果或任务进度。状态类型在创建 `Harness` 时确定，初始值在创建 `Session` 时传入：

```go
type AgentState struct {
	Searches int      `json:"searches"`
	Facts    []string `json:"facts"`
}

h, err := harness.New[AgentState](harness.Options{Models: models})
if err != nil {
	return err
}
if err := h.RegisterProvider(selected.Provider, client); err != nil {
	return err
}

	session, err := h.NewSession(
	ctx,
	harness.NewMemoryPersistence(),
	harness.SessionOptions{Model: &selected},
	AgentState{},
)
```

工具通过 `Context` 读取和更新当前会话的状态：

```go
current := c.State()

err := c.UpdateState(func(state *AgentState) {
	state.Searches++
	state.Facts = append(state.Facts, fact)
})
```

`State()` 返回副本。修改副本不会改变会话；写入必须通过 `UpdateState`。

状态需要能编码为 JSON。不要把数据库连接、锁、文件句柄或带循环引用的值放入状态。每个会话拥有独立状态。

### 替换 JSON Codec

SDK 支持替换进程级 JSON Codec，默认实现是 `encoding/json`。需要使用 Sonic 等实现时，在应用启动阶段配置：

```go
err := harness.SetJSONCodec(harness.JSONCodecFuncs{
	MarshalFunc:   sonic.Marshal,
	UnmarshalFunc: sonic.Unmarshal,
})
if err != nil {
	return err
}
```

`SetJSONCodec` 只应在启动阶段调用。配置冻结后再次调用会返回 `harness.ErrJSONCodecFrozen`。Codec 必须并发安全、输出合法 JSON、支持 JSON tag（包括 `omitempty`）、raw JSON，以及 `MarshalJSON`/`UnmarshalJSON`。该接口用于替换 JSON 实现，不能切换到 CBOR 或 protobuf。

替换 Codec 不会升级 Session 格式；旧 v4 JSONL 仍可读取，重新写入也仍是 v4。不同实现可能产生不同的字段顺序、空白或转义形式，因此不应按原始字节比较 Session 文件。

运行结束后读取状态：

```go
stateAtRunEnd := run.State()
currentState := session.State()
```

`Run.State()` 是该次运行结束时的状态。`Session.State()` 是当前分支的最新状态。

### 并行工具调用

工具默认可以并行执行。多个工具同时更新状态时，SDK 按模型给出的工具调用顺序应用更新，因此完成速度不会改变最终顺序。

如果后一个工具必须读取前一个工具写入的状态，将工具设置为顺序执行：

```go
harness.ToolSpec{
	Name:          "next_step",
	Description:   "Run the next state-dependent step.",
	ExecutionMode: harness.Sequential,
}
```

`UpdateState` 的函数应只修改内存中的状态。网络请求和文件写入应在调用 `UpdateState` 之前完成。

## 控制模型输出

### 推理强度

先在 `Model` 上声明支持推理，再在会话空闲时设置强度：

```go
selected.SupportsReasoning = true

session, err := h.NewSession(ctx, harness.NewMemoryPersistence(), harness.SessionOptions{Model: &selected}, initialState)
if err != nil {
	return err
}
if err := session.SetThinkingLevel(ctx, "high"); err != nil {
	return err
}
```

可选值为 `off`、`minimal`、`low`、`medium`、`high`、`xhigh` 和 `max`。具体支持范围取决于模型服务。模型未声明 `SupportsReasoning` 时，SDK 使用 `off`。

### 采样参数

在 `SessionOptions.Generation` 中设置常用参数：

```go
session, err := h.NewSession(ctx, harness.NewMemoryPersistence(), harness.SessionOptions{
	Model: &selected,
	Generation: harness.GenerationConfig{
		Temperature: harness.Ptr(0.2),
		TopP:        harness.Ptr(0.9),
	},
}, initialState)
```

可用字段：

- `Temperature`
- `TopP`
- `TopK`
- `Seed`
- `FrequencyPenalty`
- `PresencePenalty`
- `StopSequences`
- `Thinking`：显式启用或关闭推理（如 DeepSeek 的 thinking 开关）
- `JSONOutput`：要求模型返回 JSON 对象
- `ParallelToolCalls`：控制并行工具调用
- `ReasoningBudgetTokens`：为支持显式预算的协议设置推理 token 数（Anthropic 要求至少 1024 且小于 `MaxTokens`）
- `ThinkingBudget`：映射兼容 Chat API 的非标准 `thinking_budget`；零值表示不发送
- `PreserveThinking`：映射兼容 Chat API 的非标准 `preserve_thinking`；`true` 时发送
- `UseMaxCompletionTokens`：让 Chat Completions 使用 `max_completion_tokens`；默认使用兼容端点普遍支持的 `max_tokens`
- `ExtraBody`：对应 OpenAI Agents SDK `ModelSettings.extra_body`，使用普通 Go 值添加或覆盖请求体字段
- `Extra`：尚未进入 Harness 类型定义的 provider 原生顶层参数，值为 `jsontext.Value`

不是每个协议都支持所有字段；适配器会在发送 HTTP 请求前返回明确错误。通常只需设置 `Temperature` 或 `TopP` 中的一个。`ThinkingBudget` 与 `PreserveThinking` 是兼容 Chat API 的便捷字段；Responses 或 Anthropic 的原生扩展应放入 `ExtraBody`。

`ExtraBody` 会在标准字段之后合并，因此同名值会有意覆盖标准值。这与 OpenAI Agents SDK 的 `extra_body` 模型设置一致：

```go
Generation: harness.GenerationConfig{
	ThinkingBudget:   16_384,
	PreserveThinking: true,
	ExtraBody: map[string]any{
		"enable_thinking": true,
		"search_options": map[string]any{
			"forced_search": true,
		},
	},
}
```

由于 `int` 和 `bool` 的零值表示“不发送”，如果需要显式发送 `thinking_budget: 0` 或 `preserve_thinking: false`，请放入 `ExtraBody`。

`Extra` 提供原始 JSON 和 JSON path 形式的低层透传：

```go
Generation: harness.GenerationConfig{
	Extra: map[string]jsontext.Value{
		"service_tier":            jsontext.Value(`"flex"`),
		"reasoning.budget_tokens": jsontext.Value(`2048`),
	},
}
```

`Extra` 的键支持 SDK 的 JSON path 语法，因此可以只增加一个嵌套原生字段。它不能覆盖 `model`、`messages`、`tools` 等已经标准化的顶层字段，冲突时会返回错误；需要有意覆盖时应使用 `ExtraBody`。

## 订阅运行事件

订阅 `AgentEvent` 可以获取增量文本、工具进度和运行阶段：

```go
unsubscribe := session.On(func(
	ctx context.Context,
		c harness.Context[harness.NoState],
	event harness.AgentEvent,
) {
	stream := event.Event.Stream
	if stream != nil && stream.Type == harness.EventTextDelta {
		fmt.Print(stream.Text)
	}
})
defer unsubscribe()
```

常用事件：

- `harness.AgentEvent`：模型输出和工具执行事件。
- `harness.EntryAppendedEvent`：一条消息或记录已写入会话。
- `harness.RunEvent`：运行状态发生变化。
- `harness.ModelChangedEvent`：当前模型发生变化。
- `harness.CompactionEvent`：上下文压缩结束。

事件回调中的 `Context` 只用于读取事件发生时的状态，不能更新状态。工具需要报告中间进度时调用 `ctx.Report(details)`。

## 在运行期间追加输入

`Steer` 用于修正当前任务，`FollowUp` 用于在当前任务结束后继续处理另一条输入：

```go
err := run.Steer(ctx, harness.User("Focus on the database package."))
err = run.FollowUp(ctx, harness.User("After that, run the package tests."))
```

`Steer` 会在当前一组工具调用结束后送达。`FollowUp` 会在模型准备结束本次任务时送达。

使用 `run.Abort()` 主动终止运行。使用带超时的 `context.Context` 可以限制 `Wait` 的等待时间；`Wait` 超时不代表后台运行已经停止，如需停止还应调用 `Abort`。

## 持久化会话

每个 Session 都必须显式传入一个 `harness.Persistence`。不需要持久化时使用内存实现：

```go
session, err := h.NewSession(
	ctx,
	harness.NewMemoryPersistence(),
	harness.SessionOptions{Model: &selected},
	initialState,
)
```

需要文件持久化时，只选择保存目录；SDK 为每个 Session 生成独立的 JSONL 文件：

```go
persistence, err := harness.NewFilePersistence("/var/lib/myapp/sessions")
if err != nil {
	return err
}
session, err := h.NewSession(
	ctx,
	persistence,
	harness.SessionOptions{Model: &selected, Cwd: projectDir},
	initialState,
)
```

恢复会话：

```go
opened, err := h.OpenSession(ctx, path)
latest, err := h.ResumeLatest(ctx, directory, projectDir)
```

当前文件格式为 JSONL v4。它保存消息、模型选择、推理强度、运行记录和状态。数据库或其他存储可以实现 `harness.Persistence`，通过 `harness.NewSessionManager(persistence, options)` 创建 Manager，再用 `NewSessionWithManager` 接入 Harness。

始终在不再使用会话时调用 `Close`。同一个持久化文件不能同时有两个写入者。

### 分支

`Navigate` 把当前会话切换到历史节点，`Fork` 从历史节点创建独立会话：

```go
entries := session.Entries()
target := entries[0].ID

if err := session.Navigate(ctx, &target); err != nil {
	return err
}

child, err := session.Fork(ctx, target, harness.SessionOptions{})
```

切换分支时，对话和状态会一起恢复。Fork 默认通过当前 Persistence 的 `Fork` 方法继承相同存储方式。运行期间不能切换分支。

### 上下文压缩与 token 估算

未提供 estimator 时，SDK 使用基于 `novocab-go v0.2.0` 的 `harness.NovocabEstimator`。文本按 novocab 的无词表模型估算；可识别的 base64 图片会先解析尺寸，再按 Anthropic 图片公式估算。它适合上下文压缩的容量估算，但不是 Provider 的精确计费结果。

如果模型使用其他图片计费公式，可以设置 `ImageGeneration`（例如 `novocab.ImageOpenAI`）后传入 `WithTokenEstimator`。

应用可以实现 `harness.TokenEstimator`，或使用 `harness.TokenEstimatorFunc` 替换默认实现：

```go
session, err := h.NewSession(
	ctx,
	harness.NewMemoryPersistence(),
	harness.SessionOptions{
		Model: &selected,
	},
	initialState,
	harness.WithTokenEstimator(harness.TokenEstimatorFunc(func(message harness.Message) int64 {
		return estimateForSelectedModel(message)
	})),
)
```

Estimator 必须返回非负值。Provider 已返回 `Usage.TotalTokens` 时，总量判断优先使用该值；estimator 仍用于决定压缩后保留哪些最近消息。

## 加载项目资源

资源可以提供项目说明、系统提示词、技能和提示词模板。SDK 只读取已注册的 `harness.ResourceLoader`。

在代码中提供项目说明：

```go
h.Resources().Register(harness.ProgramResourceLoader{Snapshot: harness.ResourceSnapshot{
	ProjectInstructions: []harness.ResourceSource{
		{
			Name:    "project",
			Path:    "memory:project",
			Content: "Run package tests after editing Go files.",
		},
	},
}})
```

从指定项目目录加载：

```go
h.Resources().Register(fsloader.New(projectRoot))
```

`fsloader` 会在 `projectRoot` 范围内读取项目说明，以及 `.pi` 目录中的系统提示词、提示词模板和技能。会话的 `Cwd` 必须位于该根目录内。

只需要固定系统提示词时，直接设置：

```go
harness.SessionOptions{
	Model:        &selected,
	SystemPrompt: "Answer questions about this repository.",
}
```

## 扩展和 Hook

Extension 用于给多个会话复用相同的检查或记录逻辑。Hook 是在请求、响应或工具调用等阶段执行的回调：

```go
type auditExtension struct{}

func (auditExtension) Register(r *harness.ExtensionRegistry[AgentState]) error {
	r.AddRequestHook(func(
		ctx context.Context,
		c harness.Context[AgentState],
		request *harness.Request,
	) error {
		if request.Headers == nil {
			request.Headers = make(map[string][]string)
		}
		request.Headers.Set("X-Session-ID", c.SessionID())
		return nil
	})
	return nil
}

if err := h.RegisterExtension(auditExtension{}); err != nil {
	return err
}
```

常用 Hook 包括输入处理、请求发送前、响应完成后、工具调用前后、会话开始和会话关闭。Extension 的状态类型必须与 `Harness[S]` 一致。

优先使用普通工具和 `Session` API。只有需要跨会话复用同一段生命周期逻辑时再使用 Extension。

## 直接调用模型客户端

不需要会话、工具和持久化时，可以直接调用 `harness.LLMClient.Stream`：

```go
stream, err := client.Stream(ctx, harness.Request{
	Model:     selected,
	Messages:  []harness.Message{harness.User("Write a concise answer.")},
	MaxTokens: 512,
})
if err != nil {
	return err
}
defer stream.Close()

for {
	event, err := stream.Next()
	if errors.Is(err, io.EOF) {
		break
	}
	if err != nil {
		return err
	}
	if event.Type == harness.EventTextDelta {
		fmt.Print(event.Text)
	}
}
```

## 常用 API 速查

### `SessionOptions`

| 字段 | 用途 |
| --- | --- |
| `Model` | 本次会话使用的模型 |
| `SystemPrompt` | 覆盖资源中的系统提示词 |
| `Cwd` | 工具和资源使用的工作目录 |
| `ActiveTools` | 本次会话允许使用的工具名称 |
| `Generation` | 采样参数和服务自有参数 |
| `QueueMode` | 多条追加输入的处理方式 |
| `ExecutionMode` | 会话级工具执行方式 |
| `Summarizer` / `Compaction` | 上下文压缩配置 |

### `Session[S]`

| 方法 | 用途 |
| --- | --- |
| `Start` | 开始一次运行 |
| `State` | 读取当前状态 |
| `Conversation` | 读取当前分支的模型消息 |
| `On` | 订阅事件 |
| `SetModel` | 更换模型 |
| `SetThinkingLevel` | 设置推理强度 |
| `SetActiveTools` | 修改允许使用的工具 |
| `Compact` | 手动压缩上下文 |
| `Navigate` | 切换到历史节点 |
| `Fork` | 从历史节点创建新会话 |
| `Stats` | 读取消息数、Token 和费用统计 |
| `Location` | 返回当前 Persistence 的逻辑位置 |
| `Close` | 关闭会话并释放 Persistence 资源 |

### `Run[S]`

| 方法 | 用途 |
| --- | --- |
| `Wait` | 等待运行结束 |
| `Done` | 获取结束通知 channel |
| `Status` / `Err` | 读取运行结果 |
| `State` | 读取该次运行结束时的状态 |
| `Steer` | 修正当前任务 |
| `FollowUp` | 排队后续任务 |
| `Abort` | 请求终止运行 |

## 示例

仓库中的示例按能力拆分：

SDK、`examples` 与 `e2e` 是三个独立的 workspace module。Hertz 与 SQLite
只属于应用示例和 E2E 覆盖，不会进入根 SDK module 的依赖图。

- [`examples/unified_llm`](../examples/unified_llm)：OpenAI Responses 和 Anthropic 配置
- [`examples/openai_compatible`](../examples/openai_compatible)：OpenAI 兼容服务
- [`examples/custom_tool`](../examples/custom_tool)：自定义工具
- [`examples/resources`](../examples/resources)：项目资源
- [`examples/queues`](../examples/queues)：追加输入
- [`examples/sqlite_session`](../examples/sqlite_session)：应用侧自行实现 SQLite `Persistence`
- [`examples/sqlite_resources`](../examples/sqlite_resources)：SQLite 资源加载
- [`examples/agui_echarts`](../examples/agui_echarts)：AG-UI Web 应用
- [`examples/a2ui_echarts`](../examples/a2ui_echarts)：A2UI Web 应用

## 从旧 API 迁移

```go
// 无状态
h, err := harness.NewStateless(opts)

// 有状态
h, err := harness.New[AgentState](opts)

// 注册工具
err = h.RegisterTool(harness.ToolSpec{...}, handler)

// 读取消息
messages := session.Conversation().Messages

// 获取可执行 Session 操作的 Context
runtimeCtx := session.Context()
```

`Options.Extensions` 已删除。先创建 `Harness`，再调用 `RegisterExtension`。
