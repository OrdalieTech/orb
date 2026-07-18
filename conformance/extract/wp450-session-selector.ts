import { existsSync } from "node:fs";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

type SessionInfo = {
	path: string;
	id: string;
	cwd: string;
	name?: string;
	parentSessionPath?: string;
	created: Date;
	modified: Date;
	messageCount: number;
	firstMessage: string;
	allMessagesText: string;
};

const WIDTH = 100;

function stripTerminalControls(value: string): string {
	return value
		.replace(/\u001b\][^\u0007]*(?:\u0007|\u001b\\)/g, "")
		.replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, "")
		.replace(/\r/g, "");
}

function normalizeLines(lines: string[], fixtureRoot: string): string[] {
	const normalized = lines.map((line) => stripTerminalControls(line)
		.replaceAll(fixtureRoot, "<fixture>")
		.replaceAll(fixtureRoot.replaceAll("\\", "/"), "<fixture>")
		.replace(/\s+$/g, ""));
	while (normalized.length > 0 && normalized[normalized.length - 1] === "") normalized.pop();
	return normalized;
}

async function flush(): Promise<void> {
	for (let index = 0; index < 4; index++) {
		await Promise.resolve();
		await new Promise<void>((resolve) => setImmediate(resolve));
	}
}

function ids(sessions: SessionInfo[]): string[] {
	return sessions.map((session) => session.id);
}

