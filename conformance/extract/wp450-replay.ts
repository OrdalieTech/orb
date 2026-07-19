import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withUpstreamModelData } from "./upstream-model-data.ts";

type Component = { render(width: number): string[]; invalidate?(): void; dispose?(): void };
type ExtensionHandler = (event: unknown, context: any) => unknown;
type ExtensionFactory = (api: any) => unknown;

type DemoState = {
	statuses: Map<string, string>;
	widgets: Map<string, { lines: string[]; placement: "aboveEditor" | "belowEditor" }>;
	headerFactory?: (ui: any, theme: any) => Component;
	footerFactory?: (ui: any, theme: any, footerData: any) => Component;
	notifications: string[];
};

type ToolOutputPreviewCase = {
	id: string;
	width: number;
	lines: string[];
};

type ExtensionHost = {
	api: any;
	emit(event: string, context: any): Promise<void>;
	command(name: string): { handler: (args: string, context: any) => unknown };
};

const WIDTHS = [52, 88];
const ROWS = 120;
const FIXED_TIMESTAMP = 1_700_000_000_000;

function longToolOutput(): string {
	return Array.from(
		{ length: 24 },
		(_, index) => `stream line ${String(index + 1).padStart(2, "0")}: abcdefghijklmnopqrstuvwxyz 0123456789`,
	).join("\n");
}

function extensionHost(): ExtensionHost {
	const handlers = new Map<string, ExtensionHandler[]>();
	const commands = new Map<string, { handler: (args: string, context: any) => unknown }>();
	return {
		api: {
			on(event: string, handler: ExtensionHandler) {
				const registered = handlers.get(event) ?? [];
				registered.push(handler);
				handlers.set(event, registered);
			},
			registerCommand(name: string, command: { handler: (args: string, context: any) => unknown }) {
				commands.set(name, command);
			},
		},
		async emit(event: string, context: any) {
			for (const handler of handlers.get(event) ?? []) {
				await handler({ type: event }, context);
			}
		},
		command(name: string) {
			const command = commands.get(name);
			if (!command) throw new Error(`extension command ${name} was not registered`);
			return command;
		},
	};
}

function stripTerminalControls(value: string): string {
	return value
		.replace(/\u001b\][^\u0007]*(?:\u0007|\u001b\\)/g, "")
		.replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, "")
		.replace(/\r/g, "");
}

function normalizeLines(lines: string[], fixtureRoot?: string): string[] {
	const normalized = lines.map((line) => {
		let result = stripTerminalControls(line);
		if (fixtureRoot) {
			result = result
				.replaceAll(fixtureRoot, "<fixture>")
				.replaceAll(fixtureRoot.replaceAll("\\", "/"), "<fixture>");
		}
		return result.replace(/\s+$/g, "");
	});
	while (normalized.length > 0 && normalized[normalized.length - 1] === "") normalized.pop();
	return normalized;
}

function normalizeStatus(value: string | undefined): string | undefined {
	return value === undefined ? undefined : stripTerminalControls(value);
}

async function waitForEditPreview(component: Component, width: number): Promise<void> {
	for (let attempt = 0; attempt < 60; attempt++) {
		const rendered = normalizeLines(component.render(width)).join("\n");
		if (rendered.includes("old value") && rendered.includes("new value")) return;
		await new Promise<void>((resolve) => setTimeout(resolve, 5));
	}
	throw new Error("edit preview did not settle deterministically");
}

