import { mkdtemp, readFile, rm, writeFile, mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

export async function generateF6Release(upstreamRoot: string, outputRoot: string): Promise<void> {
  const { SessionManager, loadEntriesFromFile } = await import(pathToFileURL(path.join(upstreamRoot,
    "packages/coding-agent/src/core/session-manager.ts")).href);
  const temporary = await mkdtemp(path.join(os.tmpdir(), "orb-f6-release-"));
  try {
    const inputs = [
      '{"type":"session","version":3,"id":"fixture","timestamp":"2026-09-01T00:00:00.000Z","cwd":"/fixture"}',
      '{"type":"session","version":3,"id":"fixture"}\nmalformed',
      '{"type":"message","id":"invalid"}',
      '{"type":"session","version":3,"id":"fixture"}\n',
    ];
    const repairs = [];
    for (const input of inputs) {
      const file = path.join(temporary, "repair.jsonl");
      await writeFile(file, input);
      loadEntriesFromFile(file);
      repairs.push({ input, expected: await readFile(file, "utf8") });
    }
    const timestamp = "2026-09-01T00:00:00.000Z";
    const entries = [
      { type: "session", version: 3, id: "restored", timestamp, cwd: "/fixture" },
      { type: "message", id: "user", parentId: null, timestamp,
        message: { role: "user", content: "hello", timestamp: 1 } },
      { type: "label", id: "label", parentId: "user", timestamp, targetId: "user", label: "checkpoint" },
      { type: "message", id: "next", parentId: "label", timestamp,
        message: { role: "user", content: "next", timestamp: 2 } },
      { type: "compaction", id: "compact", parentId: "next", timestamp,
        summary: "prior", firstKeptEntryId: "label", tokensBefore: 10 },
    ];
    const manager = SessionManager.inMemory("/fixture", undefined, structuredClone(entries));
    const restored = { sessionId: manager.getSessionId(), leafId: manager.getLeafId(), entries: manager.getEntries() };
    manager.createBranchedSession("compact");
    const fork = manager.getEntries().filter((entry: any) => entry.type !== "label");
    const family = path.join(outputRoot, "F6");
    await mkdir(family, { recursive: true });
    await writeFile(path.join(family, "release.json"), JSON.stringify({ repairs, entries, restored, fork }, null, 2) + "\n");
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
}
