// Unit tests for the pure symbols of the embedded orb-extension-sdk.
// Run by TestSDKJavaScriptUnitTests (sdk_script_test.go) via `node --test`.
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import * as codingAgent from "../sdk/coding-agent.mjs";
import * as ai from "../sdk/ai.mjs";
import * as aiCompat from "../sdk/ai-compat.mjs";
import * as tui from "../sdk/tui.mjs";
import manifest from "../sdk/sdk.json" with { type: "json" };
import { bindTransport } from "../sdk/internal/services.mjs";

const THEME_GLOBAL = Symbol.for("@earendil-works/pi-coding-agent:theme");

function installMarkerTheme() {
	globalThis[THEME_GLOBAL] = {
		fg: (color, text) => `<${color}>${text}</>`,
		bg: (color, text) => `<bg:${color}>${text}</>`,
		bold: (text) => `<b>${text}</>`,
		italic: (text) => `<i>${text}</>`,
		underline: (text) => `<u>${text}</>`,
		inverse: (text) => `~${text}~`,
		strikethrough: (text) => `<s>${text}</>`,
	};
}

function clearTheme() {
	delete globalThis[THEME_GLOBAL];
}

// ── Export surface ───────────────────────────────────────────────────────────

test("modules expose every upstream runtime export name", () => {
	assert.equal(Object.keys(codingAgent).length, 151);
	assert.equal(Object.keys(ai).length, 48);
	assert.equal(Object.keys(aiCompat).length, 102);
	assert.equal(Object.keys(tui).length, 71);
	for (const name of manifest.modules["coding-agent"].implemented) {
		assert.ok(name in codingAgent, `coding-agent missing ${name}`);
	}
	for (const name of manifest.modules.tui.implemented) assert.ok(name in tui, `tui missing ${name}`);
	assert.ok("modelsAreEqual" in ai);
	// The compat surface re-exports the root implementation.
	assert.equal(aiCompat.modelsAreEqual, ai.modelsAreEqual);
});

