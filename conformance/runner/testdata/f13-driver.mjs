// F13 Orb replay driver. Loaded through the real extension host, this
// extension registers one tool (f13_scenario) that replays the extractor's
// scenarios (conformance/extract/f13-dynamic-workflows.ts) against the plugin
// sources installed by the Go harness. The plugin's @earendil-works/pi-*
// imports resolve to the materialized orb-extension-sdk via loader.mjs; child
// sessions bridge over agent_session_v1; faux responses are scripted Go-side
// (f13_orb_harness_test.go), which also records provider calls and splices
// them over the __F13_PROVIDER_CALLS__/__F13_PENDING__ placeholders below.
import { existsSync } from "node:fs";
import { mkdir, readdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

import { createCodingTools, ModelRegistry, ModelRuntime } from "@earendil-works/pi-coding-agent";

// ---------------------------------------------------------------------------
// determinism helpers (ported from the extractor)
// ---------------------------------------------------------------------------

function withPinnedClock(fixedNow, run) {
	const NativeDate = Date;
	const nativeRandom = Math.random;
	class FixtureDate extends NativeDate {
		constructor(value) {
			super(value === undefined ? fixedNow : value);
		}
		static now() {
			return fixedNow;
		}
	}
	globalThis.Date = FixtureDate;
	Math.random = () => 0;
	return run().finally(() => {
		globalThis.Date = NativeDate;
		Math.random = nativeRandom;
	});
}

function sleep(ms) {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitFor(predicate, timeoutMs, intervalMs = 10) {
	const deadline = performance.now() + timeoutMs;
	for (;;) {
		if (await predicate()) return true;
		if (performance.now() > deadline) return false;
		await sleep(intervalMs);
	}
}

function waitForSignal(signalDir, name, timeoutMs = 30000) {
	return waitFor(() => existsSync(join(signalDir, name)), timeoutMs, 5);
}

// Theme handed to plugin render/factory seams. The extractor passed upstream's
// initialized dark theme; only `toolTitle` foreground and bold reach compared
// output (workflow renderCall), pinned to the upstream dark palette.
const themeShim = {
	fg: (key, text) => (key === "toolTitle" ? `\x1b[38;5;188m${text}\x1b[39m` : String(text)),
	bg: (_key, text) => String(text),
	bold: (text) => `\x1b[1m${text}\x1b[22m`,
	italic: (text) => String(text),
	underline: (text) => String(text),
	inverse: (text) => String(text),
	strikethrough: (text) => String(text),
};

// ---------------------------------------------------------------------------
// scenario scripts (verbatim from the extractor)
// ---------------------------------------------------------------------------

const SCRIPTS = {
	"foreground-basic": `export const meta = {
  name: "basic",
  description: "two-phase basic run",
  phases: [{ title: "Explore" }, { title: "Report" }],
};
log("starting");
const found = await agent("Describe the project marker file.", { label: "explorer" });
phase("Report");
const summary = await agent("Summarize: " + found, { label: "reporter", phase: "Report" });
return { found, summary };`,
	"structured-output": `export const meta = { name: "structured", description: "schema outputs" };
const direct = await agent("Emit fruit", {
  label: "direct",
  schema: { type: "object", properties: { fruit: { type: "string" }, count: { type: "number" } }, required: ["fruit", "count"] },
});
const repaired = await agent("Emit veg after repair", {
  label: "repaired",
  schema: { type: "object", properties: { veg: { type: "string" } }, required: ["veg"] },
});
const prose = await agent("Emit mineral in prose", {
  label: "prose",
  schema: { type: "object", properties: { mineral: { type: "string" } }, required: ["mineral"] },
});
return { direct, repaired, prose };`,
	"store-tools": `export const meta = { name: "store", description: "shared store roundtrip" };
const wrote = await agent("Write the finding to the shared store.", { label: "writer" });
const read = await agent("Read the finding from the shared store.", { label: "reader" });
return { wrote, read };`,
	"web-toolset": `export const meta = { name: "web", description: "hermetic web research" };
return await agent("Research the topic online.", { label: "researcher" });`,
	"agent-types": `export const meta = { name: "agents", description: "agentType policies and worktree cwd" };
const scout = await agent("Read the marker file.", { label: "scout", agentType: "reader" });
const isolated = await agent("Read the marker file from isolation.", { label: "isolated", agentType: "isolated" });
const ghost = await agent("No such type.", { label: "ghost", agentType: "does-not-exist" });
return { scout, isolated, ghost };`,
	"nested-workflow": `export const meta = { name: "parent", description: "nested workflow" };
const child = await workflow("child-flow", { topic: "seeds" });
const after = await agent("Use child result: " + JSON.stringify(child), { label: "after" });
return { child, after };`,
	"nested-child": `export const meta = { name: "child", description: "child flow" };
const one = await agent("Child agent one for " + JSON.stringify(args), { label: "child-one" });
return { one, args };`,
	cancellation: `export const meta = { name: "cancel", description: "mid-run abort" };
const first = await agent("First does work", { label: "one" });
const second = await agent("Second gets aborted", { label: "two" });
return { first, second };`,
	"model-routing": `export const meta = { name: "routing", description: "tier routing and fallbacks" };
const small = await agent("Small tier work", { label: "small", tier: "small" });
const untagged = await agent("Untagged work", { label: "untagged" });
const synthesized = await agent("Custom off-catalog id", { label: "synthesized", model: "faux/synth-id" });
let explicitError = null;
try {
  await agent("Pinned to a ghost", { label: "pinned", model: "ghost/nope" });
} catch (error) {
  explicitError = { code: error.code, message: error.message, recoverable: error.recoverable };
}
let tierError = null;
try {
  await agent("Big tier is broken", { label: "big", tier: "big" });
} catch (error) {
  tierError = { code: error.code, message: error.message, recoverable: error.recoverable };
}
return { small, untagged, synthesized, explicitError, tierError };`,
	"background-lifecycle": `export const meta = { name: "limits", description: "provider limit pause and resume" };
const first = await agent("Succeeds", { label: "first" });
const second = await agent("Hits the wall", { label: "second" });
return { first, second };`,
	"background-stop": `export const meta = { name: "stopper", description: "stopped mid-flight" };
return await agent("Hangs until stopped", { label: "hanging" });`,
	"persist-agent-sessions": `export const meta = { name: "persist", description: "session transcript persistence" };
return await agent("Persisted agent", { label: "keeper" });`,
	"extension-run": `export const meta = { name: "ext-run", description: "background run via the workflow tool" };
return await agent("Extension-driven work", { label: "ext-agent" });`,
	"extension-hang": `export const meta = { name: "ext-hang", description: "in-flight during quit" };
return await agent("Hangs across shutdown", { label: "ext-hanging" });`,
};

// ---------------------------------------------------------------------------
// plugin + world loading
// ---------------------------------------------------------------------------

let pluginModulesPromise = null;

function loadPlugin(root) {
	pluginModulesPromise ??= (async () => {
		const pl = (rel) => import(pathToFileURL(join(root, rel)).href);
		const [workflow, workflowManager, webTools, workflowsModelsCommand, extensionModule] = await Promise.all([
			pl("src/workflow.ts"),
			pl("src/workflow-manager.ts"),
			pl("src/web-tools.ts"),
			pl("src/workflows-models-command.ts"),
			pl("extensions/workflow.ts"),
		]);
		return { workflow, workflowManager, webTools, workflowsModelsCommand, extensionFactory: extensionModule.default };
	})();
	return pluginModulesPromise;
}

// The SDK-backed analog of the extractor's createFauxWorld: a ModelRuntime
// handle over the Go-side catalog for this scenario plus the sync facade.
async function createWorld(config) {
	const runtime = await ModelRuntime.create({
		authPath: join(config.catalogDir, "auth.json"),
		modelsPath: join(config.catalogDir, "models.json"),
	});
	await runtime.getAvailable().catch(() => {});
	const registry = new ModelRegistry(runtime);
	return {
		runtime,
		registry,
		model: (id = "faux-model") => registry.find("faux", id) ?? registry.getAll()[0],
	};
}

// ---------------------------------------------------------------------------
// capture plumbing (ported from the extractor)
// ---------------------------------------------------------------------------

function captureWorkflowOptions() {
	const events = [];
	const journal = [];
	const histories = {};
	const options = {
		onLog: (message) => events.push({ event: "log", message }),
		onPhase: (title) => events.push({ event: "phase", title }),
		onRuntimeEvent: (event) => events.push({ event: "runtime", ...event }),
		onAgentStart: (event) => events.push({ event: "agentStart", ...event }),
		onAgentEnd: (event) => events.push({ event: "agentEnd", ...event }),
		onRetrySpend: (tokens) => events.push({ event: "retrySpend", tokens }),
		onTokenUsage: (usage) => events.push({ event: "tokenUsage", usage }),
		onAgentJournal: (entry) => journal.push(entry),
		onAgentHistory: (event) => {
			histories[`${event.id} ${event.label}`] = event.history;
		},
	};
	return { events, journal, histories, options };
}

async function runWorkflowScenario(ctx, name, extraOptions = {}) {
	const world = await createWorld(ctx.config);
	const capture = captureWorkflowOptions();
	const runOptions = {
		cwd: ctx.config.project,
		runId: `run-fixed-${name}`,
		concurrency: 1,
		mainModel: "faux/faux-model",
		modelRegistry: world.registry,
		persistLogs: false,
		...capture.options,
		...extraOptions,
	};
	let result;
	let error;
	try {
		result = await ctx.plugin.workflow.runWorkflow(SCRIPTS[name], runOptions);
	} catch (thrown) {
		error = thrown instanceof Error ? { name: thrown.name, message: thrown.message, ...thrown } : thrown;
	}
	return {
		script: SCRIPTS[name],
		result: result ?? null,
		error: error ?? null,
		events: capture.events,
		journal: capture.journal,
		histories: capture.histories,
		providerCalls: "__F13_PROVIDER_CALLS__",
		pendingFauxResponses: "__F13_PENDING__",
	};
}

const MANAGER_EVENTS = [
	"log",
	"phase",
	"agentStart",
	"agentEnd",
	"tokenUsage",
	"complete",
	"paused",
	"error",
	"resumed",
	"stopped",
	"delivered",
];

function recordManagerEvents(manager) {
	const events = [];
	for (const name of MANAGER_EVENTS) {
		manager.on(name, (payload) => {
			const record = { event: name };
			if (payload && typeof payload === "object") {
				for (const [key, value] of Object.entries(payload)) {
					if (key === "history") continue;
					if (key === "error" && value instanceof Error) {
						record.error = { name: value.name, message: value.message, ...value };
						continue;
					}
					record[key] = value;
				}
			}
			events.push(record);
		});
	}
	return events;
}

function waitForEvent(manager, name) {
	return new Promise((resolve) => manager.once(name, resolve));
}

async function readRunArtifacts(config) {
	const projectsRoot = join(config.home, ".pi", "workflows", "projects");
	const artifacts = {};
	const walk = async (dir, prefix) => {
		let entries;
		try {
			entries = (await readdir(dir)).sort();
		} catch {
			return;
		}
		for (const entry of entries) {
			const full = join(dir, entry);
			const info = await stat(full);
			if (info.isDirectory()) {
				await walk(full, `${prefix}${entry}/`);
			} else if (entry.endsWith(".json")) {
				artifacts[`${prefix}${entry}`] = JSON.parse(await readFile(full, "utf8"));
			}
		}
	};
	await walk(projectsRoot, "");
	return artifacts;
}

// ---------------------------------------------------------------------------
// scenarios
// ---------------------------------------------------------------------------

async function scenarioWebToolset(ctx) {
	const web = ctx.plugin.webTools;
	const nativeFetch = globalThis.fetch;
	const searchHtml = [
		"<html><body>",
		'<h2><a href="https://www.bing.com/internal">Skip me</a></h2>',
		'<h2><a href="https://example.test/page">Example <b>Result</b></a></h2>',
		'<h2><a href="https://second.test/doc">Second Result</a></h2>',
		"</body></html>",
	].join("");
	const pageHtml =
		"<html><head><style>p{}</style><script>bad()</script></head><body><h1>Doc &amp; Title</h1><p>First&nbsp;paragraph.</p><p>Second paragraph.</p></body></html>";
	globalThis.fetch = async (input) => {
		const url = String(input);
		const body = url.includes("bing.com/search") ? searchHtml : pageHtml;
		return { status: 200, text: async () => body };
	};
	try {
		return await runWorkflowScenario(ctx, "web-toolset", {
			tools: [...createCodingTools(ctx.config.project), ...web.createWebTools()],
		});
	} finally {
		globalThis.fetch = nativeFetch;
	}
}

async function scenarioCancellation(ctx) {
	const controller = new AbortController();
	const signalFile = join(ctx.config.signalDir, "cancellation");
	// The Go faux factory drops the signal file once the second call is in
	// flight, then blocks until the abort lands (the extractor's response
	// callback aborted synchronously at the same point).
	const watcher = setInterval(() => {
		if (existsSync(signalFile)) {
			clearInterval(watcher);
			controller.abort();
		}
	}, 5);
	try {
		return await runWorkflowScenario(ctx, "cancellation", { signal: controller.signal });
	} finally {
		clearInterval(watcher);
	}
}

async function scenarioModelRouting(ctx) {
	const workflowsDir = join(ctx.config.home, ".pi", "workflows");
	await mkdir(workflowsDir, { recursive: true });
	await writeFile(
		join(workflowsDir, "model-tiers.json"),
		`${JSON.stringify({ tiers: { small: "faux/faux-mini", medium: "ghost/nowhere", big: "ghost/nowhere" } }, null, 2)}\n`,
	);
	try {
		return await runWorkflowScenario(ctx, "model-routing", {});
	} finally {
		await rm(join(workflowsDir, "model-tiers.json"), { force: true });
	}
}

async function scenarioBackgroundLifecycle(ctx) {
	const { WorkflowManager } = ctx.plugin.workflowManager;
	const world = await createWorld(ctx.config);
	const manager = new WorkflowManager({
		cwd: ctx.config.project,
		concurrency: 1,
		mainModel: "faux/faux-model",
		modelRegistry: world.registry,
		sessionId: "session-fixed-1",
	});
	const events = recordManagerEvents(manager);

	const paused = waitForEvent(manager, "paused");
	const started = manager.startInBackground(SCRIPTS["background-lifecycle"], undefined, {});
	await paused;
	const pausedRuns = manager.listRuns();
	const pausedArtifacts = await readRunArtifacts(ctx.config);

	// Resume: the journaled first agent replays; the second runs live.
	const completed = waitForEvent(manager, "complete");
	const resumed = await manager.resume(started.runId);
	await completed;
	const completedRuns = manager.listRuns();

	// Stop a hanging run (aux faux provider; its call is not recorded).
	const stopManager = new WorkflowManager({
		cwd: ctx.config.project,
		concurrency: 1,
		mainModel: "faux/faux-model",
		modelRegistry: world.registry,
		sessionId: "session-fixed-1",
	});
	const stopEvents = recordManagerEvents(stopManager);
	const stopStarted = stopManager.startInBackground(SCRIPTS["background-stop"], undefined, {});
	await waitForSignal(ctx.config.signalDir, "background-stop");
	const stopped = stopManager.stop(stopStarted.runId);
	await waitFor(
		() => stopManager.listRuns().some((run) => run.runId === stopStarted.runId && run.status !== "running"),
		5000,
	);
	await sleep(50);
	const stoppedRuns = stopManager.listRuns().filter((run) => run.runId === stopStarted.runId);

	return {
		script: SCRIPTS["background-lifecycle"],
		startedRunId: started.runId,
		events,
		pausedRuns,
		pausedArtifacts,
		resumed,
		completedRuns,
		stop: { runId: stopStarted.runId, stopped, events: stopEvents, runs: stoppedRuns },
		providerCalls: "__F13_PROVIDER_CALLS__",
		pendingFauxResponses: "__F13_PENDING__",
	};
}

async function scenarioPersistAgentSessions(ctx) {
	const result = await runWorkflowScenario(ctx, "persist-agent-sessions", { persistAgentSessions: true });
	const sessionsRoot = join(ctx.config.agentDir, "sessions");
	const artifacts = [];
	const walk = async (dir, prefix) => {
		let entries;
		try {
			entries = (await readdir(dir)).sort();
		} catch {
			return;
		}
		for (const entry of entries) {
			const full = join(dir, entry);
			const info = await stat(full);
			if (info.isDirectory()) {
				await walk(full, `${prefix}${entry}/`);
			} else {
				const content = await readFile(full, "utf8");
				// Session entry ids are random hex; remap them (and their parent
				// references) to stable ordinals so the tree shape stays comparable.
				const idMap = new Map();
				const remapIds = (value) => {
					if (Array.isArray(value)) return value.map(remapIds);
					if (value && typeof value === "object") {
						return Object.fromEntries(
							Object.entries(value).map(([key, item]) => {
								if ((key === "id" || key === "parentId") && typeof item === "string" && /^[0-9a-f]{6,}$/i.test(item)) {
									let alias = idMap.get(item);
									if (!alias) {
										alias = `<entry-${idMap.size + 1}>`;
										idMap.set(item, alias);
									}
									return [key, alias];
								}
								return [key, remapIds(item)];
							}),
						);
					}
					return value;
				};
				artifacts.push({
					path: `${prefix}${entry}`,
					entries: content
						.split("\n")
						.filter(Boolean)
						.map((line) => remapIds(JSON.parse(line))),
				});
			}
		}
	};
	await walk(sessionsRoot, "");
	return { ...result, sessionArtifacts: artifacts };
}

// ---------------------------------------------------------------------------
// extension lifecycle scenario
// ---------------------------------------------------------------------------

function createCaptureExtension() {
	const handlers = new Map();
	const tools = new Map();
	const commands = new Map();
	const sentMessages = [];
	const registrationLog = [];
	const activeTools = ["read", "bash", "edit", "write"];
	const target = {
		on(event, handler) {
			handlers.set(event, [...(handlers.get(event) ?? []), handler]);
		},
		registerTool(tool) {
			tools.set(tool.name, tool);
			registrationLog.push({ kind: "tool", name: tool.name });
		},
		registerCommand(name, command) {
			commands.set(name, command);
			registrationLog.push({ kind: "command", name, description: command?.description ?? null });
		},
		getCommands() {
			return [...commands.entries()].map(([name, command]) => ({ name, description: command?.description ?? null }));
		},
		getActiveTools() {
			return [...activeTools];
		},
		setActiveTools(names) {
			activeTools.splice(0, activeTools.length, ...names);
			registrationLog.push({ kind: "setActiveTools", names: [...names] });
		},
		sendMessage(message, options) {
			sentMessages.push({ message, options: options ?? null });
		},
		events: { emit() {} },
	};
	const api = new Proxy(target, {
		get(t, property) {
			if (property in t) return t[property];
			return () => {};
		},
	});
	return { api, handlers, tools, commands, sentMessages, activeTools, registrationLog };
}

async function emitExtensionEvent(capture, event, value, ctx) {
	for (const handler of capture.handlers.get(event) ?? []) {
		await handler(value, ctx);
	}
}

function projectToolDefinition(tool) {
	return {
		name: tool.name,
		label: tool.label ?? null,
		description: tool.description ?? null,
		promptSnippet: tool.promptSnippet ?? null,
		promptGuidelines: tool.promptGuidelines ?? null,
		parameters: JSON.parse(JSON.stringify(tool.parameters)),
		hasPrepareArguments: typeof tool.prepareArguments === "function",
		hasRenderCall: typeof tool.renderCall === "function",
		hasRenderResult: typeof tool.renderResult === "function",
	};
}

async function scenarioExtensionLifecycle(ctx) {
	const world = await createWorld(ctx.config);
	const theme = themeShim;

	const originalCwd = process.cwd();
	process.chdir(ctx.config.project);
	let generationOne;
	try {
		generationOne = createCaptureExtension();
		ctx.plugin.extensionFactory(generationOne.api);
	} finally {
		process.chdir(originalCwd);
	}

	const widgets = new Map();
	const notifications = [];
	const makeCtx = (sessionId) => ({
		cwd: ctx.config.project,
		hasUI: true,
		model: world.model(),
		modelRegistry: world.registry,
		sessionManager: { getSessionId: () => sessionId },
		ui: {
			notify: (message, level) => notifications.push({ message, level: level ?? null }),
			setWidget: (key, factory, options) => widgets.set(key, { factory, options: options ?? null }),
			setStatus() {},
			select: async () => undefined,
			confirm: async () => false,
			input: async () => undefined,
			custom: async () => undefined,
		},
	});

	await emitExtensionEvent(generationOne, "session_start", { type: "session_start", reason: "startup" }, makeCtx("session-a"));

	const workflowTool = generationOne.tools.get("workflow");
	const controlTool = generationOne.tools.get("workflow_control");

	// Lazy promptGuidelines: re-read available models on every access.
	const guidelinesFirst = workflowTool.promptGuidelines;
	const guidelinesSecond = workflowTool.promptGuidelines;

	const prepareCases = {};
	const tryPrepare = (label, tool, args) => {
		try {
			prepareCases[label] = { ok: true, value: tool.prepareArguments(args) };
		} catch (error) {
			prepareCases[label] = { ok: false, error: error instanceof Error ? error.message : String(error) };
		}
	};
	tryPrepare("control-list", controlTool, { action: "list" });
	tryPrepare("control-missing-run", controlTool, { action: "pause" });
	tryPrepare("control-extra-key", controlTool, { action: "list", runId: "run-x" });
	tryPrepare("control-bad-action", controlTool, { action: "dance" });
	tryPrepare("workflow-script", workflowTool, { script: SCRIPTS["extension-run"] });
	tryPrepare("workflow-bad", workflowTool, { script: 42 });

	// Background run through the REAL registered workflow tool.
	const toolCtx = { hasUI: true, ui: { confirm: async () => true } };
	const updates = [];
	const startResult = await workflowTool.execute(
		"tool-call-1",
		{ script: SCRIPTS["extension-run"], background: true },
		new AbortController().signal,
		(update) => updates.push(update),
		toolCtx,
	);
	const runId = startResult.details.runId;

	// Result delivery: wait until the completed run's message lands (the
	// extractor slept 200ms; the bridged child session is slower, the flush
	// point is identical).
	await waitFor(() => generationOne.sentMessages.length > 0, 30000, 20);

	const controlList = await controlTool.execute("tool-call-2", { action: "list" }, new AbortController().signal, () => {}, {});
	const controlStatus = await controlTool.execute(
		"tool-call-3",
		{ action: "status", runId },
		new AbortController().signal,
		() => {},
		{},
	);

	// The task panel mounts through ui.setWidget; idle render only (live-state
	// frames are reference-only, D35).
	const widget = widgets.get("workflow-tasks");
	const tuiStub = { requestRender() {}, terminal: { columns: 100, rows: 30 } };
	let widgetMount = null;
	if (widget) {
		const component = widget.factory(tuiStub, theme);
		widgetMount = {
			placement: widget.options?.placement ?? null,
			idleFrame: component.render(100),
		};
		component.dispose?.();
	}

	const renderCall = workflowTool.renderCall?.({}, theme)?.render(60) ?? null;
	const renderResult = workflowTool.renderResult?.(startResult, { expanded: false, isPartial: false }, theme)?.render(80) ?? null;

	// /new handoff.
	await emitExtensionEvent(generationOne, "session_shutdown", { type: "session_shutdown", reason: "new" }, undefined);
	const nextGeneration = () => {
		process.chdir(ctx.config.project);
		try {
			const generation = createCaptureExtension();
			ctx.plugin.extensionFactory(generation.api);
			return generation;
		} finally {
			process.chdir(originalCwd);
		}
	};
	const generationTwo = nextGeneration();
	await emitExtensionEvent(generationTwo, "session_start", { type: "session_start", reason: "new" }, makeCtx("session-b"));
	const controlAfterNew = await generationTwo.tools
		.get("workflow_control")
		.execute("tool-call-4", { action: "status", runId }, new AbortController().signal, () => {}, {});

	// /reload handoff.
	await emitExtensionEvent(generationTwo, "session_shutdown", { type: "session_shutdown", reason: "reload" }, undefined);
	const generationThree = nextGeneration();
	await emitExtensionEvent(generationThree, "session_start", { type: "session_start", reason: "reload" }, makeCtx("session-b"));

	// quit with a live run: pause onto the persisted journal.
	const hangingStart = await generationThree.tools
		.get("workflow")
		.execute(
			"tool-call-5",
			{ script: SCRIPTS["extension-hang"], background: true },
			new AbortController().signal,
			() => {},
			toolCtx,
		);
	const hangingRunId = hangingStart.details.runId;
	await waitForSignal(ctx.config.signalDir, "ext-hang");
	await emitExtensionEvent(generationThree, "session_shutdown", { type: "session_shutdown", reason: "quit" }, undefined);
	await waitFor(async () => {
		const artifacts = await readRunArtifacts(ctx.config);
		return Object.entries(artifacts).some(
			([key, value]) => key.includes(hangingRunId) && value.status === "paused",
		);
	}, 10000, 25);
	await sleep(100);
	const artifactsAfterQuit = await readRunArtifacts(ctx.config);
	const hangingAfterQuit = Object.entries(artifactsAfterQuit)
		.filter(([key]) => key.includes(hangingRunId))
		.map(([key, value]) => ({
			path: key,
			status: value.status ?? null,
			agents: (value.agents ?? []).map((agent) => ({ label: agent.label, status: agent.status })),
		}));

	return {
		registration: {
			log: generationOne.registrationLog,
			tools: [...generationOne.tools.values()].map(projectToolDefinition),
			commands: [...generationOne.commands.keys()].sort(),
			events: [...generationOne.handlers.keys()].sort(),
		},
		sessionStart: {
			notifications,
			activeToolsAfterStart: generationOne.activeTools,
			widgets: [...widgets.keys()],
			widgetMount,
		},
		lazyPromptGuidelines: {
			stableAcrossReads: JSON.stringify(guidelinesFirst) === JSON.stringify(guidelinesSecond),
			value: guidelinesFirst,
		},
		prepareArguments: prepareCases,
		backgroundRun: {
			startResult,
			updates,
			deliveredMessages: generationOne.sentMessages,
			controlList,
			controlStatus,
		},
		renderFrames: { renderCall, renderResult },
		handoff: {
			generationTwoRegistrationLog: generationTwo.registrationLog,
			controlAfterNew,
			generationTwoDelivered: generationTwo.sentMessages,
			generationThreeCommands: [...generationThree.commands.keys()].sort(),
			quitWithLiveRun: hangingAfterQuit,
		},
		providerCalls: "__F13_PROVIDER_CALLS__",
		pendingFauxResponses: "__F13_PENDING__",
	};
}

// ---------------------------------------------------------------------------
// /workflows-models select flow
// ---------------------------------------------------------------------------

async function scenarioWorkflowsModels(ctx) {
	const world = await createWorld(ctx.config);
	const { editSingleTier } = ctx.plugin.workflowsModelsCommand;
	const frames = [];
	const notifications = [];
	const selects = [];
	const tuiStub = { requestRender() {}, terminal: { columns: 72, rows: 24 } };
	const commandCtx = {
		model: world.model(),
		modelRegistry: world.registry,
		ui: {
			notify: (message, level) => notifications.push({ message, level: level ?? null }),
			select: async (title, options) => {
				selects.push({ title, options });
				return "high";
			},
			custom: async (factory) => {
				let resolved;
				let settled = false;
				const done = (value) => {
					resolved = value;
					settled = true;
				};
				const component = factory(tuiStub, themeShim, undefined, done);
				frames.push({ input: null, lines: component.render(72) });
				for (const input of ["\x1b[B", "\r"]) {
					if (settled) break;
					component.handleInput?.(input);
					frames.push({ input: JSON.stringify(input), lines: component.render(72) });
				}
				return resolved;
			},
		},
		waitForIdle: async () => {},
	};
	const tiers = { small: "faux/faux-mini", medium: "faux/faux-model", big: "faux/faux-model" };
	const updated = await editSingleTier(commandCtx, tiers, "medium");
	return {
		updatedTiers: updated,
		thinkingSelects: selects,
		notifications,
	};
}

// ---------------------------------------------------------------------------
// export surface + unsupported-export probes
// ---------------------------------------------------------------------------

async function scenarioExportSurface() {
	const [codingAgent, aiModule, tui] = await Promise.all([
		import("@earendil-works/pi-coding-agent"),
		import("@earendil-works/pi-ai"),
		import("@earendil-works/pi-tui"),
	]);
	const surface = (namespace) => Object.keys(namespace).sort();
	const probe = (namespace, name) => ({
		export: name,
		present: name in namespace,
		typeof: typeof namespace[name],
	});
	return {
		note:
			"Upstream serves every export below; orb-extension-sdk must expose the same names, implementing the supported set and throwing OrbUnsupportedCapability from the rest. The Orb-side diagnostic is asserted by a Go test, not this golden.",
		exports: {
			"@earendil-works/pi-coding-agent": surface(codingAgent),
			"@earendil-works/pi-ai": surface(aiModule),
			"@earendil-works/pi-tui": surface(tui),
		},
		unsupportedProbes: [
			probe(codingAgent, "main"),
			probe(codingAgent, "DefaultPackageManager"),
			probe(codingAgent, "copyToClipboard"),
			probe(aiModule, "retryAssistantCall"),
			probe(aiModule, "createProvider"),
			probe(tui, "Editor"),
			probe(tui, "VStack"),
		],
		supportedProbes: [
			probe(codingAgent, "createAgentSession"),
			probe(codingAgent, "defineTool"),
			probe(codingAgent, "parseFrontmatter"),
			probe(aiModule, "modelsAreEqual"),
			probe(tui, "SelectList"),
			probe(tui, "truncateToWidth"),
			probe(tui, "Container"),
			probe(tui, "Markdown"),
		],
	};
}

// ---------------------------------------------------------------------------
// dispatch
// ---------------------------------------------------------------------------

async function runScenario(config) {
	if (config.scenario === "export-surface") {
		return await scenarioExportSurface();
	}
	const plugin = await loadPlugin(config.pluginRoot);
	const ctx = { config, plugin };
	return await withPinnedClock(config.fixedNow, async () => {
		switch (config.scenario) {
			case "foreground-basic":
			case "structured-output":
			case "store-tools":
			case "agent-types":
				return await runWorkflowScenario(ctx, config.scenario, {});
			case "web-toolset":
				return await scenarioWebToolset(ctx);
			case "nested-workflow":
				return await runWorkflowScenario(ctx, "nested-workflow", {
					loadSavedWorkflow: (name) => (name === "child-flow" ? SCRIPTS["nested-child"] : undefined),
				});
			case "cancellation":
				return await scenarioCancellation(ctx);
			case "model-routing":
				return await scenarioModelRouting(ctx);
			case "background-lifecycle":
				return await scenarioBackgroundLifecycle(ctx);
			case "persist-agent-sessions":
				return await scenarioPersistAgentSessions(ctx);
			case "extension-lifecycle":
				return await scenarioExtensionLifecycle(ctx);
			case "workflows-models":
				return await scenarioWorkflowsModels(ctx);
			default:
				throw new Error(`unknown scenario ${config.scenario}`);
		}
	});
}

export default function activate(pi) {
	pi.registerTool({
		name: "f13_scenario",
		description: "Replay one F13-dynamic-workflows scenario through the orb-extension-sdk",
		parameters: {
			type: "object",
			required: ["scenario"],
			properties: {
				scenario: { type: "string" },
				fixedNow: { type: "number" },
				pluginRoot: { type: "string" },
				project: { type: "string" },
				home: { type: "string" },
				agentDir: { type: "string" },
				catalogDir: { type: "string" },
				signalDir: { type: "string" },
			},
		},
		async execute(_id, params) {
			let envelope;
			try {
				const value = await runScenario(params);
				envelope = {
					ok: true,
					value: { schemaVersion: 1, scenario: params.scenario, ...value },
				};
			} catch (error) {
				envelope = { ok: false, error: error?.stack ?? String(error) };
			}
			return { content: [{ type: "text", text: JSON.stringify(envelope) }] };
		},
	});
}
