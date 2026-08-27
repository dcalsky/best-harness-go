import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useChat } from "@ai-sdk/react";
import { DefaultChatTransport, getToolName, isToolUIPart, type UIMessage } from "ai";
import * as echarts from "echarts";
import {
  BarChart3Icon, BotIcon, ChartNoAxesCombinedIcon, CheckIcon, CircleGaugeIcon,
  LayoutDashboardIcon, PlusIcon, SettingsIcon, SparklesIcon, Trash2Icon, UsersIcon, WrenchIcon,
} from "lucide-react";
import { Conversation, ConversationContent, ConversationScrollButton } from "@/components/ai-elements/conversation";
import { Message, MessageContent, MessageResponse } from "@/components/ai-elements/message";
import { PromptInput, PromptInputFooter, PromptInputSubmit, PromptInputTextarea, type PromptInputMessage } from "@/components/ai-elements/prompt-input";
import { TooltipProvider } from "@/components/ui/tooltip";

type ChartSpec = {
  id: string;
  title: string;
  chartType: string;
  description?: string;
  option: echarts.EChartsOption;
};
type ChartCandidate = { sessionId: string; chart: ChartSpec };
type DashboardChart = ChartSpec & { sessionId?: string; source: "sample" | "agent" };
type DashboardUIMessage = UIMessage<unknown, { chart: ChartCandidate }>;

const currentSessionKey = "pulseboard.ai-sdk.currentSession";
const dashboardStorageKey = "pulseboard.ai-sdk.dashboardCharts";
const starterPrompts = ["生成月度销售趋势折线图", "生成各渠道占比饼图", "创建区域销售排名柱状图"];
const sampleCharts: DashboardChart[] = [
  {
    id: "revenue-trend", source: "sample", title: "收入趋势", chartType: "area", description: "每日收入与目标基准",
    option: {
      tooltip: { trigger: "axis" }, legend: { top: 0, right: 0, data: ["实际收入", "目标"] },
      grid: { left: 12, right: 12, top: 42, bottom: 10, containLabel: true },
      xAxis: { type: "category", boundaryGap: false, data: ["01", "05", "09", "13", "17", "21", "25", "29"] },
      yAxis: { type: "value", splitLine: { lineStyle: { color: "#f0f0f4" } } },
      series: [
        { name: "实际收入", type: "line", smooth: true, symbol: "none", lineStyle: { color: "#7657f6", width: 3 }, areaStyle: { color: "rgba(118,87,246,.12)" }, data: [28, 42, 36, 58, 51, 68, 74, 91] },
        { name: "目标", type: "line", smooth: true, symbol: "none", lineStyle: { color: "#c4c4cd", width: 1, type: "dashed" }, data: [32, 37, 43, 50, 57, 64, 71, 78] },
      ],
    },
  },
  {
    id: "channel-mix", source: "sample", title: "流量来源", chartType: "pie", description: "按访问渠道划分",
    option: {
      tooltip: { trigger: "item" }, legend: { orient: "vertical", right: 8, top: "middle" },
      series: [{ type: "pie", center: ["38%", "52%"], radius: ["43%", "68%"], label: { show: false }, data: [
        { name: "自然搜索", value: 42, itemStyle: { color: "#7657f6" } },
        { name: "直接访问", value: 26, itemStyle: { color: "#4e90f2" } },
        { name: "社交媒体", value: 19, itemStyle: { color: "#30b586" } },
        { name: "广告投放", value: 13, itemStyle: { color: "#f2ae54" } },
      ] }],
    },
  },
];

function newSessionID() { return `ai-${crypto.randomUUID?.() ?? Date.now().toString(36)}`; }
function initialSessionID() {
  const current = sessionStorage.getItem(currentSessionKey);
  if (current) return current;
  const next = newSessionID();
  sessionStorage.setItem(currentSessionKey, next);
  return next;
}
function initialDashboardCharts() {
  try {
    const stored = JSON.parse(localStorage.getItem(dashboardStorageKey) ?? "[]") as DashboardChart[];
    return [...sampleCharts, ...stored.filter((chart) => chart?.source === "agent" && chart.id && chart.option)];
  } catch {
    return sampleCharts;
  }
}
function isToolPart(part: DashboardUIMessage["parts"][number]) { return isToolUIPart(part); }