async function loadModules(upstreamRoot: string) {
	const load = async (relativePath: string) => import(pathToFileURL(path.join(upstreamRoot, relativePath)).href);
	process.env.PI_PACKAGE_DIR = path.join(upstreamRoot, "packages/coding-agent");
	process.env.FORCE_COLOR = "3";
	const chalk = await load("node_modules/chalk/source/index.js");
	(chalk.default as { level: number }).level = 3;

	const tui = await load("packages/tui/src/index.ts");
	const virtualTerminal = await load("packages/tui/test/virtual-terminal.ts");
	const theme = await load("packages/coding-agent/src/modes/interactive/theme/theme.ts");
	const keybindings = await load("packages/coding-agent/src/core/keybindings.ts");
	const customEditor = await load("packages/coding-agent/src/modes/interactive/components/custom-editor.ts");
	const userMessage = await load("packages/coding-agent/src/modes/interactive/components/user-message.ts");
	const assistantMessage = await load("packages/coding-agent/src/modes/interactive/components/assistant-message.ts");
	const toolExecution = await load("packages/coding-agent/src/modes/interactive/components/tool-execution.ts");
	const bashExecution = await load("packages/coding-agent/src/modes/interactive/components/bash-execution.ts");
	const compaction = await load("packages/coding-agent/src/modes/interactive/components/compaction-summary-message.ts");
	const branch = await load("packages/coding-agent/src/modes/interactive/components/branch-summary-message.ts");
	const customMessage = await load("packages/coding-agent/src/modes/interactive/components/custom-message.ts");
	const footer = await load("packages/coding-agent/src/modes/interactive/components/footer.ts");
	const status = await load("packages/coding-agent/src/modes/interactive/components/status-indicator.ts");
	const editDiff = await load("packages/coding-agent/src/core/tools/edit-diff.ts");
	const statusLineExtension = await load("packages/coding-agent/examples/extensions/status-line.ts");
	const widgetPlacementExtension = await load("packages/coding-agent/examples/extensions/widget-placement.ts");
	const customHeaderExtension = await load("packages/coding-agent/examples/extensions/custom-header.ts");
	const customFooterExtension = await load("packages/coding-agent/examples/extensions/custom-footer.ts");

	tui.setCapabilities({ images: null, trueColor: true, hyperlinks: false });
	theme.initTheme("dark");
	const bindings = keybindings.KeybindingsManager.create();
	tui.setKeybindings(bindings);

	return {
		tui,
		VirtualTerminal: virtualTerminal.VirtualTerminal,
		theme,
		bindings,
		CustomEditor: customEditor.CustomEditor,
		UserMessageComponent: userMessage.UserMessageComponent,
		AssistantMessageComponent: assistantMessage.AssistantMessageComponent,
		ToolExecutionComponent: toolExecution.ToolExecutionComponent,
		BashExecutionComponent: bashExecution.BashExecutionComponent,
		CompactionSummaryMessageComponent: compaction.CompactionSummaryMessageComponent,
		BranchSummaryMessageComponent: branch.BranchSummaryMessageComponent,
		CustomMessageComponent: customMessage.CustomMessageComponent,
		FooterComponent: footer.FooterComponent,
		IdleStatus: status.IdleStatus,
		generateDiffString: editDiff.generateDiffString,
		extensions: {
			statusLine: statusLineExtension.default as ExtensionFactory,
			widgetPlacement: widgetPlacementExtension.default as ExtensionFactory,
			customHeader: customHeaderExtension.default as ExtensionFactory,
			customFooter: customFooterExtension.default as ExtensionFactory,
		},
	};
}

function demoContext(modules: Awaited<ReturnType<typeof loadModules>>, state: DemoState) {
	const assistant = {
		role: "assistant",
		content: [{ type: "text", text: "done" }],
		usage: {
			input: 1200,
			output: 34,
			cacheRead: 200,
			cacheWrite: 0,
			totalTokens: 1434,
			cost: { input: 0.001, output: 0.002, cacheRead: 0, cacheWrite: 0, total: 0.003 },
		},
	};
	return {
		mode: "tui",
		hasUI: true,
		model: { id: "fixture-model" },
		sessionManager: { getBranch: () => [{ type: "message", message: assistant }] },
		ui: {
			theme: modules.theme.theme,
			setStatus(key: string, text: string | undefined) {
				if (text === undefined) state.statuses.delete(key);
				else state.statuses.set(key, text);
			},
			setWidget(key: string, lines: string[] | undefined, options?: { placement?: "aboveEditor" | "belowEditor" }) {
				if (lines === undefined) state.widgets.delete(key);
				else state.widgets.set(key, { lines: [...lines], placement: options?.placement ?? "aboveEditor" });
			},
			setHeader(factory: DemoState["headerFactory"]) { state.headerFactory = factory; },
			setFooter(factory: DemoState["footerFactory"]) { state.footerFactory = factory; },
			notify(message: string) { state.notifications.push(message); },
		},
	};
}

async function registerDemoExtensions(modules: Awaited<ReturnType<typeof loadModules>>, includeFooter: boolean) {
	const host = extensionHost();
	modules.extensions.statusLine(host.api);
	modules.extensions.widgetPlacement(host.api);
	modules.extensions.customHeader(host.api);
	if (includeFooter) modules.extensions.customFooter(host.api);
	const state: DemoState = { statuses: new Map(), widgets: new Map(), notifications: [] };
	const context = demoContext(modules, state);
	await host.emit("session_start", context);
	return { host, state, context };
}

