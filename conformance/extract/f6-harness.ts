import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

type Result<T> = { ok: true; value: T } | { ok: false; error: { code: string; message: string; path?: string } };

const fixedNow = "2026-02-03T04:05:06.789Z";

async function withFixedDate<T>(operation: () => Promise<T>): Promise<T> {
  const NativeDate = Date;
  class FixtureDate extends NativeDate {
    constructor(value?: string | number | Date) {
      super(value === undefined ? fixedNow : value);
    }
    static now(): number { return new NativeDate(fixedNow).getTime(); }
  }
  globalThis.Date = FixtureDate as DateConstructor;
  try {
    return await operation();
  } finally {
    globalThis.Date = NativeDate;
  }
}

const fixedEntries = [
  {
    type: "message",
    id: "root-user",
    parentId: null,
    timestamp: "2026-02-03T04:05:07.000Z",
    message: { role: "user", content: [{ type: "text", text: "root <>&\u2028\u2029" }], timestamp: 1 },
  },
  {
    type: "message",
    id: "main-assistant",
    parentId: "root-user",
    timestamp: "2026-02-03T04:05:08.000Z",
    message: {
      role: "assistant",
      content: [{ type: "text", text: "answer" }],
      api: "openai-responses",
      provider: "openai",
      model: "gpt-test",
      usage: {
        input: 1,
        output: 2,
        cacheRead: 0,
        cacheWrite: 0,
        totalTokens: 3,
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
      },
      stopReason: "stop",
      timestamp: 2,
    },
  },
  {
    type: "message",
    id: "second-user",
    parentId: "main-assistant",
    timestamp: "2026-02-03T04:05:09.000Z",
    message: { role: "user", content: [{ type: "text", text: "continue" }], timestamp: 3 },
  },
  {
    type: "thinking_level_change",
    id: "thinking",
    parentId: "second-user",
    timestamp: "2026-02-03T04:05:10.000Z",
    thinkingLevel: "high",
  },
  {
    type: "model_change",
    id: "model",
    parentId: "thinking",
    timestamp: "2026-02-03T04:05:11.000Z",
    provider: "anthropic",
    modelId: "claude-test",
  },
  {
    type: "active_tools_change",
    id: "tools",
    parentId: "model",
    timestamp: "2026-02-03T04:05:12.000Z",
    activeToolNames: ["read", "bash"],
  },
  {
    type: "active_tools_change",
    id: "tools-empty",
    parentId: "tools",
    timestamp: "2026-02-03T04:05:12.500Z",
    activeToolNames: [],
  },
  {
    type: "custom",
    id: "custom",
    parentId: "tools-empty",
    timestamp: "2026-02-03T04:05:13.000Z",
    customType: "state",
    data: { nested: [1, "two"] },
  },
  {
    type: "custom_message",
    id: "custom-message",
    parentId: "custom",
    timestamp: "2026-02-03T04:05:14.000Z",
    customType: "notice",
    content: "visible note",
    display: true,
    details: { source: "fixture" },
  },
  {
    type: "session_info",
    id: "session-name",
    parentId: "custom-message",
    timestamp: "2026-02-03T04:05:15.000Z",
    name: "  fixture name  ",
  },
  {
    type: "compaction",
    id: "compaction",
    parentId: "session-name",
    timestamp: "2026-02-03T04:05:16.000Z",
    summary: "prior work",
    firstKeptEntryId: "second-user",
    tokensBefore: 42.5,
    details: { readFiles: ["a.go"] },
    fromHook: false,
  },
  {
    type: "message",
    id: "branch-user",
    parentId: "root-user",
    timestamp: "2026-02-03T04:05:17.000Z",
    message: { role: "user", content: [{ type: "text", text: "branch" }], timestamp: 4 },
  },
  {
    type: "branch_summary",
    id: "branch-summary",
    parentId: "branch-user",
    timestamp: "2026-02-03T04:05:17.500Z",
    fromId: "compaction",
    summary: "discarded branch work",
    details: { modifiedFiles: ["b.go"] },
    fromHook: true,
  },
  {
    type: "message",
    id: "empty-parent",
    parentId: "",
    timestamp: "2026-02-03T04:05:17.750Z",
    message: { role: "user", content: [{ type: "text", text: "empty parent is root" }], timestamp: 5 },
  },
  {
    type: "label",
    id: "label-root-set",
    parentId: "branch-user",
    timestamp: "2026-02-03T04:05:18.000Z",
    targetId: "root-user",
    label: "  checkpoint  ",
  },
  {
    type: "label",
    id: "label-root-clear",
    parentId: "label-root-set",
    timestamp: "2026-02-03T04:05:19.000Z",
    targetId: "root-user",
    label: "   ",
  },
  {
    type: "label",
    id: "label-branch",
    parentId: "label-root-clear",
    timestamp: "2026-02-03T04:05:20.000Z",
    targetId: "branch-user",
    label: "  branch point  ",
  },
  {
    type: "leaf",
    id: "leaf-record",
    parentId: "label-branch",
    timestamp: "2026-02-03T04:05:21.000Z",
    targetId: "tools-empty",
  },
] as const;

function normalize(value: unknown, root: string): unknown {
  if (typeof value === "string") return value.split(root).join("<fixture>");
  if (Array.isArray(value)) return value.map((item) => normalize(item, root));
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, normalize(item, root)]));
  }
  return value;
}

function get<T>(result: Result<T>): T {
  if (!result.ok) throw result.error;
  return result.value;
}

async function captureError(operation: () => Promise<unknown>, root: string): Promise<unknown> {
  try {
    await operation();
    return null;
  } catch (error) {
    const typed = error as { code?: string; message?: string; path?: string };
    return normalize({ code: typed.code, message: typed.message, path: typed.path }, root);
  }
}

function messageRole(message: unknown): string | null {
  if (!message || typeof message !== "object" || !("role" in message)) return null;
  const role = (message as { role?: unknown }).role;
  return typeof role === "string" ? role : null;
}

function observeContext(context: any): unknown {
  return {
    messages: context.messages,
    roles: context.messages.map(messageRole),
    thinkingLevel: context.thinkingLevel,
    model: context.model,
    activeToolNames: context.activeToolNames,
  };
}

async function observeStorage(storage: any, Session: any, metadataRoot: string): Promise<unknown> {
  const session = new Session(storage);
  const leafId = await storage.getLeafId();
  const entries = await storage.getEntries();
  const branch = await storage.getPathToRootOrCompaction(leafId);
  const context = await session.buildContext();
  return normalize({
    metadata: await storage.getMetadata(),
    leafId,
    entries,
    entryIds: entries.map((entry: any) => entry.id),
    branchIds: branch.map((entry: any) => entry.id),
    messageIds: (await storage.findEntries("message")).map((entry: any) => entry.id),
    labels: {
      root: (await storage.getLabel("root-user")) ?? null,
      branch: (await storage.getLabel("branch-user")) ?? null,
    },
    sessionName: (await session.getSessionName()) ?? null,
    context: observeContext(context),
  }, metadataRoot);
}

async function generateTransformObservations(memoryModule: any, Session: any): Promise<unknown> {
  const entries = [
    {
      type: "message", id: "transform-root", parentId: null, timestamp: "2026-02-03T04:06:00.000Z",
      message: { role: "user", content: [{ type: "text", text: "transform root" }], timestamp: 10 },
    },
    {
      type: "custom", id: "constructor-custom", parentId: "transform-root", timestamp: "2026-02-03T04:06:01.000Z",
      customType: "constructor_state", data: { label: "constructor" },
    },
    {
      type: "message", id: "constructor-drop", parentId: "constructor-custom", timestamp: "2026-02-03T04:06:02.000Z",
      message: { role: "user", content: [{ type: "text", text: "constructor drop" }], timestamp: 11 },
    },
    {
      type: "custom", id: "override-custom", parentId: "constructor-drop", timestamp: "2026-02-03T04:06:03.000Z",
      customType: "override_state", data: { label: "override" },
    },
    {
      type: "custom", id: "call-custom", parentId: "override-custom", timestamp: "2026-02-03T04:06:04.000Z",
      customType: "call_state", data: { label: "call" },
    },
    {
      type: "message", id: "call-drop", parentId: "call-custom", timestamp: "2026-02-03T04:06:05.000Z",
      message: { role: "user", content: [{ type: "text", text: "call drop" }], timestamp: 12 },
    },
    {
      type: "message", id: "transform-assistant", parentId: "call-drop", timestamp: "2026-02-03T04:06:06.000Z",
      message: {
        role: "assistant", content: [{ type: "text", text: "transform answer" }], api: "openai-responses",
        provider: "openai", model: "gpt-transform", usage: {
          input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
        }, stopReason: "stop", timestamp: 13,
      },
    },
  ];
  const storage = new memoryModule.InMemorySessionStorage({
    entries,
    metadata: { id: "transform-session", createdAt: fixedNow },
  });
  const userMessage = (text: string, timestamp: number) => ({
    role: "user", content: [{ type: "text", text }], timestamp,
  });
  const session = new Session(storage, {
    entryTransforms: [(input: any[]) => input.filter((entry: any) => entry.id !== "constructor-drop")],
    entryProjectors: {
      constructor_state: () => [userMessage("constructor projector", 20)],
      override_state: () => [userMessage("constructor override", 21)],
    },
  });
  const constructorContext = await session.buildContext();
  const perCallContext = await session.buildContext({
    entryTransforms: [(input: any[]) => input.filter((entry: any) => entry.id !== "call-drop")],
    entryProjectors: {
      override_state: () => [userMessage("per-call override", 22)],
      call_state: () => [userMessage("per-call projector", 23)],
    },
  });
  return {
    constructorOnly: observeContext(constructorContext),
    constructorAndPerCall: observeContext(perCallContext),
  };
}

