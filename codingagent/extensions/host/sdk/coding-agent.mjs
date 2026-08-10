// orb-extension-sdk: @earendil-works/pi-coding-agent surface.
// Pure symbols are ported from pi-coding-agent (pi 0.84.1, commit 53fa77cc,
// MIT © Mario Zechner), trimmed to the contracts published extensions
// exercise. The three protocol services (createAgentSession, ModelRuntime,
// ModelRegistry) call the host transport through internal/services.mjs;
// SessionManager / SettingsManager / DefaultResourceLoader / createCodingTools
// are thin handles whose semantics land in the Go session bridge. Every other
// upstream export throws OrbUnsupportedCapability on use.
import { homedir } from "node:os";
import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import manifest from "./sdk.json" with { type: "json" };
import { parseFrontmatter as parseFrontmatterInternal } from "./internal/frontmatter.mjs";
import { optionalServiceCall, serviceCall } from "./internal/services.mjs";
import { activeTheme } from "./internal/theme.mjs";
import { attachUnsupportedMethods, unsupported } from "./internal/unsupported.mjs";

const PKG = "coding-agent";
const SUPPORTED = manifest.modules[PKG].implemented;
const stub = (name) => unsupported(PKG, name, SUPPORTED);
const CAP_AGENT_SESSION = "agent_session_v1";
const CAP_MODEL_RUNTIME = "model_runtime_v1";

// ── Pure symbols ─────────────────────────────────────────────────────────────

/** Identity helper preserving tool parameter inference (upstream extensions/types.ts). */
export function defineTool(tool) {
	return tool;
}

function expandTildePath(path) {
	if (path === "~") return homedir();
	if (path.startsWith("~/")) return join(homedir(), path.slice(2));
	return path;
}

/** The agent config directory: $PI_CODING_AGENT_DIR (exported by the orb host) or ~/.pi/agent. */
export function getAgentDir() {
	const envDir = process.env.PI_CODING_AGENT_DIR;
	if (envDir) return expandTildePath(envDir);
	return join(homedir(), ".pi", "agent");
}

const EXT_TO_LANG = {
	ts: "typescript", tsx: "typescript", js: "javascript", jsx: "javascript",
	mjs: "javascript", cjs: "javascript", py: "python", rb: "ruby", rs: "rust",
	go: "go", java: "java", kt: "kotlin", swift: "swift", c: "c", h: "c",
	cpp: "cpp", cc: "cpp", cxx: "cpp", hpp: "cpp", cs: "csharp", php: "php",
	sh: "bash", bash: "bash", zsh: "bash", fish: "fish", ps1: "powershell",
	sql: "sql", html: "html", htm: "html", css: "css", scss: "scss",
	sass: "sass", less: "less", json: "json", yaml: "yaml", yml: "yaml",
	toml: "toml", xml: "xml", md: "markdown", markdown: "markdown",
	dockerfile: "dockerfile", makefile: "makefile", cmake: "cmake", lua: "lua",
	perl: "perl", r: "r", scala: "scala", clj: "clojure", ex: "elixir",
	exs: "elixir", erl: "erlang", hs: "haskell", ml: "ocaml", vim: "vim",
	graphql: "graphql", proto: "protobuf", tf: "hcl", hcl: "hcl",
};

/** Language identifier from a file path's extension, or undefined. */
export function getLanguageFromPath(filePath) {
	const ext = filePath.split(".").pop()?.toLowerCase();
	if (!ext) return undefined;
	return EXT_TO_LANG[ext];
}

/**
 * MarkdownTheme built from the active theme snapshot the host publishes under
 * the SDK theme global. highlightCode uses the theme's plain code-block color
 * (no syntax highlighter is bundled; extensions degrade exactly like upstream
 * does for unknown languages).
 */
