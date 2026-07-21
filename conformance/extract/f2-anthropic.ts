import { cp, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import {
  stream as streamAnthropic,
  streamSimple as streamSimpleAnthropic,
  type AnthropicOptions,
} from "../../.upstream/packages/ai/src/api/anthropic-messages.ts";
import { cloudflareAIGatewayAuth } from "../../.upstream/packages/ai/src/providers/cloudflare-auth.ts";
import { cloudflareStreams } from "../../.upstream/packages/ai/src/providers/cloudflare-stream.ts";
import type {
  AssistantMessage,
  AssistantMessageEvent,
  Context,
  Model,
  ProviderStreams,
  SimpleStreamOptions,
  Tool,
} from "../../.upstream/packages/ai/src/types.ts";

const FIXED_NOW = 1_700_000_000_123;

interface AnthropicDefinition {
  name: string;
  api: "anthropic-messages";
  simple?: boolean;
  payloadHook?: "disable-stream";
  model: Model<"anthropic-messages">;
  context: Context;
  options: AnthropicOptions | SimpleStreamOptions;
}

interface AnthropicStreamDefinition extends AnthropicDefinition {
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

interface AnthropicProviderFixture {
  id: string;
  name: string;
  baseUrl?: string;
  apis: string[];
  auth: {
    kind: "api_key";
    name: string;
    oauthName?: string;
    env: string[];
    resolved: { apiKey?: string };
    source?: string;
  };
}

const selectedHeaders = [
  "accept",
  "anthropic-beta",
  "anthropic-dangerous-direct-browser-access",
  "anthropic-version",
  "authorization",
  "cf-aig-authorization",
  "content-type",
  "x-api-key",
  "x-app",
  "x-fixture",
  "x-model-header",
  "x-session-affinity",
] as const;

const zeroCost = { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 };

const echoTool: Tool = {
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
};

const lateTool: Tool = {
  name: "late_tool",
  description: "A deferred tool",
  parameters: {
    type: "object",
    properties: { value: { type: "boolean" } },
    required: ["value"],
    additionalProperties: false,
  },
};

function anthropicModel(
  overrides: Partial<Model<"anthropic-messages">> = {},
): Model<"anthropic-messages"> {
  return {
    id: "claude-opus-4-6",
    name: "Anthropic Fixture Model",
    api: "anthropic-messages",
    provider: "anthropic",
    baseUrl: "https://api.anthropic.com",
    reasoning: true,
    input: ["text", "image"],
    cost: zeroCost,
    contextWindow: 200_000,
    maxTokens: 32_000,
    ...overrides,
  };
}

function assistant(
  model: Model<"anthropic-messages">,
  content: AssistantMessage["content"],
  stopReason: AssistantMessage["stopReason"] = "toolUse",
): AssistantMessage {
  return {
    role: "assistant",
    content,
    api: model.api,
    provider: model.provider,
    model: model.id,
    usage: {
      input: 0,
      output: 0,
      cacheRead: 0,
      cacheWrite: 0,
      totalTokens: 0,
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
    },
    stopReason,
    timestamp: FIXED_NOW - 2,
  };
}

const textModel = anthropicModel({
  reasoning: false,
  headers: { "x-model-header": "anthropic-text" },
  compat: { sendSessionAffinityHeaders: true },
});

const replayModel = anthropicModel({
  compat: {
    supportsToolReferences: true,
    supportsLongCacheRetention: true,
  },
});

const oauthModel = anthropicModel({
  id: "claude-opus-4-8",
  compat: {
    forceAdaptiveThinking: true,
    supportsTemperature: false,
  },
  thinkingLevelMap: { off: "none", high: "high", xhigh: "xhigh" },
});

export const fireworksCompatModel = anthropicModel({
  id: "accounts/fireworks/models/minimax-m3",
  name: "MiniMax-M3",
  provider: "fireworks",
  baseUrl: "https://api.fireworks.ai/inference",
  input: ["text"],
  cost: { input: 0.3, output: 1.2, cacheRead: 0.06, cacheWrite: 0 },
  contextWindow: 512_000,
  maxTokens: 512_000,
  compat: {
    sendSessionAffinityHeaders: true,
    supportsEagerToolInputStreaming: false,
    supportsCacheControlOnTools: false,
    supportsLongCacheRetention: false,
  },
});

const requestDefinitions: AnthropicDefinition[] = [
  {
    name: "anthropic-fireworks-session-cache-compat",
    api: "anthropic-messages",
    model: fireworksCompatModel,
    context: {
      systemPrompt: "Use Fireworks compatibility.",
      messages: [{ role: "user", content: "echo once", timestamp: FIXED_NOW }],
      tools: [echoTool],
    },
    options: {
      apiKey: "fixture-fireworks-key",
      cacheRetention: "long",
      sessionId: "fireworks-session",
    },
  },
  {
    name: "anthropic-text-default-cache",
    api: "anthropic-messages",
    model: textModel,
    context: {
      systemPrompt: "You are concise.",
      messages: [{ role: "user", content: "hello <fixture>", timestamp: FIXED_NOW - 1 }],
      tools: [echoTool],
    },
    options: {
      apiKey: "fixture-anthropic-key",
      temperature: 0,
      maxTokens: 777,
      cacheRetention: "short",
      sessionId: "anthropic-session",
      headers: { "x-fixture": "anthropic-text" },
    },
  },
  {
    name: "anthropic-rich-replay-long-cache",
    api: "anthropic-messages",
    model: replayModel,
    context: {
      systemPrompt: "Use tools and preserve thinking.",
      messages: [
        {
          role: "user",
          content: [
            { type: "text", text: "inspect image" },
            { type: "image", data: "aW1hZ2U=", mimeType: "image/png" },
          ],
          timestamp: FIXED_NOW - 4,
        },
        assistant(replayModel, [
          { type: "thinking", thinking: "signed reasoning", thinkingSignature: "sig_fixture" },
          {
            type: "toolCall",
            id: "toolu_fixture",
            name: "echo",
            arguments: { text: "first", mode: "plain" },
          },
        ]),
        {
          role: "toolResult",
          toolCallId: "toolu_fixture",
          toolName: "echo",
          content: [{ type: "text", text: "work completed" }],
          addedToolNames: ["late_tool"],
          isError: false,
          timestamp: FIXED_NOW - 1,
        },
        { role: "user", content: "continue", timestamp: FIXED_NOW },
      ],
      tools: [echoTool, lateTool],
    },
    options: {
      apiKey: "fixture-anthropic-key",
      maxTokens: 4096,
      cacheRetention: "long",
      thinkingEnabled: true,
      thinkingBudgetTokens: 2048,
      thinkingDisplay: "omitted",
      metadata: { user_id: "fixture-user", ignored: 42 },
      toolChoice: { type: "tool", name: "echo" },
    },
  },
  {
    name: "anthropic-oauth-adaptive-no-cache",
    api: "anthropic-messages",
    model: oauthModel,
    context: {
      systemPrompt: "Read carefully.",
      messages: [{ role: "user", content: "read it", timestamp: FIXED_NOW }],
      tools: [{ ...echoTool, name: "read" }],
    },
    options: {
      apiKey: "sk-ant-oat-fixture",
      cacheRetention: "none",
      temperature: 0,
      thinkingEnabled: true,
      effort: "xhigh",
      toolChoice: "auto",
    },
  },
  {
    name: "anthropic-long-cache-compat-disabled",
    api: "anthropic-messages",
    model: anthropicModel({
      reasoning: false,
      compat: {
        supportsLongCacheRetention: false,
        supportsCacheControlOnTools: false,
        supportsEagerToolInputStreaming: false,
      },
    }),
    context: {
      systemPrompt: "Proxy system.",
      messages: [
        {
          role: "user",
          content: [{ type: "image", data: "cGl4ZWw=", mimeType: "image/webp" }],
          timestamp: FIXED_NOW,
        },
      ],
      tools: [echoTool],
    },
    options: {
      apiKey: "fixture-anthropic-key",
      cacheRetention: "long",
    },
  },
  {
    name: "anthropic-empty-signature-compat",
    api: "anthropic-messages",
    model: anthropicModel({
      provider: "compatible-anthropic",
      compat: { allowEmptySignature: true, supportsToolReferences: false },
    }),
    context: {
      messages: [
        { role: "user", content: "first", timestamp: FIXED_NOW - 2 },
        assistant(
          anthropicModel({ provider: "compatible-anthropic" }),
          [{ type: "thinking", thinking: "unsigned reasoning", thinkingSignature: " " }],
          "stop",
        ),
        { role: "user", content: "second", timestamp: FIXED_NOW },
      ],
    },
    options: {
      apiKey: "fixture-anthropic-key",
      cacheRetention: "none",
    },
  },
  {
    name: "anthropic-simple-budget-thinking",
    api: "anthropic-messages",
    simple: true,
    model: anthropicModel({ maxTokens: 12_000 }),
    context: { messages: [{ role: "user", content: "reason", timestamp: FIXED_NOW }] },
    options: {
      apiKey: "fixture-anthropic-key",
      maxTokens: 2_000,
      cacheRetention: "none",
      reasoning: "medium",
      thinkingBudgets: { medium: 3_000 },
    },
  },
  {
    name: "anthropic-simple-adaptive-thinking",
    api: "anthropic-messages",
    simple: true,
    model: oauthModel,
    context: { messages: [{ role: "user", content: "reason adaptively", timestamp: FIXED_NOW }] },
    options: {
      apiKey: "fixture-anthropic-key",
      cacheRetention: "none",
      reasoning: "xhigh",
      temperature: 0,
    },
  },
  {
    name: "anthropic-simple-reasoning-off-forces-stream",
    api: "anthropic-messages",
    simple: true,
    payloadHook: "disable-stream",
    model: anthropicModel({ thinkingLevelMap: { off: "none" } }),
    context: { messages: [{ role: "user", content: "answer directly", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-anthropic-key", cacheRetention: "none" },
  },
];

function encodeAnthropicSSE(events: Array<{ event: string; data: unknown }>): string {
  return events
    .map(({ event, data }) => `event: ${event}\ndata: ${typeof data === "string" ? data : JSON.stringify(data)}\n\n`)
    .join("");
}

const richSSE = encodeAnthropicSSE([
  {
    event: "message_start",
    data: {
      type: "message_start",
      message: {
        id: "msg_anthropic_fixture",
        usage: {
          input_tokens: 10,
          output_tokens: 0,
          cache_read_input_tokens: 3,
          cache_creation_input_tokens: 5,
          cache_creation: { ephemeral_5m_input_tokens: 3, ephemeral_1h_input_tokens: 2 },
        },
      },
    },
  },
  {
    event: "content_block_start",
    data: { type: "content_block_start", index: 0, content_block: { type: "thinking", thinking: "" } },
  },
  {
    event: "content_block_delta",
    data: { type: "content_block_delta", index: 0, delta: { type: "thinking_delta", thinking: "reason" } },
  },
  {
    event: "content_block_delta",
    data: { type: "content_block_delta", index: 0, delta: { type: "signature_delta", signature: "sig_stream" } },
  },
  { event: "content_block_stop", data: { type: "content_block_stop", index: 0 } },
  {
    event: "content_block_start",
    data: { type: "content_block_start", index: 1, content_block: { type: "text", text: "" } },
  },
  {
    event: "content_block_delta",
    data: { type: "content_block_delta", index: 1, delta: { type: "text_delta", text: "working" } },
  },
  { event: "content_block_stop", data: { type: "content_block_stop", index: 1 } },
  {
    event: "content_block_start",
    data: {
      type: "content_block_start",
      index: 2,
      content_block: { type: "tool_use", id: "toolu_stream", name: "echo", input: {} },
    },
  },
  {
    event: "content_block_delta",
    data: {
      type: "content_block_delta",
      index: 2,
      delta: { type: "input_json_delta", partial_json: '{"text":"hello",' },
    },
  },
  {
    event: "content_block_delta",
    data: {
      type: "content_block_delta",
      index: 2,
      delta: { type: "input_json_delta", partial_json: '"mode":"plain"}' },
    },
  },
  { event: "content_block_stop", data: { type: "content_block_stop", index: 2 } },
  {
    event: "message_delta",
    data: {
      type: "message_delta",
      delta: { stop_reason: "tool_use" },
      usage: {
        input_tokens: 10,
        output_tokens: 7,
        cache_read_input_tokens: 3,
        cache_creation_input_tokens: 5,
        output_tokens_details: { thinking_tokens: 2 },
      },
    },
  },
  { event: "message_stop", data: { type: "message_stop" } },
]);

const malformedToolJsonDelta = String.raw`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"A\H\",\"text\":\"col1	col2\"}"}}`;
const malformedToolSSE = encodeAnthropicSSE([
  {
    event: "message_start",
    data: {
      type: "message_start",
      message: {
        id: "msg_malformed",
        usage: {
          input_tokens: 12,
          output_tokens: 0,
          cache_read_input_tokens: 0,
          cache_creation_input_tokens: 0,
        },
      },
    },
  },
  {
    event: "content_block_start",
    data: {
      type: "content_block_start",
      index: 0,
      content_block: { type: "tool_use", id: "toolu_malformed", name: "echo", input: {} },
    },
  },
  { event: "content_block_delta", data: malformedToolJsonDelta },
  { event: "content_block_stop", data: { type: "content_block_stop", index: 0 } },
  {
    event: "message_delta",
    data: {
      type: "message_delta",
      delta: { stop_reason: "tool_use" },
      usage: {
        input_tokens: 12,
        output_tokens: 5,
        cache_read_input_tokens: 0,
        cache_creation_input_tokens: 0,
      },
    },
  },
  { event: "message_stop", data: { type: "message_stop" } },
]);

const refusalExplanation = "fixture refusal explanation";
const streamDefinitions: AnthropicStreamDefinition[] = [
  {
    name: "anthropic-thinking-text-tool-use",
    api: "anthropic-messages",
    model: replayModel,
    context: {
      messages: [{ role: "user", content: "use echo", timestamp: FIXED_NOW }],
      tools: [echoTool],
    },
    options: { apiKey: "fixture-anthropic-key", cacheRetention: "none" },
    sse: richSSE,
  },
  {
    name: "anthropic-malformed-tool-json",
    api: "anthropic-messages",
    model: replayModel,
    context: {
      messages: [{ role: "user", content: "use malformed echo", timestamp: FIXED_NOW }],
      tools: [echoTool],
    },
    options: { apiKey: "fixture-anthropic-key", cacheRetention: "none" },
    sse: malformedToolSSE,
  },
  {
    name: "anthropic-malformed-sse-event-raw",
    api: "anthropic-messages",
    model: textModel,
    context: { messages: [{ role: "user", content: "malformed event", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-anthropic-key", cacheRetention: "none" },
    sse: ": heartbeat\nevent: message_delta\ndata: \n\n",
  },
  {
    name: "anthropic-max-tokens",
    api: "anthropic-messages",
    model: textModel,
    context: { messages: [{ role: "user", content: "fill the limit", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-anthropic-key", cacheRetention: "none" },
    sse: encodeAnthropicSSE([
      {
        event: "message_start",
        data: {
          type: "message_start",
          message: {
            id: "msg_max_tokens",
            usage: {
              input_tokens: 3,
              output_tokens: 0,
              cache_read_input_tokens: 0,
              cache_creation_input_tokens: 0,
            },
          },
        },
      },
      { event: "content_block_start", data: { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } } },
      { event: "content_block_delta", data: { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "limited" } } },
      { event: "content_block_stop", data: { type: "content_block_stop", index: 0 } },
      {
        event: "message_delta",
        data: { type: "message_delta", delta: { stop_reason: "max_tokens" }, usage: { output_tokens: 1 } },
      },
      { event: "message_stop", data: { type: "message_stop" } },
    ]),
  },
  {
    name: "anthropic-redacted-refusal",
    api: "anthropic-messages",
    model: oauthModel,
    context: { messages: [{ role: "user", content: "blocked", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-anthropic-key", cacheRetention: "none" },
    sse: encodeAnthropicSSE([
      {
        event: "message_start",
        data: {
          type: "message_start",
          message: {
            id: "msg_refusal",
            usage: {
              input_tokens: 4,
              output_tokens: 0,
              cache_read_input_tokens: 0,
              cache_creation_input_tokens: 0,
            },
          },
        },
      },
      {
        event: "content_block_start",
        data: {
          type: "content_block_start",
          index: 0,
          content_block: { type: "redacted_thinking", data: "redacted_fixture" },
        },
      },
      { event: "content_block_stop", data: { type: "content_block_stop", index: 0 } },
      {
        event: "message_delta",
        data: {
          type: "message_delta",
          delta: {
            stop_reason: "refusal",
            stop_details: { type: "refusal", category: "fixture", explanation: refusalExplanation },
          },
          usage: { output_tokens: 0 },
        },
      },
      { event: "message_stop", data: { type: "message_stop" } },
    ]),
  },
  {
    name: "anthropic-missing-delta-usage-and-unknown-events",
    api: "anthropic-messages",
    model: textModel,
    context: { messages: [{ role: "user", content: "hello", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-anthropic-key", cacheRetention: "none" },
    sse: encodeAnthropicSSE([
      {
        event: "message_start",
        data: {
          type: "message_start",
          message: {
            id: "msg_text",
            usage: {
              input_tokens: 12,
              output_tokens: 0,
              cache_read_input_tokens: 0,
              cache_creation_input_tokens: 0,
            },
          },
        },
      },
      {
        event: "content_block_start",
        data: { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } },
      },
      {
        event: "content_block_delta",
        data: { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "Hello" } },
      },
      { event: "content_block_stop", data: { type: "content_block_stop", index: 0 } },
      { event: "message_delta", data: { type: "message_delta", delta: { stop_reason: "end_turn" } } },
      { event: "message_stop", data: { type: "message_stop" } },
      { event: "done", data: "[DONE]" },
      { event: "proxy.stats", data: "not json" },
    ]),
  },
  {
    name: "anthropic-truncated-stream",
    api: "anthropic-messages",
    model: textModel,
    context: { messages: [{ role: "user", content: "truncate", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-anthropic-key", cacheRetention: "none" },
    sse: encodeAnthropicSSE([
      {
        event: "message_start",
        data: {
          type: "message_start",
          message: {
            id: "msg_truncated",
            usage: {
              input_tokens: 1,
              output_tokens: 0,
              cache_read_input_tokens: 0,
              cache_creation_input_tokens: 0,
            },
          },
        },
      },
    ]),
  },
  {
    name: "anthropic-http-403-json",
    api: "anthropic-messages",
    model: textModel,
    context: { messages: [{ role: "user", content: "denied", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-anthropic-key", cacheRetention: "none" },
    httpStatus: 403,
    httpBody: '{"type":"error","error":{"type":"permission_error","message":"denied"}}',
    httpContentType: "application/json",
  },
  {
    name: "anthropic-http-403-empty-body",
    api: "anthropic-messages",
    model: textModel,
    context: { messages: [{ role: "user", content: "denied without details", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-anthropic-key", cacheRetention: "none" },
    httpStatus: 403,
    httpBody: "",
    httpContentType: "text/plain",
  },
];

// G1: the gateway-anthropic wire golden. Auth-resolve emits the
// cf-aig-authorization triple (cloudflare-auth.ts:84-89) whose null members
// must delete the SDK-owned x-api-key / authorization headers, and
// cloudflare-stream.ts materializes the endpoint placeholders from the
// resolved provider env.
async function buildCloudflareGatewayDefinition(): Promise<AnthropicDefinition> {
  const values: Record<string, string> = {
    CLOUDFLARE_API_KEY: "fixture-cloudflare-key",
    CLOUDFLARE_ACCOUNT_ID: "fixture-account",
    CLOUDFLARE_GATEWAY_ID: "fixture-gateway",
  };
  const resolved = await cloudflareAIGatewayAuth().resolve?.({
    ctx: {
      env: async (name: string) => values[name],
      fileExists: async () => false,
    },
  } as never);
  const auth = resolved as
    | { auth: { headers?: Record<string, string | null> }; env?: Record<string, string> }
    | undefined;
  if (!auth?.auth.headers || !auth.env) {
    throw new Error("cloudflareAIGatewayAuth() did not resolve the header triple");
  }
  return {
    name: "anthropic-cloudflare-gateway-null-header-auth",
    api: "anthropic-messages",
    simple: true,
    model: anthropicModel({
      provider: "cloudflare-ai-gateway",
      baseUrl: "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic",
    }),
    context: {
      systemPrompt: "Answer through the gateway.",
      messages: [{ role: "user", content: "hello <gateway>", timestamp: FIXED_NOW }],
    },
    options: {
      cacheRetention: "none",
      headers: auth.auth.headers,
      env: auth.env,
    } as SimpleStreamOptions,
  };
}

function pickAnthropicStream(definition: AnthropicDefinition) {
  const base = definition.simple ? streamSimpleAnthropic : streamAnthropic;
  if (!definition.model.provider.startsWith("cloudflare-")) return base;
  const wrapped = cloudflareStreams({
    stream: streamAnthropic,
    streamSimple: streamSimpleAnthropic,
  } as unknown as ProviderStreams);
  return (definition.simple ? wrapped.streamSimple.bind(wrapped) : wrapped.stream.bind(wrapped)) as typeof base;
}

function cloneEvent(event: AssistantMessageEvent): AssistantMessageEvent {
  return JSON.parse(JSON.stringify(event)) as AssistantMessageEvent;
}

async function captureRequest(input: RequestInfo | URL, init?: RequestInit): Promise<CapturedRequest> {
  const request = new Request(input, init);
  const headers: Record<string, string> = {};
  for (const name of selectedHeaders) {
    const value = request.headers.get(name);
    if (value !== null) headers[name] = value;
  }
  const userAgent = request.headers.get("user-agent");
  if (userAgent?.startsWith("claude-cli/")) headers["user-agent"] = userAgent;
  return {
    method: request.method,
    url: request.url,
    headers,
    body: await request.clone().text(),
  };
}

async function runAnthropic(
  definition: AnthropicDefinition,
  sse: string,
  status = 200,
  contentType = "text/event-stream",
): Promise<{ request: CapturedRequest; events: AssistantMessageEvent[] }> {
  let request: CapturedRequest | undefined;
  const events: AssistantMessageEvent[] = [];
  const originalFetch = globalThis.fetch;
  try {
    globalThis.fetch = async (input, init) => {
      request = await captureRequest(input, init);
      return new Response(sse, { status, headers: { "content-type": contentType } });
    };
    const options = { ...definition.options } as AnthropicOptions & SimpleStreamOptions;
    if (definition.payloadHook === "disable-stream") {
      options.onPayload = (payload: any) => {
        payload.stream = false;
      };
    }
    const stream = pickAnthropicStream(definition)(
      definition.model,
      definition.context,
      options,
    );
    for await (const event of stream) {
      events.push(cloneEvent(event));
    }
  } finally {
    globalThis.fetch = originalFetch;
  }
  if (!request) throw new Error(`${definition.name}: Anthropic request was not captured`);
  return { request, events };
}

async function extractAnthropicProvider(upstreamRoot: string): Promise<AnthropicProviderFixture> {
  const temporaryRoot = await mkdtemp(path.join(tmpdir(), "pigo-f2-anthropic-provider-"));
  const packageRoot = path.join(temporaryRoot, "ai");
  try {
    await cp(path.join(upstreamRoot, "packages/ai"), packageRoot, { recursive: true });
    const providerData = path.join(packageRoot, "src/providers/data");
    await mkdir(providerData, { recursive: true });
    await writeFile(
      path.join(providerData, "anthropic.json"),
      `${JSON.stringify({
        "fixture-anthropic-model": {
          id: "fixture-anthropic-model",
          name: "Fixture Anthropic Model",
          api: "anthropic-messages",
          provider: "anthropic",
          baseUrl: "https://api.anthropic.com",
          reasoning: false,
          input: ["text"],
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
          contextWindow: 1,
          maxTokens: 1,
        },
      })}\n`,
    );
    const providerModule = (await import(
      pathToFileURL(path.join(packageRoot, "src/providers/anthropic.ts")).href
    )) as {
      anthropicProvider(): {
        id: string;
        name: string;
        baseUrl?: string;
        auth: {
          apiKey?: {
            name: string;
            resolve(input: {
              ctx: {
                env(name: string): Promise<string | undefined>;
                fileExists(path: string): Promise<boolean>;
              };
            }): Promise<{ auth: { apiKey?: string }; source?: string } | undefined>;
          };
          oauth?: { name: string };
        };
        getModels(): readonly { api: string }[];
      };
    };
    const provider = providerModule.anthropicProvider();
    const apiKeyAuth = provider.auth.apiKey;
    if (!apiKeyAuth) throw new Error("anthropicProvider() did not expose API-key auth");
    const env: string[] = [];
    const unresolved = await apiKeyAuth.resolve({
      ctx: {
        env: async (name) => {
          env.push(name);
          return undefined;
        },
        fileExists: async () => false,
      },
    });
    if (unresolved !== undefined) throw new Error("anthropicProvider() resolved without credentials");
    const resolved = await apiKeyAuth.resolve({
      ctx: { env: async () => "fixture-anthropic-api-key", fileExists: async () => false },
    });
    if (!resolved?.auth.apiKey) throw new Error("anthropicProvider() did not resolve its environment API key");
    return {
      id: provider.id,
      name: provider.name,
      baseUrl: provider.baseUrl,
      apis: [...new Set(provider.getModels().map((entry) => entry.api))].sort(),
      auth: {
        kind: "api_key",
        name: apiKeyAuth.name,
        oauthName: provider.auth.oauth?.name,
        env,
        resolved: resolved.auth,
        source: resolved.source,
      },
    };
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
}

export async function extractAnthropicF2(upstreamRoot: string): Promise<{
  provider: AnthropicProviderFixture;
  requests: unknown[];
  streams: unknown[];
}> {
  const provider = await extractAnthropicProvider(upstreamRoot);
  const requests = [];
  for (const definition of [...requestDefinitions, await buildCloudflareGatewayDefinition()]) {
    const { request } = await runAnthropic(definition, "");
    requests.push({ ...definition, expected: request });
  }
  const streams = [];
  for (const definition of streamDefinitions) {
    const { events } = await runAnthropic(
      definition,
      definition.httpStatus === undefined ? (definition.sse ?? "") : (definition.httpBody ?? ""),
      definition.httpStatus ?? 200,
      definition.httpContentType ?? "text/event-stream",
    );
    streams.push({ ...definition, expectedEvents: events });
  }
  return { provider, requests, streams };
}