async function generateUIDemoArtifact(modules: Awaited<ReturnType<typeof loadModules>>) {
	const { host, state, context } = await registerDemoExtensions(modules, true);
	const statusEvents = [{
		event: "session_start",
		value: normalizeStatus(state.statuses.get("status-demo")),
	}];
	await host.command("footer").handler("", context);
	await host.emit("turn_start", context);
	statusEvents.push({ event: "turn_start", value: normalizeStatus(state.statuses.get("status-demo")) });
	await host.emit("turn_end", context);
	statusEvents.push({ event: "turn_end", value: normalizeStatus(state.statuses.get("status-demo")) });

	if (!state.headerFactory || !state.footerFactory) throw new Error("header/footer demo factory was not retained");
	const terminal = new modules.VirtualTerminal(72, 30);
	const ui = new modules.tui.TUI(terminal);
	const footerData = {
		getGitBranch: () => "main",
		getExtensionStatuses: () => state.statuses,
		onBranchChange: () => () => {},
	};
	const header = state.headerFactory(ui, modules.theme.theme);
	const footer = state.footerFactory(ui, modules.theme.theme, footerData);
	const widgets = [...state.widgets.entries()].map(([key, widget]) => ({
		key,
		placement: widget.placement,
		lines: widget.lines,
	}));

	return {
		schemaVersion: 1,
		statusLine: { events: statusEvents },
		widgetPlacement: {
			above: widgets.filter((widget) => widget.placement === "aboveEditor"),
			below: widgets.filter((widget) => widget.placement === "belowEditor"),
		},
		headerFooterInitialization: {
			width: 72,
			header: normalizeLines(header.render(72)),
			footer: normalizeLines(footer.render(72)),
			retained: {
				statusKeys: [...state.statuses.keys()].sort(),
				widgetKeys: [...state.widgets.keys()].sort(),
				header: state.headerFactory !== undefined,
				footer: state.footerFactory !== undefined,
			},
		},
	};
}

function renderToolOutputPreviewCases(modules: Awaited<ReturnType<typeof loadModules>>, width: number) {
	const terminal = new modules.VirtualTerminal(width, ROWS);
	const ui = new modules.tui.TUI(terminal);
	const output = longToolOutput();
	const cases: ToolOutputPreviewCase[] = [];
	const capture = (id: string, component: Component) => {
		cases.push({
			id,
			width,
			lines: normalizeLines(component.render(width)).filter((line) => !line.includes("Running... (")),
		});
	};

	const tool = new modules.ToolExecutionComponent(
		"bash",
		"call-streaming-bash",
		{ command: "printf streaming-output" },
		{ showImages: false, imageWidthCells: 24 },
		undefined,
		ui,
		"/workspace",
	);
	tool.updateResult({ content: [{ type: "text", text: output }], isError: false }, true);
	capture("tool-partial-collapsed", tool);
	tool.setExpanded(true);
	capture("tool-partial-expanded", tool);
	tool.updateResult({ content: [{ type: "text", text: output }], isError: false }, false);
	capture("tool-final-expanded", tool);
	tool.setExpanded(false);
	capture("tool-final-collapsed", tool);

	const bash = new modules.BashExecutionComponent("printf streaming-output", ui, false);
	bash.appendOutput(output);
	capture("bang-bash-partial-collapsed", bash);
	bash.setExpanded(true);
	capture("bang-bash-partial-expanded", bash);
	bash.setComplete(0, false);
	capture("bang-bash-final-expanded", bash);
	bash.setExpanded(false);
	capture("bang-bash-final-collapsed", bash);

	return cases;
}

