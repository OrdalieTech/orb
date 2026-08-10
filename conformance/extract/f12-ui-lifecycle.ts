import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

type Renderable = { render(width: number): string[] };

async function loadModules(upstreamRoot: string) {
	const load = async (relativePath: string) =>
		import(pathToFileURL(path.join(upstreamRoot, relativePath)).href);
	process.env.PI_PACKAGE_DIR = path.join(upstreamRoot, "packages/coding-agent");
	process.env.FORCE_COLOR = "3";
	const chalk = await load("node_modules/chalk/source/index.js");
	(chalk.default as { level: number }).level = 3;
	const tui = await load("packages/tui/src/index.ts");
	const theme = await load(
		"packages/coding-agent/src/modes/interactive/theme/theme.ts",
	);
	const interactive = await load(
		"packages/coding-agent/src/modes/interactive/interactive-mode.ts",
	);
	const assistant = await load(
		"packages/coding-agent/src/modes/interactive/components/assistant-message.ts",
	);
	const modelSelector = await load(
		"packages/coding-agent/src/modes/interactive/components/model-selector.ts",
	);
	const keybindings = await load(
		"packages/coding-agent/src/core/keybindings.ts",
	);
	theme.initTheme("dark");
	tui.setKeybindings(keybindings.KeybindingsManager.create());
	return {
		tui,
		theme,
		InteractiveMode: interactive.InteractiveMode,
		AssistantMessageComponent: assistant.AssistantMessageComponent,
		ModelSelectorComponent: modelSelector.ModelSelectorComponent,
	};
}

function prototypeMethod(
	modules: Awaited<ReturnType<typeof loadModules>>,
	name: string,
): (this: Record<string, unknown>, ...args: any[]) => any {
	return Reflect.get(modules.InteractiveMode.prototype, name) as (
		this: Record<string, unknown>,
		...args: any[]
	) => any;
}

function render(component: Renderable, width = 40): string[] {
	return component.render(width);
}