async function generateSessionFixture(upstreamRoot: string, root: string): Promise<{ bytes: Uint8Array; observations: unknown }> {
  const jsonlModule = await import(pathToFileURL(path.join(upstreamRoot, "packages/agent/src/harness/session/jsonl-storage.ts")).href);
  const memoryModule = await import(pathToFileURL(path.join(upstreamRoot, "packages/agent/src/harness/session/memory-storage.ts")).href);
  const sessionModule = await import(pathToFileURL(path.join(upstreamRoot, "packages/agent/src/harness/session/session.ts")).href);
  const repoUtils = await import(pathToFileURL(path.join(upstreamRoot, "packages/agent/src/harness/session/repo-utils.ts")).href);
  const envModule = await import(pathToFileURL(path.join(upstreamRoot, "packages/agent/src/harness/env/nodejs.ts")).href);

  const env = new envModule.NodeExecutionEnv({ cwd: root });
  const filePath = path.join(root, "session.jsonl");
  await withFixedDate(async () => {
    const storage = await jsonlModule.JsonlSessionStorage.create(env, filePath, {
      cwd: "/fixture/project",
      sessionId: "session-fixed",
      parentSessionPath: "/fixture/parent.jsonl",
      metadata: { profile: "reviewer", nested: { enabled: true } },
    });
    for (const entry of fixedEntries) await storage.appendEntry(entry);
  });

  const bytes = await readFile(filePath);
  const reopened = await jsonlModule.JsonlSessionStorage.open(env, filePath);
  const memory = new memoryModule.InMemorySessionStorage({
    entries: [...fixedEntries],
    metadata: { id: "session-fixed", createdAt: fixedNow },
  });
  const forkBefore = await repoUtils.getEntriesToFork(reopened, { entryId: "second-user" });
  const forkAt = await repoUtils.getEntriesToFork(reopened, { entryId: "model", position: "at" });
  const invalidFork = await captureError(
    () => repoUtils.getEntriesToFork(reopened, { entryId: "main-assistant" }),
    root,
  );
  const compactionPath = await reopened.getPathToRootOrCompaction("compaction");
  const compactedContext = sessionModule.buildSessionContext(compactionPath);
  const branchSummaryPath = await reopened.getPathToRootOrCompaction("branch-summary");
  const branchSummaryContext = sessionModule.buildSessionContext(branchSummaryPath);
  const emptyParentPath = await reopened.getPathToRootOrCompaction("empty-parent");

  const invalidCases = [
    { name: "missing-header", content: "" },
    { name: "unsupported-version", content: '{"type":"session","version":2,"id":"s","timestamp":"t","cwd":"/c"}\n' },
    { name: "metadata-array", content: '{"type":"session","version":3,"id":"s","timestamp":"t","cwd":"/c","metadata":[]}\n' },
    { name: "invalid-entry", content: '{"type":"session","version":3,"id":"s","timestamp":"t","cwd":"/c"}\n{"type":"message","id":"e","parentId":3,"timestamp":"t"}\n' },
    { name: "dangling-leaf", content: '{"type":"session","version":3,"id":"s","timestamp":"t","cwd":"/c"}\n{"type":"leaf","id":"l","parentId":null,"timestamp":"t","targetId":"missing"}\n', leaf: true },
  ];
  const invalid: unknown[] = [];
  for (const fixtureCase of invalidCases) {
    const invalidPath = path.join(root, `${fixtureCase.name}.jsonl`);
    await writeFile(invalidPath, fixtureCase.content);
    invalid.push({
      name: fixtureCase.name,
      error: await captureError(async () => {
        const loaded = await jsonlModule.JsonlSessionStorage.open(env, invalidPath);
        if (fixtureCase.leaf) await loaded.getLeafId();
      }, root),
    });
  }

  const jsonlObservations = await observeStorage(reopened, sessionModule.Session, root);
  const memoryObservations = await observeStorage(memory, sessionModule.Session, root);
  const transformObservations = await generateTransformObservations(memoryModule, sessionModule.Session);

  const typedActiveToolsStorage = new memoryModule.InMemorySessionStorage({
    metadata: { id: "typed-active-tools", createdAt: fixedNow },
  });
  const typedActiveToolsSession = new sessionModule.Session(typedActiveToolsStorage);
  await typedActiveToolsSession.appendActiveToolsChange([]);
  const typedActiveToolsEntries = await typedActiveToolsStorage.getEntries();
  const typedActiveToolsContext = await typedActiveToolsSession.buildContext();

  await reopened.appendEntry({
    type: "custom",
    id: "appended-fixed",
    parentId: "tools",
    timestamp: "2026-02-03T04:05:22.000Z",
    customType: "after-rehydrate",
    data: { text: "<>&\u2028\u2029" },
  });
  const mutatedBytes = await readFile(filePath);

  return {
    bytes,
    observations: {
      jsonl: jsonlObservations,
      memory: memoryObservations,
      forks: {
        beforeSecondUser: forkBefore.map((entry: any) => entry.id),
        atModel: forkAt.map((entry: any) => entry.id),
        beforeAssistantError: invalidFork,
      },
      compactedContext: observeContext(compactedContext),
      branchSummaryContext: observeContext(branchSummaryContext),
      emptyParentPath: emptyParentPath.map((entry: any) => entry.id),
      transformsAndProjectors: transformObservations,
      typedEmptyActiveTools: {
        entry: {
          type: typedActiveToolsEntries[0].type,
          activeToolNames: typedActiveToolsEntries[0].activeToolNames,
        },
        context: { activeToolNames: typedActiveToolsContext.activeToolNames },
      },
      appendLine: mutatedBytes.subarray(bytes.length).toString("utf8"),
      invalid,
    },
  };
}

// Upstream >=0.84 replaced the v3 session tree (jsonl-storage/memory-storage/
// repo-utils) with a v4 header + mutation log model (session/jsonl{,.ts},
// memory.ts, state.ts): entries carry seq and numeric timestamps, lanes replace
// leaf entries, records capture operations, and contexts build via context.ts.
const fixedNowMs = new Date(fixedNow).getTime();

const v4User = (text: string, timestamp: number) => ({
  role: "user", content: [{ type: "text", text }], timestamp,
});
const v4Assistant = (text: string, timestamp: number, stopReason = "stop") => ({
  role: "assistant", content: [{ type: "text", text }], api: "openai-responses", provider: "openai", model: "gpt-test",
  usage: {
    input: 1, output: 2, cacheRead: 0, cacheWrite: 0, totalTokens: 3,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  },
  stopReason, timestamp,
});
const v4Usage = {
  input: 100, output: 20, cacheRead: 30, cacheWrite: 10, totalTokens: 160,
  cost: { input: 0.1, output: 0.2, cacheRead: 0.01, cacheWrite: 0.02, total: 0.33 },
};

// modifiedAt mirrors filesystem mtimes, the only nondeterministic v4 metadata.
function normalizeV4(value: unknown, root: string): unknown {
  if (Array.isArray(value)) return value.map((item) => normalizeV4(item, root));
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [
      key,
      key === "modifiedAt" ? "<modifiedAt>" : normalizeV4(item, root),
    ]));
  }
  return normalize(value, root);
}

async function upstreamHasV3SessionStorage(upstreamRoot: string): Promise<boolean> {
  try {
    await readFile(path.join(upstreamRoot, "packages/agent/src/harness/session/jsonl-storage.ts"));
    return true;
  } catch {
    return false;
  }
}