async function replayWidth(modules: Awaited<ReturnType<typeof loadModules>>, width: number) {
	const fixtureRoot = await mkdtemp(path.join(tmpdir(), "pi-wp450-replay-"));
	await writeFile(path.join(fixtureRoot, "fixture.txt"), "alpha\nold value\nomega\n");
	const terminal = new modules.VirtualTerminal(width, ROWS);
	const ui = new modules.tui.TUI(terminal);
	const { host, state, context } = await registerDemoExtensions(modules, false);

	const header = new modules.tui.Container();
	const chat = new modules.tui.Container();
	const pending = new modules.tui.Container();
	const status = new modules.tui.Container();
	const widgetAbove = new modules.tui.Container();
	const editorContainer = new modules.tui.Container();
	const widgetBelow = new modules.tui.Container();
	if (!state.headerFactory) throw new Error("custom-header session initialization did not set a factory");
	header.addChild(state.headerFactory(ui, modules.theme.theme));
	status.addChild(new modules.IdleStatus());
	widgetAbove.addChild(new modules.tui.Spacer(1));
	for (const widget of state.widgets.values()) {
		const target = widget.placement === "belowEditor" ? widgetBelow : widgetAbove;
		for (const line of widget.lines) target.addChild(new modules.tui.Text(line, 1, 0));
	}
	const editor = new modules.CustomEditor(ui, modules.theme.getEditorTheme(), modules.bindings, { paddingX: 1 });
	editorContainer.addChild(editor);
	const footerData = {
		getGitBranch: () => "main",
		getExtensionStatuses: () => state.statuses,
		getAvailableProviderCount: () => 1,
	};
	const footerSession = {
		state: {
			model: { id: "fixture-model", provider: "fixture", contextWindow: 8192, reasoning: true },
			thinkingLevel: "medium",
		},
		sessionManager: {
			getEntries: () => [],
			getCwd: () => "/workspace",
			getSessionName: () => "fixture-session",
		},
		getContextUsage: () => ({ tokens: 1024, contextWindow: 8192, percent: 12.5 }),
		modelRuntime: { isUsingOAuth: () => false },
	};
	const footer = new modules.FooterComponent(footerSession, footerData);

	for (const component of [header, chat, pending, status, widgetAbove, editorContainer, widgetBelow, footer]) {
		ui.addChild(component);
	}
	ui.setFocus(editor);
	ui.start();
	const frames: { id: string; width: number; lines: string[] }[] = [];
	const capture = async (id: string) => {
		ui.requestRender(true);
		await terminal.waitForRender();
		frames.push({ id, width, lines: normalizeLines(terminal.getViewport(), fixtureRoot) });
	};

	try {
		await capture("session-initialized");
		await host.emit("turn_start", context);
		chat.addChild(new modules.UserMessageComponent("Please update `fixture.txt` and explain the change."));
		await capture("user-message");

		const assistantMessage = {
			role: "assistant",
			content: [
				{ type: "thinking", thinking: "I should inspect the target and make one precise replacement." },
				{ type: "text", text: "I found the requested line and will update it now." },
			],
			api: "anthropic-messages",
			provider: "anthropic",
			model: "fixture-model",
			usage: {
				input: 12,
				output: 8,
				cacheRead: 0,
				cacheWrite: 0,
				totalTokens: 20,
				cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
			},
			stopReason: "stop",
			timestamp: FIXED_TIMESTAMP,
		};
		chat.addChild(new modules.AssistantMessageComponent(assistantMessage));
		await capture("assistant-thinking-text");

		const edit = new modules.ToolExecutionComponent(
			"edit",
			"call-edit",
			{ path: "fixture.txt" },
			{ showImages: false, imageWidthCells: 24 },
			undefined,
			ui,
			fixtureRoot,
		);
		chat.addChild(edit);
		await capture("tool-call");
		const editArgs = { path: "fixture.txt", edits: [{ oldText: "old value", newText: "new value" }] };
		edit.updateArgs(editArgs);
		edit.setArgsComplete();
		edit.markExecutionStarted();
		await waitForEditPreview(edit, width);
		await capture("tool-update-diff");
		const diff = modules.generateDiffString("alpha\nold value\nomega\n", "alpha\nnew value\nomega\n");
		edit.updateResult({
			content: [{ type: "text", text: "Successfully replaced text in fixture.txt" }],
			details: { diff: diff.diff, patch: "", firstChangedLine: diff.firstChangedLine },
			isError: false,
		});
		await capture("tool-result-diff");

		const bash = new modules.BashExecutionComponent("printf 'alpha\\nbeta\\n'", ui, false);
		bash.appendOutput("alpha\nbeta\n");
		bash.setComplete(0, false);
		chat.addChild(bash);
		await capture("bash-complete");

		const compacted = new modules.CompactionSummaryMessageComponent({
			role: "compactionSummary",
			summary: "Earlier work inspected the file and preserved the surrounding lines.",
			tokensBefore: 12_345,
			timestamp: FIXED_TIMESTAMP,
		});
		compacted.setExpanded(true);
		chat.addChild(compacted);
		chat.addChild(new modules.CustomMessageComponent({
			role: "custom",
			customType: "fixture-note",
			content: "Custom boundary retained after compaction.",
			display: true,
			timestamp: FIXED_TIMESTAMP,
		}));
		const branch = new modules.BranchSummaryMessageComponent({
			role: "branchSummary",
			summary: "The alternate branch changed the same fixture line.",
			fromId: "entry-fixed",
			timestamp: FIXED_TIMESTAMP,
		});
		branch.setExpanded(true);
		chat.addChild(branch);
		await capture("session-boundaries");

		pending.addChild(new modules.tui.Spacer(1));
		pending.addChild(new modules.tui.TruncatedText(modules.theme.theme.fg("dim", "Steering: verify the diff"), 1, 0));
		pending.addChild(new modules.tui.TruncatedText(modules.theme.theme.fg("dim", "Follow-up: summarize the result"), 1, 0));
		pending.addChild(new modules.tui.TruncatedText(modules.theme.theme.fg("dim", "↳ alt+up to edit all queued messages"), 1, 0));
		await capture("queue-pending");

		await host.emit("turn_end", context);
		editor.setText("/name replay-界");
		await capture("editor-ready");
		return frames;
	} finally {
		ui.stop();
		await rm(fixtureRoot, { recursive: true, force: true });
	}
}

