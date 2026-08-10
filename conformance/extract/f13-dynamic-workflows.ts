/**
 * F13-dynamic-workflows — hermetic reference behavior for
 * `@quintinshaw/pi-dynamic-workflows@3.5.1` running on the REAL pinned upstream
 * pi (.upstream, v0.84.1). The Orb implementation (orb-extension-sdk +
 * agent_session_v1/model_runtime_v1 bridges) is tested against these goldens.
 *
 * Hermeticity:
 *  - The plugin is installed by `npm ci` from an embedded, integrity-pinned
 *    lockfile (sha512 recorded in manifest.json) into a temp dir. Its
 *    `@earendil-works/pi-{coding-agent,ai,tui}` and `typebox` imports are
 *    resolved to the pinned `.upstream` sources via generated shim packages, so
 *    the plugin runs against the exact upstream commit — never a published SDK.
 *  - HOME and PI_CODING_AGENT_DIR point into the temp tree; nothing under the
 *    real ~/.pi is read or written.
 *  - All model calls go through upstream's faux provider, registered as a
 *    native provider on a real ModelRuntime with in-memory credentials.
 *  - `web_search`/`web_fetch` run against an in-process `fetch` stub.
 *  - Clocks are pinned (fixed Date, Math.random = 0); remaining ambient values
 *    (temp paths, UUIDs, PIDs) are canonicalized before goldens are written, so
 *    fixtures-check's regenerate-and-byte-diff passes.
 *  - Faux token accounting counts the serialized context (system prompt with
 *    cwd and PI_PACKAGE_DIR docs paths, message texts, tools JSON), so every
 *    machine-specific path root is rewritten to the same canonical
 *    placeholders BEFORE the provider estimates usage
 *    (canonicalizeTokenContext). Token counts in goldens therefore never
 *    encode where the extraction ran; the Orb replay harness mirrors the same
 *    rewrite over its own roots (f13_orb_harness_test.go).
 *
 * Scenario coverage (behavior goldens; cases/<name>.json):
 *  - foreground-basic ........ foreground run: phases, logs, journal, usage +
 *                              history events, provider-call mirror.
 *  - structured-output ....... structured_output tool capture (terminate:true),
 *                              schema repair re-prompt path, prose-JSON
 *                              recovery after exhausted repairs.
 *  - store-tools ............. custom JS tools store_put/store_get shared-store
 *                              roundtrip + per-agent journal storeDelta.
 *  - web-toolset ............. web-research toolset (coding tools + hermetic
 *                              web_search/web_fetch) tool calls/results.
 *  - agent-types ............. named agentType tool allowlist/denylist,
 *                              worktree isolation cwd mapping (real git
 *                              worktree), unknown-agentType fallback.
 *  - nested-workflow ......... nested/child workflow() run: runId namespacing,
 *                              shared store across nesting, runtime events.
 *  - cancellation ............ AbortSignal mid-run: in-flight subagent aborts,
 *                              WORKFLOW_ABORTED surfaces, no result delivered.
 *  - model-routing ........... tier routing via model-tiers.json, implicit
 *                              default-tier degrade (onModelFallback + log) and
 *                              explicit-pin MODEL_NOT_FOUND (#131 asymmetry).
 *  - background-lifecycle .... WorkflowManager background run: manager event
 *                              stream, persisted run state, provider-limit
 *                              pause (stopReason:"error" + errorMessage,
 *                              pauseReason usage_limit + resetHint), resume
 *                              with journal replay, stop() of a live run.
 *  - persist-agent-sessions .. persistAgentSessions transcript artifacts under
 *                              the standard sessions dir + appendSessionInfo.
 *  - extension-lifecycle ..... extensions/workflow.ts on a capture ExtensionAPI:
 *                              registration inventory, session_start wiring
 *                              (setActiveTools, model registry adoption),
 *                              workflow tool background execute + result
 *                              delivery flush (enqueue-before-session_start),
 *                              workflow_control prepareArguments + execute,
 *                              /new + /reload handoff across factory
 *                              generations, quit shutdown pausing live runs.
 *  - export-surface .......... full export-name inventory of the three upstream
 *                              packages + unsupported-export probes (upstream
 *                              serves them; Orb's stub diagnostic is asserted
 *                              by a Go test, not a golden).
 *
 * Reference-only TUI observations (reference-tui/*.json, D35): task panel
 * frames and the /workflows-models SelectList flow rendered by the pinned
 * upstream. Orb frame goldens are Orb-owned snapshots; these files exist for
 * human comparison and MUST NOT be wired up as byte-parity gates.
 */

import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readdir, readFile, rm, stat, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { promisify } from "node:util";

import { withOfflineGeneratedCatalog } from "./f3-agent.ts";

const execFileAsync = promisify(execFile);

const FIXED_NOW = 1_700_000_200_321;

const PLUGIN_NAME = "@quintinshaw/pi-dynamic-workflows";
const PLUGIN_VERSION = "3.5.1";
const PLUGIN_INTEGRITY =
  "sha512-YeIIJQpYpF5hPCQm2dlW6zsonZ82BTdelIh1J7TKmaoZkatiTKwM/yBSby7bZpatWQ2YbYdj+RfGEll+cXT8Hg==";
const ACORN_VERSION = "8.16.0";
const ACORN_INTEGRITY =
  "sha512-UVJyE9MttOsBQIDKw1skb9nAwQuR5wuGD3+82K6JgJlm/Y+KI92oNsMNGZCYdDsVtRHSak0pcV5Dno5+4jh9sw==";

const FAMILY = "F13-dynamic-workflows";

// ---------------------------------------------------------------------------
// determinism helpers
// ---------------------------------------------------------------------------

function withPinnedClock<T>(run: () => Promise<T>): Promise<T> {
  const NativeDate = Date;
  const nativeRandom = Math.random;
  class FixtureDate extends NativeDate {
    constructor(value?: string | number | Date) {
      super(value === undefined ? FIXED_NOW : value);
    }
    static now(): number {
      return FIXED_NOW;
    }
  }
  globalThis.Date = FixtureDate as DateConstructor;
  Math.random = () => 0;
  return run().finally(() => {
    globalThis.Date = NativeDate;
    Math.random = nativeRandom;
  });
}

/** Canonicalize machine-dependent strings inside a golden value. */
function makeCanonicalizer(roots: string[], extraReplacements: Array<[string, string]> = []): (value: unknown) => unknown {
  const replacements: Array<[string, string]> = [...extraReplacements];
  for (const root of roots) {
    replacements.push([root, "<fixture>"]);
    // Session dirs encode the cwd with path separators and dots dashed.
    replacements.push([root.replace(/[/.]/g, "-"), "<fixture-dash>"]);
  }
  const uuids = new Map<string, string>();
  const uuidRe = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi;
  const canonString = (input: string): string => {
    let out = input;
    for (const [from, to] of replacements) out = out.split(from).join(to);
    out = out.replace(uuidRe, (match) => {
      const key = match.toLowerCase();
      let alias = uuids.get(key);
      if (!alias) {
        alias = `<uuid-${uuids.size + 1}>`;
        uuids.set(key, alias);
      }
      return alias;
    });
    return out;
  };
  const canon = (value: unknown): unknown => {
    if (typeof value === "string") return canonString(value);
    if (Array.isArray(value)) return value.map(canon);
    if (value instanceof Error) {
      return canon({ name: value.name, message: value.message, ...value });
    }
    if (value && typeof value === "object") {
      return Object.fromEntries(
        Object.entries(value as Record<string, unknown>).map(([k, v]) => [canonString(k), canon(v)]),
      );
    }
    return value;
  };
  return canon;
}

