import { createHash } from "node:crypto";
import { access, mkdir, mkdtemp, readFile, rm, utimes, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withUpstreamModelData } from "./upstream-model-data.ts";

type SessionModule = typeof import("../../.upstream/packages/coding-agent/src/core/session-manager.ts");
type ExportModule = typeof import("../../.upstream/packages/coding-agent/src/core/export-html/index.ts");

type ParseCase = {
  name: string;
  input: string;
  expected?: unknown[];
};

type MigrationCase = {
  name: string;
  input: string;
  normalizeGeneratedIDs?: boolean;
  expected?: string;
};

type InvalidUTF8Fixture = {
  inputBase64: string;
  expected: unknown[];
};

const parseCases: ParseCase[] = [
  { name: "empty", input: "" },
  {
    name: "blank-and-malformed-lines",
    input:
      "  \nnot-json\n" +
      '{"type":"session","version":3,"id":"session-a","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/work"}\n' +
      "{broken}\n" +
      '{"type":"message","id":"00000001","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"user","content":null},"future":42}\n',
  },
  {
    name: "crlf-and-final-line-without-lf",
    input:
      '{"type":"session","version":3,"id":"session-b","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/work"}\r\n' +
      '{"type":"custom","customType":"future","data":null,"id":"00000001","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z"}',
  },
  {
    name: "unicode-whitespace-does-not-wrap-json",
    input:
      '\u00a0{"type":"session","version":3,"id":"nbsp","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/work"}\u00a0\n' +
      '\u1680{"type":"session","version":3,"id":"ogham","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/work"}\u1680\n' +
      ' \t{"type":"session","version":3,"id":"json-space","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/work"}\r ',
  },
  {
    name: "outer-unicode-whitespace-is-trimmed-once",
    input:
      '\ufeff\u00a0{"type":"session","version":3,"id":"outer-trim","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/work"}\u1680\ufeff',
  },
  {
    name: "outer-next-line-is-not-js-whitespace",
    input:
      '\u0085{"type":"session","version":3,"id":"next-line","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/work"}\u0085',
  },
];

const migrationCases: MigrationCase[] = [
  {
    name: "v1-linear-compaction-and-unknown-fields",
    normalizeGeneratedIDs: true,
    input:
      '{"type":"session","id":"legacy-v1","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/legacy","futureHeader":true}\n' +
      '{"type":"message","timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"user","content":"hello","timestamp":1},"futureEntry":"kept"}\n' +
      '{"type":"compaction","timestamp":"2025-01-01T00:00:02.000Z","summary":"summary","firstKeptEntryIndex":1,"tokensBefore":12}\n' +
      '{"type":"message","timestamp":"2025-01-01T00:00:03.000Z","message":{"role":"hookMessage","customType":"legacy","content":"hook","display":false,"timestamp":2}}\n',
  },
  {
    name: "v1-invalid-compaction-targets",
    normalizeGeneratedIDs: true,
    input:
      '{"type":"session","id":"legacy-invalid","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/legacy"}\n' +
      '{"type":"compaction","timestamp":"2025-01-01T00:00:01.000Z","summary":"header target","firstKeptEntryIndex":0,"tokensBefore":1}\n' +
      '{"type":"compaction","timestamp":"2025-01-01T00:00:02.000Z","summary":"missing target","firstKeptEntryIndex":99,"tokensBefore":2}\n',
  },
  {
    name: "v2-hook-message-only",
    input:
      '{"type":"session","version":2,"id":"legacy-v2","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/legacy"}\n' +
      '{"type":"message","id":"deadbeef","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"hookMessage","customType":"legacy","content":[],"display":true,"timestamp":1},"unknown":{"x":1}}\n',
  },
  {
    name: "v3-is-untouched",
    input:
      '{"type":"session","version":3,"id":"current","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/current"}\n' +
      '{"type":"message","id":"cafebabe","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"hookMessage","content":"left alone"}}\n',
  },
  {
    name: "v2-json-stringify-normalization",
    input:
      '{ "10" : "ten", "2" : "two", "type" : "session", "version" : 2e0, "id" : "normalize", "timestamp" : "2025-01-01T00:00:00.000Z", "cwd" : "\\u002flegacy" }\n' +
      '{"type":"message","id":"feedface","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","message": { "role" : "hook\\u004dessage", "content" : "\\u0061\\/b", "numbers" : [ -0, 1e+2, 1.2300, 9007199254740993 ], "keys" : { "10" : "ten", "2" : "two", "01" : "leading", "4294967294" : "last-index", "4294967295" : "not-index", "escaped\\u004bey" : "\\u003c" } }, "unknown" : { "3" : "three", "1" : "one", "a" : "\\u0026" }, "spaced" : [ 1, { "0" : 0, "x" : true } ] }\n',
  },
  {
    name: "v2-json-stringify-surrogates",
    input:
      '{"type":"session","version":2,"id":"surrogates","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/legacy"}\n' +
      '{"type":"message","id":"decafbad","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","\\ud800":"high-key","\\udc00":"low-key","\\ud83d\\ude00":"pair-key","message":{"role":"user","content":{"\\ud800":"\\ud800","\\udc00":"\\udc00","\\ud83d\\ude00":"\\ud83d\\ude00"}}}\n',
  },
];

