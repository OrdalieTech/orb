import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withOfflineGeneratedCatalog } from "./f3-agent.ts";

const FIXED_NOW = 1_700_000_100_321;

interface Scenario {
  name: string;
  trace: string;
  fixedNow: number;
  systemPrompt: string;
  initialMessage?: string;
  messages: string[];
  tokenSize: number;
  settings: Record<string, unknown>;
  queue?: { steering: string[]; followUp: string[] };
  compactAfterFirstPrompt?: boolean;
  responses: unknown[];
  expectedExitCode: number;
  requiredEventTypes: string[];
}

function captureStdout(run: () => Promise<number>): Promise<{ output: string; exitCode: number }> {
  const originalWrite = process.stdout.write;
  let output = "";
  process.stdout.write = ((
    chunk: string | Uint8Array,
    encodingOrCallback?: BufferEncoding | ((error?: Error | null) => void),
    callback?: (error?: Error | null) => void,
  ): boolean => {
    output += typeof chunk === "string" ? chunk : Buffer.from(chunk).toString();
    const done = typeof encodingOrCallback === "function" ? encodingOrCallback : callback;
    done?.(null);
    return true;
  }) as typeof process.stdout.write;
  return run()
    .then((exitCode) => ({ output, exitCode }))
    .finally(() => {
      process.stdout.write = originalWrite;
    });
}