function prettyJSON(value: unknown): string {
  return JSON.stringify(value, null, 2);
}

/**
 * Machine-specific path roots that can reach the faux provider's token
 * accounting: the child-session system prompt (cwd, worktree cwds,
 * PI_PACKAGE_DIR docs paths), message texts, and the serialized tools array.
 * Upstream faux estimates usage over the serialized context, so these are
 * rewritten to the family's canonical placeholders BEFORE the provider sees
 * the context — the counts baked into goldens must not depend on the temp
 * root or the checkout location. Set for the duration of a generate run; the
 * Orb harness applies the identical table over its own roots.
 */
let tokenCountReplacements: Array<[string, string]> = [];

function canonicalizeTokenText(input: string): string {
  let out = input;
  for (const [from, to] of tokenCountReplacements) out = out.split(from).join(to);
  return out;
}

/**
 * Canonical copy of a provider-call context for token counting only. The
 * faux provider serializes systemPrompt + message texts + JSON.stringify of
 * tools; replacing the path roots through a stringify/parse round trip is
 * byte-equivalent to replacing them inside that serialization (POSIX paths
 * and the placeholders contain no JSON-escaped characters).
 */
function canonicalizeTokenContext<T>(context: T): T {
  if (tokenCountReplacements.length === 0 || !context || typeof context !== "object") return context;
  const source = context as Record<string, unknown>;
  const canonical: Record<string, unknown> = { ...source };
  if (typeof source.systemPrompt === "string") {
    canonical.systemPrompt = canonicalizeTokenText(source.systemPrompt);
  }
  if (source.messages !== undefined) {
    canonical.messages = JSON.parse(canonicalizeTokenText(JSON.stringify(source.messages)));
  }
  if (source.tools !== undefined) {
    canonical.tools = JSON.parse(canonicalizeTokenText(JSON.stringify(source.tools)));
  }
  return canonical as T;
}

// ---------------------------------------------------------------------------
// hermetic plugin install
// ---------------------------------------------------------------------------

