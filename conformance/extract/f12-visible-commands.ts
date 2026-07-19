import { createHash } from "node:crypto";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

type CommandFixture = {
	name: string;
	input: string;
	dispatchTrace: string[];
	finalEditorText: string;
	transition: string | null;
	trace: string[];
	chat: ChatSnapshot;
};

type ChatSnapshot = {
	lineCount: number;
	sha256: string;
	raw: RawFrameSnapshot;
	lines?: string[];
	head?: string[];
	tail?: string[];
};

type RawFrameSnapshot = {
	lineCount: number;
	sha256: string;
	lines?: string[];
	head?: string[];
	tail?: string[];
};

type LoadedModules = Awaited<ReturnType<typeof loadModules>>;

const WIDTH = 100;

function stripTerminalControls(value: string): string {
	return value
		.replace(/\u001b\][^\u0007]*(?:\u0007|\u001b\\)/g, "")
		.replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, "")
		.replace(/\r/g, "");
}

function normalizeLines(lines: string[]): string[] {
	const normalized = lines.map((line) =>
		stripTerminalControls(line).replace(/\s+$/g, ""),
	);
	while (normalized.length > 0 && normalized[normalized.length - 1] === "")
		normalized.pop();
	return normalized;
}

function replaceFramePaths(
	lines: string[],
	replacements: Array<[string, string]> = [],
): string[] {
	return lines.map((line) => {
		let value = line;
		for (const [from, to] of replacements) {
			const originalLength = value.length;
			value = value.replaceAll(from, to);
			if (value.length < originalLength)
				value += " ".repeat(originalLength - value.length);
		}
		return value;
	});
}

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
	theme.initTheme("dark");
	tui.setKeybindings(new keybindings.KeybindingsManager());
	return {
		tui,
		theme,
		InteractiveMode: interactive.InteractiveMode,
		KeybindingsManager: keybindings.KeybindingsManager,
	};
}

function method(
	modules: LoadedModules,
	name: string,
): (...args: unknown[]) => unknown {
	const value = Reflect.get(modules.InteractiveMode.prototype, name);
	if (typeof value !== "function")
		throw new Error(`InteractiveMode.${name} not found`);
	return value;
}

function baseContext(modules: LoadedModules) {
	const context = Object.create(modules.InteractiveMode.prototype) as Record<
		string,
		unknown
	>;
	context.chatContainer = new modules.tui.Container();
	context.editorContainer = new modules.tui.Container();
	context.lastStatusSpacer = undefined;
	context.lastStatusText = undefined;
	context.ui = { requestRender: () => {} };
	return context;
}

async function captureDispatch(
	modules: LoadedModules,
	input: string,
): Promise<{ trace: string[]; editorText: string }> {
	const trace: string[] = [];
	let editorText = input;
	const context = Object.create(modules.InteractiveMode.prototype) as Record<
		string,
		unknown
	>;
	const editor = {
		setText: (value: string) => {
			editorText = value;
			trace.push(`editor:${JSON.stringify(value)}`);
		},
	};
	context.defaultEditor = {};
	context.editor = editor;
	const actionNames = [
		"showSettingsSelector",
		"showModelsSelector",
		"handleModelCommand",
		"handleExportCommand",
		"handleImportCommand",
		"handleShareCommand",
		"handleCopyCommand",
		"handleNameCommand",
		"handleSessionCommand",
		"handleChangelogCommand",
		"handleHotkeysCommand",
		"showUserMessageSelector",
		"handleCloneCommand",
		"showTreeSelector",
		"showTrustSelector",
		"handleLoginCommand",
		"showOAuthSelector",
		"handleClearCommand",
		"handleCompactCommand",
		"handleReloadCommand",
		"showSessionSelector",
		"shutdown",
	];
	for (const actionName of actionNames) {
		context[actionName] = async (...args: unknown[]) => {
			trace.push(
				`action:${actionName}:${JSON.stringify(args.map((arg) => arg ?? null))}`,
			);
		};
	}
	method(modules, "setupEditorSubmitHandler").call(context);
	const onSubmit = (
		context.defaultEditor as { onSubmit?: (text: string) => Promise<void> }
	).onSubmit;
	if (!onSubmit) throw new Error("upstream submit handler was not installed");
	await onSubmit(input);
	return { trace, editorText };
}

