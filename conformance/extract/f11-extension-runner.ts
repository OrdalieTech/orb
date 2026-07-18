import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

type Handler = (event: any, ctx: any) => unknown | Promise<unknown>;

function extension(extensionPath: string, handlers: Record<string, Handler | Handler[]>) {
  return {
    path: extensionPath,
    resolvedPath: extensionPath,
    sourceInfo: {
      path: extensionPath,
      source: "fixture",
      scope: "temporary",
      origin: "top-level",
    },
    handlers: new Map(
      Object.entries(handlers).map(([event, value]) => [event, Array.isArray(value) ? value : [value]]),
    ),
    tools: new Map(),
    messageRenderers: new Map(),
    entryRenderers: new Map(),
    commands: new Map(),
    flags: new Map(),
    shortcuts: new Map(),
  };
}

export async function generateF11ExtensionRunner(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  const source = "packages/coding-agent/src/core/extensions/runner.ts";
  const runnerModule = (await import(pathToFileURL(path.join(upstreamRoot, source)).href)) as any;
  const createRunner = (extensions: any[]) =>
    new runnerModule.ExtensionRunner(extensions, {} as any, "/fixture", {} as any, {} as any);

  const orderedCalls: string[] = [];
  const orderedErrors: Array<{ extensionPath: string; event: string; error: string }> = [];
  const orderedRunner = createRunner([
    extension("first", { agent_start: () => { orderedCalls.push("first"); } }),
    extension("broken", { agent_start: () => { orderedCalls.push("broken"); throw new Error("boom"); } }),
    extension("third", { agent_start: () => { orderedCalls.push("third"); } }),
  ]);
  orderedRunner.onError((error: any) => orderedErrors.push({
    extensionPath: error.extensionPath,
    event: error.event,
    error: error.error,
  }));
  await orderedRunner.emit({ type: "agent_start" });

  const originalContext = [{ role: "custom", nested: { value: "original" } }];
  const contextRunner = createRunner([
    extension("first", {
      context: (event) => {
        event.messages[0].nested.value = "first";
        return { messages: [...event.messages, { role: "custom", added: true }] };
      },
    }),
    extension("second", {
      context: (event) => {
        event.messages[0].seen = event.messages.length;
        return { messages: event.messages };
      },
    }),
  ]);
  const contextResult = await contextRunner.emitContext(originalContext);

  const toolResultErrors: string[] = [];
  const toolResultRunner = createRunner([
    extension("first", {
      tool_result: () => ({
        content: [{ type: "text", text: "first" }],
        details: { source: "first" },
      }),
    }),
    extension("broken", { tool_result: () => { throw new Error("ignored"); } }),
    extension("third", {
      tool_result: (event) => ({
        content: [...event.content, { type: "text", text: event.details.source }],
        isError: true,
      }),
    }),
  ]);
  toolResultRunner.onError((error: any) => toolResultErrors.push(error.error));
  const toolResult = await toolResultRunner.emitToolResult({
    type: "tool_result",
    toolName: "fixture",
    toolCallId: "call-1",
    input: {},
    content: [{ type: "text", text: "base" }],
    details: { initial: true },
    isError: false,
  });

  const toolInput = { command: "base" };
  const toolCallOrder: string[] = [];
  const toolCallRunner = createRunner([
    extension("first", {
      tool_call: (event) => {
        toolCallOrder.push("first");
        event.input.command = "prefixed";
      },
    }),
    extension("second", {
      tool_call: (event) => {
        toolCallOrder.push(`second:${event.input.command}`);
        return { block: true, reason: "denied" };
      },
    }),
    extension("third", { tool_call: () => { toolCallOrder.push("third"); } }),
  ]);
  const toolCall = await toolCallRunner.emitToolCall({
    type: "tool_call", toolName: "bash", toolCallId: "call-2", input: toolInput,
  });
  const failingToolCallRunner = createRunner([
    extension("broken", { tool_call: () => { throw new Error("tool-call-boom"); } }),
  ]);
  let toolCallFailure: { block: boolean; reason: string } | undefined;
  try {
    await failingToolCallRunner.emitToolCall({
      type: "tool_call", toolName: "bash", toolCallId: "call-3", input: {},
    });
  } catch (error) {
    toolCallFailure = { block: true, reason: error instanceof Error ? error.message : String(error) };
  }

  const beforeAgentRunner = createRunner([
    extension("first", {
      before_agent_start: (event, ctx) => ({
        message: { customType: "first", content: ctx.getSystemPrompt(), display: true },
        systemPrompt: `${event.systemPrompt}\nfirst`,
      }),
    }),
    extension("second", {
      before_agent_start: (event, ctx) => ({
        message: { customType: "second", content: ctx.getSystemPrompt(), display: true },
        systemPrompt: `${event.systemPrompt}\nsecond`,
      }),
    }),
  ]);
  const beforeAgent = await beforeAgentRunner.emitBeforeAgentStart(
    "hello", undefined, "base", { cwd: "/fixture" },
  );

  const inputRunner = createRunner([
    extension("first", { input: (event) => ({ action: "transform", text: `${event.text}[first]` }) }),
    extension("second", { input: (event) => ({ action: "transform", text: `${event.text}[second]` }) }),
  ]);
  const input = await inputRunner.emitInput("x", undefined, "rpc", "steer");

  const payloadRunner = createRunner([
    extension("first", {
      before_provider_request: (event) => ({ ...event.payload, first: true }),
      before_provider_headers: (event) => { event.headers["X-First"] = "one"; },
    }),
    extension("broken", {
      before_provider_request: () => { throw new Error("payload ignored"); },
      before_provider_headers: () => { throw new Error("headers ignored"); },
    }),
    extension("second", {
      before_provider_request: (event) => ({ ...event.payload, second: true }),
      before_provider_headers: (event) => { event.headers.Remove = null; event.headers["X-Second"] = "two"; },
    }),
  ]);
  const payloadErrors: string[] = [];
  payloadRunner.onError((error: any) => payloadErrors.push(`${error.event}:${error.error}`));
  const payload = await payloadRunner.emitBeforeProviderRequest({ base: true });
  const headers = await payloadRunner.emitBeforeProviderHeaders({ Existing: "yes", Remove: "old" });

  const resourcesRunner = createRunner([
    extension("first", {
      resources_discover: () => ({ skillPaths: ["/skill-a"], promptPaths: ["/prompt-a"] }),
    }),
    extension("second", {
      resources_discover: () => ({ skillPaths: ["/skill-b"], themePaths: ["/theme-b"] }),
    }),
  ]);
  const resources = await resourcesRunner.emitResourcesDiscover("/fixture", "startup");

  const trustExtensions = [
    extension("first", { project_trust: () => ({ trusted: "undecided", remember: true }) }),
    extension("second", { project_trust: () => ({ trusted: "no", remember: true }) }),
    extension("third", { project_trust: () => ({ trusted: "yes" }) }),
  ];
  const trust = await runnerModule.emitProjectTrustEvent(
    { extensions: trustExtensions, runtime: {} },
    { type: "project_trust", cwd: "/fixture" },
    {
      cwd: "/fixture",
      mode: "print",
      hasUI: false,
      ui: {
        select: async () => undefined,
        confirm: async () => false,
        input: async () => undefined,
        notify: () => {},
      },
    },
  );

  const sessionOrder: string[] = [];
  const sessionRunner = createRunner([
    extension("first", { session_before_switch: () => { sessionOrder.push("first"); return { cancel: false }; } }),
    extension("second", { session_before_switch: () => { sessionOrder.push("second"); return { cancel: true }; } }),
    extension("third", { session_before_switch: () => { sessionOrder.push("third"); } }),
  ]);
  const sessionBefore = await sessionRunner.emit({ type: "session_before_switch", reason: "new" });

  const cases = {
    orderedErrorIsolation: { calls: orderedCalls, errors: orderedErrors },
    contextMiddleware: { original: originalContext, result: contextResult },
    toolResultMiddleware: { result: toolResult, errors: toolResultErrors },
    toolCall: { input: toolInput, order: toolCallOrder, result: toolCall },
    toolCallFailure,
    beforeAgentStart: beforeAgent,
    input,
    providerHooks: { payload, headers, errors: payloadErrors },
    resources,
    projectTrust: trust,
    sessionBefore: { order: sessionOrder, result: sessionBefore },
  };

  const familyDir = path.join(outputRoot, "F11-native");
  await mkdir(familyDir, { recursive: true });
  await writeFile(path.join(familyDir, "cases.json"), `${JSON.stringify(cases, null, 2)}\n`);
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({
    family: "F11-native",
    upstreamCommit,
    generator: "conformance/extract/f11-extension-runner.ts",
    source,
    files: ["cases.json"],
  }, null, 2)}\n`);
}
