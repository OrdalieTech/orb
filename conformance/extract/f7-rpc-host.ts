import path from "node:path";
import { createRequire, syncBuiltinESMExports } from "node:module";
import { pathToFileURL } from "node:url";

const FIXED_NOW = 1_700_000_200_321;
const FIXTURE_CWD = "/tmp/pi-go-f7-project";
const upstreamRoot = process.cwd();

const RealDate = Date;
const FixedDate = function (...args: any[]) {
  return Reflect.construct(RealDate, args.length > 0 ? args : [FIXED_NOW]);
} as unknown as DateConstructor;
FixedDate.prototype = RealDate.prototype;
Object.setPrototypeOf(FixedDate, RealDate);
FixedDate.now = () => FIXED_NOW;
globalThis.Date = FixedDate;
Math.random = () => 0;
let uuidCounter = 0;
const nodeCrypto = createRequire(import.meta.url)("node:crypto") as typeof import("node:crypto");
nodeCrypto.randomUUID = () => {
  uuidCounter++;
  const prefix = uuidCounter.toString(16).padStart(8, "0");
  const suffix = uuidCounter.toString(16).padStart(12, "0");
  return `${prefix}-0000-4000-8000-${suffix}`;
};
syncBuiltinESMExports();

{
  const faux = await import(
    `${pathToFileURL(path.join(upstreamRoot, "packages/ai/src/providers/faux.ts")).href}?f7-rpc-host`
  );
  const agentModule = await import(
    `${pathToFileURL(path.join(upstreamRoot, "packages/agent/src/agent.ts")).href}?f7-rpc-host`
  );
  const sessionModule = await import(
    `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/agent-session.ts")).href}?f7-rpc-host`
  );
  const sessionManagerModule = await import(
    `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/session-manager.ts")).href}?f7-rpc-host`
  );
  const settingsModule = await import(
    `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/settings-manager.ts")).href}?f7-rpc-host`
  );
  const authModule = await import(
    `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/auth-storage.ts")).href}?f7-rpc-host`
  );
  const modelRuntimeModule = await import(
    `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/model-runtime.ts")).href}?f7-rpc-host`
  );
  const rpcModule = await import(
    `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/modes/rpc/rpc-mode.ts")).href}?f7-rpc-host`
  );
  const utilities = await import(
    `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/test/utilities.ts")).href}?f7-rpc-host`
  );

  const core = faux.fauxProvider({
    api: "faux",
    provider: "faux",
    tokenSize: { min: 4, max: 4 },
  });
  const response = faux.fauxAssistantMessage("RPC fixture complete.", { timestamp: FIXED_NOW });
  core.setResponses([response]);
  const model = core.getModel();
  const agent = new agentModule.Agent({
    getApiKey: () => "faux-key",
    initialState: {
      model,
      systemPrompt: "Return one short answer.",
      tools: [],
    },
    streamFunction: core.provider.streamSimple.bind(core.provider),
  });
  const manager = sessionManagerModule.SessionManager.inMemory(FIXTURE_CWD, {
    id: "fixture-rpc-session",
  });
  const settings = settingsModule.SettingsManager.inMemory({
    compaction: { enabled: false },
    retry: { enabled: false },
  });
  const credentials = authModule.AuthStorage.inMemory();
  await credentials.modify("faux", async () => ({ type: "api_key", key: "faux-key" }));
  const modelRuntime = await modelRuntimeModule.ModelRuntime.create({
    credentials,
    modelsPath: null,
    allowModelNetwork: false,
  });
  modelRuntime.registerNativeProvider(core.provider);
  const resourceLoader = {
    ...utilities.createTestResourceLoader(),
    getSystemPrompt: () => "Return one short answer.",
  };
  const session = new sessionModule.AgentSession({
    agent,
    sessionManager: manager,
    settingsManager: settings,
    cwd: FIXTURE_CWD,
    modelRuntime,
    resourceLoader,
    baseToolsOverride: {},
    initialActiveToolNames: [],
  });

  let rebind: ((session: unknown) => Promise<void>) | undefined;
  const runtimeHost = {
    get session() {
      return session;
    },
    setRebindSession(callback: (session: unknown) => Promise<void>) {
      rebind = callback;
    },
    async newSession() {
      return { cancelled: true };
    },
    async switchSession() {
      return { cancelled: true };
    },
    async fork() {
      return { selectedText: "", cancelled: true };
    },
    async dispose() {
      rebind = undefined;
      session.dispose();
    },
  };

  await rpcModule.runRpcMode(runtimeHost as any);
}