export function getMarkdownTheme() {
	const theme = activeTheme();
	return {
		heading: (text) => theme.fg("mdHeading", text),
		link: (text) => theme.fg("mdLink", text),
		linkUrl: (text) => theme.fg("mdLinkUrl", text),
		code: (text) => theme.fg("mdCode", text),
		codeBlock: (text) => theme.fg("mdCodeBlock", text),
		codeBlockBorder: (text) => theme.fg("mdCodeBlockBorder", text),
		quote: (text) => theme.fg("mdQuote", text),
		quoteBorder: (text) => theme.fg("mdQuoteBorder", text),
		hr: (text) => theme.fg("mdHr", text),
		listBullet: (text) => theme.fg("mdListBullet", text),
		bold: (text) => theme.bold(text),
		italic: (text) => theme.italic(text),
		underline: (text) => theme.underline(text),
		strikethrough: (text) => `\x1b[9m${text}\x1b[29m`,
		highlightCode: (code, _lang) => code.split("\n").map((line) => theme.fg("mdCodeBlock", line)),
	};
}

/** Parse `---` YAML frontmatter; body is trimmed. Throws on malformed YAML. */
export function parseFrontmatter(content) {
	return parseFrontmatterInternal(content);
}

// Word-level diff for intra-line highlighting (replaces upstream's `diff`
// dependency): LCS over word/whitespace tokens.
function diffWords(oldText, newText) {
	const tokenize = (text) => text.split(/(\s+)/).filter((token) => token !== "");
	const a = tokenize(oldText);
	const b = tokenize(newText);
	const lcs = Array.from({ length: a.length + 1 }, () => new Array(b.length + 1).fill(0));
	for (let i = a.length - 1; i >= 0; i--) {
		for (let j = b.length - 1; j >= 0; j--) {
			lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
		}
	}
	const parts = [];
	const push = (value, kind) => {
		const last = parts[parts.length - 1];
		if (last && last.kind === kind) last.value += value;
		else parts.push({ value, kind });
	};
	let i = 0;
	let j = 0;
	while (i < a.length && j < b.length) {
		if (a[i] === b[j]) {
			push(a[i], "common");
			i++;
			j++;
		} else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
			push(a[i], "removed");
			i++;
		} else {
			push(b[j], "added");
			j++;
		}
	}
	while (i < a.length) push(a[i++], "removed");
	while (j < b.length) push(b[j++], "added");
	return parts;
}

function parseDiffLine(line) {
	const match = line.match(/^([+-\s])(\s*\d*)\s(.*)$/);
	if (!match) return null;
	return { prefix: match[1], lineNum: match[2], content: match[3] };
}

const replaceTabs = (text) => text.replace(/\t/g, "   ");

function renderIntraLineDiff(oldContent, newContent, theme) {
	let removedLine = "";
	let addedLine = "";
	let isFirstRemoved = true;
	let isFirstAdded = true;
	for (const part of diffWords(oldContent, newContent)) {
		if (part.kind === "removed") {
			let value = part.value;
			if (isFirstRemoved) {
				const leadingWs = value.match(/^(\s*)/)?.[1] || "";
				value = value.slice(leadingWs.length);
				removedLine += leadingWs;
				isFirstRemoved = false;
			}
			if (value) removedLine += theme.inverse(value);
		} else if (part.kind === "added") {
			let value = part.value;
			if (isFirstAdded) {
				const leadingWs = value.match(/^(\s*)/)?.[1] || "";
				value = value.slice(leadingWs.length);
				addedLine += leadingWs;
				isFirstAdded = false;
			}
			if (value) addedLine += theme.inverse(value);
		} else {
			removedLine += part.value;
			addedLine += part.value;
		}
	}
	return { removedLine, addedLine };
}

/**
 * Render a "+N/-N/ N content" diff string as one ANSI string: context dim,
 * removals red, additions green (theme toolDiff* colors), inverse on changed
 * tokens for single-line modifications.
 */
