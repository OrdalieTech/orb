import {
  stream as streamAzure,
  type AzureOpenAIResponsesOptions,
} from "../../.upstream/packages/ai/src/api/azure-openai-responses.ts";
import {
  stream as streamMistral,
  type MistralOptions,
} from "../../.upstream/packages/ai/src/api/mistral-conversations.ts";
import type {
  AssistantMessage,
  AssistantMessageEvent,
  Context,
  Model,
  Tool,
} from "../../.upstream/packages/ai/src/types.ts";

type FixtureAPI = "mistral-conversations" | "azure-openai-responses";
type FixtureModel = Model<"mistral-conversations"> | Model<"azure-openai-responses">;
type FixtureOptions = MistralOptions | AzureOpenAIResponsesOptions;

interface Definition {
  name: string;
  api: FixtureAPI;
  model: FixtureModel;
  context: Context;
  options: FixtureOptions;
}

interface StreamDefinition extends Definition {
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

interface FixtureResponse {
  status: number;
  body: string;
  contentType: string;
}

const FIXED_NOW = 1_700_000_000_123;
const zeroCost = { input: 1_000_000, output: 2_000_000, cacheRead: 3_000_000, cacheWrite: 4_000_000 };
const zeroUsage = {
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  totalTokens: 0,
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
};

const echoTool = {
  name: "echo",
  description: "Echo structured text",
  parameters: {
    type: "object",
    properties: {
      text: { type: "string" },
      metadata: {
        type: "object",
        properties: { count: { type: "integer" } },
        required: ["count"],
        additionalProperties: false,
      },
    },
    required: ["text"],
    additionalProperties: false,
  },
} as Tool;

const mistralModel: Model<"mistral-conversations"> = {
  id: "mistral-fixture",
  name: "Mistral Fixture",
  api: "mistral-conversations",
  provider: "mistral",
  baseUrl: "https://mistral.fixture.invalid",
  reasoning: true,
  thinkingLevelMap: { off: "none", medium: "high" },
  input: ["text", "image"],
  cost: zeroCost,
  contextWindow: 128_000,
  maxTokens: 8_192,
  headers: { "x-model-header": "mistral" },
};

const mistralTextModel: Model<"mistral-conversations"> = {
  ...mistralModel,
  id: "mistral-text-fixture",
  input: ["text"],
};

const azureModel: Model<"azure-openai-responses"> = {
  id: "gpt-fixture-azure",
  name: "Azure Fixture",
  api: "azure-openai-responses",
  provider: "azure-openai-responses",
  baseUrl: "https://model-resource.openai.azure.com",
  reasoning: true,
  thinkingLevelMap: { off: "none", high: "xhigh" },
  input: ["text", "image"],
  cost: zeroCost,
  contextWindow: 128_000,
  maxTokens: 8_192,
  headers: { "x-model-header": "azure" },
};

function assistant(
  api: FixtureAPI,
  provider: string,
  model: string,
  content: AssistantMessage["content"],
): AssistantMessage {
  return {
    role: "assistant",
    content,
    api,
    provider,
    model,
    usage: zeroUsage,
    stopReason: "stop",
    timestamp: FIXED_NOW - 2,
  };
}

const requestDefinitions: Definition[] = [
  {
    name: "mistral-rich-messages-tools-cache",
    api: "mistral-conversations",
    model: mistralModel,
    context: {
      systemPrompt: "Use tools carefully.",
      messages: [
        {
          role: "user",
          content: [
            { type: "text", text: "inspect" },
            { type: "image", data: "AAEC", mimeType: "image/png" },
          ],
          timestamp: FIXED_NOW - 4,
        },
        assistant("mistral-conversations", "mistral", mistralModel.id, [
          { type: "thinking", thinking: "prior plan" },
          { type: "text", text: "prior answer" },
          { type: "toolCall", id: "Abc123XYZ", name: "echo", arguments: { text: "first" } },
        ]),
        {
          role: "toolResult",
          toolCallId: "Abc123XYZ",
          toolName: "echo",
          content: [
            { type: "text", text: "tool text" },
            { type: "image", data: "AQID", mimeType: "image/jpeg" },
          ],
          isError: true,
          timestamp: FIXED_NOW - 1,
        },
        { role: "user", content: "continue", timestamp: FIXED_NOW },
      ],
      tools: [echoTool],
    },
    options: {
      apiKey: "fixture-key",
      temperature: 0,
      maxTokens: 7,
      cacheRetention: "long",
      sessionId: "mistral-session",
      toolChoice: { type: "function", function: { name: "echo" } },
      promptMode: "reasoning",
      reasoningEffort: "high",
      headers: { "x-fixture": "mistral-rich" },
    },
  },
  {
    name: "mistral-nonvision-foreign-tool-id",
    api: "mistral-conversations",
    model: mistralTextModel,
    context: {
      messages: [
        assistant("openai-responses", "openai", "gpt-foreign", [
          { type: "toolCall", id: "call.foreign/tool", name: "echo", arguments: { text: "foreign" } },
        ]),
        {
          role: "toolResult",
          toolCallId: "call.foreign/tool",
          toolName: "echo",
          content: [{ type: "image", data: "AAEC", mimeType: "image/png" }],
          isError: false,
          timestamp: FIXED_NOW - 1,
        },
      ],
    },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
  },
  {
    name: "azure-env-config-deployment-reasoning",
    api: "azure-openai-responses",
    model: azureModel,
    context: {
      systemPrompt: "Use the Azure deployment.",
      messages: [
        assistant("azure-openai-responses", azureModel.provider, azureModel.id, [
          {
            type: "thinking",
            thinking: "prior plan",
            thinkingSignature: JSON.stringify({
              type: "reasoning",
              id: "rs_prior",
              summary: [{ type: "summary_text", text: "prior plan" }],
              encrypted_content: "encrypted-prior",
            }),
          },
          { type: "toolCall", id: "call_azure|fc_azure", name: "echo", arguments: { text: "azure" } },
        ]),
        {
          role: "toolResult",
          toolCallId: "call_azure|fc_azure",
          toolName: "echo",
          content: [{ type: "text", text: "done" }],
          isError: false,
          timestamp: FIXED_NOW - 1,
        },
        { role: "user", content: "continue", timestamp: FIXED_NOW },
      ],
      tools: [echoTool],
    },
    options: {
      apiKey: "fixture-key",
      temperature: 0,
      maxTokens: 7,
      sessionId: `${"a".repeat(64)}-tail`,
      reasoningEffort: "high",
      reasoningSummary: "detailed",
      env: {
        AZURE_OPENAI_BASE_URL: "https://fixture-resource.cognitiveservices.azure.com/openai",
        AZURE_OPENAI_API_VERSION: "2025-04-01-preview",
        AZURE_OPENAI_DEPLOYMENT_NAME_MAP: "other=unused, gpt-fixture-azure=fixture-deployment",
      },
      headers: { "x-fixture": "azure-env" },
    },
  },
  {
    name: "azure-explicit-proxy-off-reasoning",
    api: "azure-openai-responses",
    model: azureModel,
    context: { messages: [{ role: "user", content: "hello", timestamp: FIXED_NOW }] },
    options: {
      apiKey: "fixture-key",
      azureBaseUrl: "https://azure-proxy.fixture.invalid/custom/v1",
      azureApiVersion: "v1",
      azureDeploymentName: "explicit-deployment",
      cacheRetention: "none",
    },
  },
  {
    name: "azure-explicit-proxy-query-replacement",
    api: "azure-openai-responses",
    model: azureModel,
    context: { messages: [{ role: "user", content: "query", timestamp: FIXED_NOW }] },
    options: {
      apiKey: "fixture-key",
      azureBaseUrl: "https://azure-proxy.fixture.invalid/custom/v1?custom=true",
      azureApiVersion: "v1",
      cacheRetention: "none",
    },
  },
];

function encodeSSE(events: unknown[]): string {
  return `${events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join("")}data: [DONE]\n\n`;
}

function mistralTerminal(model: string): string {
  return encodeSSE([
    {
      id: "mistral-request-fixture",
      model,
      choices: [{ index: 0, delta: {}, finish_reason: "stop" }],
    },
  ]);
}

function azureTerminal(model: string): string {
  return encodeSSE([
    {
      type: "response.completed",
      sequence_number: 0,
      response: {
        id: "azure-request-fixture",
        status: "completed",
        model,
        output: [],
        usage: {
          input_tokens: 0,
          output_tokens: 0,
          total_tokens: 0,
          input_tokens_details: { cached_tokens: 0 },
          output_tokens_details: { reasoning_tokens: 0 },
        },
      },
    },
  ]);
}

const mistralRichSSE = encodeSSE([
  {
    id: "mistral-stream-fixture",
    model: mistralModel.id,
    choices: [
      {
        index: 0,
        delta: { content: [{ type: "thinking", thinking: [{ type: "text", text: "plan" }] }] },
        finish_reason: null,
      },
    ],
  },
  {
    id: "mistral-stream-fixture",
    model: mistralModel.id,
    choices: [{ index: 0, delta: { content: "hello " }, finish_reason: null }],
  },
  {
    id: "mistral-stream-fixture",
    model: mistralModel.id,
    choices: [
      {
        index: 0,
        delta: {
          tool_calls: [
            {
              id: "tool12345",
              type: "function",
              index: 0,
              function: { name: "echo", arguments: '{"text":"hello",' },
            },
          ],
        },
        finish_reason: null,
      },
    ],
  },
  {
    id: "mistral-stream-fixture",
    model: mistralModel.id,
    choices: [
      {
        index: 0,
        delta: {
          content: [{ type: "text", text: "world" }],
          tool_calls: [
            {
              id: "tool12345",
              type: "function",
              index: 0,
              function: { name: "echo", arguments: '"metadata":{"count":2}}' },
            },
          ],
        },
        finish_reason: "tool_calls",
      },
    ],
    usage: {
      prompt_tokens: 20,
      completion_tokens: 7,
      total_tokens: 27,
      prompt_tokens_details: { cached_tokens: 2 },
    },
  },
]);

const azureBackfillSSE = encodeSSE([
  { type: "response.created", sequence_number: 0, response: { id: "azure-stream-fixture" } },
  {
    type: "response.output_item.added",
    sequence_number: 1,
    output_index: 0,
    item: { type: "reasoning", id: "rs_azure", summary: [] },
  },
  {
    type: "response.reasoning_summary_text.delta",
    sequence_number: 2,
    output_index: 0,
    item_id: "rs_azure",
    summary_index: 0,
    delta: "plan",
  },
  {
    type: "response.output_item.done",
    sequence_number: 3,
    output_index: 0,
    item: { type: "reasoning", id: "rs_azure", summary: [{ type: "summary_text", text: "plan" }] },
  },
  {
    type: "response.output_item.added",
    sequence_number: 4,
    output_index: 1,
    item: { type: "message", id: "msg_azure", role: "assistant", content: [], status: "in_progress" },
  },
  {
    type: "response.output_text.delta",
    sequence_number: 5,
    output_index: 1,
    item_id: "msg_azure",
    content_index: 0,
    delta: "done",
  },
  {
    type: "response.output_item.done",
    sequence_number: 6,
    output_index: 1,
    item: {
      type: "message",
      id: "msg_azure",
      role: "assistant",
      content: [{ type: "output_text", text: "done", annotations: [] }],
      status: "completed",
    },
  },
  {
    type: "response.completed",
    sequence_number: 7,
    response: {
      id: "azure-stream-fixture",
      status: "completed",
      output: [
        {
          type: "reasoning",
          id: "rs_azure",
          summary: [{ type: "summary_text", text: "plan" }],
          encrypted_content: "azure-encrypted",
        },
      ],
      usage: {
        input_tokens: 12,
        output_tokens: 4,
        total_tokens: 16,
        input_tokens_details: { cached_tokens: 2 },
        output_tokens_details: { reasoning_tokens: 1 },
      },
    },
  },
]);

const streamDefinitions: StreamDefinition[] = [
  {
    name: "mistral-thinking-text-tool-cache-usage",
    api: "mistral-conversations",
    model: mistralModel,
    context: { messages: [{ role: "user", content: "stream", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    sse: mistralRichSSE,
  },
  {
    name: "mistral-http-403-body",
    api: "mistral-conversations",
    model: mistralModel,
    context: { messages: [{ role: "user", content: "reject", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    httpStatus: 403,
    httpBody: "denied",
    httpContentType: "text/plain",
  },
  {
    name: "mistral-http-403-empty-body",
    api: "mistral-conversations",
    model: mistralModel,
    context: { messages: [{ role: "user", content: "reject empty", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    httpStatus: 403,
    httpBody: "",
    httpContentType: "text/plain",
  },
  {
    name: "azure-reasoning-backfill-text",
    api: "azure-openai-responses",
    model: azureModel,
    context: { messages: [{ role: "user", content: "stream", timestamp: FIXED_NOW }] },
    options: {
      apiKey: "fixture-key",
      azureBaseUrl: "https://fixture-resource.openai.azure.com",
      cacheRetention: "none",
    },
    sse: azureBackfillSSE,
  },
  {
    name: "azure-http-403-body",
    api: "azure-openai-responses",
    model: azureModel,
    context: { messages: [{ role: "user", content: "reject", timestamp: FIXED_NOW }] },
    options: {
      apiKey: "fixture-key",
      azureBaseUrl: "https://fixture-resource.openai.azure.com",
      cacheRetention: "none",
    },
    httpStatus: 403,
    httpBody: "denied",
    httpContentType: "text/plain",
  },
];

function selectedHeaders(request: Request): Record<string, string> {
  const headers: Record<string, string> = {};
  for (const name of [
    "accept",
    "api-key",
    "authorization",
    "content-type",
    "x-affinity",
    "x-fixture",
    "x-model-header",
  ]) {
    const value = request.headers.get(name);
    if (value !== null) headers[name] = value;
  }
  return headers;
}

async function captureRequest(input: RequestInfo | URL, init?: RequestInit): Promise<CapturedRequest> {
  const request = new Request(input, init);
  return {
    method: request.method,
    url: request.url,
    headers: selectedHeaders(request),
    body: await request.clone().text(),
  };
}

function cloneEvent(event: AssistantMessageEvent): AssistantMessageEvent {
  return JSON.parse(JSON.stringify(event)) as AssistantMessageEvent;
}

async function runUpstream(
  definition: Definition,
  response: FixtureResponse,
): Promise<{ request: CapturedRequest; events: AssistantMessageEvent[] }> {
  let captured: CapturedRequest | undefined;
  globalThis.fetch = async (input, init) => {
    captured = await captureRequest(input, init);
    const body = definition.api === "mistral-conversations" && response.status < 400
      ? mistralSSEBody(response.body)
      : response.body;
    return new Response(body, {
      status: response.status,
      headers: { "content-type": response.contentType },
    });
  };
  const stream = definition.api === "mistral-conversations"
    ? streamMistral(
        definition.model as Model<"mistral-conversations">,
        definition.context,
        definition.options as MistralOptions,
      )
    : streamAzure(
        definition.model as Model<"azure-openai-responses">,
        definition.context,
        definition.options as AzureOpenAIResponsesOptions,
      );
  const events: AssistantMessageEvent[] = [];
  for await (const event of stream) events.push(cloneEvent(event));
  if (!captured) throw new Error(`${definition.name}: provider request was not captured`);
  return { request: captured, events };
}

function mistralSSEBody(body: string): ReadableStream<Uint8Array> {
  const records = body.split("\n\n").filter(Boolean).map((record) => `${record}\n\n`);
  const encoder = new TextEncoder();
  let index = 0;
  return new ReadableStream<Uint8Array>({
    async pull(controller) {
      if (index === records.length) {
        controller.close();
        return;
      }
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
      controller.enqueue(encoder.encode(records[index++]));
    },
  }, { highWaterMark: 0 });
}

function fixtureResponse(definition: StreamDefinition): FixtureResponse {
  if (definition.httpStatus !== undefined) {
    return {
      status: definition.httpStatus,
      body: definition.httpBody ?? "",
      contentType: definition.httpContentType ?? "text/plain",
    };
  }
  if (definition.sse === undefined) throw new Error(`${definition.name}: missing SSE`);
  return { status: 200, body: definition.sse, contentType: "text/event-stream" };
}

export async function extractMistralAzureF2(): Promise<{
  mistralRequests: Array<Definition & { expected: CapturedRequest }>;
  mistralStreams: Array<StreamDefinition & { expectedEvents: AssistantMessageEvent[] }>;
  azureRequests: Array<Definition & { expected: CapturedRequest }>;
  azureStreams: Array<StreamDefinition & { expectedEvents: AssistantMessageEvent[] }>;
}> {
  const requests = [];
  for (const definition of requestDefinitions) {
    const body = definition.api === "mistral-conversations"
      ? mistralTerminal(definition.model.id)
      : azureTerminal(definition.model.id);
    const { request } = await runUpstream(definition, { status: 200, body, contentType: "text/event-stream" });
    requests.push({ ...definition, expected: request });
  }
  const streams = [];
  for (const definition of streamDefinitions) {
    const { events } = await runUpstream(definition, fixtureResponse(definition));
    streams.push({ ...definition, expectedEvents: events });
  }
  return {
    mistralRequests: requests.filter((item) => item.api === "mistral-conversations"),
    mistralStreams: streams.filter((item) => item.api === "mistral-conversations"),
    azureRequests: requests.filter((item) => item.api === "azure-openai-responses"),
    azureStreams: streams.filter((item) => item.api === "azure-openai-responses"),
  };
}
