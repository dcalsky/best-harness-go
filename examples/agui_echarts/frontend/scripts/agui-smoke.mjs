import { EventType, HttpAgent } from "@ag-ui/client";

const baseURL = process.env.AGENT_URL ?? "http://127.0.0.1:8080/api/copilotkit/agent/chart-agent/run";
const threadId = `smoke-${Date.now()}`;
const agent = new HttpAgent({ agentId: "chart-agent", threadId, url: baseURL });
const events = [];

agent.addMessage({
  id: crypto.randomUUID(),
  role: "user",
  content: "生成各渠道占比饼图",
});

const result = await agent.runAgent({}, {
  onEvent: ({ event }) => events.push(event),
});

const types = events.map((event) => event.type);
for (const required of [
  EventType.RUN_STARTED,
  EventType.TOOL_CALL_START,
  EventType.TOOL_CALL_ARGS,
  EventType.TOOL_CALL_END,
  EventType.TOOL_CALL_RESULT,
  EventType.RUN_FINISHED,
]) {
  if (!types.includes(required)) {
    throw new Error(`missing ${required}: ${types.join(", ")}`);
  }
}
if (types.at(-1) !== EventType.RUN_FINISHED) {
  throw new Error(`terminal event is ${types.at(-1)}`);
}
if (!result.newMessages.some((message) => message.role === "tool")) {
  throw new Error("AG-UI client did not materialize the tool result message");
}
if (!agent.messages.some((message) => message.role === "assistant")) {
  throw new Error("AG-UI client did not materialize an assistant message");
}

console.log(JSON.stringify({ ok: true, threadId, events: types, messages: agent.messages.length }, null, 2));