async function installPlugin(pluginRoot: string, upstreamRoot: string): Promise<string> {
  await mkdir(pluginRoot, { recursive: true });
  await writeFile(
    path.join(pluginRoot, "package.json"),
    `${JSON.stringify(
      {
        name: "orb-f13-plugin-fixture",
        version: "1.0.0",
        private: true,
        dependencies: { [PLUGIN_NAME]: PLUGIN_VERSION },
      },
      null,
      2,
    )}\n`,
  );
  await writeFile(
    path.join(pluginRoot, "package-lock.json"),
    `${JSON.stringify(
      {
        name: "orb-f13-plugin-fixture",
        version: "1.0.0",
        lockfileVersion: 3,
        requires: true,
        packages: {
          "": {
            name: "orb-f13-plugin-fixture",
            version: "1.0.0",
            dependencies: { [PLUGIN_NAME]: PLUGIN_VERSION },
          },
          [`node_modules/${PLUGIN_NAME}`]: {
            version: PLUGIN_VERSION,
            resolved: `https://registry.npmjs.org/${PLUGIN_NAME}/-/pi-dynamic-workflows-${PLUGIN_VERSION}.tgz`,
            integrity: PLUGIN_INTEGRITY,
            license: "MIT",
            dependencies: { acorn: "^8.16.0" },
            peerDependencies: {
              "@earendil-works/pi-coding-agent": ">=0.80.8",
              "@earendil-works/pi-tui": ">=0.80.6",
              typebox: "*",
            },
          },
          "node_modules/acorn": {
            version: ACORN_VERSION,
            resolved: `https://registry.npmjs.org/acorn/-/acorn-${ACORN_VERSION}.tgz`,
            integrity: ACORN_INTEGRITY,
            license: "MIT",
            bin: { acorn: "bin/acorn" },
            engines: { node: ">=0.4.0" },
          },
        },
      },
      null,
      2,
    )}\n`,
  );
  await execFileAsync(
    "npm",
    ["ci", "--ignore-scripts", "--no-audit", "--no-fund", "--legacy-peer-deps", "--prefer-offline"],
    {
      cwd: pluginRoot,
      env: { ...process.env, npm_config_update_notifier: "false" },
      maxBuffer: 16 * 1024 * 1024,
    },
  );

  // Resolve the plugin's pi SDK imports to the pinned upstream sources.
  const shim = async (name: string, target: string) => {
    const dir = path.join(pluginRoot, "node_modules", name);
    await mkdir(dir, { recursive: true });
    await writeFile(
      path.join(dir, "package.json"),
      `${JSON.stringify({ name, version: "0.0.0-upstream-shim", type: "module", main: "./index.ts" }, null, 2)}\n`,
    );
    await writeFile(path.join(dir, "index.ts"), `export * from ${JSON.stringify(pathToFileURL(target).href)};\n`);
  };
  await shim("@earendil-works/pi-coding-agent", path.join(upstreamRoot, "packages/coding-agent/src/index.ts"));
  await shim("@earendil-works/pi-ai", path.join(upstreamRoot, "packages/ai/src/index.ts"));
  await shim("@earendil-works/pi-tui", path.join(upstreamRoot, "packages/tui/src/index.ts"));
  await symlink(
    path.join(upstreamRoot, "node_modules", "typebox"),
    path.join(pluginRoot, "node_modules", "typebox"),
    "dir",
  );
  return path.join(pluginRoot, "node_modules", PLUGIN_NAME);
}

// ---------------------------------------------------------------------------
// upstream + plugin module loading
// ---------------------------------------------------------------------------

interface Loaded {
  upstream: {
    faux: any;
    authStorage: any;
    modelRuntime: any;
    modelRegistry: any;
    sessionManager: any;
    settingsManager: any;
    codingAgent: any;
    tui: any;
    ai: any;
    theme: any;
  };
  plugin: {
    root: string;
    agent: any;
    workflow: any;
    workflowManager: any;
    sharedStore: any;
    webTools: any;
    workflowTool: any;
    workflowControlTool: any;
    taskPanel: any;
    workflowsModelsCommand: any;
    modelTierConfig: any;
    extensionFactory: (api: unknown) => void;
  };
}

async function loadModules(upstreamRoot: string, pluginPackageRoot: string): Promise<Loaded> {
  const up = (rel: string) => import(pathToFileURL(path.join(upstreamRoot, rel)).href);
  const pl = (rel: string) => import(pathToFileURL(path.join(pluginPackageRoot, rel)).href);
  const [
    faux,
    authStorage,
    modelRuntime,
    modelRegistry,
    sessionManager,
    settingsManager,
    codingAgent,
    tui,
    ai,
    theme,
  ] = await Promise.all([
    up("packages/ai/src/providers/faux.ts"),
    up("packages/coding-agent/src/core/auth-storage.ts"),
    up("packages/coding-agent/src/core/model-runtime.ts"),
    up("packages/coding-agent/src/core/model-registry.ts"),
    up("packages/coding-agent/src/core/session-manager.ts"),
    up("packages/coding-agent/src/core/settings-manager.ts"),
    up("packages/coding-agent/src/index.ts"),
    up("packages/tui/src/index.ts"),
    up("packages/ai/src/index.ts"),
    up("packages/coding-agent/src/modes/interactive/theme/theme.ts"),
  ]);
  const [
    agent,
    workflow,
    workflowManager,
    sharedStore,
    webTools,
    workflowTool,
    workflowControlTool,
    taskPanel,
    workflowsModelsCommand,
    modelTierConfig,
    extensionModule,
  ] = await Promise.all([
    pl("src/agent.ts"),
    pl("src/workflow.ts"),
    pl("src/workflow-manager.ts"),
    pl("src/shared-store.ts"),
    pl("src/web-tools.ts"),
    pl("src/workflow-tool.ts"),
    pl("src/workflow-control-tool.ts"),
    pl("src/task-panel.ts"),
    pl("src/workflows-models-command.ts"),
    pl("src/model-tier-config.ts"),
    pl("extensions/workflow.ts"),
  ]);
  return {
    upstream: {
      faux,
      authStorage,
      modelRuntime,
      modelRegistry,
      sessionManager,
      settingsManager,
      codingAgent,
      tui,
      ai,
      theme,
    },
    plugin: {
      root: pluginPackageRoot,
      agent,
      workflow,
      workflowManager,
      sharedStore,
      webTools,
      workflowTool,
      workflowControlTool,
      taskPanel,
      workflowsModelsCommand,
      modelTierConfig,
      extensionFactory: extensionModule.default,
    },
  };
}

// ---------------------------------------------------------------------------
// faux model runtime
// ---------------------------------------------------------------------------

interface ProviderCallRecord {
  model: string;
  toolNames: string[];
  messages: unknown[];
}

interface FauxWorld {
  handle: any;
  runtime: any;
  registry: any;
  providerCalls: ProviderCallRecord[];
  setResponses(responses: unknown[]): void;
  pendingResponses(): number;
  model(id?: string): any;
}

function projectMessage(message: any): unknown {
  const role = message?.role;
  if (role === "toolResult") {
    return {
      role,
      toolName: message.toolName,
      isError: message.isError ?? false,
      text: Array.isArray(message.content)
        ? message.content
            .filter((part: any) => part?.type === "text")
            .map((part: any) => part.text)
            .join("")
        : String(message.content ?? ""),
    };
  }
  const content = message?.content;
  if (typeof content === "string") return { role, text: content };
  if (Array.isArray(content)) {
    return {
      role,
      parts: content.map((part: any) => {
        if (part?.type === "text") return { type: "text", text: part.text };
        if (part?.type === "toolCall") return { type: "toolCall", name: part.name, arguments: part.arguments };
        return { type: part?.type ?? "unknown" };
      }),
      ...(message?.stopReason ? { stopReason: message.stopReason } : {}),
      ...(message?.errorMessage ? { errorMessage: message.errorMessage } : {}),
    };
  }
  return { role };
}

async function createFauxWorld(loaded: Loaded, options: { models?: any[] } = {}): Promise<FauxWorld> {
  const { faux, authStorage, modelRuntime, modelRegistry } = loaded.upstream;
  const handle = faux.fauxProvider({
    api: "faux",
    provider: "faux",
    tokenSize: { min: 64, max: 64 },
    models: options.models ?? [
      {
        id: "faux-model",
        name: "Faux Model",
        reasoning: false,
        input: ["text"],
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
        contextWindow: 128000,
        maxTokens: 16384,
      },
    ],
  });
  const providerCalls: ProviderCallRecord[] = [];
  const innerStreamSimple = handle.provider.streamSimple.bind(handle.provider);
  const recordingProvider = {
    ...handle.provider,
    streamSimple: (model: any, context: any, streamOptions: any) => {
      providerCalls.push({
        model: `${model.provider}/${model.id}`,
        toolNames: (context?.tools ?? []).map((tool: any) => tool.name),
        messages: (context?.messages ?? []).map(projectMessage),
      });
      // Faux token accounting counts the serialized context; hand it a
      // canonical copy so usage never encodes machine-specific paths.
      return innerStreamSimple(model, canonicalizeTokenContext(context), streamOptions);
    },
  };
  const credentials = authStorage.AuthStorage.inMemory();
  await credentials.modify("faux", async () => ({ type: "api_key", key: "faux-key" }));
  const runtime = await modelRuntime.ModelRuntime.create({
    credentials,
    modelsPath: null,
    allowModelNetwork: false,
  });
  runtime.registerNativeProvider(recordingProvider);
  await runtime.refresh({ allowNetwork: false });
  await runtime.getAvailable().catch(() => {});
  const registry = new modelRegistry.ModelRegistry(runtime);
  return {
    handle,
    runtime,
    registry,
    providerCalls,
    setResponses: (responses) => handle.setResponses(responses),
    pendingResponses: () => handle.getPendingResponseCount(),
    model: (id = "faux-model") => handle.models.find((m: any) => m.id === id) ?? handle.getModel(),
  };
}

// ---------------------------------------------------------------------------
// event capture for runWorkflow scenarios
// ---------------------------------------------------------------------------

interface WorkflowCapture {
  events: unknown[];
  journal: unknown[];
  histories: Record<string, unknown>;
  options: Record<string, unknown>;
}

function captureWorkflowOptions(): WorkflowCapture {
  const events: unknown[] = [];
  const journal: unknown[] = [];
  const histories: Record<string, unknown> = {};
  const options: Record<string, unknown> = {
    onLog: (message: string) => events.push({ event: "log", message }),
    onPhase: (title: string) => events.push({ event: "phase", title }),
    onRuntimeEvent: (event: unknown) => events.push({ event: "runtime", ...(event as object) }),
    onAgentStart: (event: unknown) => events.push({ event: "agentStart", ...(event as object) }),
    onAgentEnd: (event: unknown) => events.push({ event: "agentEnd", ...(event as object) }),
    onRetrySpend: (tokens: number) => events.push({ event: "retrySpend", tokens }),
    onTokenUsage: (usage: unknown) => events.push({ event: "tokenUsage", usage }),
    onAgentJournal: (entry: any) => journal.push(entry),
    onAgentHistory: (event: any) => {
      histories[`${event.id} ${event.label}`] = event.history;
    },
  };
  return { events, journal, histories, options };
}

// ---------------------------------------------------------------------------
// scenarios
// ---------------------------------------------------------------------------

interface ScenarioContext {
  loaded: Loaded;
  project: string;
  agentDir: string;
  home: string;
  canon: (value: unknown) => unknown;
}

type ScenarioResult = Record<string, unknown>;

const SCRIPTS: Record<string, string> = {
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

function text(loadedFaux: any, body: string) {
  return loadedFaux.fauxAssistantMessage(body, { timestamp: FIXED_NOW });
}

function toolUse(loadedFaux: any, calls: Array<[string, unknown, string]>, leadText?: string) {
  const content = [
    ...(leadText ? [loadedFaux.fauxText(leadText)] : []),
    ...calls.map(([name, args, id]) => loadedFaux.fauxToolCall(name, args, { id })),
  ];
  return loadedFaux.fauxAssistantMessage(content, { stopReason: "toolUse", timestamp: FIXED_NOW });
}

async function runWorkflowScenario(
  ctx: ScenarioContext,
  name: string,
  configure: (world: FauxWorld, capture: WorkflowCapture) => Record<string, unknown>,
  worldOptions: { models?: any[] } = {},
): Promise<ScenarioResult> {
  const { loaded } = ctx;
  const world = await createFauxWorld(loaded, worldOptions);
  const capture = captureWorkflowOptions();
  const extraOptions = configure(world, capture);
  const runOptions = {
    cwd: ctx.project,
    runId: `run-fixed-${name}`,
    concurrency: 1,
    mainModel: "faux/faux-model",
    modelRegistry: world.registry,
    persistLogs: false,
    ...capture.options,
    ...extraOptions,
  };
  let result: unknown;
  let error: unknown;
  try {
    result = await loaded.plugin.workflow.runWorkflow(SCRIPTS[name], runOptions);
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
    providerCalls: world.providerCalls,
    pendingFauxResponses: world.pendingResponses(),
  };
}

// ---------------------------------------------------------------------------
// entry point
// ---------------------------------------------------------------------------

export async function generateF13DynamicWorkflows(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  const tmpRoot = await mkdtemp(path.join(tmpdir(), "orb-f13-"));
  const home = path.join(tmpRoot, "home");
  const agentDir = path.join(home, ".pi", "agent");
  const project = path.join(tmpRoot, "project");
  const pluginRoot = path.join(tmpRoot, "plugin");
  const savedEnv = {
    HOME: process.env.HOME,
    PI_CODING_AGENT_DIR: process.env.PI_CODING_AGENT_DIR,
    XDG_CONFIG_HOME: process.env.XDG_CONFIG_HOME,
    PI_PACKAGE_DIR: process.env.PI_PACKAGE_DIR,
  };
  try {
    await mkdir(agentDir, { recursive: true });
    await mkdir(project, { recursive: true });
    const pluginPackageRoot = await installPlugin(pluginRoot, upstreamRoot);

    // Project fixture tree: marker file + named agent definitions + git repo
    // (worktree isolation needs one).
    await writeFile(path.join(project, "marker.txt"), "marker-from-repo\n");
    await mkdir(path.join(project, ".pi", "agents"), { recursive: true });
    await writeFile(
      path.join(project, ".pi", "agents", "reader.md"),
      "---\nname: reader\ndescription: read-only scout\ntools: read\n---\nOnly read files; never modify anything.\n",
    );
    await writeFile(
      path.join(project, ".pi", "agents", "isolated.md"),
      "---\nname: isolated\ndescription: worktree-isolated worker\ntools: read, bash\ndisallowedTools: bash\nisolation: worktree\n---\nWork inside your isolated worktree only.\n",
    );
    const git = (...args: string[]) =>
      execFileAsync("git", ["-C", project, ...args], {
        env: {
          ...process.env,
          GIT_AUTHOR_NAME: "fixture",
          GIT_AUTHOR_EMAIL: "fixture@example.invalid",
          GIT_COMMITTER_NAME: "fixture",
          GIT_COMMITTER_EMAIL: "fixture@example.invalid",
          GIT_AUTHOR_DATE: "2026-02-03T04:05:06Z",
          GIT_COMMITTER_DATE: "2026-02-03T04:05:06Z",
          GIT_CONFIG_GLOBAL: "/dev/null",
          GIT_CONFIG_SYSTEM: "/dev/null",
        },
      });
    await git("init", "--quiet", "--initial-branch=main");
    await git("add", "marker.txt");
    await git("commit", "--quiet", "-m", "fixture");

    // Subagent sessions must resolve the faux default model through the real
    // settings path.
    await writeFile(
      path.join(agentDir, "settings.json"),
      `${JSON.stringify({ defaultProvider: "faux", defaultModel: "faux-model" }, null, 2)}\n`,
    );

    process.env.HOME = home;
    process.env.PI_CODING_AGENT_DIR = agentDir;
    delete process.env.XDG_CONFIG_HOME;
    // Pin the package dir explicitly (the F12 extractors happen to leave the
    // same value behind, but F13's faux token accounting depends on it: the
    // child-session system prompt embeds the docs paths derived from it).
    const packageDir = path.join(upstreamRoot, "packages", "coding-agent");
    process.env.PI_PACKAGE_DIR = packageDir;
    // Match the F12 extractors' explicit styling state so rendered frames are
    // identical whether this family runs standalone or after them in
    // generate.ts (they leave FORCE_COLOR=3 and an initialized theme behind).
    process.env.FORCE_COLOR = "3";

    // Workflow project state is keyed by `<basename>-<sha256(cwd)[:12]>`
    // (workflow-paths.ts); the hash is temp-path-dependent, so alias it.
    const projectKeyHash = createHash("sha256").update(project).digest("hex").slice(0, 12);
    // Path replacements shared by the golden canonicalizer and the faux
    // token-count canonicalizer. Order matters: packageDir is a child of
    // upstreamRoot and must be rewritten first.
    const pathReplacements: Array<[string, string]> = [
      [projectKeyHash, "<cwdhash>"],
      [packageDir, "<pi-package>"],
      [upstreamRoot, "<upstream>"],
    ];
    const canon = makeCanonicalizer([tmpRoot], pathReplacements);
    tokenCountReplacements = [
      [tmpRoot, "<fixture>"],
      [tmpRoot.replace(/[/.]/g, "-"), "<fixture-dash>"],
      ...pathReplacements,
    ];

    await withOfflineGeneratedCatalog(upstreamRoot, async () => {
      const loaded = await loadModules(upstreamRoot, pluginPackageRoot);
      loaded.upstream.theme.initTheme("dark");
      const ctx: ScenarioContext = { loaded, project, agentDir, home, canon };
      const cases = new Map<string, ScenarioResult>();
      const referenceTui = new Map<string, unknown>();

      await withPinnedClock(async () => {
        cases.set("foreground-basic", await scenarioForegroundBasic(ctx));
        cases.set("structured-output", await scenarioStructuredOutput(ctx));
        cases.set("store-tools", await scenarioStoreTools(ctx));
        cases.set("web-toolset", await scenarioWebToolset(ctx));
        cases.set("agent-types", await scenarioAgentTypes(ctx));
        cases.set("nested-workflow", await scenarioNestedWorkflow(ctx));
        cases.set("cancellation", await scenarioCancellation(ctx));
        cases.set("model-routing", await scenarioModelRouting(ctx));
        const background = await scenarioBackgroundLifecycle(ctx);
        cases.set("background-lifecycle", background.observations);
        referenceTui.set("task-panel", background.taskPanelFrames);
        cases.set("persist-agent-sessions", await scenarioPersistAgentSessions(ctx));
        cases.set("extension-lifecycle", await scenarioExtensionLifecycle(ctx));
        const models = await scenarioWorkflowsModels(ctx);
        cases.set("workflows-models", models.observations);
        referenceTui.set("workflows-models-select", models.frames);
      });
      cases.set("export-surface", await scenarioExportSurface(ctx));

      const familyDir = path.join(outputRoot, FAMILY);
      await rm(familyDir, { recursive: true, force: true });
      await mkdir(path.join(familyDir, "cases"), { recursive: true });
      await mkdir(path.join(familyDir, "reference-tui"), { recursive: true });

      const caseFiles: string[] = [];
      for (const [name, value] of cases) {
        const file = `cases/${name}.json`;
        caseFiles.push(file);
        await writeFile(path.join(familyDir, file), `${prettyJSON(canon({ schemaVersion: 1, scenario: name, ...value }))}\n`);
      }
      const referenceFiles: string[] = [];
      for (const [name, value] of referenceTui) {
        const file = `reference-tui/${name}.json`;
        referenceFiles.push(file);
        await writeFile(
          path.join(familyDir, file),
          `${prettyJSON(
            canon({
              schemaVersion: 1,
              referenceOnly: true,
              note: "Reference-only upstream TUI observation (D35): Orb frame goldens are Orb-owned snapshots; do not gate byte parity on this file.",
              scenario: name,
              ...(value as object),
            }),
          )}\n`,
        );
      }

      await writeFile(
        path.join(familyDir, "manifest.json"),
        `${prettyJSON({
          family: FAMILY,
          upstreamCommit,
          generator: "conformance/extract/f13-dynamic-workflows.ts",
          source:
            "packages/coding-agent/src/core/{sdk.ts,agent-session.ts,model-runtime.ts,model-registry.ts,session-manager.ts,settings-manager.ts,resource-loader.ts,tools/*} + packages/ai/src/providers/faux.ts + packages/tui/src + npm:@quintinshaw/pi-dynamic-workflows@3.5.1",
          plugin: { name: PLUGIN_NAME, version: PLUGIN_VERSION, integrity: PLUGIN_INTEGRITY },
          fixedNow: FIXED_NOW,
          canonicalized: [
            "<fixture> temp roots",
            "<fixture-dash> dashed cwd encodings",
            "<uuid-N> UUIDs",
            "<cwdhash> workflow project-key hash",
            "<pi-package>/<upstream> package dirs",
            "faux token counts estimated over the same canonical placeholders (machine-independent)",
          ],
          referenceOnly: referenceFiles,
          files: ["cases.json", ...caseFiles, ...referenceFiles],
        })}\n`,
      );
      await writeFile(
        path.join(familyDir, "cases.json"),
        `${prettyJSON({ schemaVersion: 1, scenarios: caseFiles.map((f) => f.replace(/^cases\//, "").replace(/\.json$/, "")) })}\n`,
      );
    });
  } finally {
    tokenCountReplacements = [];
    for (const [key, value] of Object.entries(savedEnv)) {
      if (value === undefined) delete process.env[key];
      else process.env[key] = value;
    }
    await rm(tmpRoot, { recursive: true, force: true });
  }
}