async function runV4SessionScript(storage: any, root: string): Promise<unknown> {
  const append = (entry: unknown, lane = "main") => storage.appendEntry(entry, lane);
  await append({ type: "message", id: "root-user", message: v4User("root <>&\u2028\u2029", 1) });
  await append({ type: "message", id: "main-assistant", message: v4Assistant("answer", 2) });
  await append({ type: "message", id: "second-user", message: v4User("continue", 3) });
  await append({ type: "thinking_level_change", id: "thinking", thinkingLevel: "high" });
  await append({ type: "model_change", id: "model", provider: "anthropic", modelId: "claude-test" });
  await append({ type: "active_tools_change", id: "tools", activeToolNames: ["read", "bash"] });
  await append({ type: "active_tools_change", id: "tools-empty", activeToolNames: [] });
  await append({ type: "custom", id: "custom", customType: "state", data: { nested: [1, "two"] } });
  await append({
    type: "compaction", id: "compaction", summary: "prior work",
    retainedTail: [v4User("retained tail", 4)], tokensBefore: 42.5, details: { readFiles: ["a.go"] },
  });
  await append({
    type: "branch_summary", id: "branch-summary", fromId: "compaction",
    summary: "discarded branch work", details: { modifiedFiles: ["b.go"] },
  });
  await storage.setName("  fixture name  ");
  await storage.setLabel("root-user", "  checkpoint  ");
  await storage.setLabel("root-user", undefined);
  await storage.setLabel("second-user", "  branch point  ");
  await storage.createLane("branch", "root-user");
  await append({ type: "message", id: "branch-user", message: v4User("branch", 5) }, "branch");
  await storage.createLane("idle", "second-user");
  await storage.moveLane("idle", null);
  await storage.appendRecord({
    type: "operation_started", id: "op-run", lane: "main", sourceLeafId: "branch-summary",
    intent: {
      kind: "run",
      originalPrompt: [v4User("run prompt", 6)],
      initialMessages: [{ type: "message", id: "queued-1", message: v4User("queued", 7) }],
    },
  });
  await storage.appendRecord({
    type: "step_attempt", id: "step-1", lane: "main", runId: "op-run",
    step: "assistant", attempt: 1, resultEntryId: "main-assistant",
  });
  await storage.appendRecord({
    type: "usage", id: "usage-1", lane: "main", cause: "assistant", runId: "op-run",
    entryId: "main-assistant", attempt: 1, stopReason: "stop", usage: v4Usage,
  });
  const openOperationConflict = await captureError(() => storage.appendRecord({
    type: "operation_started", id: "op-run-2", lane: "main", sourceLeafId: null,
    intent: { kind: "navigation", targetId: null, summarize: false },
  }), root);
  const openBeforeFinish = (await storage.findOpenOperations("main")).map((record: any) => record.id);
  await storage.appendRecord({
    type: "operation_finished", id: "finish-1", lane: "main", runId: "op-run", outcome: "completed",
  });
  return {
    openOperationConflict,
    openBeforeFinish,
    duplicateEntryId: await captureError(() => append({ type: "custom", id: "root-user", customType: "dup" }), root),
    missingLaneAppend: await captureError(() => append({ type: "custom", id: "orphan", customType: "x" }, "missing-lane"), root),
    laneAlreadyExists: await captureError(() => storage.createLane("branch", null), root),
    labelTargetMissing: await captureError(() => storage.setLabel("ghost", "x"), root),
    invalidLimit: await captureError(() => storage.findEntries({ limit: 0 }), root),
  };
}

async function observeV4Storage(storage: any, root: string): Promise<unknown> {
  const lanes = await storage.getLanes();
  const mainLeaf = lanes.find((pointer: any) => pointer.lane === "main")?.leafId ?? null;
  const entries = await storage.findEntries({ order: "oldestFirst" });
  const branch = mainLeaf === null ? [] : await storage.findEntriesOnBranch({ start: mainLeaf, order: "oldestFirst" });
  return normalizeV4({
    metadata: await storage.getMetadata(),
    lanes,
    entries,
    entryIds: entries.map((entry: any) => entry.id),
    branchIds: branch.map((entry: any) => entry.id),
    messageIds: (await storage.findEntries({ type: "message", order: "oldestFirst" })).map((entry: any) => entry.id),
    customStateIds: (await storage.findEntries({ type: "custom", customType: "state" })).map((entry: any) => entry.id),
    pagedNewestIds: (await storage.findEntries({ limit: 3 })).map((entry: any) => entry.id),
    branchToCompactionIds: mainLeaf === null ? [] :
      (await storage.findEntriesOnBranch({ start: mainLeaf, stopAtType: "compaction" })).map((entry: any) => entry.id),
    records: await storage.findRecords(),
    usageRecordIds: (await storage.findRecords({ lane: "main", type: "usage" })).map((record: any) => record.id),
    openOperations: (await storage.findOpenOperations("main")).map((record: any) => record.id),
    log: await storage.getLog(),
    logAfterSeqIds: (await storage.getLog({ afterSeq: 10, limit: 3 })).map((item: any) => item.seq),
    stats: await storage.getStats(),
    name: (await storage.getName()) ?? null,
    labels: {
      root: (await storage.getLabel("root-user")) ?? null,
      second: (await storage.getLabel("second-user")) ?? null,
    },
  }, root);
}

