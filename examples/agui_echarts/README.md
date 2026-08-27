# AG-UI + CopilotKit ECharts example

这是 `examples/a2ui_echarts` 的 AG-UI 对照实现。它使用同一类分析图表场景，但聊天端改为 CopilotKit v2，Go 服务直接提供 CopilotKit multi-route runtime，并用 `harness.AGUIAdapter` 输出标准 AG-UI SSE。

链路如下：

```text
CopilotKit React
  -> GET  /api/copilotkit/info
  -> POST /api/copilotkit/agent/chart-agent/connect
  -> POST /api/copilotkit/agent/chart-agent/run
  -> best-harness-go Session
  -> harness.AGUIAdapter
  -> AG-UI SSE
  -> CopilotKit tool-call renderer
  -> ECharts preview
```

## 运行

仓库中包含已经构建好的前端，因此默认 demo provider 不需要 API key：

```bash
go run ./examples/agui_echarts
```

打开 <http://localhost:8080>，输入“生成各渠道占比饼图”，再点击工具卡中的“添加到仪表盘”。

使用 OpenAI compatible provider：

```bash
POC_PROVIDER=openai \
OPENAI_API_KEY=... \
OPENAI_MODEL=gpt-5-mini \
go run ./examples/agui_echarts
```

使用 DeepSeek：

```bash
POC_PROVIDER=deepseek \
DEEPSEEK_API_KEY=... \
POC_MODEL=deepseek-v4-flash \
go run ./examples/agui_echarts
```

## 前端开发

```bash
cd examples/agui_echarts/frontend
npm install
npm run dev
```

Vite 会把 `/api` 代理到 `localhost:8080`。生成可嵌入 Go binary 的静态资源：

```bash
npm run build
```

## 兼容性验证

Go 集成测试检查 runtime discovery、thread snapshot、run/thread ID、reasoning、message、tool call 和唯一终止事件：

```bash
go test -v ./examples/agui_echarts
```

运行服务后，可让官方 `@ag-ui/client` 的 `HttpAgent` 和事件验证器消费真实流：

```bash
cd examples/agui_echarts/frontend
AGENT_URL=http://127.0.0.1:8080/api/copilotkit/agent/chart-agent/run npm run smoke
```

这个 smoke test 会验证 `RUN_STARTED`、工具生命周期、`TOOL_CALL_RESULT`、消息物化以及 `RUN_FINISHED`。流末尾不会发送非 AG-UI 的 `[DONE]`。

CopilotKit 内部仍保留完整消息用于 UI 渲染，但 `IncrementalRunInput` middleware 会在 AG-UI HTTP run 前只保留最新一条 message。完整历史由 Go Session 管理，并在重连时通过 `MESSAGES_SNAPSHOT` 恢复。
