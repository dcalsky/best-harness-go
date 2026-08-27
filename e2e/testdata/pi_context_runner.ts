import { Agent } from "@earendil-works/pi-agent-core";
import { AssistantMessageEventStream } from "@earendil-works/pi-ai";
import { transformMessages } from "@earendil-works/pi-ai/api/transform-messages";

interface FixtureContent {
	type: "text" | "thinking" | "image" | "toolCall";
	text?: string;
	thinking?: string;
	thinkingSignature?: string;
	data?: string;
	mimeType?: string;
	id?: string;
	name?: string;
	arguments?: Record<string, unknown>;
}

interface FixtureUsage {
	input: number;
	output: number;
	cacheRead: number;
	cacheWrite: number;
	totalTokens: number;
}

interface FixtureResponse {
	content: FixtureContent[];
	stopReason: "stop" | "length" | "toolUse" | "error" | "aborted";
	errorMessage?: string;
	usage: FixtureUsage;
}

interface FixtureTool {
	name: string;
	description: string;
	parameters: Record<string, unknown>;
	result: FixtureContent[];
	isError: boolean;
}

interface Fixture {
	systemPrompt: string;
	prompts: string[];
	responses: FixtureResponse[];
	tools: FixtureTool[];
	normalizeMessages?: Array<Record<string, unknown>>;
}

async function main(): Promise<void> {
const input = await new Promise<string>((resolve, reject) => {
	let data = "";
	process.stdin.setEncoding("utf8");
	process.stdin.on("data", (chunk: string) => {
		data += chunk;
	});
	process.stdin.on("end", () => resolve(data));
	process.stdin.on("error", reject);
});
const fixture = JSON.parse(input) as Fixture;
const contexts: unknown[] = [];
let responseIndex = 0;

function canonicalContent(content: FixtureContent): Record<string, unknown> {
	const canonical: Record<string, unknown> = { type: content.type };
	if (content.type === "text") canonical.text = content.text ?? "";
	if (content.type === "thinking") {
		canonical.thinking = content.thinking ?? "";
		if (content.thinkingSignature) canonical.thinkingSignature = content.thinkingSignature;
	}
	if (content.type === "image") {
		canonical.data = content.data ?? "";
		canonical.mimeType = content.mimeType ?? "";
	}
	if (content.type === "toolCall") {
		canonical.id = content.id ?? "";
		canonical.name = content.name ?? "";
		canonical.arguments = content.arguments ?? {};
	}
	return canonical;
}

function canonicalContext(context: {
	systemPrompt: string;
	messages: Array<Record<string, unknown>>;
	tools?: Array<Record<string, unknown>>;
}): Record<string, unknown> {
	return {
		systemPrompt: context.systemPrompt,
		messages: context.messages.map((message) => {
			const role = message.role as string;
			const rawContent =
				typeof message.content === "string"
					? [{ type: "text", text: message.content }]
					: (message.content as FixtureContent[]);
			const canonical: Record<string, unknown> = {
				role,
				content: rawContent.map(canonicalContent),
			};
			if (role === "assistant") {
				canonical.provider = message.provider;
				canonical.model = message.model;
				canonical.stopReason = message.stopReason;
				if (message.errorMessage) canonical.errorMessage = message.errorMessage;
				const usage = message.usage as FixtureUsage;
				canonical.usage = {
					input: usage.input ?? 0,
					output: usage.output ?? 0,
					cacheRead: usage.cacheRead ?? 0,
					cacheWrite: usage.cacheWrite ?? 0,
					totalTokens: usage.totalTokens ?? 0,
				};
			}
			if (role === "toolResult") {
				canonical.toolCallId = message.toolCallId;
				canonical.toolName = message.toolName;
				canonical.isError = message.isError ?? false;
			}
			return canonical;
		}),
		tools: (context.tools ?? []).map((tool) => ({
			name: tool.name,
			description: tool.description,
			parameters: tool.parameters,
		})),
	};
}

const tools = (fixture.tools ?? []).map((fixtureTool) => ({
	name: fixtureTool.name,
	label: fixtureTool.name,
	description: fixtureTool.description,
	parameters: fixtureTool.parameters,
	execute: async () => ({
		content: fixtureTool.result,
		details: {},
	}),
}));

const model = {
	id: "mock",
	name: "mock",
	api: "openai-responses",
	provider: "parity",
	baseUrl: "https://example.invalid",
	reasoning: false,
	input: ["text"],
	cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
	contextWindow: 8192,
	maxTokens: 2048,
};

if (fixture.normalizeMessages) {
	const normalized = transformMessages(
		fixture.normalizeMessages as never,
		model as never,
		(id) => id,
	);
	process.stdout.write(
		JSON.stringify([
			canonicalContext({
				systemPrompt: fixture.systemPrompt ?? "",
				messages: normalized as unknown as Array<Record<string, unknown>>,
				tools: [],
			}),
		]),
	);
	return;
}

const agent = new Agent({
	initialState: {
		systemPrompt: fixture.systemPrompt,
		model,
		thinkingLevel: "off",
		tools,
	},
	streamFn: (_model, context) => {
		contexts.push(
			canonicalContext(
				context as unknown as {
					systemPrompt: string;
					messages: Array<Record<string, unknown>>;
					tools?: Array<Record<string, unknown>>;
				},
			),
		);
		const response = fixture.responses[responseIndex++];
		if (!response) throw new Error(`pi requested unexpected provider turn ${responseIndex}`);
		const stream = new AssistantMessageEventStream();
		queueMicrotask(() => {
			const message = {
				role: "assistant" as const,
				content: response.content,
				api: "openai-responses" as const,
				provider: "parity",
				model: "mock",
				usage: {
					...response.usage,
					cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
				},
				stopReason: response.stopReason,
				errorMessage: response.errorMessage,
				timestamp: Date.now(),
			};
			stream.push({ type: "done", reason: response.stopReason, message });
		});
		return stream;
	},
	afterToolCall: async ({ toolCall }) => ({
		isError: (fixture.tools ?? []).find((tool) => tool.name === toolCall.name)?.isError ?? false,
	}),
});

for (const prompt of fixture.prompts) await agent.prompt(prompt);
if (responseIndex !== fixture.responses.length) {
	throw new Error(`pi consumed ${responseIndex} responses, fixture contains ${fixture.responses.length}`);
}
process.stdout.write(JSON.stringify(contexts));
}

void main().catch((error: unknown) => {
	process.stderr.write(`${error instanceof Error ? error.stack : String(error)}\n`);
	process.exitCode = 1;
});
