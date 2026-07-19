import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { withUpstreamModelData } from "./upstream-model-data.ts";

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
  const loaderSource = "packages/coding-agent/src/core/extensions/loader.ts";
  const runnerModule = (await import(pathToFileURL(path.join(upstreamRoot, source)).href)) as any;
  const loaderModule = await withUpstreamModelData(
    upstreamRoot,
    async () => (await import(pathToFileURL(path.join(upstreamRoot, loaderSource)).href)) as any,
  );
  const createRunner = (extensions: any[]) =>
    new runnerModule.ExtensionRunner(
      extensions,
      { invalidate() {} } as any,
      "/fixture",
      {} as any,
      {} as any,
    );

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

  const genericCalls: string[] = [];
  const genericErrors: Array<{
    extensionPath: string;
    event: string;
    error: string;
    hasStack: boolean;
    stackIncludesOrigin: boolean;
  }> = [];
  function panicOriginMarker() {
    genericCalls.push("panic");
    throw new Error("panic-origin");
  }
  const genericRunner = createRunner([
    extension("returns-value", {
      agent_start: () => {
        genericCalls.push("returns-value");
        return { ignored: true };
      },
    }),
    extension("panic-origin", { agent_start: panicOriginMarker }),
    extension("after-panic", { agent_start: () => { genericCalls.push("after-panic"); } }),
  ]);
  genericRunner.onError((error: any) => genericErrors.push({
    extensionPath: error.extensionPath,
    event: error.event,
    error: error.error,
    hasStack: typeof error.stack === "string" && error.stack.length > 0,
    stackIncludesOrigin: typeof error.stack === "string" && error.stack.includes("panicOriginMarker"),
  }));
  const genericResult = await genericRunner.emit({ type: "agent_start" });

  const nilHandlerCalls: string[] = [];
  const nilHandlerErrors: Array<{ extensionPath: string; event: string; reported: boolean }> = [];
  const nilHandlerRunner = createRunner([
    extension("nil-handler", {
      agent_start: [undefined as unknown as Handler, () => { nilHandlerCalls.push("after-nil"); }],
    }),
  ]);
  nilHandlerRunner.onError((error: any) => nilHandlerErrors.push({
    extensionPath: error.extensionPath,
    event: error.event,
    reported: true,
  }));
  await nilHandlerRunner.emit({ type: "agent_start" });

  const identityImages = [{ type: "image", data: "original", mimeType: "image/png" }];
  const copiedIdentityRunner = createRunner([
    extension("copy-images", {
      input: (event) => ({ action: "transform", text: event.text, images: event.images.slice() }),
    }),
  ]);
  const copiedIdentity = await copiedIdentityRunner.emitInput("same", identityImages, "rpc");
  const retainedIdentityRunner = createRunner([
    extension("retain-images", {
      input: (event) => ({ action: "transform", text: event.text, images: event.images }),
    }),
  ]);
  const retainedIdentity = await retainedIdentityRunner.emitInput("same", identityImages, "rpc");

  const trustBoundaryOrder: string[] = [];
  const trustBoundaryErrors: Array<{ extensionPath: string; event: string; error: string }> = [];
  let trustBoundaryContext: Record<string, unknown> | undefined;
  const projectTrustContext = {
    cwd: "/fixture",
    mode: "print",
    hasUI: false,
    ui: {
      select: async () => undefined,
      confirm: async () => false,
      input: async () => undefined,
      notify: () => {},
    },
  };
  const observeTrustContext = (ctx: any) => {
    trustBoundaryContext ??= {
      cwd: ctx.cwd,
      mode: ctx.mode,
      hasUI: ctx.hasUI,
      hasGetSystemPrompt: "getSystemPrompt" in ctx,
      hasFullUI: "setStatus" in ctx.ui,
    };
  };
  const trustBoundaryExtensions = [
    extension("trust-broken", {
      project_trust: (_event, ctx) => {
        observeTrustContext(ctx);
        trustBoundaryOrder.push("broken");
        throw new Error("trust-boom");
      },
    }),
    extension("trust-undecided", {
      project_trust: (_event, ctx) => {
        observeTrustContext(ctx);
        trustBoundaryOrder.push("undecided");
        return { trusted: "undecided", remember: true };
      },
    }),
    extension("trust-decided", {
      project_trust: (_event, ctx) => {
        observeTrustContext(ctx);
        trustBoundaryOrder.push("decided");
        return { trusted: "no", remember: true };
      },
    }),
    extension("trust-after", {
      project_trust: () => {
        trustBoundaryOrder.push("after");
        return { trusted: "yes" };
      },
    }),
  ];
  const trustBoundary = await runnerModule.emitProjectTrustEvent(
    { extensions: trustBoundaryExtensions, runtime: {} },
    { type: "project_trust", cwd: "/fixture" },
    projectTrustContext,
  );
  for (const error of trustBoundary.errors) {
    trustBoundaryErrors.push({
      extensionPath: error.extensionPath,
      event: error.event,
      error: error.error,
    });
  }

  const trustStartupOrder: string[] = [];
  const trustStartupRuntime = loaderModule.createExtensionRuntime();
  trustStartupRuntime.registerProvider(
    "queued-before-trust",
    { baseUrl: "https://queued.test" },
    "trust-startup",
  );
  const trustStartupExtension = extension("trust-startup", {
    project_trust: () => {
      trustStartupOrder.push("project_trust");
      return { trusted: "yes" };
    },
  });
  await runnerModule.emitProjectTrustEvent(
    { extensions: [trustStartupExtension], runtime: trustStartupRuntime },
    { type: "project_trust", cwd: "/fixture" },
    projectTrustContext,
  );
  const trustStartupRunner = new runnerModule.ExtensionRunner(
    [trustStartupExtension], trustStartupRuntime, "/fixture", {} as any, {} as any,
  );
  trustStartupRunner.bindCore({} as any, {} as any, {
    registerProvider: () => { trustStartupOrder.push("register_provider"); },
  });

  const providerRegistrations: Array<Record<string, unknown>> = [];
  const providerErrors: Array<{ extensionPath: string; event: string; error: string }> = [];
  const providerRuntime = loaderModule.createExtensionRuntime();
  const oauth = {
    name: "Fixture OAuth",
    login: async () => ({ refresh: "refresh", access: "access", expires: 1 }),
    refreshToken: async (credentials: unknown) => credentials,
    getApiKey: () => "fixture-key",
  };
  providerRuntime.registerProvider(
    "config-first",
    { name: "Config First", baseUrl: "https://config.test", oauth },
    "config-extension",
  );
  providerRuntime.registerNativeProvider({
    id: "native-provider",
    name: "Native Provider",
    baseUrl: "https://native.test",
    auth: {
      apiKey: {
        name: "Native API key",
        resolve: async () => ({ auth: { apiKey: "fixture-key" }, source: "fixture" }),
      },
    },
    getModels: () => [],
    stream: () => { throw new Error("unused"); },
    streamSimple: () => { throw new Error("unused"); },
  }, "native-extension");
  providerRuntime.registerProvider("broken-provider", { name: "Broken" }, "broken-extension");
  providerRuntime.registerProvider("repeated-provider", { name: "Repeated One" }, "repeat-one");
  providerRuntime.registerProvider("repeated-provider", { name: "Repeated Two" }, "repeat-two");
  const providerModelRegistry = {
    registerProvider(first: any, second?: any) {
      if (typeof first === "string") {
        if (first === "broken-provider") throw new Error("bad registration");
        providerRegistrations.push({
          kind: "config",
          id: first,
          name: second?.name ?? null,
          oauthName: second?.oauth?.name ?? null,
        });
        return;
      }
      providerRegistrations.push({
        kind: "native",
        id: first.id,
        name: first.name,
        apiKeyName: first.auth?.apiKey?.name ?? null,
        hasOAuth: first.auth?.oauth !== undefined,
      });
    },
    unregisterProvider() {},
  };
  const providerRunner = new runnerModule.ExtensionRunner(
    [], providerRuntime, "/fixture", {} as any, providerModelRegistry as any,
  );
  providerRunner.onError((error: any) => providerErrors.push({
    extensionPath: error.extensionPath,
    event: error.event,
    error: error.error,
  }));
  providerRunner.bindCore({} as any, {} as any);
  let providerPostBindError: string | null = null;
  try {
    providerRuntime.registerProvider("post-bind", { name: "Post Bind" }, "post-bind-extension");
  } catch (error) {
    providerPostBindError = error instanceof Error ? error.message : String(error);
  }

  const registrationRuntime = loaderModule.createExtensionRuntime();
  const registrationBus = {} as any;
  const registrationFirst = await loaderModule.loadExtensionFromFactory(
    (api: any) => {
      api.registerTool({ name: "shared", description: "first-initial" });
      api.registerTool({ name: "shared", description: "first-final" });
      api.registerCommand("duplicate", { description: "first-initial", handler: async () => {} });
      api.registerCommand("duplicate", { description: "first-final", handler: async () => {} });
      api.registerFlag("shared", { type: "boolean", default: true, description: "first-initial" });
      api.registerFlag("shared", { type: "boolean", default: false, description: "first-final" });
    },
    "/fixture",
    registrationBus,
    registrationRuntime,
    "registration-first",
  );
  const registrationSecond = await loaderModule.loadExtensionFromFactory(
    (api: any) => {
      api.registerTool({ name: "shared", description: "second" });
      api.registerCommand("duplicate", { description: "second", handler: async () => {} });
      api.registerFlag("shared", { type: "boolean", default: false, description: "second" });
    },
    "/fixture",
    registrationBus,
    registrationRuntime,
    "registration-second",
  );
  const registrationRunner = new runnerModule.ExtensionRunner(
    [registrationFirst, registrationSecond], registrationRuntime, "/fixture", {} as any, {} as any,
  );
  const registrationFlag = registrationRunner.getFlags().get("shared");

  const lifecycleRecords: Array<Record<string, unknown>> = [];
  const lifecycleErrors: string[] = [];
  let lifecycleContext: any;
  const lifecycleRunner = createRunner([
    extension("shutdown-broken", {
      session_shutdown: () => { throw new Error("shutdown-boom"); },
    }),
    extension("shutdown-observer", {
      session_shutdown: (event, ctx) => {
        lifecycleContext = ctx;
        lifecycleRecords.push({
          type: event.type,
          reason: event.reason,
          targetSessionFile: event.targetSessionFile ?? null,
        });
        return { ignored: true };
      },
    }),
  ]);
  lifecycleRunner.onError((error: any) => lifecycleErrors.push(error.error));
  const lifecycleEmitted = await runnerModule.emitSessionShutdownEvent(lifecycleRunner, {
    type: "session_shutdown",
    reason: "resume",
    targetSessionFile: "/fixture/next.jsonl",
  });
  const lifecycleBeforeInvalidation = lifecycleContext.cwd;
  lifecycleRunner.invalidate("stale");
  let lifecycleStaleError: string | null = null;
  try {
    void lifecycleContext.cwd;
  } catch (error) {
    lifecycleStaleError = error instanceof Error ? error.message : String(error);
  }
  const lifecycleMissing = await runnerModule.emitSessionShutdownEvent(
    createRunner([]),
    { type: "session_shutdown", reason: "quit" },
  );

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
    genericVoidAndPanic: {
      calls: genericCalls,
      result: genericResult ?? null,
      errors: genericErrors,
    },
    nilHandler: { calls: nilHandlerCalls, errors: nilHandlerErrors },
    inputIdentity: { copied: copiedIdentity, retained: retainedIdentity },
    projectTrustBoundary: {
      order: trustBoundaryOrder,
      context: trustBoundaryContext,
      result: trustBoundary.result,
      errors: trustBoundaryErrors,
    },
    projectTrustStartupOrder: trustStartupOrder,
    providerRegistration: {
      registrations: providerRegistrations,
      errors: providerErrors,
      postBindError: providerPostBindError,
    },
    registrationConflicts: {
      toolDescriptions: registrationRunner.getAllRegisteredTools().map((tool: any) => tool.definition.description),
      commands: registrationRunner.getRegisteredCommands().map((command: any) => ({
        name: command.name,
        invocationName: command.invocationName,
        description: command.description,
      })),
      flag: {
        description: registrationFlag?.description ?? null,
        value: registrationRunner.getFlagValues().get("shared") ?? null,
      },
    },
    runnerLifecycle: {
      emitted: lifecycleEmitted,
      missing: lifecycleMissing,
      records: lifecycleRecords,
      errors: lifecycleErrors,
      beforeInvalidation: lifecycleBeforeInvalidation,
      staleError: lifecycleStaleError,
    },
  };

  const familyDir = path.join(outputRoot, "F11-native");
  await mkdir(familyDir, { recursive: true });
  await writeFile(path.join(familyDir, "cases.json"), `${JSON.stringify(cases, null, 2)}\n`);
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({
    family: "F11-native",
    upstreamCommit,
    generator: "conformance/extract/f11-extension-runner.ts",
    source: [
      source,
      loaderSource,
      "packages/coding-agent/test/extensions-runner.test.ts",
      "packages/coding-agent/test/extensions-input-event.test.ts",
      "packages/coding-agent/test/agent-session-dynamic-provider.test.ts",
      "packages/coding-agent/test/agent-session-runtime-events.test.ts",
    ].join("; "),
    files: ["cases.json"],
  }, null, 2)}\n`);
}
