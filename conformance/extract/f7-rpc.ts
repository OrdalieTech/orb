import { spawn } from "node:child_process";
import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";

import { withOfflineGeneratedCatalog } from "./f3-agent.ts";

const FIXED_NOW = 1_700_000_200_321;
const FIXTURE_CWD = "/tmp/pi-go-f7-project";

interface TranscriptStep {
  name: string;
  input: string;
  framing: "lf" | "crlf";
  expectedLineCount: number;
}

interface OutputLine {
  raw: string;
  value: any;
}

export async function generateF7(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  await withOfflineGeneratedCatalog(upstreamRoot, async () => {
    await generateF7WithCatalog(upstreamRoot, outputRoot, upstreamCommit);
  });
}

async function generateF7WithCatalog(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  await rm(FIXTURE_CWD, { recursive: true, force: true });
  await mkdir(FIXTURE_CWD, { recursive: true });
  const helper = path.resolve(upstreamRoot, "../conformance/extract/f7-rpc-host.ts");
  const child = spawn(process.execPath, ["--import", "tsx", helper], {
    cwd: upstreamRoot,
    env: { ...process.env, NO_COLOR: "1" },
    stdio: ["pipe", "pipe", "pipe"],
  });
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");

  let stderr = "";
  child.stderr.on("data", (chunk) => {
    stderr += chunk;
  });
  const pending: OutputLine[] = [];
  const waiters: Array<(line: OutputLine) => void> = [];
  let stdoutBuffer = "";
  child.stdout.on("data", (chunk: string) => {
    stdoutBuffer += chunk;
    while (true) {
      const newline = stdoutBuffer.indexOf("\n");
      if (newline < 0) break;
      let raw = stdoutBuffer.slice(0, newline);
      stdoutBuffer = stdoutBuffer.slice(newline + 1);
      if (raw.endsWith("\r")) raw = raw.slice(0, -1);
      const line = { raw, value: JSON.parse(raw) };
      const waiter = waiters.shift();
      if (waiter) waiter(line);
      else pending.push(line);
    }
  });

  const nextLine = async (): Promise<OutputLine> => {
    const ready = pending.shift();
    if (ready) return ready;
    return new Promise<OutputLine>((resolve, reject) => {
      const timer = setTimeout(() => {
        const index = waiters.indexOf(onLine);
        if (index >= 0) waiters.splice(index, 1);
        reject(new Error(`timed out waiting for F7 output; stderr=${JSON.stringify(stderr)}`));
      }, 10_000);
      const onLine = (line: OutputLine) => {
        clearTimeout(timer);
        resolve(line);
      };
      waiters.push(onLine);
    });
  };

  const steps: TranscriptStep[] = [];
  const trace: string[] = [];
  const runStep = async (
    name: string,
    input: string | object,
    until: (line: OutputLine) => boolean,
    framing: "lf" | "crlf" = "lf",
  ) => {
    const rawInput = typeof input === "string" ? input : JSON.stringify(input);
    child.stdin.write(rawInput + (framing === "crlf" ? "\r\n" : "\n"));
    let count = 0;
    while (true) {
      const line = await nextLine();
      trace.push(line.raw);
      count++;
      if (until(line)) break;
    }
    steps.push({ name, input: rawInput, framing, expectedLineCount: count });
  };
  const response = (id: string) => (line: OutputLine) => line.value.type === "response" && line.value.id === id;

  try {
    await runStep("empty-line-parse-error", "", (line) => line.value.command === "parse");
    await runStep("invalid-token-parse-error", "not-json", (line) => line.value.command === "parse");
    await runStep("truncated-object-parse-error", "{", (line) => line.value.command === "parse");
    await runStep("initial-state-crlf", { id: "state-1", type: "get_state" }, response("state-1"), "crlf");
    await runStep("initial-messages", { id: "messages-1", type: "get_messages" }, response("messages-1"));
    await runStep("empty-entries", { id: "entries-1", type: "get_entries" }, response("entries-1"));
    await runStep("empty-tree", { id: "tree-1", type: "get_tree" }, response("tree-1"));
    await runStep("empty-fork-messages", { id: "fork-messages-1", type: "get_fork_messages" }, response("fork-messages-1"));
    await runStep("clone-without-leaf", { id: "clone-1", type: "clone" }, response("clone-1"));
    await runStep("available-models", { id: "models-1", type: "get_available_models" }, response("models-1"));
    await runStep("set-model", { id: "model-1", type: "set_model", provider: "faux", modelId: "faux-1" }, response("model-1"));
    await runStep("cycle-model", { id: "model-2", type: "cycle_model" }, response("model-2"));
    await runStep("set-thinking", { id: "thinking-1", type: "set_thinking_level", level: "high" }, response("thinking-1"));
    await runStep("cycle-thinking", { id: "thinking-2", type: "cycle_thinking_level" }, response("thinking-2"));
    await runStep("steering-mode", { id: "queue-1", type: "set_steering_mode", mode: "all" }, response("queue-1"));
    await runStep("follow-up-mode", { id: "queue-2", type: "set_follow_up_mode", mode: "all" }, response("queue-2"));
    await runStep("auto-compaction", { id: "auto-1", type: "set_auto_compaction", enabled: true }, response("auto-1"));
    await runStep("auto-retry", { id: "auto-2", type: "set_auto_retry", enabled: true }, response("auto-2"));
    await runStep("abort-retry", { id: "auto-3", type: "abort_retry" }, response("auto-3"));
    await runStep("cancelled-fork", { id: "fork-1", type: "fork", entryId: "missing" }, response("fork-1"));
    await runStep("cancelled-new-session", { id: "new-1", type: "new_session", parentSession: "parent.jsonl" }, response("new-1"));
    await runStep("cancelled-switch", { id: "switch-1", type: "switch_session", sessionPath: "session.jsonl" }, response("switch-1"));
    await runStep("last-text-before-prompt", { id: "last-1", type: "get_last_assistant_text" }, response("last-1"));
    await runStep("session-stats-before-prompt", { id: "stats-1", type: "get_session_stats" }, response("stats-1"));
    await runStep("commands", { id: "commands-1", type: "get_commands" }, response("commands-1"));
    await runStep("unicode-separator-name", { id: "name-1", type: "set_session_name", name: "rpc\u2028name" }, response("name-1"));
    await runStep("unicode-separator-state", { id: "state-2", type: "get_state" }, response("state-2"));
    await runStep("unknown-command", { id: "unknown-1", type: "does_not_exist" }, response("unknown-1"));
    await runStep(
      "prompt-events",
      { id: "prompt-1", type: "prompt", message: "Say complete." },
      (line) => line.value.type === "agent_settled",
    );
    await runStep("last-text-after-prompt", { id: "last-2", type: "get_last_assistant_text" }, response("last-2"));
    await runStep("messages-after-prompt", { id: "messages-2", type: "get_messages" }, response("messages-2"));
    await runStep("session-stats-after-prompt", { id: "stats-2", type: "get_session_stats" }, response("stats-2"));
    await runStep("entries-after-prompt", { id: "entries-2", type: "get_entries" }, response("entries-2"));
    await runStep(
      "entries-since-session-name",
      { id: "entries-3", type: "get_entries", since: "00000001" },
      response("entries-3"),
    );
    await runStep("tree-after-prompt", { id: "tree-2", type: "get_tree" }, response("tree-2"));
    await runStep("fork-messages-after-prompt", { id: "fork-messages-2", type: "get_fork_messages" }, response("fork-messages-2"));
    await runStep("bash", { id: "bash-1", type: "bash", command: "printf rpc-bash", excludeFromContext: true }, response("bash-1"));
    await runStep(
      "bash-entry",
      { id: "entries-4", type: "get_entries", since: "00000004" },
      response("entries-4"),
    );
    await runStep("abort-bash", { id: "bash-2", type: "abort_bash" }, response("bash-2"));
    await runStep("abort-idle", { id: "abort-1", type: "abort" }, response("abort-1"));

    child.stdin.end();
    const exitCode = await new Promise<number | null>((resolve, reject) => {
      child.once("error", reject);
      child.once("exit", resolve);
    });
    if (exitCode !== 0 || stderr !== "" || stdoutBuffer !== "") {
      throw new Error(`F7 helper exit=${exitCode}, stderr=${JSON.stringify(stderr)}, trailing=${JSON.stringify(stdoutBuffer)}`);
    }
    await rm(FIXTURE_CWD, { recursive: true, force: true });

    const familyDir = path.join(outputRoot, "F7");
    await rm(familyDir, { recursive: true, force: true });
    await mkdir(familyDir, { recursive: true });
    const scenario = {
      schemaVersion: 1,
      fixedNow: FIXED_NOW,
      cwd: FIXTURE_CWD,
      sessionId: "fixture-rpc-session",
      systemPrompt: `Return one short answer.\nCurrent working directory: ${FIXTURE_CWD}`,
      tokenSize: 4,
      responses: [
        {
          role: "assistant",
          content: [{ type: "text", text: "RPC fixture complete." }],
          api: "faux",
          provider: "faux",
          model: "faux-1",
          usage: {
            input: 0,
            output: 0,
            cacheRead: 0,
            cacheWrite: 0,
            totalTokens: 0,
            cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
          },
          stopReason: "stop",
          timestamp: FIXED_NOW,
        },
      ],
      steps,
    };
    const manifest = {
      family: "F7",
      upstreamCommit,
      generator: "conformance/extract/f7-rpc.ts",
      source: "packages/coding-agent/src/modes/rpc/ + packages/coding-agent/docs/rpc.md",
      format: "strict-lf-bidirectional-rpc-jsonl-v1",
      canonicalized: [],
      files: ["scenario.json", "trace.jsonl"],
    };
    await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
    await writeFile(path.join(familyDir, "scenario.json"), `${JSON.stringify(scenario, null, 2)}\n`);
    await writeFile(path.join(familyDir, "trace.jsonl"), `${trace.join("\n")}\n`);
  } catch (error) {
    child.kill("SIGKILL");
    await rm(FIXTURE_CWD, { recursive: true, force: true });
    throw error;
  }
}