test("unsupported exports are importable and throw the precise diagnostic on use", () => {
	assert.throws(() => codingAgent.compact(), (error) => {
		assert.equal(error.name, "OrbUnsupportedCapability");
		assert.match(
			String(error),
			/^OrbUnsupportedCapability: coding-agent#compact is not implemented by orb-extension-sdk 1\.0\.0; supported exports: /,
		);
		return true;
	});
	assert.throws(() => new tui.Editor(), /tui#Editor is not implemented/);
	assert.throws(() => ai.retryAssistantCall(), /ai#retryAssistantCall is not implemented/);
	assert.throws(() => aiCompat.streamSimple.anything, /ai#streamSimple is not implemented/);
	// Init-safety probes: inspection-adjacent keys must not throw.
	assert.equal(typeof tui.Box, "function");
	void tui.Box[Symbol.toPrimitive];
});

// ── parseFrontmatter ─────────────────────────────────────────────────────────

test("parseFrontmatter round-trips frontmatter and body", () => {
	const doc = [
		"---",
		"name: reviewer",
		'description: "Reads, then writes"',
		"tools: read, bash",
		"disallowedTools: [write, edit]",
		"model: anthropic/claude-4",
		"count: 3",
		"enabled: true",
		"steps:",
		"  - first",
		"  - second",
		"---",
		"",
		"Body text here.",
		"More body.",
	].join("\n");
	const { frontmatter, body } = codingAgent.parseFrontmatter(doc);
	assert.deepEqual(frontmatter, {
		name: "reviewer",
		description: "Reads, then writes",
		tools: "read, bash",
		disallowedTools: ["write", "edit"],
		model: "anthropic/claude-4",
		count: 3,
		enabled: true,
		steps: ["first", "second"],
	});
	assert.equal(body, "Body text here.\nMore body.");
});

test("parseFrontmatter without frontmatter returns the whole body", () => {
	const { frontmatter, body } = codingAgent.parseFrontmatter("just text\r\nwith crlf");
	assert.deepEqual(frontmatter, {});
	assert.equal(body, "just text\nwith crlf");
	const unterminated = codingAgent.parseFrontmatter("---\nkey: value\nno terminator");
	assert.deepEqual(unterminated.frontmatter, {});
});

// ── renderDiff ───────────────────────────────────────────────────────────────

test("renderDiff colors context/removed/added and inverses intra-line changes", () => {
	installMarkerTheme();
	try {
		const rendered = codingAgent.renderDiff(
			[" 1 unchanged line", "-2 the old value", "+2 the new value", " 3 mid", "+4 appended"].join("\n"),
		);
		const lines = rendered.split("\n");
		assert.equal(lines[0], "<toolDiffContext> 1 unchanged line</>");
		assert.equal(lines[1], "<toolDiffRemoved>-2 the ~old~ value</>");
		assert.equal(lines[2], "<toolDiffAdded>+2 the ~new~ value</>");
		assert.equal(lines[3], "<toolDiffContext> 3 mid</>");
		assert.equal(lines[4], "<toolDiffAdded>+4 appended</>");
		// Multi-line hunks render without intra-line inverse, exactly like upstream.
		const hunk = codingAgent.renderDiff(["-1 one", "+1 uno", "+2 dos"].join("\n")).split("\n");
		assert.equal(hunk[0], "<toolDiffRemoved>-1 one</>");
		assert.equal(hunk[1], "<toolDiffAdded>+1 uno</>");
		assert.equal(hunk[2], "<toolDiffAdded>+2 dos</>");
	} finally {
		clearTheme();
	}
});

test("renderDiff without a host theme degrades to plain text", () => {
	clearTheme();
	assert.equal(codingAgent.renderDiff("+1 added"), "+1 added");
});

// ── getMarkdownTheme ─────────────────────────────────────────────────────────

test("getMarkdownTheme styles through the host theme snapshot global", () => {
	installMarkerTheme();
	try {
		const markdownTheme = codingAgent.getMarkdownTheme();
		assert.equal(markdownTheme.heading("Title"), "<mdHeading>Title</>");
		assert.equal(markdownTheme.bold("x"), "<b>x</>");
		assert.deepEqual(markdownTheme.highlightCode("a\nb"), ["<mdCodeBlock>a</>", "<mdCodeBlock>b</>"]);
	} finally {
		clearTheme();
	}
});

// ── visibleWidth / truncateToWidth / wrapTextWithAnsi ────────────────────────

test("visibleWidth handles ASCII, ANSI, CJK, emoji, and tabs", () => {
	assert.equal(tui.visibleWidth(""), 0);
	assert.equal(tui.visibleWidth("hello"), 5);
	assert.equal(tui.visibleWidth("\x1b[31mred\x1b[0m"), 3);
	assert.equal(tui.visibleWidth("日本語"), 6);
	assert.equal(tui.visibleWidth("한글"), 4);
	assert.equal(tui.visibleWidth("👍"), 2);
	assert.equal(tui.visibleWidth("👩‍👩‍👧‍👦"), 2); // ZWJ family collapses to one cluster
	assert.equal(tui.visibleWidth("a\tb"), 5); // tab renders as 3 spaces
	assert.equal(tui.visibleWidth("mix日👍"), 3 + 2 + 2);
});

test("truncateToWidth truncates, pads, and stays ANSI-aware", () => {
	assert.equal(tui.truncateToWidth("hello", 10), "hello");
	assert.equal(tui.truncateToWidth("hello", 10, "...", true), "hello     ");
	assert.equal(tui.truncateToWidth("hello world", 8), "hello\x1b[0m...\x1b[0m");
	assert.equal(tui.truncateToWidth("hello world", 8, ""), "hello wo\x1b[0m");
	assert.equal(tui.truncateToWidth("", 4, "...", true), "    ");
	assert.equal(tui.truncateToWidth("abc", 0), "");
	// CJK: no torn double-width cell — 日本 (4 cols) + "..." fits in 7.
	assert.equal(tui.truncateToWidth("日本語です", 7), "日本\x1b[0m...\x1b[0m");
	// ANSI codes are preserved in the kept prefix and never counted.
	const colored = tui.truncateToWidth("\x1b[31mhello world\x1b[0m", 8);
	assert.ok(colored.startsWith("\x1b[31mhello"));
	assert.equal(tui.visibleWidth(colored), 8);
	// pad=true with "" ellipsis pads to exactly maxWidth (task-panel usage).
	assert.equal(tui.visibleWidth(tui.truncateToWidth("hi", 6, "", true)), 6);
});

test("wrapTextWithAnsi wraps words and carries ANSI state across lines", () => {
	assert.deepEqual(tui.wrapTextWithAnsi("", 10), [""]);
	assert.deepEqual(tui.wrapTextWithAnsi("one two three", 7), ["one two", "three"]);
	const wrapped = tui.wrapTextWithAnsi("\x1b[32mgreen text wraps here\x1b[0m", 11);
	assert.ok(wrapped.length > 1);
	for (const line of wrapped.slice(1)) assert.ok(line.startsWith("\x1b[32m"), `style lost: ${JSON.stringify(line)}`);
	// Long unbroken words break by grapheme.
	assert.deepEqual(tui.wrapTextWithAnsi("abcdefghij", 4), ["abcd", "efgh", "ij"]);
	// CJK breaks anywhere.
	assert.deepEqual(tui.wrapTextWithAnsi("日本語です", 4), ["日本", "語で", "す"]);
});

// ── parseKey ─────────────────────────────────────────────────────────────────

test("parseKey covers the navigator's consumed alphabet", () => {
	const table = {
		"\x1b[A": "up",
		"\x1b[B": "down",
		"\x1b[C": "right",
		"\x1b[D": "left",
		"\x1b[H": "home",
		"\x1bOF": "end",
		"\x1b[5~": "pageUp",
		"\x1b[6~": "pageDown",
		"\x1b[3~": "delete",
		"\r": "enter",
		"\n": "enter",
		"\x1b": "escape",
		"\t": "tab",
		" ": "space",
		"\x7f": "backspace",
		"\x15": "ctrl+u",
		"\x02": "ctrl+b",
		"\x04": "ctrl+d",
		"\x06": "ctrl+f",
		"\x03": "ctrl+c",
		g: "g",
		G: "G",
		k: "k",
		j: "j",
		q: "q",
		p: "p",
		x: "x",
		r: "r",
		s: "s",
		t: "t",
		"\x1b[Z": "shift+tab",
		"\x1b[1;5C": "ctrl+right",
		"\x1b[103;2u": "shift+g",
	};
	for (const [data, expected] of Object.entries(table)) {
		assert.equal(tui.parseKey(data), expected, `parseKey(${JSON.stringify(data)})`);
	}
	assert.equal(tui.parseKey("\x1b[999z"), undefined);
});

// ── Components ───────────────────────────────────────────────────────────────

const selectTheme = {
	selectedPrefix: (text) => text,
	selectedText: (text) => `[sel]${text}`,
	description: (text) => `[desc]${text}`,
	scrollInfo: (text) => `[scroll]${text}`,
	noMatch: (text) => `[nomatch]${text}`,
};

test("SelectList renders selection, scroll info, and reacts to default keys", () => {
	const items = [
		{ value: "alpha", label: "Alpha" },
		{ value: "beta", label: "Beta" },
		{ value: "gamma", label: "Gamma" },
	];
	const list = new tui.SelectList(items, 2, selectTheme);
	const selected = [];
	const cancelled = [];
	list.onSelect = (item) => selected.push(item.value);
	list.onCancel = () => cancelled.push(true);

	let lines = list.render(30);
	assert.equal(lines[0], "[sel]→ Alpha");
	assert.equal(lines[1], "  Beta");
	assert.equal(lines[2], "[scroll]  (1/3)");

	list.handleInput("\x1b[B"); // down
	assert.equal(list.getSelectedItem().value, "beta");
	list.handleInput("\x1b[A"); // up
	assert.equal(list.getSelectedItem().value, "alpha");
	list.handleInput("\x1b[A"); // wraps to bottom
	assert.equal(list.getSelectedItem().value, "gamma");

	list.setSelectedIndex(1);
	list.handleInput("\r"); // confirm
	assert.deepEqual(selected, ["beta"]);
	list.handleInput("\x1b"); // cancel
	assert.equal(cancelled.length, 1);

	lines = list.render(30);
	assert.deepEqual(lines.slice(0, 2), ["  Alpha", "[sel]→ Beta"]);
});

test("Container, Spacer, and Text compose into padded line arrays", () => {
	const container = new tui.Container();
	container.addChild(new tui.Text("hi", 1, 0));
	container.addChild(new tui.Spacer(2));
	const lines = container.render(6);
	assert.deepEqual(lines, [" hi   ", "", ""]);
	container.invalidate(); // must not throw with Spacer children
	assert.equal(new tui.Text("", 0, 1).render(4).length, 0);
});

test("Markdown renders through the MarkdownTheme style functions", () => {
	installMarkerTheme();
	try {
		const markdown = new tui.Markdown(
			"# Title\n\nplain **bold** `code`\n\n- item",
			0,
			0,
			codingAgent.getMarkdownTheme(),
		);
		const lines = markdown.render(40);
		assert.ok(lines[0].includes("<mdHeading># Title</>"));
		const body = lines.join("\n");
		assert.ok(body.includes("<b>bold</>"));
		assert.ok(body.includes("<mdCode>code</>"));
		assert.ok(body.includes("<mdListBullet>- </>item"));
	} finally {
		clearTheme();
	}
});

// ── Pure coding-agent helpers ────────────────────────────────────────────────

test("defineTool is identity; createCodingTools returns name markers bound to cwd", () => {
	const tool = { name: "custom", execute: async () => ({ content: [] }) };
	assert.equal(codingAgent.defineTool(tool), tool);
	const tools = codingAgent.createCodingTools("/some/cwd");
	assert.deepEqual(tools.map((entry) => entry.name), ["read", "bash", "edit", "write"]);
	for (const entry of tools) {
		assert.equal(entry.cwd, "/some/cwd");
		assert.equal(entry.__orbBuiltinTool, entry.name);
	}
});

test("getLanguageFromPath maps extensions and tolerates unknowns", () => {
	assert.equal(codingAgent.getLanguageFromPath("a/b.ts"), "typescript");
	assert.equal(codingAgent.getLanguageFromPath("x.yml"), "yaml");
	assert.equal(codingAgent.getLanguageFromPath("noext"), undefined);
	assert.equal(codingAgent.getLanguageFromPath("weird.zzz"), undefined);
});

test("modelsAreEqual compares only id and provider", () => {
	assert.equal(ai.modelsAreEqual({ id: "m", provider: "p" }, { id: "m", provider: "p", name: "x" }), true);
	assert.equal(ai.modelsAreEqual({ id: "m", provider: "p" }, { id: "m", provider: "q" }), false);
	assert.equal(ai.modelsAreEqual(null, { id: "m", provider: "p" }), false);
});

// ── Thin handles ─────────────────────────────────────────────────────────────

test("SessionManager.create yields a real writable session dir; inMemory does not", () => {
	const agentDir = mkdtempSync(join(tmpdir(), "orb-sdk-test-"));
	const previous = process.env.PI_CODING_AGENT_DIR;
	process.env.PI_CODING_AGENT_DIR = agentDir;
	try {
		const cwd = mkdtempSync(join(tmpdir(), "orb-sdk-cwd-"));
		const manager = codingAgent.SessionManager.create(cwd);
		const dir = manager.getSessionDir();
		assert.ok(statSync(dir).isDirectory());
		assert.ok(dir.startsWith(join(agentDir, "sessions")));
		assert.ok(/--.*--$/.test(dir));
		assert.ok(!dir.includes(cwd)); // slashes are dashed
		writeFileSync(join(dir, ".probe"), ""); // the plugin's write probe
		assert.equal(manager.persist, true);
		assert.equal(manager.getCwd(), cwd);
		assert.equal(typeof manager.appendSessionInfo("My run"), "string");
		assert.deepEqual(manager.sessionInfoNames, ["My run"]);

		const memory = codingAgent.SessionManager.inMemory();
		assert.equal(memory.getSessionDir(), "");
		assert.equal(memory.persist, false);
		rmSync(cwd, { recursive: true, force: true });
	} finally {
		if (previous === undefined) delete process.env.PI_CODING_AGENT_DIR;
		else process.env.PI_CODING_AGENT_DIR = previous;
		rmSync(agentDir, { recursive: true, force: true });
	}
});

test("SettingsManager and DefaultResourceLoader are inert handles", async () => {
	const settings = codingAgent.SettingsManager.create("/tmp", "/tmp/agent");
	assert.equal(settings.cwd, "/tmp");
	assert.equal(settings.agentDir, "/tmp/agent");
	assert.throws(() => settings.getSettings(), /SettingsManager\.getSettings is not implemented/);
	const loader = new codingAgent.DefaultResourceLoader({
		cwd: "/tmp",
		agentDir: "/tmp/agent",
		settingsManager: settings,
		noExtensions: true,
	});
	await loader.reload(); // documented no-op until the services lane binds the seam
	assert.equal(loader.options.noExtensions, true);
});

// ── Services seam ────────────────────────────────────────────────────────────

test("service exports throw the capability diagnostic when the transport is unbound", async () => {
	bindTransport(null);
	await assert.rejects(
		() => codingAgent.createAgentSession({ cwd: "/tmp" }),
		/coding-agent#createAgentSession is not implemented by orb-extension-sdk 1\.0\.0;.*requires host capability agent_session_v1/,
	);
	await assert.rejects(() => codingAgent.ModelRuntime.create({}), /requires host capability model_runtime_v1/);
});

test("service exports round-trip through a bound transport", async () => {
	const calls = [];
	let sink;
	const model = { id: "m1", provider: "prov", name: "Model One", cost: { output: 2 }, contextWindow: 128 };
	bindTransport({
		capabilities: ["agent_session_v1", "model_runtime_v1"],
		async agentSessionCreate(options, events) {
			calls.push(["create", options.cwd]);
			sink = events;
			return { sessionId: "s1" };
		},
		async agentSessionPrompt(sessionId, text) {
			calls.push(["prompt", sessionId, text]);
			sink.onMessageAppended({ role: "user", content: [{ type: "text", text }] });
			sink.onEvent({ type: "message_start" });
			sink.onMessageAppended({ role: "assistant", content: [], stopReason: "stop" });
			sink.onStats({ tokens: { input: 3, output: 5, cacheRead: 0, cacheWrite: 0, total: 8 }, cost: 0.01 });
			sink.onEvent({ type: "agent_end" });
		},
		async agentSessionAbort(sessionId) {
			calls.push(["abort", sessionId]);
		},
		async agentSessionSetActiveTools(sessionId, names) {
			calls.push(["tools", sessionId, names]);
		},
		async agentSessionDispose(sessionId) {
			calls.push(["dispose", sessionId]);
		},
		async modelRuntimeCreate(options) {
			calls.push(["runtimeCreate", options.authPath]);
			return {
				runtimeId: "r1",
				catalog: { models: [model], available: [model], authenticatedProviders: ["prov"] },
			};
		},
		async modelRuntimeRefresh(runtimeId) {
			calls.push(["runtimeRefresh", runtimeId]);
			return { models: [model], available: [model], authenticatedProviders: ["prov"] };
		},
	});
	try {
		const { session, extensionsResult } = await codingAgent.createAgentSession({ cwd: "/w" });
		assert.ok(extensionsResult);
		const seen = [];
		const unsubscribe = session.subscribe((event) => seen.push(event.type));
		await session.prompt("hello");
		assert.deepEqual(seen, ["message_start", "agent_end"]);
		assert.equal(session.messages.length, 2);
		assert.equal(session.messages[1].stopReason, "stop");
		assert.equal(session.getSessionStats().tokens.total, 8);
		await session.setActiveToolsByName(["read"]);
		unsubscribe();
		await session.abort();
		await session.dispose();

		const runtime = await codingAgent.ModelRuntime.create({ authPath: "/a/auth.json" });
		const available = await runtime.getAvailable();
		assert.deepEqual(available, [model]);
		assert.equal(runtime.hasConfiguredAuth("prov"), true);
		assert.equal(runtime.hasConfiguredAuth("other"), false);
		assert.equal(runtime.getModel("prov", "m1"), model);

		const registry = new codingAgent.ModelRegistry(runtime);
		// The plugin reads the upstream-private field through a cast (runtimeOf).
		assert.equal(registry.runtime, runtime);
		assert.ok(Object.hasOwn(registry, "runtime"));
		assert.deepEqual(registry.getAll(), [model]);
		assert.deepEqual(registry.getAvailable(), [model]);
		assert.equal(registry.hasConfiguredAuth(model), true);
		assert.equal(registry.find("prov", "m1"), model);
		assert.throws(() => registry.complete(), /ModelRegistry\.complete is not implemented/);

		assert.deepEqual(
			calls.map((entry) => entry[0]),
			["create", "prompt", "tools", "abort", "dispose", "runtimeCreate", "runtimeRefresh"],
		);
	} finally {
		bindTransport(null);
	}
});


test("image MIME file sniffing matches upstream format fixtures", async () => {
 const fixtures = JSON.parse(readFileSync(new URL("../../../../conformance/fixtures/WP440/images.json", import.meta.url), "utf8"));
 const root = mkdtempSync(join(tmpdir(), "orb-sdk-mime-"));
 try {
  for (const fixture of fixtures.formatCases) {
   const file = join(root, fixture.name);
   writeFileSync(file, Buffer.from(fixture.inputBase64, "base64"));
   assert.equal(await codingAgent.detectSupportedImageMimeTypeFromFile(file), fixture.detectedMimeType, fixture.name);
  }
 } finally { rmSync(root, {recursive:true, force:true}); }
});
