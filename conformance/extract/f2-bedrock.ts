import { createServer, type IncomingHttpHeaders } from "node:http";
import { createRequire } from "node:module";
import { cp, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import {
  stream as streamBedrock,
  type BedrockOptions,
} from "../../.upstream/packages/ai/src/api/bedrock-converse-stream.ts";
import type {
  AssistantMessage,
  AssistantMessageEvent,
  Context,
  Model,
  Tool,
} from "../../.upstream/packages/ai/src/types.ts";

const FIXED_NOW = 1_700_000_000_123;
const zeroCost = { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 };

interface BedrockDefinition {
  name: string;
  api: "bedrock-converse-stream";
  model: Model<"bedrock-converse-stream">;
  context: Context;
  options: BedrockOptions;
}

interface BedrockStreamDefinition extends BedrockDefinition {
  items: unknown[];
  status?: number;
  requestId?: string;
}

interface CapturedRequest {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string;
}

interface BedrockProviderFixture {
  id: string;
  name: string;
  baseUrl?: string;
  apis: string[];
  auth: {
    kind: "api_key";
    name: string;
    env: string[];
    login: Array<{
      name: string;
      responses: string[];
      credential: { type: "api_key"; key?: string; env?: Record<string, string> };
      prompts: unknown[];
      notifications: unknown[];
    }>;
    cases: Array<{
      name: string;
      env: Record<string, string>;
      authenticated: boolean;
      source?: string;
      apiKey?: string;
    }>;
  };
}

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

function bedrockModel(
  overrides: Partial<Model<"bedrock-converse-stream">> = {},
): Model<"bedrock-converse-stream"> {
  return {
    id: "anthropic.claude-sonnet-4-5-20250929-v1:0",
    name: "Claude Sonnet 4.5",
    api: "bedrock-converse-stream",
    provider: "amazon-bedrock",
    baseUrl: "https://bedrock-runtime.us-east-1.amazonaws.com",
    reasoning: true,
    input: ["text", "image"],
    cost: zeroCost,
    contextWindow: 200_000,
    maxTokens: 64_000,
    ...overrides,
  };
}

function assistant(
  model: Model<"bedrock-converse-stream">,
  content: AssistantMessage["content"],
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
    stopReason: "toolUse",
    timestamp: FIXED_NOW - 2,
  };
}

const textModel = bedrockModel({
  reasoning: false,
  headers: { "x-model-header": "ignored-by-bedrock" },
});
const replayModel = bedrockModel();
const profileModel = bedrockModel({
  id: "arn:aws-us-gov:bedrock:us-gov-west-1:123456789012:application-inference-profile/fixture",
  name: "Claude Sonnet 4.6 Profile",
  maxTokens: 32_000,
});

