import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";
import * as zlib from "node:zlib";

import {
  closeOpenAICodexWebSocketSessions,
  resetOpenAICodexWebSocketDebugStats,
  stream as streamOpenAICodex,
  streamSimple as streamSimpleOpenAICodex,
  type OpenAICodexResponsesOptions,
} from "../../.upstream/packages/ai/src/api/openai-codex-responses.ts";
import type { Provider } from "../../.upstream/packages/ai/src/models.ts";
import type {
  AssistantMessageEvent,
  Context,
  Model,
  SimpleStreamOptions,
  Tool,
} from "../../.upstream/packages/ai/src/types.ts";
import { withUpstreamModelData } from "./upstream-model-data.ts";

interface Definition {
  name: string;
  api: "openai-codex-responses";
  model: Model<"openai-codex-responses">;
  context: Context;
  options: OpenAICodexResponsesOptions | SimpleStreamOptions;
  simple?: boolean;
  sse?: string;
  httpStatus?: number;
  httpBody?: string;
  httpContentType?: string;
}

interface CapturedRequest {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string;
}

const FIXED_NOW = 1_700_000_000_123;
const ACCOUNT_ID = "account-f2-codex";
const ACCESS_TOKEN = codexAccessToken(ACCOUNT_ID);
const SELECTED_HEADERS = [
  "accept",
  "authorization",
  "chatgpt-account-id",
  "content-type",
  "openai-beta",
  "originator",
  "session-id",
  "x-client-request-id",
  "x-fixture",
  "x-model-header",
] as const;

const zeroCost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
const streamCost = { input: 1_000_000, output: 2_000_000, cacheRead: 3_000_000, cacheWrite: 4_000_000 };
const longSessionId = `${"s".repeat(64)}-tail`;

const echoTool = {
  name: "echo",
  description: "Echo structured text",
  parameters: {
    type: "object",
    properties: {
      text: { type: "string" },
      mode: { type: "string", enum: ["plain", "loud"] },
    },
    required: ["text"],
    additionalProperties: false,
  },
} as Tool;

function model(overrides: Partial<Model<"openai-codex-responses">> = {}): Model<"openai-codex-responses"> {
  return {
    id: "gpt-codex-fixture",
    name: "Codex Fixture Model",
    api: "openai-codex-responses",
    provider: "openai-codex",
    baseUrl: "https://codex.fixture.invalid/backend-api",
    reasoning: true,
    thinkingLevelMap: { off: "none", high: "xhigh" },
    input: ["text", "image"],
    cost: zeroCost,
    contextWindow: 128_000,
    maxTokens: 8_192,
    ...overrides,
  };
}