function normalizeGeneratedEntries(entries: Array<Record<string, unknown>>): Array<Record<string, unknown>> {
  const idMap = new Map<string, string>();
  let next = 1;
  for (const entry of entries) {
    if (entry.type === "session" || typeof entry.id !== "string") continue;
    idMap.set(entry.id, next.toString(16).padStart(8, "0"));
    next++;
  }

  for (const entry of entries) {
    if (entry.type === "session") continue;
    if (typeof entry.id === "string") entry.id = idMap.get(entry.id) ?? entry.id;
    if (typeof entry.parentId === "string") entry.parentId = idMap.get(entry.parentId) ?? entry.parentId;
    if (typeof entry.firstKeptEntryId === "string") {
      entry.firstKeptEntryId = idMap.get(entry.firstKeptEntryId) ?? entry.firstKeptEntryId;
    }
    if (typeof entry.fromId === "string") entry.fromId = idMap.get(entry.fromId) ?? entry.fromId;
    if (typeof entry.targetId === "string") entry.targetId = idMap.get(entry.targetId) ?? entry.targetId;
  }
  return entries;
}

function jsonl(entries: unknown[]): string {
  return entries.map((entry) => JSON.stringify(entry)).join("\n") + "\n";
}

function canonicalizeWrittenSession(raw: string, fixtureCwd: string): string {
  const entries = raw
    .trim()
    .split("\n")
    .map((line) => JSON.parse(line) as Record<string, unknown>);
  const idMap = new Map<string, string>();
  let nextID = 1;
  let nextTimestamp = 0;

  for (const entry of entries) {
    if (entry.type === "session") {
      entry.timestamp = "2025-01-01T00:00:00.000Z";
      entry.cwd = fixtureCwd;
      continue;
    }
    if (typeof entry.id === "string") {
      idMap.set(entry.id, nextID.toString(16).padStart(8, "0"));
      nextID++;
    }
  }

  for (const entry of entries) {
    if (entry.type === "session") continue;
    nextTimestamp++;
    entry.timestamp = `2025-01-01T00:00:${nextTimestamp.toString().padStart(2, "0")}.000Z`;
    if (typeof entry.id === "string") entry.id = idMap.get(entry.id) ?? entry.id;
    if (typeof entry.parentId === "string") entry.parentId = idMap.get(entry.parentId) ?? entry.parentId;
    if (typeof entry.firstKeptEntryId === "string") {
      entry.firstKeptEntryId = idMap.get(entry.firstKeptEntryId) ?? entry.firstKeptEntryId;
    }
    if (typeof entry.fromId === "string") entry.fromId = idMap.get(entry.fromId) ?? entry.fromId;
    if (typeof entry.targetId === "string") entry.targetId = idMap.get(entry.targetId) ?? entry.targetId;
  }

  return jsonl(entries);
}

function projectSession(manager: InstanceType<SessionModule["SessionManager"]>) {
  return {
    header: manager.getHeader(),
    leafId: manager.getLeafId(),
    entryTypes: manager.getEntries().map((entry) => entry.type),
    branchIds: manager.getBranch().map((entry) => entry.id),
    sessionName: manager.getSessionName() ?? null,
    entries: manager.getEntries(),
  };
}

async function pathExists(filePath: string): Promise<boolean> {
  try {
    await access(filePath);
    return true;
  } catch {
    return false;
  }
}

