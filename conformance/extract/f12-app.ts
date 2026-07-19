import { appendFileSync } from "node:fs";
import {
	chmod,
	mkdir,
	mkdtemp,
	readFile,
	rm,
	writeFile,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

type Frame = {
	id: string;
	width: number;
	lines: string[];
};

type AutocompleteReplay = {
	input: string;
	result: {
		prefix: string;
		items: Array<{ value: string; label: string; description?: string }>;
	} | null;
};

type AutocompleteProviderTransfer = {
	defaultAssigned: boolean;
	replacementAssigned: boolean;
	sameProvider: boolean;
	storedProvider: boolean;
};

type CapturedInterval = {
	callback: () => void;
	delay: number;
	active: boolean;
};

const WIDTHS = [48, 88];

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
	const keybindings = await load(
		"packages/coding-agent/src/core/keybindings.ts",
	);
	const extensionSelector = await load(
		"packages/coding-agent/src/modes/interactive/components/extension-selector.ts",
	);
	const extensionInput = await load(
		"packages/coding-agent/src/modes/interactive/components/extension-input.ts",
	);
	const extensionEditor = await load(
		"packages/coding-agent/src/modes/interactive/components/extension-editor.ts",
	);
	const statusIndicator = await load(
		"packages/coding-agent/src/modes/interactive/components/status-indicator.ts",
	);
	const countdownTimer = await load(
		"packages/coding-agent/src/modes/interactive/components/countdown-timer.ts",
	);
	const dynamicBorder = await load(
		"packages/coding-agent/src/modes/interactive/components/dynamic-border.ts",
	);
	theme.initTheme("dark");
	const bindings = keybindings.KeybindingsManager.create();
	tui.setKeybindings(bindings);
	return {
		tui,
		theme,
		bindings,
		InteractiveMode: interactive.InteractiveMode,
		ExtensionSelectorComponent: extensionSelector.ExtensionSelectorComponent,
		ExtensionInputComponent: extensionInput.ExtensionInputComponent,
		ExtensionEditorComponent: extensionEditor.ExtensionEditorComponent,
		WorkingStatusIndicator: statusIndicator.WorkingStatusIndicator,
		CountdownTimer: countdownTimer.CountdownTimer,
		DynamicBorder: dynamicBorder.DynamicBorder,
	};
}

async function withCapturedIntervals<T>(
	run: (intervals: CapturedInterval[]) => T | Promise<T>,
): Promise<T> {
	const originalSetInterval = globalThis.setInterval;
	const originalClearInterval = globalThis.clearInterval;
	const intervals: CapturedInterval[] = [];
	globalThis.setInterval = ((callback: () => void, delay?: number) => {
		const interval = { callback, delay: Number(delay ?? 0), active: true };
		intervals.push(interval);
		return interval as unknown as ReturnType<typeof setInterval>;
	}) as typeof setInterval;
	globalThis.clearInterval = ((handle: ReturnType<typeof setInterval>) => {
		(handle as unknown as CapturedInterval).active = false;
	}) as typeof clearInterval;
	try {
		return await run(intervals);
	} finally {
		globalThis.setInterval = originalSetInterval;
		globalThis.clearInterval = originalClearInterval;
	}
}

function defineContextValues(
	context: Record<string, unknown>,
	values: Record<string, unknown>,
): void {
	Object.defineProperties(
		context,
		Object.fromEntries(
			Object.entries(values).map(([key, value]) => [
				key,
				{ configurable: true, writable: true, value },
			]),
		),
	);
}

function replayStatusFrames(
	modules: Awaited<ReturnType<typeof loadModules>>,
	width: number,
): Frame[] {
	const chatContainer = new modules.tui.Container();
	const context: Record<string, unknown> = {
		chatContainer,
		ui: { requestRender: () => {} },
		lastStatusSpacer: undefined,
		lastStatusText: undefined,
	};
	const showStatus = Reflect.get(
		modules.InteractiveMode.prototype,
		"showStatus",
	) as (this: Record<string, unknown>, message: string) => void;
	const frames: Frame[] = [];
	const capture = (id: string) => {
		frames.push({ id, width, lines: chatContainer.render(width) });
	};

	showStatus.call(context, "STATUS_ONE");
	capture("status-first");
	showStatus.call(context, "STATUS_TWO");
	capture("status-replaced");
	chatContainer.addChild(
		new modules.tui.Text(modules.theme.theme.fg("accent", "OTHER"), 1, 0),
	);
	showStatus.call(context, "STATUS_THREE");
	capture("status-after-content");
	showStatus.call(context, "STATUS_FOUR");
	capture("status-after-content-replaced");
	return frames;
}