const requestDefinitions: BedrockDefinition[] = [
  {
    name: "bedrock-text-tools-short-cache",
    api: "bedrock-converse-stream",
    model: textModel,
    context: {
      systemPrompt: "You are concise.",
      messages: [{ role: "user", content: "hello <fixture>", timestamp: FIXED_NOW }],
      tools: [echoTool],
    },
    options: {
      maxTokens: 777,
      temperature: 0,
      cacheRetention: "short",
      toolChoice: "any",
      requestMetadata: { tenant: "fixture", purpose: "conformance" },
      headers: {
        "x-fixture": "bedrock-text",
        authorization: "must-not-override",
        "x-amz-fixture": "must-not-override",
      },
      env: {
        AWS_BEDROCK_SKIP_AUTH: "1",
        AWS_BEDROCK_FORCE_HTTP1: "1",
        AWS_REGION: "us-east-1",
        HTTP_PROXY: "",
        HTTPS_PROXY: "",
        ALL_PROXY: "",
        NO_PROXY: "*",
      },
    },
  },
  {
    name: "bedrock-rich-replay-long-cache-thinking",
    api: "bedrock-converse-stream",
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
          timestamp: FIXED_NOW - 5,
        },
        assistant(replayModel, [
          { type: "thinking", thinking: "signed reasoning", thinkingSignature: "sig_fixture" },
          {
            type: "toolCall",
            id: `call:${"x".repeat(70)}`,
            name: "echo",
            arguments: { text: "first", mode: "plain" },
          },
        ]),
        {
          role: "toolResult",
          toolCallId: `call:${"x".repeat(70)}`,
          toolName: "echo",
          content: [{ type: "text", text: "done" }],
          isError: false,
          timestamp: FIXED_NOW - 2,
        },
        {
          role: "toolResult",
          toolCallId: "call:error",
          toolName: "echo",
          content: [{ type: "image", data: "cGl4ZWw=", mimeType: "image/webp" }],
          isError: true,
          timestamp: FIXED_NOW - 1,
        },
        { role: "user", content: "continue", timestamp: FIXED_NOW },
      ],
      tools: [echoTool],
    },
    options: {
      maxTokens: 4096,
      cacheRetention: "long",
      reasoning: "high",
      thinkingBudgets: { high: 2048 },
      thinkingDisplay: "omitted",
      toolChoice: { type: "tool", name: "echo" },
      env: {
        AWS_BEDROCK_SKIP_AUTH: "1",
        AWS_BEDROCK_FORCE_HTTP1: "1",
        AWS_REGION: "us-east-1",
        HTTP_PROXY: "",
        HTTPS_PROXY: "",
        ALL_PROXY: "",
        NO_PROXY: "*",
      },
    },
  },
  {
    name: "bedrock-govcloud-adaptive-profile",
    api: "bedrock-converse-stream",
    model: profileModel,
    context: {
      messages: [{ role: "user", content: "think", timestamp: FIXED_NOW }],
    },
    options: {
      maxTokens: 2048,
      cacheRetention: "none",
      reasoning: "xhigh",
      thinkingDisplay: "summarized",
      env: {
        AWS_BEDROCK_SKIP_AUTH: "1",
        AWS_BEDROCK_FORCE_HTTP1: "1",
        AWS_REGION: "us-east-2",
        HTTP_PROXY: "",
        HTTPS_PROXY: "",
        ALL_PROXY: "",
        NO_PROXY: "*",
      },
    },
  },
  {
    name: "bedrock-nova-empty-inference-config",
    api: "bedrock-converse-stream",
    model: bedrockModel({
      id: "amazon.nova-micro-v1:0",
      name: "Nova Micro",
      reasoning: false,
      input: ["text"],
      maxTokens: 8192,
    }),
    context: {
      messages: [{ role: "user", content: "hello", timestamp: FIXED_NOW }],
    },
    options: {
      cacheRetention: "none",
      env: {
        AWS_BEDROCK_SKIP_AUTH: "1",
        AWS_BEDROCK_FORCE_HTTP1: "1",
        AWS_REGION: "us-east-1",
        HTTP_PROXY: "",
        HTTPS_PROXY: "",
        ALL_PROXY: "",
        NO_PROXY: "*",
      },
    },
  },
];

const streamDefinitions: BedrockStreamDefinition[] = [
  {
    name: "bedrock-thinking-text-tool-use",
    api: "bedrock-converse-stream",
    model: replayModel,
    context: {
      messages: [{ role: "user", content: "use echo", timestamp: FIXED_NOW }],
      tools: [echoTool],
    },
    options: { cacheRetention: "none", region: "us-east-1" },
    status: 200,
    requestId: "bedrock-request-fixture",
    items: [
      { messageStart: { role: "assistant" } },
      { contentBlockDelta: { contentBlockIndex: 0, delta: { reasoningContent: { text: "reason" } } } },
      { contentBlockDelta: { contentBlockIndex: 0, delta: { reasoningContent: { signature: "sig_stream" } } } },
      { contentBlockStop: { contentBlockIndex: 0 } },
      { contentBlockDelta: { contentBlockIndex: 1, delta: { text: "working" } } },
      { contentBlockStop: { contentBlockIndex: 1 } },
      {
        contentBlockStart: {
          contentBlockIndex: 2,
          start: { toolUse: { toolUseId: "toolu_stream", name: "echo" } },
        },
      },
      { contentBlockDelta: { contentBlockIndex: 2, delta: { toolUse: { input: '{"text":"hello",' } } } },
      { contentBlockDelta: { contentBlockIndex: 2, delta: { toolUse: { input: '"mode":"plain"}' } } } },
      { contentBlockStop: { contentBlockIndex: 2 } },
      { messageStop: { stopReason: "tool_use" } },
      {
        metadata: {
          usage: {
            inputTokens: 10,
            outputTokens: 7,
            cacheReadInputTokens: 3,
            cacheWriteInputTokens: 5,
            totalTokens: 17,
          },
        },
      },
    ],
  },
  {
    name: "bedrock-length",
    api: "bedrock-converse-stream",
    model: textModel,
    context: { messages: [{ role: "user", content: "long", timestamp: FIXED_NOW }] },
    options: { cacheRetention: "none", region: "us-east-1" },
    items: [
      { messageStart: { role: "assistant" } },
      { contentBlockDelta: { contentBlockIndex: 0, delta: { text: "partial" } } },
      { contentBlockStop: { contentBlockIndex: 0 } },
      { messageStop: { stopReason: "model_context_window_exceeded" } },
      { metadata: { usage: { inputTokens: 9, outputTokens: 3 } } },
    ],
  },
  {
    name: "bedrock-unknown-stop-reason",
    api: "bedrock-converse-stream",
    model: textModel,
    context: { messages: [{ role: "user", content: "guard", timestamp: FIXED_NOW }] },
    options: { cacheRetention: "none", region: "us-east-1" },
    items: [
      { messageStart: { role: "assistant" } },
      { messageStop: { stopReason: "guardrail_intervened" } },
    ],
  },
  {
    name: "bedrock-unexpected-user-role",
    api: "bedrock-converse-stream",
    model: textModel,
    context: { messages: [{ role: "user", content: "role", timestamp: FIXED_NOW }] },
    options: { cacheRetention: "none", region: "us-east-1" },
    items: [{ messageStart: { role: "user" } }],
  },
];

