import { spawn } from "node:child_process";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withOfflineGeneratedCatalog } from "./f3-agent.ts";

const PROVIDER = "cli-fixture";
const MODEL = "fixture-1";
const PROMPT = "piped contextCLI prompt";
const STREAM = [
  'data: {"id":"chatcmpl_cli_fixture","object":"chat.completion.chunk","created":0,"model":"fixture-1","choices":[{"index":0,"delta":{"role":"assistant","content":"CLI fixture "},"finish_reason":null}]}',
  "",
  'data: {"id":"chatcmpl_cli_fixture","object":"chat.completion.chunk","created":0,"model":"fixture-1","choices":[{"index":0,"delta":{"content":"complete.\\nUnicode: caf\u00e9 \u4e16\u754c."},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":6,"total_tokens":10}}',
  "",
  "data: [DONE]",
  "",
  "",
].join("\n");

interface ProcessResult {
  exitCode: number;
  stdout: string;
  stderr: string;
}

interface FixtureCase {
  name: string;
  route: "print" | "json" | "rpc";
  args: string[];
  stdin: string;
  configuredModel: boolean;
  expectedPrompt?: string;
  expectedRequests: number;
  expected: ProcessResult;
}

function containsString(value: unknown, expected: string): boolean {
  if (value === expected) return true;
  if (Array.isArray(value)) return value.some((entry) => containsString(entry, expected));
  if (value && typeof value === "object") {
    return Object.values(value as Record<string, unknown>).some((entry) => containsString(entry, expected));
  }
  return false;
}

