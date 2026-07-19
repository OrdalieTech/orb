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
	};
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
			"packages/coding-agent/src/modes/interactive/model-search.ts",
			"packages/coding-agent/src/modes/interactive/components/oauth-selector.ts",
			"packages/tui/src/components/{spacer,text}.ts",
			"packages/tui/src/tui.ts",
			"packages/coding-agent/test/interactive-mode-status.test.ts",
			"packages/coding-agent/src/modes/interactive/components/{extension-selector,extension-input,extension-editor,keybinding-hints,dynamic-border,countdown-timer}.ts",
		],
		files: ["status-frames.json"],
		metadata: {
			widths: WIDTHS,
			normalization: [],
			divergences: [],
		},
	};
	const fixture = {
		schemaVersion: 6,
		frames,
		notificationFrames,
		dialogFrames: dialogReplays.flatMap((replay) => replay.frames),
		dialogResults: dialogReplays.map((replay, index) => ({
			width: WIDTHS[index],
			...replay.result,
		})),
		externalEditor,
		autocomplete,
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