function replayNotificationFrames(
	modules: Awaited<ReturnType<typeof loadModules>>,
	width: number,
): Frame[] {
	const chatContainer = new modules.tui.Container();
	const context: Record<string, unknown> = {
		chatContainer,
		ui: { requestRender: () => {} },
		lastStatusSpacer: undefined,
		lastStatusText: undefined,
	};
	for (const name of ["showStatus", "showWarning", "showError"]) {
		const handler = Reflect.get(modules.InteractiveMode.prototype, name) as (
			this: Record<string, unknown>,
			message: string,
		) => void;
		context[name] = handler.bind(context);
	}
	const notify = Reflect.get(
		modules.InteractiveMode.prototype,
		"showExtensionNotify",
	) as (
		this: Record<string, unknown>,
		message: string,
		type?: "info" | "warning" | "error",
	) => void;
	const frames: Frame[] = [];
	const capture = (id: string) =>
		frames.push({ id, width, lines: chatContainer.render(width) });

	notify.call(context, "NOTICE", "info");
	capture("notify-info");
	notify.call(context, "CAUTION", "warning");
	capture("notify-warning");
	notify.call(context, "BROKEN", "error");
	capture("notify-error");
	return frames;
}

function replayDialogFrames(
	modules: Awaited<ReturnType<typeof loadModules>>,
	width: number,
) {
	const frames: Frame[] = [];
	const capture = (
		id: string,
		component: { render(width: number): string[] },
	) => {
		frames.push({ id, width, lines: component.render(width) });
	};
	let selected: string | undefined;
	let selectorCancelled = false;
	const selector = new modules.ExtensionSelectorComponent(
		"Pick one",
		["alpha", "beta", "gamma"],
		(value: string) => {
			selected = value;
		},
		() => {
			selectorCancelled = true;
		},
	);
	capture("selector-initial", selector);
	selector.handleInput("\x1b[B");
	capture("selector-down", selector);
	selector.handleInput("\r");

	let inputValue: string | undefined;
	let inputCancelled = false;
	const input = new modules.ExtensionInputComponent(
		"Enter value",
		"PLACEHOLDER_IS_IGNORED",
		(value: string) => {
			inputValue = value;
		},
		() => {
			inputCancelled = true;
		},
	);
	capture("input-initial", input);
	input.handleInput("abc");
	capture("input-typed", input);
	input.handleInput("\r");

	const fakeTui = {
		terminal: { columns: width, rows: 40 },
		requestRender() {},
		stop() {},
		start() {},
	};
	const editor = new modules.ExtensionEditorComponent(
		fakeTui,
		modules.bindings,
		"Edit value",
		"alpha\nbeta",
		() => {},
		() => {},
		undefined,
		"false",
	);
	capture("editor-prefill", editor);

	return {
		frames,
		result: {
			selected,
			selectorCancelled,
			inputValue,
			inputCancelled,
			placeholderVisible: false,
		},
	};
}

async function replayExternalEditor(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const tempDir = await mkdtemp(
		path.join(os.tmpdir(), "pi-f12-external-editor-"),
	);
	const lifecyclePath = path.join(tempDir, "lifecycle.txt");
	const editorPath = path.join(tempDir, "editor.cjs");
	await writeFile(
		editorPath,
		[
			"#!/usr/bin/env node",
			'const fs = require("node:fs");',
			'fs.appendFileSync(process.argv[2], "edit\\n");',
			'fs.writeFileSync(process.argv[3], "edited externally\\n", "utf8");',
			"",
		].join("\n"),
	);
	await chmod(editorPath, 0o700);

	let component: InstanceType<typeof modules.ExtensionEditorComponent>;
	const record = (event: string) => appendFileSync(lifecyclePath, `${event}\n`);
	const fakeTui = {
		terminal: { columns: 88, rows: 40 },
		stop() {
			record("stop");
		},
		start() {
			const editor = Reflect.get(component, "editor") as { getText(): string };
			record(`start:${editor.getText()}`);
		},
		requestRender(force?: boolean) {
			record(`render:${String(force)}`);
		},
	};

	try {
		component = new modules.ExtensionEditorComponent(
			fakeTui,
			modules.bindings,
			"Edit value",
			"before",
			() => {},
			() => {},
			undefined,
			`${process.execPath} ${editorPath} ${lifecyclePath}`,
		);
		const editor = Reflect.get(component, "editor") as { getText(): string };
		const initialText = editor.getText();
		const hintVisible = component
			.render(88)
			.join("\n")
			.includes("external editor");
		component.handleInput("\x07");

		const deadline = Date.now() + 5000;
		let lifecycle = "";
		while (!lifecycle.includes("render:true")) {
			if (Date.now() >= deadline)
				throw new Error("external editor transcript timed out");
			await new Promise((resolve) => setTimeout(resolve, 5));
			lifecycle = await readFile(lifecyclePath, "utf8").catch(() => "");
		}
		return {
			keyData: "\x07",
			hintVisible,
			initialText,
			finalText: editor.getText(),
			lifecycle: lifecycle.trimEnd().split("\n"),
		};
	} finally {
		await rm(tempDir, { recursive: true, force: true });
	}
}

