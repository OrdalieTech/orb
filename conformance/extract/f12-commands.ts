import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

type VisibleCommand = {
	name: string;
	description: string;
	argumentHint: string | null;
	visible: true;
};

type HiddenCommand = {
	name: string;
	description: null;
	argumentHint: null;
	visible: false;
};

type UnexpectedArgumentProbe = {
	name: string;
	input: string;
	dispatchTrace: string[];
	finalEditorText: string;
};

type RawFrameSnapshot = {
	lineCount: number;
	sha256: string;
	lines: string[];
};

function stripTerminalControls(value: string): string {
	return value
		.replace(/\u001b\][^\u0007]*(?:\u0007|\u001b\\)/g, "")
		.replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, "")
		.replace(/\r/g, "");
}

function normalizeLines(
	lines: string[],
	replacements: Array<[string, string]> = [],
): string[] {
	const normalized = lines.map((line) => {
		let value = stripTerminalControls(line);
		for (const [from, to] of replacements) value = value.replaceAll(from, to);
		return value.replace(/\s+$/g, "");
	});
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

function rawFrameSnapshot(lines: string[]): RawFrameSnapshot {
	return {
		lineCount: lines.length,
		sha256: createHash("sha256").update(JSON.stringify(lines)).digest("hex"),
		lines,
	};
}

async function loadInteractiveModules(upstreamRoot: string) {
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
	const config = await load("packages/coding-agent/src/config.ts");
	const interactive = await load(
		"packages/coding-agent/src/modes/interactive/interactive-mode.ts",
	);
	theme.initTheme("dark");
	return { tui, config, InteractiveMode: interactive.InteractiveMode };
}

function interactiveHandler(
	modules: Awaited<ReturnType<typeof loadInteractiveModules>>,
	name: string,
) {
	const handler = Reflect.get(modules.InteractiveMode.prototype, name);
	if (typeof handler !== "function")
		throw new Error(`InteractiveMode.${name} not found`);
	return handler as (this: Record<string, unknown>) => void;
}

async function captureUnexpectedArgument(
	modules: Awaited<ReturnType<typeof loadInteractiveModules>>,
	name: string,
): Promise<UnexpectedArgumentProbe> {
	const input = `/${name} unexpected`;
	const trace: string[] = [];
	let editorText = input;
	const context = Object.create(modules.InteractiveMode.prototype) as Record<
		string,
		unknown
	>;
	context.defaultEditor = {};
	context.editor = {
		setText: (value: string) => {
			editorText = value;
			trace.push(`editor:${JSON.stringify(value)}`);
		},
		addToHistory: (value: string) =>
			trace.push(`history:${JSON.stringify(value)}`),
	};
	context.runtimeHost = {
		session: { isCompacting: false, isStreaming: false },
	};
	context.flushPendingBashComponents = () => {};
	context.onInputCallback = (value: string) =>
		trace.push(`input:${JSON.stringify(value)}`);
	context.pendingUserInputs = [];
	interactiveHandler(modules, "setupEditorSubmitHandler").call(context);
	const onSubmit = (
		context.defaultEditor as { onSubmit?: (value: string) => Promise<void> }
	).onSubmit;
	if (!onSubmit) throw new Error("upstream submit handler was not installed");
	await onSubmit(input);
	return { name, input, dispatchTrace: trace, finalEditorText: editorText };
}

async function captureDebugBehavior(
	modules: Awaited<ReturnType<typeof loadInteractiveModules>>,
): Promise<Record<string, unknown>> {
	const agentDir = await mkdtemp(path.join(tmpdir(), "pi-f12-debug-"));
	const previousAgentDir = process.env.PI_CODING_AGENT_DIR;
	process.env.PI_CODING_AGENT_DIR = agentDir;
	try {
		let requestRenders = 0;
		const chatContainer = new modules.tui.Container();
		const context: Record<string, unknown> = {
			ui: {
				terminal: { columns: 42, rows: 17 },
				render: () => ["debug <root>&\u2028\u2029", "wide 界"],
				requestRender: () => {
					requestRenders++;
				},
			},
			chatContainer,
			session: {
				messages: [
					{ role: "user", content: "debug <user>&\u2028\u2029", timestamp: 0 },
					{
						role: "custom",
						customType: "fixture",
						content: "debug custom",
						display: true,
					},
				],
			},
		};
		interactiveHandler(modules, "handleDebugCommand").call(context);
		const debugPath = path.join(agentDir, "pi-debug.log");
		const content = (await readFile(debugPath, "utf8"))
			.replace(/^Debug output at .*$/m, "Debug output at <timestamp>")
			.replaceAll(agentDir, "<agent-dir>");
		const rawChatFrame = replaceFramePaths(chatContainer.render(52), [
			[agentDir, "<agent-dir>"],
		]);
		return {
			terminal: { columns: 42, rows: 17 },
			path: "<agent-dir>/pi-debug.log",
			content,
			chatFrame: normalizeLines(rawChatFrame),
			rawChatFrame: rawFrameSnapshot(rawChatFrame),
			requestRenders,
		};
	} finally {
		if (previousAgentDir === undefined) delete process.env.PI_CODING_AGENT_DIR;
		else process.env.PI_CODING_AGENT_DIR = previousAgentDir;
		await rm(agentDir, { recursive: true, force: true });
	}
}

function captureArminBehavior(
	modules: Awaited<ReturnType<typeof loadInteractiveModules>>,
): Record<string, unknown> {
	const originalRandom = Math.random;
	const originalSetInterval = globalThis.setInterval;
	const originalClearInterval = globalThis.clearInterval;
	type FakeInterval = {
		callback: () => void;
		delayMs: number;
		active: boolean;
	};
	const intervals = new Map<number, FakeInterval>();
	let nextInterval = 1;
	let clearedIntervals = 0;
	Math.random = () => 0.2;
	globalThis.setInterval = ((
		callback: (...args: unknown[]) => void,
		delay?: number,
		...args: unknown[]
	) => {
		const handle = nextInterval++;
		intervals.set(handle, {
			callback: () => callback(...args),
			delayMs: Number(delay ?? 0),
			active: true,
		});
		return handle;
	}) as typeof setInterval;
	globalThis.clearInterval = ((handle: ReturnType<typeof setInterval>) => {
		const interval = intervals.get(Number(handle));
		if (!interval || !interval.active) return;
		interval.active = false;
		clearedIntervals++;
	}) as typeof clearInterval;

	try {
		let requestRenders = 0;
		const chatContainer = new modules.tui.Container();
		const context: Record<string, unknown> = {
			chatContainer,
			ui: {
				requestRender: () => {
					requestRenders++;
				},
			},
		};
		interactiveHandler(modules, "handleArminSaysHi").call(context);
		const component = chatContainer.children[1] as Record<string, unknown>;
		const interval = intervals.values().next().value as
			| FakeInterval
			| undefined;
		if (!interval)
			throw new Error("Armin animation interval was not scheduled");
		const frames: Array<{
			id: string;
			width: number;
			lines: string[];
			raw: RawFrameSnapshot;
		}> = [];
		const capture = (id: string) => {
			const rawLines = chatContainer.render(40);
			frames.push({
				id,
				width: 40,
				lines: normalizeLines(rawLines),
				raw: rawFrameSnapshot(rawLines),
			});
		};
		let ticks = 0;
		const tick = () => {
			if (!interval.active) return;
			interval.callback();
			ticks++;
		};
		capture("initial");
		tick();
		capture("tick-1");
		while (ticks < 9) tick();
		capture("tick-9");
		while (interval.active && ticks < 100) tick();
		capture("complete");
		(component as { dispose?: () => void }).dispose?.();
		return {
			effect: component.effect,
			intervalDelayMs: Math.round(interval.delayMs * 1000) / 1000,
			ticks,
			scheduledIntervals: intervals.size,
			clearedIntervals,
			requestRenders,
			frames,
		};
	} finally {
		Math.random = originalRandom;
		globalThis.setInterval = originalSetInterval;
		globalThis.clearInterval = originalClearInterval;
	}
}

async function captureDementedElvesBehavior(
	modules: Awaited<ReturnType<typeof loadInteractiveModules>>,
): Promise<Record<string, unknown>> {
	modules.tui.setCapabilities({
		images: null,
		trueColor: true,
		hyperlinks: false,
	});
	try {
		let requestRenders = 0;
		const chatContainer = new modules.tui.Container();
		const context: Record<string, unknown> = {
			chatContainer,
			ui: {
				requestRender: () => {
					requestRenders++;
				},
			},
		};
		interactiveHandler(modules, "handleDementedDelves").call(context);
		const assetPath = modules.config.getBundledInteractiveAssetPath(
			"clankolas.png",
		) as string;
		const asset = await readFile(assetPath);
		const dimensions = modules.tui.getImageDimensions(
			asset.toString("base64"),
			"image/png",
		);
		const announcement = chatContainer.children[1] as {
			children: Array<{ constructor: { name: string } }>;
		};
		const frames = [32, 80].map((width) => {
			const rawLines = chatContainer.render(width);
			return {
				width,
				lines: normalizeLines(rawLines),
				raw: rawFrameSnapshot(rawLines),
			};
		});
		return {
			requestRenders,
			bundledImage: {
				filename: "clankolas.png",
				mimeType: "image/png",
				byteLength: asset.length,
				sha256: createHash("sha256").update(asset).digest("hex"),
				dimensions,
				imageChildren: announcement.children.filter(
					(child) => child.constructor.name === "Image",
				).length,
				terminalCapability: null,
			},
			frames,
		};
	} finally {
		modules.tui.resetCapabilitiesCache();
	}
}

function dispatchedCommandNames(source: string): string[] {
	const methodStart = source.indexOf(
		"\tprivate setupEditorSubmitHandler(): void {",
	);
	if (methodStart < 0) throw new Error("interactive submit handler not found");
	const methodEnd = source.indexOf("\n\tprivate ", methodStart + 1);
	if (methodEnd < 0)
		throw new Error("interactive submit handler end not found");
	const method = source.slice(methodStart, methodEnd);
	const names: string[] = [];
	const seen = new Set<string>();
	for (const match of method.matchAll(/\btext === "\/([a-z0-9-]+)"/g)) {
		const name = match[1]!;
		if (seen.has(name)) continue;
		seen.add(name);
		names.push(name);
	}
	return names;
}

export async function generateF12Commands(
	upstreamRoot: string,
	outputRoot: string,
	upstreamCommit: string,
): Promise<void> {
	const slashCommandsPath = path.join(
		upstreamRoot,
		"packages/coding-agent/src/core/slash-commands.ts",
	);
	const slashCommands = await import(pathToFileURL(slashCommandsPath).href);
	const interactivePath = path.join(
		upstreamRoot,
		"packages/coding-agent/src/modes/interactive/interactive-mode.ts",
	);
	const interactiveSource = await readFile(interactivePath, "utf8");

	const visible: VisibleCommand[] = slashCommands.BUILTIN_SLASH_COMMANDS.map(
		(command: {
			name: string;
			description: string;
			argumentHint?: string;
		}) => ({
			name: command.name,
			description: command.description,
			argumentHint: command.argumentHint ?? null,
			visible: true,
		}),
	);
	const dispatch = dispatchedCommandNames(interactiveSource);
	const visibleNames = new Set(visible.map((command) => command.name));
	const hidden: HiddenCommand[] = dispatch
		.filter((name) => !visibleNames.has(name))
		.map((name) => ({
			name,
			description: null,
			argumentHint: null,
			visible: false,
		}));
	const missingDispatch = visible.filter(
		(command) => !dispatch.includes(command.name),
	);
	if (missingDispatch.length > 0) {
		throw new Error(
			`autocomplete commands missing from dispatch: ${missingDispatch.map((command) => command.name).join(", ")}`,
		);
	}
	const modules = await loadInteractiveModules(upstreamRoot);
	const argumentCommands = new Set([
		"model",
		"export",
		"import",
		"name",
		"login",
		"compact",
	]);
	const unexpectedArguments = await Promise.all(
		dispatch
			.filter((name) => !argumentCommands.has(name))
			.map((name) => captureUnexpectedArgument(modules, name)),
	);
	const behavior = {
		debug: await captureDebugBehavior(modules),
		arminSaysHi: captureArminBehavior(modules),
		dementedElves: await captureDementedElvesBehavior(modules),
	};

	const familyDir = path.join(outputRoot, "F12-commands");
	await rm(familyDir, { recursive: true, force: true });
	await mkdir(familyDir, { recursive: true });
	await writeFile(
		path.join(familyDir, "commands.json"),
		`${JSON.stringify(
			{
				schemaVersion: 4,
				visible,
				hidden,
				dispatch,
				unexpectedArguments,
				behavior,
			},
			null,
			2,
		)}\n`,
	);
	await writeFile(
		path.join(familyDir, "manifest.json"),
		`${JSON.stringify(
			{
				family: "F12-commands",
				upstreamCommit,
				generator: "conformance/extract/f12-commands.ts",
				sources: [
					"packages/coding-agent/src/core/slash-commands.ts",
					"packages/coding-agent/src/config.ts",
					"packages/coding-agent/src/modes/interactive/interactive-mode.ts",
					"packages/coding-agent/src/modes/interactive/components/armin.ts",
					"packages/coding-agent/src/modes/interactive/components/earendil-announcement.ts",
					"packages/coding-agent/src/modes/interactive/assets/clankolas.png",
					"packages/tui/src/components/image.ts",
					"packages/tui/src/terminal-image.ts",
				],
				files: ["commands.json"],
				metadata: {
					normalization: [
						"debug timestamp replaced with <timestamp>",
						"temporary PI_CODING_AGENT_DIR replaced with <agent-dir>",
						"raw frame digests preserve terminal controls, carriage returns, trailing padding, and trailing empty lines",
						"readable frame lines additionally remove terminal controls and trailing render padding",
					],
					visibility:
						"commands absent from BUILTIN_SLASH_COMMANDS but present in setupEditorSubmitHandler are hidden",
					argumentDispatch:
						"unexpected arguments to exact-only commands fall through as normal messages",
					divergences: [],
				},
			},
			null,
			2,
		)}\n`,
	);
}
