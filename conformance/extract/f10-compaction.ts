import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

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

function entryRoles(messages: Array<{ role: string }>): string[] {
  return messages.map((message) => message.role);
}

function fileLists(fileOps: { read: Set<string>; written: Set<string>; edited: Set<string> }) {
  const modified = new Set([...fileOps.written, ...fileOps.edited]);
  return {
    readFiles: [...fileOps.read].filter((file) => !modified.has(file)).sort(),
    modifiedFiles: [...modified].sort(),
  };
}

function resultValue<T>(result: { ok: boolean; value?: T; error?: unknown }): T {
  if (!result.ok) throw result.error;
  return result.value as T;
}

function completionResponse(text: string) {
  return {
    role: "assistant",
    content: [{ type: "text", text }],
    api: "openai-responses",
    provider: "fixture",
    model: "fixture-model",
    usage: usage(10, 5, 0, 0, 15),
    stopReason: "stop",
    timestamp: millis(59),
  };
}

function capturedRequest(context: any, options: any, hasSignal = false) {
  const capturedContext = JSON.parse(JSON.stringify(context));
  for (const message of capturedContext.messages ?? []) delete message.timestamp;
  const capturedOptions = { ...options };
  if (hasSignal) capturedOptions.signal = "<signal>";
  else delete capturedOptions.signal;
  return { context: capturedContext, options: capturedOptions };
}