async function generateSessionFixtureV4(upstreamRoot: string, root: string): Promise<{ bytes: Uint8Array; observations: unknown }> {
  const load = (rel: string) => import(pathToFileURL(path.join(upstreamRoot, rel)).href);
  const storageModule = await load("packages/agent/src/harness/session/jsonl/storage.ts");
  const memoryModule = await load("packages/agent/src/harness/session/memory.ts");
  const sessionModule = await load("packages/agent/src/harness/session/session.ts");
  const contextModule = await load("packages/agent/src/harness/session/context.ts");
  const envModule = await load("packages/agent/src/harness/env/nodejs.ts");

  const env = new envModule.NodeExecutionEnv({ cwd: root });
  const filePath = path.join(root, "session.jsonl");
  const header = {
    kind: "header", version: 4, id: "session-fixed", createdAt: fixedNowMs, cwd: "/fixture/project",
    parentSessionId: "parent-session", metadata: { profile: "reviewer", nested: { enabled: true } },
  };
  let scriptErrors: unknown;
  await withFixedDate(async () => {
    const storage = await storageModule.JsonlSessionStorage.create(env, filePath, header);
    scriptErrors = await runV4SessionScript(storage, root);
  });
  const bytes = await readFile(filePath);
  const reopened = await storageModule.JsonlSessionStorage.load(env, filePath);
  const jsonlObservations = await observeV4Storage(reopened, root);

  const memory = new memoryModule.InMemorySessionStorage({
    id: "session-fixed", createdAt: fixedNowMs, parentSessionId: "parent-session",
  });
  let memoryScriptErrors: unknown;
  await withFixedDate(async () => {
    memoryScriptErrors = await runV4SessionScript(memory, root);
  });
  const memoryObservations = await observeV4Storage(memory, root);

  const forkHeader = (id: string) => ({
    kind: "header", version: 4, id, createdAt: fixedNowMs, cwd: "/fixture/project", parentSessionId: "session-fixed",
  });
  const observeFork = async (storage: any) => normalizeV4({
    entryIds: (await storage.findEntries({ order: "oldestFirst" })).map((entry: any) => entry.id),
    lanes: await storage.getLanes(),
    name: (await storage.getName()) ?? null,
    labels: { second: (await storage.getLabel("second-user")) ?? null },
  }, root);
  let forks: unknown;
  await withFixedDate(async () => {
    const before = await reopened.fork(path.join(root, "fork-before.jsonl"), forkHeader("fork-before"), {
      entryId: "second-user", position: "before",
    });
    const at = await reopened.fork(path.join(root, "fork-at.jsonl"), forkHeader("fork-at"), {
      entryId: "main-assistant", position: "at",
    });
    const tree = await reopened.fork(path.join(root, "fork-tree.jsonl"), forkHeader("fork-tree"), { scope: "tree" });
    forks = {
      before: await observeFork(before),
      at: await observeFork(at),
      tree: await observeFork(tree),
      treeBytes: normalizeV4((await readFile(path.join(root, "fork-tree.jsonl"))).toString("utf8"), root),
      invalidTarget: await captureError(() => reopened.fork(
        path.join(root, "fork-invalid.jsonl"), forkHeader("fork-invalid"), { entryId: "thinking" },
      ), root),
    };
  });

  const lanes = await reopened.getLanes();
  const mainLeaf = lanes.find((pointer: any) => pointer.lane === "main")!.leafId;
  const branchPath = await reopened.findEntriesOnBranch({ start: mainLeaf, order: "oldestFirst" });
  const compactedContext = contextModule.buildSessionContext(branchPath);
  const projectorPath = [
    { type: "message", id: "p-root", parentId: null, seq: 1, timestamp: fixedNowMs, message: v4User("transform root", 10) },
    { type: "custom", id: "p-constructor", parentId: "p-root", seq: 2, timestamp: fixedNowMs, customType: "constructor_state", data: { label: "constructor" } },
    { type: "custom", id: "p-call", parentId: "p-constructor", seq: 3, timestamp: fixedNowMs, customType: "call_state" },
    { type: "custom", id: "p-drop", parentId: "p-call", seq: 4, timestamp: fixedNowMs, customType: "noise" },
    { type: "message", id: "p-deferred", parentId: "p-drop", seq: 5, timestamp: fixedNowMs, message: v4Assistant("deferred work", 11, "deferred") },
    { type: "branch_summary", id: "p-empty-summary", parentId: "p-deferred", seq: 6, timestamp: fixedNowMs, fromId: "p-root", summary: "" },
    { type: "message", id: "p-tail", parentId: "p-empty-summary", seq: 7, timestamp: fixedNowMs, message: v4User("tail", 12) },
  ];
  const projectorContexts = {
    default: observeContext(contextModule.buildSessionContext(projectorPath)),
    projected: observeContext(contextModule.buildSessionContext(projectorPath, {
      entryTransforms: [(entries: any[]) => entries.filter((entry: any) => entry.id !== "p-drop")],
      entryProjectors: {
        constructor_state: () => [v4User("constructor projector", 20)],
        call_state: () => [v4User("call projector", 21)],
      },
    })),
  };

  const validHeader = '{"kind":"header","version":4,"id":"s","createdAt":0,"cwd":"/c"}';
  const invalidCases = [
    { name: "missing-header", content: "" },
    { name: "v3-header", content: '{"type":"session","version":3,"id":"s","timestamp":"t","cwd":"/c"}\n' },
    { name: "unsupported-version", content: '{"kind":"header","version":5,"id":"s","createdAt":0,"cwd":"/c"}\n' },
    { name: "metadata-array", content: '{"kind":"header","version":4,"id":"s","createdAt":0,"cwd":"/c","metadata":[]}\n' },
    { name: "unknown-mutation", content: `${validHeader}\n{"kind":"bogus","seq":1}\n` },
    { name: "non-consecutive-seq", content: `${validHeader}\n{"kind":"entry","lane":"main","type":"custom","id":"e","customType":"x","parentId":null,"seq":2,"timestamp":0}\n` },
    { name: "missing-parent", content: `${validHeader}\n{"kind":"entry","lane":"main","type":"custom","id":"e","customType":"x","parentId":"ghost","seq":1,"timestamp":0}\n` },
    { name: "dangling-lane", content: `${validHeader}\n{"kind":"lane","seq":1,"lane":"side","leafId":"missing"}\n` },
  ];
  const invalid: unknown[] = [];
  for (const fixtureCase of invalidCases) {
    const invalidPath = path.join(root, `${fixtureCase.name}.jsonl`);
    await writeFile(invalidPath, fixtureCase.content);
    invalid.push({
      name: fixtureCase.name,
      content: fixtureCase.content,
      error: await captureError(() => storageModule.JsonlSessionStorage.load(env, invalidPath), root),
    });
  }

  const keptEntryLine = '{"kind":"entry","lane":"main","type":"custom","id":"kept","customType":"x","parentId":null,"seq":1,"timestamp":0}';
  const repair = async (name: string, content: string) => {
    const repairPath = path.join(root, `${name}.jsonl`);
    await writeFile(repairPath, content);
    const repaired = await storageModule.JsonlSessionStorage.load(env, repairPath);
    return {
      content,
      entryIds: (await repaired.findEntries({ order: "oldestFirst" })).map((entry: any) => entry.id),
      repairedContent: (await readFile(repairPath)).toString("utf8"),
    };
  };
  const repairs = {
    tornTail: await repair("torn-tail", `${validHeader}\n${keptEntryLine}\n{"kind":"en`),
    unterminatedTail: await repair("unterminated-tail", `${validHeader}\n${keptEntryLine}`),
  };

  await withFixedDate(async () => {
    await reopened.appendEntry({
      type: "custom", id: "appended-fixed", customType: "after-rehydrate", data: { text: "<>&\u2028\u2029" },
    }, "main");
  });
  const mutatedBytes = await readFile(filePath);

  let sessionApi: unknown;
  await withFixedDate(async () => {
    let generated = 0;
    const session = new sessionModule.Session(memory, { idGenerator: { next: () => `gen-${++generated}` } });
    const appendedMessageId = await session.appendMessage(v4User("appended via session", 8));
    const viewCustomId = await session.view("branch").appendCustomEntry("note", { via: "view" });
    const newestMessage = await session.findEntryOnBranch({ type: "message" });
    sessionApi = normalizeV4({
      appendedMessageId,
      viewCustomId,
      mainLeaf: await session.getLeafId(),
      branchLeaf: await session.view("branch").getLeafId(),
      newestMessageId: newestMessage?.id ?? null,
      stats: await session.getStats(),
    }, root);
  });

  return {
    bytes,
    observations: {
      jsonl: jsonlObservations,
      memory: memoryObservations,
      scriptErrors: normalizeV4(scriptErrors, root),
      memoryScriptErrors: normalizeV4(memoryScriptErrors, root),
      forks,
      compactedContext: observeContext(compactedContext),
      projectorContexts,
      sessionApi,
      appendLine: mutatedBytes.subarray(bytes.length).toString("utf8"),
      invalid,
      repairs: normalizeV4(repairs, root),
    },
  };
}

async function generateRepoFixtureV4(upstreamRoot: string, root: string): Promise<unknown> {
  const load = (rel: string) => import(pathToFileURL(path.join(upstreamRoot, rel)).href);
  const repoModule = await load("packages/agent/src/harness/session/jsonl.ts");
  const memoryModule = await load("packages/agent/src/harness/session/memory.ts");
  const envModule = await load("packages/agent/src/harness/env/nodejs.ts");
  const env = new envModule.NodeExecutionEnv({ cwd: root });
  const provisioned = [
    { type: "message", id: "root-user", message: v4User("root", 1) },
    { type: "message", id: "main-assistant", message: v4Assistant("answer", 2) },
    { type: "message", id: "second-user", message: v4User("continue", 3) },
  ];
  const sortById = (metadata: any[]) => [...metadata].sort((left, right) => compareASCII(left.id, right.id));
  const entriesOf = (session: any) => session.findEntries({ order: "oldestFirst" });

  return await withFixedDate(async () => {
    const memoryRepo = new memoryModule.InMemorySessionRepo();
    const memorySource = await memoryRepo.create({ id: "memory-source" });
    for (const entry of provisioned) await memorySource.appendEntry(entry, "main");
    const memoryMetadata = await memorySource.getMetadata();
    const memoryOpened = await memoryRepo.open(memoryMetadata);
    const memoryBefore = await memoryRepo.fork(memoryMetadata, { entryId: "second-user", position: "before", id: "memory-before" });
    const memoryAt = await memoryRepo.fork(memoryMetadata, { entryId: "main-assistant", position: "at", id: "memory-at" });
    const memoryFull = await memoryRepo.fork(memoryMetadata, { id: "memory-full" });
    const memoryTree = await memoryRepo.fork(memoryMetadata, { scope: "tree", id: "memory-tree" });
    const memoryListed = await memoryRepo.list();
    const memoryDuplicate = await captureError(() => memoryRepo.create({ id: "memory-source" }), root);
    await memoryRepo.delete(memoryMetadata);
    const memoryOpenAfterDelete = await captureError(() => memoryRepo.open(memoryMetadata), root);

    const jsonlRepo = new repoModule.JsonlSessionRepo({ fs: env, sessionsRoot: path.join(root, "repo-sessions") });
    const jsonlSource = await jsonlRepo.create({
      cwd: "/tmp/my-project", id: "jsonl-source",
      metadata: { "10": "ten", "2": "two", profile: "reviewer", nested: { z: 1, a: 2 } },
    });
    for (const entry of provisioned) await jsonlSource.appendEntry(entry, "main");
    const jsonlOther = await jsonlRepo.create({ cwd: "/tmp/other-project", id: "jsonl-other" });
    const jsonlMetadata = await jsonlSource.getMetadata();
    const jsonlOtherMetadata = await jsonlOther.getMetadata();
    const jsonlOpened = await jsonlRepo.open(jsonlMetadata);
    const jsonlListByCwd = await jsonlRepo.list({ cwd: "/tmp/my-project" });
    const jsonlListAll = await jsonlRepo.list();
    const jsonlBefore = await jsonlRepo.fork(jsonlMetadata, { cwd: "/tmp/target", id: "jsonl-before", entryId: "second-user" });
    const jsonlInherited = await jsonlRepo.fork(jsonlMetadata, { cwd: "/tmp/target", id: "jsonl-inherited" });
    const jsonlTree = await jsonlRepo.fork(jsonlMetadata, {
      cwd: "/tmp/target", id: "jsonl-tree", scope: "tree",
      metadata: { profile: "writer" }, parentSessionId: "override-parent",
    });
    const invalidIdCreate = await captureError(() => jsonlRepo.create({ cwd: "/tmp/target", id: "-bad-" }), root);
    const duplicateIdCreate = await captureError(() => jsonlRepo.create({ cwd: "/tmp/my-project", id: "jsonl-source" }), root);
    const beforeMetadata = await jsonlBefore.getMetadata();
    const inheritedMetadata = await jsonlInherited.getMetadata();
    const treeMetadata = await jsonlTree.getMetadata();
    const sourceBytes = (await readFile(jsonlMetadata.path)).toString("utf8");
    const treeBytes = (await readFile(treeMetadata.path)).toString("utf8");
    const sourceExistsBeforeDelete = get(await env.exists(jsonlMetadata.path));
    await jsonlRepo.delete(jsonlMetadata);
    const sourceExistsAfterDelete = get(await env.exists(jsonlMetadata.path));
    const jsonlOpenAfterDelete = await captureError(() => jsonlRepo.open(jsonlMetadata), root);

    return normalizeV4({
      memory: {
        sourceMetadata: memoryMetadata,
        openedMetadata: await memoryOpened.getMetadata(),
        listed: sortById(memoryListed),
        beforeEntries: await entriesOf(memoryBefore),
        atEntries: await entriesOf(memoryAt),
        fullEntries: await entriesOf(memoryFull),
        treeEntries: await entriesOf(memoryTree),
        treeLanes: await memoryTree.getLanes(),
        duplicateCreate: memoryDuplicate,
        openAfterDelete: memoryOpenAfterDelete,
      },
      jsonl: {
        sourceMetadata: jsonlMetadata,
        otherMetadata: jsonlOtherMetadata,
        openedMetadata: await jsonlOpened.getMetadata(),
        openedEntries: await entriesOf(jsonlOpened),
        listByCwd: sortById(jsonlListByCwd),
        listAll: sortById(jsonlListAll),
        encodedCwdDirectory: path.basename(path.dirname(jsonlMetadata.path)),
        before: { metadata: beforeMetadata, entries: await entriesOf(jsonlBefore) },
        inherited: { metadata: inheritedMetadata, entries: await entriesOf(jsonlInherited) },
        tree: { metadata: treeMetadata, entries: await entriesOf(jsonlTree), lanes: await jsonlTree.getLanes() },
        sourceBytes,
        treeBytes,
        invalidIdCreate,
        duplicateIdCreate,
        sourceExistsBeforeDelete,
        sourceExistsAfterDelete,
        openAfterDelete: jsonlOpenAfterDelete,
      },
    }, root);
  });
}

