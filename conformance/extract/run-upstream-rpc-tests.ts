import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withOfflineGeneratedCatalog } from "./f3-agent.ts";

const upstreamRoot = process.cwd();
const binaryArgument = process.argv[2];
if (!binaryArgument) {
  throw new Error("pi-go binary path is required");
}
const binary = path.resolve(upstreamRoot, binaryArgument);
const adapterPath = path.join(upstreamRoot, "packages/coding-agent/dist/cli.js");
const vitestPath = path.join(upstreamRoot, "node_modules/.bin/vitest");
const packageRoot = path.join(upstreamRoot, "packages/coding-agent");
const tests = [
  "test/rpc-jsonl.test.ts",
  "test/rpc-client-clone.test.ts",
  "test/rpc-client-process-exit.test.ts",
  "test/rpc-prompt-response-semantics.test.ts",
  "test/suite/regressions/5868-rpc-unknown-command-id.test.ts",
  "test/rpc.test.ts",
];

const mockServer = createServer(async (request, response) => {
  if (request.method !== "POST" || request.url !== "/v1/messages") {
    response.writeHead(404).end();
    return;
  }
  let body = "";
  for await (const chunk of request) body += chunk;
  const unique = body.match(/unique-\d+/)?.[0];
  const text = unique ?? (body.includes("test123") ? "test123" : "ok");
  const events = [
    ["message_start", { type: "message_start", message: { id: "msg_mock", usage: { input_tokens: 1, output_tokens: 0 } } }],
    ["content_block_start", { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } }],
    ["content_block_delta", { type: "content_block_delta", index: 0, delta: { type: "text_delta", text } }],
    ["content_block_stop", { type: "content_block_stop", index: 0 }],
    ["message_delta", { type: "message_delta", delta: { stop_reason: "end_turn" }, usage: { output_tokens: 1 } }],
    ["message_stop", { type: "message_stop" }],
  ] as const;
  response.writeHead(200, { "Content-Type": "text/event-stream" });
  for (const [event, data] of events) response.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
  response.end();
});
await new Promise<void>((resolve, reject) => {
  mockServer.once("error", reject);
  mockServer.listen(0, "127.0.0.1", resolve);
});
const mockAddress = mockServer.address();
if (!mockAddress || typeof mockAddress === "string") throw new Error("RPC mock server did not bind TCP");
const mockBaseURL = `http://127.0.0.1:${mockAddress.port}`;

let previousAdapter: Buffer | undefined;
try {
  previousAdapter = await readFile(adapterPath);
} catch {
  previousAdapter = undefined;
}

const adapter = `#!/usr/bin/env node
import { spawn } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const binary = process.env.PI_GO_RPC_BINARY;
if (!binary) {
  console.error("PI_GO_RPC_BINARY is required");
  process.exit(1);
}
const agentDir = process.env.PI_CODING_AGENT_DIR;
const mockBaseURL = process.env.PI_GO_RPC_ANTHROPIC_BASE_URL;
if (agentDir && mockBaseURL) {
  mkdirSync(agentDir, { recursive: true });
  writeFileSync(join(agentDir, "models.json"), JSON.stringify({ providers: { anthropic: { baseUrl: mockBaseURL } } }));
  writeFileSync(join(agentDir, "settings.json"), JSON.stringify({ compaction: { keepRecentTokens: 1 } }));
}
const child = spawn(binary, process.argv.slice(2), { env: process.env, stdio: "inherit" });
for (const signal of ["SIGTERM", "SIGHUP", "SIGINT"]) {
  process.on(signal, () => child.kill(signal));
}
child.on("error", (error) => {
  console.error(error.message);
  process.exit(1);
});
child.on("exit", (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  else process.exit(code ?? 1);
});
`;

await mkdir(path.dirname(adapterPath), { recursive: true });
await writeFile(adapterPath, adapter, { mode: 0o755 });
try {
  await withOfflineGeneratedCatalog(upstreamRoot, async () => {
    const anthropicCatalog = path.join(upstreamRoot, "packages/ai/src/providers/data/anthropic.json");
    await writeFile(
      anthropicCatalog,
      `${JSON.stringify(
        {
          "claude-sonnet-4-5": {
            id: "claude-sonnet-4-5",
            name: "Claude Sonnet 4.5",
            api: "anthropic-messages",
            provider: "anthropic",
            baseUrl: "https://api.anthropic.com",
            reasoning: true,
            input: ["text", "image"],
            cost: { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 },
            contextWindow: 200000,
            maxTokens: 64000,
          },
        },
        null,
        2,
      )}\n`,
    );
    const rpcClientModule = await import(
      `${pathToFileURL(path.join(upstreamRoot, "packages/coding-agent/src/modes/rpc/rpc-client.ts")).href}?pi-go-adapter`
    );
    const smokeDir = await mkdtemp(path.join(tmpdir(), "pi-go-rpc-smoke-"));
    const smokeClient = new rpcClientModule.RpcClient({
      cliPath: adapterPath,
      cwd: path.join(upstreamRoot, "packages/coding-agent"),
      env: {
        PI_GO_RPC_BINARY: binary,
        PI_CODING_AGENT_DIR: smokeDir,
        PI_GO_RPC_ANTHROPIC_BASE_URL: mockBaseURL,
        ANTHROPIC_API_KEY: "pi-go-rpc-mock",
      },
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      args: ["--no-session"],
    });
    try {
      await smokeClient.start();
      const state = await smokeClient.getState();
      if (
        state.model?.provider !== "anthropic" ||
        state.model.id !== "claude-sonnet-4-5" ||
        state.isStreaming !== false ||
        !state.sessionId
      ) {
        throw new Error(`unexpected pi-go adapter state: ${JSON.stringify(state)}`);
      }
    } finally {
      await smokeClient.stop();
      await rm(smokeDir, { recursive: true, force: true });
    }
    const exitCode = await new Promise<number | null>((resolve, reject) => {
      const child = spawn(vitestPath, ["run", "--config", "vitest.config.ts", ...tests], {
        cwd: packageRoot,
        env: {
          ...process.env,
          PI_GO_RPC_BINARY: binary,
          PI_GO_RPC_ANTHROPIC_BASE_URL: mockBaseURL,
          ANTHROPIC_API_KEY: "pi-go-rpc-mock",
          NO_COLOR: "1",
        },
        stdio: "inherit",
      });
      child.once("error", reject);
      child.once("exit", resolve);
    });
    if (exitCode !== 0) {
      throw new Error(`upstream RPC tests failed with exit code ${exitCode}`);
    }
  });
} finally {
  if (previousAdapter) {
    await writeFile(adapterPath, previousAdapter);
  } else {
    await rm(adapterPath, { force: true });
  }
  await new Promise<void>((resolve, reject) => mockServer.close((error) => error ? reject(error) : resolve()));
}