function cloneEvent(event: AssistantMessageEvent): AssistantMessageEvent {
  return JSON.parse(JSON.stringify(event)) as AssistantMessageEvent;
}

function selectedHeaders(headers: IncomingHttpHeaders): Record<string, string> {
  const result: Record<string, string> = {};
  for (const name of ["content-type", "x-fixture"] as const) {
    const value = headers[name];
    if (typeof value === "string") result[name] = value;
  }
  return result;
}

async function captureBedrockRequest(
  definition: BedrockDefinition,
): Promise<CapturedRequest> {
  let captured: CapturedRequest | undefined;
  const server = createServer((request, response) => {
    const chunks: Buffer[] = [];
    request.on("data", (chunk) => chunks.push(Buffer.from(chunk)));
    request.on("end", () => {
      captured = {
        method: request.method ?? "",
        url: request.url ?? "",
        headers: selectedHeaders(request.headers),
        body: Buffer.concat(chunks).toString("utf8"),
      };
      response.writeHead(400, {
        "content-type": "application/json",
        "x-amzn-errortype": "ValidationException",
      });
      response.end('{"message":"fixture capture complete"}');
    });
  });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  try {
    const address = server.address();
    if (!address || typeof address === "string") throw new Error("Bedrock fixture server has no TCP address");
    const model = { ...definition.model, baseUrl: `http://127.0.0.1:${address.port}` };
    for await (const _event of streamBedrock(model, definition.context, definition.options)) {
      // The deterministic 400 response stops the request after serialization.
    }
  } finally {
    await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
  }
  if (!captured) throw new Error(`${definition.name}: Bedrock request was not captured`);
  return captured;
}

async function runBedrockStream(
  upstreamRoot: string,
  definition: BedrockStreamDefinition,
): Promise<AssistantMessageEvent[]> {
  const require = createRequire(path.join(upstreamRoot, "package.json"));
  const sdk = require("@aws-sdk/client-bedrock-runtime") as {
    BedrockRuntimeClient: { prototype: { send: (...args: unknown[]) => Promise<unknown> } };
  };
  const originalSend = sdk.BedrockRuntimeClient.prototype.send;
  sdk.BedrockRuntimeClient.prototype.send = async () => ({
    $metadata: {
      httpStatusCode: definition.status ?? 200,
      requestId: definition.requestId,
    },
    stream: (async function* () {
      for (const item of definition.items) {
        await new Promise<void>((resolve) => setImmediate(resolve));
        yield item;
      }
    })(),
  });
  try {
    const events: AssistantMessageEvent[] = [];
    for await (const event of streamBedrock(definition.model, definition.context, definition.options)) {
      events.push(cloneEvent(event));
    }
    return events;
  } finally {
    sdk.BedrockRuntimeClient.prototype.send = originalSend;
  }
}

