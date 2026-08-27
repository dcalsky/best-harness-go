import { useEffect, useMemo, useRef, useState } from "react";
import {
  CopilotChat,
  CopilotKitProvider,
  ToolCallStatus,
  defineToolCallRenderer,
  useAgent,
} from "@copilotkit/react-core/v2";
import * as echarts from "echarts";
import { BarChart3, Check, CirclePlus, Plus, RefreshCw, Sparkles } from "lucide-react";
import { z } from "zod";

const chartSchema = z.object({
  title: z.string(),
  chart_type: z.enum(["line", "bar", "pie", "scatter", "radar", "gauge", "area"]),
  description: z.string().optional(),
  option: z.record(z.unknown()),
});

type Chart = {
  id: string;
  title: string;
  chartType: string;
  description?: string;
  option: echarts.EChartsOption;
};

type AddChartEvent = CustomEvent<Chart>;
const addChartEvent = "pulseboard:add-chart";
const threadStorageKey = "pulseboard.agui.thread";
const incrementalAgents = new WeakSet<object>();

function newThreadID() {
  return `thread-${crypto.randomUUID?.() ?? Date.now().toString(36)}`;
}

function initialThreadID() {
  const existing = localStorage.getItem(threadStorageKey);
  if (existing) return existing;
  const id = newThreadID();
  localStorage.setItem(threadStorageKey, id);
  return id;
}

function parseResult(result: string | undefined): Partial<Chart> | undefined {
  if (!result) return undefined;
  try {
    const payload = JSON.parse(result) as { details?: { chart?: Partial<Chart> & { chartType?: string } } };
    return payload.details?.chart;
  } catch {
    return undefined;
  }
}

function EChart({ option, compact = false }: { option: echarts.EChartsOption; compact?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!ref.current) return;
    const chart = echarts.init(ref.current, undefined, { renderer: "canvas" });
    chart.setOption(option, { notMerge: true });
    const observer = new ResizeObserver(() => chart.resize());
    observer.observe(ref.current);
    return () => {
      observer.disconnect();
      chart.dispose();
    };
  }, [option]);
  return <div className={compact ? "chart chart--compact" : "chart"} ref={ref} />;
}

function ChartToolCard({
  toolCallId,
  args,
  status,
  result,
}: {
  toolCallId: string;
  args: Partial<z.infer<typeof chartSchema>>;
  status: ToolCallStatus;
  result?: string;
}) {
  const [added, setAdded] = useState(false);
  const fromResult = parseResult(result);
  const ready = status === ToolCallStatus.Complete && args.title && args.chart_type && args.option;
  const chart: Chart | undefined = ready
    ? {
        id: fromResult?.id ?? `chart-${toolCallId}`,
        title: fromResult?.title ?? args.title!,
        chartType: fromResult?.chartType ?? args.chart_type!,
        description: fromResult?.description ?? args.description,
        option: (fromResult?.option ?? args.option!) as echarts.EChartsOption,
      }
    : undefined;

  if (!chart) {
    return (
      <section className="tool-card tool-card--loading">
        <div className="pulse" />
        <div><strong>正在生成图表</strong><span>AG-UI 正在流式传输工具参数…</span></div>
      </section>
    );
  }

  return (
    <section className="tool-card">
      <header><div><strong>{chart.title}</strong><span>{chart.description || `${chart.chartType} chart`}</span></div><em>{chart.chartType}</em></header>
      <EChart option={chart.option} compact />
      <button
        className={added ? "add-button add-button--done" : "add-button"}
        disabled={added}
        onClick={() => {
          window.dispatchEvent(new CustomEvent(addChartEvent, { detail: chart }));
          setAdded(true);
        }}
        type="button"
      >
        {added ? <><Check size={15} />已添加到仪表盘</> : <><Plus size={15} />添加到仪表盘</>}
      </button>
    </section>
  );
}

const chartRenderer = defineToolCallRenderer({
  name: "render_chart",
  args: chartSchema,
  render: (props) => <ChartToolCard {...props} />,
});

function IncrementalRunInput() {
  const { agent, isReady } = useAgent({ agentId: "chart-agent" });

  useEffect(() => {
    if (!isReady || incrementalAgents.has(agent)) return;
    agent.use((input, next) => next.run({
      ...input,
      // The Go Session is authoritative for history; only the newest client
      // message needs to cross the network for a normal chat submission.
      messages: input.messages.slice(-1),
    }));
    incrementalAgents.add(agent);
  }, [agent, isReady]);

  return null;
}

export function App() {
  const [threadID, setThreadID] = useState(initialThreadID);
  const [charts, setCharts] = useState<Chart[]>([]);

  useEffect(() => {
    const add = (event: Event) => {
      const chart = (event as AddChartEvent).detail;
      setCharts((current) => current.some((item) => item.id === chart.id) ? current : [...current, chart]);
    };
    window.addEventListener(addChartEvent, add);
    return () => window.removeEventListener(addChartEvent, add);
  }, []);

  const provider = useMemo(() => (
    <CopilotKitProvider
      runtimeUrl="/api/copilotkit"
      useSingleEndpoint={false}
      renderToolCalls={[chartRenderer]}
      showDevConsole={false}
      onError={(event) => console.error("CopilotKit", event)}
    >
      <IncrementalRunInput />
      <CopilotChat
        key={threadID}
        agentId="chart-agent"
        threadId={threadID}
        labels={{
          modalHeaderTitle: "图表 Copilot",
          welcomeMessageText: "告诉我你想看的数据，我会通过 AG-UI 调用 render_chart 并把 ECharts 预览渲染在消息中。",
          chatInputPlaceholder: "例如：生成各渠道销售占比饼图",
        }}
      />
    </CopilotKitProvider>
  ), [threadID]);

  function resetThread() {
    const next = newThreadID();
    localStorage.setItem(threadStorageKey, next);
    setThreadID(next);
  }

  return (
    <main className="workspace">
      <section className="dashboard-panel">
        <header className="topbar">
          <div className="brand"><span><BarChart3 size={20} /></span><div><strong>Pulseboard</strong><small>AG-UI protocol lab</small></div></div>
          <div className="protocol-badge"><i />AG-UI · CopilotKit 1.69</div>
        </header>
        <div className="dashboard-heading">
          <div><p><Sparkles size={14} />AI GENERATIVE ANALYTICS</p><h1>数据仪表盘</h1><span>在右侧对话生成图表，再添加到这里。</span></div>
          <div className="metric"><b>{charts.length}</b><span>已添加图表</span></div>
        </div>
        {charts.length === 0 ? (
          <div className="empty-dashboard"><span><CirclePlus size={28} /></span><h2>仪表盘还是空的</h2><p>试试“生成月度销售趋势折线图”</p></div>
        ) : (
          <div className="chart-grid">
            {charts.map((chart) => <article className="dashboard-card" key={chart.id}><header><div><strong>{chart.title}</strong><span>{chart.description}</span></div><em>{chart.chartType}</em></header><EChart option={chart.option} /></article>)}
          </div>
        )}
      </section>
      <aside className="chat-panel">
        <div className="chat-heading"><div><span><Sparkles size={16} /></span><div><strong>Analytics Copilot</strong><small><i />AG-UI stream connected</small></div></div><button onClick={resetThread} title="新建对话" type="button"><RefreshCw size={16} /></button></div>
        <div className="chat-body">{provider}</div>
        <footer>thread: {threadID}</footer>
      </aside>
    </main>
  );
}