export function renderDiff(diffText, _options = {}) {
	const theme = activeTheme();
	const lines = diffText.split("\n");
	const result = [];
	let i = 0;
	while (i < lines.length) {
		const parsed = parseDiffLine(lines[i]);
		if (!parsed) {
			result.push(theme.fg("toolDiffContext", lines[i]));
			i++;
			continue;
		}
		if (parsed.prefix === "-") {
			const removedLines = [];
			while (i < lines.length) {
				const p = parseDiffLine(lines[i]);
				if (!p || p.prefix !== "-") break;
				removedLines.push({ lineNum: p.lineNum, content: p.content });
				i++;
			}
			const addedLines = [];
			while (i < lines.length) {
				const p = parseDiffLine(lines[i]);
				if (!p || p.prefix !== "+") break;
				addedLines.push({ lineNum: p.lineNum, content: p.content });
				i++;
			}
			if (removedLines.length === 1 && addedLines.length === 1) {
				const { removedLine, addedLine } = renderIntraLineDiff(
					replaceTabs(removedLines[0].content),
					replaceTabs(addedLines[0].content),
					theme,
				);
				result.push(theme.fg("toolDiffRemoved", `-${removedLines[0].lineNum} ${removedLine}`));
				result.push(theme.fg("toolDiffAdded", `+${addedLines[0].lineNum} ${addedLine}`));
			} else {
				for (const removed of removedLines) {
					result.push(theme.fg("toolDiffRemoved", `-${removed.lineNum} ${replaceTabs(removed.content)}`));
				}
				for (const added of addedLines) {
					result.push(theme.fg("toolDiffAdded", `+${added.lineNum} ${replaceTabs(added.content)}`));
				}
			}
		} else if (parsed.prefix === "+") {
			result.push(theme.fg("toolDiffAdded", `+${parsed.lineNum} ${replaceTabs(parsed.content)}`));
			i++;
		} else {
			result.push(theme.fg("toolDiffContext", ` ${parsed.lineNum} ${replaceTabs(parsed.content)}`));
			i++;
		}
	}
	return result.join("\n");
}

// ── Thin handles (fields are read by the Go session bridge; see services.mjs) ─

const BUILTIN_TOOL_NAMES = ["read", "bash", "edit", "write"];

// Upstream createCodingTools' system-prompt contribution: bash re-attaches
// its snippet + guideline after wrapToolDefinition, every other coding tool's
// contribution is dropped by the wrapper (pi core/tools/bash.ts:500-508). The
// session bridge applies these verbatim in place of the interactive built-in
// metadata.
const CODING_TOOL_PROMPTS = {
	bash: {
		promptSnippet: "Execute bash commands (ls, grep, find, etc.)",
		promptGuidelines: ["You can inspect PI_* environment variables for current model and session details."],
	},
};

/**
 * Name-bearing markers for the built-in coding tools. The session bridge
 * translates them to Go-native tools bound to `cwd` (tools capture their cwd
 * at construction); no JS execute exists.
 */
export function createCodingTools(cwd, options = {}) {
	return BUILTIN_TOOL_NAMES.map((name) => ({
		name,
		__orbBuiltinTool: name,
		cwd,
		options,
		...(CODING_TOOL_PROMPTS[name] ?? {}),
	}));
}

/** Inert settings handle; the Go side applies real settings at session creation. */
export class SettingsManager {
	constructor(kind, cwd, agentDir) {
		this.kind = kind;
		this.cwd = cwd;
		this.agentDir = agentDir;
	}

	static create(cwd, agentDir = getAgentDir(), _options = {}) {
		return new SettingsManager("create", resolve(cwd), resolve(expandTildePath(agentDir)));
	}

	static inMemory(_settings = {}, cwd = process.cwd(), agentDir = getAgentDir()) {
		return new SettingsManager("inMemory", resolve(cwd), resolve(expandTildePath(agentDir)));
	}
}
SettingsManager.fromStorage = stub("SettingsManager.fromStorage");
attachUnsupportedMethods(
	SettingsManager.prototype,
	PKG,
	"SettingsManager",
	[
		"getSettings", "getGlobalSettings", "updateGlobalSettings", "getProjectSettings",
		"updateProjectSettings", "getDefaultProvider", "getDefaultModel", "getDefaultThinkingLevel",
		"setDefaultModel", "setDefaultThinkingLevel", "getCompactionSettings", "getRetrySettings",
	],
	SUPPORTED,
);