async function buildWriteFixture(session: SessionModule) {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), "pi-go-f6-"));
  try {
    const sessionDir = path.join(tempRoot, "sessions");
    await mkdir(sessionDir, { recursive: true });
    const manager = session.SessionManager.create(path.join(tempRoot, "project"), sessionDir, { id: "session-fixed" });
    const sessionFile = manager.getSessionFile();
    if (!sessionFile) throw new Error("upstream did not allocate a session file");

    const userID = manager.appendMessage({
      role: "user",
      content: "hello <>&\u2028\u2029",
      timestamp: 1,
    });
    manager.appendThinkingLevelChange("high");
    manager.appendModelChange("openai", "gpt-test");
    manager.appendCustomEntry("state-empty");
    manager.appendCustomEntry("state-null", null);
    manager.appendCustomMessageEntry(
      "injected",
      [
        { type: "text", text: "custom" },
        { type: "image", data: "AA==", mimeType: "image/png" },
      ],
      false,
    );
    manager.appendSessionInfo("  line one\r\nline two  ");
    manager.appendSessionInfo("\ufeff\u0085edge\u0085\ufeff");

    const preAssistantExists = await pathExists(sessionFile);
    manager.appendMessage({
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
    });
    const postAssistantExists = await pathExists(sessionFile);
    manager.appendCompaction("compact", userID, 99, { files: ["a.go"] }, false);
    manager.branchWithSummary(userID, "alternate branch");
    manager.appendLabelChange(userID, "checkpoint");
    manager.appendLabelChange(userID, undefined);

    const canonical = canonicalizeWrittenSession(await readFile(sessionFile, "utf8"), "/fixture/project");
    const canonicalPath = path.join(tempRoot, "canonical.jsonl");
    await writeFile(canonicalPath, canonical);
    const reopened = session.SessionManager.open(canonicalPath);

    return {
      preAssistantExists,
      postAssistantExists,
      jsonl: canonical,
      projection: projectSession(reopened),
    };
  } finally {
    await rm(tempRoot, { recursive: true, force: true });
  }
}

async function buildInvalidUTF8Fixture(session: SessionModule): Promise<InvalidUTF8Fixture> {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), "pi-go-f6-invalid-utf8-"));
  try {
    const sessionPath = path.join(tempRoot, "session.jsonl");
    const input = Buffer.concat([
      Buffer.from(
        '{"type":"session","version":3,"id":"invalid-utf8","timestamp":"t","cwd":"/tmp"}\n' +
          '{"type":"message","id":"message","parentId":null,"timestamp":"t","message":{"role":"user","content":"',
        "utf8",
      ),
      Buffer.from([0xff, 0xff, 0xe2, 0x82]),
      Buffer.from('","timestamp":1}}\n', "utf8"),
    ]);
    await writeFile(sessionPath, input);
    return {
      inputBase64: input.toString("base64"),
      expected: session.loadEntriesFromFile(sessionPath),
    };
  } finally {
    await rm(tempRoot, { recursive: true, force: true });
  }
}

function assistantMessage(text: string, timestamp = 2) {
  return {
    role: "assistant",
    content: [{ type: "text", text }],
    api: "openai-responses",
    provider: "openai",
    model: "gpt-test",
    usage: {
      input: 1,
      output: 1,
      cacheRead: 0,
      cacheWrite: 0,
      totalTokens: 2,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
    },
    stopReason: "stop",
    timestamp,
  };
}

function normalizedTreeProjection(
  manager: InstanceType<SessionModule["SessionManager"]>,
  sessionID: string,
  sourceLabelTimestamps: Map<string, string>,
) {
  const entries = JSON.parse(JSON.stringify(manager.getEntries())) as Array<Record<string, unknown>>;
  const idMap = new Map<string, string>();
  entries.forEach((entry, index) => {
    if (typeof entry.id === "string") idMap.set(entry.id, (index + 1).toString(16).padStart(8, "0"));
  });
  const normalizeEntry = (entry: Record<string, unknown>, index?: number) => {
    const normalized = JSON.parse(JSON.stringify(entry)) as Record<string, unknown>;
    for (const key of ["id", "parentId", "targetId", "fromId", "firstKeptEntryId"]) {
      if (typeof normalized[key] === "string") normalized[key] = idMap.get(normalized[key] as string) ?? normalized[key];
    }
    if (normalized.type === "label" && typeof entry.timestamp === "string") {
      normalized.timestamp = sourceLabelTimestamps.get(entry.timestamp) ?? "unmapped-label-timestamp";
    } else if (index !== undefined) {
      normalized.timestamp = `entry-time-${index + 1}`;
    }
    return normalized;
  };
  const normalizedEntries = entries.map((entry, index) => normalizeEntry(entry, index));
  const normalizeNode = (node: Record<string, unknown>): Record<string, unknown> => {
    const rawEntry = node.entry as Record<string, unknown>;
    const entryIndex = entries.findIndex((entry) => entry.id === rawEntry.id);
    const result: Record<string, unknown> = {
      entry: normalizeEntry(rawEntry, entryIndex),
      children: ((node.children as Array<Record<string, unknown>>) ?? []).map(normalizeNode),
    };
    if (node.label !== undefined) result.label = node.label;
    if (typeof node.labelTimestamp === "string") {
      result.labelTimestamp = sourceLabelTimestamps.get(node.labelTimestamp) ?? "unmapped-label-timestamp";
    }
    return result;
  };
  const header = JSON.parse(JSON.stringify(manager.getHeader())) as Record<string, unknown>;
  header.id = sessionID;
  header.timestamp = "session-time";
  header.cwd = "/fixture/project";
  delete header.parentSession;
  return {
    header,
    leafId: manager.getLeafId() ? idMap.get(manager.getLeafId()!) : null,
    branchIds: manager.getBranch().map((entry) => idMap.get(entry.id) ?? entry.id),
    entries: normalizedEntries,
    tree: (manager.getTree() as unknown as Array<Record<string, unknown>>).map(normalizeNode),
  };
}