async function replayAutocomplete(
	modules: Awaited<ReturnType<typeof loadModules>>,
	enableSkillCommands: boolean,
): Promise<{
	replays: AutocompleteReplay[];
	transfer: AutocompleteProviderTransfer;
}> {
	const projectSource = {
		path: "/fixture/prompts/review-prompt.md",
		source: "local",
		scope: "project",
		origin: "top-level",
	};
	const userSource = {
		path: "/fixture/extensions/commands.ts",
		source: "local",
		scope: "user",
		origin: "top-level",
	};
	const temporarySource = {
		path: "/fixture/skills/inspect-skill/SKILL.md",
		source: "cli",
		scope: "temporary",
		origin: "top-level",
	};
	const context = Object.create(modules.InteractiveMode.prototype) as Record<
		string,
		unknown
	>;
	Object.defineProperties(
		context,
		Object.fromEntries(
			Object.entries({
				session: {
					scopedModels: [],
					modelRuntime: {
						getAvailable: async () => [
							{
								id: "claude-sonnet-4-5",
								provider: "anthropic",
								name: "Claude Sonnet 4.5",
							},
							{ id: "gpt-5.1", provider: "openai", name: "GPT 5.1" },
							{
								id: "openai/gpt-5",
								provider: "openrouter",
								name: "GPT 5 via OpenRouter",
							},
						],
						getProviders: () => [
							{ id: "openai", name: "OpenAI", auth: { oauth: {} } },
							{
								id: "anthropic",
								name: "Anthropic",
								auth: { oauth: {}, apiKey: {} },
							},
							{ id: "google", name: "Google", auth: { apiKey: {} } },
						],
						getProviderAuthStatus: () => ({ configured: false }),
						isUsingOAuth: () => false,
					},
					promptTemplates: [
						{
							name: "review-prompt",
							description: "Review a path",
							argumentHint: "<path>",
							sourceInfo: projectSource,
						},
					],
					extensionRunner: {
						getRegisteredCommands: () => [
							{
								name: "extension-command",
								invocationName: "extension-command",
								description: "Run extension command",
								sourceInfo: userSource,
							},
							{
								name: "model",
								invocationName: "model:1",
								description: "Conflicts with a built-in",
								sourceInfo: userSource,
							},
						],
					},
					resourceLoader: {
						getSkills: () => ({
							skills: [
								{
									name: "inspect-skill",
									description: "Inspect the workspace",
									filePath: temporarySource.path,
									sourceInfo: temporarySource,
								},
							],
							diagnostics: [],
						}),
					},
				},
				settingsManager: { getEnableSkillCommands: () => enableSkillCommands },
				sessionManager: { getCwd: () => "/fixture" },
				skillCommands: new Map<string, string>(),
				fdPath: null,
				autocompleteProviderWrappers: [],
			}).map(([key, value]) => [
				key,
				{ configurable: true, writable: true, value },
			]),
		),
	);
	const createProvider = Reflect.get(
		modules.InteractiveMode.prototype,
		"createBaseAutocompleteProvider",
	) as (this: Record<string, unknown>) => {
		getSuggestions(
			lines: string[],
			cursorLine: number,
			cursorCol: number,
			options: { signal: AbortSignal; force?: boolean },
		): Promise<AutocompleteReplay["result"]>;
	};
	const provider = createProvider.call(context);
	const inputs = [
		"/review-prompt",
		"/extension-command",
		"/skill:inspect-skill",
		"/model:1",
		"/model ",
		"/model openai",
		"/model sonnet",
		"/login ",
		"/login api",
		"/login ant",
	];
	const signal = new AbortController().signal;
	const replays = await Promise.all(
		inputs.map(async (input) => ({
			input,
			result: await provider.getSuggestions([input], 0, input.length, {
				signal,
			}),
		})),
	);

	let defaultProvider: unknown;
	let replacementProvider: unknown;
	Object.defineProperties(
		context,
		Object.fromEntries(
			Object.entries({
				defaultEditor: {
					setAutocompleteProvider: (value: unknown) => {
						defaultProvider = value;
					},
				},
				editor: {
					setAutocompleteProvider: (value: unknown) => {
						replacementProvider = value;
					},
				},
			}).map(([key, value]) => [
				key,
				{ configurable: true, writable: true, value },
			]),
		),
	);
	const setupProvider = Reflect.get(
		modules.InteractiveMode.prototype,
		"setupAutocompleteProvider",
	) as (this: Record<string, unknown>) => void;
	setupProvider.call(context);

	return {
		replays,
		transfer: {
			defaultAssigned: defaultProvider !== undefined,
			replacementAssigned: replacementProvider !== undefined,
			sameProvider: defaultProvider === replacementProvider,
			storedProvider: context.autocompleteProvider === defaultProvider,
		},
	};
}

