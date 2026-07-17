import { execFile } from "node:child_process";
import { access, cp, mkdir, mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const FIXED_NOW = 1_700_000_000_321;

type ToolBehavior =
  | "echo"
  | "parallel-second-finishes-first"
  | "queue-steering"
  | "throw"
  | "terminate-all"
  | "none";

interface ScenarioDefinition {
  name: string;
  trace: string;
  systemPrompt: string;
  prompt: unknown;
  responses: unknown[];
  tools: Array<{
    name: string;
    label: string;
    description: string;
    parameters: unknown;
  }>;
  toolExecution: "parallel" | "sequential";
  toolBehavior: ToolBehavior;
  tokensPerSecond?: number;
  tokenSize: { min: number; max: number };
  steering?: { trigger: "tool_execute"; messages: unknown[] };
  abort?: { trigger: "first_text_delta" };
  expected: {
    providerCalls: number;
    pendingResponses: number;
    toolEndOrder?: string[];
    toolResultOrder?: string[];
  };
}

interface GeneratedScenario extends ScenarioDefinition {
  eventCount: number;
}

interface UpstreamModules {
  runAgentLoop: (
    prompts: any[],
    context: any,
    config: any,
    emit: (event: any) => Promise<void> | void,
    signal?: AbortSignal,
    streamFn?: any,
  ) => Promise<any[]>;
  createFauxCore: (options: any) => any;
  fauxAssistantMessage: (content: any, options?: any) => any;
  fauxText: (text: string) => any;
  fauxToolCall: (name: string, args: unknown, options?: { id?: string }) => any;
  Type: {
    Object(properties: Record<string, unknown>): any;
    String(): any;
  };
}

async function pathExists(candidate: string): Promise<boolean> {
  try {
    await access(candidate);
    return true;
  } catch {
    return false;
  }
}

async function withOfflineGeneratedCatalog<T>(upstreamRoot: string, run: () => Promise<T>): Promise<T> {
  // agent-loop.ts imports the legacy compat entrypoint, which eagerly imports
  // generated, gitignored provider JSON. Generate that pinned baseline in a
  // temporary package copy, then restore the checkout byte-for-byte afterward.
  const temporaryRoot = await mkdtemp(path.join(tmpdir(), "pi-go-f3-agent-"));
  const temporaryPackage = path.join(temporaryRoot, "ai");
  const generatedData = path.join(temporaryPackage, "src/providers/data");
  const targetData = path.join(upstreamRoot, "packages/ai/src/providers/data");
  const backupData = path.join(temporaryRoot, "provider-data-backup");
  let hadTargetData = false;
  let targetReplaced = false;

  try {
    await cp(path.join(upstreamRoot, "packages/ai"), temporaryPackage, { recursive: true });
    const disableNetwork = path.join(temporaryRoot, "disable-network.mjs");
    await writeFile(
      disableNetwork,
      'globalThis.fetch = async () => { throw new Error("network disabled for deterministic F3 extraction"); };\n',
    );
    await execFileAsync(
      process.execPath,
      ["--import", pathToFileURL(disableNetwork).href, path.join(temporaryPackage, "scripts/generate-models.ts")],
      { cwd: temporaryPackage, maxBuffer: 16 * 1024 * 1024 },
    );

    // Offline generation legitimately has no entries for some remote-only
    // providers, but compat imports every generated module eagerly. Complete
    // the generated directory with empty catalogs for those unrelated modules.
    const providerSource = path.join(upstreamRoot, "packages/ai/src/providers");
    for (const entry of await readdir(providerSource, { withFileTypes: true })) {
      if (!entry.isFile() || !entry.name.endsWith(".models.ts")) continue;
      const source = await readFile(path.join(providerSource, entry.name), "utf8");
      const match = source.match(/\.\/data\/([^"']+\.json)/);
      if (!match) continue;
      const dataFile = path.join(generatedData, match[1]);
      if (!(await pathExists(dataFile))) {
        await mkdir(path.dirname(dataFile), { recursive: true });
        await writeFile(dataFile, "{}\n");
      }
    }

    hadTargetData = await pathExists(targetData);
    if (hadTargetData) {
      await cp(targetData, backupData, { recursive: true });
    }
    targetReplaced = true;
    await rm(targetData, { recursive: true, force: true });
    await cp(generatedData, targetData, { recursive: true });
    if (!(await pathExists(path.join(targetData, "amazon-bedrock.json")))) {
      throw new Error("F3 offline provider catalog is incomplete: amazon-bedrock.json is missing");
    }
    return await run();
  } finally {
    try {
      if (targetReplaced) {
        await rm(targetData, { recursive: true, force: true });
        if (hadTargetData) {
          await cp(backupData, targetData, { recursive: true });
        }
      }
    } finally {
      await rm(temporaryRoot, { recursive: true, force: true });
    }
  }
}

async function loadUpstream(upstreamRoot: string): Promise<UpstreamModules> {
  const agentLoop = (await import(
    pathToFileURL(path.join(upstreamRoot, "packages/agent/src/agent-loop.ts")).href
  )) as Pick<UpstreamModules, "runAgentLoop">;
  const faux = (await import(
    pathToFileURL(path.join(upstreamRoot, "packages/ai/src/providers/faux.ts")).href
  )) as Pick<
    UpstreamModules,
    "createFauxCore" | "fauxAssistantMessage" | "fauxText" | "fauxToolCall"
  >;
  const ai = (await import(
    pathToFileURL(path.join(upstreamRoot, "packages/ai/src/index.ts")).href
  )) as Pick<UpstreamModules, "Type">;
  return { ...agentLoop, ...faux, ...ai };
}

function serializableTool(tool: any): ScenarioDefinition["tools"][number] {
  return {
    name: tool.name,
    label: tool.label,
    description: tool.description,
    parameters: JSON.parse(JSON.stringify(tool.parameters)),
  };
}

function buildScenarios(upstream: UpstreamModules): Array<ScenarioDefinition & { runtimeTools: any[] }> {
  const { Type, fauxAssistantMessage, fauxText, fauxToolCall } = upstream;
  const valueSchema = Type.Object({ value: Type.String() });

  const echoTool = {
    name: "echo",
    label: "Echo",
    description: "Echo a value",
    parameters: valueSchema,
  };
  const failTool = {
    name: "explode",
    label: "Explode",
    description: "Fail with a deterministic error",
    parameters: valueSchema,
  };
  const finishTool = {
    name: "finish",
    label: "Finish",
    description: "Return a terminating result",
    parameters: valueSchema,
  };

  const user = (text: string, timestamp = FIXED_NOW - 20) => ({ role: "user", content: text, timestamp });
  const assistant = (content: any, stopReason: "stop" | "toolUse" = "stop") =>
    fauxAssistantMessage(content, { stopReason, timestamp: FIXED_NOW - 10 });

  return [
    {
      name: "basic-multi-turn-tool",
      trace: "basic-multi-turn-tool.jsonl",
      systemPrompt: "Use the echo tool, then answer.",
      prompt: user("Echo hello."),
      responses: [
        assistant(
          [fauxText("Calling echo."), fauxToolCall("echo", { value: "hello" }, { id: "basic-1" })],
          "toolUse",
        ),
        assistant("Echo complete."),
      ],
      tools: [serializableTool(echoTool)],
      runtimeTools: [echoTool],
      toolExecution: "parallel",
      toolBehavior: "echo",
      tokenSize: { min: 64, max: 64 },
      expected: {
        providerCalls: 2,
        pendingResponses: 0,
        toolEndOrder: ["basic-1"],
        toolResultOrder: ["basic-1"],
      },
    },
    {
      name: "parallel-completion-vs-source-order",
      trace: "parallel-completion-vs-source-order.jsonl",
      systemPrompt: "Echo both values.",
      prompt: user("Echo first and second."),
      responses: [
        assistant(
          [
            fauxToolCall("echo", { value: "first" }, { id: "parallel-1" }),
            fauxToolCall("echo", { value: "second" }, { id: "parallel-2" }),
          ],
          "toolUse",
        ),
        assistant("Both echoes complete."),
      ],
      tools: [serializableTool(echoTool)],
      runtimeTools: [echoTool],
      toolExecution: "parallel",
      toolBehavior: "parallel-second-finishes-first",
      tokenSize: { min: 64, max: 64 },
      expected: {
        providerCalls: 2,
        pendingResponses: 0,
        toolEndOrder: ["parallel-2", "parallel-1"],
        toolResultOrder: ["parallel-1", "parallel-2"],
      },
    },
    {
      name: "steering-queued-during-tools",
      trace: "steering-queued-during-tools.jsonl",
      systemPrompt: "Work, then honor steering.",
      prompt: user("Start work."),
      responses: [
        assistant(fauxToolCall("echo", { value: "work" }, { id: "steering-1" }), "toolUse"),
        assistant("Steering applied."),
      ],
      tools: [serializableTool(echoTool)],
      runtimeTools: [echoTool],
      toolExecution: "parallel",
      toolBehavior: "queue-steering",
      tokenSize: { min: 64, max: 64 },
      steering: {
        trigger: "tool_execute",
        messages: [user("Change direction now.", FIXED_NOW - 5)],
      },
      expected: {
        providerCalls: 2,
        pendingResponses: 0,
        toolEndOrder: ["steering-1"],
        toolResultOrder: ["steering-1"],
      },
    },
    {
      name: "abort-mid-text",
      trace: "abort-mid-text.jsonl",
      systemPrompt: "Stream a long answer.",
      prompt: user("Begin streaming."),
      responses: [assistant("alpha beta gamma delta epsilon")],
      tools: [],
      runtimeTools: [],
      toolExecution: "parallel",
      toolBehavior: "none",
      tokensPerSecond: 100,
      tokenSize: { min: 1, max: 1 },
      abort: { trigger: "first_text_delta" },
      expected: { providerCalls: 1, pendingResponses: 0 },
    },
    {
      name: "tool-error-recovery",
      trace: "tool-error-recovery.jsonl",
      systemPrompt: "Recover after a tool failure.",
      prompt: user("Run the failing tool."),
      responses: [
        assistant(fauxToolCall("explode", { value: "boom" }, { id: "error-1" }), "toolUse"),
        assistant("Recovered from tool error."),
      ],
      tools: [serializableTool(failTool)],
      runtimeTools: [failTool],
      toolExecution: "parallel",
      toolBehavior: "throw",
      tokenSize: { min: 64, max: 64 },
      expected: {
        providerCalls: 2,
        pendingResponses: 0,
        toolEndOrder: ["error-1"],
        toolResultOrder: ["error-1"],
      },
    },
    {
      name: "all-results-terminate",
      trace: "all-results-terminate.jsonl",
      systemPrompt: "Stop when every tool finishes the run.",
      prompt: user("Finish both tasks."),
      responses: [
        assistant(
          [
            fauxToolCall("finish", { value: "first" }, { id: "terminate-1" }),
            fauxToolCall("finish", { value: "second" }, { id: "terminate-2" }),
          ],
          "toolUse",
        ),
      ],
      tools: [serializableTool(finishTool)],
      runtimeTools: [finishTool],
      toolExecution: "parallel",
      toolBehavior: "terminate-all",
      tokenSize: { min: 64, max: 64 },
      expected: {
        providerCalls: 1,
        pendingResponses: 0,
        toolEndOrder: ["terminate-1", "terminate-2"],
        toolResultOrder: ["terminate-1", "terminate-2"],
      },
    },
  ];
}

function textResult(prefix: string, value: string, terminate?: boolean) {
  return {
    content: [{ type: "text", text: `${prefix}:${value}` }],
    details: { value },
    ...(terminate === undefined ? {} : { terminate }),
  };
}

async function runScenario(
  upstream: UpstreamModules,
  definition: ScenarioDefinition & { runtimeTools: any[] },
): Promise<{ lines: string[]; generated: GeneratedScenario }> {
  const core = upstream.createFauxCore({
    api: "faux",
    provider: "faux",
    tokensPerSecond: definition.tokensPerSecond,
    tokenSize: definition.tokenSize,
  });
  core.setResponses(definition.responses);

  let releaseFirst: (() => void) | undefined;
  const firstMayFinish = new Promise<void>((resolve) => {
    releaseFirst = resolve;
  });
  let releaseSecondTerminate: (() => void) | undefined;
  const secondTerminateMayFinish = new Promise<void>((resolve) => {
    releaseSecondTerminate = resolve;
  });
  let steeringReady = false;
  let steeringDelivered = false;

  const tools = definition.runtimeTools.map((tool) => ({
    ...tool,
    async execute(_toolCallId: string, params: { value: string }) {
      switch (definition.toolBehavior) {
        case "parallel-second-finishes-first":
          if (params.value === "first") await firstMayFinish;
          return textResult("echo", params.value);
        case "queue-steering":
          steeringReady = true;
          return textResult("echo", params.value);
        case "throw":
          throw new Error("fixture tool exploded");
        case "terminate-all":
          if (params.value === "second") await secondTerminateMayFinish;
          return textResult("finished", params.value, true);
        case "echo":
          return textResult("echo", params.value);
        case "none":
          throw new Error("scenario configured a tool with no behavior");
      }
    },
  }));

  const controller = new AbortController();
  const lines: string[] = [];
  let aborted = false;
  await upstream.runAgentLoop(
    [definition.prompt],
    { systemPrompt: definition.systemPrompt, messages: [], tools },
    {
      model: core.getModel(),
      convertToLlm: (messages: any[]) =>
        messages.filter(
          (message) => message.role === "user" || message.role === "assistant" || message.role === "toolResult",
        ),
      toolExecution: definition.toolExecution,
      getSteeringMessages: definition.steering
        ? async () => {
            if (!steeringReady || steeringDelivered) return [];
            steeringDelivered = true;
            return definition.steering?.messages ?? [];
          }
        : undefined,
    },
    async (event) => {
      lines.push(JSON.stringify(event));
      if (event.type === "tool_execution_end" && event.toolCallId === "parallel-2") {
        releaseFirst?.();
      }
      if (event.type === "tool_execution_end" && event.toolCallId === "terminate-1") {
        releaseSecondTerminate?.();
      }
      if (
        definition.abort?.trigger === "first_text_delta" &&
        !aborted &&
        event.type === "message_update" &&
        event.assistantMessageEvent.type === "text_delta"
      ) {
        aborted = true;
        controller.abort();
      }
    },
    controller.signal,
    core.streamSimple,
  );

  const events = lines.map((line) => JSON.parse(line));
  validateScenario(definition, events, core.state.callCount, core.getPendingResponseCount());
  const { runtimeTools: _runtimeTools, ...serializable } = definition;
  return {
    lines,
    generated: { ...serializable, eventCount: lines.length },
  };
}

function validateScenario(
  definition: ScenarioDefinition,
  events: any[],
  providerCalls: number,
  pendingResponses: number,
): void {
  const fail = (message: string): never => {
    throw new Error(`${definition.name}: ${message}`);
  };
  if (events[0]?.type !== "agent_start") fail("trace does not start with agent_start");
  if (events.at(-1)?.type !== "agent_end") fail("trace does not end with agent_end");
  if (providerCalls !== definition.expected.providerCalls) {
    fail(`provider calls ${providerCalls}, want ${definition.expected.providerCalls}`);
  }
  if (pendingResponses !== definition.expected.pendingResponses) {
    fail(`pending responses ${pendingResponses}, want ${definition.expected.pendingResponses}`);
  }

  const toolEndOrder = events
    .filter((event) => event.type === "tool_execution_end")
    .map((event) => event.toolCallId);
  const toolResultOrder = events
    .filter((event) => event.type === "message_end" && event.message?.role === "toolResult")
    .map((event) => event.message.toolCallId);
  if (
    definition.expected.toolEndOrder &&
    JSON.stringify(toolEndOrder) !== JSON.stringify(definition.expected.toolEndOrder)
  ) {
    fail(`tool end order ${JSON.stringify(toolEndOrder)}, want ${JSON.stringify(definition.expected.toolEndOrder)}`);
  }
  if (
    definition.expected.toolResultOrder &&
    JSON.stringify(toolResultOrder) !== JSON.stringify(definition.expected.toolResultOrder)
  ) {
    fail(
      `tool result order ${JSON.stringify(toolResultOrder)}, want ${JSON.stringify(definition.expected.toolResultOrder)}`,
    );
  }

  if (definition.abort) {
    const terminalTurn = events.findLast((event) => event.type === "turn_end");
    if (terminalTurn?.message?.stopReason !== "aborted") fail("abort trace did not end with stopReason=aborted");
  }
  if (definition.toolBehavior === "throw") {
    const toolEnd = events.find((event) => event.type === "tool_execution_end");
    if (!toolEnd?.isError) fail("throwing tool was not emitted as an error result");
    const finalTurn = events.findLast((event) => event.type === "turn_end");
    if (finalTurn?.message?.stopReason !== "stop") fail("loop did not recover after the tool error");
  }
  if (definition.steering) {
    const toolResultIndex = events.findIndex(
      (event) => event.type === "message_end" && event.message?.role === "toolResult",
    );
    const steeringIndex = events.findIndex(
      (event) => event.type === "message_start" && event.message?.role === "user" && event.message?.content === "Change direction now.",
    );
    if (toolResultIndex < 0 || steeringIndex <= toolResultIndex) {
      fail("steering message was not injected after tool results");
    }
  }
}

export async function generateF3(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const originalNow = Date.now;
  Date.now = () => FIXED_NOW;
  try {
    await withOfflineGeneratedCatalog(upstreamRoot, async () => {
      const upstream = await loadUpstream(upstreamRoot);
      const definitions = buildScenarios(upstream);
      const familyDir = path.join(outputRoot, "F3");
      await rm(familyDir, { recursive: true, force: true });
      await mkdir(familyDir, { recursive: true });

      const cases: GeneratedScenario[] = [];
      for (const definition of definitions) {
        const { lines, generated } = await runScenario(upstream, definition);
        cases.push(generated);
        await writeFile(path.join(familyDir, definition.trace), `${lines.join("\n")}\n`);
      }

      const traceFiles = cases.map((fixtureCase) => fixtureCase.trace);
      const manifest = {
        family: "F3",
        upstreamCommit,
        generator: "conformance/extract/f3-agent.ts",
        source:
          "packages/agent/src/agent-loop.ts + packages/agent/src/agent.ts + packages/agent/src/types.ts + packages/ai/src/providers/faux.ts",
        format: "agent-event-jsonl-v1",
        files: ["cases.json", ...traceFiles],
      };
      await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
      await writeFile(
        path.join(familyDir, "cases.json"),
        `${JSON.stringify({ schemaVersion: 1, fixedNow: FIXED_NOW, cases }, null, 2)}\n`,
      );
    });
  } finally {
    Date.now = originalNow;
  }
}
