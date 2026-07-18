import { cp, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import {
  stream as streamGoogle,
  type GoogleOptions,
} from "../../.upstream/packages/ai/src/api/google-generative-ai.ts";
import type {
  AssistantMessage,
  AssistantMessageEvent,
  Context,
  Model,
  Tool,
} from "../../.upstream/packages/ai/src/types.ts";

const FIXED_NOW = 1_700_000_000_123;

interface GoogleDefinition {
  name: string;
  api: "google-generative-ai";
  model: Model<"google-generative-ai">;
  context: Context;
  options: GoogleOptions;
  payloadConfigPatch?: Record<string, unknown>;
  payloadContents?: unknown;
}

interface GoogleStreamDefinition extends GoogleDefinition {
  sse?: string;
  httpStatus?: number;
  httpStatusText?: string;
  httpBody?: string;
  httpContentType?: string;
}

interface CapturedRequest {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string;
}

interface GoogleProviderFixture {
  id: string;
  name: string;
  baseUrl?: string;
  apis: string[];
  auth: {
    kind: "api_key";
    name: string;
    env: string[];
    resolved: { apiKey?: string };
    source?: string;
  };
}

interface FixtureResponse {
  status: number;
  statusText?: string;
  body: string;
  contentType: string;
}

const zeroCost = { input: 1, output: 4, cacheRead: 0.25, cacheWrite: 0 };

function googleModel(
  overrides: Partial<Model<"google-generative-ai">> = {},
): Model<"google-generative-ai"> {
  return {
    id: "gemini-2.5-flash",
    name: "Google Fixture Model",
    api: "google-generative-ai",
    provider: "google",
    baseUrl: "https://generativelanguage.googleapis.com/v1beta",
    reasoning: true,
    input: ["text", "image"],
    cost: zeroCost,
    contextWindow: 1_000_000,
    maxTokens: 65_536,
    ...overrides,
  };
}

function assistant(
  model: Model<"google-generative-ai">,
  content: AssistantMessage["content"],
  overrides: Partial<AssistantMessage> = {},
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
    stopReason: "stop",
    timestamp: FIXED_NOW - 2,
    ...overrides,
  };
}

const stringEnumTool: Tool = {
  name: "calculate",
  description: "Calculate with a provider-compatible string enum",
  parameters: {
    type: "object",
    properties: {
      operation: {
        type: "string",
        enum: ["add", "subtract", "multiply", "divide"],
        description: "The operation to perform",
      },
      operands: {
        type: "array",
        items: { type: "number" },
      },
    },
    required: ["operation", "operands"],
    additionalProperties: false,
  },
};

const textModel = googleModel({
  headers: {
    "Content-Type": "application/model-json",
    "x-goog-api-key": "model-header-key",
    "x-model-header": "google-text",
  },
});

const replayModel = googleModel();