function replayEditorLifecycle(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const events: string[] = [];
	const defaultSubmit = (text: string) => events.push(`submit:${text}`);
	const defaultChange = (text: string) => events.push(`change:${text}`);
	const defaultEditor = {
		text: "draft",
		onSubmit: defaultSubmit,
		onChange: defaultChange,
		borderColor: "DEFAULT_BORDER",
		actionHandlers: new Map<string, () => void>([
			["app.clear", () => events.push("action:clear")],
		]),
		onEscape: () => events.push("escape"),
		onCtrlD: () => events.push("ctrl-d"),
		onPasteImage: () => events.push("paste-image"),
		onExtensionShortcut: (data: string) => {
			events.push(`shortcut:${data}`);
			return true;
		},
		getText() {
			return this.text;
		},
		setText(text: string) {
			this.text = text;
		},
		getPaddingX: () => 3,
		setAutocompleteProvider() {},
		render: () => ["DEFAULT"],
		invalidate() {},
	};
	const customEditor = {
		text: "",
		onSubmit: undefined as ((text: string) => void) | undefined,
		onChange: undefined as ((text: string) => void) | undefined,
		borderColor: "CUSTOM_BORDER",
		paddingX: -1,
		autocompleteProvider: undefined as unknown,
		actionHandlers: new Map<string, () => void>(),
		onEscape: undefined as (() => void) | undefined,
		onCtrlD: undefined as (() => void) | undefined,
		onPasteImage: undefined as (() => void) | undefined,
		onExtensionShortcut: undefined as
			| ((data: string) => boolean | undefined)
			| undefined,
		inputs: [] as string[],
		getText() {
			return this.text;
		},
		getExpandedText() {
			return `expanded:${this.text}`;
		},
		setText(text: string) {
			this.text = text;
		},
		setPaddingX(value: number) {
			this.paddingX = value;
		},
		setAutocompleteProvider(value: unknown) {
			this.autocompleteProvider = value;
		},
		handleInput(data: string) {
			this.inputs.push(data);
		},
		render: () => ["CUSTOM"],
		invalidate() {},
	};
	const children: unknown[] = [defaultEditor];
	const focus: string[] = [];
	let renders = 0;
	const autocompleteProvider = { id: "autocomplete" };
	const context = Object.create(modules.InteractiveMode.prototype) as Record<
		string,
		unknown
	>;
	defineContextValues(context, {
		defaultEditor,
		editor: defaultEditor,
		editorComponentFactory: undefined,
		autocompleteProvider,
		editorContainer: {
			clear: () => {
				children.length = 0;
			},
			addChild: (child: unknown) => children.push(child),
		},
		ui: {
			setFocus: (component: unknown) =>
				focus.push(component === customEditor ? "custom" : "default"),
			requestRender: () => {
				renders++;
			},
		},
	});
	const extensionUI = (
		Reflect.get(
			modules.InteractiveMode.prototype,
			"createExtensionUIContext",
		) as (this: Record<string, unknown>) => Record<string, (...args: any[]) => any>
	).call(context);
	const factory = () => customEditor;
	extensionUI.setEditorComponent(factory);
	const factoryStored = extensionUI.getEditorComponent() === factory;
	const customInstalled = context.editor === customEditor;
	const textCopied = customEditor.text;
	const callbacksCopied =
		customEditor.onSubmit === defaultSubmit && customEditor.onChange === defaultChange;
	customEditor.onSubmit?.("value");
	customEditor.onChange?.("changed");
	customEditor.onEscape?.();
	customEditor.onCtrlD?.();
	customEditor.onPasteImage?.();
	customEditor.onExtensionShortcut?.("ctrl+x");
	customEditor.actionHandlers.get("app.clear")?.();
	const expandedText = extensionUI.getEditorText();
	extensionUI.pasteToEditor("paste\nblock");
	customEditor.setText("custom-change");
	extensionUI.setEditorComponent(undefined);
	const actionEvents = events.slice();

	const preservedEditor = {
		...customEditor,
		text: "",
		onSubmit: undefined as ((text: string) => void) | undefined,
		onChange: undefined as ((text: string) => void) | undefined,
		actionHandlers: new Map<string, () => void>([
			["app.clear", () => events.push("custom-action:clear")],
			["custom.action", () => events.push("custom-action:kept")],
		]),
		onEscape: () => events.push("custom-escape"),
		onCtrlD: () => events.push("custom-ctrl-d"),
		onPasteImage: () => events.push("custom-paste-image"),
		onExtensionShortcut: (data: string) => {
			events.push(`custom-shortcut:${data}`);
			return false;
		},
	};
	const preservedStart = events.length;
	extensionUI.setEditorComponent(() => preservedEditor);
	preservedEditor.onSubmit?.("preserved");
	preservedEditor.onChange?.("preserved-change");
	preservedEditor.onEscape?.();
	preservedEditor.onCtrlD?.();
	preservedEditor.onPasteImage?.();
	preservedEditor.onExtensionShortcut?.("ctrl+y");
	preservedEditor.actionHandlers.get("app.clear")?.();
	preservedEditor.actionHandlers.get("custom.action")?.();
	const preservedActionEvents = events.slice(preservedStart);
	extensionUI.setEditorComponent(undefined);

	return {
		factoryStored,
		customInstalled,
		textCopied,
		callbacksCopied,
		borderColor: customEditor.borderColor,
		paddingX: customEditor.paddingX,
		autocompleteTransferred:
			customEditor.autocompleteProvider === autocompleteProvider,
		expandedText,
		pasteInput: customEditor.inputs[0],
		pasteTargetWasCustom: customEditor.inputs.length === 1,
		actionEvents,
		preservedActionEvents,
		restoredText: defaultEditor.text,
		restoredDefault: context.editor === defaultEditor,
		focus,
		renders,
		finalChild: children[0] === defaultEditor ? "default" : "other",
	};
}