function snapshotLines(rawLines: string[]): ChatSnapshot {
	const lines = normalizeLines(rawLines);
	const raw: RawFrameSnapshot = {
		lineCount: rawLines.length,
		sha256: createHash("sha256").update(JSON.stringify(rawLines)).digest("hex"),
	};
	if (rawLines.length <= 80) raw.lines = rawLines;
	else {
		raw.head = rawLines.slice(0, 20);
		raw.tail = rawLines.slice(-20);
	}
	const snapshot: ChatSnapshot = {
		lineCount: lines.length,
		sha256: createHash("sha256").update(JSON.stringify(lines)).digest("hex"),
		raw,
	};
	if (lines.length <= 80) snapshot.lines = lines;
	else {
		snapshot.head = lines.slice(0, 20);
		snapshot.tail = lines.slice(-20);
	}
	return snapshot;
}

function renderChat(
	context: Record<string, unknown>,
	agentDir: string,
): ChatSnapshot {
	const rawLines = replaceFramePaths(
		(context.chatContainer as { render(width: number): string[] }).render(
			WIDTH,
		),
		[[agentDir, "<tmp>"]],
	);
	return snapshotLines(rawLines);
}

function transitionContext(modules: LoadedModules, transition: string) {
	const context = baseContext(modules);
	context.showSelector = () => {
		context.__transition = transition;
	};
	return context;
}

