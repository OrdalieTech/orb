// e2e fixture for the real agent_session_v1 runtime: drives the materialized
// orb-extension-sdk createAgentSession against the NewAgentSession-backed
// service (faux provider injected Go-side) and reports JSON through tool
// output.
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const sdkRoot = process.env.ORB_EXTENSION_SDK_ROOT;
const sdkImport = (name) => import(pathToFileURL(join(sdkRoot, name)).href);
const codingAgent = await sdkImport("coding-agent.mjs");

const text = (value) => ({ content: [{ type: "text", text: JSON.stringify(value) }] });

export default function activate(pi) {
	pi.registerTool({
		name: "child_flow",
		description: "native child session flow over agent_session_v1",
		parameters: { type: "object", properties: {} },
		async execute(_id, _params, _signal, _onUpdate, ctx) {
			const loader = new codingAgent.DefaultResourceLoader({ cwd: ctx.cwd, noExtensions: true });
			await loader.reload();
			let storeParams = null;
			const captured = { value: null };
			const customTools = [
				codingAgent.defineTool({
					name: "store_put",
					description: "store a value",
					parameters: { type: "object", properties: { key: { type: "string" } }, required: ["key"] },
					prepareArguments: (args) => ({ ...args, key: String(args.key).toUpperCase() }),
					execute: async (toolCallId, toolParams) => {
						storeParams = toolParams;
						return { content: [{ type: "text", text: `stored:${toolParams.key}:${toolCallId}` }] };
					},
				}),
				codingAgent.defineTool({
					name: "structured_output",
					description: "capture structured output",
					parameters: { type: "object", properties: { value: { type: "object" } }, required: ["value"] },
					execute: async (_toolCallId, toolParams) => {
						captured.value = toolParams.value;
						return { content: [{ type: "text", text: "captured" }], terminate: true };
					},
				}),
				...codingAgent.createCodingTools(ctx.cwd).filter((tool) => tool.name === "read"),
			];
			const created = await codingAgent.createAgentSession({
				cwd: ctx.cwd,
				// Off-catalog synthesized model: routed by its own fields.
				model: { provider: "faux", id: "synth-model", name: "Synthesized" },
				excludeTools: ["workflow", "workflow_control"],
				customTools,
				sessionManager: codingAgent.SessionManager.inMemory(),
				settingsManager: codingAgent.SettingsManager.create(ctx.cwd),
				resourceLoader: loader,
			});
			const session = created.session;
			const subscribeEvents = [];
			const unsubscribe = session.subscribe((event) => subscribeEvents.push(event?.type ?? "event"));
			await session.prompt("go");
			// Everything below observes state as of prompt resolution: the
			// events-before-terminal contract means the mirrors are complete.
			const eventsAtPromptResolve = subscribeEvents.length;
			const stats = session.getSessionStats();
			const messages = session.messages;
			unsubscribe();
			await session.dispose();
			await session.dispose(); // SDK-side dedupe
			return text({
				modelFallbackMessage: created.modelFallbackMessage ?? null,
				eventsAtPromptResolve,
				subscribeEventTypes: [...new Set(subscribeEvents)].sort(),
				stats,
				messageCount: messages.length,
				roles: messages.map((message) => message?.role ?? "unknown"),
				lastRole: messages.length > 0 ? (messages[messages.length - 1]?.role ?? null) : null,
				storeParams,
				captured: captured.value,
			});
		},
	});
}