// ---------------------------------------------------------------------------
// individual scenarios
// ---------------------------------------------------------------------------

async function scenarioForegroundBasic(ctx: ScenarioContext): Promise<ScenarioResult> {
  const faux = ctx.loaded.upstream.faux;
  return runWorkflowScenario(ctx, "foreground-basic", (world) => {
    world.setResponses([text(faux, "The marker file says marker-from-repo."), text(faux, "Summary: the marker holds marker-from-repo.")]);
    return {};
  });
}

async function scenarioStructuredOutput(ctx: ScenarioContext): Promise<ScenarioResult> {
  const faux = ctx.loaded.upstream.faux;
  return runWorkflowScenario(ctx, "structured-output", (world) => {
    world.setResponses([
      toolUse(faux, [["structured_output", { fruit: "apple", count: 3 }, "structured-1"]]),
      text(faux, "I will not call the tool."),
      toolUse(faux, [["structured_output", { veg: "leek" }, "structured-2"]]),
      text(faux, "Thinking about minerals..."),
      text(faux, "Still refusing the tool."),
      text(faux, 'Here you go:\n```json\n{"mineral":"quartz"}\n```'),
    ]);
    return {};
  });
}

async function scenarioStoreTools(ctx: ScenarioContext): Promise<ScenarioResult> {
  const faux = ctx.loaded.upstream.faux;
  return runWorkflowScenario(ctx, "store-tools", (world) => {
    world.setResponses([
      toolUse(faux, [["store_put", { key: "finding", value: { n: 1, kind: "seed" } }, "store-1"]]),
      text(faux, "Stored the finding."),
      toolUse(faux, [["store_get", { key: "finding" }, "store-2"]]),
      text(faux, "The finding was n=1 kind=seed."),
    ]);
    return {};
  });
}

