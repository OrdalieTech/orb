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
  // Telemetry is excluded by DECISIONS.md; this is an in-process context, not provider wire.
  delete capturedOptions.telemetryContext;
  if (capturedOptions.sessionId !== undefined) {
    if (typeof capturedOptions.sessionId !== "string" || !/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(capturedOptions.sessionId)) {
      throw new Error(`captured sessionId is not UUIDv7: ${String(capturedOptions.sessionId)}`);
    }
    capturedOptions.sessionId = "00000000-0000-7000-8000-000000000000";
  }
  if (hasSignal) capturedOptions.signal = "<signal>";
  else delete capturedOptions.signal;
  return { context: capturedContext, options: capturedOptions };
}

export async function generateF10(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const compactionSource = "packages/coding-agent/src/core/compaction/compaction.ts";
  const branchSource = "packages/coding-agent/src/core/compaction/branch-summarization.ts";
  const { codingCompaction, codingBranch, sessionManager } = await withUpstreamModelData(upstreamRoot, async () => ({
    sessionManager: await import(pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/session-manager.ts")).href) as any,
    codingCompaction: await import(pathToFileURL(path.join(upstreamRoot, compactionSource)).href) as any,
    codingBranch: await import(pathToFileURL(path.join(upstreamRoot, branchSource)).href) as any,
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
  const compactPromptInput = {
    firstKeptEntryId: "keep",
    messagesToSummarize: [user("history", 45)],
    turnPrefixMessages: [user("large request", 46), assistant([{ type: "text", text: "early work" }], 47)],
    isSplitTurn: true,
    tokensBefore: 1234,
    previousSummary: null,
    settings: { enabled: true, reserveTokens: 400, keepRecentTokens: 100 },
  };

  // Split-turn compaction through the product compactor: both stage prompts
  // and the merged result.
  const compactCaptures: any[] = [];
  const compactOutput = await codingCompaction.compact(
    { ...compactPromptInput, previousSummary: undefined, fileOps: { read: new Set<string>(), written: new Set<string>(), edited: new Set<string>() } },
    model, undefined, undefined, undefined, undefined, "high",
    (_model: any, context: any, options: any) => {
      compactCaptures.push(capturedRequest(context, options));
      const text = compactCaptures.length === 1 ? "history summary" : "prefix summary";
      return { result: async () => completionResponse(text) };
    },
  );
  const compactPromptCases: any[] = [{ name: "split-turn-two-stage-prompts", input: compactPromptInput, expected: { captured: compactCaptures, output: compactOutput } }];

  const systemEntries = [
    messageEntry("system", null, { role: "system", content: "system instructions", timestamp: millis(40) }, 40),
    messageEntry("user", "system", user("repair the evaluator", 41), 41),
    messageEntry("prefix-system", "user", { role: "system", content: "updated instructions", timestamp: millis(42) }, 42),
    messageEntry("early", "prefix-system", assistant([{ type: "text", text: "probe findings ".repeat(100) }], 43, usage(1000, 0, 0, 0, 1000)), 43),
    messageEntry("latest", "early", assistant([{ type: "text", text: "latest step" }], 44, usage(10, 0, 0, 0, 10)), 44),
  ];
  const systemSettings = { enabled: true, reserveTokens: 100, keepRecentTokens: 2 };
  const systemPreparation = codingCompaction.prepareCompaction(systemEntries, systemSettings);
  if (!systemPreparation?.isSplitTurn) throw new Error("system-only history did not produce a split preparation");
  const systemCaptures: any[] = [];
  const systemOutput = await codingCompaction.compact(
    systemPreparation, model, undefined, undefined, undefined, undefined, "high",
    (_model: any, context: any, options: any) => {
      systemCaptures.push(capturedRequest(context, options));
      return { result: async () => completionResponse("prefix summary") };
    },
  );
  compactPromptCases.push({
    name: "system-only-history-first-split-turn",
    input: {
      entries: systemEntries,
      firstKeptEntryId: systemPreparation.firstKeptEntryId,
      messagesToSummarize: systemPreparation.messagesToSummarize,
      turnPrefixMessages: systemPreparation.turnPrefixMessages,
      isSplitTurn: systemPreparation.isSplitTurn,
      tokensBefore: systemPreparation.tokensBefore,
      previousSummary: null,
      settings: systemSettings,
    },
    expected: { captured: systemCaptures, output: systemOutput },
  });

  const productSummaryCases = [];
  for (const mode of ["summary", "prefix", "branch"]) {
    for (const stop of ["stop", "length", "error", "tool"]) {
      const captures: any[] = [];
      const response = { ...completionResponse("summary output"), stopReason: stop === "tool" ? "toolUse" : stop, ...(stop === "error" ? {errorMessage:"provider failed"} : {}), ...(stop === "tool" ? {content:[{type:"toolCall",id:"call",name:"read",arguments:{}}]} : {}) };
      const sessionId = "11111111-1111-7111-8111-111111111111";
      const streamFn = (_model:any, context:any, options:any) => {
        captures.push({...capturedRequest(context, options), routingPreserved: options.sessionId === sessionId});
        return {result:async()=>response};
      };
      let error: string | undefined;
      let output: any;
      try {
        if (mode === "branch") {
          const result = await codingBranch.generateBranchSummary([messageEntry("user",null,user("work",1),1)], {model,streamFn});
          error=result.error;output=result.summary;
        } else {
          const prep = {...compactPromptInput, firstKeptEntryId:"keep", messagesToSummarize:mode === "prefix" ? [] : [user("work",1)],turnPrefixMessages:mode === "prefix" ? [user("work",1)] : [],isSplitTurn:mode === "prefix",previousSummary:"previous summary",fileOps:{read:new Set(),written:new Set(),edited:new Set()}};
          output=(await codingCompaction.compact(prep,model,undefined,undefined,undefined,undefined,"off",streamFn,undefined,undefined,undefined,sessionId)).summary;
        }
      } catch(thrown) {error=(thrown as Error).message}
      productSummaryCases.push({mode,stop,modelMaxTokens:model.maxTokens,expected:{captures,...(error===undefined?{output}:{error})}});
    }
  }
  for(const modelMaxTokens of [0,9000]) {
    let captured:any;
    await codingBranch.generateBranchSummary([messageEntry("user",null,user("work",1),1)],{model:{...model,maxTokens:modelMaxTokens},streamFn:(_model:any,context:any,options:any)=>{captured=capturedRequest(context,options);return {result:async()=>completionResponse("summary output")}}});
    productSummaryCases.push({mode:"branch-limit",stop:"stop",modelMaxTokens,expected:{maxTokens:captured.options.maxTokens}});
  }

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
    orbOwned: ["tokenCases", "conversationCases", "contextCases", "prepareCases", "branchCases", "summaryPromptCases", "branchPromptCases"],
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
    compactPromptCases,
    productSummaryCases,
  }, null, 2)}\n`);
}