function ChartCanvas({ option, className = "h-64" }: { option: echarts.EChartsOption; className?: string }) {
  const targetRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!targetRef.current) return;
    const instance = echarts.init(targetRef.current, undefined, { renderer: "canvas" });
    instance.setOption(option, { notMerge: true });
    const observer = new ResizeObserver(() => instance.resize());
    observer.observe(targetRef.current);
    return () => { observer.disconnect(); instance.dispose(); };
  }, [option]);
  return <div className={className} ref={targetRef} />;
}

function ChartPreview({ candidate, added, onAdd }: { candidate: ChartCandidate; added: boolean; onAdd: (candidate: ChartCandidate) => void }) {
  return (
    <section className="mt-3 overflow-hidden rounded-xl border border-violet-100 bg-white shadow-sm">
      <div className="border-b border-zinc-100 px-3 py-2.5"><div className="flex items-start justify-between gap-3">
        <div><h3 className="text-xs font-semibold text-zinc-900">{candidate.chart.title}</h3><p className="mt-1 text-[10px] text-zinc-500">{candidate.chart.description || `${candidate.chart.chartType} chart`}</p></div>
        <span className="rounded-md bg-violet-50 px-1.5 py-1 text-[9px] font-semibold uppercase text-violet-600">{candidate.chart.chartType}</span>
      </div></div>
      <ChartCanvas className="h-48 w-full" option={candidate.chart.option} />
      <div className="border-t border-zinc-100 p-2.5"><button className={`flex w-full items-center justify-center gap-1.5 rounded-lg px-3 py-2 text-xs font-semibold transition ${added ? "cursor-default bg-emerald-50 text-emerald-700" : "bg-violet-600 text-white hover:bg-violet-700"}`} disabled={added} onClick={() => onAdd(candidate)} type="button">
        {added ? <><CheckIcon className="size-3.5" />已添加到仪表盘</> : <><PlusIcon className="size-3.5" />添加到仪表盘</>}
      </button></div>
    </section>
  );
}

type ChatSessionProps = {
  sessionID: string; dashboardCount: number; addedChartIDs: Set<string>;
  onAddChart: (candidate: ChartCandidate) => void; onNewSession: () => void; onEvent: (message: string) => void;
};

