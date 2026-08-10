// e2e fixture for the orb-extension-sdk service protocol (sdk_v1,
// agent_session_v1, model_runtime_v1): drives the REAL materialized SDK
// modules (ORB_EXTENSION_SDK_ROOT) against the host transport and reports
// JSON results through tool output.
import { accessSync, constants, statSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const sdkRoot = process.env.ORB_EXTENSION_SDK_ROOT;
const sdkImport = (name) => import(pathToFileURL(join(sdkRoot, name)).href);
const codingAgent = await sdkImport("coding-agent.mjs");
const services = await sdkImport(join("internal", "services.mjs"));

const text = (value) => ({ content: [{ type: "text", text: JSON.stringify(value) }] });
const errorInfo = (error) => ({ name: error?.name, code: error?.code, message: String(error?.message ?? error) });

export default function activate(pi) {
	pi.registerTool({
		name: "svc_probe",
		description: "negotiated capabilities, hello payload, capability gate, resource reload",
		parameters: { type: "object", properties: {} },
		async execute(_id, _params, _signal, _onUpdate, ctx) {
			const transport = services.boundTransport();
			const result = {
				transportBound: transport !== null && transport !== undefined,
				capabilities: transport ? [...transport.capabilities].sort() : [],
				hello: typeof transport?.hello === "function" ? transport.hello() : null,
			};
			try {
				services.serviceCall("time_travel_v9", "agentSessionCreate", "coding-agent", "timeTravel", [], []);
				result.gate = { threw: false };
			} catch (error) {
				result.gate = { threw: true, ...errorInfo(error) };
			}
			try {
				await new codingAgent.DefaultResourceLoader({ cwd: ctx.cwd, noExtensions: true }).reload();
				result.reloadOk = true;
			} catch (error) {
				result.reloadOk = errorInfo(error);
			}
			return text(result);
		},
	});

	pi.registerTool({
		name: "svc_model_runtime",
		description: "model_runtime_v1 lifecycle through ModelRuntime/ModelRegistry",
		parameters: { type: "object", properties: { dir: { type: "string" } } },
		async execute(_id, params) {
			const runtime = await codingAgent.ModelRuntime.create({
				authPath: join(params.dir, "auth.json"),
				modelsPath: join(params.dir, "models.json"),
			});
			// The plugin reads the upstream-private `runtime` field through a cast.
			const registry = new codingAgent.ModelRegistry(runtime);
			const runtimeField = registry.runtime;
			const all = registry.getAll().map((model) => `${model.provider}/${model.id}`);
			const available = await runtime.getAvailable();
			await runtime.dispose();
			let afterDispose = null;
			try {
				await runtime.getAvailable();
			} catch (error) {
				afterDispose = errorInfo(error);
			}
			return text({
				handle: runtime.runtimeId,
				runtimeFieldSurvives: runtimeField === runtime,
				allCount: all.length,
				hasFixtureModel: all.includes("svc-fixture/svc-model"),
				availableIsArray: Array.isArray(available),
				hasConfiguredAuthType: typeof runtime.hasConfiguredAuth("svc-fixture"),
				afterDispose,
			});
		},
	});

	pi.registerTool({
		name: "svc_context_runtime",
		description: "ctx.modelRegistry.runtime facade over the context registry",
		parameters: { type: "object", properties: {} },
		async execute(_id, _params, _signal, _onUpdate, ctx) {
			const runtime = ctx.modelRegistry?.runtime;
			if (!runtime) return text({ hasRuntime: false });
			const registry = new codingAgent.ModelRegistry(runtime);
			let availableCount = null;
			let availableError = null;
			try {
				availableCount = (await runtime.getAvailable()).length;
			} catch (error) {
				availableError = errorInfo(error);
			}
			return text({
				hasRuntime: true,
				runtimeId: runtime.runtimeId,
				snapshotIsArray: Array.isArray(runtime.getAvailableSnapshot()),
				modelsCount: runtime.getModels().length,
				registryGetAllCount: registry.getAll().length,
				availableCount,
				availableError,
			});
		},
	});

	pi.registerTool({
		name: "svc_session_stub",
		description: "agent_session_v1 stub error shape",
		parameters: { type: "object", properties: {} },
		async execute(_id, _params, _signal, _onUpdate, ctx) {
			try {
				await codingAgent.createAgentSession({ cwd: ctx.cwd });
				return text({ created: true });
			} catch (error) {
				return text({ created: false, ...errorInfo(error) });
			}
		},
	});

	pi.registerTool({
		name: "svc_session_flow",
		description: "full agent_session_v1 round trip through createAgentSession",
		parameters: { type: "object", properties: {} },
		async execute(_id, _params, _signal, _onUpdate, ctx) {
			const sessionManager = codingAgent.SessionManager.create(ctx.cwd);
			const sessionDir = sessionManager.getSessionDir();
			let sessionDirWritable = false;
			try {
				accessSync(sessionDir, constants.W_OK);
				sessionDirWritable = statSync(sessionDir).isDirectory();
			} catch {
				sessionDirWritable = false;
			}
			const loader = new codingAgent.DefaultResourceLoader({ cwd: ctx.cwd, noExtensions: true });
			await loader.reload();
			const customTools = [
				codingAgent.defineTool({
					name: "store_put",
					description: "store a value",
					parameters: { type: "object", properties: { key: { type: "string" } } },
					prepareArguments: (args) => ({ ...args, key: String(args.key).toUpperCase() }),
					execute: async (toolCallId, toolParams) => ({
						content: [{ type: "text", text: `stored:${toolParams.key}:${toolCallId}` }],
					}),
				}),
				...codingAgent.createCodingTools(ctx.cwd).filter((tool) => tool.name === "read"),
			];
			const created = await codingAgent.createAgentSession({
				cwd: ctx.cwd,
				model: { provider: "faux", id: "synth-model", name: "Synthesized" },
				thinkingLevel: "medium",
				tools: ["read", "store_put"],
				excludeTools: ["workflow", "workflow_control"],
				customTools,
				sessionManager,
				settingsManager: codingAgent.SettingsManager.create(ctx.cwd),
				resourceLoader: loader,
				modelRuntime: ctx.modelRegistry.runtime,
			});
			const session = created.session;
			const subscribeEvents = [];
			const unsubscribe = session.subscribe((event) => subscribeEvents.push(event?.type ?? "event"));
			await session.prompt("go");
			const eventsAtPromptResolve = subscribeEvents.length;
			const stats = session.getSessionStats();
			const messages = session.messages;
			await session.setActiveToolsByName(["read"]);
			sessionManager.appendSessionInfo("workflow:test");
			// appendSessionInfo is fire-and-forget; give the wire call a beat.
			await new Promise((resolve) => setTimeout(resolve, 200));
			await session.abort();
			unsubscribe();
			await session.dispose();
			await session.dispose(); // SDK-side dedupe
			return text({
				sessionDir,
				sessionDirWritable,
				modelFallbackMessage: created.modelFallbackMessage ?? null,
				subscribeEvents,
				eventsAtPromptResolve,
				stats,
				messageCount: messages.length,
				bigLen: typeof messages[0]?.big === "string" ? messages[0].big.length : 0,
			});
		},
	});

	pi.registerTool({
		name: "svc_session_cancel",
		description: "AbortSignal propagation over service_cancel + post-dispose error shape",
		parameters: { type: "object", properties: {} },
		async execute(_id, _params, _signal, _onUpdate, ctx) {
			const transport = services.boundTransport();
			const created = await transport.agentSessionCreate({ cwd: ctx.cwd }, {});
			const controller = new AbortController();
			const prompting = transport.agentSessionPrompt(created.sessionId, "block", { signal: controller.signal });
			setTimeout(() => controller.abort(new Error("fixture-abort")), 50);
			let cancel = null;
			try {
				await prompting;
				cancel = { resolved: true };
			} catch (error) {
				cancel = errorInfo(error);
			}
			await transport.agentSessionDispose(created.sessionId);
			let afterDispose = null;
			try {
				await transport.agentSessionAbort(created.sessionId);
			} catch (error) {
				afterDispose = errorInfo(error);
			}
			return text({ cancel, afterDispose });
		},
	});
}
