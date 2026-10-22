import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { committedFixture } from "./orb-owned.ts";
import { withUpstreamModelData } from "./upstream-model-data.ts";

const iso = (index: number): string => new Date(Date.UTC(2026, 0, 1, 0, 0, index)).toISOString();
const millis = (index: number): number => Date.parse(iso(index));

function usage(input = 0, output = 0, cacheRead = 0, cacheWrite = 0, totalTokens = 0) {
  return {
    input,
    output,
    cacheRead,
    cacheWrite,
    totalTokens,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  };
}

function user(text: string, index: number) {
  return { role: "user", content: [{ type: "text", text }], timestamp: millis(index) };
}

function assistant(content: unknown[], index: number, reportedUsage = usage()) {
  return {
    role: "assistant",
    content,
    api: "openai-responses",
    provider: "fixture",
    model: "fixture-model",
    usage: reportedUsage,
    stopReason: "stop",
    timestamp: millis(index),
  };
}

function toolResult(text: string, index: number) {
  return {
    role: "toolResult",
    toolCallId: `call-${index}`,
    toolName: "read",
    content: [{ type: "text", text }],
    isError: false,
    timestamp: millis(index),
  };
}

function messageEntry(id: string, parentId: string | null, message: unknown, index: number) {
  return { type: "message", id, parentId, timestamp: iso(index), message };
}