async function branchPersistenceProjection(
  session: SessionModule,
  tempRoot: string,
  includeAssistant: boolean,
) {
  const project = path.join(tempRoot, includeAssistant ? "assistant-project" : "user-project");
  const sessions = path.join(tempRoot, includeAssistant ? "assistant-sessions" : "user-sessions");
  await mkdir(project, { recursive: true });
  const manager = session.SessionManager.create(project, sessions, {
    id: includeAssistant ? "assistant-source" : "user-source",
  });
  const user = manager.appendMessage({ role: "user", content: "root", timestamp: 1 });
  let leaf = user;
  if (includeAssistant) leaf = manager.appendMessage(assistantMessage("answer"));
  manager.appendLabelChange(user, "checkpoint");
  const originalFile = manager.getSessionFile();
  const originalExists = originalFile ? await pathExists(originalFile) : false;
  const branchedFile = manager.createBranchedSession(leaf);
  if (!branchedFile) throw new Error("persisted branch did not allocate a session file");
  const branchExists = await pathExists(branchedFile);
  let afterAssistantExists = branchExists;
  if (!includeAssistant) {
    manager.appendMessage(assistantMessage("first assistant after branch", 3));
    afterAssistantExists = await pathExists(branchedFile);
  }
  const raw = afterAssistantExists ? await readFile(branchedFile, "utf8") : "";
  const lines = raw === "" ? [] : raw.trimEnd().split("\n");
  const parsed = lines.map((line) => JSON.parse(line) as Record<string, unknown>);
  return {
    originalExists,
    branchExists,
    afterAssistantExists,
    headerCount: parsed.filter((entry) => entry.type === "session").length,
    entryTypes: parsed.map((entry) => entry.type),
    parentChainValid: parsed
      .filter((entry) => entry.type !== "session")
      .every((entry, index, retained) => entry.parentId === (index === 0 ? null : retained[index - 1].id)),
  };
}

async function buildTreeFixture(session: SessionModule) {
  const manager = session.SessionManager.inMemory("/fixture/project", { id: "tree-source" });
  const root = manager.appendMessage({ role: "user", content: "root", timestamp: 1 });
  const assistant = manager.appendMessage(assistantMessage("answer"));
  manager.appendMessage({ role: "user", content: "abandoned", timestamp: 3 });
  manager.branch(assistant);
  const alternate = manager.appendMessage({ role: "user", content: "alternate", timestamp: 4 });
  const OriginalDate = globalThis.Date;
  let labelTime = OriginalDate.parse("2025-01-01T01:00:00.000Z");
  class IncrementingDate extends OriginalDate {
    constructor(value?: string | number | Date) {
      super(value === undefined ? labelTime++ : value);
    }
    static override now() {
      return labelTime;
    }
  }
  globalThis.Date = IncrementingDate as DateConstructor;
  try {
    manager.appendLabelChange(root, "root-first");
    manager.appendLabelChange(alternate, "alternate-first");
    manager.appendLabelChange(root, undefined);
    manager.appendLabelChange(root, "root-readded");
    manager.appendLabelChange(alternate, "alternate-updated");
  } finally {
    globalThis.Date = OriginalDate;
  }

  const sourceLabelTimestamps = new Map<string, string>();
  let labelIndex = 0;
  for (const entry of manager.getEntries()) {
    if (entry.type !== "label") continue;
    labelIndex++;
    sourceLabelTimestamps.set(entry.timestamp, `label-time-${labelIndex}`);
  }
  const before = normalizedTreeProjection(manager, "tree-source", sourceLabelTimestamps);
  manager.createBranchedSession(alternate);
  const branched = normalizedTreeProjection(manager, "tree-branched", sourceLabelTimestamps);

  const tempRoot = await mkdtemp(path.join(os.tmpdir(), "pi-go-f6-branch-persistence-"));
  try {
    return {
      before,
      branched,
      persistence: {
        userOnly: await branchPersistenceProjection(session, tempRoot, false),
        assistantPresent: await branchPersistenceProjection(session, tempRoot, true),
      },
    };
  } finally {
    await rm(tempRoot, { recursive: true, force: true });
  }
}