const ansiEscape = /\u001B\[[0-?]*[ -/]*[@-~]/g;

function renderPlain(component: Renderable, width = 40): string[] {
	return render(component, width).map((line) =>
		line.replace(ansiEscape, "").trimEnd(),
	);
}

function replayReset(modules: Awaited<ReturnType<typeof loadModules>>) {
	const events: string[] = [];
	const disposalOrder: string[] = [];
	const context: Record<string, any> = {
		extensionSelector: {},
		extensionInput: {},
		extensionEditor: {},
		autocompleteProviderWrappers: ["wrapper"],
		defaultEditor: { onExtensionShortcut: () => {} },
		workingMessage: "extension working",
		workingVisible: false,
		defaultWorkingMessage: "Working...",
		defaultHiddenThinkingLabel: "Thinking...",
		hiddenThinkingLabel: "Extension thinking",
		activeStatusIndicator: {
			kind: "working",
			setMessage(message: string) {
				events.push(`working-status:${message}`);
			},
		},
		ui: {
			hideOverlay() {
				events.push("overlay:hide-top");
			},
		},
		footerDataProvider: {
			clearExtensionStatuses() {
				events.push("statuses:clear");
			},
		},
		footer: {
			invalidate() {
				events.push("footer:invalidate");
			},
		},
		hideExtensionSelector() {
			events.push("selector:hide");
			context.extensionSelector = undefined;
		},
		hideExtensionInput() {
			events.push("input:hide");
			context.extensionInput = undefined;
		},
		hideExtensionEditor() {
			events.push("editor-dialog:hide");
			context.extensionEditor = undefined;
		},
		clearExtensionTerminalInputListeners() {
			events.push("terminal-listeners:clear");
		},
		setExtensionFooter(factory: unknown) {
			events.push(`footer:${factory === undefined ? "default" : "custom"}`);
			if (factory === undefined) disposalOrder.push("footer");
		},
		setExtensionHeader(factory: unknown) {
			events.push(`header:${factory === undefined ? "default" : "custom"}`);
			if (factory === undefined) disposalOrder.push("header");
		},
		clearExtensionWidgets() {
			events.push("widgets:clear");
			disposalOrder.push("widget-above", "widget-below");
		},
		setCustomEditorComponent(factory: unknown) {
			events.push(`editor:${factory === undefined ? "default" : "custom"}`);
		},
		setupAutocompleteProvider() {
			events.push("autocomplete:rebuild");
		},
		updateTerminalTitle() {
			events.push("title:default");
		},
		setWorkingIndicator(options: unknown) {
			events.push(`working-indicator:${options === undefined ? "default" : "custom"}`);
		},
		setHiddenThinkingLabel(label: unknown) {
			events.push(`thinking-label:${label === undefined ? "default" : "custom"}`);
			context.hiddenThinkingLabel = "Thinking...";
		},
	};

	prototypeMethod(modules, "resetExtensionUI").call(context);
	return {
		events,
		disposalOrder,
		final: {
			selectorPresent: context.extensionSelector !== undefined,
			inputPresent: context.extensionInput !== undefined,
			editorDialogPresent: context.extensionEditor !== undefined,
			autocompleteWrappers: context.autocompleteProviderWrappers.length,
			extensionShortcutPresent:
				typeof context.defaultEditor.onExtensionShortcut === "function",
			workingMessage: context.workingMessage ?? null,
			workingVisible: context.workingVisible,
			hiddenThinkingLabel: context.hiddenThinkingLabel,
		},
	};
}

class TraceComponent {
	expanded = false;

	constructor(
		private readonly label: string,
		private readonly events: string[],
	) {}

	render(): string[] {
		return [this.label];
	}

	dispose(): void {
		this.events.push(`dispose:${this.label}`);
	}

	setExpanded(expanded: boolean): void {
		this.expanded = expanded;
		this.events.push(`expand:${this.label}:${String(expanded)}`);
	}
}

class OverlayTerminal {
	columns = 80;
	rows = 24;
	kittyProtocolActive = false;

	start(): void {}
	stop(): void {}
	async drainInput(): Promise<void> {}
	write(): void {}
	moveBy(): void {}
	hideCursor(): void {}
	showCursor(): void {}
	clearLine(): void {}
	clearFromCursor(): void {}
	clearScreen(): void {}
	setTitle(): void {}
	setProgress(): void {}
}

class OverlayComponent {
	focused = false;
	inputs: string[] = [];
	renderWidths: number[] = [];
	disposeCount = 0;
	width?: number;

	constructor(
		private readonly label: string,
		width?: number,
		private readonly throwOnDispose = false,
	) {
		this.width = width;
	}

	render(width: number): string[] {
		this.renderWidths.push(width);
		return [this.label];
	}

	handleInput(data: string): void {
		this.inputs.push(data);
	}

	invalidate(): void {}

	dispose(): void {
		this.disposeCount++;
		if (this.throwOnDispose) throw new Error("dispose failed");
	}
}

class OverlayEditor extends OverlayComponent {
	private text = "draft";

	getText(): string {
		return this.text;
	}

	setText(text: string): void {
		this.text = text;
	}
}

async function flushMicrotasks(): Promise<void> {
	await Promise.resolve();
	await Promise.resolve();
}

function customContext(
	modules: Awaited<ReturnType<typeof loadModules>>,
	ui: Record<string, any>,
	editor = new OverlayEditor("editor"),
) {
	const editorContainer = new modules.tui.Container();
	editorContainer.addChild(editor);
	return {
		context: {
			editor,
			editorContainer,
			keybindings: {},
			// Upstream >=0.84 disposes any active selector before installing.
			disposeActiveSelector: () => {},
			ui,
		},
		editor,
		editorContainer,
	};
}

async function replayCustomOverlay(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const showCustom = prototypeMethod(modules, "showExtensionCustom");

	let dynamicCalls = 0;
	let dynamicDone: ((value: string) => void) | undefined;
	let dynamicOptions: Record<string, unknown> | undefined;
	const dynamicUI = {
		showOverlay(_component: unknown, options: Record<string, unknown>) {
			dynamicOptions = options;
			return {};
		},
		hideOverlay() {},
		setFocus() {},
		requestRender() {},
	};
	const dynamicContext = customContext(modules, dynamicUI).context;
	const dynamicPromise = showCustom.call(
		dynamicContext,
		(
			_ui: unknown,
			_theme: unknown,
			_keybindings: unknown,
			done: (value: string) => void,
		) => {
			dynamicDone = done;
			return new OverlayComponent("dynamic");
		},
		{
			overlay: true,
			overlayOptions: () => {
				dynamicCalls++;
				return {
					anchor: "top-right",
					width: "50%",
					margin: { top: 2, right: 3, bottom: 4, left: 5 },
					offsetX: -1,
					offsetY: 1,
				};
			},
		},
	);
	await flushMicrotasks();
	dynamicDone?.("dynamic-done");
	await dynamicPromise;

	let fallbackDone: ((value: string) => void) | undefined;
	let fallbackOptions: Record<string, unknown> | undefined;
	const fallbackUI = {
		showOverlay(_component: unknown, options: Record<string, unknown>) {
			fallbackOptions = options;
			return {};
		},
		hideOverlay() {},
		setFocus() {},
		requestRender() {},
	};
	const fallbackContext = customContext(modules, fallbackUI).context;
	const fallbackPromise = showCustom.call(
		fallbackContext,
		(
			_ui: unknown,
			_theme: unknown,
			_keybindings: unknown,
			done: (value: string) => void,
		) => {
			fallbackDone = done;
			return new OverlayComponent("fallback", 23);
		},
		{ overlay: true },
	);
	await flushMicrotasks();
	fallbackDone?.("fallback-done");
	await fallbackPromise;

	let earlyOverlayShows = 0;
	const earlyComponent = new OverlayComponent("early");
	const earlyUI = {
		showOverlay() {
			earlyOverlayShows++;
			return {};
		},
		hideOverlay() {},
		setFocus() {},
		requestRender() {},
	};
	const earlyContext = customContext(modules, earlyUI).context;
	const earlyResult = await showCustom.call(
		earlyContext,
		(
			_ui: unknown,
			_theme: unknown,
			_keybindings: unknown,
			done: (value: string) => void,
		) => {
			done("early-done");
			return earlyComponent;
		},
		{ overlay: true },
	);
	await flushMicrotasks();

	const throwingComponent = new OverlayComponent("throwing", undefined, true);
	let throwingDone: ((value: string) => void) | undefined;
	const throwingContext = customContext(modules, {
		showOverlay() {
			return {};
		},
		hideOverlay() {},
		setFocus() {},
		requestRender() {},
	}).context;
	const throwingPromise = showCustom.call(
		throwingContext,
		(
			_ui: unknown,
			_theme: unknown,
			_keybindings: unknown,
			done: (value: string) => void,
		) => {
			throwingDone = done;
			return throwingComponent;
		},
		{ overlay: true },
	);
	await flushMicrotasks();
	throwingDone?.("dispose-done");
	const throwingResult = await throwingPromise;

	let factoryFailure = "";
	const failureContext = customContext(modules, {
		showOverlay() {
			throw new Error("overlay must not be shown");
		},
		hideOverlay() {},
		setFocus() {},
		requestRender() {},
	}).context;
	try {
		await showCustom.call(
			failureContext,
			() => {
				throw new Error("factory failed");
			},
			{ overlay: true },
		);
	} catch (error) {
		factoryFailure = error instanceof Error ? error.message : String(error);
	}

	const terminal = new OverlayTerminal();
	const realUI = new (modules.tui.TUI ?? modules.tui.TuiMainScreen)(terminal);
	const real = customContext(modules, realUI);
	realUI.addChild(real.editorContainer);
	realUI.setFocus(real.editor);
	const overlay = new OverlayComponent("overlay");
	let overlayDone: ((value: string) => void) | undefined;
	let overlayHandle: any;
	const overlayPromise = showCustom.call(
		real.context,
		(
			_ui: unknown,
			_theme: unknown,
			_keybindings: unknown,
			done: (value: string) => void,
		) => {
			overlayDone = done;
			return overlay;
		},
		{ overlay: true, onHandle: (handle: unknown) => (overlayHandle = handle) },
	);
	await flushMicrotasks();
	const observe = () => ({
		hasOverlay: realUI.hasOverlay(),
		hidden: overlayHandle.isHidden(),
		handleFocused: overlayHandle.isFocused(),
		editorFocused: real.editor.focused,
		overlayFocused: overlay.focused,
	});
	const initial = observe();
	overlayHandle.unfocus();
	const unfocusedToPrevious = observe();
	overlayHandle.focus();
	overlayHandle.unfocus({ target: real.editor });
	const unfocusedToTarget = observe();
	overlayHandle.focus();
	overlayHandle.unfocus({ target: null });
	const unfocusedToNull = observe();
	overlayHandle.focus();
	overlayHandle.setHidden(true);
	const temporaryHidden = observe();
	overlayHandle.setHidden(false);
	const temporaryRestored = observe();
	overlayHandle.hide();
	const permanentlyHidden = observe();
	overlayHandle.setHidden(false);
	overlayHandle.focus();
	const afterPermanentShowAttempt = observe();
	overlayDone?.("overlay-done");
	const overlayResult = await overlayPromise;

	return {
		options: {
			dynamicCalls,
			dynamicOptions: dynamicOptions ?? null,
			fallbackOptions: fallbackOptions ?? null,
		},
		earlyDone: {
			result: earlyResult,
			overlayShows: earlyOverlayShows,
			disposeCount: earlyComponent.disposeCount,
		},
		disposeFailure: {
			result: throwingResult,
			disposeCount: throwingComponent.disposeCount,
			rejected: false,
		},
		factoryFailure,
		handle: {
			initial,
			unfocusedToPrevious,
			unfocusedToTarget,
			unfocusedToNull,
			temporaryHidden,
			temporaryRestored,
			permanentlyHidden,
			afterPermanentShowAttempt,
			result: overlayResult,
			disposeCount: overlay.disposeCount,
		},
	};
}

function replayWidgets(modules: Awaited<ReturnType<typeof loadModules>>) {
	const events: string[] = [];
	const setWidget = prototypeMethod(modules, "setExtensionWidget");
	const clearWidgets = prototypeMethod(modules, "clearExtensionWidgets");
	const context: Record<string, any> = {
		extensionWidgetsAbove: new Map(),
		extensionWidgetsBelow: new Map(),
		widgetContainerAbove: new modules.tui.Container(),
		widgetContainerBelow: new modules.tui.Container(),
		ui: {
			requestRender() {
				events.push("render");
			},
		},
	};
	context.renderWidgetContainer = prototypeMethod(
		modules,
		"renderWidgetContainer",
	);
	context.renderWidgets = prototypeMethod(modules, "renderWidgets");

	const snapshots: Array<{
		step: string;
		above: string[];
		below: string[];
	}> = [];
	const capture = (step: string) =>
		snapshots.push({
			step,
			above: render(context.widgetContainerAbove),
			below: render(context.widgetContainerBelow),
		});

	context.renderWidgets.call(context);
	capture("empty");
	setWidget.call(
		context,
		"capped",
		Array.from({ length: 11 }, (_, index) => `line-${index + 1}`),
	);
	capture("eleven-lines-capped");
	setWidget.call(
		context,
		"capped",
		() => {
			events.push("factory:replacement");
			return new TraceComponent("replacement", events);
		},
		{ placement: "belowEditor" },
	);
	capture("replacement-moved-below");
	setWidget.call(context, "outer", () => {
		events.push("factory:outer:start");
		setWidget.call(
			context,
			"nested",
			() => {
				events.push("factory:nested");
				return new TraceComponent("nested", events);
			},
			{ placement: "belowEditor" },
		);
		events.push("factory:outer:end");
		return new TraceComponent("outer", events);
	});
	capture("reentrant-factory");
	setWidget.call(context, "capped", undefined);
	capture("removed-by-key");
	clearWidgets.call(context);
	capture("cleared");

	return { snapshots, events };
}

function assistantMessage() {
	return {
		role: "assistant",
		content: [
			{ type: "thinking", thinking: "first secret" },
			{ type: "thinking", thinking: "second secret" },
			{ type: "text", text: "visible answer" },
		],
		api: "fixture",
		provider: "fixture",
		model: "fixture",
		usage: {
			input: 0,
			output: 0,
			cacheRead: 0,
			cacheWrite: 0,
			totalTokens: 0,
			cost: {
				input: 0,
				output: 0,
				cacheRead: 0,
				cacheWrite: 0,
				total: 0,
			},
		},
		stopReason: "stop",
		timestamp: 0,
	};
}

function replayHiddenThinking(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	let renders = 0;
	const historical = new modules.AssistantMessageComponent(
		assistantMessage(),
		true,
	);
	const streaming = new modules.AssistantMessageComponent(
		assistantMessage(),
		true,
	);
	const chatContainer = new modules.tui.Container();
	chatContainer.addChild(historical);
	const context: Record<string, any> = {
		hiddenThinkingLabel: "Thinking...",
		defaultHiddenThinkingLabel: "Thinking...",
		chatContainer,
		streamingComponent: streaming,
		ui: {
			requestRender() {
				renders++;
			},
		},
	};
	const setLabel = prototypeMethod(modules, "setHiddenThinkingLabel");
	setLabel.call(context, "Extension thought");
	const custom = {
		historical: render(historical, 48),
		streaming: render(streaming, 48),
		storedLabel: context.hiddenThinkingLabel,
	};
	setLabel.call(context, undefined);
	const reset = {
		historical: render(historical, 48),
		streaming: render(streaming, 48),
		storedLabel: context.hiddenThinkingLabel,
	};
	return { custom, reset, requestRenderCount: renders };
}

function replayToolsExpanded(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const events: string[] = [];
	const builtIn = new TraceComponent("built-in-header", events);
	const resource = new TraceComponent("resource", events);
	const chat = new TraceComponent("chat", events);
	const context: Record<string, any> = {
		toolOutputExpanded: false,
		customHeader: undefined,
		builtInHeader: builtIn,
		headerContainer: new modules.tui.Container(),
		loadedResourcesContainer: new modules.tui.Container(),
		chatContainer: new modules.tui.Container(),
		ui: {
			requestRender() {
				events.push("render");
			},
		},
		showStatus() {
			context.ui.requestRender();
		},
	};
	context.headerContainer.addChild(builtIn);
	context.loadedResourcesContainer.addChild(resource);
	context.chatContainer.addChild(chat);
	const setExpanded = prototypeMethod(modules, "setToolsExpanded");
	const setHeader = prototypeMethod(modules, "setExtensionHeader");

	setExpanded.call(context, true);
	setHeader.call(context, () => {
		events.push("factory:custom-header");
		return new TraceComponent("custom-header", events);
	});
	setExpanded.call(context, false);
	setHeader.call(context, undefined);

	return {
		events,
		final: {
			toolsExpanded: context.toolOutputExpanded,
			activeHeader: render(context.headerContainer)[0] ?? null,
			builtInExpanded: builtIn.expanded,
			resourceExpanded: resource.expanded,
			chatExpanded: chat.expanded,
		},
	};
}

function replayLoadedContext(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const cwd = "/fixture/project";
	const renderListing = (
		quietStartup: boolean,
		verbose: boolean,
		expanded: boolean,
	): string[] => {
		const context: Record<string, any> = {
			loadedResourcesContainer: new modules.tui.Container(),
			options: { verbose },
			toolOutputExpanded: expanded,
			settingsManager: {
				getQuietStartup: () => quietStartup,
			},
			sessionManager: {
				getCwd: () => cwd,
			},
			session: {
				promptTemplates: [],
				resourceLoader: {
					getSystemPromptSource: () => ({
						path: path.join(cwd, ".pi", "SYSTEM.md"),
					}),
					getAppendSystemPromptSources: () => [
						{ path: path.join(cwd, ".pi", "APPEND_SYSTEM.md") },
					],
					getAgentsFiles: () => ({
						agentsFiles: [{ path: path.join(cwd, "AGENTS.md") }],
					}),
					getSkills: () => ({ skills: [], diagnostics: [] }),
					getPrompts: () => ({ prompts: [], diagnostics: [] }),
					getThemes: () => ({ themes: [], diagnostics: [] }),
					getExtensions: () => ({ extensions: [], errors: [] }),
				},
				extensionRunner: {
					getCommandDiagnostics: () => [],
					getShortcutDiagnostics: () => [],
				},
			},
			getBuiltInCommandConflictDiagnostics: () => [],
		};
		for (const method of [
			"formatDisplayPath",
			"formatContextPath",
			"getStartupExpansionState",
		]) {
			context[method] = prototypeMethod(modules, method).bind(context);
		}
		prototypeMethod(modules, "showLoadedResources").call(context, {
			force: false,
		});
		return renderPlain(context.loadedResourcesContainer, 80);
	};

	return {
		collapsed: renderListing(false, false, false),
		expanded: renderListing(false, false, true),
		quiet: renderListing(true, false, false),
		verbose: renderListing(true, true, false),
	};
}

function replayModelSelector(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const models = [
		{
			provider: "fixture",
			id: "alpha-1",
			name: "Alpha One",
			reasoning: true,
		},
		{
			provider: "fixture",
			id: "alpha-2",
			name: "Alpha Two",
			reasoning: true,
		},
		{
			provider: "fixture",
			id: "alpha-3",
			name: "Alpha Three",
			reasoning: true,
		},
		{
			provider: "fixture",
			id: "beta-1",
			name: "Beta One",
			reasoning: true,
		},
	];
	const current = models[0];
	const tui = { requestRender() {} };
	const settingsManager = { setDefaultModelAndProvider() {} };
	const modelRuntime = {
		getAvailableSnapshot: () => models,
		getModel: (provider: string, id: string) =>
			models.find((model) => model.provider === provider && model.id === id),
		refresh: ({ signal }: { signal: AbortSignal }) =>
			new Promise((resolve) => {
				signal.addEventListener(
					"abort",
					() => resolve({ aborted: true, errors: new Map() }),
					{ once: true },
				);
			}),
		getError: () => undefined,
	};
	const selectedModelId = (selector: Renderable): string => {
		const line = renderPlain(selector, 120).find((value) =>
			value.startsWith("→ "),
		);
		if (!line) throw new Error("model selector did not render a selected row");
		return line.replace(/^→\s*/, "").split(" [")[0]?.trim() ?? "";
	};
	const createSelector = (scopedModels: Array<{ model: (typeof models)[number] }>) =>
		new modules.ModelSelectorComponent(
			tui,
			current,
			settingsManager,
			modelRuntime,
			scopedModels,
			() => {},
			() => {},
		);

	const all = createSelector([]);
	all.handleInput("\x1b[B");
	all.handleInput("\x1b[B");
	const allBeforeQuery = selectedModelId(all);
	for (const char of "alpha") all.handleInput(char);
	const allAfterQuery = selectedModelId(all);
	all.handleInput("\x1b[B");
	all.handleInput("\x1b[B");
	all.handleInput("\x15");
	const allAfterClear = selectedModelId(all);
	all.handleInput("\x1b");

	const scoped = createSelector([
		{ model: models[1] },
		{ model: models[2] },
		{ model: models[0] },
	]);
	const scopedBeforeQuery = selectedModelId(scoped);
	for (const char of "alpha") scoped.handleInput(char);
	const scopedAfterQuery = selectedModelId(scoped);
	scoped.handleInput("\x1b");

	return {
		all: {
			beforeQuery: allBeforeQuery,
			afterQuery: allAfterQuery,
			afterClear: allAfterClear,
		},
		scoped: {
			beforeQuery: scopedBeforeQuery,
			afterQuery: scopedAfterQuery,
		},
	};
}

export async function generateF12UILifecycle(
	upstreamRoot: string,
	outputRoot: string,
	upstreamCommit: string,
): Promise<void> {
	const modules = await loadModules(upstreamRoot);
	const familyDir = path.join(outputRoot, "F12-ui-lifecycle");
	await rm(familyDir, { recursive: true, force: true });
	await mkdir(familyDir, { recursive: true });
	const manifest = {
		family: "F12-ui-lifecycle",
		upstreamCommit,
		generator: "conformance/extract/f12-ui-lifecycle.ts",
		sources: [
			"packages/coding-agent/src/modes/interactive/interactive-mode.ts",
			"packages/coding-agent/src/modes/interactive/components/assistant-message.ts",
			"packages/coding-agent/src/modes/interactive/theme/theme.ts",
			"packages/coding-agent/src/core/extensions/types.ts",
			"packages/tui/src/{tui,container}.ts",
			"packages/coding-agent/test/interactive-mode-status.test.ts",
			"packages/tui/test/overlay-non-capturing.test.ts",
			"packages/tui/test/overlay-options.test.ts",
			"packages/coding-agent/src/modes/interactive/components/model-selector.ts",
			"packages/coding-agent/src/modes/interactive/model-search.ts",
			"packages/coding-agent/test/suite/regressions/7209-model-selector-filter-resets-selection.test.ts",
		],
		files: ["lifecycle.json"],
		metadata: {
			normalization: [
				"loadedContext strips ANSI color and trailing terminal padding",
				"modelSelector records selected model IDs from rendered rows",
			],
			divergences: [],
		},
	};
	const fixture = {
		schemaVersion: 4,
		reset: replayReset(modules),
		widgets: replayWidgets(modules),
		hiddenThinking: replayHiddenThinking(modules),
		toolsExpanded: replayToolsExpanded(modules),
		loadedContext: replayLoadedContext(modules),
		modelSelector: replayModelSelector(modules),
		customOverlay: await replayCustomOverlay(modules),
	};
	await writeFile(
		path.join(familyDir, "manifest.json"),
		`${JSON.stringify(manifest, null, 2)}\n`,
	);
	await writeFile(
		path.join(familyDir, "lifecycle.json"),
		`${JSON.stringify(fixture, null, 2)}\n`,
	);
}