function compareASCII(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}

function normalizeRepoValue(value: unknown, root: string): unknown {
  if (typeof value === "string") {
    return normalize(value.replace(/\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-\d{3}Z_/g, "<createdAt>_"), root);
  }
  if (Array.isArray(value)) return value.map((item) => normalizeRepoValue(item, root));
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [
      key,
      key === "createdAt" ? "<createdAt>" : normalizeRepoValue(item, root),
    ]));
  }
  return value;
}

function observeRepoMetadata(metadata: any, root: string): unknown {
  return normalizeRepoValue(metadata, root);
}

function observeRepoMetadataList(metadata: any[], root: string): unknown[] {
  return [...metadata]
    .sort((left, right) => compareASCII(left.id, right.id))
    .map((item) => observeRepoMetadata(item, root));
}

function normalizeRepoJSONL(content: string, metadata: any, root: string): string {
  const pathTimestamp = String(metadata.createdAt).replace(/[:.]/g, "-");
  return normalizeRepoValue(
    content.split(String(metadata.createdAt)).join("<createdAt>").split(pathTimestamp).join("<createdAt>"),
    root,
  ) as string;
}

async function generateRepoFixture(upstreamRoot: string, root: string): Promise<unknown> {
  const memoryRepoModule = await import(pathToFileURL(path.join(upstreamRoot, "packages/agent/src/harness/session/memory-repo.ts")).href);
  const jsonlRepoModule = await import(pathToFileURL(path.join(upstreamRoot, "packages/agent/src/harness/session/jsonl-repo.ts")).href);
  const envModule = await import(pathToFileURL(path.join(upstreamRoot, "packages/agent/src/harness/env/nodejs.ts")).href);
  const env = new envModule.NodeExecutionEnv({ cwd: root });
  const repoEntries = fixedEntries.slice(0, 3);

  return await withFixedDate(async () => {
    const memoryRepo = new memoryRepoModule.InMemorySessionRepo();
    const memorySource = await memoryRepo.create({ id: "memory-source" });
    for (const entry of repoEntries) await memorySource.getStorage().appendEntry(entry);
    const memoryMetadata = await memorySource.getMetadata();
    const memoryOpened = await memoryRepo.open(memoryMetadata);
    const memoryBefore = await memoryRepo.fork(memoryMetadata, { entryId: "second-user", id: "memory-before" });
    const memoryAt = await memoryRepo.fork(memoryMetadata, {
      entryId: "main-assistant", position: "at", id: "memory-at",
    });
    const memoryFull = await memoryRepo.fork(memoryMetadata, { id: "memory-full" });
    const memoryListed = await memoryRepo.list();
    await memoryRepo.delete(memoryMetadata);
    const memoryOpenAfterDelete = await captureError(() => memoryRepo.open(memoryMetadata), root);

    const jsonlRepo = new jsonlRepoModule.JsonlSessionRepo({
      fs: env,
      sessionsRoot: path.join(root, "repo-sessions"),
    });
    const jsonlSource = await jsonlRepo.create({
      cwd: "/tmp/my-project",
      id: "jsonl-source",
      metadata: { "10": "ten", "2": "two", profile: "reviewer", nested: { z: 1, a: 2 } },
    });
    for (const entry of repoEntries) await jsonlSource.getStorage().appendEntry(entry);
    const jsonlOther = await jsonlRepo.create({ cwd: "/tmp/other-project", id: "jsonl-other" });
    const jsonlMetadata = await jsonlSource.getMetadata();
    const jsonlOtherMetadata = await jsonlOther.getMetadata();
    const jsonlOpened = await jsonlRepo.open(jsonlMetadata);
    const jsonlListByCwd = await jsonlRepo.list({ cwd: "/tmp/my-project" });
    const jsonlListAll = await jsonlRepo.list();
    const jsonlBefore = await jsonlRepo.fork(jsonlMetadata, {
      cwd: "/tmp/target", id: "jsonl-before", entryId: "second-user",
    });
    const jsonlInherited = await jsonlRepo.fork(jsonlMetadata, { cwd: "/tmp/target", id: "jsonl-inherited" });
    const jsonlOverridden = await jsonlRepo.fork(jsonlMetadata, {
      cwd: "/tmp/target", id: "jsonl-overridden",
      parentSessionPath: "/fixture/override-parent.jsonl",
      metadata: { profile: "writer" },
    });
    const beforeMetadata = await jsonlBefore.getMetadata();
    const inheritedMetadata = await jsonlInherited.getMetadata();
    const overriddenMetadata = await jsonlOverridden.getMetadata();
    const sourceExistsBeforeDelete = get(await env.exists(jsonlMetadata.path));
    await jsonlRepo.delete(jsonlMetadata);
    const sourceExistsAfterDelete = get(await env.exists(jsonlMetadata.path));
    const jsonlOpenAfterDelete = await captureError(() => jsonlRepo.open(jsonlMetadata), root);

    const noncanonicalPath = path.join(root, "noncanonical-source.jsonl");
    const noncanonicalBytes = [
      '{ "type" : "session", "version" : 3, "id" : "noncanonical", "timestamp" : "2026-02-03T04:05:06.789Z", "cwd" : "/tmp/noncanonical", "metadata" : { "10" : "ten", "2" : "two", "profile" : "reviewer", "nested" : { "z" : 1, "a" : 2 } } }',
      '{ "type" : "message", "id" : "noncanonical-user", "parentId" : null, "timestamp" : "2026-02-03T04:07:00.000Z", "message" : { "role" : "user", "content" : [ { "type" : "text", "text" : "noncanonical" } ], "timestamp" : 30 } }',
      "",
    ].join("\n");
    get(await env.writeFile(noncanonicalPath, noncanonicalBytes));
    const noncanonicalSession = await jsonlRepo.open({
      id: "noncanonical", createdAt: fixedNow, cwd: "/tmp/noncanonical", path: noncanonicalPath,
    });
    const noncanonicalMetadata = await noncanonicalSession.getMetadata();
    const reserialized = await jsonlRepo.fork(noncanonicalMetadata, {
      cwd: "/tmp/reserialized", id: "jsonl-reserialized",
    });
    const reserializedMetadata = await reserialized.getMetadata();
    const reserializedBytes = get(await env.readTextFile(reserializedMetadata.path));

    return normalizeRepoValue({
      memory: {
        sourceMetadata: observeRepoMetadata(memoryMetadata, root),
        openedSameObject: memoryOpened === memorySource,
        listed: observeRepoMetadataList(memoryListed, root),
        beforeEntries: await memoryBefore.getEntries(),
        atEntries: await memoryAt.getEntries(),
        fullEntries: await memoryFull.getEntries(),
        openAfterDelete: memoryOpenAfterDelete,
      },
      jsonl: {
        sourceMetadata: observeRepoMetadata(jsonlMetadata, root),
        otherMetadata: observeRepoMetadata(jsonlOtherMetadata, root),
        openedMetadata: observeRepoMetadata(await jsonlOpened.getMetadata(), root),
        openedEntries: await jsonlOpened.getEntries(),
        listByCwd: observeRepoMetadataList(jsonlListByCwd, root),
        listAll: observeRepoMetadataList(jsonlListAll, root),
        encodedCwdDirectory: path.basename(path.dirname(jsonlMetadata.path)),
        before: { metadata: observeRepoMetadata(beforeMetadata, root), entries: await jsonlBefore.getEntries() },
        inherited: { metadata: observeRepoMetadata(inheritedMetadata, root), entries: await jsonlInherited.getEntries() },
        overridden: { metadata: observeRepoMetadata(overriddenMetadata, root), entries: await jsonlOverridden.getEntries() },
        sourceExistsBeforeDelete,
        sourceExistsAfterDelete,
        openAfterDelete: jsonlOpenAfterDelete,
        noncanonicalSourceBytes: noncanonicalBytes,
        noncanonicalMetadata: observeRepoMetadata(noncanonicalMetadata, root),
        reserializedMetadata: observeRepoMetadata(reserializedMetadata, root),
        reserializedBytes: normalizeRepoJSONL(reserializedBytes, reserializedMetadata, root),
      },
    }, root);
  });
}