function ChatSession({ sessionID, dashboardCount, addedChartIDs, onAddChart, onNewSession, onEvent }: ChatSessionProps) {
  const [hydrated, setHydrated] = useState(false);
  const transport = useMemo(() => new DefaultChatTransport<DashboardUIMessage>({
    api: "/api/chat",
    prepareSendMessagesRequest: ({ id, messages, trigger, messageId }) => ({ body: { id, messages: messages.slice(-1), trigger, messageId } }),
  }), []);
  const { messages, sendMessage, setMessages, status, stop, error } = useChat<DashboardUIMessage>({ id: sessionID, transport, onError: (reason) => onEvent(`Agent 错误：${reason.message}`) });

  useEffect(() => {
    const controller = new AbortController();
    setHydrated(false);
    void fetch(`/api/sessions/${encodeURIComponent(sessionID)}/messages`, { signal: controller.signal })
      .then(async (response) => { if (!response.ok) throw new Error(`会话恢复失败 (${response.status})`); return response.json() as Promise<DashboardUIMessage[]>; })
      .then((history) => { setMessages(history); setHydrated(true); })
      .catch((reason: Error) => { if (reason.name !== "AbortError") onEvent(reason.message); setHydrated(true); });
    return () => controller.abort();
  }, [onEvent, sessionID, setMessages]);

  const submitText = useCallback(async (text: string) => {
    if (!text.trim() || status !== "ready" || !hydrated) return;
    await sendMessage({ text: text.trim() });
  }, [hydrated, sendMessage, status]);
  const handleSubmit = useCallback(async (message: PromptInputMessage) => submitText(message.text), [submitText]);
  const streaming = status === "submitted" || status === "streaming";

  return (
    <aside className="flex h-full min-h-0 flex-col border-l border-zinc-200 bg-white text-zinc-900">
      <header className="flex h-[99px] shrink-0 items-center gap-3 border-b border-zinc-200 px-5">
        <div className="grid size-10 place-items-center rounded-xl bg-gradient-to-br from-violet-500 to-violet-700 text-white shadow-lg shadow-violet-200"><SparklesIcon className="size-5" /></div>
        <div className="min-w-0"><h1 className="m-0 text-sm font-semibold tracking-tight">Pulse Agent</h1><p className="mt-1 flex items-center gap-1.5 text-[10px] text-zinc-500"><span className="size-1.5 rounded-full bg-emerald-500" />AI SDK · {dashboardCount} 张图表 · {sessionID.slice(-8)}</p></div>
        <button className="ml-auto grid size-8 place-items-center rounded-lg bg-zinc-100 text-zinc-500 hover:bg-violet-100 hover:text-violet-700" onClick={onNewSession} title="开始新会话" type="button"><PlusIcon className="size-4" /></button>
      </header>
      <Conversation className="min-h-0 bg-zinc-50/40"><ConversationContent className="gap-4 p-4">
        <Message from="assistant"><MessageContent className="rounded-xl bg-white p-3 shadow-sm ring-1 ring-zinc-100"><div className="mb-2 flex items-center gap-1.5 text-[10px] font-medium text-violet-600"><BotIcon className="size-3" />PULSE AGENT · AI SDK</div><MessageResponse>描述你想分析的指标。我会生成并预览图表，由你确认后直接添加到当前仪表盘；点击仪表盘中的 Agent 图表可以恢复对应会话。</MessageResponse></MessageContent></Message>
        {!hydrated && <div className="px-1 text-[10px] text-zinc-400">正在恢复 Session…</div>}
        {messages.map((message) => <Message from={message.role} key={message.id}><MessageContent className={message.role === "user" ? "bg-violet-600 text-white" : "rounded-xl bg-white p-3 shadow-sm ring-1 ring-zinc-100"}>
          {message.parts.map((part, index) => {
            if (part.type === "text") return message.role === "assistant" ? <MessageResponse key={`${message.id}-${index}`}>{part.text}</MessageResponse> : <span key={`${message.id}-${index}`}>{part.text}</span>;
            if (part.type === "data-chart") return <ChartPreview added={addedChartIDs.has(part.data.chart.id)} candidate={part.data} key={`${message.id}-${part.data.chart.id}`} onAdd={onAddChart} />;
            if (isToolPart(part)) return <div className="mt-2 flex items-center gap-2 rounded-lg border border-violet-100 bg-violet-50 px-2.5 py-2 text-xs text-violet-800" key={`${message.id}-${index}`}><WrenchIcon className="size-3.5" /><span>{getToolName(part)} · {part.state}</span></div>;
            return null;
          })}
        </MessageContent></Message>)}
        {error && <div className="rounded-lg bg-red-50 p-2 text-xs text-red-700">{error.message}</div>}
      </ConversationContent><ConversationScrollButton /></Conversation>
      <div className="shrink-0 border-t border-zinc-200 bg-white p-4">
        {messages.length === 0 && hydrated && <div className="mb-3 grid gap-2">{starterPrompts.map((suggestion) => <button className="rounded-lg border border-zinc-200 bg-white px-3 py-2 text-left text-xs text-zinc-600 transition hover:border-violet-300 hover:bg-violet-50 hover:text-violet-700" key={suggestion} onClick={() => void submitText(suggestion)} type="button">{suggestion}</button>)}</div>}
        <PromptInput className="rounded-xl border border-zinc-300 bg-white shadow-sm" onSubmit={handleSubmit}><PromptInputTextarea className="min-h-12 text-xs" placeholder="描述你想生成或调整的图表…" /><PromptInputFooter className="justify-between p-2"><span className="text-[10px] text-zinc-400">AI SDK · UI Message Stream</span><PromptInputSubmit disabled={!hydrated} onClick={streaming ? () => void stop() : undefined} status={status} /></PromptInputFooter></PromptInput>
      </div>
    </aside>
  );
}