export async function generateWP450SessionSelector(
	upstreamRoot: string,
	outputRoot: string,
	upstreamCommit: string,
): Promise<void> {
	const load = async (relativePath: string) => import(pathToFileURL(path.join(upstreamRoot, relativePath)).href);
	process.env.PI_PACKAGE_DIR = path.join(upstreamRoot, "packages/coding-agent");
	process.env.FORCE_COLOR = "3";
	const chalk = await load("node_modules/chalk/source/index.js");
	(chalk.default as { level: number }).level = 3;
	const tui = await load("packages/tui/src/index.ts");
	const theme = await load("packages/coding-agent/src/modes/interactive/theme/theme.ts");
	const keybindings = await load("packages/coding-agent/src/core/keybindings.ts");
	const selectorModule = await load("packages/coding-agent/src/modes/interactive/components/session-selector.ts");
	const search = await load("packages/coding-agent/src/modes/interactive/components/session-selector-search.ts");

	tui.setCapabilities({ images: null, trueColor: true, hyperlinks: false });
	theme.initTheme("dark");
	const bindings = new keybindings.KeybindingsManager({});
	tui.setKeybindings(bindings);

	const fixtureRoot = await mkdtemp(path.join(tmpdir(), "pi-session-selector-"));
	try {
		const now = Date.now();
		const file = async (name: string) => {
			const value = path.join(fixtureRoot, `${name}.jsonl`);
			await writeFile(value, "{}\n");
			return value;
		};
		const rootPath = await file("root");
		const childPath = await file("child");
		const incidentPath = await file("incident");
		const miscPath = await file("misc");
		const make = (value: Partial<SessionInfo> & Pick<SessionInfo, "path" | "id" | "modified" | "firstMessage" | "allMessagesText">): SessionInfo => ({
			cwd: path.join(fixtureRoot, value.id === "incident" ? "other" : "project"),
			created: new Date(now - 3_600_000),
			messageCount: 2,
			...value,
		});
		const root = make({
			path: rootPath, id: "root", name: "Root plan", modified: new Date(now - 20 * 60_000),
			firstMessage: "Plan the alpha rollout", allMessagesText: "alpha rollout details",
		});
		const child = make({
			path: childPath, id: "child", parentSessionPath: rootPath, modified: new Date(now - 5 * 60_000),
			firstMessage: "Investigate Node CVE", allMessagesText: "node cve remediation",
		});
		const incident = make({
			path: incidentPath, id: "incident", name: "Incident", modified: new Date(now - 60_000),
			firstMessage: "Alpha failure", allMessagesText: "alpha fatal error",
		});
		const misc = make({
			path: miscPath, id: "misc", modified: new Date(now - 30 * 60_000),
			firstMessage: "Misc notes", allMessagesText: "unrelated notes",
		});
		const current = [child, root, misc];
		const all = [incident, child, root, misc];

		const searches = [
			{ id: "recent-alpha", query: "alpha", sortMode: "recent", nameFilter: "all" },
			{ id: "fuzzy", query: "ndcv", sortMode: "relevance", nameFilter: "all" },
			{ id: "exact-phrase", query: '"node cve"', sortMode: "relevance", nameFilter: "all" },
			{ id: "regex", query: "re:alpha.*error", sortMode: "relevance", nameFilter: "all" },
			{ id: "invalid-regex", query: "re:[", sortMode: "relevance", nameFilter: "all" },
			{ id: "named", query: "", sortMode: "recent", nameFilter: "named" },
		].map((entry) => ({
			...entry,
			result: ids(search.filterAndSortSessions(all, entry.query, entry.sortMode, entry.nameFilter)),
		}));

		let releaseCurrent!: () => void;
		const currentLoader = (onProgress?: (loaded: number, total: number) => void) => new Promise<SessionInfo[]>((resolve) => {
			onProgress?.(1, current.length);
			releaseCurrent = () => {
				onProgress?.(current.length, current.length);
				resolve(current.filter((session) => existsSync(session.path)));
			};
		});
		const allLoader = async (onProgress?: (loaded: number, total: number) => void) => {
			onProgress?.(all.length, all.length);
			return all.filter((session) => existsSync(session.path));
		};
		const selections: string[] = [];
		let cancellations = 0;
		const selector = new selectorModule.SessionSelectorComponent(
			currentLoader,
			allLoader,
			(sessionPath: string) => selections.push(sessionPath),
			() => { cancellations++; },
			() => {},
			() => {},
			{ showRenameHint: false, keybindings: bindings },
		);
		const frames: { id: string; lines: string[] }[] = [];
		const capture = (id: string) => frames.push({ id, lines: normalizeLines(selector.render(WIDTH), fixtureRoot) });

		await flush();
		capture("loading-progress");
		releaseCurrent();
		await flush();
		capture("current-threaded");
		selector.handleInput("\t");
		await flush();
		capture("all-threaded");
		selector.handleInput("\u0013");
		capture("all-recent");
		selector.handleInput("\u0013");
		capture("all-relevance");
		selector.handleInput("ndcv");
		capture("fuzzy-search");
		selector.handleInput("\u0015");
		selector.handleInput('"node cve"');
		capture("exact-search");
		selector.handleInput("\u0015");
		selector.handleInput("re:alpha.*error");
		capture("regex-search");
		selector.handleInput("\u0015");
		selector.handleInput("re:[");
		capture("invalid-regex");
		selector.handleInput("\u0015");
		selector.handleInput("\u000e");
		capture("named-filter");
		selector.handleInput("\u0010");
		capture("path-toggle");
		selector.handleInput("\u0004");
		capture("delete-confirmation");
		selector.handleInput("\u001b");
		capture("delete-cancelled");
		selector.handleInput("\u0004");
		selector.handleInput("\r");
		for (let attempt = 0; attempt < 100 && existsSync(incidentPath); attempt++) {
			await new Promise<void>((resolve) => setTimeout(resolve, 5));
		}
		await flush();
		capture("after-delete");

		const selected = new selectorModule.SessionSelectorComponent(
			async () => current.filter((session) => existsSync(session.path)),
			allLoader,
			(sessionPath: string) => selections.push(sessionPath),
			() => { cancellations++; },
			() => {},
			() => {},
			{ showRenameHint: false, keybindings: bindings },
		);
		await flush();
		selected.handleInput("\u001b[B");
		selected.handleInput("\r");
		const cancelled = new selectorModule.SessionSelectorComponent(
			async () => current.filter((session) => existsSync(session.path)),
			allLoader,
			(sessionPath: string) => selections.push(sessionPath),
			() => { cancellations++; },
			() => {},
			() => {},
			{ showRenameHint: false, keybindings: bindings },
		);
		await flush();
		cancelled.handleInput("\u001b");

		const familyDir = path.join(outputRoot, "WP450-session-selector");
		await mkdir(familyDir, { recursive: true });
		await writeFile(path.join(familyDir, "selector.json"), `${JSON.stringify({
			schemaVersion: 1,
			width: WIDTH,
			searches,
			frames,
			callbacks: {
				selected: selections.map((value) => value.replaceAll(fixtureRoot, "<fixture>")),
				cancellations,
			},
		}, null, 2)}\n`);
		await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({
			family: "WP450-session-selector",
			upstreamCommit,
			generator: "conformance/extract/wp450-session-selector.ts",
			sources: [
				"packages/coding-agent/src/cli/session-picker.ts",
				"packages/coding-agent/src/modes/interactive/components/session-selector.ts",
				"packages/coding-agent/src/modes/interactive/components/session-selector-search.ts",
			],
			files: ["selector.json"],
			metadata: { width: WIDTH, normalization: ["strip terminal controls", "replace temporary root with <fixture>"], divergences: [] },
		}, null, 2)}\n`);
	} finally {
		await rm(fixtureRoot, { recursive: true, force: true });
		tui.resetCapabilitiesCache();
	}
}