const requestDefinitions: Definition[] = [
  {
    name: "codex-defaults-cache-and-forced-auth",
    api: "openai-codex-responses",
    model: model({ headers: { "x-model-header": "codex-default" } }),
    context: {
      systemPrompt: "You are concise.",
      messages: [{ role: "user", content: "hello <fixture>", timestamp: FIXED_NOW }],
    },
    options: {
      apiKey: ACCESS_TOKEN,
      transport: "sse",
      temperature: 0,
      sessionId: longSessionId,
      reasoningEffort: "none",
      headers: { authorization: "Bearer wrong", originator: "wrong", "x-fixture": "codex-default" },
    },
  },
  {
    name: "codex-tools-images-reasoning",
    api: "openai-codex-responses",
    model: model({ baseUrl: "https://codex.fixture.invalid/backend-api/codex" }),
    context: {
      systemPrompt: "Inspect and use tools.",
      messages: [
        {
          role: "user",
          content: [
            { type: "text", text: "inspect" },
            { type: "image", data: "AAEC", mimeType: "image/png" },
          ],
          timestamp: FIXED_NOW,
        },
      ],
      tools: [echoTool],
    },
    options: {
      apiKey: ACCESS_TOKEN,
      transport: "sse",
      reasoningEffort: "high",
      reasoningSummary: "concise",
      serviceTier: "priority",
      textVerbosity: "high",
      toolChoice: "required",
    },
  },
  {
    name: "codex-fallback-instructions-normalized-url",
    api: "openai-codex-responses",
    model: model({ baseUrl: "https://codex.fixture.invalid/backend-api/codex/responses///" }),
    context: { messages: [] },
    options: { apiKey: ACCESS_TOKEN, transport: "sse" },
  },
  {
    name: "codex-simple-nonreasoning-omits-reasoning",
    api: "openai-codex-responses",
    model: model({ reasoning: false }),
    context: { messages: [{ role: "user", content: "simple", timestamp: FIXED_NOW }] },
    options: { apiKey: ACCESS_TOKEN, transport: "sse", reasoning: "high" },
    simple: true,
  },
  {
    // CX-M4: a null thinkingLevelMap entry coalesces back to the requested
    // effort (?? in openai-codex-responses.ts:523), so reasoning is still sent.
    name: "codex-null-thinking-level-map-keeps-reasoning",
    api: "openai-codex-responses",
    model: model({ thinkingLevelMap: { off: "none", high: null } }),
    context: { messages: [{ role: "user", content: "think hard", timestamp: FIXED_NOW }] },
    options: { apiKey: ACCESS_TOKEN, transport: "sse", reasoningEffort: "high" },
  },
];

const richSSE = encodeSSE([
  { type: "response.created", response: { id: "resp_codex_rich" } },
  { type: "response.output_item.added", output_index: 0, item: { type: "reasoning", id: "rs_codex", summary: [] } },
  { type: "response.reasoning_summary_text.delta", output_index: 0, delta: "plan" },
  { type: "response.output_item.done", output_index: 0, item: { type: "reasoning", id: "rs_codex", summary: [{ type: "summary_text", text: "plan" }], encrypted_content: "encrypted-codex" } },
  { type: "response.output_item.added", output_index: 1, item: { type: "message", id: "msg_codex", role: "assistant", content: [], status: "in_progress" } },
  { type: "response.output_text.delta", output_index: 1, delta: "hello" },
  { type: "response.output_item.done", output_index: 1, item: { type: "message", id: "msg_codex", role: "assistant", content: [{ type: "output_text", text: "hello", annotations: [] }], status: "completed", phase: "final_answer" } },
  { type: "response.done", response: { id: "resp_codex_rich", status: "completed", service_tier: "default", output: [{ type: "reasoning", id: "rs_codex", summary: [{ type: "summary_text", text: "plan" }], encrypted_content: "encrypted-codex" }], usage: { input_tokens: 20, output_tokens: 7, total_tokens: 27, input_tokens_details: { cached_tokens: 2, cache_write_tokens: 3 }, output_tokens_details: { reasoning_tokens: 4 } } } },
  { type: "response.output_text.delta", output_index: 1, delta: "must-not-be-read" },
]);