function Dashboard({ charts, onRemove, onSelect }: { charts: DashboardChart[]; onRemove: (id: string) => void; onSelect: (chart: DashboardChart) => void }) {
  const navigation = [["概览", LayoutDashboardIcon], ["分析", BarChart3Icon], ["报表", CircleGaugeIcon], ["受众", UsersIcon]] as const;
  return (
    <div className="flex min-h-0 bg-[#f8f8fb]">
      <aside className="hidden w-52 shrink-0 flex-col border-r border-zinc-200 bg-white p-5 lg:flex">
        <div className="mb-10 flex items-center gap-2 text-base font-bold text-zinc-900"><span className="grid size-8 place-items-center rounded-lg bg-violet-600 text-white"><ChartNoAxesCombinedIcon className="size-4" /></span>pulseboard</div>
        <nav className="space-y-1">{navigation.map(([label, Icon], index) => <button className={`flex w-full items-center gap-3 rounded-lg px-3 py-2.5 text-sm ${index === 0 ? "bg-violet-50 font-semibold text-violet-700" : "text-zinc-500 hover:bg-zinc-50"}`} key={label} type="button"><Icon className="size-4" />{label}</button>)}</nav>
        <div className="mt-auto space-y-3"><div className="flex items-center gap-2 rounded-xl bg-zinc-50 p-2"><span className="grid size-8 place-items-center rounded-lg bg-zinc-800 text-xs font-bold text-white">YP</span><div><b className="block text-[11px]">Yiheng's Space</b><small className="text-[9px] text-zinc-400">Professional</small></div></div><button className="flex items-center gap-3 px-3 text-sm text-zinc-500" type="button"><SettingsIcon className="size-4" />设置</button></div>
      </aside>
      <main className="min-w-0 flex-1 overflow-y-auto">
        <header className="flex h-[99px] items-center justify-between border-b border-zinc-200 bg-white px-7"><div><p className="text-[9px] font-semibold tracking-[0.2em] text-violet-500">ANALYTICS / OVERVIEW</p><h1 className="mt-1 text-xl font-bold text-zinc-900">增长概览</h1></div><div className="flex items-center gap-3"><span className="hidden items-center gap-1.5 text-xs text-zinc-500 sm:flex"><i className="size-2 rounded-full bg-emerald-500" />实时数据</span><button className="rounded-lg border border-zinc-200 bg-white px-3 py-2 text-xs text-zinc-600" type="button">过去 30 天⌄</button></div></header>
        <div className="p-5 xl:p-7">
          <div className="mb-5"><h2 className="text-base font-bold text-zinc-900">业务脉搏</h2><p className="mt-1 text-xs text-zinc-400">关键指标与实时趋势</p></div>
          <section className="mb-5 grid grid-cols-2 gap-3 xl:grid-cols-4">{[
            ["总收入", "¥ 1,284,920", "↗ 12.8%"], ["活跃用户", "84,291", "↗ 8.2%"], ["转化率", "4.82%", "↘ 0.4%"], ["客单价", "¥ 328.40", "↗ 3.7%"],
          ].map(([label, value, trend]) => <article className="rounded-xl border border-zinc-200 bg-white p-4 shadow-sm" key={label}><span className="text-[10px] text-zinc-400">{label}</span><strong className="mt-2 block text-lg text-zinc-900">{value}</strong><div className={`mt-2 text-[10px] ${trend.startsWith("↗") ? "text-emerald-600" : "text-amber-600"}`}>{trend} <small className="text-zinc-400">较上周期</small></div></article>)}</section>
          <section className="grid grid-cols-1 gap-4 2xl:grid-cols-2">{charts.map((chart) => <article className={`group overflow-hidden rounded-xl border bg-white shadow-sm transition ${chart.sessionId ? "cursor-pointer border-violet-200 hover:shadow-md" : "border-zinc-200"}`} key={chart.id} onClick={() => onSelect(chart)}>
            <div className="flex items-start justify-between px-4 pt-4"><div><div className="flex items-center gap-2"><h3 className="text-sm font-semibold text-zinc-900">{chart.title}</h3>{chart.source === "agent" && <span className="rounded bg-violet-50 px-1.5 py-0.5 text-[8px] font-bold text-violet-600">AI</span>}</div><p className="mt-1 text-[10px] text-zinc-400">{chart.description || `${chart.chartType} chart`}</p></div>{chart.source === "agent" && <button className="grid size-7 place-items-center rounded-md text-zinc-300 opacity-0 hover:bg-red-50 hover:text-red-500 group-hover:opacity-100" onClick={(event) => { event.stopPropagation(); onRemove(chart.id); }} title="删除图表" type="button"><Trash2Icon className="size-3.5" /></button>}</div>
            <ChartCanvas className="h-64 w-full" option={chart.option} />
          </article>)}</section>
        </div>
      </main>
    </div>
  );
}