async function captureBehavior(
	modules: LoadedModules,
	name: string,
	agentDir: string,
): Promise<{ transition: string | null; trace: string[]; chat: ChatSnapshot }> {
	const trace: string[] = [];
	let context: Record<string, unknown>;

	switch (name) {
		case "settings": {
			context = transitionContext(modules, "settings-selector");
			method(modules, "showSettingsSelector").call(context);
			break;
		}
		case "model": {
			context = transitionContext(modules, "model-selector:fixture/model");
			context.findExactModelMatch = async () => undefined;
			await method(modules, "handleModelCommand").call(
				context,
				"fixture/model",
			);
			break;
		}
		case "scoped-models": {
			context = baseContext(modules);
			context.runtimeHost = {
				session: {
					modelRuntime: {
						refresh: async () => {},
						getAvailable: async () => [],
					},
				},
			};
			await method(modules, "showModelsSelector").call(context);
			break;
		}
		case "export": {
			context = baseContext(modules);
			context.runtimeHost = {
				session: {
					exportToJsonl: (outputPath: string) => {
						trace.push(`export:jsonl:${outputPath}`);
						return "<tmp>/session.jsonl";
					},
					exportToHtml: async () => {
						throw new Error("unexpected HTML export");
					},
				},
			};
			await method(modules, "handleExportCommand").call(
				context,
				'/export "<tmp>/session.jsonl" ignored',
			);
			break;
		}
		case "import": {
			context = baseContext(modules);
			context.clearStatusIndicator = () => trace.push("clear-status");
			context.showExtensionConfirm = async (title: string, message: string) => {
				trace.push(`confirm:${title}:${message}`);
				return true;
			};
			context.runtimeHost = {
				importFromJsonl: async (inputPath: string) => {
					trace.push(`import:${inputPath}`);
					return { cancelled: false };
				},
			};
			await method(modules, "handleImportCommand").call(
				context,
				'/import "fixture path/session.jsonl" trailing',
			);
			break;
		}
		case "share": {
			// The divergence ledger replaces upstream gist upload with upstream's local HTML export behavior.
			context = baseContext(modules);
			context.runtimeHost = {
				session: {
					exportToJsonl: () => {
						throw new Error("unexpected JSONL export");
					},
					exportToHtml: async (outputPath?: string) => {
						trace.push(`export:html:${outputPath ?? "<default>"}`);
						return "<tmp>/session.html";
					},
				},
			};
			await method(modules, "handleExportCommand").call(context, "/export");
			break;
		}
		case "copy": {
			context = baseContext(modules);
			context.runtimeHost = {
				session: { getLastAssistantText: () => undefined },
			};
			await method(modules, "handleCopyCommand").call(context);
			break;
		}
		case "name": {
			context = baseContext(modules);
			let sessionName: string | undefined;
			const sessionManager = { getSessionName: () => sessionName };
			context.runtimeHost = {
				session: {
					sessionManager,
					setSessionName: (value: string) => {
						sessionName = value;
						trace.push(`name:${value}`);
					},
				},
			};
			method(modules, "handleNameCommand").call(
				context,
				"/name fixture session",
			);
			break;
		}
		case "session": {
			context = baseContext(modules);
			context.runtimeHost = {
				session: {
					sessionManager: {
						getSessionName: () => "fixture session",
						getEntries: () => [],
					},
					modelRuntime: {},
					getSessionStats: () => ({
						sessionFile: null,
						sessionId: "fixture-session-id",
						totalMessages: 0,
						userMessages: 0,
						assistantMessages: 0,
						toolCalls: 0,
						toolResults: 0,
						tokens: {
							input: 0,
							output: 0,
							cacheRead: 0,
							cacheWrite: 0,
							total: 0,
						},
						cost: 0,
					}),
				},
			};
			method(modules, "handleSessionCommand").call(context);
			break;
		}
		case "changelog": {
			context = baseContext(modules);
			context.getMarkdownThemeWithSettings = () =>
				modules.theme.getMarkdownTheme();
			method(modules, "handleChangelogCommand").call(context);
			break;
		}
		case "hotkeys": {
			context = baseContext(modules);
			context.keybindings = new modules.KeybindingsManager();
			context.getMarkdownThemeWithSettings = () =>
				modules.theme.getMarkdownTheme();
			context.runtimeHost = {
				session: { extensionRunner: { getShortcuts: () => new Map() } },
			};
			method(modules, "handleHotkeysCommand").call(context);
			break;
		}
		case "fork": {
			context = baseContext(modules);
			context.runtimeHost = {
				session: { getUserMessagesForForking: () => [] },
			};
			method(modules, "showUserMessageSelector").call(context);
			break;
		}
		case "clone": {
			context = baseContext(modules);
			context.runtimeHost = {
				session: { sessionManager: { getLeafId: () => null } },
				fork: async () => {
					throw new Error("fork must not run without a leaf");
				},
			};
			await method(modules, "handleCloneCommand").call(context);
			break;
		}
		case "tree": {
			context = baseContext(modules);
			context.runtimeHost = {
				session: {
					sessionManager: { getTree: () => [], getLeafId: () => null },
					settingsManager: { getTreeFilterMode: () => "default" },
				},
			};
			method(modules, "showTreeSelector").call(context);
			break;
		}
		case "trust": {
			context = transitionContext(modules, "trust-selector");
			context.runtimeHost = {
				services: { agentDir },
				session: {
					sessionManager: { getCwd: () => path.join(agentDir, "project") },
					settingsManager: { isProjectTrusted: () => false },
				},
			};
			method(modules, "showTrustSelector").call(context);
			break;
		}
		case "login": {
			context = baseContext(modules);
			context.runtimeHost = {
				session: {
					modelRuntime: {
						getAvailable: async () => [],
						getProviders: () => [],
					},
				},
			};
			await method(modules, "handleLoginCommand").call(
				context,
				"missing-provider",
			);
			break;
		}
		case "logout": {
			context = baseContext(modules);
			context.runtimeHost = {
				session: { modelRuntime: { listCredentials: async () => [] } },
			};
			await method(modules, "showOAuthSelector").call(context, "logout");
			break;
		}
		case "new": {
			context = baseContext(modules);
			context.clearStatusIndicator = () => trace.push("clear-status");
			context.runtimeHost = {
				newSession: async () => {
					trace.push("new-session");
					return { cancelled: false };
				},
			};
			await method(modules, "handleClearCommand").call(context);
			break;
		}
		case "compact": {
			context = baseContext(modules);
			context.clearStatusIndicator = () => trace.push("clear-status");
			context.runtimeHost = {
				session: {
					compact: async (instructions?: string) => {
						trace.push(`compact:${instructions ?? "<default>"}`);
					},
				},
			};
			await method(modules, "handleCompactCommand").call(
				context,
				"fixture instructions",
			);
			break;
		}
		case "resume": {
			context = transitionContext(modules, "session-selector");
			method(modules, "showSessionSelector").call(context);
			break;
		}
		case "reload": {
			context = baseContext(modules);
			context.runtimeHost = {
				session: { isStreaming: true, isCompacting: false },
			};
			await method(modules, "handleReloadCommand").call(context);
			break;
		}
		case "quit": {
			context = baseContext(modules);
			context.isShuttingDown = false;
			context.themeController = {
				disableAutoSync: () => trace.push("theme-stop"),
			};
			context.ui = {
				terminal: {
					drainInput: async (milliseconds: number) =>
						trace.push(`drain:${milliseconds}`),
				},
			};
			context.stop = () => trace.push("stop");
			context.runtimeHost = {
				dispose: async () => trace.push("dispose"),
				session: {
					sessionManager: {
						isPersisted: () => false,
						getSessionFile: () => undefined,
						getSessionId: () => "fixture-session-id",
						getSessionDir: () => "<tmp>",
						usesDefaultSessionDir: () => true,
					},
				},
			};
			const originalExit = process.exit;
			process.exit = ((code?: number) => {
				trace.push(`exit:${code ?? 0}`);
				throw new Error("<fixture-exit>");
			}) as typeof process.exit;
			try {
				await method(modules, "shutdown").call(context);
			} catch (error) {
				if (!(error instanceof Error) || error.message !== "<fixture-exit>")
					throw error;
			} finally {
				process.exit = originalExit;
			}
			break;
		}
		default:
			throw new Error(`no deterministic visible-command scenario for ${name}`);
	}

	return {
		transition:
			typeof context.__transition === "string" ? context.__transition : null,
		trace,
		chat: renderChat(context, agentDir),
	};
}