/**
 * Resource-loader handle. `noExtensions: true` is the structural
 * anti-recursion guarantee: the bridged child session loads no extensions
 * (skills/prompts/context still load Go-side).
 */
export class DefaultResourceLoader {
	constructor(options = {}) {
		this.options = { ...options };
	}

	/**
	 * Resolves once host resources are (re)loaded. Calls the optional
	 * `resourceLoaderReload` transport seam when the services lane has bound
	 * it; until then this is a documented no-op — the Go bridge performs the
	 * real resource loading when the session is created.
	 */
	async reload(options) {
		const pending = optionalServiceCall("resourceLoaderReload", [this, options]);
		if (pending !== undefined) await pending;
	}
}
attachUnsupportedMethods(
	DefaultResourceLoader.prototype,
	PKG,
	"DefaultResourceLoader",
	["getExtensions", "getSkills", "getPrompts", "getThemes", "getAgentsFiles", "getExtensionErrors"],
	SUPPORTED,
);

function defaultSessionDirPath(cwd, agentDir = getAgentDir()) {
	const resolvedCwd = resolve(expandTildePath(cwd));
	const resolvedAgentDir = resolve(expandTildePath(agentDir));
	const safePath = `--${resolvedCwd.replace(/^[/\\]/, "").replace(/[/\\:]/g, "-")}--`;
	return join(resolvedAgentDir, "sessions", safePath);
}

/**
 * Session handle. `create()` eagerly creates a real, writable session
 * directory using the same path scheme as the Go side (extensions write-probe
 * it before any protocol round-trip); `inMemory()` mirrors upstream's empty
 * sessionDir. appendSessionInfo is best-effort and forwarded to the transport
 * when bound; queued names stay on `sessionInfoNames` for the bridge.
 */
export class SessionManager {
	constructor(kind, cwd, sessionDir, persist) {
		this.kind = kind;
		this.cwd = cwd;
		this.sessionDir = sessionDir;
		this.persist = persist;
		this.sessionInfoNames = [];
	}

	static create(cwd, sessionDir, _options = {}) {
		const resolvedCwd = resolve(expandTildePath(cwd));
		const dir = sessionDir ? resolve(expandTildePath(sessionDir)) : defaultSessionDirPath(resolvedCwd);
		mkdirSync(dir, { recursive: true });
		return new SessionManager("create", resolvedCwd, dir, true);
	}

	static inMemory(cwd = process.cwd(), _options = {}) {
		return new SessionManager("inMemory", resolve(expandTildePath(cwd)), "", false);
	}

	getSessionDir() {
		return this.sessionDir;
	}

	getCwd() {
		return this.cwd;
	}

	isPersisted() {
		return this.persist;
	}

	/** Append a session_info (display name) entry; best-effort, never load-bearing. */
	appendSessionInfo(name) {
		const sanitizedName = String(name).replace(/[\r\n]+/g, " ").trim();
		this.sessionInfoNames.push(sanitizedName);
		optionalServiceCall("sessionInfoAppend", [this, sanitizedName]);
		return `session-info-${this.sessionInfoNames.length}`;
	}
}
for (const name of ["open", "continueRecent", "forkFrom", "list", "listAll"]) {
	SessionManager[name] = stub(`SessionManager.${name}`);
}
attachUnsupportedMethods(
	SessionManager.prototype,
	PKG,
	"SessionManager",
	[
		"setSessionFile", "newSession", "buildSessionContext", "getEntries", "getBranch",
		"appendMessage", "appendCustomEntry", "appendCompaction", "appendLabel", "getSessionFile",
		"getSessionId", "getSessionName", "usesDefaultSessionDir", "branchFrom", "resetToMessage",
	],
	SUPPORTED,
);