async function runCLI(
  upstreamRoot: string,
  route: FixtureCase["route"],
  args: string[],
  stdin: string,
  configuredModel: boolean,
  baseURL: string,
): Promise<ProcessResult> {
  const root = await mkdtemp(path.join(tmpdir(), "pi-go-f7-cli-"));
  const agentDir = path.join(root, "agent");
  const projectDir = path.join(root, "project");
  const homeDir = path.join(root, "home");
  await Promise.all([
    mkdir(agentDir, { recursive: true }),
    mkdir(projectDir, { recursive: true }),
    mkdir(homeDir, { recursive: true }),
  ]);
  if (configuredModel) {
    const models = {
      providers: {
        [PROVIDER]: {
          baseUrl: `${baseURL}/v1`,
          api: "openai-completions",
          apiKey: "fixture-key",
          models: [
            {
              id: MODEL,
              name: "CLI Fixture",
              reasoning: false,
              input: ["text"],
              cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
              contextWindow: 128000,
              maxTokens: 16384,
            },
          ],
        },
      },
    };
    await writeFile(path.join(agentDir, "models.json"), `${JSON.stringify(models)}\n`, { mode: 0o600 });
  }

  try {
    return await new Promise<ProcessResult>((resolve, reject) => {
      const cli = path.join(upstreamRoot, "packages/coding-agent/src/cli.ts");
      const packageDir = path.join(upstreamRoot, "packages/coding-agent");
      const tsxLoader = pathToFileURL(path.join(upstreamRoot, "node_modules/tsx/dist/loader.mjs")).href;
      const child = spawn(process.execPath, ["--import", tsxLoader, cli, ...args], {
        cwd: projectDir,
        env: {
          PATH: process.env.PATH,
          HOME: homeDir,
          USERPROFILE: homeDir,
          LANG: "C.UTF-8",
          TZ: "UTC",
          TERM: "dumb",
          CI: "1",
          NO_COLOR: "1",
          PI_OFFLINE: "1",
          PI_SKIP_VERSION_CHECK: "1",
          PI_CODING_AGENT_DIR: agentDir,
          PI_PACKAGE_DIR: packageDir,
          TSX_TSCONFIG_PATH: path.join(upstreamRoot, "tsconfig.json"),
        },
        stdio: ["pipe", "pipe", "pipe"],
      });
      const stdout: Buffer[] = [];
      const stderr: Buffer[] = [];
      child.stdout.on("data", (chunk) => stdout.push(Buffer.from(chunk)));
      child.stderr.on("data", (chunk) => stderr.push(Buffer.from(chunk)));
      child.once("error", reject);
      child.once("close", (exitCode, signal) => {
        if (signal || exitCode === null) {
          reject(new Error(`upstream CLI exited via ${signal ?? "unknown signal"}`));
          return;
        }
        let capturedStdout = Buffer.concat(stdout).toString("utf8");
        if (route === "json") {
          capturedStdout = capturedStdout
            .replaceAll(projectDir, "<cwd>")
            .replace(/"timestamp":"[^"]+"/, '"timestamp":"<timestamp>"');
        }
        capturedStdout = capturedStdout.replaceAll(packageDir, "<package>");
        resolve({
          exitCode,
          stdout: capturedStdout,
          stderr: Buffer.concat(stderr).toString("utf8").replaceAll(packageDir, "<package>"),
        });
      });
      child.stdin.end(stdin);
    });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

export async function generateF7CLI(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const requests: string[] = [];
  const serverErrors: string[] = [];
  const server = createServer(async (request, response) => {
    let body = "";
    for await (const chunk of request) body += chunk;
    if (request.method !== "POST" || request.url !== "/v1/chat/completions") {
      serverErrors.push(`${request.method} ${request.url}`);
      response.writeHead(404).end();
      return;
    }
    requests.push(body);
    response.writeHead(200, { "Content-Type": "text/event-stream" });
    response.end(STREAM);
  });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("F7 CLI server did not bind TCP");
  const baseURL = `http://127.0.0.1:${address.port}`;

  try {
    const common = [
      "--no-session",
      "--no-extensions",
      "--no-skills",
      "--no-prompt-templates",
      "--no-context-files",
      "--no-tools",
    ];
    const definitions = [
      {
        name: "scripted-print-text",
        route: "print" as const,
        args: ["-p", ...common, "--provider", PROVIDER, "--model", MODEL, "CLI prompt"],
        stdin: "piped context\n",
        configuredModel: true,
        expectedPrompt: PROMPT,
        expectedRequests: 1,
      },
      {
        name: "no-model-print",
        route: "print" as const,
        args: ["-p", ...common, "--session-id", "f7-cli-print", "unreachable prompt"],
        stdin: "",
        configuredModel: false,
        expectedRequests: 0,
      },
      {
        name: "no-model-json",
        route: "json" as const,
        args: ["--mode", "json", ...common, "--session-id", "f7-cli-json", "unreachable prompt"],
        stdin: "",
        configuredModel: false,
        expectedRequests: 0,
      },
      {
        name: "no-model-rpc",
        route: "rpc" as const,
        args: ["--mode", "rpc", ...common, "--session-id", "f7-cli-rpc"],
        stdin:
          '{"id":"state","type":"get_state"}\n' +
          '{"id":"prompt","type":"prompt","message":"unreachable prompt"}\n',
        configuredModel: false,
        expectedRequests: 0,
      },
    ];

    const cases: FixtureCase[] = [];
    await withOfflineGeneratedCatalog(upstreamRoot, async () => {
      for (const definition of definitions) {
        const requestStart = requests.length;
        const expected = await runCLI(
          upstreamRoot,
          definition.route,
          definition.args,
          definition.stdin,
          definition.configuredModel,
          baseURL,
        );
        const requestCount = requests.length - requestStart;
        if (requestCount !== definition.expectedRequests) {
          throw new Error(
            `${definition.name}: requests=${requestCount}, want ${definition.expectedRequests}, process=${JSON.stringify(expected)}`,
          );
        }
        if (definition.expectedPrompt) {
          const request = JSON.parse(requests[requestStart]) as unknown;
          if (!containsString(request, definition.expectedPrompt)) {
            throw new Error(
              `${definition.name}: request did not contain ${JSON.stringify(definition.expectedPrompt)}: ${requests[requestStart]}`,
            );
          }
        }
        cases.push({ ...definition, expected });
      }
    });
    if (serverErrors.length > 0) throw new Error(`F7 CLI server errors: ${serverErrors.join(", ")}`);

    const familyDir = path.join(outputRoot, "F7-cli");
    await rm(familyDir, { recursive: true, force: true });
    await mkdir(familyDir, { recursive: true });
    const manifest = {
      family: "F7-cli",
      upstreamCommit,
      generator: "conformance/extract/f7-cli.ts",
      source:
        "packages/coding-agent/src/main.ts + src/core/agent-session.ts + src/modes/{print-mode,rpc/rpc-mode}.ts + test/{print-mode,stdout-cleanliness,initial-message}.test.ts",
      format: "black-box-cli-stdout-stderr-v1",
      canonicalized: ["package installation directory", "JSON session-header timestamp", "temporary project cwd"],
      files: ["cases.json"],
    };
    const fixture = {
      schemaVersion: 1,
      model: { provider: PROVIDER, id: MODEL },
      server: { path: "/v1/chat/completions", stream: STREAM },
      cases,
    };
    await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
    await writeFile(path.join(familyDir, "cases.json"), `${JSON.stringify(fixture, null, 2)}\n`);
  } finally {
    await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
  }
}