const commandInputs = new Map<string, string>([
	["settings", "/settings"],
	["model", "/model fixture/model"],
	["scoped-models", "/scoped-models"],
	["export", '/export "<tmp>/session.jsonl" ignored'],
	["import", '/import "fixture path/session.jsonl" trailing'],
	["share", "/share"],
	["copy", "/copy"],
	["name", "/name fixture session"],
	["session", "/session"],
	["changelog", "/changelog"],
	["hotkeys", "/hotkeys"],
	["fork", "/fork"],
	["clone", "/clone"],
	["tree", "/tree"],
	["trust", "/trust"],
	["login", "/login missing-provider"],
	["logout", "/logout"],
	["new", "/new"],
	["compact", "/compact fixture instructions"],
	["resume", "/resume"],
	["reload", "/reload"],
	["quit", "/quit"],
]);

export async function generateF12VisibleCommands(
	upstreamRoot: string,
	outputRoot: string,
	upstreamCommit: string,
): Promise<void> {
	const modules = await loadModules(upstreamRoot);
	const slashCommands = await import(
		pathToFileURL(
			path.join(
				upstreamRoot,
				"packages/coding-agent/src/core/slash-commands.ts",
			),
		).href
	);
	const agentDir = await mkdtemp(
		path.join(tmpdir(), "pi-f12-visible-commands-"),
	);
	try {
		const commands: CommandFixture[] = [];
		for (const command of slashCommands.BUILTIN_SLASH_COMMANDS as Array<{
			name: string;
		}>) {
			const input = commandInputs.get(command.name);
			if (!input)
				throw new Error(
					`missing deterministic visible-command scenario for ${command.name}`,
				);
			const dispatch = await captureDispatch(modules, input);
			const behavior = await captureBehavior(modules, command.name, agentDir);
			commands.push({
				name: command.name,
				input,
				dispatchTrace: dispatch.trace,
				finalEditorText: dispatch.editorText,
				...behavior,
			});
		}
		if (commands.length !== commandInputs.size) {
			throw new Error(
				`visible command count ${commands.length} does not match scenarios ${commandInputs.size}`,
			);
		}

		const familyDir = path.join(outputRoot, "F12-visible-commands");
		await rm(familyDir, { recursive: true, force: true });
		await mkdir(familyDir, { recursive: true });
		await writeFile(
			path.join(familyDir, "cases.json"),
			`${JSON.stringify({ schemaVersion: 2, width: WIDTH, commands }, null, 2)}\n`,
		);
		await writeFile(
			path.join(familyDir, "manifest.json"),
			`${JSON.stringify(
				{
					family: "F12-visible-commands",
					upstreamCommit,
					generator: "conformance/extract/f12-visible-commands.ts",
					sources: [
						"packages/coding-agent/src/core/slash-commands.ts",
						"packages/coding-agent/src/modes/interactive/interactive-mode.ts",
						"packages/coding-agent/src/core/keybindings.ts",
						"packages/coding-agent/CHANGELOG.md",
						"packages/coding-agent/src/utils/changelog.ts",
						"packages/coding-agent/test/interactive-mode-import-command.test.ts",
						"packages/coding-agent/test/interactive-mode-clone-command.test.ts",
						"packages/coding-agent/test/interactive-mode-status.test.ts",
						"packages/coding-agent/test/session-selector-rename.test.ts",
						"packages/coding-agent/test/trust-selector.test.ts",
						"packages/coding-agent/test/suite/regressions/5943-session-start-notify.test.ts",
						"packages/coding-agent/test/suite/regressions/5080-signal-shutdown-extension-cleanup.test.ts",
					],
					files: ["cases.json"],
					metadata: {
						normalization: [
							"temporary paths replaced with <tmp> in every observation",
							"raw frame digests preserve terminal controls, carriage returns, trailing padding, and trailing empty lines",
							"readable frame lines additionally remove terminal controls and trailing render padding",
						],
						scenarios:
							"one deterministic no-network/no-credential/no-native-clipboard scenario per visible built-in command",
						divergences: [
							"/share uses upstream local HTML export behavior per docs/DECISIONS.md instead of upstream gist upload",
						],
					},
				},
				null,
				2,
			)}\n`,
		);
	} finally {
		await rm(agentDir, { recursive: true, force: true });
	}
}