function replayTerminalInputLifecycle(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const listeners: Array<{
		active: boolean;
		handler: (data: string) => Record<string, unknown>;
	}> = [];
	const unsubscribeEvents: number[] = [];
	const context = Object.create(modules.InteractiveMode.prototype) as Record<
		string,
		unknown
	>;
	defineContextValues(context, {
		extensionTerminalInputUnsubscribers: new Set<() => void>(),
		ui: {
			addInputListener: (
				handler: (data: string) => Record<string, unknown>,
			) => {
				const index = listeners.length;
				const entry = { active: true, handler };
				listeners.push(entry);
				return () => {
					if (entry.active) unsubscribeEvents.push(index);
					entry.active = false;
				};
			},
		},
	});
	const extensionUI = (
		Reflect.get(
			modules.InteractiveMode.prototype,
			"createExtensionUIContext",
		) as (this: Record<string, unknown>) => Record<string, (...args: any[]) => any>
	).call(context);
	const unsubscribeFirst = extensionUI.onTerminalInput(() => ({
		consume: false,
		data: "",
	}));
	extensionUI.onTerminalInput(() => ({ consume: false }));
	const presentEmpty = listeners[0].handler("source");
	const absent = listeners[1].handler("source");
	unsubscribeFirst();
	const activeAfterExplicit = listeners.filter((listener) => listener.active).length;
	(
		Reflect.get(
			modules.InteractiveMode.prototype,
			"clearExtensionTerminalInputListeners",
		) as (this: Record<string, unknown>) => void
	).call(context);
	return {
		presentEmpty: {
			consume: presentEmpty.consume ?? false,
			hasData: Object.hasOwn(presentEmpty, "data"),
			data: presentEmpty.data,
		},
		absent: {
			consume: absent.consume ?? false,
			hasData: Object.hasOwn(absent, "data"),
		},
		activeAfterExplicit,
		activeAfterReset: listeners.filter((listener) => listener.active).length,
		trackedAfterReset: (
			context.extensionTerminalInputUnsubscribers as Set<() => void>
		).size,
		unsubscribeEvents,
	};
}

async function replayWorkingLifecycle(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	return withCapturedIntervals(async (intervals) => {
		const requestRenders: string[] = [];
		const clears: Array<string | undefined> = [];
		const session = { isStreaming: false };
		const context = Object.create(
			modules.InteractiveMode.prototype,
		) as Record<string, any>;
		defineContextValues(context, {
			session,
			workingMessage: undefined,
			workingVisible: true,
			workingIndicatorOptions: undefined,
			defaultWorkingMessage: "Working...",
			activeStatusIndicator: undefined,
			ui: { requestRender: () => requestRenders.push("render") },
			clearStatusIndicator: (kind?: string) => {
				clears.push(kind);
				if (!kind || context.activeStatusIndicator?.kind === kind) {
					context.activeStatusIndicator?.dispose?.();
					context.activeStatusIndicator = undefined;
				}
			},
			showStatusIndicator: (indicator: unknown) => {
				context.activeStatusIndicator = indicator;
			},
		});
		const extensionUI = (
			Reflect.get(
				modules.InteractiveMode.prototype,
				"createExtensionUIContext",
			) as (this: Record<string, unknown>) => Record<string, (...args: any[]) => any>
		).call(context);
		extensionUI.setWorkingMessage("Reviewing");
		extensionUI.setWorkingIndicator({ frames: ["A", "B"] });
		const storedBeforeStreaming = {
			message: context.workingMessage,
			options: context.workingIndicatorOptions,
		};
		session.isStreaming = true;
		extensionUI.setWorkingVisible(false);
		extensionUI.setWorkingVisible(true);
		const indicator = context.activeStatusIndicator as {
			kind: string;
			render(width: number): string[];
			dispose(): void;
		};
		const initialLines = indicator.render(48);
		const animation = intervals.find((interval) => interval.active);
		animation?.callback();
		const nextLines = indicator.render(48);
		extensionUI.setWorkingMessage();
		const defaultMessageLines = indicator.render(48);
		extensionUI.setWorkingIndicator({ frames: [] });
		const hiddenIndicatorLines = indicator.render(48);
		extensionUI.setWorkingVisible(false);
		extensionUI.setWorkingMessage("");
		extensionUI.setWorkingVisible(true);
		const emptyMessageLines = context.activeStatusIndicator.render(48);
		extensionUI.setWorkingVisible(false);

		return {
			storedBeforeStreaming,
			visibleAfterHide: context.workingVisible,
			shownKind: indicator.kind,
			clearKinds: clears,
			defaultIntervalMs: animation?.delay,
			initialLines,
			nextLines,
			defaultMessageLines,
			hiddenIndicatorLines,
			emptyMessageLines,
			requestRenderCount: requestRenders.length,
			activeIntervalsAfterHide: intervals.filter((interval) => interval.active)
				.length,
		};
	});
}