async function scenarioWebToolset(ctx: ScenarioContext): Promise<ScenarioResult> {
  const faux = ctx.loaded.upstream.faux;
  const coding = ctx.loaded.upstream.codingAgent;
  const web = ctx.loaded.plugin.webTools;
  const nativeFetch = globalThis.fetch;
  const searchHtml = [
    '<html><body>',
    '<h2><a href="https://www.bing.com/internal">Skip me</a></h2>',
    '<h2><a href="https://example.test/page">Example <b>Result</b></a></h2>',
    '<h2><a href="https://second.test/doc">Second Result</a></h2>',
    "</body></html>",
  ].join("");
  const pageHtml =
    "<html><head><style>p{}</style><script>bad()</script></head><body><h1>Doc &amp; Title</h1><p>First&nbsp;paragraph.</p><p>Second paragraph.</p></body></html>";
  globalThis.fetch = (async (input: any) => {
    const url = String(input);
    const body = url.includes("bing.com/search") ? searchHtml : pageHtml;
    return {
      status: 200,
      text: async () => body,
    } as Response;
  }) as typeof fetch;
  try {
    return await runWorkflowScenario(ctx, "web-toolset", (world) => {
      world.setResponses([
        toolUse(faux, [["web_search", { query: "orb conformance" }, "web-1"]]),
        toolUse(faux, [["web_fetch", { url: "https://example.test/page" }, "web-2"]]),
        text(faux, "Research done: example.test documents the topic."),
      ]);
      return {
        tools: [...coding.createCodingTools(ctx.project), ...web.createWebTools()],
      };
    });
  } finally {
    globalThis.fetch = nativeFetch;
  }
}