const forkSource =
  '{"type":"session","version":3,"id":"fork-source","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/fixture/source","futureHeader":true}\n' +
  '{"10":"ten","2":"two","type":"message","id":"source-user","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"user","content":"fork me","timestamp":1},"futureEntry":{"3":"three","1":"one","z":"last"}}\n' +
  '{"type":"message","id":"source-assistant","parentId":"source-user","timestamp":"2025-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"forked"}],"api":"openai-responses","provider":"openai","model":"gpt-test","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":2},"unknown":"kept"}\n';

function normalizedError(error: unknown, tempRoot: string) {
  const candidate = error as NodeJS.ErrnoException;
  return {
    message: String(candidate?.message ?? error).replaceAll(tempRoot, "<tmp>").replaceAll("\\", "/"),
    code: candidate?.code ?? null,
  };
}

async function buildForkFixture(session: SessionModule) {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), "pi-go-f6-fork-"));
  const OriginalDate = globalThis.Date;
  try {
    const sourcePath = path.join(tempRoot, "source.jsonl");
    const targetCwd = path.join(tempRoot, "target");
    const sessionDir = path.join(tempRoot, "sessions");
    await mkdir(targetCwd, { recursive: true });
    await writeFile(sourcePath, forkSource.replaceAll("/fixture/source", path.join(tempRoot, "source")));

    const fixedMilliseconds = OriginalDate.parse("2025-01-02T03:04:05.006Z");
    class FixedDate extends OriginalDate {
      constructor(value?: string | number | Date) {
        super(value === undefined ? fixedMilliseconds : value);
      }
      static override now() {
        return fixedMilliseconds;
      }
    }
    globalThis.Date = FixedDate as DateConstructor;

    const forked = session.SessionManager.forkFrom(sourcePath, targetCwd, sessionDir, { id: "fork-session" });
    const output = (await readFile(forked.getSessionFile()!, "utf8"))
      .trim()
      .split("\n")
      .map((line, index) => {
        const entry = JSON.parse(line) as Record<string, unknown>;
        if (index === 0) {
          entry.timestamp = "2025-01-01T00:00:00.000Z";
          entry.cwd = "/fixture/target";
          entry.parentSession = "/fixture/source.jsonl";
        }
        return JSON.stringify(entry);
      })
      .join("\n") + "\n";

    let invalidID: ReturnType<typeof normalizedError> | undefined;
    try {
      session.SessionManager.forkFrom(sourcePath, targetCwd, sessionDir, { id: ".invalid" });
    } catch (error) {
      invalidID = normalizedError(error, tempRoot);
    }
    const emptyPath = path.join(tempRoot, "empty.jsonl");
    await writeFile(emptyPath, "");
    let emptySource: ReturnType<typeof normalizedError> | undefined;
    try {
      session.SessionManager.forkFrom(emptyPath, targetCwd, sessionDir, { id: "empty-source" });
    } catch (error) {
      emptySource = normalizedError(error, tempRoot);
    }
    let collision: ReturnType<typeof normalizedError> | undefined;
    try {
      session.SessionManager.forkFrom(sourcePath, targetCwd, sessionDir, { id: "fork-session" });
    } catch (error) {
      collision = normalizedError(error, tempRoot);
    }
    if (!invalidID || !emptySource || !collision) throw new Error("upstream fork error fixture did not fail as expected");
    return {
      source: forkSource,
      expected: output,
      errors: {
        invalidID,
        emptySource,
        collision,
        collisionTolerance: "Node and Go format EEXIST syscall messages differently; compare error classification and target basename.",
      },
    };
  } finally {
    globalThis.Date = OriginalDate;
    await rm(tempRoot, { recursive: true, force: true });
  }
}