export async function generateWP450Replay(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
	await withUpstreamModelData(upstreamRoot, async () => {
		const modules = await loadModules(upstreamRoot);
		try {
			const frames = [] as { id: string; width: number; lines: string[] }[];
			for (const width of WIDTHS) frames.push(...await replayWidth(modules, width));
			const toolOutputPreviews = [] as ToolOutputPreviewCase[];
			for (const width of WIDTHS) toolOutputPreviews.push(...renderToolOutputPreviewCases(modules, width));
			const demos = await generateUIDemoArtifact(modules);
			const familyDir = path.join(outputRoot, "WP450");
			await mkdir(familyDir, { recursive: true });
			await writeFile(path.join(familyDir, "replay.json"), `${JSON.stringify({ schemaVersion: 1, frames }, null, 2)}\n`);
			await writeFile(
				path.join(familyDir, "tool-output-previews.json"),
				`${JSON.stringify({ schemaVersion: 1, cases: toolOutputPreviews }, null, 2)}\n`,
			);
			await writeFile(path.join(familyDir, "ui-demos.json"), `${JSON.stringify(demos, null, 2)}\n`);
			await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({
				family: "WP450",
				upstreamCommit,
				generator: "conformance/extract/wp450-replay.ts",
				sources: [
					"packages/coding-agent/src/modes/interactive/interactive-mode.ts",
					"packages/coding-agent/src/modes/interactive/components/user-message.ts",
					"packages/coding-agent/src/modes/interactive/components/assistant-message.ts",
					"packages/coding-agent/src/modes/interactive/components/tool-execution.ts",
					"packages/coding-agent/src/modes/interactive/components/visual-truncate.ts",
					"packages/coding-agent/src/modes/interactive/components/diff.ts",
					"packages/coding-agent/src/modes/interactive/components/bash-execution.ts",
					"packages/coding-agent/src/core/tools/bash.ts",
					"packages/coding-agent/src/modes/interactive/components/compaction-summary-message.ts",
					"packages/coding-agent/src/modes/interactive/components/branch-summary-message.ts",
					"packages/coding-agent/src/modes/interactive/components/custom-message.ts",
					"packages/coding-agent/src/modes/interactive/components/footer.ts",
					"packages/coding-agent/examples/extensions/status-line.ts",
					"packages/coding-agent/examples/extensions/widget-placement.ts",
					"packages/coding-agent/examples/extensions/custom-header.ts",
					"packages/coding-agent/examples/extensions/custom-footer.ts",
					"packages/tui/test/virtual-terminal.ts",
				],
				files: ["replay.json", "tool-output-previews.json", "ui-demos.json"],
				metadata: {
					widths: WIDTHS,
					rows: ROWS,
					terminal: "@xterm/headless VirtualTerminal 5.5.0",
					fixedTimestamp: FIXED_TIMESTAMP,
					normalization: [
						"capture visible cells after the real differential TUI render settles",
						"strip trailing cell whitespace and trailing blank viewport rows",
						"replace native spellings of the temporary scenario root with <fixture>",
						"use completed loaders only so spinner clocks never enter the artifact",
					],
					divergences: [],
				},
			}, null, 2)}\n`);
		} finally {
			modules.tui.resetCapabilitiesCache();
		}
	});
}