async function scenarioAgentTypes(ctx: ScenarioContext): Promise<ScenarioResult> {
  const faux = ctx.loaded.upstream.faux;
  return runWorkflowScenario(ctx, "agent-types", (world) => {
    world.setResponses([
      toolUse(faux, [["read", { path: "marker.txt" }, "agents-1"]]),
      text(faux, "The marker says marker-from-repo."),
      toolUse(faux, [["read", { path: "marker.txt" }, "agents-2"]]),
      text(faux, "Isolated read complete."),
      text(faux, "Ghost type fell back to defaults."),
    ]);
    return {};
  });
}

async function scenarioNestedWorkflow(ctx: ScenarioContext): Promise<ScenarioResult> {
  const faux = ctx.loaded.upstream.faux;
  return runWorkflowScenario(ctx, "nested-workflow", (world) => {
    world.setResponses([text(faux, "Child one reporting on seeds."), text(faux, "Parent used the child result.")]);
    return {
      loadSavedWorkflow: (name: string) => (name === "child-flow" ? SCRIPTS["nested-child"] : undefined),
    };
  });
}

async function scenarioCancellation(ctx: ScenarioContext): Promise<ScenarioResult> {
  const faux = ctx.loaded.upstream.faux;
  const controller = new AbortController();
  return runWorkflowScenario(ctx, "cancellation", (world) => {
    world.setResponses([
      text(faux, "First result before the abort."),
      () => {
        controller.abort();
        return text(faux, "Never delivered.");
      },
    ]);
    return { signal: controller.signal };
  });
}

async function scenarioModelRouting(ctx: ScenarioContext): Promise<ScenarioResult> {
  const workflowsDir = path.join(ctx.home, ".pi", "workflows");
  await mkdir(workflowsDir, { recursive: true });
  // small: resolvable tier pin. medium (the implicit default tier): unknown
  // PROVIDER, so untagged agents degrade to the session default via
  // onModelFallback. big: explicit tier request onto the same unknown provider
  // throws MODEL_NOT_FOUND naming the tier (#131 asymmetry). An off-catalog id
  // on a known provider ("faux/synth-id") synthesizes a custom Model object
  // that the session bridge must accept as-is.
  await writeFile(
    path.join(workflowsDir, "model-tiers.json"),
    `${JSON.stringify(
      { tiers: { small: "faux/faux-mini", medium: "ghost/nowhere", big: "ghost/nowhere" } },
      null,
      2,
    )}\n`,
  );
  const faux = ctx.loaded.upstream.faux;
  try {
    return await runWorkflowScenario(
      ctx,
      "model-routing",
      (world) => {
        world.setResponses([
          text(faux, "Small tier answer."),
          text(faux, "Untagged answer."),
          text(faux, "Synthesized custom-id answer."),
        ]);
        return {};
      },
      {
        models: [
          { id: "faux-model", name: "Faux Model" },
          { id: "faux-mini", name: "Faux Mini", cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } },
        ],
      },
    );
  } finally {
    await rm(path.join(workflowsDir, "model-tiers.json"), { force: true });
  }
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
] as const;