export async function generateF10(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const compactionSource = "packages/agent/src/harness/compaction/compaction.ts";
  const codingCompactionSource = "packages/coding-agent/src/core/compaction/compaction.ts";
  const branchSource = "packages/agent/src/harness/compaction/branch-summarization.ts";
  const messagesSource = "packages/agent/src/harness/messages.ts";
  const utilsSource = "packages/agent/src/harness/compaction/utils.ts";
  const { compaction, codingCompaction, branch, messages, utils } = await withUpstreamModelData(upstreamRoot, async () => ({
    compaction: await import(pathToFileURL(path.join(upstreamRoot, compactionSource)).href) as any,
    codingCompaction: await import(pathToFileURL(path.join(upstreamRoot, codingCompactionSource)).href) as any,
    branch: await import(pathToFileURL(path.join(upstreamRoot, branchSource)).href) as any,
    messages: await import(pathToFileURL(path.join(upstreamRoot, messagesSource)).href) as any,
    utils: await import(pathToFileURL(path.join(upstreamRoot, utilsSource)).href) as any,
  }));

  const tokenMessages = [
    user("ASCII and emoji 🙂", 1),
    { role: "user", content: [{ type: "image", data: "aGVsbG8=", mimeType: "image/png" }], timestamp: millis(2) },
    assistant([
      { type: "thinking", thinking: "consider 🙂" },
      { type: "text", text: "answer" },
      { type: "toolCall", id: "ordered", name: "read", arguments: { z: 1, a: "two" } },
    ], 3),
    toolResult("tool text 🙂", 4),
    { role: "custom", customType: "fixture", content: "custom text", display: true, timestamp: millis(5) },
    { role: "bashExecution", command: "printf x", output: "x🙂", exitCode: 0, cancelled: false, truncated: false, timestamp: millis(6) },
    { role: "branchSummary", summary: "branch 🙂", fromId: "from", timestamp: millis(7) },
    { role: "compactionSummary", summary: "compact 🙂", tokensBefore: 50, timestamp: millis(8) },
  ];
  const tokenCases = tokenMessages.map((message, index) => ({
    name: `role-${index}-${message.role}`,
    message,
    expected: compaction.estimateTokens(message),
  }));

  const longToolResult = `${"x".repeat(1998)}🙂tail`;
  const conversationMessages = [
    user("start", 1),
    {
      role: "custom",
      customType: "fixture",
      content: [{ type: "text", text: "custom " }, { type: "image", data: "aA==", mimeType: "image/png" }, { type: "text", text: "blocks" }],
      display: true,
      timestamp: millis(2),
    },
    { role: "bashExecution", command: "go test ./...", output: "failed", exitCode: 2, cancelled: false, truncated: true, fullOutputPath: "/tmp/full.log", timestamp: millis(3) },
    { role: "bashExecution", command: "secret", output: "hidden", exitCode: 0, cancelled: false, truncated: false, excludeFromContext: true, timestamp: millis(4) },
    { role: "branchSummary", summary: "branch work", fromId: "old", timestamp: millis(5) },
    { role: "compactionSummary", summary: "older work", tokensBefore: 900, timestamp: millis(6) },
    assistant([
      { type: "thinking", thinking: "first\nsecond" },
      { type: "text", text: "done" },
      { type: "toolCall", id: "order", name: "write", arguments: { z: 1, a: { nested: true } } },
    ], 7),
    toolResult(longToolResult, 8),
  ];
  const conversationCases = [{
    name: "all-agent-message-projections",
    messages: conversationMessages,
    expected: utils.serializeConversation(messages.convertToLlm(conversationMessages)),
  }];

  const contextMessages = [
    user("before usage", 1),
    assistant([{ type: "text", text: "reported" }], 2, usage(100, 20, 5, 3, 0)),
    user("trailing 🙂 text", 3),
    { role: "branchSummary", summary: "tail summary", fromId: "x", timestamp: millis(4) },
  ];
  const estimateOnlyMessages = [user("no usage", 5), toolResult("tail", 6)];
  const contextCases = [
    { name: "last-valid-usage-plus-trailing-estimate", messages: contextMessages },
    { name: "all-estimated-without-usage", messages: estimateOnlyMessages },
  ].map((fixtureCase) => ({ ...fixtureCase, expected: compaction.estimateContextTokens(fixtureCase.messages) }));

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

  const prepareInputs = [
    {
      name: "initial-compaction-split-turn",
      entries: splitEntries,
      settings: { enabled: true, reserveTokens: 256, keepRecentTokens: 130 },
    },
    {
      name: "iterative-compaction-boundary",
      entries: [
        messageEntry("old-u", null, user("old request", 20), 20),
        messageEntry("old-a", "old-u", assistant([{ type: "toolCall", id: "r", name: "read", arguments: { path: "/read.txt" } }], 21), 21),
        { type: "compaction", id: "compact-1", parentId: "old-a", timestamp: iso(22), summary: "previous summary", firstKeptEntryId: "new-u", tokensBefore: 800, details: { readFiles: ["/prior.txt"], modifiedFiles: ["/changed.txt"] } },
        messageEntry("new-u", "compact-1", user("new request ".repeat(40), 23), 23),
        messageEntry("new-a", "new-u", assistant([{ type: "toolCall", id: "w", name: "write", arguments: { path: "/written.txt", content: "x" } }], 24, usage(250, 30, 0, 0, 280)), 24),
        messageEntry("new-u2", "new-a", user("latest request ".repeat(20), 25), 25),
        messageEntry("new-a2", "new-u2", assistant([{ type: "text", text: "latest answer ".repeat(20) }], 26), 26),
      ],
      settings: { enabled: true, reserveTokens: 128, keepRecentTokens: 90 },
    },
  ];
  const prepareCases = prepareInputs.map((fixtureCase) => {
    const prepared = resultValue<any>(compaction.prepareCompaction(fixtureCase.entries, fixtureCase.settings));
    return {
      ...fixtureCase,
      expected: prepared === undefined ? null : {
        firstKeptEntryId: prepared.firstKeptEntryId,
        summaryRoles: entryRoles(prepared.messagesToSummarize),
        prefixRoles: entryRoles(prepared.turnPrefixMessages),
        summaryConversation: utils.serializeConversation(messages.convertToLlm(prepared.messagesToSummarize)),
        prefixConversation: utils.serializeConversation(messages.convertToLlm(prepared.turnPrefixMessages)),
        isSplitTurn: prepared.isSplitTurn,
        tokensBefore: prepared.tokensBefore,
        previousSummary: prepared.previousSummary ?? null,
        ...fileLists(prepared.fileOps),
      },
    };
  });

  const branchEntries = [
    messageEntry("root", null, user("root", 30), 30),
    messageEntry("branch-u", "root", user("branch request", 31), 31),
    messageEntry("branch-a", "branch-u", assistant([{ type: "toolCall", id: "edit", name: "edit", arguments: { path: "/edited.txt", oldText: "a", newText: "b" } }], 32), 32),
    { type: "branch_summary", id: "nested-summary", parentId: "branch-a", timestamp: iso(33), fromId: "nested", summary: "nested branch summary", details: { readFiles: ["/nested-read.txt"], modifiedFiles: ["/nested-modified.txt"] } },
    messageEntry("branch-leaf", "nested-summary", assistant([{ type: "text", text: "leaf answer" }], 34), 34),
    messageEntry("other-u", "root", user("other path", 35), 35),
  ];
  const entryIndex = new Map(branchEntries.map((entry) => [entry.id, entry]));
  const session = {
    async getEntry(id: string) { return entryIndex.get(id); },
    async getBranch(id: string) {
      const result: any[] = [];
      let current: string | null = id;
      while (current) {
        const entry = entryIndex.get(current);
        if (!entry) break;
        result.push(entry);
        current = entry.parentId;
      }
      return result.reverse();
    },
  };
  const collected = await branch.collectEntriesForBranchSummary(session, "branch-leaf", "other-u");
  const branchPrepared = branch.prepareBranchEntries(collected.entries, 70);
  const branchCases = [{
    name: "abandoned-branch-common-ancestor-and-budget",
    entries: branchEntries,
    oldLeafId: "branch-leaf",
    targetId: "other-u",
    tokenBudget: 70,
    expected: {
      entryIds: collected.entries.map((entry: any) => entry.id),
      commonAncestorId: collected.commonAncestorId,
      roles: entryRoles(branchPrepared.messages),
      conversation: utils.serializeConversation(messages.convertToLlm(branchPrepared.messages)),
      totalTokens: branchPrepared.totalTokens,
      ...fileLists(branchPrepared.fileOps),
    },
  }];

  const model = {
    id: "fixture-model",
    name: "Fixture",
    api: "openai-responses",
    provider: "fixture",
    baseUrl: "https://fixture.invalid",
    reasoning: true,
    input: ["text"],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: 4096,
    maxTokens: 300,
  };
  const summaryPromptInputs = [
    { name: "new-summary-with-custom-focus", messages: conversationMessages.slice(0, 7), reserveTokens: 500, customInstructions: "Keep exact paths.", previousSummarySet: false, previousSummary: "", thinkingLevel: "high" },
    { name: "empty-previous-summary-is-absent", messages: [user("small", 40)], reserveTokens: 200, customInstructions: "", previousSummarySet: true, previousSummary: "", thinkingLevel: "off" },
    { name: "update-existing-summary", messages: [user("new work", 41)], reserveTokens: 200, customInstructions: "", previousSummarySet: true, previousSummary: "## Goal\nExisting", thinkingLevel: "off" },
  ];
  const summaryPromptCases = [];
  for (const input of summaryPromptInputs) {
    let captured: any;
    const models = {
      async completeSimple(_model: unknown, context: unknown, options: unknown) {
        captured = capturedRequest(context, options);
        return completionResponse("summary output");
      },
    };
    const args = [input.messages, models, model, input.reserveTokens, undefined, input.customInstructions] as any[];
    if (input.previousSummarySet) args.push(input.previousSummary);
    else args.push(undefined);
    args.push(input.thinkingLevel);
    const output = resultValue<string>(await compaction.generateSummary(...args));
    summaryPromptCases.push({ input, expected: { captured, output } });
  }

  let branchCaptured: any;
  const branchModels = {
    async completeSimple(_model: unknown, context: unknown, options: unknown) {
      branchCaptured = capturedRequest(context, options, true);
      return completionResponse("branch output");
    },
  };
  const branchPromptInput = {
    entries: collected.entries,
    reserveTokens: 300,
    customInstructions: "Only decisions.",
    replaceInstructions: false,
  };
  const branchOutput = resultValue<any>(await branch.generateBranchSummary(branchPromptInput.entries, {
    models: branchModels,
    model,
    signal: new AbortController().signal,
    reserveTokens: branchPromptInput.reserveTokens,
    customInstructions: branchPromptInput.customInstructions,
    replaceInstructions: branchPromptInput.replaceInstructions,
  }));
  const branchPromptCases = [{ name: "append-custom-instructions", input: branchPromptInput, expected: { captured: branchCaptured, output: branchOutput } }];

  const compactPromptInput = {
    firstKeptEntryId: "keep",
    messagesToSummarize: [user("history", 45)],
    turnPrefixMessages: [user("large request", 46), assistant([{ type: "text", text: "early work" }], 47)],
    isSplitTurn: true,
    tokensBefore: 1234,
    previousSummary: null,
    settings: { enabled: true, reserveTokens: 400, keepRecentTokens: 100 },
  };
  const compactCaptures: any[] = [];
  const compactModels = {
    async completeSimple(_model: unknown, context: unknown, options: unknown) {
      compactCaptures.push(capturedRequest(context, options));
      return completionResponse(compactCaptures.length === 1 ? "history summary" : "prefix summary");
    },
  };
  const compactOutput = resultValue<any>(await compaction.compact({
    ...compactPromptInput,
    previousSummary: undefined,
    fileOps: { read: new Set<string>(), written: new Set<string>(), edited: new Set<string>() },
  }, compactModels, model, undefined, undefined, "high"));
  const compactPromptCases = [{ name: "split-turn-two-stage-prompts", input: compactPromptInput, expected: { captured: compactCaptures, output: compactOutput } }];

  const familyDir = path.join(outputRoot, "F10");
  await mkdir(familyDir, { recursive: true });
  const manifest = {
    family: "F10",
    upstreamCommit,
    generator: "conformance/extract/f10-compaction.ts",
    source: compactionSource,
    additionalSources: [codingCompactionSource, branchSource, messagesSource, utilsSource],
    files: ["cases.json"],
  };
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(path.join(familyDir, "cases.json"), `${JSON.stringify({
    schemaVersion: 1,
    tokenCases,
    conversationCases,
    contextCases,
    cutCases,
    prepareCases,
    branchCases,
    summaryPromptCases,
    branchPromptCases,
    compactPromptCases,
  }, null, 2)}\n`);
}
