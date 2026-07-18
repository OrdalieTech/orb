import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

type LifecycleRecord = {
  phase: string;
  event?: Record<string, unknown>;
  cwd?: string;
};

type RuntimeModule = {
  AgentSessionRuntime: new (
    session: unknown,
    services: unknown,
    createRuntime: (options: Record<string, any>) => Promise<Record<string, any>>,
  ) => {
    setBeforeSessionInvalidate(callback?: () => void): void;
    setRebindSession(callback?: (session: unknown) => Promise<void>): void;
    newSession(options?: Record<string, unknown>): Promise<{ cancelled: boolean }>;
    dispose(): Promise<void>;
  };
};

type SessionManagerModule = {
  SessionManager: {
    inMemory(cwd: string): unknown;
  };
};

function normalizedEvent(event: Record<string, unknown>): Record<string, unknown> {
  return JSON.parse(JSON.stringify(event)) as Record<string, unknown>;
}

async function runNewSessionCase(
  Runtime: RuntimeModule["AgentSessionRuntime"],
  SessionManager: SessionManagerModule["SessionManager"],
  cancel: boolean,
): Promise<{ result: { cancelled: boolean }; records: LifecycleRecord[] }> {
  const records: LifecycleRecord[] = [];
  const cwd = "/fixture";

  const createSession = (manager: unknown) => ({
    sessionFile: undefined,
    sessionManager: manager,
    agent: { state: { messages: [] } },
    extensionRunner: {
      hasHandlers(type: string) {
        return type === "session_before_switch" || type === "session_shutdown";
      },
      async emit(event: Record<string, unknown>) {
        records.push({ phase: "event", event: normalizedEvent(event) });
        if (cancel && event.type === "session_before_switch") return { cancel: true };
        return undefined;
      },
    },
    dispose() {},
    createReplacedSessionContext() {
      return { cwd };
    },
  });

  const createRuntime = async (options: Record<string, any>) => {
    records.push({
      phase: "create",
      event: normalizedEvent(options.sessionStartEvent ?? {}),
      cwd: options.cwd,
    });
    return {
      session: createSession(options.sessionManager),
      services: { cwd: options.cwd, agentDir: options.agentDir },
      diagnostics: [],
    };
  };

  const manager = SessionManager.inMemory(cwd);
  const initial = await createRuntime({ cwd, agentDir: "/agent", sessionManager: manager });
  const runtime = new Runtime(initial.session, initial.services, createRuntime);
  records.length = 0;
  runtime.setBeforeSessionInvalidate(() => records.push({ phase: "beforeSessionInvalidate" }));
  runtime.setRebindSession(async () => {
    records.push({ phase: "rebindSession" });
  });
  const result = await runtime.newSession({
    setup: async () => {
      records.push({ phase: "setup" });
    },
    withSession: async (context: { cwd: string }) => {
      records.push({ phase: "withSession", cwd: context.cwd });
    },
  });
  return { result, records };
}

async function runDisposeCase(
  Runtime: RuntimeModule["AgentSessionRuntime"],
  SessionManager: SessionManagerModule["SessionManager"],
): Promise<LifecycleRecord[]> {
  const records: LifecycleRecord[] = [];
  const manager = SessionManager.inMemory("/fixture");
  const session = {
    sessionFile: undefined,
    sessionManager: manager,
    extensionRunner: {
      hasHandlers(type: string) { return type === "session_shutdown"; },
      async emit(event: Record<string, unknown>) {
        records.push({ phase: "event", event: normalizedEvent(event) });
      },
    },
    dispose() {},
  };
  const runtime = new Runtime(
    session,
    { cwd: "/fixture", agentDir: "/agent" },
    async () => ({ session, services: { cwd: "/fixture", agentDir: "/agent" }, diagnostics: [] }),
  );
  runtime.setBeforeSessionInvalidate(() => records.push({ phase: "beforeSessionInvalidate" }));
  await runtime.dispose();
  return records;
}

export async function generateWP370Runtime(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  const runtimeSource = "packages/coding-agent/src/core/agent-session-runtime.ts";
  const managerSource = "packages/coding-agent/src/core/session-manager.ts";
  const runtimeModule = await import(pathToFileURL(path.join(upstreamRoot, runtimeSource)).href) as RuntimeModule;
  const managerModule = await import(pathToFileURL(path.join(upstreamRoot, managerSource)).href) as SessionManagerModule;
  const success = await runNewSessionCase(runtimeModule.AgentSessionRuntime, managerModule.SessionManager, false);
  const cancelled = await runNewSessionCase(runtimeModule.AgentSessionRuntime, managerModule.SessionManager, true);
  const dispose = await runDisposeCase(runtimeModule.AgentSessionRuntime, managerModule.SessionManager);

  const familyDir = path.join(outputRoot, "WP370Runtime");
  await mkdir(familyDir, { recursive: true });
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({
    family: "WP370Runtime",
    upstreamCommit,
    generator: "conformance/extract/wp370-runtime.ts",
    source: `${runtimeSource}, ${managerSource}`,
    files: ["lifecycle.json"],
  }, null, 2)}\n`);
  await writeFile(path.join(familyDir, "lifecycle.json"), `${JSON.stringify({
    schemaVersion: 1,
    cases: { success, cancelled, dispose },
  }, null, 2)}\n`);
}