async function replayCustomUILifecycle(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const showCustom = Reflect.get(
		modules.InteractiveMode.prototype,
		"showExtensionCustom",
	) as <T>(
		this: Record<string, unknown>,
		factory: (...args: any[]) => unknown,
		options?: Record<string, unknown>,
	) => Promise<T>;

	const runInstalled = async () => {
		const events: string[] = [];
		const editor = {
			text: "draft",
			getText() {
				return this.text;
			},
			setText(text: string) {
				this.text = text;
				events.push(`editor-text:${text}`);
			},
			render: () => ["EDITOR"],
			invalidate() {},
		};
		const component = {
			dispose: () => events.push("dispose"),
			render: () => ["CUSTOM"],
			invalidate() {},
		};
		const children: unknown[] = [editor];
		let done: (value: string) => void = () => {};
		const context: Record<string, unknown> = {
			editor,
			editorContainer: {
				clear: () => {
					children.length = 0;
				},
				addChild: (child: unknown) => {
					children.push(child);
					events.push(child === component ? "install-custom" : "install-editor");
				},
			},
			keybindings: {},
			ui: {
				setFocus: (child: unknown) =>
					events.push(child === component ? "focus-custom" : "focus-editor"),
				requestRender: () => events.push("render"),
			},
		};
		const pending = showCustom.call<string>(context, (_ui, _theme, _keys, close) => {
			done = close;
			return component;
		});
		await Promise.resolve();
		await Promise.resolve();
		editor.text = "mutated-during-custom";
		done("first");
		done("second");
		const result = await pending;
		return {
			result,
			editorText: editor.text,
			finalChild: children[0] === editor ? "editor" : "other",
			events,
			disposeCount: events.filter((event) => event === "dispose").length,
		};
	};

	const runEarlyDone = async () => {
		const events: string[] = [];
		const editor = {
			text: "early-draft",
			getText() {
				return this.text;
			},
			setText(text: string) {
				this.text = text;
				events.push(`editor-text:${text}`);
			},
			render: () => ["EDITOR"],
			invalidate() {},
		};
		const component = {
			dispose: () => events.push("dispose"),
			render: () => ["EARLY"],
			invalidate() {},
		};
		const children: unknown[] = [editor];
		const context: Record<string, unknown> = {
			editor,
			editorContainer: {
				clear: () => {
					children.length = 0;
				},
				addChild: (child: unknown) => {
					children.push(child);
					events.push(child === component ? "install-custom" : "install-editor");
				},
			},
			keybindings: {},
			ui: {
				setFocus: (child: unknown) =>
					events.push(child === component ? "focus-custom" : "focus-editor"),
				requestRender: () => events.push("render"),
			},
		};
		const result = await showCustom.call<string>(
			context,
			(_ui, _theme, _keys, close) => {
				close("first");
				close("second");
				return component;
			},
		);
		await Promise.resolve();
		return {
			result,
			finalChild: children[0] === editor ? "editor" : "other",
			events,
			disposeCount: events.filter((event) => event === "dispose").length,
		};
	};

	return { installed: await runInstalled(), earlyDone: await runEarlyDone() };
}