// ── Protocol services (transport implemented by the services lane) ───────────

class AgentSessionProxy {
	#sessionId = null;
	#messages = [];
	#stats = null;
	#listeners = new Set();
	#disposed = false;
	// Upstream setActiveToolsByName applies synchronously, so extensions call
	// it fire-and-forget right before prompt(). The bridged call is a wire
	// round trip; prompt() drains pending mutations first to keep the
	// set-then-prompt ordering contract.
	#pendingMutations = [];

	/** Event sink handed to transport.agentSessionCreate; see services.mjs. */
	_events() {
		return {
			onEvent: (event) => {
				for (const listener of [...this.#listeners]) {
					try {
						listener(event);
					} catch {
						// Listener errors never tear the event stream.
					}
				}
			},
			onMessagesSnapshot: (messages) => {
				this.#messages = Array.isArray(messages) ? messages : [];
			},
			onMessageAppended: (message) => {
				this.#messages.push(message);
			},
			onMessageUpdated: (index, message) => {
				if (index >= 0 && index < this.#messages.length) this.#messages[index] = message;
				else this.#messages.push(message);
			},
			onStats: (stats) => {
				this.#stats = stats;
			},
		};
	}

	_bind(sessionId) {
		this.#sessionId = sessionId;
	}

	#call(method, args) {
		return serviceCall(CAP_AGENT_SESSION, method, PKG, "createAgentSession", SUPPORTED, [
			this.#sessionId,
			...args,
		]);
	}

	/** Live mirror of the session's message list (updated during prompt()). */
	get messages() {
		return this.#messages;
	}

	/** Resolves when the turn settles. Provider errors land in messages, not here. */
	async prompt(text, options) {
		if (this.#pendingMutations.length > 0) {
			const pending = this.#pendingMutations;
			this.#pendingMutations = [];
			await Promise.allSettled(pending);
		}
		await this.#call("agentSessionPrompt", [text, options]);
	}

	async abort() {
		await this.#call("agentSessionAbort", []);
	}

	subscribe(listener) {
		this.#listeners.add(listener);
		return () => {
			this.#listeners.delete(listener);
		};
	}

	getSessionStats() {
		return (
			this.#stats ?? {
				tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
				cost: 0,
			}
		);
	}

	async setActiveToolsByName(names) {
		const pending = this.#call("agentSessionSetActiveTools", [names]);
		this.#pendingMutations.push(Promise.resolve(pending).catch(() => {}));
		await pending;
	}

	async dispose() {
		if (this.#disposed) return;
		this.#disposed = true;
		this.#listeners.clear();
		await this.#call("agentSessionDispose", []);
	}
}

/**
 * Create a bridged child agent session (capability agent_session_v1).
 * Accepts CreateAgentSessionOptions; `customTools` execute in this process via
 * the transport's tool bridge. Returns { session, extensionsResult } — the
 * child session never loads extensions, so extensionsResult is an empty stub.
 *
 * Model tiers are not a session concept: upstream CreateAgentSessionOptions
 * has no tier field, and extensions (e.g. pi-dynamic-workflows) resolve tiers
 * client-side against the ModelRegistry into a concrete `model` object before
 * calling this. Nothing tier-shaped crosses the wire.
 *
 * session.getSessionStats() reads a mirror updated at each message end and
 * flushed once more before prompt() resolves — exact at settle, one message
 * behind while a response is still streaming (matching how extensions consume
 * it: usage is read after prompt() returns).
 */
export async function createAgentSession(options = {}) {
	const session = new AgentSessionProxy();
	const created = await serviceCall(CAP_AGENT_SESSION, "agentSessionCreate", PKG, "createAgentSession", SUPPORTED, [
		options,
		session._events(),
	]);
	session._bind(created.sessionId);
	const result = { session, extensionsResult: { extensions: [], errors: [] } };
	if (created.modelFallbackMessage) result.modelFallbackMessage = created.modelFallbackMessage;
	return result;
}

/**
 * Canonical model/auth runtime handle (capability model_runtime_v1), backed by
 * the Go-side catalog. Holds a synchronous catalog snapshot so the
 * ModelRegistry facade stays sync; getAvailable() refreshes it.
 */
export class ModelRuntime {
	constructor(runtimeId, catalog) {
		this.runtimeId = runtimeId;
		this.#catalog = normalizeCatalog(catalog);
	}

