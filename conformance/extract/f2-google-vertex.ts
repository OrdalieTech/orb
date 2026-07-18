import { cp, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import {
  stream as streamGoogleVertex,
  streamSimple as streamSimpleGoogleVertex,
  type GoogleVertexOptions,
} from "../../.upstream/packages/ai/src/api/google-vertex.ts";
import type {
  AssistantMessage,
  AssistantMessageEvent,
  Context,
  Model,
  SimpleStreamOptions,
  Tool,
} from "../../.upstream/packages/ai/src/types.ts";

const FIXED_NOW = 1_700_000_000_123;

interface VertexDefinition {
  name: string;
  api: "google-vertex";
  simple?: boolean;
  model: Model<"google-vertex">;
  context: Context;
  options: GoogleVertexOptions | SimpleStreamOptions;
  payloadConfigPatch?: Record<string, unknown>;
  payloadContents?: unknown;
}

interface VertexStreamDefinition extends VertexDefinition {
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

interface FixtureResponse {
  status: number;
  statusText?: string;
  body: string;
  contentType: string;
}

interface VertexProviderFixture {
  id: string;
  name: string;
  baseUrl?: string;
  apis: string[];
  auth: {
    kind: "api_key";
    name: string;
    login: {
      apiKey: unknown;
      adc: unknown;
      serviceAccount: unknown;
      notifications: unknown[];
    };
    envAPIKeys: {
      apiKey: string | undefined;
      adc: string | undefined;
      missingLocation: string | null;
      found: string[] | undefined;
    };
    resolutions: Array<{
      name: string;
      result: unknown;
      envLookups: string[];
      fileLookups: string[];
    }>;
  };
}

const cost = { input: 1, output: 4, cacheRead: 0.25, cacheWrite: 0 };
const zeroUsage = {
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  totalTokens: 0,
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
};

function vertexModel(
  overrides: Partial<Model<"google-vertex">> = {},
): Model<"google-vertex"> {
  return {
    id: "gemini-3-flash-preview",
    name: "Vertex Fixture Model",
    api: "google-vertex",
    provider: "google-vertex",
    baseUrl: "https://{location}-aiplatform.googleapis.com/v1",
    reasoning: true,
    input: ["text", "image"],
    cost,
    contextWindow: 1_000_000,
    maxTokens: 65_536,
    ...overrides,
  };
}

function assistant(
  model: Model<"google-vertex">,
  content: AssistantMessage["content"],
  overrides: Partial<AssistantMessage> = {},
): AssistantMessage {
  return {
    role: "assistant",
    content,
    api: model.api,
    provider: model.provider,
    model: model.id,
    usage: zeroUsage,
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
      },
      operands: { type: "array", items: { type: "number" } },
    },
    required: ["operation", "operands"],
    additionalProperties: false,
  },
};