const requestDefinitions: GoogleDefinition[] = [
  {
    name: "google-text-system-string-enum-tool",
    api: "google-generative-ai",
    model: textModel,
    context: {
      systemPrompt: "You are concise.",
      messages: [{ role: "user", content: "hello <google>", timestamp: FIXED_NOW }],
      tools: [stringEnumTool],
    },
    options: {
      apiKey: "fixture-google-key",
      temperature: 0,
      maxTokens: 777,
      toolChoice: "auto",
      headers: {
        "Content-Type": "application/x-google-fixture",
        "x-fixture": "google-text",
        "x-goog-api-key": "option-header-key",
      },
    },
  },
  {
    name: "google-rich-replay-thinking-images-tools",
    api: "google-generative-ai",
    model: replayModel,
    context: {
      systemPrompt: "Preserve signed thoughts and use tools.",
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
          { type: "thinking", thinking: "signed plan", thinkingSignature: "cGxhbg==" },
          { type: "text", text: "using tool", textSignature: "dGV4dA==" },
          {
            type: "toolCall",
            id: "call_fixture",
            name: "calculate",
            arguments: { operation: "add", operands: [2, 3] },
            thoughtSignature: "dG9vbA==",
          },
        ], { stopReason: "toolUse" }),
        {
          role: "toolResult",
          toolCallId: "call_fixture",
          toolName: "calculate",
          content: [
            { type: "text", text: "5" },
            { type: "image", data: "cGl4ZWw=", mimeType: "image/webp" },
          ],
          isError: false,
          timestamp: FIXED_NOW - 1,
        },
        { role: "user", content: "continue", timestamp: FIXED_NOW },
      ],
      tools: [stringEnumTool],
    },
    options: {
      apiKey: "fixture-google-key",
      maxTokens: 4_096,
      toolChoice: "any",
      thinking: { enabled: true, budgetTokens: 2_048 },
    },
  },
  {
    name: "google-cross-model-signatures-and-nonvision-image",
    api: "google-generative-ai",
    model: googleModel({ id: "gemini-2.5-flash-lite", input: ["text"] }),
    context: {
      messages: [
        {
          role: "user",
          content: [
            { type: "image", data: "aW1hZ2U=", mimeType: "image/png" },
            { type: "text", text: "describe" },
          ],
          timestamp: FIXED_NOW - 2,
        },
        assistant(
          googleModel({ id: "other-model" }),
          [
            { type: "thinking", thinking: "old thought", thinkingSignature: "not-base64" },
            { type: "text", text: "old answer", textSignature: "not-base64" },
            {
              type: "toolCall",
              id: "call_old",
              name: "calculate",
              arguments: { operation: "add", operands: [1, 1] },
              thoughtSignature: "not-base64",
            },
          ],
          { model: "other-model", stopReason: "toolUse" },
        ),
        {
          role: "toolResult",
          toolCallId: "call_old",
          toolName: "calculate",
          content: [{ type: "text", text: "2" }],
          isError: false,
          timestamp: FIXED_NOW,
        },
      ],
    },
    options: { apiKey: "fixture-google-key", thinking: { enabled: false } },
  },
  {
    name: "google-gemini31-pro-thinking-disabled",
    api: "google-generative-ai",
    model: googleModel({ id: "gemini-3.1-pro-preview" }),
    context: { messages: [{ role: "user", content: "no visible thoughts", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key", thinking: { enabled: false } },
  },
  {
    name: "google-gemini3-flash-thinking-level",
    api: "google-generative-ai",
    model: googleModel({ id: "gemini-3-flash-preview" }),
    context: { messages: [{ role: "user", content: "think minimally", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key", thinking: { enabled: true, level: "MINIMAL" } },
  },
  {
    name: "google-payload-hook-supported-config",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "structured response", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    payloadConfigPatch: {
      topP: 0.25,
      topK: 32,
      candidateCount: 2,
      stopSequences: ["END"],
      responseMimeType: "application/json",
      responseSchema: {
        type: "object",
        properties: { answer: { type: "string" } },
        additionalProperties: false,
      },
      responseJsonSchema: {
        type: "object",
        properties: { answer: { type: "string" } },
        required: ["answer"],
        additionalProperties: false,
      },
      safetySettings: [
        { category: "HARM_CATEGORY_HATE_SPEECH", threshold: "BLOCK_NONE" },
      ],
      systemInstruction: { parts: [{ text: "hook system" }], role: "model" },
      tools: [{ googleSearch: {} }],
      toolConfig: {
        retrievalConfig: { latLng: { latitude: 48.8566, longitude: 2.3522 } },
        functionCallingConfig: { allowedFunctionNames: ["calculate"], mode: "ANY" },
        includeServerSideToolInvocations: true,
      },
      responseModalities: ["TEXT"],
      imageConfig: { aspectRatio: "1:1", imageSize: "1K", ignoredUnknown: "dropped" },
    },
  },
  {
    name: "google-uppercase-slash-model-thinking-disabled",
    api: "google-generative-ai",
    model: googleModel({ id: "publishers/acme/models/GEMINI-3-FLASH-PREVIEW" }),
    context: { messages: [{ role: "user", content: "no visible thoughts", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key", thinking: { enabled: false } },
  },
  {
    name: "google-payload-hook-dollar-schema-empty-tools",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "schema compatibility", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    payloadConfigPatch: {
      responseSchema: {
        $schema: "https://json-schema.org/draft/2020-12/schema",
        type: "object",
        properties: { answer: { type: "string" } },
      },
      responseJsonSchema: null,
      tools: [],
      toolConfig: { functionCallingConfig: { allowedFunctionNames: [] } },
    },
  },
  {
    name: "google-payload-hook-content-union",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "replaced by hook", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    payloadContents: [
      "hook text",
      {
        mediaResolution: "MEDIA_RESOLUTION_HIGH",
        executableCode: { language: "PYTHON", code: "print('hook')" },
        fileData: { fileUri: "gs://fixture/image.png", mimeType: "image/png" },
        text: "hook advanced",
        ignoredUnknown: "dropped",
      },
    ],
  },
  {
    name: "google-claude-astral-tool-id",
    api: "google-generative-ai",
    model: googleModel({ id: "claude-sonnet-fixture" }),
    context: {
      messages: [
        { role: "user", content: "use the tool", timestamp: FIXED_NOW - 2 },
        assistant(
          googleModel({ id: "gemini-2.5-flash" }),
          [{ type: "toolCall", id: "call🙈id", name: "calculate", arguments: { operation: "add", operands: [1, 2] } }],
          { stopReason: "toolUse", timestamp: FIXED_NOW - 1 },
        ),
        {
          role: "toolResult",
          toolCallId: "call🙈id",
          toolName: "calculate",
          content: [{ type: "text", text: "3" }],
          isError: false,
          timestamp: FIXED_NOW,
        },
      ],
      tools: [stringEnumTool],
    },
    options: { apiKey: "fixture-google-key" },
  },
  {
    name: "google-gemini3-nested-image-tool-results",
    api: "google-generative-ai",
    model: googleModel({ id: "gemini-3-pro-preview" }),
    context: {
      messages: [
        { role: "user", content: "read the files", timestamp: FIXED_NOW - 4 },
        assistant(
          googleModel({ id: "gemini-3-pro-preview" }),
          [
            { type: "toolCall", id: "call_a", name: "read", arguments: { path: "a.txt" } },
            { type: "toolCall", id: "call_img", name: "read", arguments: { path: "image.png" } },
            { type: "toolCall", id: "call_b", name: "read", arguments: { path: "b.txt" } },
          ],
          { model: "gemini-3-pro-preview", stopReason: "toolUse", timestamp: FIXED_NOW - 3 },
        ),
        {
          role: "toolResult",
          toolCallId: "call_a",
          toolName: "read",
          content: [{ type: "text", text: "alpha text" }],
          isError: false,
          timestamp: FIXED_NOW - 2,
        },
        {
          role: "toolResult",
          toolCallId: "call_img",
          toolName: "read",
          content: [{ type: "image", data: "abc", mimeType: "image/png" }],
          isError: false,
          timestamp: FIXED_NOW - 1,
        },
        {
          role: "toolResult",
          toolCallId: "call_b",
          toolName: "read",
          content: [{ type: "text", text: "beta text" }],
          isError: false,
          timestamp: FIXED_NOW,
        },
      ],
    },
    options: { apiKey: "fixture-google-key" },
  },
];

function encodeSSE(chunks: unknown[]): string {
  return chunks.map((chunk) => `data: ${JSON.stringify(chunk)}\n\n`).join("");
}

const minimalSSE = encodeSSE([
  {
    responseId: "google-request-fixture",
    candidates: [{ content: { role: "model", parts: [] }, finishReason: "STOP" }],
    usageMetadata: {
      promptTokenCount: 0,
      candidatesTokenCount: 0,
      totalTokenCount: 0,
    },
  },
]);

const streamDefinitions: GoogleStreamDefinition[] = [
  {
    name: "google-thinking-text-tool-use",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "stream", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    sse: encodeSSE([
      {
        responseId: "google-response-id",
        candidates: [
          {
            content: {
              role: "model",
              parts: [{ text: "plan ", thought: true, thoughtSignature: "c2lnLTE=" }],
            },
          },
        ],
      },
      {
        candidates: [
          {
            content: {
              role: "model",
              parts: [{ text: "carefully", thought: true }],
            },
          },
        ],
      },
      {
        candidates: [
          {
            content: {
              role: "model",
              parts: [
                { text: "answer", thoughtSignature: "dGV4dC1zaWc=" },
                {
                  functionCall: {
                    id: "call_google",
                    name: "calculate",
                    args: { operation: "add", operands: [2, 3] },
                  },
                  thoughtSignature: "dG9vbC1zaWc=",
                },
              ],
            },
            finishReason: "STOP",
          },
        ],
        usageMetadata: {
          promptTokenCount: 20,
          cachedContentTokenCount: 5,
          candidatesTokenCount: 7,
          thoughtsTokenCount: 3,
          totalTokenCount: 30,
        },
      },
    ]),
  },
  {
    name: "google-text-length",
    api: "google-generative-ai",
    model: googleModel({ reasoning: false }),
    context: { messages: [{ role: "user", content: "long answer", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    sse: encodeSSE([
      {
        responseId: "google-length-id",
        candidates: [
          {
            content: { role: "model", parts: [{ text: "partial" }] },
            finishReason: "MAX_TOKENS",
          },
        ],
        usageMetadata: { promptTokenCount: 4, candidatesTokenCount: 2, totalTokenCount: 6 },
      },
    ]),
  },
  {
    name: "google-safety-finish-error",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "blocked", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    sse: encodeSSE([
      {
        candidates: [{ content: { role: "model", parts: [] }, finishReason: "SAFETY" }],
        usageMetadata: { promptTokenCount: 1, totalTokenCount: 1 },
      },
    ]),
  },
  {
    name: "google-http-403-json",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "reject", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    httpStatus: 403,
    httpBody: '{"error":{"code":403,"message":"denied","status":"PERMISSION_DENIED"}}',
    httpContentType: "application/json",
  },
  {
    name: "google-http-pretty-json",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "pretty reject", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    httpStatus: 400,
    httpStatusText: "Bad Request",
    httpBody: '{\n  "error": { "message": "bad shape", "code": 400, "status": "INVALID_ARGUMENT" }\n}',
    httpContentType: "application/json; charset=utf-8",
  },
  {
    name: "google-http-429-text",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "plain reject", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    httpStatus: 429,
    httpStatusText: "Too Many Requests",
    httpBody: "slow down",
    httpContentType: "text/plain",
  },
  {
    name: "google-http-503-empty",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "empty reject", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    httpStatus: 503,
    httpStatusText: "Service Unavailable",
    httpBody: "",
    httpContentType: "text/plain",
  },
  {
    name: "google-multiline-tool-call-empty-signature",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "call the tool", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    sse: `data: {
  "responseId": "google-multiline-id",
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {
            "functionCall": { "id": "call_empty", "name": "calculate", "args": {} },
            "thoughtSignature": ""
          }
        ]
      },
      "finishReason": "STOP"
    }
  ],
  "usageMetadata": { "promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2 }
}

`,
  },
  {
    name: "google-raw-json-error-chunk",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "fail in stream", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    sse: '{"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied"}}',
  },
  {
    name: "google-raw-json-error-coercion",
    api: "google-generative-ai",
    model: googleModel(),
    context: { messages: [{ role: "user", content: "coerced stream failure", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-google-key" },
    sse: '{"10":"ten","2":"two","error":{"code":"4.03e2","message":"\\u0061"}}',
  },
];

function cloneEvent(event: AssistantMessageEvent): AssistantMessageEvent {
  return JSON.parse(JSON.stringify(event)) as AssistantMessageEvent;
}

async function captureRequest(input: RequestInfo | URL, init?: RequestInit): Promise<CapturedRequest> {
  const request = new Request(input, init);
  const headers: Record<string, string> = {};
  for (const name of ["content-type", "x-fixture", "x-goog-api-key", "x-model-header"]) {
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

function fixtureResponse(definition: GoogleStreamDefinition): FixtureResponse {
  if (definition.httpStatus !== undefined) {
    return {
      status: definition.httpStatus,
      statusText: definition.httpStatusText,
      body: definition.httpBody ?? "",
      contentType: definition.httpContentType ?? "text/plain",
    };
  }
  return { status: 200, body: definition.sse ?? minimalSSE, contentType: "text/event-stream" };
}

async function runGoogle(
  definition: GoogleDefinition,
  response: FixtureResponse,
): Promise<{ request: CapturedRequest; events: AssistantMessageEvent[] }> {
  let request: CapturedRequest | undefined;
  const events: AssistantMessageEvent[] = [];
  const originalFetch = globalThis.fetch;
  try {
    globalThis.fetch = async (input, init) => {
      request = await captureRequest(input, init);
      return new Response(response.body, {
        status: response.status,
        statusText: response.statusText,
        headers: { "content-type": response.contentType },
      });
    };
    const options: GoogleOptions = { ...definition.options };
    if (definition.payloadConfigPatch || definition.payloadContents !== undefined) {
      const patch = definition.payloadConfigPatch;
      options.onPayload = (params) => ({
        ...params,
        ...(definition.payloadContents !== undefined ? { contents: definition.payloadContents } : {}),
        ...(patch ? { config: { ...params.config, ...patch } } : {}),
      });
    }
    for await (const event of streamGoogle(definition.model, definition.context, options)) {
      events.push(cloneEvent(event));
    }
  } finally {
    globalThis.fetch = originalFetch;
  }
  if (!request) throw new Error(`${definition.name}: Google request was not captured`);
  const terminal = events.at(-1);
  const expectsError =
    response.status >= 400 ||
    definition.name === "google-safety-finish-error" ||
    definition.name.startsWith("google-raw-json-error");
  if (!terminal || (terminal.type === "error") !== expectsError) {
    throw new Error(`${definition.name}: unexpected terminal event: ${JSON.stringify(terminal)}`);
  }
  return { request, events };
}

async function extractGoogleProvider(upstreamRoot: string): Promise<GoogleProviderFixture> {
  const temporaryRoot = await mkdtemp(path.join(tmpdir(), "pi-go-f2-google-provider-"));
  const packageRoot = path.join(temporaryRoot, "ai");
  try {
    await cp(path.join(upstreamRoot, "packages/ai"), packageRoot, { recursive: true });
    const providerData = path.join(packageRoot, "src/providers/data");
    await mkdir(providerData, { recursive: true });
    await writeFile(
      path.join(providerData, "google.json"),
      `${JSON.stringify({
        "gemini-fixture": {
          id: "gemini-fixture",
          name: "Fixture Google Model",
          api: "google-generative-ai",
          provider: "google",
          baseUrl: "https://generativelanguage.googleapis.com/v1beta",
          reasoning: true,
          input: ["text"],
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
          contextWindow: 1,
          maxTokens: 1,
        },
      })}\n`,
    );
    const providerModule = (await import(
      pathToFileURL(path.join(packageRoot, "src/providers/google.ts")).href
    )) as {
      googleProvider(): {
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
        };
        getModels(): readonly { api: string }[];
      };
    };
    const provider = providerModule.googleProvider();
    const auth = provider.auth.apiKey;
    if (!auth) throw new Error("googleProvider() did not expose API-key auth");
    const env: string[] = [];
    const unresolved = await auth.resolve({
      ctx: {
        env: async (name) => {
          env.push(name);
          return undefined;
        },
        fileExists: async () => false,
      },
    });
    if (unresolved !== undefined) throw new Error("googleProvider() resolved without credentials");
    const resolved = await auth.resolve({
      ctx: { env: async () => "fixture-google-api-key", fileExists: async () => false },
    });
    if (!resolved?.auth.apiKey) throw new Error("googleProvider() did not resolve its environment API key");
    return {
      id: provider.id,
      name: provider.name,
      baseUrl: provider.baseUrl,
      apis: [...new Set(provider.getModels().map((entry) => entry.api))].sort(),
      auth: {
        kind: "api_key",
        name: auth.name,
        env,
        resolved: resolved.auth,
        source: resolved.source,
      },
    };
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
}

export async function extractGoogleF2(upstreamRoot: string): Promise<{
  provider: GoogleProviderFixture;
  requests: unknown[];
  streams: unknown[];
}> {
  const provider = await extractGoogleProvider(upstreamRoot);
  const requests = [];
  for (const definition of requestDefinitions) {
    const { request } = await runGoogle(definition, {
      status: 200,
      body: minimalSSE,
      contentType: "text/event-stream",
    });
    requests.push({ ...definition, expected: request });
  }
  const streams = [];
  for (const definition of streamDefinitions) {
    const { events } = await runGoogle(definition, fixtureResponse(definition));
    streams.push({ ...definition, expectedEvents: events });
  }
  return { provider, requests, streams };
}