function sessionInfoProjection(info: Awaited<ReturnType<SessionModule["SessionManager"]["list"]>>[number], root: string) {
  return {
    path: path.basename(info.path),
    id: info.id,
    cwd: info.cwd.replace(root, "<tmp>").replaceAll("\\", "/"),
    name: info.name ?? null,
    parentSessionPath: info.parentSessionPath?.replace(root, "<tmp>").replaceAll("\\", "/") ?? null,
    created: Number.isNaN(info.created.getTime()) ? "Invalid Date" : info.created.toISOString(),
    modified: Number.isNaN(info.modified.getTime()) ? "Invalid Date" : info.modified.toISOString(),
    messageCount: info.messageCount,
    firstMessage: info.firstMessage,
    allMessagesText: info.allMessagesText,
  };
}

async function buildListFixture(session: SessionModule) {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), "pi-go-f6-list-"));
  try {
    const flatDir = path.join(tempRoot, "flat");
    const projectA = path.join(tempRoot, "project-a");
    const projectB = path.join(tempRoot, "project-b");
    await Promise.all([mkdir(flatDir), mkdir(projectA), mkdir(projectB)]);
    const files = new Map<string, string>([
      [
        "a-rich.jsonl",
        JSON.stringify({
          type: "session", version: 3, id: "rich", timestamp: "2025-01-01T00:00:00.000Z", cwd: projectA,
          parentSession: path.join(tempRoot, "parent.jsonl"),
        }) + "\n" +
          '{"type":"session_info","id":"info-1","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","name":"old"}\n' +
          '{"type":"message","id":"user","parentId":"info-1","timestamp":"2025-01-01T00:00:02.000Z","message":{"role":"user","content":"hello","timestamp":1735689602000}}\n' +
          '{"type":"message","id":"tool","parentId":"user","timestamp":"2025-01-01T00:00:03.000Z","message":{"role":"toolResult","content":[{"type":"text","text":"ignored search text"}],"timestamp":1735689603000}}\n' +
          '{"type":"session_info","id":"info-clear","parentId":"tool","timestamp":"2025-01-01T00:00:04.000Z","name":"   "}\n' +
          '{"type":"message","id":"assistant","parentId":"info-clear","timestamp":"2025-01-01T00:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"answer"},{"type":"image","data":"AA==","mimeType":"image/png"}],"timestamp":1735689612345}}\n' +
          '{"type":"session_info","id":"info-final","parentId":"assistant","timestamp":"2025-01-01T00:00:06.000Z","name":"  final  "}\n',
      ],
      ["a-header.jsonl", JSON.stringify({ type: "session", version: 3, id: "header", timestamp: "2025-02-01T00:00:00.000Z", cwd: projectA }) + "\n"],
      [
        "b-other.jsonl",
        JSON.stringify({ type: "session", version: 3, id: "other", timestamp: "2025-03-01T00:00:00.000Z", cwd: projectB }) + "\n" +
          '{"type":"message","id":"other-user","parentId":null,"timestamp":"2025-03-01T00:00:01.000Z","message":{"role":"user","content":"other project","timestamp":1740787201000}}\n',
      ],
      [
        "invalid-content.jsonl",
        JSON.stringify({ type: "session", version: 3, id: "invalid-content", timestamp: "2025-04-01T00:00:00.000Z", cwd: projectA }) + "\n" +
          '{"type":"message","id":"bad","parentId":null,"timestamp":"2025-04-01T00:00:01.000Z","message":{"role":"user","content":null,"timestamp":1743465601000}}\n',
      ],
      ["malformed.jsonl", '{"type":"message","id":"no-header"}\n'],
    ]);
    for (const [name, contents] of files) {
      const file = path.join(flatDir, name);
      await writeFile(file, contents);
      await utimes(file, new Date("2030-01-01T00:00:00.000Z"), new Date("2030-01-01T00:00:00.000Z"));
    }
    await writeFile(path.join(flatDir, "ignored.txt"), "ignored");

    const currentProgress: Array<{ loaded: number; total: number }> = [];
    const allProgress: Array<{ loaded: number; total: number }> = [];
    const current = await session.SessionManager.list(projectA, flatDir, (loaded, total) => currentProgress.push({ loaded, total }));
    const all = await session.SessionManager.listAll(flatDir, (loaded, total) => allProgress.push({ loaded, total }));
    return {
      files: Object.fromEntries(
        Array.from(files, ([name, contents]) => [name, contents.replaceAll(tempRoot, "<tmp>").replaceAll("\\", "/")]),
      ),
      projectA: "<tmp>/project-a",
      current: current.map((info) => sessionInfoProjection(info, tempRoot)),
      all: all.map((info) => sessionInfoProjection(info, tempRoot)),
      currentProgress,
      allProgress,
      invalidContentRejected: !all.some((info) => info.id === "invalid-content"),
    };
  } finally {
    await rm(tempRoot, { recursive: true, force: true });
  }
}

