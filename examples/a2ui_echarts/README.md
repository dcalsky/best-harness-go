# AI SDK ECharts Dashboard

这个示例展示如何用 `best-harness-go`、Vercel AI SDK 和 ECharts 开发一个可交互的 Dashboard Agent：

- 单个 React 页面同时渲染仪表盘和 Agent Chatbox，不使用 iframe 或额外的浏览器消息协议；
- Chatbox 基于 AI SDK 的 `useChat`、`DefaultChatTransport` 和 AI Elements；
- Agent 使用 typed Session State 和 `render_chart` Tool 生成候选图表；
- 后端使用 `harness.VercelAIAdapter` 把 `AgentEvent` 编码成 AI SDK UI Message Stream；
- Tool 的结构化 `Details` 以持久的 `data-chart` part 推送到前端并用 ECharts 预览；
- 用户确认后，React 直接把候选图表加入仪表盘并保存到 `localStorage`；
- Agent 图表保留生成它的 Session ID，点击图表即可恢复对应对话。

前端源码在 [frontend](frontend)。Vite 构建产物放在 `web/ai` 并通过 Go `embed` 发布；修改前端后执行：

```bash
cd examples/a2ui_echarts/frontend
npm install
npm run build
```

## 运行

默认使用内置 demo Provider，不需要 API Key：

```bash
go run ./examples/a2ui_echarts
```

打开 <http://localhost:8080/>，可以输入：

- `生成月度销售趋势折线图`
- `添加各渠道占比饼图`
- `创建区域销售排名柱状图`
- `生成营销投入与转化率散点图`

ECharts 已打包在前端产物中，页面运行时不依赖第三方 CDN。

## 使用 DeepSeek Flash

`.env` 中配置 `DEEPSEEK_API_KEY` 后运行：

```bash
set -a
source ./.env
set +a
POC_PROVIDER=deepseek go run ./examples/a2ui_echarts
```

默认模型为 `deepseek-v4-flash`。可通过 `POC_MODEL` 和 `DEEPSEEK_BASE_URL` 覆盖。

## 使用 OpenAI-compatible 模型

```bash
POC_PROVIDER=openai \
OPENAI_API_KEY=your-key \
OPENAI_BASE_URL=https://api.openai.com/v1 \
OPENAI_MODEL=gpt-5-mini \
go run ./examples/a2ui_echarts
```

`OPENAI_BASE_URL` 可替换为任何实现 streaming Chat Completions 与 tool calling 的兼容服务。

## API

- `POST /api/chat`：接收 AI SDK `DefaultChatTransport` 请求，返回 SSE UI Message Stream（`x-vercel-ai-ui-message-stream: v1`）。
- `GET /api/sessions/{chatID}/messages`：返回可供 AI SDK 恢复的 Session UI 消息，包括 `data-chart` part。
- `GET /api/health`：返回服务状态和当前 Provider。

标准文本、reasoning、tool 和错误事件由 `harness.VercelAIAdapter` 编码；应用通过 `MapEvent` 扩展点添加 `data-chart`。前端通过 `prepareSendMessagesRequest` 只上传最新一条 UI message，完整历史由服务端 Session 保存并通过 history API 恢复。

## 测试

```bash
go test ./examples/a2ui_echarts
```

真实 DeepSeek E2E 默认跳过，显式启用方式：

```bash
set -a
source ./.env
set +a
BEST_HARNESS_DEEPSEEK_E2E=1 go test -v ./examples/a2ui_echarts -run TestDeepSeekDashboardE2E -count=1
```