	#catalog;

	static async create(options = {}) {
		const created = await serviceCall(CAP_MODEL_RUNTIME, "modelRuntimeCreate", PKG, "ModelRuntime", SUPPORTED, [
			options,
		]);
		return new ModelRuntime(created.runtimeId, created.catalog);
	}

	async getAvailable(providerId, _options) {
		const catalog = await serviceCall(CAP_MODEL_RUNTIME, "modelRuntimeRefresh", PKG, "ModelRuntime", SUPPORTED, [
			this.runtimeId,
			providerId,
		]);
		this.#catalog = normalizeCatalog(catalog);
		const available = this.#catalog.available;
		return providerId ? available.filter((model) => model.provider === providerId) : available;
	}

	getAvailableSnapshot() {
		return this.#catalog.available;
	}

	getModels() {
		return this.#catalog.models;
	}

	getModel(providerId, modelId) {
		return this.#catalog.models.find((model) => model.provider === providerId && model.id === modelId);
	}

	hasConfiguredAuth(providerId) {
		return this.#catalog.authenticatedProviders.includes(providerId);
	}

	async dispose() {
		await optionalServiceCall("modelRuntimeDispose", [this.runtimeId]);
	}
}
attachUnsupportedMethods(
	ModelRuntime.prototype,
	PKG,
	"ModelRuntime",
	[
		"refresh", "getError", "getAuth", "getProvider", "getProviderAuthStatus", "complete",
		"registerProvider", "registerNativeProvider", "unregisterProvider", "isUsingOAuth",
		"getCompatibilityRequestConfig", "getRegisteredProviderConfig", "getRegisteredNativeProvider",
		"getRegisteredProviderIds",
	],
	SUPPORTED,
);

function normalizeCatalog(catalog) {
	return {
		models: Array.isArray(catalog?.models) ? catalog.models : [],
		available: Array.isArray(catalog?.available) ? catalog.available : [],
		authenticatedProviders: Array.isArray(catalog?.authenticatedProviders)
			? catalog.authenticatedProviders
			: [],
	};
}

/**
 * Synchronous compatibility facade over a ModelRuntime. Extensions read the
 * upstream-private `runtime` field through a cast (runtimeOf), so it is a
 * plain own property here — never a #private.
 */
export class ModelRegistry {
	constructor(runtime) {
		this.runtime = runtime;
	}

	getAll() {
		return [...this.runtime.getModels()];
	}

	getAvailable() {
		return [...this.runtime.getAvailableSnapshot()];
	}

	find(provider, modelId) {
		return this.runtime.getModel(provider, modelId);
	}