const streamDefinitions: Definition[] = [
  {
    name: "codex-rich-done-priority",
    api: "openai-codex-responses",
    model: model({ cost: streamCost }),
    context: { messages: [{ role: "user", content: "stream", timestamp: FIXED_NOW }] },
    options: { apiKey: ACCESS_TOKEN, transport: "sse", serviceTier: "priority" },
    sse: richSSE,
  },
  {
    name: "codex-incomplete-length",
    api: "openai-codex-responses",
    model: model({ cost: streamCost }),
    context: { messages: [{ role: "user", content: "length", timestamp: FIXED_NOW }] },
    options: { apiKey: ACCESS_TOKEN, transport: "sse" },
    sse: encodeSSE([{ type: "response.incomplete", response: { id: "resp_codex_length", status: "incomplete", output: [], usage: { input_tokens: 8, output_tokens: 3, total_tokens: 11 } } }]),
  },
  {
    name: "codex-error-event",
    api: "openai-codex-responses",
    model: model(),
    context: { messages: [] },
    options: { apiKey: ACCESS_TOKEN, transport: "sse" },
    sse: encodeSSE([{ type: "error", error: { code: "denied", message: "fixture denied" } }]),
  },
  {
    name: "codex-response-failed",
    api: "openai-codex-responses",
    model: model(),
    context: { messages: [] },
    options: { apiKey: ACCESS_TOKEN, transport: "sse" },
    sse: encodeSSE([{ type: "response.failed", response: { status: "failed", error: { code: "failed", message: "fixture response failed" } } }]),
  },
  {
    name: "codex-http-429-friendly",
    api: "openai-codex-responses",
    model: model(),
    context: { messages: [] },
    options: { apiKey: ACCESS_TOKEN, transport: "sse" },
    httpStatus: 429,
    httpBody: '{"error":{"code":"usage_limit_reached","message":"raw limit","plan_type":"Plus","resets_at":1700000300}}',
    httpContentType: "application/json",
  },
  {
    name: "codex-negative-http-timeout",
    api: "openai-codex-responses",
    model: model(),
    context: { messages: [] },
    options: { apiKey: ACCESS_TOKEN, transport: "sse", timeoutMs: -1 },
  },
  {
    name: "codex-negative-websocket-timeout",
    api: "openai-codex-responses",
    model: model(),
    context: { messages: [] },
    options: { apiKey: ACCESS_TOKEN, transport: "sse", websocketConnectTimeoutMs: -1 },
  },
];

export async function generateF2Codex(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  const originalNow = Date.now;
  const originalFetch = globalThis.fetch;
  Date.now = () => FIXED_NOW;
  try {
    const requests = [];
    for (const definition of requestDefinitions) {
      const result = await runUpstream(definition, { status: 200, body: minimalTerminalSSE(), contentType: "text/event-stream" });
      if (!result.request) throw new Error(`${definition.name}: no request captured`);
      requests.push({ ...definition, expected: result.request });
    }
    const streams = [];
    for (const definition of streamDefinitions) {
      const response = definition.httpStatus !== undefined
        ? { status: definition.httpStatus, body: definition.httpBody ?? "", contentType: definition.httpContentType ?? "text/plain" }
        : { status: 200, body: definition.sse ?? "", contentType: "text/event-stream" };
      const result = await runUpstream(definition, response);
      streams.push({ ...definition, expectedEvents: result.events });
    }
    const providers = await extractSubscriptionProviders(upstreamRoot);
    const websocket = await extractCodexWebSocketTrace();
    const familyDir = path.join(outputRoot, "F2");
    await mkdir(familyDir, { recursive: true });
    await writeFile(path.join(familyDir, "codex-requests.json"), `${JSON.stringify({ cases: requests }, null, 2)}\n`);
    await writeFile(path.join(familyDir, "codex-streams.json"), `${JSON.stringify({ cases: streams }, null, 2)}\n`);
    await writeFile(path.join(familyDir, "codex-websocket.json"), `${JSON.stringify(websocket, null, 2)}\n`);
    await writeFile(path.join(familyDir, "subscription-providers.json"), `${JSON.stringify({ providers }, null, 2)}\n`);

    const manifestPath = path.join(familyDir, "manifest.json");
    const manifest = JSON.parse(await readFile(manifestPath, "utf8")) as { source: string; generator: string; files: string[]; upstreamCommit: string };
    manifest.upstreamCommit = upstreamCommit;
    manifest.generator = "conformance/extract/f2-openai.ts + conformance/extract/f2-codex.ts";
    manifest.source += " + packages/ai/src/api/openai-codex-responses.ts + packages/ai/src/auth/oauth/{openai-codex,github-copilot,xai,device-code}.ts + packages/ai/src/providers/{openai-codex,github-copilot,xai}.ts";
    manifest.files.push("codex-requests.json", "codex-streams.json", "codex-websocket.json", "subscription-providers.json");
    await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  } finally {
    Date.now = originalNow;
    globalThis.fetch = originalFetch;
  }
}