export async function generateF3Session(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  const originalDate = Date;
  const originalNow = Date.now;
  const originalRandom = Math.random;
  Date.now = () => FIXED_NOW;
  Math.random = () => 0;
  try {
    await withOfflineGeneratedCatalog(upstreamRoot, async () => {
      const faux = await import(
        `${pathToFileURL(path.join(upstreamRoot, "packages/ai/src/providers/faux.ts")).href}?f3-session`
      );
      const agentModule = await import(
        `${pathToFileURL(path.join(upstreamRoot, "packages/agent/src/agent.ts")).href}?f3-session`
      );
      const sessionModule = await import(
        `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/agent-session.ts")).href}?f3-session`
      );
      const sessionManagerModule = await import(
        `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/session-manager.ts")).href}?f3-session`
      );
      const settingsModule = await import(
        `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/settings-manager.ts")).href}?f3-session`
      );
      const authModule = await import(
        `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/auth-storage.ts")).href}?f3-session`
      );
      const modelRuntimeModule = await import(
        `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/core/model-runtime.ts")).href}?f3-session`
      );
      const printModeModule = await import(
        `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/modes/print-mode.ts")).href}?f3-session`
      );
      const utilities = await import(
        `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/test/utilities.ts")).href}?f3-session`
      );

      const response = (content: unknown, options: Record<string, unknown> = {}) =>
        faux.fauxAssistantMessage(content, { timestamp: FIXED_NOW, ...options });
      const definitions: Omit<Scenario, "systemPrompt">[] = [
        {
          name: "multiple-prompts",
          trace: "multiple-prompts.jsonl",
          fixedNow: FIXED_NOW,
          initialMessage: "first prompt",
          messages: ["second prompt", "third prompt"],
          tokenSize: 64,
          settings: { compaction: { enabled: false }, retry: { enabled: false } },
          responses: [response("first answer"), response("second answer"), response("third answer")],
          expectedExitCode: 0,
          requiredEventTypes: ["agent_start", "message_update", "agent_end", "agent_settled"],
        },
        {
          name: "queue",
          trace: "queue.jsonl",
          fixedNow: FIXED_NOW,
          initialMessage: "primary prompt",
          messages: [],
          tokenSize: 64,
          settings: { compaction: { enabled: false }, retry: { enabled: false } },
          queue: { steering: ["steer now"], followUp: ["follow up"] },
          responses: [response("primary answer"), response("queued answer")],
          expectedExitCode: 0,
          requiredEventTypes: ["queue_update", "agent_start", "agent_end", "agent_settled"],
        },
        {
          name: "retry",
          trace: "retry.jsonl",
          fixedNow: FIXED_NOW,
          initialMessage: "retry prompt",
          messages: [],
          tokenSize: 64,
          settings: {
            compaction: { enabled: false },
            retry: { enabled: true, maxRetries: 1, baseDelayMs: 0 },
          },
          responses: [
            response([], { stopReason: "error", errorMessage: "429 Too Many Requests" }),
            response("retry recovered"),
          ],
          expectedExitCode: 0,
          requiredEventTypes: ["auto_retry_start", "auto_retry_end", "agent_end", "agent_settled"],
        },
        {
          name: "compaction",
          trace: "compaction.jsonl",
          fixedNow: FIXED_NOW,
          initialMessage: "short session",
          messages: [],
          tokenSize: 64,
          settings: { compaction: { enabled: true, reserveTokens: 16384, keepRecentTokens: 20000 }, retry: { enabled: false } },
          compactAfterFirstPrompt: true,
          responses: [response("too short to compact")],
          expectedExitCode: 0,
          requiredEventTypes: ["compaction_start", "compaction_end", "agent_settled"],
        },
        {
          name: "assistant-error",
          trace: "assistant-error.jsonl",
          fixedNow: FIXED_NOW,
          initialMessage: "fail",
          messages: [],
          tokenSize: 64,
          settings: { compaction: { enabled: false }, retry: { enabled: false } },
          responses: [response([], { stopReason: "error", errorMessage: "provider failed" })],
          expectedExitCode: 0,
          requiredEventTypes: ["agent_end", "agent_settled"],
        },
        {
          name: "assistant-aborted",
          trace: "assistant-aborted.jsonl",
          fixedNow: FIXED_NOW,
          initialMessage: "abort",
          messages: [],
          tokenSize: 64,
          settings: { compaction: { enabled: false }, retry: { enabled: false } },
          responses: [response([], { stopReason: "aborted", errorMessage: "Request was aborted" })],
          expectedExitCode: 0,
          requiredEventTypes: ["agent_end", "agent_settled"],
        },
      ];

      const familyDir = path.join(outputRoot, "F3-session");
      await rm(familyDir, { recursive: true, force: true });
      await mkdir(familyDir, { recursive: true });
      const scenarios: Scenario[] = [];

      for (const definition of definitions) {
        const core = faux.createFauxCore({
          api: "faux",
          provider: "faux",
          tokenSize: { min: definition.tokenSize, max: definition.tokenSize },
        });
        core.setResponses(definition.responses);
        const model = core.getModel();
        const manager = sessionManagerModule.SessionManager.inMemory("/fixture/project", {
          id: `fixture-json-${definition.name}`,
        });
        const settings = settingsModule.SettingsManager.inMemory(definition.settings);
        const credentials = authModule.AuthStorage.inMemory();
        await credentials.modify("faux", async () => ({ type: "api_key", key: "faux-key" }));
        const modelRuntime = await modelRuntimeModule.ModelRuntime.create({
          credentials,
          modelsPath: null,
          allowModelNetwork: false,
        });
        modelRuntime.registerProvider("faux", {
          baseUrl: model.baseUrl,
          api: model.api,
          models: [
            {
              id: model.id,
              name: model.name,
              api: model.api,
              reasoning: model.reasoning,
              input: model.input,
              cost: model.cost,
              contextWindow: model.contextWindow,
              maxTokens: model.maxTokens,
              baseUrl: model.baseUrl,
            },
          ],
        });
        const resourceLoader = {
          ...utilities.createTestResourceLoader(),
          getSystemPrompt: () => "Exercise JSON event streaming.",
        };
        const agent = new agentModule.Agent({
          getApiKey: () => "faux-key",
          initialState: { model, systemPrompt: "Exercise JSON event streaming.", tools: [] },
          streamFn: core.streamSimple,
        });
        const session = new sessionModule.AgentSession({
          agent,
          sessionManager: manager,
          settingsManager: settings,
          cwd: "/fixture/project",
          modelRuntime,
          resourceLoader,
          baseToolsOverride: {},
          initialActiveToolNames: [],
        });
        const originalPrompt = session.prompt.bind(session);
        let promptIndex = 0;
        session.prompt = async (text: string, options?: unknown) => {
          const index = promptIndex++;
          if (index === 0 && definition.queue) {
            for (const queued of definition.queue.steering) await session.steer(queued);
            for (const queued of definition.queue.followUp) await session.followUp(queued);
          }
          await originalPrompt(text, options);
          if (index === 0 && definition.compactAfterFirstPrompt) {
            try {
              await session.compact();
            } catch {
              // The deliberately short session exercises the upstream failure event shape.
            }
          }
        };

        const runtimeHost = {
          session,
          setRebindSession() {},
          async newSession() { return { cancelled: false }; },
          async fork() { return { cancelled: false }; },
          async switchSession() { return { cancelled: false }; },
          async dispose() { session.dispose(); },
        };
        const captured = await captureStdout(() =>
          printModeModule.runPrintMode(runtimeHost, {
            mode: "json",
            initialMessage: definition.initialMessage,
            messages: definition.messages,
          }),
        );
        const lines = captured.output.endsWith("\n") ? captured.output.slice(0, -1).split("\n") : [];
        const header = lines.length > 0 ? JSON.parse(lines[0]) : undefined;
        if (
          lines.length < 2 ||
          header?.type !== "session" ||
          header.version !== 3 ||
          header.id !== `fixture-json-${definition.name}` ||
          header.cwd !== "/fixture/project"
        ) {
          throw new Error(`${definition.name}: runPrintMode did not emit a header followed by events`);
        }
        header.timestamp = new originalDate(FIXED_NOW).toISOString();
        lines[0] = JSON.stringify(header);
        const output = `${lines.join("\n")}\n`;
        const events = lines.slice(1).map((line) => JSON.parse(line));
        for (const type of definition.requiredEventTypes) {
          if (!events.some((event) => event.type === type)) {
            throw new Error(`${definition.name}: runPrintMode did not emit ${type}`);
          }
        }
        if (captured.exitCode !== definition.expectedExitCode) {
          throw new Error(`${definition.name}: exit code ${captured.exitCode}, want ${definition.expectedExitCode}`);
        }
        if (core.getPendingResponseCount() !== 0) {
          throw new Error(`${definition.name}: ${core.getPendingResponseCount()} faux responses remain`);
        }
        const scenario: Scenario = { ...definition, systemPrompt: session.systemPrompt };
        scenarios.push(scenario);
        await writeFile(path.join(familyDir, definition.trace), output);
      }

      const traceFiles = scenarios.map((scenario) => scenario.trace);
      const manifest = {
        family: "F3-session",
        upstreamCommit,
        generator: "conformance/extract/f3-session.ts",
        source:
          "packages/coding-agent/src/modes/print-mode.ts + packages/coding-agent/src/core/agent-session.ts + packages/ai/src/providers/faux.ts",
        format: "agent-session-event-jsonl-v1",
        canonicalized: ["session.timestamp"],
        files: ["scenarios.json", ...traceFiles],
      };
      await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
      await writeFile(
        path.join(familyDir, "scenarios.json"),
        `${JSON.stringify({ schemaVersion: 2, fixedNow: FIXED_NOW, scenarios }, null, 2)}\n`,
      );
    });
  } finally {
    Date.now = originalNow;
    Math.random = originalRandom;
  }
}
