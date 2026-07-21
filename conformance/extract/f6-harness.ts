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
  const branch = await storage.getPathToRoot(leafId);
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
  const compactionPath = await reopened.getPathToRoot("compaction");
  const compactedContext = sessionModule.buildSessionContext(compactionPath);
  const branchSummaryPath = await reopened.getPathToRoot("branch-summary");
  const branchSummaryContext = sessionModule.buildSessionContext(branchSummaryPath);
  const emptyParentPath = await reopened.getPathToRoot("empty-parent");

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
  const env = new envModule.NodeExecutionEnv({ cwd: root, shellEnv: { BASE_VALUE: "base" } });
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
  const tempDir = get(await env.createTempDir("pigo-harness-"));
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
    const preAbortedTempDir = await (env as any).createTempDir("pigo-aborted-", aborted.signal);
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
        (createdPath) => path.basename(createdPath).startsWith("pigo-aborted-"),
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
        dirPrefix: path.basename(tempDir).startsWith("pigo-harness-"),
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
  const root = await mkdtemp(path.join(os.tmpdir(), "pigo-f6-harness-"));
  try {
    const sessionFixture = await generateSessionFixture(upstreamRoot, root);
    const observations = {
      schemaVersion: 1,
      session: {
        ...(sessionFixture.observations as Record<string, unknown>),
        repos: await generateRepoFixture(upstreamRoot, root),
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