async function extractCodexWebSocketTrace() {
  const serverEvents = [
    { type: "response.created", response: { id: "ws-fixture" } },
    { type: "response.done", response: { id: "ws-fixture", status: "completed", output: [] } },
  ];
  const definition: Definition = {
    name: "codex-auto-websocket-success",
    api: "openai-codex-responses",
    model: model({ headers: { "x-model-header": "websocket" } }),
    context: { messages: [{ role: "user", content: "over websocket", timestamp: FIXED_NOW }] },
    options: {
      apiKey: ACCESS_TOKEN,
      transport: "auto",
      sessionId: "codex-websocket-fixture",
      headers: { "x-fixture": "websocket" },
    },
  };
  const requests: Array<{ url: string; headers: Record<string, string>; body: string }> = [];
  let connections = 0;
  const originalWebSocket = globalThis.WebSocket;

  class FixtureWebSocket extends EventTarget {
    static readonly OPEN = 1;
    static readonly CLOSED = 3;
    readyState = 0;
    readonly url: string;
    private readonly headers: Record<string, string>;

    constructor(url: string | URL, protocols?: string | string[] | { headers?: Record<string, string> }) {
      super();
      connections++;
      this.url = url.toString();
      this.headers = typeof protocols === "object" && !Array.isArray(protocols) ? (protocols.headers ?? {}) : {};
      queueMicrotask(() => {
        this.readyState = FixtureWebSocket.OPEN;
        this.dispatchEvent(new Event("open"));
      });
    }

    send(data: string) {
      requests.push({ url: this.url, headers: selectWebSocketHeaders(this.headers), body: String(data) });
      queueMicrotask(() => {
        for (const event of serverEvents) {
          this.dispatchEvent(new MessageEvent("message", { data: JSON.stringify(event) }));
        }
      });
    }

    close(code = 1000, reason = "") {
      this.readyState = FixtureWebSocket.CLOSED;
      const event = Object.assign(new Event("close"), { code, reason, wasClean: true });
      this.dispatchEvent(event);
    }
  }

  closeOpenAICodexWebSocketSessions();
  resetOpenAICodexWebSocketDebugStats();
  globalThis.WebSocket = FixtureWebSocket as unknown as typeof WebSocket;
  globalThis.fetch = async () => {
    throw new Error("Codex websocket fixture unexpectedly used SSE");
  };
  try {
    const events: AssistantMessageEvent[] = [];
    for await (const event of streamOpenAICodex(
      definition.model,
      definition.context,
      definition.options as OpenAICodexResponsesOptions,
    )) {
      events.push(JSON.parse(JSON.stringify(event)) as AssistantMessageEvent);
    }
    return {
      cases: [{ ...definition, serverEvents, expectedConnections: connections, expectedRequests: requests, expectedEvents: events }],
    };
  } finally {
    closeOpenAICodexWebSocketSessions();
    resetOpenAICodexWebSocketDebugStats();
    globalThis.WebSocket = originalWebSocket;
  }
}

function selectWebSocketHeaders(headers: Record<string, string>): Record<string, string> {
  const selected: Record<string, string> = {};
  for (const name of [
    "authorization",
    "chatgpt-account-id",
    "openai-beta",
    "originator",
    "session-id",
    "x-client-request-id",
    "x-fixture",
    "x-model-header",
  ]) {
    const entry = Object.entries(headers).find(([key]) => key.toLowerCase() === name);
    if (entry) selected[name] = entry[1];
  }
  return selected;
}