async function resultRecord<T>(promise: Promise<Result<T>>, root: string): Promise<unknown> {
  const result = await promise;
  if (result.ok) return normalize({ ok: true, value: result.value }, root);
  return normalize({ ok: false, error: { code: result.error.code, message: result.error.message, path: result.error.path } }, root);
}

async function mappedResultRecord(
  promise: Promise<any>,
  root: string,
  mapValue: (value: any) => unknown,
): Promise<unknown> {
  const result = await promise;
  if (result.ok) return normalize({ ok: true, value: mapValue(result.value) }, root);
  return normalize({
    ok: false,
    error: { code: result.error.code, message: result.error.message, path: result.error.path },
  }, root);
}

async function generateEnvFixture(upstreamRoot: string, root: string): Promise<unknown> {
  const envModule = await import(pathToFileURL(path.join(upstreamRoot, "packages/agent/src/harness/env/nodejs.ts")).href);
  let env = new envModule.NodeExecutionEnv({ cwd: root, shellEnv: { BASE_VALUE: "base" } });
  const envSource = await readFile(path.join(upstreamRoot,"packages/agent/src/harness/env/nodejs.ts"),"utf8");
  if(envSource.includes("context.abortSignal")) {
    const original = env;
    const optionMethods = new Set(["exec","readTextLines","createDir","remove"]);
    const twoArgumentMethods = new Set(["writeFile","appendFile","renameFile"]);
    env = new Proxy(original,{get(target,property) {
      const method = Reflect.get(target,property);
      if(typeof method !== "function") return method;
      return (...args:any[]) => {
        const name = String(property);
        if(name === "cleanup") return method.call(target,{});
        if(optionMethods.has(name)) return method.call(target,args[0],args[1],{abortSignal:args[1]?.abortSignal});
        if(twoArgumentMethods.has(name)) return method.call(target,args[0],args[1],{abortSignal:args[2]});
        return method.call(target,args[0],{abortSignal:name === "createTempFile" ? args[0]?.abortSignal : args[1]});
      };
    }});
  }

  get(await env.writeFile("nested/lines.txt", "one\r\ntwo\nthree\n"));
  get(await env.writeFile("target.txt", new Uint8Array([0, 1, 2, 255])));
  get(await env.createDir("empty-remove"));
  await symlink("target.txt", path.join(root, "target-link"));
  const callbackChunks: string[] = [];
  const exec = await resultRecord(env.exec(
    'printf "out:$BASE_VALUE:$EXTRA"; printf "err" >&2; exit 7',
    {
      env: { EXTRA: "extra" },
      onStdout: (chunk: string) => callbackChunks.push(`stdout:${chunk}`),
      onStderr: (chunk: string) => callbackChunks.push(`stderr:${chunk}`),
    },
  ), root);
  const aborted = new AbortController();
  aborted.abort();
  const callbackError = await resultRecord(env.exec("printf boom", {
    onStdout: () => { throw new Error("callback boom"); },
  }), root);
  const tempDir = get(await env.createTempDir("orb-harness-"));
  const tempFile = get(await env.createTempFile({ prefix: "pre-", suffix: ".tmp" }));
  const cleanupPaths = [tempDir, path.dirname(tempFile)];
  try {
    const tempFileExists = get(await env.exists(tempFile));
    const binary = get(await env.readBinaryFile("target.txt"));
    const symlinkInfo = get(await env.fileInfo("target-link"));
    const negativeMaxLines = await resultRecord(env.readTextLines("nested/lines.txt", { maxLines: -1 }), root);
    const emptyDirectoryRemove = await resultRecord(env.remove("empty-remove", { recursive: false, force: true }), root);
    const signaledExec = await resultRecord(env.exec("kill -9 $$"), root);
    get(await env.writeFile("abort/remove.txt", "remove me"));
    const preAbortedTempDir = await (env as any).createTempDir("orb-aborted-", aborted.signal);
    const preAbortedTempFile = await (env as any).createTempFile(
      { prefix: "aborted-", suffix: ".tmp", abortSignal: aborted.signal },
    );
    if (preAbortedTempDir.ok) cleanupPaths.push(preAbortedTempDir.value);
    if (preAbortedTempFile.ok) cleanupPaths.push(path.dirname(preAbortedTempFile.value));
    const preAborted = {
      absolutePath: await resultRecord((env as any).absolutePath("/a/../b", aborted.signal), root),
      joinPath: await resultRecord((env as any).joinPath([root, "nested", "..", "target.txt"], aborted.signal), root),
      readTextFile: await resultRecord(env.readTextFile("target.txt", aborted.signal), root),
      readTextLines: await resultRecord(env.readTextLines("nested/lines.txt", { abortSignal: aborted.signal }), root),
      readBinaryFile: await resultRecord(env.readBinaryFile("target.txt", aborted.signal), root),
      writeFile: await resultRecord(env.writeFile("abort/blocked.txt", "blocked", aborted.signal), root),
      appendFile: await resultRecord((env as any).appendFile("abort/appended.txt", "appended", aborted.signal), root),
      fileInfo: await mappedResultRecord((env as any).fileInfo("target.txt", aborted.signal), root, (info) => ({
        name: info.name, path: info.path, kind: info.kind, size: info.size,
      })),
      listDir: await resultRecord(env.listDir(".", aborted.signal), root),
      canonicalPath: await resultRecord((env as any).canonicalPath("target.txt", aborted.signal), root),
      exists: await resultRecord((env as any).exists("target.txt", aborted.signal), root),
      createDir: await resultRecord((env as any).createDir("abort/created", {
        recursive: true, abortSignal: aborted.signal,
      }), root),
      remove: await resultRecord((env as any).remove("abort/remove.txt", {
        force: false, abortSignal: aborted.signal,
      }), root),
      createTempDir: await mappedResultRecord(
        Promise.resolve(preAbortedTempDir),
        root,
        (createdPath) => path.basename(createdPath).startsWith("orb-aborted-"),
      ),
      createTempFile: await mappedResultRecord(
        Promise.resolve(preAbortedTempFile),
        root,
        (createdPath) => path.basename(createdPath).startsWith("aborted-") && createdPath.endsWith(".tmp"),
      ),
    };
    return {
      absolutePath: await resultRecord(env.absolutePath("nested/../target.txt"), root),
      absolutePathAlreadyAbsolute: await resultRecord(env.absolutePath("/a/../b"), root),
      joinPath: await resultRecord(env.joinPath([root, "nested", "..", "target.txt"]), root),
      readTextLines: await resultRecord(env.readTextLines("nested/lines.txt", { maxLines: 2 }), root),
      negativeMaxLines,
      readBinary: { ok: true, value: Array.from(binary) },
      symlinkInfo: normalize({
        ok: true,
        value: { name: symlinkInfo.name, path: symlinkInfo.path, kind: symlinkInfo.kind, size: symlinkInfo.size },
      }, root),
      symlinkCanonical: await resultRecord(env.canonicalPath("target-link"), root),
      missingExists: await resultRecord(env.exists("missing"), root),
      missingRead: await resultRecord(env.readTextFile("missing"), root),
      directoryRead: await resultRecord(env.readTextFile("nested"), root),
      listFile: await resultRecord(env.listDir("target.txt"), root),
      emptyDirectoryRemove,
      exec,
      signaledExec,
      callbackChunks: callbackChunks.sort(),
      preAbortedExec: await resultRecord(env.exec("printf never", { abortSignal: aborted.signal }), root),
      preAborted,
      invalidTimeout: await resultRecord(env.exec("printf never", { timeout: 0 }), root),
      timedOutExec: await resultRecord(env.exec("sleep 1", { timeout: 0.01 }), root),
      callbackError,
      temp: {
        dirPrefix: path.basename(tempDir).startsWith("orb-harness-"),
        filePrefix: path.basename(tempFile).startsWith("pre-"),
        fileSuffix: tempFile.endsWith(".tmp"),
        fileExists: tempFileExists,
      },
    };
  } finally {
    await env.cleanup();
    await Promise.all(cleanupPaths.map((cleanupPath) => rm(cleanupPath, { recursive: true, force: true })));
  }
}