async function replayDialogAndPrimitiveLifecycle(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const createContext = () => {
		const editor = { render: () => ["EDITOR"], invalidate() {} };
		const children: unknown[] = [editor];
		const focus: string[] = [];
		let renders = 0;
		const context = Object.create(
			modules.InteractiveMode.prototype,
		) as Record<string, any>;
		defineContextValues(context, {
			editor,
			editorContainer: {
				clear: () => {
					children.length = 0;
				},
				addChild: (child: unknown) => children.push(child),
			},
			ui: {
				setFocus: (child: unknown) =>
					focus.push(child === editor ? "editor" : "dialog"),
				requestRender: () => {
					renders++;
				},
			},
		});
		return { context, editor, children, focus, renders: () => renders };
	};

	const empty = createContext();
	const emptyPromise = (
		Reflect.get(
			modules.InteractiveMode.prototype,
			"showExtensionSelector",
		) as (
			this: Record<string, unknown>,
			title: string,
			options: string[],
		) => Promise<string | undefined>
	).call(empty.context, "Empty selector", []);
	const emptyComponent = empty.context.extensionSelector as {
		render(width: number): string[];
		handleInput(data: string): void;
	};
	const emptyInstalled = empty.children[0] === emptyComponent;
	const emptyLines = emptyComponent.render(32);
	emptyComponent.handleInput("\x1b");
	const emptyResult = await emptyPromise;

	const timed = await withCapturedIntervals(async (intervals) => {
		const state = createContext();
		const promise = (
			Reflect.get(
				modules.InteractiveMode.prototype,
				"showExtensionInput",
			) as (
				this: Record<string, unknown>,
				title: string,
				placeholder: string,
				opts: Record<string, unknown>,
			) => Promise<string | undefined>
		).call(state.context, "Timed input", "ignored", { timeout: 1001 });
		const component = state.context.extensionInput as {
			render(width: number): string[];
		};
		const initialLines = component.render(32);
		const interval = intervals.find((entry) => entry.active);
		interval?.callback();
		interval?.callback();
		const result = await promise;
		return {
			initialLines,
			result,
			finalChild: state.children[0] === state.editor ? "editor" : "other",
			focus: state.focus,
			renders: state.renders(),
			intervalActive: interval?.active ?? false,
		};
	});

	const countdown = await withCapturedIntervals(async (intervals) => {
		const ticks: number[] = [];
		let expires = 0;
		let renders = 0;
		const timer = new modules.CountdownTimer(
			1001,
			{ requestRender: () => renders++ },
			(seconds: number) => ticks.push(seconds),
			() => expires++,
		);
		const interval = intervals[0];
		interval.callback();
		interval.callback();
		timer.dispose();
		return {
			ticks,
			expires,
			renders,
			intervalMs: interval.delay,
			active: interval.active,
		};
	});

	const border = new modules.DynamicBorder((value: string) => `<${value}>`);
	return {
		emptySelector: {
			installed: emptyInstalled,
			lines: emptyLines,
			result: emptyResult ?? null,
			finalChild: empty.children[0] === empty.editor ? "editor" : "other",
			focus: empty.focus,
			renders: empty.renders(),
		},
		timedInput: { ...timed, result: timed.result ?? null },
		countdown,
		dynamicBorderWidthZero: border.render(0),
	};
}

function replayHeaderFooterLifecycle(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const events: string[] = [];
	const component = (name: string) => ({
		name,
		dispose: () => events.push(`dispose:${name}`),
		render: () => [name],
		invalidate() {},
	});
	const builtInFooter = component("built-in-footer");
	const builtInHeader = component("built-in-header");
	const footerA = component("footer-a");
	const footerB = component("footer-b");
	const headerA = component("header-a");
	const headerB = component("header-b");
	const uiChildren: unknown[] = [builtInFooter];
	const headerChildren: unknown[] = [builtInHeader];
	const context = Object.create(modules.InteractiveMode.prototype) as Record<
		string,
		any
	>;
	defineContextValues(context, {
		footer: builtInFooter,
		customFooter: undefined,
		footerDataProvider: {},
		builtInHeader,
		customHeader: undefined,
		headerContainer: { children: headerChildren },
		toolOutputExpanded: false,
		ui: {
			removeChild: (child: unknown) => {
				const index = uiChildren.indexOf(child);
				if (index >= 0) uiChildren.splice(index, 1);
				events.push(`remove:${(child as { name: string }).name}`);
			},
			addChild: (child: unknown) => {
				uiChildren.push(child);
				events.push(`add:${(child as { name: string }).name}`);
			},
			requestRender: () => events.push("render"),
		},
	});
	const setFooter = Reflect.get(
		modules.InteractiveMode.prototype,
		"setExtensionFooter",
	) as (this: Record<string, unknown>, factory?: (...args: any[]) => unknown) => void;
	const setHeader = Reflect.get(
		modules.InteractiveMode.prototype,
		"setExtensionHeader",
	) as (this: Record<string, unknown>, factory?: (...args: any[]) => unknown) => void;
	setFooter.call(context, () => footerA);
	setFooter.call(context, () => footerB);
	setFooter.call(context, undefined);
	setHeader.call(context, () => headerA);
	setHeader.call(context, () => headerB);
	setHeader.call(context, undefined);
	return {
		events,
		footerDisposals: events.filter((event) => event.startsWith("dispose:footer")),
		headerDisposals: events.filter((event) => event.startsWith("dispose:header")),
		finalFooter: uiChildren[0] === builtInFooter ? "built-in" : "other",
		finalHeader: headerChildren[0] === builtInHeader ? "built-in" : "other",
	};
}

