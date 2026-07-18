import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

type Handler = (event: any, ctx: any) => unknown | Promise<unknown>;

function captureAPI() {
  const handlers = new Map<string, Handler[]>();
  const commands = new Map<string, any>();
  const api = new Proxy({
    on(event: string, handler: Handler) {
      handlers.set(event, [...(handlers.get(event) ?? []), handler]);
    },
    registerCommand(name: string, command: any) {
      commands.set(name, command);
    },
  }, {
    get(target, property) {
      if (property in target) return (target as any)[property];
      return () => {};
    },
  });
  return { api, handlers, commands };
}

async function emit(handlers: Map<string, Handler[]>, event: string, value: any, ctx: any) {
  let result: any;
  for (const handler of handlers.get(event) ?? []) {
    result = await handler(value, ctx);
  }
  return result;
}

export async function generateF11ExtensionWiring(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  const examplesRoot = path.join(upstreamRoot, "packages/coding-agent/examples/extensions");
  const permissionFactory = (await import(pathToFileURL(path.join(examplesRoot, "permission-gate.ts")).href)).default;
  const pirateFactory = (await import(pathToFileURL(path.join(examplesRoot, "pirate.ts")).href)).default;
  const statusFactory = (await import(pathToFileURL(path.join(examplesRoot, "status-line.ts")).href)).default;

  const permission = captureAPI();
  permissionFactory(permission.api as any);
  const permissionGate = await emit(permission.handlers, "tool_call", {
    type: "tool_call", toolName: "bash", toolCallId: "danger", input: { command: "sudo true" },
  }, { hasUI: false });

  const pirate = captureAPI();
  pirateFactory(pirate.api as any);
  await pirate.commands.get("pirate").handler("", { ui: { notify() {} } });
  const pirateResult = await emit(pirate.handlers, "before_agent_start", {
    type: "before_agent_start", prompt: "go", systemPrompt: "base", systemPromptOptions: { cwd: "/fixture" },
  }, {});

  const status = captureAPI();
  statusFactory(status.api as any);
  const statusLine: Array<{ key: string; value: string | undefined }> = [];
  const statusContext = {
    ui: {
      theme: { fg(_color: string, text: string) { return text; } },
      setStatus(key: string, value: string | undefined) { statusLine.push({ key, value }); },
    },
  };
  await emit(status.handlers, "session_start", { type: "session_start", reason: "startup" }, statusContext);
  await emit(status.handlers, "turn_start", { type: "turn_start", turnIndex: 0, timestamp: 1 }, statusContext);
  await emit(status.handlers, "turn_end", { type: "turn_end", turnIndex: 0 }, statusContext);

  const wrapperSource = "packages/coding-agent/src/core/extensions/wrapper.ts";
  const wrapperModule = (await import(pathToFileURL(path.join(upstreamRoot, wrapperSource)).href)) as any;
  const wrap = async (before: string[], after: string[], addedToolNames: string[] | undefined) => {
    let call = 0;
    const runner = {
      createContext: () => ({}),
      getActiveTools: () => call++ === 0 ? before : after,
    };
    const registered = {
      definition: {
        name: "loader", label: "loader", description: "loader", parameters: {},
        execute: async () => ({ content: [], ...(addedToolNames === undefined ? {} : { addedToolNames }) }),
      },
      sourceInfo: { path: "fixture", source: "fixture", scope: "temporary", origin: "top-level" },
    };
    return wrapperModule.wrapRegisteredTool(registered, runner).execute("call", {}, new AbortController().signal);
  };

  const cases = {
    permissionGate,
    pirate: pirateResult,
    statusLine,
    deferredTools: {
      additive: await wrap(["loader"], ["loader", "late"], ["existing", "existing"]),
      noChange: await wrap(["loader"], ["loader"], ["duplicate", "duplicate"]),
      removal: await wrap(["loader", "old"], ["loader", "late"], ["existing"]),
    },
  };

  const familyDir = path.join(outputRoot, "F11-wire");
  await mkdir(familyDir, { recursive: true });
  await writeFile(path.join(familyDir, "cases.json"), `${JSON.stringify(cases, null, 2)}\n`);
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({
    family: "F11-wire",
    upstreamCommit,
    generator: "conformance/extract/f11-extension-wiring.ts",
    source: wrapperSource,
    files: ["cases.json"],
  }, null, 2)}\n`);
}