export async function generateF6Harness(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const transactionCodec = await readFile(path.join(upstreamRoot, "packages/agent/src/harness/session/jsonl/codec.ts"), "utf8").catch(() => "");
  if (transactionCodec.includes("isJsonlStorageHeader")) {
    const transactionRoot = path.join(outputRoot, "F6HarnessTransactions");
    await generateF6HarnessTransactions(upstreamRoot, transactionRoot);
    await generateF6HarnessTransactionState(upstreamRoot, transactionRoot);
    await generateF6HarnessTransactionFiles(upstreamRoot, transactionRoot);
    await generateF6HarnessTransactionMigration(upstreamRoot, transactionRoot);
    await generateF6HarnessTransactionForks(upstreamRoot, transactionRoot);
    await generateF6HarnessTransactionRepos(upstreamRoot, transactionRoot);
    await generateF6HarnessV3Projection(upstreamRoot, transactionRoot);
    const root = await mkdtemp(path.join(os.tmpdir(), "orb-f6-env-"));
    try {
      const familyDir = path.join(outputRoot, "F6Harness"); await mkdir(familyDir,{recursive:true});
      const files = JSON.parse(await readFile(path.join(transactionRoot,"files.json"),"utf8"));
      await writeFile(path.join(familyDir,"session.jsonl"),files.content);
      await writeFile(path.join(familyDir,"observations.json"),JSON.stringify({schemaVersion:1,session:{format:"transactions"}},null,2)+"\n");
      await writeFile(path.join(familyDir,"manifest.json"),JSON.stringify({family:"F6Harness",upstreamCommit,generator:"conformance/extract/f6-harness.ts",source:"packages/agent/src/harness/session/jsonl/storage.ts",files:["session.jsonl","observations.json"]},null,2)+"\n");
      await writeFile(path.join(transactionRoot,"manifest.json"),JSON.stringify({family:"F6HarnessTransactions",upstreamCommit,generator:"conformance/extract/f6-harness.ts",source:"packages/agent/src/harness/session/{commit,in-memory-storage-state,fork,jsonl/storage,jsonl/legacy-v3}.ts",files:["transactions.json","state.json","files.json","migration.json","forks.json","repos.json","v3-projection.jsonl","v3-projection.json"]},null,2)+"\n");
    } finally {await rm(root,{recursive:true,force:true});}
    return;
  }

  const root = await mkdtemp(path.join(os.tmpdir(), "orb-f6-harness-"));
  try {
    const hasV3 = await upstreamHasV3SessionStorage(upstreamRoot);
    const sessionFixture = hasV3
      ? await generateSessionFixture(upstreamRoot, root)
      : await generateSessionFixtureV4(upstreamRoot, root);
    const observations = {
      schemaVersion: 1,
      session: {
        ...(sessionFixture.observations as Record<string, unknown>),
        repos: hasV3
          ? await generateRepoFixture(upstreamRoot, root)
          : await generateRepoFixtureV4(upstreamRoot, root),
      },
      env: await generateEnvFixture(upstreamRoot, root),
    };
    const familyDir = path.join(outputRoot, "F6Harness");
    await mkdir(familyDir, { recursive: true });
    await writeFile(path.join(familyDir, "session.jsonl"), sessionFixture.bytes);
    await writeFile(path.join(familyDir, "observations.json"), `${JSON.stringify(observations, null, 2)}\n`);
    await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({
      family: "F6Harness",
      upstreamCommit,
      generator: "conformance/extract/f6-harness.ts",
      source: "packages/agent/src/harness/{types.ts,messages.ts,env/nodejs.ts,session/*.ts}",
      files: ["session.jsonl", "observations.json"],
    }, null, 2)}\n`);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

/** Transaction storage introduced by released pi v0.85.0; independent of its runtime facade. */
export async function generateF6HarnessTransactions(upstreamRoot: string, outputRoot: string): Promise<void> {
  const load = (rel: string) => import(pathToFileURL(path.join(upstreamRoot, rel)).href);
  const commit = await load("packages/agent/src/harness/session/commit.ts");
  const codec = await load("packages/agent/src/harness/session/jsonl/codec.ts");
  const cases = [
    { name: "empty", firstSeq: 1, writes: [] },
    { name: "entries", firstSeq: 1, writes: [
      { kind: "entry", entry: { id: "a", parentId: null, type: "message", message: v4User("<>&", 1) } },
      { kind: "entry", entry: { id: "b", parentId: "a", type: "custom", customType: "test", data: null } },
    ] },
    { name: "values", firstSeq: 9, writes: [
      { kind: "value", op: "set", namespace: "session", key: "name", value: "sample" },
      { kind: "list", op: "append", namespace: "lane", key: "inbox", value: { entryId: "a", kind: "steer" } },
      { kind: "value", op: "delete", namespace: "session", key: "name" },
      { kind: "list", op: "delete", namespace: "lane", key: "inbox" },
    ] },
    { name: "usage", firstSeq: 20, writes: [
      { kind: "usage", row: { id: "usage", usage: v4Usage, adjustment: true, details: { source: "v3-import" } } },
    ] },
    { name: "duplicate", firstSeq: 1, writes: [
      { kind: "entry", entry: { id: "a", parentId: null, type: "custom", customType: "test" } },
      { kind: "usage", row: { id: "a", usage: v4Usage } },
    ] },
    { name: "missing-parent", firstSeq: 1, writes: [
      { kind: "entry", entry: { id: "a", parentId: "missing", type: "custom", customType: "test" } },
    ] },
  ];
  const observations = cases.map((scenario) => {
    const prepared = commit.prepareStorageCommit(scenario.writes, scenario.firstSeq, fixedNowMs);
    let error: string | null = null;
    try {
      commit.validateCommittedWrites(prepared.writes, scenario.firstSeq, {
        hasEntryOrUsageId: () => false, hasEntryId: () => false,
      });
    } catch (failure) { error = (failure as Error).message; }
    return { ...scenario, timestamp: fixedNowMs, prepared, error };
  });
  const headers = [
    { v: 4, kind: "header", id: "session", storageVersion: 1, createdAt: fixedNowMs, cwd: "/fixture" },
    { v: 4, kind: "header", id: "session", storageVersion: 2, createdAt: 0, cwd: "", nextSeq: 4 },
    { v: 4, kind: "header", id: "session", storageVersion: 1, createdAt: 0, cwd: "", parentSessionId: "a", legacyParentSessionPath: "b" },
    { kind: "header", version: 4, id: "old", createdAt: 0, cwd: "" },
    { v: 4, kind: "header", id: "bad", storageVersion: 0, createdAt: 0, cwd: "" },
    { v: 4, kind: "header", id: "bad", storageVersion: 1, createdAt: 0, cwd: "", nextSeq: 0 },
  ].map((header) => ({ header, valid: codec.isJsonlStorageHeader(header) }));
  await mkdir(outputRoot, { recursive: true });
  await writeFile(path.join(outputRoot, "transactions.json"), `${JSON.stringify({ observations, headers }, null, 2)}\n`);
}

export async function generateF6HarnessTransactionState(upstreamRoot: string, outputRoot: string): Promise<void> {
  const { InMemoryStorageState } = await import(pathToFileURL(path.join(upstreamRoot,
    "packages/agent/src/harness/session/in-memory-storage-state.ts")).href);
  const cases = JSON.parse(await readFile(path.join(outputRoot, "transactions.json"), "utf8"));
  const state = new InMemoryStorageState();
  const batches = cases.observations.filter((c: any) => c.error === null).map((c: any) => c.writes);
  batches.push([
    { kind: "value", op: "set", namespace: "test", key: "z", value: 1 },
    { kind: "value", op: "set", namespace: "test", key: "a", value: 2 },
    { kind: "value", op: "set", namespace: "test", key: "😀", value: 3 },
    { kind: "value", op: "set", namespace: "test", key: "\ue000", value: 4 },
    { kind: "list", op: "append", namespace: "test", key: "list", value: 1 },
    { kind: "list", op: "append", namespace: "test", key: "list", value: 2 },
    { kind: "list", op: "append", namespace: "test", key: "list", value: 3 },
  ]);
  const commits = batches.map((writes: any) => {
    const prepared = state.prepareCommit(writes, fixedNowMs);
    const stats = state.applyValidated(prepared.writes);
    return JSON.parse(JSON.stringify({ writes, prepared, stats }));
  });
  const observations = {
    commits,
    entries: state.scanEntries({}),
    entriesDesc: state.scanEntries({ order: "desc", limit: 1 }),
    usage: state.scanUsage({}),
    branch: state.scanBranch({ start: "b", order: "oldestFirst" }),
    values: state.scanValues({ namespace: "test", key: "", kind: "value" }),
    list: state.readList({ namespace: "test", key: "list", kind: "list" }),
    listDesc: state.readList({ namespace: "test", key: "list", kind: "list" }, { order: "desc", limit: 2 }),
    stats: state.getStats(),
  };
  await writeFile(path.join(outputRoot, "state.json"), `${JSON.stringify(observations, null, 2)}\n`);
}

export async function generateF6HarnessTransactionFiles(upstreamRoot: string, outputRoot: string): Promise<void> {
  const { JsonlStorage } = await import(pathToFileURL(path.join(upstreamRoot,
    "packages/agent/src/harness/session/jsonl/storage.ts")).href);
  const files = new Map<string, string>();
  const fileSystem = {
    readTextFile: async (p: string) => ({ ok: true, value: files.get(p) }),
    writeFile: async (p: string, value: string) => { files.set(p, value); return { ok: true }; },
    appendFile: async (p: string, value: string) => { files.set(p, (files.get(p) ?? "") + value); return { ok: true }; },
    renameFile: async (a: string, b: string) => { files.set(b, files.get(a)!); files.delete(a); return { ok: true }; },
    remove: async (p: string) => { files.delete(p); return { ok: true }; },
  };
  const input = JSON.parse(await readFile(path.join(outputRoot, "transactions.json"), "utf8"));
  const header = input.headers[0].header;
  const options = { fileSystem, path: "session.jsonl", now: () => fixedNowMs };
  const context = {};
  const storage = await JsonlStorage.create(options, header, input.observations[1].writes, context);
  const result = await storage.commit(input.observations[2].writes, context);
  const content = files.get(options.path)!;
  await storage.close(context);
  files.set(options.path, content + '{"kind":"entry"');
  const reopened = await JsonlStorage.open(options, context);
  const repaired = files.get(options.path);
  const entries = await reopened.scanEntries({}, context);
  await reopened.close(context);
  const failures = [];
  for (const broken of ["not json\n", JSON.stringify(header)+"\n"+'{"kind":"unknown","seq":1}\n']) {
    files.set(options.path,broken);
    try { await JsonlStorage.open(options,context); }
    catch(error) { failures.push({content:broken,message:(error as Error).message,cause:((error as Error).cause as Error)?.message}); }
  }
  await writeFile(path.join(outputRoot, "files.json"), `${JSON.stringify({ header, initialWrites: input.observations[1].writes,
    writes: input.observations[2].writes, timestamp: fixedNowMs, result, content, repaired, entries, failures }, null, 2)}\n`);
}

export async function generateF6HarnessTransactionMigration(upstreamRoot: string, outputRoot: string): Promise<void> {
  const { normalizeLegacyV3Records } = await import(pathToFileURL(path.join(upstreamRoot,
    "packages/agent/src/harness/session/jsonl/legacy-v3.ts")).href);
  const records = fixedEntries.slice(0, 13).map((record) => JSON.stringify(record));
  const normalized = normalizeLegacyV3Records(records);
  const retained = fixedEntries.slice(0, 13).filter((entry) => ["message", "custom", "custom_message", "compaction", "branch_summary"].includes(entry.type));
  const ids = new Map(normalized.writes.filter((write: any) => write.kind === "entry").map((write: any, index: number) => [write.id, retained[index].id]));
  const canonical = (input: any): any => {
    if (typeof input === "string") return ids.get(input) ?? input;
    if (Array.isArray(input)) return input.map(canonical);
    if (input && typeof input === "object") return Object.fromEntries(Object.entries(input).map(([key, value]) => [key, canonical(value)]));
    return input;
  };
  await writeFile(path.join(outputRoot, "migration.json"), `${JSON.stringify({ records, normalized: canonical(normalized) }, null, 2)}\n`);
}

export async function generateF6HarnessTransactionForks(upstreamRoot: string, outputRoot: string): Promise<void> {
  const { createForkSnapshot, forkSnapshotWrites } = await import(pathToFileURL(path.join(upstreamRoot,
    "packages/agent/src/harness/session/fork.ts")).href);
  const state = JSON.parse(await readFile(path.join(outputRoot, "state.json"), "utf8"));
  const scalarValues = [
    { address: { namespace: "pi.branch.tip", key: "main", kind: "value" }, value: "b", seq: 3 },
    { address: { namespace: "pi.lane.config", key: "main", kind: "value" }, value: { model: { provider: "openai", modelId: "test" }, thinkingLevel: "off", activeToolNames: [] }, seq: 4 },
    { address: { namespace: "pi.lane.state", key: "main", kind: "value" }, value: { currentOperationId: "running", lastOperationId: null, inbox: [] }, seq: 5 },
    { address: { namespace: "pi.session.name", key: "", kind: "value" }, value: "name", seq: 6 },
    { address: { namespace: "custom", key: "data", kind: "value" }, value: { x: 1 }, seq: 7 },
    { address: { namespace: "pi.entry.label", key: "b", kind: "value" }, value: "tip", seq: 8 },
    { address: { namespace: "pi.result", key: "operation", kind: "value" }, value: {}, seq: 9 },
  ];
  const source = { entries: state.entries, scalarValues };
  const cases = [
    { scope: "tree" }, { scope: "branch", branch: "main" },
    { scope: "branch", branch: "main", entryId: "b", position: "before" },
    { scope: "branch", branch: "main", entryId: "missing", position: "at" },
    { scope: "branch", branch: "missing" },
  ].map((options) => {
    try { const snapshot = createForkSnapshot(source, options); return { options, writes: forkSnapshotWrites(snapshot), nextSeq: snapshot.nextSeq }; }
    catch (error) { return { options, error: (error as Error).message }; }
  });
  await writeFile(path.join(outputRoot, "forks.json"), `${JSON.stringify({ source, cases }, null, 2)}\n`);
}

export async function generateF6HarnessTransactionRepos(upstreamRoot: string, outputRoot: string): Promise<void> {
  const load = (rel: string) => import(pathToFileURL(path.join(upstreamRoot,rel)).href);
  const { JsonlSessionRepo } = await load("packages/agent/src/harness/session/jsonl/repo.ts");
  const { NodeExecutionEnv } = await load("packages/agent/src/harness/env/nodejs.ts");
  const root = await mkdtemp(path.join(os.tmpdir(),"orb-f6-repo-"));
  try {
    const env = new NodeExecutionEnv({cwd:root});
    const repo = new JsonlSessionRepo({fileSystem:env,sessionsRoot:path.join(root,"sessions"),now:()=>fixedNowMs});
    const context = {};
    const cwd = "/fixture/project";
    const session = await repo.create({id:"id / +?",cwd},context);
    const metadata = session.metadata;
    const error = async (run:()=>Promise<unknown>) => {try{await run();return null;}catch(error){return (error as Error).message;}};
    const duplicateOpen = await error(()=>repo.open(metadata,context));
    const deleteOpen = await error(()=>repo.delete(metadata,context));
    const duplicateCreate = await error(()=>repo.create({id:metadata.id,cwd},context));
    const content = await readFile(metadata.path,"utf8");
    await session.close(context);
    const reopened = await repo.open(metadata,context);
    await reopened.close(context);
    const list = await repo.list({cwd},context);
    await repo.delete(metadata,context);
    const missingDelete = await error(()=>repo.delete(metadata,context));
    await repo.close(context);
    const closedList = await error(()=>repo.list(undefined,context));
    await writeFile(path.join(outputRoot,"repos.json"),JSON.stringify(normalizeV4({metadata,content,list,duplicateOpen,deleteOpen,duplicateCreate,missingDelete,closedList},root),null,2)+"\n");
  } finally {await rm(root,{recursive:true,force:true});}
}

export async function generateF6HarnessV3Projection(upstreamRoot: string, outputRoot: string): Promise<void> {
  const { SessionManager } = await import(pathToFileURL(path.join(upstreamRoot,"packages/coding-agent/src/core/session-manager.ts")).href);
  const entries = [
    { type: "session", version: 3, id: "session-fixed", timestamp: fixedNow, cwd: "/fixture/project" },
    ...fixedEntries.slice(0,8),
    { ...fixedEntries[10], parentId:"custom", firstKeptEntryId:"custom" },
    { ...fixedEntries[12], parentId:"compaction" },
  ];
  const manager = SessionManager.inMemory("/fixture/project",undefined,entries);
  const records = [manager.getHeader(),...manager.getEntries()];
  await writeFile(path.join(outputRoot,"v3-projection.jsonl"),records.map((record:any)=>JSON.stringify(record)).join("\n")+"\n");
  await writeFile(path.join(outputRoot,"v3-projection.json"),JSON.stringify({leafId:manager.getLeafId(),messageCount:manager.buildSessionContext().messages.length},null,2)+"\n");
}