async function extractBedrockProvider(upstreamRoot: string): Promise<BedrockProviderFixture> {
  const temporaryRoot = await mkdtemp(path.join(tmpdir(), "pigo-f2-bedrock-provider-"));
  const packageRoot = path.join(temporaryRoot, "ai");
  try {
    await cp(path.join(upstreamRoot, "packages/ai"), packageRoot, { recursive: true });
    const providerData = path.join(packageRoot, "src/providers/data");
    await mkdir(providerData, { recursive: true });
    await writeFile(
      path.join(providerData, "amazon-bedrock.json"),
      `${JSON.stringify({ "fixture-bedrock-model": bedrockModel({ id: "fixture-bedrock-model" }) })}\n`,
    );
    const providerModule = (await import(
      pathToFileURL(path.join(packageRoot, "src/providers/amazon-bedrock.ts")).href
    )) as {
      amazonBedrockProvider(): {
        id: string;
        name: string;
        baseUrl?: string;
        auth: {
          apiKey?: {
            name: string;
            login?(interaction: {
              prompt(input: unknown): Promise<string>;
              notify(event: unknown): void;
            }): Promise<{ type: "api_key"; key?: string; env?: Record<string, string> }>;
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
    const provider = providerModule.amazonBedrockProvider();
    const apiKeyAuth = provider.auth.apiKey;
    if (!apiKeyAuth) throw new Error("amazonBedrockProvider() did not expose API-key auth");
    if (!apiKeyAuth.login) throw new Error("amazonBedrockProvider() did not expose API-key login");
    const loginDefinitions = [
      { name: "bearer-token", responses: ["bearer-token", "fixture-bearer"] },
      { name: "aws-profile", responses: ["aws-profile", "fixture-profile"] },
      { name: "credential-chain", responses: ["credential-chain", ""] },
    ];
    const login = [];
    for (const definition of loginDefinitions) {
      let responseIndex = 0;
      const prompts: unknown[] = [];
      const notifications: unknown[] = [];
      const credential = await apiKeyAuth.login({
        prompt: async (input) => {
          prompts.push(input);
          return definition.responses[responseIndex++] ?? "";
        },
        notify: (event) => notifications.push(event),
      });
      login.push({ ...definition, credential, prompts, notifications });
    }
    const definitions = [
      { name: "none", env: {} },
      { name: "bearer", env: { AWS_BEARER_TOKEN_BEDROCK: "fixture-bearer" } },
      { name: "profile", env: { AWS_PROFILE: "fixture-profile" } },
      { name: "access-keys", env: { AWS_ACCESS_KEY_ID: "fixture-access", AWS_SECRET_ACCESS_KEY: "fixture-secret" } },
      { name: "ecs-relative", env: { AWS_CONTAINER_CREDENTIALS_RELATIVE_URI: "/fixture" } },
      { name: "ecs-full", env: { AWS_CONTAINER_CREDENTIALS_FULL_URI: "http://127.0.0.1/fixture" } },
      { name: "web-identity", env: { AWS_WEB_IDENTITY_TOKEN_FILE: "/fixture/token" } },
    ];
    const cases = [];
    for (const definition of definitions) {
      const resolved = await apiKeyAuth.resolve({
        ctx: {
          env: async (name) => definition.env[name as keyof typeof definition.env],
          fileExists: async () => false,
        },
      });
      cases.push({
        ...definition,
        authenticated: resolved !== undefined,
        ...(resolved?.source ? { source: resolved.source } : {}),
        ...(resolved?.auth.apiKey ? { apiKey: resolved.auth.apiKey } : {}),
      });
    }
    return {
      id: provider.id,
      name: provider.name,
      baseUrl: provider.baseUrl,
      apis: [...new Set(provider.getModels().map((entry) => entry.api))].sort(),
      auth: {
        kind: "api_key",
        name: apiKeyAuth.name,
        env: definitions.flatMap((definition) => Object.keys(definition.env)),
        login,
        cases,
      },
    };
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
}

export async function extractBedrockF2(upstreamRoot: string): Promise<{
  provider: BedrockProviderFixture;
  requests: unknown[];
  streams: unknown[];
}> {
  const provider = await extractBedrockProvider(upstreamRoot);
  const requests = [];
  for (const definition of requestDefinitions) {
    requests.push({ ...definition, expected: await captureBedrockRequest(definition) });
  }
  const streams = [];
  for (const definition of streamDefinitions) {
    streams.push({ ...definition, expectedEvents: await runBedrockStream(upstreamRoot, definition) });
  }
  return { provider, requests, streams };
}
