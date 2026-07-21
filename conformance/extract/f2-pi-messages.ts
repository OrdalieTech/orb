import {
  stream as streamPiMessages,
  type PiMessagesOptions,
} from "../../.upstream/packages/ai/src/api/pi-messages.ts";
import type {
  AssistantMessageEvent,
  Context,
  Model,
} from "../../.upstream/packages/ai/src/types.ts";

const FIXED_NOW = 1_700_000_000_123;

interface PiMessagesDefinition {
  name: string;
  api: "pi-messages";
  model: Model<"pi-messages">;
  context: Context;
  options: PiMessagesOptions;
}

interface PiMessagesStreamDefinition extends PiMessagesDefinition {
  sse: string;
}

interface CapturedRequest {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string;
}

const zeroCost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
const usage = {
  input: 10,
  output: 5,
  cacheRead: 1,
  cacheWrite: 2,
  totalTokens: 18,
  cost: { input: 0.1, output: 0.2, cacheRead: 0.01, cacheWrite: 0.02, total: 0.33 },
};

const model: Model<"pi-messages"> = {
  id: "gateway-auto",
  name: "Gateway Auto",
  api: "pi-messages",
  provider: "fixture-gateway",
  baseUrl: "https://gateway.fixture.invalid/v1///",
  reasoning: true,
  input: ["text"],
  cost: zeroCost,
  contextWindow: 128_000,
  maxTokens: 16_384,
};

const context: Context = {
  systemPrompt: "Use the gateway.",
  messages: [{ role: "user", content: "Hello <gateway>", timestamp: FIXED_NOW - 1 }],
  tools: [
    {
      name: "read",
      description: "Read a file",
      parameters: {
        type: "object",
        properties: { path: { type: "string" } },
        required: ["path"],
        additionalProperties: false,
      },
    },
  ],
};

const requestDefinitions: PiMessagesDefinition[] = [
  {
    name: "pi-messages-request-full-options",
    api: "pi-messages",
    model,
    context,
    options: {
      apiKey: "fixture-key",
      temperature: 0,
      maxTokens: 100,
      reasoning: "high",
      cacheRetention: "long",
      sessionId: "session-fixture",
      toolChoice: { type: "function", function: { name: "read" } },
      debug: true,
      headers: { "x-fixture": "pi-messages" },
    },
  },
  {
    // Minimal options: unset fields vanish from the JSON options object, no
    // debug query parameter, and cacheRetention stays a backend default.
    name: "pi-messages-request-minimal-options",
    api: "pi-messages",
    model,
    context,
    options: { apiKey: "fixture-key" },
  },
];

function encodeSSE(events: unknown[], trailingDelimiter = true): string {
  const value = events.map((event) => `data: ${JSON.stringify(event)}`).join("\r\n\r\n");
  return trailingDelimiter ? `${value}\r\n\r\n` : value;
}