export async function generateF10(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const compactionSource = "packages/coding-agent/src/core/compaction/compaction.ts";
  const branchSource = "packages/coding-agent/src/core/compaction/branch-summarization.ts";
  const { codingCompaction, sessionManager } = await withUpstreamModelData(upstreamRoot, async () => ({
    sessionManager: await import(pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/session-manager.ts")).href) as any,
    codingCompaction: await import(pathToFileURL(path.join(upstreamRoot, compactionSource)).href) as any,
  }));
  const owned = await committedFixture("F10");

  const boundaryEntries = [
    messageEntry("u1", null, user("a".repeat(80), 1), 1),
    messageEntry("a1", "u1", assistant([{ type: "text", text: "b".repeat(80) }], 2), 2),
    { type: "model_change", id: "m1", parentId: "a1", timestamp: iso(3), provider: "fixture", modelId: "fixture-model" },
    messageEntry("u2", "m1", user("c".repeat(80), 4), 4),
    messageEntry("a2", "u2", assistant([{ type: "text", text: "d".repeat(80) }], 5), 5),
    messageEntry("tr2", "a2", toolResult("e".repeat(80), 6), 6),
    messageEntry("a3", "tr2", assistant([{ type: "text", text: "f".repeat(80) }], 7), 7),
  ];
  const splitEntries = [
    messageEntry("split-user", null, user("request ".repeat(80), 10), 10),
    messageEntry("split-a1", "split-user", assistant([{ type: "text", text: "early ".repeat(80) }], 11), 11),
    messageEntry("split-tool", "split-a1", toolResult("tool ".repeat(80), 12), 12),
    messageEntry("split-a2", "split-tool", assistant([{ type: "text", text: "recent ".repeat(80) }], 13), 13),
  ];
  const customEntries = [
    messageEntry("custom-u", null, user("hi", 14), 14),
    messageEntry("custom-a1", "custom-u", assistant([{ type: "text", text: "hello" }], 15), 15),
    { type: "custom_message", id: "custom", parentId: "custom-a1", timestamp: iso(16), customType: "fixture", content: "x".repeat(4000), display: true },
    messageEntry("custom-a2", "custom", assistant([{ type: "text", text: "ok" }], 17), 17),
  ];
  const branchSummaryEntries = [
    messageEntry("branch-cut-u", null, user("hi", 14), 14),
    messageEntry("branch-cut-a1", "branch-cut-u", assistant([{ type: "text", text: "hello" }], 15), 15),
    { type: "branch_summary", id: "branch-cut", parentId: "branch-cut-a1", timestamp: iso(16), fromId: "old", summary: "x".repeat(4000) },
    messageEntry("branch-cut-a2", "branch-cut", assistant([{ type: "text", text: "ok" }], 17), 17),
  ];
  const longEntries: any[] = [];
  let parentId: string | null = null;
  for (let turn = 0; turn < 30; turn++) {
    const userId = `long-u-${turn}`;
    longEntries.push(messageEntry(userId, parentId, user(`request-${turn}-` + "u".repeat(40 + turn * 3), turn * 2), turn * 2));
    const assistantId = `long-a-${turn}`;
    longEntries.push(messageEntry(assistantId, userId, assistant([{ type: "text", text: `answer-${turn}-` + "a".repeat(55 + turn * 2) }], turn * 2 + 1), turn * 2 + 1));
    parentId = assistantId;
  }
  const cutInputs = [
    { name: "whole-turn-boundary-with-metadata", entries: boundaryEntries, startIndex: 0, endIndex: boundaryEntries.length, keepRecentTokens: 75 },
    { name: "split-large-turn", entries: splitEntries, startIndex: 0, endIndex: splitEntries.length, keepRecentTokens: 130 },
    { name: "custom-message-weight", entries: customEntries, startIndex: 0, endIndex: customEntries.length, keepRecentTokens: 2 },
    { name: "branch-summary-weight", entries: branchSummaryEntries, startIndex: 0, endIndex: branchSummaryEntries.length, keepRecentTokens: 2 },
    { name: "no-valid-message-cut-point", entries: [{ type: "label", id: "label", parentId: null, timestamp: iso(1), targetId: "missing", label: "x" }], startIndex: 0, endIndex: 1, keepRecentTokens: 20 },
    { name: "long-faux-session", entries: longEntries, startIndex: 0, endIndex: longEntries.length, keepRecentTokens: 620 },
  ];
  const cutCases = cutInputs.map((fixtureCase) => ({
    ...fixtureCase,
    expected: codingCompaction.findCutPoint(fixtureCase.entries, fixtureCase.startIndex, fixtureCase.endIndex, fixtureCase.keepRecentTokens),
  }));

  const systemMessage = {
    role: "system", content: "initial 😀", sections: { preamble: "long prompt ".repeat(40), removed: null },
    toolsAdded: [{ name: "read", description: "Read 😀", parameters: { type: "object", properties: { path: { type: "string" } } } }],
    timestamp: millis(1),
  };
  const productTokenCases = [
    { name: "system-content-utf16", message: { role: "system", content: "😀abc", timestamp: millis(1) } },
    { name: "system-sections-tools", message: systemMessage },
    { name: "system-empty-tools", message: { role: "system", content: "", toolsAdded: [], timestamp: millis(1) } },
    { name: "system-content-blocks", message: { role: "system", content: [{ type: "text", text: "hello 😀" }], timestamp: millis(1) } },
  ].map((input) => ({ ...input, expected: codingCompaction.estimateTokens(input.message) }));
  const initialContext = [
    messageEntry("system-context", null, systemMessage, 1),
    messageEntry("context-user", "system-context", user("next request", 2), 2),
  ];
  const validUsage: any = messageEntry("context-answer", "context-user", assistant([{ type: "text", text: "answer" }], 3, usage(400, 20, 10, 5, 435)), 3);
  const updatedSystem = messageEntry("context-system-update", "context-answer", {
    role: "system", content: "additional", sections: { preamble: "updated 😀", removed: null },
    toolsRemoved: [{ name: "read" }], toolsAdded: [], timestamp: millis(4),
  }, 4);
  const projectedContextCases = [
    { name: "prompt-tools-before-first-usage", entries: initialContext },
    { name: "error-does-not-anchor-zero-usage", entries: [...initialContext, { ...validUsage, message: { ...validUsage.message, stopReason: "error", usage: usage() } }] },
    { name: "usage-anchor-plus-system-delta", entries: [...initialContext, validUsage, updatedSystem] },
    { name: "edited-context-invalidates-usage", entries: [...initialContext, validUsage, updatedSystem,
      { type: "context_edit", id: "edit-context", parentId: "context-system-update", timestamp: iso(5), targetId: "context-user", replacement: { content: "next request" } },
    ] },
  ].map((input) => ({ ...input, expected: codingCompaction.estimateProjectedContextTokens(sessionManager.buildSessionProjection(input.entries), input.entries) }));

  const familyDir = path.join(outputRoot, "F10");
  await mkdir(familyDir, { recursive: true });
  const manifest = {
    family: "F10",
    upstreamCommit,
    generator: "conformance/extract/f10-compaction.ts",
    source: compactionSource,
    additionalSources: [branchSource],
    orbOwned: ["tokenCases", "conversationCases", "contextCases", "prepareCases", "branchCases", "summaryPromptCases", "branchPromptCases", "compactPromptCases", "productSummaryCases"],
    files: ["cases.json"],
  };
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(path.join(familyDir, "cases.json"), `${JSON.stringify({
    schemaVersion: 1,
    tokenCases: owned.tokenCases,
    conversationCases: owned.conversationCases,
    contextCases: owned.contextCases,
    productTokenCases,
    projectedContextCases,
    cutCases,
    prepareCases: owned.prepareCases,
    branchCases: owned.branchCases,
    summaryPromptCases: owned.summaryPromptCases,
    branchPromptCases: owned.branchPromptCases,
    compactPromptCases: owned.compactPromptCases,
    productSummaryCases: owned.productSummaryCases,
  }, null, 2)}\n`);
}