	hasConfiguredAuth(model) {
		return this.runtime.hasConfiguredAuth(model.provider);
	}
}
attachUnsupportedMethods(
	ModelRegistry.prototype,
	PKG,
	"ModelRegistry",
	[
		"refresh", "getError", "getApiKeyAndHeaders", "getProviderAuthStatus", "getProvider",
		"complete", "getProviderDisplayName", "getProviderAuth", "getApiKeyForProvider",
		"isUsingOAuth", "registerProvider", "unregisterProvider", "getRegisteredProviderConfig",
		"getRegisteredNativeProvider", "getRegisteredProviderIds",
	],
	SUPPORTED,
);

// ── Unsupported upstream exports (generated from pinned pi-coding-agent 0.84.1) ─
export const AgentSession = stub("AgentSession");
export const AgentSessionRuntime = stub("AgentSessionRuntime");
export const ArminComponent = stub("ArminComponent");
export const AssistantMessageComponent = stub("AssistantMessageComponent");
export const BashExecutionComponent = stub("BashExecutionComponent");
export const BorderedLoader = stub("BorderedLoader");
export const BranchSummaryMessageComponent = stub("BranchSummaryMessageComponent");
export const CONFIG_DIR_NAME = stub("CONFIG_DIR_NAME");
export const CURRENT_SESSION_VERSION = stub("CURRENT_SESSION_VERSION");
export const CompactionSummaryMessageComponent = stub("CompactionSummaryMessageComponent");
export const CredentialSynchronizationError = stub("CredentialSynchronizationError");
export const CustomEditor = stub("CustomEditor");
export const CustomMessageComponent = stub("CustomMessageComponent");
export const DEFAULT_COMPACTION_SETTINGS = stub("DEFAULT_COMPACTION_SETTINGS");
export const DEFAULT_MAX_BYTES = stub("DEFAULT_MAX_BYTES");
export const DEFAULT_MAX_LINES = stub("DEFAULT_MAX_LINES");
export const DefaultPackageManager = stub("DefaultPackageManager");
export const DynamicBorder = stub("DynamicBorder");
export const ExtensionEditorComponent = stub("ExtensionEditorComponent");
export const ExtensionInputComponent = stub("ExtensionInputComponent");
export const ExtensionRunner = stub("ExtensionRunner");
export const ExtensionSelectorComponent = stub("ExtensionSelectorComponent");
export const FooterComponent = stub("FooterComponent");
export const InteractiveMode = stub("InteractiveMode");
export const LoginDialogComponent = stub("LoginDialogComponent");
export const ModelSelectorComponent = stub("ModelSelectorComponent");
export const OAuthSelectorComponent = stub("OAuthSelectorComponent");
export const ProjectTrustStore = stub("ProjectTrustStore");
export const RpcClient = stub("RpcClient");
export const SessionSelectorComponent = stub("SessionSelectorComponent");
export const SettingsSelectorComponent = stub("SettingsSelectorComponent");
export const ShowImagesSelectorComponent = stub("ShowImagesSelectorComponent");
export const SkillInvocationMessageComponent = stub("SkillInvocationMessageComponent");
export const Theme = stub("Theme");
export const ThemeSelectorComponent = stub("ThemeSelectorComponent");
export const ThinkingSelectorComponent = stub("ThinkingSelectorComponent");
export const ToolExecutionComponent = stub("ToolExecutionComponent");
export const TreeSelectorComponent = stub("TreeSelectorComponent");
export const UserMessageComponent = stub("UserMessageComponent");
export const UserMessageSelectorComponent = stub("UserMessageSelectorComponent");
export const VERSION = stub("VERSION");
export const buildContextEntries = stub("buildContextEntries");
export const buildSessionContext = stub("buildSessionContext");
export const calculateContextTokens = stub("calculateContextTokens");
export const collectEntriesForBranchSummary = stub("collectEntriesForBranchSummary");
export const compact = stub("compact");
export const convertToLlm = stub("convertToLlm");
export const convertToPng = stub("convertToPng");
export const copyToClipboard = stub("copyToClipboard");
export const createAgentSessionFromServices = stub("createAgentSessionFromServices");
export const createAgentSessionRuntime = stub("createAgentSessionRuntime");
export const createAgentSessionServices = stub("createAgentSessionServices");
export const createBashTool = stub("createBashTool");
export const createBashToolDefinition = stub("createBashToolDefinition");
export const createEditTool = stub("createEditTool");
export const createEditToolDefinition = stub("createEditToolDefinition");
export const createEventBus = stub("createEventBus");
export const createExtensionRuntime = stub("createExtensionRuntime");
export const createFindTool = stub("createFindTool");
export const createFindToolDefinition = stub("createFindToolDefinition");
export const createGrepTool = stub("createGrepTool");
export const createGrepToolDefinition = stub("createGrepToolDefinition");
export const createLocalBashOperations = stub("createLocalBashOperations");
export const createLsTool = stub("createLsTool");
export const createLsToolDefinition = stub("createLsToolDefinition");
export const createReadOnlyTools = stub("createReadOnlyTools");
export const createReadTool = stub("createReadTool");
export const createReadToolDefinition = stub("createReadToolDefinition");
export const createSyntheticSourceInfo = stub("createSyntheticSourceInfo");
export const createWriteTool = stub("createWriteTool");
export const createWriteToolDefinition = stub("createWriteToolDefinition");
export const discoverAndLoadExtensions = stub("discoverAndLoadExtensions");
export const estimateTokens = stub("estimateTokens");
export const findCutPoint = stub("findCutPoint");
export const findTurnStartIndex = stub("findTurnStartIndex");
export const formatDimensionNote = stub("formatDimensionNote");
export const formatSize = stub("formatSize");
export const formatSkillsForPrompt = stub("formatSkillsForPrompt");
export const generateBranchSummary = stub("generateBranchSummary");
export const generateDiffString = stub("generateDiffString");
export const generateSummary = stub("generateSummary");
export const generateSummaryWithUsage = stub("generateSummaryWithUsage");
export const generateUnifiedPatch = stub("generateUnifiedPatch");
export const getDocsPath = stub("getDocsPath");
export const getExamplesPath = stub("getExamplesPath");
export const getLastAssistantUsage = stub("getLastAssistantUsage");
export const getLatestCompactionEntry = stub("getLatestCompactionEntry");
export const getPackageDir = stub("getPackageDir");
export const getReadmePath = stub("getReadmePath");
export const getSelectListTheme = stub("getSelectListTheme");
export const getSettingsListTheme = stub("getSettingsListTheme");
export const getShellConfig = stub("getShellConfig");
export const hasTrustRequiringProjectResources = stub("hasTrustRequiringProjectResources");
export const highlightCode = stub("highlightCode");
export const initTheme = stub("initTheme");
export const isBashToolResult = stub("isBashToolResult");
export const isEditToolResult = stub("isEditToolResult");
export const isFindToolResult = stub("isFindToolResult");
export const isGrepToolResult = stub("isGrepToolResult");
export const isLsToolResult = stub("isLsToolResult");
export const isReadToolResult = stub("isReadToolResult");
export const isToolCallEventType = stub("isToolCallEventType");
export const isWriteToolResult = stub("isWriteToolResult");
export const keyHint = stub("keyHint");
export const keyText = stub("keyText");
export const loadProjectContextFiles = stub("loadProjectContextFiles");
export const loadSkills = stub("loadSkills");
export const loadSkillsFromDir = stub("loadSkillsFromDir");
export const main = stub("main");
export const migrateSessionEntries = stub("migrateSessionEntries");
export const parseArgs = stub("parseArgs");
export const parseSessionEntries = stub("parseSessionEntries");
export const parseSkillBlock = stub("parseSkillBlock");
export const prepareBranchEntries = stub("prepareBranchEntries");
export const rawKeyHint = stub("rawKeyHint");
export const readStoredCredential = stub("readStoredCredential");
export const resizeImage = stub("resizeImage");
export const resolveCliModel = stub("resolveCliModel");
export const resolveModelScopeWithDiagnostics = stub("resolveModelScopeWithDiagnostics");
export const runPrintMode = stub("runPrintMode");
export const runRpcMode = stub("runRpcMode");
export const serializeConversation = stub("serializeConversation");
export const sessionEntryToContextMessages = stub("sessionEntryToContextMessages");
export const shouldCompact = stub("shouldCompact");
export const stripFrontmatter = stub("stripFrontmatter");
export const truncateHead = stub("truncateHead");
export const truncateLine = stub("truncateLine");
export const truncateTail = stub("truncateTail");
export const truncateToVisualLines = stub("truncateToVisualLines");
export const withFileMutationQueue = stub("withFileMutationQueue");
export const wrapRegisteredTool = stub("wrapRegisteredTool");
export const wrapRegisteredTools = stub("wrapRegisteredTools");