const streamDefinitions: PiMessagesStreamDefinition[] = [
  {
    name: "pi-messages-text-thinking-tool-rewrite",
    api: "pi-messages",
    model,
    context,
    options: { apiKey: "fixture-key" },
    sse: encodeSSE([
      { type: "start" },
      { type: "text_start", contentIndex: 0 },
      { type: "text_delta", contentIndex: 0, delta: "Hel" },
      { type: "text_delta", contentIndex: 0, delta: "lo" },
      { type: "text_end", contentIndex: 0, content: "Hello", contentSignature: "text-sig" },
      { type: "thinking_start", contentIndex: 1 },
      { type: "thinking_delta", contentIndex: 1, delta: "plan" },
      {
        type: "thinking_end",
        contentIndex: 1,
        content: "plan",
        contentSignature: "thinking-sig",
        redacted: true,
      },
      { type: "toolcall_start", contentIndex: 2, id: "call_1", toolName: "read" },
      { type: "toolcall_delta", contentIndex: 2, delta: '{"path":' },
      { type: "toolcall_delta", contentIndex: 2, delta: '"a.txt"}' },
      {
        type: "toolcall_end",
        contentIndex: 2,
        toolCall: { type: "toolCall", id: "call_1", name: "read", arguments: { path: "a.txt" } },
      },
      {
        type: "done",
        reason: "toolUse",
        usage,
        responseId: "response-1",
        rewrite: {
          policyId: "policy-1",
          policyVersion: 3,
          changed: true,
          tokenCountChange: -2,
          messageCountChange: 0,
          systemPromptChanged: true,
        },
      },
    ]),
  },
  {
    name: "pi-messages-server-error",
    api: "pi-messages",
    model,
    context,
    options: { apiKey: "fixture-key" },
    sse: encodeSSE(
      [
        { type: "start" },
        {
          type: "error",
          reason: "error",
          usage,
          errorMessage: "upstream failed",
          responseId: "response-error",
        },
      ],
      false,
    ),
  },
  {
    name: "pi-messages-missing-terminal",
    api: "pi-messages",
    model,
    context,
    options: { apiKey: "fixture-key" },
    sse: encodeSSE([
      { type: "start" },
      { type: "text_start", contentIndex: 0 },
      { type: "text_delta", contentIndex: 0, delta: "partial" },
    ]),
  },
];

function cloneEvent(event: AssistantMessageEvent): AssistantMessageEvent {
  return JSON.parse(JSON.stringify(event)) as AssistantMessageEvent;
}

async function captureRequest(input: RequestInfo | URL, init?: RequestInit): Promise<CapturedRequest> {
  const request = new Request(input, init);
  const headers: Record<string, string> = {};
  for (const name of ["accept", "authorization", "content-type", "x-fixture"] as const) {
    const value = request.headers.get(name);
    if (value !== null) headers[name] = value;
  }
  return {
    method: request.method,
    url: request.url,
    headers,
    body: await request.clone().text(),
  };
}

async function runUpstream(
  definition: PiMessagesDefinition,
  sse: string,
): Promise<{ request: CapturedRequest; events: AssistantMessageEvent[] }> {
  let captured: CapturedRequest | undefined;
  const originalFetch = globalThis.fetch;
  try {
    globalThis.fetch = async (input, init) => {
      captured = await captureRequest(input, init);
      return new Response(sse, { status: 200, headers: { "content-type": "text/event-stream" } });
    };

    const events: AssistantMessageEvent[] = [];
    for await (const event of streamPiMessages(definition.model, definition.context, definition.options)) {
      events.push(cloneEvent(event));
    }
    if (!captured) throw new Error(`${definition.name}: pi-messages request was not captured`);
    return { request: captured, events };
  } finally {
    globalThis.fetch = originalFetch;
  }
}

function requestTerminalSSE(): string {
  return encodeSSE([
    {
      type: "done",
      reason: "stop",
      usage: {
        ...usage,
        input: 0,
        output: 0,
        cacheRead: 0,
        cacheWrite: 0,
        totalTokens: 0,
        cost: { ...zeroCost, total: 0 },
      },
    },
  ]);
}

export async function extractPiMessagesF2(): Promise<{
  requests: Array<PiMessagesDefinition & { expected: CapturedRequest }>;
  streams: Array<PiMessagesStreamDefinition & { expectedEvents: AssistantMessageEvent[] }>;
}> {
  const originalNow = Date.now;
  Date.now = () => FIXED_NOW;
  try {
    const requests = [];
    for (const definition of requestDefinitions) {
      const { request } = await runUpstream(definition, requestTerminalSSE());
      requests.push({ ...definition, expected: request });
    }
    const streams = [];
    for (const definition of streamDefinitions) {
      const { events } = await runUpstream(definition, definition.sse);
      streams.push({ ...definition, expectedEvents: events });
    }
    return { requests, streams };
  } finally {
    Date.now = originalNow;
  }
}