const requestDefinitions: VertexDefinition[] = [
  {
    name: "google-vertex-express-text-system-string-enum-tool",
    api: "google-vertex",
    model: vertexModel(),
    context: {
      systemPrompt: "You are concise.",
      messages: [{ role: "user", content: "hello vertex", timestamp: FIXED_NOW }],
      tools: [stringEnumTool],
    },
    options: {
      apiKey: "  fixture-vertex-key  ",
      project: "ignored-with-api-key",
      location: "ignored-with-api-key",
      temperature: 0,
      maxTokens: 777,
      toolChoice: "auto",
      thinking: { enabled: true, level: "MINIMAL" },
    },
  },
  {
    name: "google-vertex-rich-replay-thinking-images-tools",
    api: "google-vertex",
    model: vertexModel({ id: "gemini-2.5-flash" }),
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
        assistant(
          vertexModel({ id: "gemini-2.5-flash" }),
          [
            { type: "thinking", thinking: "signed plan", thinkingSignature: "cGxhbg==" },
            { type: "text", text: "using tool", textSignature: "dGV4dA==" },
            {
              type: "toolCall",
              id: "call_fixture",
              name: "calculate",
              arguments: { operation: "add", operands: [2, 3] },
              thoughtSignature: "dG9vbA==",
            },
          ],
          { stopReason: "toolUse" },
        ),
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
      apiKey: "fixture-vertex-key",
      maxTokens: 4_096,
      toolChoice: "any",
      thinking: { enabled: true, budgetTokens: 2_048 },
    },
  },
  {
    name: "google-vertex-gemini31-pro-thinking-disabled",
    api: "google-vertex",
    model: vertexModel({ id: "gemini-3.1-pro-preview" }),
    context: { messages: [{ role: "user", content: "hide thoughts", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key", thinking: { enabled: false } },
  },
  {
    name: "google-vertex-gemini3-flash-thinking-level",
    api: "google-vertex",
    model: vertexModel(),
    context: { messages: [{ role: "user", content: "think minimally", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key", thinking: { enabled: true, level: "MINIMAL" } },
  },
  {
    name: "google-vertex-gemini25-flash-lite-simple-minimal",
    api: "google-vertex",
    simple: true,
    model: vertexModel({ id: "gemini-2.5-flash-lite" }),
    context: { messages: [{ role: "user", content: "use minimal budget", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key", reasoning: "minimal" },
  },
  {
    name: "google-vertex-custom-base-appends-v1-publisher-shorthand",
    api: "google-vertex",
    model: vertexModel({ id: "acme/model-x", baseUrl: " https://proxy.example.com/root/ " }),
    context: { messages: [{ role: "user", content: "custom route", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key" },
  },
  {
    name: "google-vertex-custom-base-keeps-version-and-appends-qualified-model",
    api: "google-vertex",
    model: vertexModel({
      id: "projects/p/locations/global/publishers/acme/models/model-x",
      baseUrl: "https://proxy.example.com/v1/projects/p/locations/global",
    }),
    context: { messages: [{ role: "user", content: "versioned route", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key" },
  },
  {
    name: "google-vertex-header-precedence",
    api: "google-vertex",
    model: vertexModel({
      headers: {
        "Content-Type": "model/type",
        "x-goog-api-key": "model-header-key",
        "x-model-header": "vertex-model",
      },
    }),
    context: { messages: [{ role: "user", content: "headers", timestamp: FIXED_NOW }] },
    options: {
      apiKey: "fixture-vertex-key",
      headers: {
        "Content-Type": "option/type",
        "x-goog-api-key": "option-header-key",
        "x-fixture": "vertex-options",
      },
    },
  },
  {
    name: "google-vertex-payload-hook-vertex-config",
    api: "google-vertex",
    model: vertexModel(),
    context: { messages: [{ role: "user", content: "vertex config", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key" },
    payloadConfigPatch: {
      topP: 0.25,
      routingConfig: { autoMode: {} },
      modelSelectionConfig: { featureSelectionPreference: "BALANCED" },
      labels: { team: "pi" },
      audioTimestamp: true,
      imageConfig: {
        aspectRatio: "1:1",
        imageSize: "1K",
        personGeneration: "ALLOW_ADULT",
        outputMimeType: "image/png",
        outputCompressionQuality: 80,
      },
      modelArmorConfig: { promptTemplateName: "projects/p/locations/l/templates/t" },
      serviceTier: "PRIORITY",
      tools: [{ enterpriseWebSearch: {} }],
      toolConfig: {
        retrievalConfig: { x: 1 },
        functionCallingConfig: { allowedFunctionNames: ["f"], mode: "ANY" },
      },
    },
  },
  {
    name: "google-vertex-unsigned-gemini3-tool-calls",
    api: "google-vertex",
    model: vertexModel({ id: "gemini-3-pro-preview" }),
    context: {
      messages: [
        { role: "user", content: "run tools", timestamp: FIXED_NOW - 1 },
        assistant(
          vertexModel({ id: "gemini-3-pro-preview" }),
          [
            { type: "toolCall", id: "call_1", name: "bash", arguments: { command: "echo hi" } },
            { type: "toolCall", id: "call_2", name: "bash", arguments: { command: "ls -la" } },
          ],
          { stopReason: "toolUse" },
        ),
      ],
    },
    options: { apiKey: "fixture-vertex-key", thinking: { enabled: false } },
  },
];

const minimalSSE =
  'data: {"responseId":"vertex_request_fixture","candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}\n\n';

const streamDefinitions: VertexStreamDefinition[] = [
  {
    name: "google-vertex-thinking-text-tool-use",
    api: "google-vertex",
    model: vertexModel(),
    context: { messages: [{ role: "user", content: "stream", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key" },
    sse: [
      'data: {"responseId":"vertex-rich-id","candidates":[{"content":{"role":"model","parts":[{"text":"plan","thought":true,"thoughtSignature":"cGxhbg=="}]}}],"usageMetadata":{"promptTokenCount":10,"cachedContentTokenCount":2,"candidatesTokenCount":1,"thoughtsTokenCount":3,"totalTokenCount":14}}',
      'data: {"responseId":"vertex-later-id","candidates":[{"content":{"role":"model","parts":[{"text":"answer","thoughtSignature":"dGV4dA=="},{"functionCall":{"id":"provided_call","name":"calculate","args":{"operation":"add","operands":[2,3]}},"thoughtSignature":"dG9vbA=="}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"cachedContentTokenCount":4,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":17}}',
      "",
    ].join("\n\n"),
  },
  {
    name: "google-vertex-generated-duplicate-tool-ids",
    api: "google-vertex",
    model: vertexModel(),
    context: { messages: [{ role: "user", content: "ids", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key" },
    sse:
      'data: {"responseId":"vertex-tool-ids","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"calculate","args":{}}},{"functionCall":{"id":"same","name":"calculate","args":{"n":1}}},{"functionCall":{"id":"same","name":"calculate","args":{"n":2}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}\n\n',
  },
  {
    name: "google-vertex-text-length",
    api: "google-vertex",
    model: vertexModel(),
    context: { messages: [{ role: "user", content: "length", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key" },
    sse:
      'data: {"responseId":"vertex-length","candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}\n\n',
  },
  {
    name: "google-vertex-safety-finish-error",
    api: "google-vertex",
    model: vertexModel(),
    context: { messages: [{ role: "user", content: "safety", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key" },
    sse:
      'data: {"responseId":"vertex-safety","candidates":[{"content":{"role":"model","parts":[]},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":0,"totalTokenCount":2}}\n\n',
  },
  {
    name: "google-vertex-http-403-json",
    api: "google-vertex",
    model: vertexModel(),
    context: { messages: [{ role: "user", content: "reject", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key" },
    httpStatus: 403,
    httpBody: '{"error":{"code":403,"message":"denied","status":"PERMISSION_DENIED"}}',
    httpContentType: "application/json",
  },
  {
    name: "google-vertex-multiline-tool-call-empty-signature",
    api: "google-vertex",
    model: vertexModel(),
    context: { messages: [{ role: "user", content: "tool", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key" },
    sse: `data: {
  "responseId": "vertex-multiline-id",
  "candidates": [{
    "content": {"role":"model","parts":[{
      "functionCall":{"id":"call_empty","name":"calculate","args":{}},
      "thoughtSignature":""
    }]},
    "finishReason":"STOP"
  }],
  "usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
}

`,
  },
  {
    name: "google-vertex-raw-json-error-coercion",
    api: "google-vertex",
    model: vertexModel(),
    context: { messages: [{ role: "user", content: "raw error", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-vertex-key" },
    sse: '{"10":"ten","2":"two","error":{"code":"4.03e2","message":"\\u0061"}}',
  },
];

function cloneEvent(event: AssistantMessageEvent): AssistantMessageEvent {
  return JSON.parse(JSON.stringify(event)) as AssistantMessageEvent;
}

async function captureRequest(input: RequestInfo | URL, init?: RequestInit): Promise<CapturedRequest> {
  const request = new Request(input, init);
  const headers: Record<string, string> = {};
  for (const name of ["authorization", "content-type", "x-fixture", "x-goog-api-key", "x-model-header"]) {
    const value = request.headers.get(name);
    if (value !== null) headers[name] = value;
  }
  return { method: request.method, url: request.url, headers, body: await request.clone().text() };
}

function fixtureResponse(definition: VertexStreamDefinition): FixtureResponse {
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

async function runVertex(
  definition: VertexDefinition,
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
    const options = { ...definition.options } as GoogleVertexOptions & SimpleStreamOptions;
    if (definition.payloadConfigPatch || definition.payloadContents !== undefined) {
      const patch = definition.payloadConfigPatch;
      options.onPayload = (params) => ({
        ...params,
        ...(definition.payloadContents !== undefined ? { contents: definition.payloadContents } : {}),
        ...(patch ? { config: { ...params.config, ...patch } } : {}),
      });
    }
    const vertexStream = definition.simple
      ? streamSimpleGoogleVertex(definition.model, definition.context, options)
      : streamGoogleVertex(definition.model, definition.context, options);
    for await (const event of vertexStream) events.push(cloneEvent(event));
  } finally {
    globalThis.fetch = originalFetch;
  }
  if (!request) throw new Error(`${definition.name}: Vertex request was not captured`);
  const terminal = events.at(-1);
  const expectsError =
    response.status >= 400 ||
    definition.name === "google-vertex-safety-finish-error" ||
    definition.name.startsWith("google-vertex-raw-json-error");
  if (!terminal || (terminal.type === "error") !== expectsError) {
    throw new Error(`${definition.name}: unexpected terminal event: ${JSON.stringify(terminal)}`);
  }
  return { request, events };
}

async function extractVertexProvider(upstreamRoot: string): Promise<VertexProviderFixture> {
  const temporaryRoot = await mkdtemp(path.join(tmpdir(), "pi-go-f2-google-vertex-provider-"));
  const packageRoot = path.join(temporaryRoot, "ai");
  try {
    await cp(path.join(upstreamRoot, "packages/ai"), packageRoot, { recursive: true });
    const providerData = path.join(packageRoot, "src/providers/data");
    await mkdir(providerData, { recursive: true });
    await writeFile(
      path.join(providerData, "google-vertex.json"),
      `${JSON.stringify({
        "vertex-fixture": {
          id: "vertex-fixture",
          name: "Fixture Vertex Model",
          api: "google-vertex",
          provider: "google-vertex",
          baseUrl: "https://{location}-aiplatform.googleapis.com/v1",
          reasoning: true,
          input: ["text"],
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
          contextWindow: 1,
          maxTokens: 1,
        },
      })}\n`,
    );
    const providerModule = (await import(
      pathToFileURL(path.join(packageRoot, "src/providers/google-vertex.ts")).href
    )) as {
      googleVertexProvider(): {
        id: string;
        name: string;
        baseUrl?: string;
        auth: {
          apiKey?: {
            name: string;
            login?(interaction: {
              prompt(input: unknown): Promise<string>;
              notify(event: unknown): void;
            }): Promise<unknown>;
            resolve(input: {
              ctx: {
                env(name: string): Promise<string | undefined>;
                fileExists(path: string): Promise<boolean>;
              };
              credential?: unknown;
            }): Promise<unknown>;
          };
        };
        getModels(): readonly { api: string }[];
      };
    };
    const provider = providerModule.googleVertexProvider();
    const auth = provider.auth.apiKey;
    if (!auth?.login) throw new Error("googleVertexProvider() did not expose API-key login");

    const notifications: unknown[] = [];
    const login = async (answers: string[]): Promise<unknown> => {
      const remaining = [...answers];
      return auth.login!({
        prompt: async () => {
          const answer = remaining.shift();
          if (answer === undefined) throw new Error("Vertex login requested an unexpected prompt");
          return answer;
        },
        notify: (event) => notifications.push(event),
      });
    };
    const apiKeyLogin = await login(["api-key", "fixture-vertex-key"]);
    const adcLogin = await login(["adc", "fixture-project", "us-central1"]);
    const serviceAccountLogin = await login([
      "service-account",
      "fixture-project",
      "us-central1",
      "/fixture/service-account.json",
    ]);

    const adcFixturePath = path.join(temporaryRoot, "application-default-credentials.json");
    await writeFile(adcFixturePath, "{}\n");
    const envAPIKeyModule = (await import(
      pathToFileURL(path.join(packageRoot, "src/env-api-keys.ts")).href
    )) as {
      findEnvKeys(provider: string, env?: Record<string, string>): string[] | undefined;
      getEnvApiKey(provider: string, env?: Record<string, string>): string | undefined;
    };
    await new Promise<void>((resolve) => setImmediate(resolve));
    const envAPIKeys = {
      apiKey: envAPIKeyModule.getEnvApiKey("google-vertex", {
        GOOGLE_CLOUD_API_KEY: "fixture-env-key",
      }),
      adc: envAPIKeyModule.getEnvApiKey("google-vertex", {
        GOOGLE_APPLICATION_CREDENTIALS: adcFixturePath,
        GOOGLE_CLOUD_PROJECT: "fixture-project",
        GOOGLE_CLOUD_LOCATION: "us-central1",
      }),
      missingLocation:
        envAPIKeyModule.getEnvApiKey("google-vertex", {
          GOOGLE_APPLICATION_CREDENTIALS: adcFixturePath,
          GOOGLE_CLOUD_PROJECT: "fixture-project",
        }) ?? null,
      found: envAPIKeyModule.findEnvKeys("google-vertex", {
        GOOGLE_CLOUD_API_KEY: "fixture-env-key",
      }),
    };

    const resolutionDefinitions: Array<{
      name: string;
      env: Record<string, string>;
      files: string[];
      credential?: unknown;
    }> = [
      {
        name: "stored-api-key-wins",
        env: { GOOGLE_CLOUD_API_KEY: "ambient-key" },
        files: [],
        credential: { type: "api_key", key: "stored-key" },
      },
      { name: "environment-api-key", env: { GOOGLE_CLOUD_API_KEY: "environment-key" }, files: [] },
      {
        name: "stored-service-account-adc",
        env: {},
        files: ["/fixture/service-account.json"],
        credential: serviceAccountLogin,
      },
      {
        name: "ambient-default-adc",
        env: { GOOGLE_CLOUD_PROJECT: "ambient-project", GOOGLE_CLOUD_LOCATION: "global" },
        files: ["~/.config/gcloud/application_default_credentials.json"],
      },
      {
        name: "adc-missing-location",
        env: { GOOGLE_CLOUD_PROJECT: "partial-project" },
        files: ["~/.config/gcloud/application_default_credentials.json"],
      },
      {
        name: "api-key-wins-over-adc",
        env: {
          GOOGLE_CLOUD_API_KEY: "winning-key",
          GOOGLE_CLOUD_PROJECT: "ambient-project",
          GOOGLE_CLOUD_LOCATION: "us-central1",
        },
        files: ["~/.config/gcloud/application_default_credentials.json"],
      },
    ];
    const resolutions = [];
    for (const definition of resolutionDefinitions) {
      const envLookups: string[] = [];
      const fileLookups: string[] = [];
      const result = await auth.resolve({
        credential: definition.credential,
        ctx: {
          env: async (name) => {
            envLookups.push(name);
            return definition.env[name];
          },
          fileExists: async (file) => {
            fileLookups.push(file);
            return definition.files.includes(file);
          },
        },
      });
      resolutions.push({ name: definition.name, result: result ?? null, envLookups, fileLookups });
    }

    return {
      id: provider.id,
      name: provider.name,
      baseUrl: provider.baseUrl,
      apis: [...new Set(provider.getModels().map((entry) => entry.api))].sort(),
      auth: {
        kind: "api_key",
        name: auth.name,
        login: {
          apiKey: apiKeyLogin,
          adc: adcLogin,
          serviceAccount: serviceAccountLogin,
          notifications,
        },
        envAPIKeys,
        resolutions,
      },
    };
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
}

export async function extractGoogleVertexF2(upstreamRoot: string): Promise<{
  provider: VertexProviderFixture;
  requests: unknown[];
  streams: unknown[];
}> {
  const provider = await extractVertexProvider(upstreamRoot);
  const requests = [];
  for (const definition of requestDefinitions) {
    const { request } = await runVertex(definition, {
      status: 200,
      body: minimalSSE,
      contentType: "text/event-stream",
    });
    requests.push({ ...definition, expected: request });
  }
  const streams = [];
  for (const definition of streamDefinitions) {
    const { events } = await runVertex(definition, fixtureResponse(definition));
    streams.push({ ...definition, expectedEvents: events });
  }
  return { provider, requests, streams };
}