function replayThemeObject(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const settingsWrites: string[] = [];
	const instanceCalls: string[] = [];
	const nameCalls: string[] = [];
	const themeObject = modules.theme.getThemeByName("light");
	if (!themeObject) throw new Error("light theme was not loaded");
	const context = Object.create(modules.InteractiveMode.prototype) as Record<
		string,
		unknown
	>;
	defineContextValues(context, {
		settingsManager: {
			getTheme: () => "dark",
			setTheme: (name: string) => settingsWrites.push(name),
		},
		themeController: {
			setThemeInstance: (value: { name?: string }) => {
				instanceCalls.push(value.name ?? "<unnamed>");
				return { success: true };
			},
			setThemeName: (name: string) => {
				nameCalls.push(name);
				return { success: true };
			},
		},
	});
	const extensionUI = (
		Reflect.get(
			modules.InteractiveMode.prototype,
			"createExtensionUIContext",
		) as (this: Record<string, unknown>) => Record<string, (...args: any[]) => any>
	).call(context);
	return {
		result: extensionUI.setTheme(themeObject),
		instanceCalls,
		nameCalls,
		settingsWrites,
	};
}

function replayOrdinaryErrorFrame(
	modules: Awaited<ReturnType<typeof loadModules>>,
) {
	const chatContainer = new modules.tui.Container();
	chatContainer.addChild(new modules.tui.Text("BEFORE", 1, 0));
	const context = {
		chatContainer,
		ui: { requestRender() {} },
	};
	(
		Reflect.get(
			modules.InteractiveMode.prototype,
			"showError",
		) as (this: Record<string, unknown>, message: string) => void
	).call(context, "ORDINARY");
	return { width: 32, lines: chatContainer.render(32) };
}

export async function generateF12App(
	upstreamRoot: string,
	outputRoot: string,
	upstreamCommit: string,
): Promise<void> {
	const modules = await loadModules(upstreamRoot);
	const frames = WIDTHS.flatMap((width) => replayStatusFrames(modules, width));
	const notificationFrames = WIDTHS.flatMap((width) =>
		replayNotificationFrames(modules, width),
	);
	const dialogReplays = WIDTHS.map((width) =>
		replayDialogFrames(modules, width),
	);
	const externalEditor = await replayExternalEditor(modules);
	const enabledAutocomplete = await replayAutocomplete(modules, true);
	const disabledAutocomplete = await replayAutocomplete(modules, false);
	const autocomplete = {
		enabled: enabledAutocomplete.replays,
		skillCommandsDisabled: disabledAutocomplete.replays,
		providerTransfer: enabledAutocomplete.transfer,
	};
	const lifecycle = {
		editor: replayEditorLifecycle(modules),
		terminalInput: replayTerminalInputLifecycle(modules),
		working: await replayWorkingLifecycle(modules),
		customUI: await replayCustomUILifecycle(modules),
		dialogsAndPrimitives: await replayDialogAndPrimitiveLifecycle(modules),
		headerFooter: replayHeaderFooterLifecycle(modules),
		themeObject: replayThemeObject(modules),
		ordinaryError: replayOrdinaryErrorFrame(modules),
	};
	const familyDir = path.join(outputRoot, "F12-app");
	await rm(familyDir, { recursive: true, force: true });
	await mkdir(familyDir, { recursive: true });
	const manifest = {
		family: "F12-app",
		upstreamCommit,
		generator: "conformance/extract/f12-app.ts",
		sources: [
			"packages/coding-agent/src/modes/interactive/interactive-mode.ts",
			"packages/coding-agent/src/core/slash-commands.ts",
			"packages/coding-agent/src/core/keybindings.ts",
			"packages/coding-agent/src/core/extensions/types.ts",
			"packages/coding-agent/src/modes/interactive/model-search.ts",
			"packages/coding-agent/src/modes/interactive/theme/{theme,theme-controller}.ts",
			"packages/coding-agent/src/modes/interactive/components/oauth-selector.ts",
			"packages/tui/src/components/{loader,spacer,text}.ts",
			"packages/tui/src/tui.ts",
			"packages/coding-agent/test/interactive-mode-status.test.ts",
			"packages/coding-agent/test/status-indicator.test.ts",
			"packages/coding-agent/src/modes/interactive/components/{extension-selector,extension-input,extension-editor,keybinding-hints,dynamic-border,countdown-timer,status-indicator,custom-editor}.ts",
		],
		files: ["status-frames.json"],
		metadata: {
			widths: WIDTHS,
			normalization: [],
			divergences: [],
		},
	};
	const fixture = {
		schemaVersion: 7,
		frames,
		notificationFrames,
		dialogFrames: dialogReplays.flatMap((replay) => replay.frames),
		dialogResults: dialogReplays.map((replay, index) => ({
			width: WIDTHS[index],
			...replay.result,
		})),
		externalEditor,
		autocomplete,
		lifecycle,
	};
	await writeFile(
		path.join(familyDir, "manifest.json"),
		`${JSON.stringify(manifest, null, 2)}\n`,
	);
	await writeFile(
		path.join(familyDir, "status-frames.json"),
		`${JSON.stringify(fixture, null, 2)}\n`,
	);
}