function recordManagerEvents(manager: any): unknown[] {
  const events: unknown[] = [];
  for (const name of MANAGER_EVENTS) {
    manager.on(name, (payload: unknown) => {
      if (name === "agentHistory") return;
      const record: Record<string, unknown> = { event: name };
      if (payload && typeof payload === "object") {
        for (const [key, value] of Object.entries(payload as Record<string, unknown>)) {
          if (key === "history") continue;
          if (key === "error" && value instanceof Error) {
            record.error = { name: value.name, message: value.message, ...(value as object) };
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

function waitForEvent(manager: any, name: string): Promise<unknown> {
  return new Promise((resolve) => manager.once(name, resolve));
}

async function readRunArtifacts(ctx: ScenarioContext): Promise<Record<string, unknown>> {
  const projectsRoot = path.join(ctx.home, ".pi", "workflows", "projects");
  const artifacts: Record<string, unknown> = {};
  const walk = async (dir: string, prefix: string) => {
    let entries: string[];
    try {
      entries = (await readdir(dir)).sort();
    } catch {
      return;
    }
    for (const entry of entries) {
      const full = path.join(dir, entry);
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

async function scenarioBackgroundLifecycle(
  ctx: ScenarioContext,
): Promise<{ observations: ScenarioResult; taskPanelFrames: unknown }> {
  const faux = ctx.loaded.upstream.faux;
  const themeModule = ctx.loaded.upstream.theme;
  const theme = themeModule.theme;
  const { WorkflowManager } = ctx.loaded.plugin.workflowManager;
  const world = await createFauxWorld(ctx.loaded);
  world.setResponses([
    text(faux, "First succeeded."),
    faux.fauxAssistantMessage([], {
      stopReason: "error",
      errorMessage: "Codex usage limit reached (plus plan). Resets in ~3h.",
      timestamp: FIXED_NOW,
    }),
  ]);
  const manager = new WorkflowManager({
    cwd: ctx.project,
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
  const pausedArtifacts = await readRunArtifacts(ctx);

  // Reference-only task panel frames (D35) while the run sits paused, plus the
  // idle render after completion.
  const { renderPanel, renderPanelDetailed } = ctx.loaded.plugin.taskPanel;
  const taskPanelFrames = {
    pausedCompact: { "60": renderPanel(manager, theme, 60), "100": renderPanel(manager, theme, 100) },
    pausedDetailed: { "100": renderPanelDetailed(manager, theme, 100, 4, FIXED_NOW) },
  } as Record<string, unknown>;

  // Resume: the journaled first agent replays; the second runs live and succeeds.
  world.setResponses([text(faux, "Second succeeded after the reset.")]);
  const completed = waitForEvent(manager, "complete");
  const resumed = await manager.resume(started.runId);
  await completed;
  const completedRuns = manager.listRuns();

  // Stop a hanging run.
  const stopWorld = await createFauxWorld(ctx.loaded);
  stopWorld.setResponses([
    (_context: unknown, options: any) =>
      new Promise((resolve) => {
        options?.signal?.addEventListener("abort", () =>
          resolve(
            faux.fauxAssistantMessage([], {
              stopReason: "aborted",
              errorMessage: "Request was aborted",
              timestamp: FIXED_NOW,
            }),
          ),
        );
      }),
  ]);
  const stopManager = new WorkflowManager({
    cwd: ctx.project,
    concurrency: 1,
    mainModel: "faux/faux-model",
    modelRegistry: stopWorld.registry,
    sessionId: "session-fixed-1",
  });
  const stopEvents = recordManagerEvents(stopManager);
  const stopStarted = stopManager.startInBackground(SCRIPTS["background-stop"], undefined, {});
  await new Promise((resolve) => setTimeout(resolve, 50));
  const stopped = stopManager.stop(stopStarted.runId);
  await new Promise((resolve) => setTimeout(resolve, 50));
  const stoppedRuns = stopManager.listRuns().filter((run: any) => run.runId === stopStarted.runId);
  taskPanelFrames.idleAfterCompletion = { "100": renderPanel(manager, theme, 100) };

  return {
    observations: {
      script: SCRIPTS["background-lifecycle"],
      startedRunId: started.runId,
      events,
      pausedRuns,
      pausedArtifacts,
      resumed,
      completedRuns,
      stop: { runId: stopStarted.runId, stopped, events: stopEvents, runs: stoppedRuns },
      providerCalls: world.providerCalls,
      pendingFauxResponses: world.pendingResponses() + stopWorld.pendingResponses(),
    },
    taskPanelFrames,
  };
}

async function scenarioPersistAgentSessions(ctx: ScenarioContext): Promise<ScenarioResult> {
  const faux = ctx.loaded.upstream.faux;
  const result = await runWorkflowScenario(ctx, "persist-agent-sessions", (world) => {
    world.setResponses([toolUse(faux, [["read", { path: "marker.txt" }, "persist-1"]]), text(faux, "Kept the marker safe.")]);
    return { persistAgentSessions: true };
  });
  const sessionsRoot = path.join(ctx.agentDir, "sessions");
  const artifacts: Array<Record<string, unknown>> = [];
  const walk = async (dir: string, prefix: string) => {
    let entries: string[];
    try {
      entries = (await readdir(dir)).sort();
    } catch {
      return;
    }
    for (const entry of entries) {
      const full = path.join(dir, entry);
      const info = await stat(full);
      if (info.isDirectory()) {
        await walk(full, `${prefix}${entry}/`);
      } else {
        const content = await readFile(full, "utf8");
        // Session entry ids are random hex; remap them (and their parent
        // references) to stable ordinals so the tree shape stays comparable.
        const idMap = new Map<string, string>();
        const remapIds = (value: unknown): unknown => {
          if (Array.isArray(value)) return value.map(remapIds);
          if (value && typeof value === "object") {
            return Object.fromEntries(
              Object.entries(value as Record<string, unknown>).map(([key, item]) => {
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

interface CaptureExtension {
  api: any;
  handlers: Map<string, Array<(event: any, ctx: any) => unknown>>;
  tools: Map<string, any>;
  commands: Map<string, any>;
  sentMessages: unknown[];
  activeTools: string[];
  registrationLog: unknown[];
}

function createCaptureExtension(): CaptureExtension {
  const handlers = new Map<string, Array<(event: any, ctx: any) => unknown>>();
  const tools = new Map<string, any>();
  const commands = new Map<string, any>();
  const sentMessages: unknown[] = [];
  const registrationLog: unknown[] = [];
  const activeTools = ["read", "bash", "edit", "write"];
  const target = {
    on(event: string, handler: (event: any, ctx: any) => unknown) {
      handlers.set(event, [...(handlers.get(event) ?? []), handler]);
    },
    registerTool(tool: any) {
      tools.set(tool.name, tool);
      registrationLog.push({ kind: "tool", name: tool.name });
    },
    registerCommand(name: string, command: any) {
      commands.set(name, command);
      registrationLog.push({ kind: "command", name, description: command?.description ?? null });
    },
    getCommands() {
      return [...commands.entries()].map(([name, command]) => ({ name, description: command?.description ?? null }));
    },
    getActiveTools() {
      return [...activeTools];
    },
    setActiveTools(names: string[]) {
      activeTools.splice(0, activeTools.length, ...names);
      registrationLog.push({ kind: "setActiveTools", names: [...names] });
    },
    sendMessage(message: unknown, options?: unknown) {
      sentMessages.push({ message, options: options ?? null });
    },
    events: { emit() {} },
  };
  const api = new Proxy(target, {
    get(t, property) {
      if (property in t) return (t as any)[property];
      return () => {};
    },
  });
  return { api, handlers, tools, commands, sentMessages, activeTools, registrationLog };
}

async function emitExtensionEvent(
  capture: CaptureExtension,
  event: string,
  value: unknown,
  ctx: unknown,
): Promise<void> {
  for (const handler of capture.handlers.get(event) ?? []) {
    await handler(value, ctx);
  }
}

function projectToolDefinition(tool: any): Record<string, unknown> {
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

async function scenarioExtensionLifecycle(ctx: ScenarioContext): Promise<ScenarioResult> {
  const faux = ctx.loaded.upstream.faux;
  const themeModule = ctx.loaded.upstream.theme;
  const world = await createFauxWorld(ctx.loaded);
  const theme = themeModule.theme;

  const originalCwd = process.cwd();
  process.chdir(ctx.project);
  let generationOne: CaptureExtension;
  try {
    generationOne = createCaptureExtension();
    ctx.loaded.plugin.extensionFactory(generationOne.api);
  } finally {
    process.chdir(originalCwd);
  }

  const widgets = new Map<string, { factory: any; options: unknown }>();
  const notifications: unknown[] = [];
  const makeCtx = (sessionId: string) => ({
    cwd: ctx.project,
    hasUI: true,
    model: world.model(),
    modelRegistry: world.registry,
    sessionManager: { getSessionId: () => sessionId },
    ui: {
      notify: (message: string, level?: string) => notifications.push({ message, level: level ?? null }),
      setWidget: (key: string, factory: unknown, options?: unknown) => widgets.set(key, { factory, options: options ?? null }),
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

  // Lazy promptGuidelines: the workflow tool's guidelines re-read available
  // models on every access (frozen-snapshot hosts would diverge).
  const guidelinesFirst = workflowTool.promptGuidelines;
  const guidelinesSecond = workflowTool.promptGuidelines;

  // prepareArguments normalization goldens.
  const prepareCases: Record<string, unknown> = {};
  const tryPrepare = (label: string, tool: any, args: unknown) => {
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
  world.setResponses([text(faux, "Extension agent result.")]);
  const toolCtx = { hasUI: true, ui: { confirm: async () => true } };
  const updates: unknown[] = [];
  const startResult = await workflowTool.execute(
    "tool-call-1",
    { script: SCRIPTS["extension-run"], background: true },
    new AbortController().signal,
    (update: unknown) => updates.push(update),
    toolCtx,
  );
  const runId = (startResult.details as { runId: string }).runId;

  // Result delivery: the completion is delivered into the session via
  // pi.sendMessage once the run finishes (delivery resumed on session_start).
  await new Promise((resolve) => setTimeout(resolve, 200));

  const controlList = await controlTool.execute("tool-call-2", { action: "list" }, new AbortController().signal, () => {}, {});
  const controlStatus = await controlTool.execute(
    "tool-call-3",
    { action: "status", runId },
    new AbortController().signal,
    () => {},
    {},
  );

  // The task panel mounts through ui.setWidget; with only terminal runs left
  // it renders idle (no lines). Live-state frames are captured (reference-only)
  // in the background-lifecycle scenario.
  const widget = widgets.get("workflow-tasks");
  const tuiStub = { requestRender() {}, terminal: { columns: 100, rows: 30 } };
  let widgetMount: unknown = null;
  if (widget) {
    const component = widget.factory(tuiStub, theme);
    widgetMount = {
      placement: (widget.options as { placement?: string } | null)?.placement ?? null,
      idleFrame: component.render(100),
    };
    component.dispose?.();
  }

  // renderCall/renderResult reference frames for the workflow tool.
  const renderCall = workflowTool.renderCall?.({}, theme)?.render(60) ?? null;
  const renderResult = workflowTool.renderResult?.(startResult, { expanded: false, isPartial: false }, theme)?.render(80) ?? null;

  // /new handoff: shutdown stages the runtime; the next factory generation
  // claims it and re-homes live runs onto the new session.
  await emitExtensionEvent(generationOne, "session_shutdown", { type: "session_shutdown", reason: "new" }, undefined);
  const nextGeneration = () => {
    process.chdir(ctx.project);
    try {
      const generation = createCaptureExtension();
      ctx.loaded.plugin.extensionFactory(generation.api);
      return generation;
    } finally {
      process.chdir(originalCwd);
    }
  };
  const generationTwo = nextGeneration();
  await emitExtensionEvent(generationTwo, "session_start", { type: "session_start", reason: "new" }, makeCtx("session-b"));
  // The completed run stays homed on session-a (history belongs to the session
  // that ran it), so the session-b view reports run-not-found.
  const controlAfterNew = await generationTwo.tools
    .get("workflow_control")
    .execute("tool-call-4", { action: "status", runId }, new AbortController().signal, () => {}, {});

  // /reload handoff: same-project replacement reasons always hand off.
  await emitExtensionEvent(generationTwo, "session_shutdown", { type: "session_shutdown", reason: "reload" }, undefined);
  const generationThree = nextGeneration();
  await emitExtensionEvent(
    generationThree,
    "session_start",
    { type: "session_start", reason: "reload" },
    makeCtx("session-b"),
  );

  // quit with a live run: the runtime is discarded and the in-flight run is
  // paused onto the persisted journal.
  world.setResponses([
    (_context: unknown, options: any) =>
      new Promise((resolve) => {
        options?.signal?.addEventListener("abort", () =>
          resolve(
            faux.fauxAssistantMessage([], {
              stopReason: "aborted",
              errorMessage: "Request was aborted",
              timestamp: FIXED_NOW,
            }),
          ),
        );
      }),
  ]);
  const hangingStart = await generationThree.tools
    .get("workflow")
    .execute(
      "tool-call-5",
      { script: SCRIPTS["extension-hang"], background: true },
      new AbortController().signal,
      () => {},
      toolCtx,
    );
  const hangingRunId = (hangingStart.details as { runId: string }).runId;
  await new Promise((resolve) => setTimeout(resolve, 50));
  await emitExtensionEvent(generationThree, "session_shutdown", { type: "session_shutdown", reason: "quit" }, undefined);
  await new Promise((resolve) => setTimeout(resolve, 100));
  const artifactsAfterQuit = await readRunArtifacts(ctx);
  const hangingAfterQuit = Object.entries(artifactsAfterQuit)
    .filter(([key]) => key.includes(hangingRunId))
    .map(([key, value]) => ({
      path: key,
      status: (value as { status?: string }).status ?? null,
      agents: ((value as { agents?: Array<{ label: string; status: string }> }).agents ?? []).map((agent) => ({
        label: agent.label,
        status: agent.status,
      })),
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
    providerCalls: world.providerCalls,
    pendingFauxResponses: world.pendingResponses(),
  };
}

// ---------------------------------------------------------------------------
// /workflows-models select flow (reference-only frames + tier write)
// ---------------------------------------------------------------------------

async function scenarioWorkflowsModels(
  ctx: ScenarioContext,
): Promise<{ observations: ScenarioResult; frames: unknown }> {
  const themeModule = ctx.loaded.upstream.theme;
  const theme = themeModule.theme;
  const world = await createFauxWorld(ctx.loaded, {
    models: [
      { id: "faux-model", name: "Faux Model" },
      { id: "faux-mini", name: "Faux Mini" },
    ],
  });
  const { editSingleTier } = ctx.loaded.plugin.workflowsModelsCommand;
  const frames: Array<{ input: string | null; lines: string[] }> = [];
  const notifications: unknown[] = [];
  const selects: unknown[] = [];
  const tuiStub = { requestRender() {}, terminal: { columns: 72, rows: 24 } };
  const commandCtx = {
    model: world.model(),
    modelRegistry: world.registry,
    ui: {
      notify: (message: string, level?: string) => notifications.push({ message, level: level ?? null }),
      select: async (title: string, options: string[]) => {
        selects.push({ title, options });
        return "high";
      },
      custom: async (factory: (tui: any, theme: any, keybindings: unknown, done: (value: unknown) => void) => any) => {
        let resolved: unknown;
        let settled = false;
        const done = (value: unknown) => {
          resolved = value;
          settled = true;
        };
        const component = factory(tuiStub, theme, undefined, done);
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
    observations: {
      updatedTiers: updated,
      thinkingSelects: selects,
      notifications,
    },
    frames: { frames },
  };
}

// ---------------------------------------------------------------------------
// export surface + unsupported-export probes
// ---------------------------------------------------------------------------

async function scenarioExportSurface(ctx: ScenarioContext): Promise<ScenarioResult> {
  const { codingAgent, ai, tui } = ctx.loaded.upstream;
  const surface = (namespace: object) => Object.keys(namespace).sort();
  const probe = (namespace: any, name: string) => ({
    export: name,
    present: name in namespace,
    typeof: typeof namespace[name],
  });
  return {
    note:
      "Upstream serves every export below; orb-extension-sdk must expose the same names, implementing the supported set and throwing OrbUnsupportedCapability from the rest. The Orb-side diagnostic is asserted by a Go test, not this golden.",
    exports: {
      "@earendil-works/pi-coding-agent": surface(codingAgent),
      "@earendil-works/pi-ai": surface(ai),
      "@earendil-works/pi-tui": surface(tui),
    },
    unsupportedProbes: [
      probe(codingAgent, "main"),
      probe(codingAgent, "DefaultPackageManager"),
      probe(codingAgent, "copyToClipboard"),
      probe(ai, "retryAssistantCall"),
      probe(ai, "createProvider"),
      probe(tui, "Editor"),
      probe(tui, "VStack"),
    ],
    supportedProbes: [
      probe(codingAgent, "createAgentSession"),
      probe(codingAgent, "defineTool"),
      probe(codingAgent, "parseFrontmatter"),
      probe(ai, "modelsAreEqual"),
      probe(tui, "SelectList"),
      probe(tui, "truncateToWidth"),
      probe(tui, "Container"),
      probe(tui, "Markdown"),
    ],
  };
}