function sha256(value: string | Buffer) {
  return createHash("sha256").update(value).digest("hex");
}

function htmlStructure(html: string) {
  const withoutBodies = html
    .replace(/(<style[^>]*>)[\s\S]*?(<\/style>)/gi, "$1$2")
    .replace(/(<script[^>]*>)[\s\S]*?(<\/script>)/gi, "$1$2");
  return Array.from(withoutBodies.matchAll(/<(\/)?([A-Za-z][A-Za-z0-9-]*)([^>]*)>/g)).map((match) => {
    const attrs: Record<string, string> = {};
    for (const attr of match[3].matchAll(/([A-Za-z_:][A-Za-z0-9_:.-]*)=(?:"([^"]*)"|'([^']*)')/g)) {
      if (["id", "class", "type", "role", "aria-orientation", "aria-label"].includes(attr[1])) {
        attrs[attr[1]] = attr[2] ?? attr[3] ?? "";
      }
    }
    return { closing: Boolean(match[1]), name: match[2].toLowerCase(), attrs };
  });
}

async function buildExportFixture(upstreamRoot: string, exportModule: ExportModule) {
  const tempRoot = await mkdtemp(path.join(os.tmpdir(), "pi-go-f6-export-"));
  try {
    const input =
      '{"type":"session","version":3,"id":"export-source","timestamp":"2025-01-01T00:00:00.000Z","cwd":"/fixture/project"}\n' +
      '{"type":"message","id":"export-user","parentId":null,"timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"user","content":"<skill name=\\"audit\\" location=\\"/tmp/SKILL.md\\">\\n# Skill\\n\\nbody\\n</skill>\\n\\n<script>alert(\\"xss-fixture\\")</script>  spaced","timestamp":1}}\n' +
      '{"type":"message","id":"export-assistant","parentId":"export-user","timestamp":"2025-01-01T00:00:02.000Z","message":{"role":"assistant","content":[{"type":"text","text":"[unsafe](java\\u0000script:alert(1))"}],"api":"openai-responses","provider":"openai","model":"gpt-test","usage":{"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"totalTokens":2,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":2}}\n';
    const inputPath = path.join(tempRoot, "export.jsonl");
    const outputPath = path.join(tempRoot, "export.html");
    await writeFile(inputPath, input);
    await exportModule.exportFromFile(inputPath, { outputPath, themeName: "dark" });
    const html = await readFile(outputPath, "utf8");
    const sessionMatch = html.match(/<script id="session-data" type="application\/json">([^<]+)<\/script>/);
    if (!sessionMatch) throw new Error("export did not contain session data");
    const payload = Buffer.from(sessionMatch[1], "base64").toString("utf8");
    const assetPaths = {
      templateHTML: "template.html",
      templateCSS: "template.css",
      templateJS: "template.js",
      markedJS: "vendor/marked.min.js",
      highlightJS: "vendor/highlight.min.js",
    };
    const assetHashes: Record<string, string> = {};
    for (const [name, relative] of Object.entries(assetPaths)) {
      assetHashes[name] = sha256(await readFile(path.join(upstreamRoot, "packages/coding-agent/src/core/export-html", relative)));
    }
    const css = await readFile(path.join(upstreamRoot, "packages/coding-agent/src/core/export-html/template.css"), "utf8");
    const renderer = await readFile(path.join(upstreamRoot, "packages/coding-agent/src/core/export-html/template.js"), "utf8");
    const placeholders = ["CSS", "JS", "SESSION_DATA", "MARKED_JS", "HIGHLIGHT_JS", "THEME_VARS", "BODY_BG", "CONTAINER_BG", "INFO_BG"];
    return {
      input,
      sessionDataBase64: sessionMatch[1],
      sessionDataJSON: payload,
      assetHashes,
      htmlSha256: sha256(html),
      htmlBytes: Buffer.byteLength(html),
      placeholderCounts: Object.fromEntries(placeholders.map((name) => [name, html.split(`{{${name}}}`).length - 1])),
      selfContained: !/<script[^>]+src=|<link[^>]+href=/i.test(html),
      rawPayloadExposed: html.includes('<script>alert("xss-fixture")</script>'),
      securityMarkers: Object.fromEntries(
        [
          "sanitizeMarkdownUrl(token.href)",
          "escapeHtml(href)",
          "replace(/[\\x00-\\x1f\\x7f]/g, '')",
          "parseSkillBlock",
          "safeMarkedParse(skillBlock.content)",
          "escapeHtml(img.mimeType",
          "escapeHtml(img.data || '')",
          "escapeHtml(entry.id)",
        ].map((marker) => [marker, renderer.includes(marker)]),
      ),
      whitespaceMarkers: {
        outputLinePreWrap: /\.output-preview > div:not\(\.expand-hint\),\s*\.output-full > div:not\(\.expand-hint\) \{[\s\S]*?white-space:\s*pre-wrap;/.test(css),
        ansiLinePre: /\.ansi-line\s*\{[\s\S]*?white-space:\s*pre;/.test(css),
        containerDoesNotPreWrap: !/\.output-preview,\s*\.output-full\s*\{[\s\S]*?white-space:\s*pre-wrap;/.test(css),
      },
      themeMarkers: {
        variablesResolved: !html.includes("{{THEME_VARS}}"),
        bodyBackgroundResolved: !html.includes("{{BODY_BG}}"),
        exportPageBackgroundPresent: html.includes("--exportPageBg:"),
      },
      domProjection: htmlStructure(html),
      domTolerances: [
        "Script and style bodies are compared separately by exact asset and full-document SHA-256 hashes.",
        "The DOM projection ignores attribute order, non-selected attributes, and whitespace between tags.",
        "Browser execution and computed style layout are outside fixture scope.",
      ],
    };
  } finally {
    await rm(tempRoot, { recursive: true, force: true });
  }
}

export async function generateF6(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const sessionSource = "packages/coding-agent/src/core/session-manager.ts";
  const exportSource = "packages/coding-agent/src/core/export-html/index.ts";
  const session = await withUpstreamModelData(
    upstreamRoot,
    async () => (await import(pathToFileURL(path.join(upstreamRoot, sessionSource)).href)) as SessionModule,
  );

  for (const fixtureCase of parseCases) {
    fixtureCase.expected = session.parseSessionEntries(fixtureCase.input);
  }

  for (const fixtureCase of migrationCases) {
    const entries = session.parseSessionEntries(fixtureCase.input) as Array<Record<string, unknown>>;
    session.migrateSessionEntries(entries as never);
    fixtureCase.expected = jsonl(fixtureCase.normalizeGeneratedIDs ? normalizeGeneratedEntries(entries) : entries);
  }

  const writeFixture = await buildWriteFixture(session);
  const invalidUTF8 = await buildInvalidUTF8Fixture(session);
  const tree = await buildTreeFixture(session);
  const fork = await buildForkFixture(session);
  const list = await buildListFixture(session);
  const exportModule = (await import(
    pathToFileURL(path.join(upstreamRoot, exportSource)).href
  )) as ExportModule;
  const exportFixture = await buildExportFixture(upstreamRoot, exportModule);
  const familyDir = path.join(outputRoot, "F6");
  await mkdir(familyDir, { recursive: true });
  const manifest = {
    family: "F6",
    upstreamCommit,
    generator: "conformance/extract/f6-session.ts",
    source: `${sessionSource} + ${exportSource}`,
    files: ["cases.json", "write.jsonl", "projection.json", "tree-export.json"],
  };

  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(
    path.join(familyDir, "cases.json"),
    `${JSON.stringify({ schemaVersion: 1, parseCases, migrationCases, invalidUTF8, lazyPersistence: {
      preAssistantExists: writeFixture.preAssistantExists,
      postAssistantExists: writeFixture.postAssistantExists,
    } }, null, 2)}\n`,
  );
  await writeFile(path.join(familyDir, "write.jsonl"), writeFixture.jsonl);
  await writeFile(path.join(familyDir, "projection.json"), `${JSON.stringify(writeFixture.projection, null, 2)}\n`);
  await writeFile(
    path.join(familyDir, "tree-export.json"),
    `${JSON.stringify({ schemaVersion: 2, tree, fork, list, export: exportFixture }, null, 2)}\n`,
  );
}