async function runUpstream(
  definition: Definition,
  fixtureResponse: { status: number; body: string; contentType: string },
): Promise<{ request?: CapturedRequest; events: AssistantMessageEvent[] }> {
  let captured: CapturedRequest | undefined;
  globalThis.fetch = async (input, init) => {
    captured = await captureRequest(input, init);
    return new Response(fixtureResponse.body, {
      status: fixtureResponse.status,
      headers: { "content-type": fixtureResponse.contentType },
    });
  };
  const events: AssistantMessageEvent[] = [];
  const stream = definition.simple
    ? streamSimpleOpenAICodex(definition.model, definition.context, definition.options as SimpleStreamOptions)
    : streamOpenAICodex(definition.model, definition.context, definition.options as OpenAICodexResponsesOptions);
  for await (const event of stream) {
    events.push(JSON.parse(JSON.stringify(event)) as AssistantMessageEvent);
  }
  return { request: captured, events };
}

async function captureRequest(input: RequestInfo | URL, init?: RequestInit): Promise<CapturedRequest> {
  const headers = new Headers(init?.headers);
  const selected: Record<string, string> = {};
  for (const name of SELECTED_HEADERS) {
    const value = headers.get(name);
    if (value !== null) selected[name] = value;
  }
  let bytes = await requestBodyBytes(init?.body);
  if (headers.get("content-encoding") === "zstd") {
    const decompress = (zlib as unknown as { zstdDecompressSync?: (input: Uint8Array) => Uint8Array }).zstdDecompressSync;
    if (!decompress) throw new Error("upstream emitted zstd but this Node runtime cannot decompress it");
    bytes = decompress(bytes);
  }
  return {
    method: init?.method ?? "GET",
    url: typeof input === "string" ? input : input.toString(),
    headers: selected,
    body: new TextDecoder().decode(bytes),
  };
}

async function requestBodyBytes(body: BodyInit | null | undefined): Promise<Uint8Array> {
  if (body === undefined || body === null) return new Uint8Array();
  if (typeof body === "string") return new TextEncoder().encode(body);
  if (body instanceof Uint8Array) return body;
  if (body instanceof ArrayBuffer) return new Uint8Array(body);
  return new Uint8Array(await new Response(body).arrayBuffer());
}

function providerFixture(provider: Provider, apis: string[]) {
  return {
    id: provider.id,
    name: provider.name,
    baseUrl: provider.baseUrl,
    apis,
    auth: {
      apiKeyName: provider.auth.apiKey?.name,
      oauthName: provider.auth.oauth?.name,
    },
  };
}

async function extractSubscriptionProviders(upstreamRoot: string) {
  return withUpstreamModelData(upstreamRoot, async () => {
    const providersDir = path.join(upstreamRoot, "packages/ai/src/providers");
    const definitions = [
      { file: "openai-codex", factory: "openaiCodexProvider" },
      { file: "github-copilot", factory: "githubCopilotProvider" },
      { file: "xai", factory: "xaiProvider" },
    ] as const;
    const result = [];
    for (const definition of definitions) {
      const module = (await import(pathToFileURL(path.join(providersDir, `${definition.file}.ts`)).href)) as Record<string, () => Provider>;
      const provider = module[definition.factory]?.();
      if (!provider) throw new Error(`${definition.factory}() was not exported`);
      const modelSource = await readFile(path.join(providersDir, `${definition.file}.models.ts`), "utf8");
      const apis = Array.from(modelSource.matchAll(/Model<"([^"]+)">/g), (match) => match[1]);
      result.push(providerFixture(provider, Array.from(new Set(apis)).sort()));
    }
    return result;
  });
}

function codexAccessToken(accountId: string): string {
  const payload = Buffer.from(JSON.stringify({ "https://api.openai.com/auth": { chatgpt_account_id: accountId } })).toString("base64url");
  return `header.${payload}.signature`;
}

function encodeSSE(events: unknown[]): string {
  return events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join("");
}

function minimalTerminalSSE(): string {
  return encodeSSE([{ type: "response.done", response: { id: "resp_codex_request", status: "completed", output: [] } }]);
}
