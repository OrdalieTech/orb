import { access, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withUpstreamModelData } from "./upstream-model-data.ts";

type SessionModule = typeof import("../../.upstream/packages/coding-agent/src/core/session-manager.ts");

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

export async function generateF6(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const source = "packages/coding-agent/src/core/session-manager.ts";
  const session = await withUpstreamModelData(
    upstreamRoot,
    async () => (await import(pathToFileURL(path.join(upstreamRoot, source)).href)) as SessionModule,
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
  const familyDir = path.join(outputRoot, "F6");
  await mkdir(familyDir, { recursive: true });
  const manifest = {
    family: "F6",
    upstreamCommit,
    generator: "conformance/extract/f6-session.ts",
    source,
    files: ["cases.json", "write.jsonl", "projection.json"],
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
}