export function App() {
  const [activeSessionID, setActiveSessionID] = useState(initialSessionID);
  const [dashboardCharts, setDashboardCharts] = useState(initialDashboardCharts);
  const [events, setEvents] = useState<string[]>([]);
  const addedChartIDs = useMemo(() => new Set(dashboardCharts.map((chart) => chart.id)), [dashboardCharts]);

  useEffect(() => {
    localStorage.setItem(dashboardStorageKey, JSON.stringify(dashboardCharts.filter((chart) => chart.source === "agent")));
  }, [dashboardCharts]);

  const addEvent = useCallback((message: string) => setEvents((items) => [...items.slice(-3), message]), []);
  const switchSession = useCallback((sessionID: string) => { sessionStorage.setItem(currentSessionKey, sessionID); setActiveSessionID(sessionID); }, []);
  const startNewSession = useCallback(() => switchSession(newSessionID()), [switchSession]);
  const addChart = useCallback((candidate: ChartCandidate) => {
    setDashboardCharts((charts) => [...charts.filter((chart) => chart.id !== candidate.chart.id), { ...candidate.chart, sessionId: candidate.sessionId, source: "agent" }]);
    addEvent(`「${candidate.chart.title}」已添加到仪表盘`);
  }, [addEvent]);
  const removeChart = useCallback((chartID: string) => {
    setDashboardCharts((charts) => charts.filter((chart) => chart.id !== chartID));
    addEvent("图表已从仪表盘移除");
  }, [addEvent]);
  const selectChart = useCallback((chart: DashboardChart) => {
    if (!chart.sessionId) { addEvent("示例图表没有关联 Agent Session"); return; }
    switchSession(chart.sessionId);
    addEvent(`已恢复「${chart.title}」的 Session`);
  }, [addEvent, switchSession]);

  return <TooltipProvider><div className="relative grid h-full min-h-0 grid-cols-1 xl:grid-cols-[minmax(0,1fr)_420px]"><Dashboard charts={dashboardCharts} onRemove={removeChart} onSelect={selectChart} /><div className="min-h-[640px] xl:min-h-0"><ChatSession addedChartIDs={addedChartIDs} dashboardCount={dashboardCharts.length} key={activeSessionID} onAddChart={addChart} onEvent={addEvent} onNewSession={startNewSession} sessionID={activeSessionID} /></div>{events.length > 0 && <div className="pointer-events-none absolute bottom-5 right-5 z-20 w-80 space-y-1">{events.map((event, index) => <div className="flex items-center gap-2 rounded-md bg-zinc-900/90 px-3 py-2 text-[10px] text-white shadow" key={`${event}-${index}`}><ChartNoAxesCombinedIcon className="size-3 text-violet-300" />{event}</div>)}</div>}</div></TooltipProvider>;
}
