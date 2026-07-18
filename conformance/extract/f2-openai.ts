import { execFile } from "node:child_process";
import { cp, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { promisify } from "node:util";

import {
  stream as streamOpenAICompletions,
  type OpenAICompletionsOptions,
} from "../../.upstream/packages/ai/src/api/openai-completions.ts";
import {
  stream as streamOpenAIResponses,
  type OpenAIResponsesOptions,
} from "../../.upstream/packages/ai/src/api/openai-responses.ts";
import type {
  AssistantMessage,
  AssistantMessageEvent,
  Context,
  Model,
  Tool,
} from "../../.upstream/packages/ai/src/types.ts";
import { extractAuthStorageFixture } from "./f2-auth.ts";
import { extractAnthropicF2 } from "./f2-anthropic.ts";
import { extractGoogleF2 } from "./f2-google.ts";
import { extractGoogleVertexF2 } from "./f2-google-vertex.ts";
import { extractMistralAzureF2 } from "./f2-mistral-azure.ts";

type OpenAIAPI = "openai-responses" | "openai-completions";
type OpenAIModel = Model<"openai-responses"> | Model<"openai-completions">;
type SerializableOptions = OpenAIResponsesOptions | OpenAICompletionsOptions;

const execFileAsync = promisify(execFile);

interface RequestDefinition {
  name: string;
  api: OpenAIAPI;
  model: OpenAIModel;
  context: Context;
  options: SerializableOptions;
}

interface StreamDefinition extends RequestDefinition {
  sse?: string;
  httpStatus?: number;
  httpBody?: string;
  httpContentType?: string;
}

interface FixtureResponse {
  status: number;
  body: string;
  contentType: string;
}

interface CapturedRequest {
  method: string;
  url: string;
  headers: Record<string, string>;
  body: string;
}

interface ProviderFixture {
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

const FIXED_NOW = 1_700_000_000_123;
const SELECTED_HEADERS = [
  "authorization",
  "content-type",
  "session_id",
  "x-client-request-id",
  "x-fixture",
  "x-model-header",
  "x-session-affinity",
  "x-session-id",
] as const;

const zeroUsage = {
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  totalTokens: 0,
  cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
};

const zeroCost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };

const echoTool = {
  name: "echo",
  description: "Echo structured text",
  parameters: {
    type: "object",
    properties: {
      text: { type: "string" },
      mode: { type: "string", enum: ["plain", "loud"] },
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

const lateTool = {
  name: "late_tool",
  description: "A tool loaded after a tool result",
  parameters: {
    type: "object",
    properties: { value: { type: "boolean" } },
    required: ["value"],
    additionalProperties: false,
  },
} as Tool;

function model<TAPI extends OpenAIAPI>(
  api: TAPI,
  overrides: Partial<Model<TAPI>> = {},
): Model<TAPI> {
  return {
    id: api === "openai-responses" ? "gpt-fixture-responses" : "gpt-fixture-completions",
    name: "OpenAI Fixture Model",
    api,
    provider: "openai",
    baseUrl: "https://api.openai.com/v1",
    reasoning: false,
    input: ["text", "image"],
    cost: zeroCost,
    contextWindow: 128_000,
    maxTokens: 8_192,
    ...overrides,
  };
}

function assistant(
  api: OpenAIAPI,
  modelId: string,
  content: AssistantMessage["content"],
  overrides: Partial<AssistantMessage> = {},
): AssistantMessage {
  return {
    role: "assistant",
    content,
    api,
    provider: "openai",
    model: modelId,
    usage: zeroUsage,
    stopReason: "stop",
    timestamp: FIXED_NOW - 2,
    ...overrides,
  };
}

function toolResult(
  toolCallId: string,
  content: Array<
    | { type: "text"; text: string }
    | { type: "image"; data: string; mimeType: string }
  >,
  addedToolNames?: string[],
) {
  return {
    role: "toolResult" as const,
    toolCallId,
    toolName: "echo",
    content,
    ...(addedToolNames ? { addedToolNames } : {}),
    isError: false,
    timestamp: FIXED_NOW - 1,
  };
}

const longSessionId = `${"s".repeat(64)}-tail`;

const responsesTextModel = model("openai-responses", {
  reasoning: true,
  thinkingLevelMap: { off: "none", high: "high" },
  headers: { "x-model-header": "responses-text" },
});

const responsesToolsModel = model("openai-responses", {
  compat: { supportsToolSearch: true },
});

const responsesImagesModel = model("openai-responses", {
  provider: "proxy-responses",
  baseUrl: "https://responses.fixture.invalid/custom/v1",
  reasoning: true,
  compat: { supportsDeveloperRole: false },
});

const responsesThinkingModel = model("openai-responses", {
  provider: "openrouter",
  baseUrl: "https://openrouter.fixture.invalid/api/v1",
  reasoning: true,
  thinkingLevelMap: { off: "none", high: "xhigh" },
  compat: { sessionAffinityFormat: "openrouter", supportsLongCacheRetention: false },
});

const completionsTextModel = model("openai-completions", {
  reasoning: true,
  thinkingLevelMap: { off: "none", high: "high" },
  headers: { "x-model-header": "completions-text" },
});

const completionsToolsModel = model("openai-completions", {
  provider: "compat-proxy",
  baseUrl: "https://completions.fixture.invalid/custom/v1",
  reasoning: true,
  compat: {
    supportsStore: false,
    supportsDeveloperRole: false,
    supportsReasoningEffort: false,
    supportsUsageInStreaming: false,
    maxTokensField: "max_tokens",
    requiresToolResultName: true,
    requiresAssistantAfterToolResult: true,
    requiresReasoningContentOnAssistantMessages: true,
    supportsStrictMode: false,
    sendSessionAffinityHeaders: true,
    sessionAffinityFormat: "openai-nosession",
    supportsLongCacheRetention: false,
  },
});

const completionsImagesModel = model("openai-completions");

const completionsThinkingModel = model("openai-completions", {
  provider: "openrouter",
  baseUrl: "https://openrouter.fixture.invalid/api/v1",
  reasoning: true,
  thinkingLevelMap: { off: "none", high: "xhigh" },
  compat: {
    supportsDeveloperRole: true,
    thinkingFormat: "openrouter",
    cacheControlFormat: "anthropic",
    sendSessionAffinityHeaders: true,
    sessionAffinityFormat: "openrouter",
    supportsLongCacheRetention: true,
    openRouterRouting: { allow_fallbacks: false, order: ["fixture-primary"] },
    vercelGatewayRouting: { only: ["fixture-provider"] },
  },
});

const completionsChatTemplateModel = model("openai-completions", {
  id: "chat-template-fixture-model",
  provider: "chat-template-proxy",
  baseUrl: "https://chat-template.fixture.invalid/v1",
  reasoning: true,
  thinkingLevelMap: { off: "none", xhigh: "max" },
  compat: {
    supportsStore: false,
    supportsDeveloperRole: false,
    supportsReasoningEffort: false,
    thinkingFormat: "chat-template",
    chatTemplateKwargs: {
      zeta_mode: "fixture",
      preserve_thinking: true,
      reasoning_effort: { $var: "thinking.effort", omitWhenOff: true },
      alpha_enabled: { $var: "thinking.enabled" },
    },
  },
});

const requestDefinitions: RequestDefinition[] = [
  {
    name: "responses-text-default-cache",
    api: "openai-responses",
    model: responsesTextModel,
    context: {
      systemPrompt: "You are concise.",
      messages: [{ role: "user", content: "hello <fixture>", timestamp: FIXED_NOW - 3 }],
    },
    options: {
      apiKey: "fixture-key",
      temperature: 0,
      maxTokens: 7,
      cacheRetention: "long",
      sessionId: longSessionId,
      headers: { "x-fixture": "responses-text" },
    },
  },
  {
    name: "responses-tools-replay-deferred",
    api: "openai-responses",
    model: responsesToolsModel,
    context: {
      systemPrompt: "Use tools.",
      messages: [
        { role: "user", content: "echo once", timestamp: FIXED_NOW - 5 },
        assistant("openai-responses", responsesToolsModel.id, [
          {
            type: "toolCall",
            id: "call_echo|fc_echo",
            name: "echo",
            arguments: { text: "first", mode: "plain", metadata: { count: 1 } },
          },
        ]),
        toolResult("call_echo|fc_echo", [], ["late_tool"]),
        { role: "user", content: "continue", timestamp: FIXED_NOW },
      ],
      tools: [echoTool, lateTool],
    },
    options: {
      apiKey: "fixture-key",
      cacheRetention: "none",
      toolChoice: "required",
    },
  },
  {
    name: "responses-images-header-auth-base-url",
    api: "openai-responses",
    model: responsesImagesModel,
    context: {
      systemPrompt: "Inspect images.",
      messages: [
        assistant("openai-responses", responsesImagesModel.id, [
          { type: "toolCall", id: "call_image", name: "echo", arguments: { text: "image" } },
        ], { provider: responsesImagesModel.provider }),
        toolResult("call_image", [
          { type: "text", text: "tool caption" },
          { type: "image", data: "AAEC", mimeType: "image/png" },
        ]),
        {
          role: "user",
          content: [
            { type: "text", text: "user image" },
            { type: "image", data: "AQID", mimeType: "image/jpeg" },
          ],
          timestamp: FIXED_NOW,
        },
      ],
    },
    options: {
      cacheRetention: "none",
      sessionId: "must-not-be-sent",
      headers: { authorization: "Bearer fixture-proxy-token", "x-fixture": "responses-images" },
    },
  },
  {
    name: "responses-thinking-replay-openrouter-affinity",
    api: "openai-responses",
    model: responsesThinkingModel,
    context: {
      systemPrompt: "Think, then answer.",
      messages: [
        assistant(
          "openai-responses",
          responsesThinkingModel.id,
          [
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
            {
              type: "text",
              text: "prior answer",
              textSignature: JSON.stringify({ v: 1, id: "msg_prior", phase: "commentary" }),
            },
          ],
          { provider: responsesThinkingModel.provider },
        ),
        { role: "user", content: "continue reasoning", timestamp: FIXED_NOW },
      ],
    },
    options: {
      apiKey: "fixture-key",
      cacheRetention: "long",
      sessionId: "responses-openrouter-session",
      reasoningEffort: "high",
      reasoningSummary: "detailed",
      serviceTier: "flex",
    },
  },
  {
    name: "completions-text-default-cache",
    api: "openai-completions",
    model: completionsTextModel,
    context: {
      systemPrompt: "You are concise.",
      messages: [{ role: "user", content: "hello <fixture>", timestamp: FIXED_NOW }],
    },
    options: {
      apiKey: "fixture-key",
      temperature: 0,
      maxTokens: 7,
      cacheRetention: "long",
      sessionId: longSessionId,
      headers: { "x-fixture": "completions-text" },
    },
  },
  {
    name: "completions-tools-conservative-compat",
    api: "openai-completions",
    model: completionsToolsModel,
    context: {
      systemPrompt: "Use the echo tool.",
      messages: [
        assistant(
          "openai-completions",
          completionsToolsModel.id,
          [
            {
              type: "toolCall",
              id: "call_compat",
              name: "echo",
              arguments: { text: "compat" },
              thoughtSignature: JSON.stringify({
                type: "reasoning.encrypted",
                id: "call_compat",
                data: "encrypted-compat",
              }),
            },
          ],
          { provider: completionsToolsModel.provider },
        ),
        toolResult("call_compat", [{ type: "text", text: "compat result" }]),
        { role: "user", content: "continue", timestamp: FIXED_NOW },
      ],
      tools: [echoTool],
    },
    options: {
      apiKey: "fixture-key",
      maxTokens: 9,
      cacheRetention: "short",
      sessionId: "completions-compat-session",
      reasoningEffort: "high",
      toolChoice: "required",
    },
  },
  {
    name: "completions-images-tool-results",
    api: "openai-completions",
    model: completionsImagesModel,
    context: {
      messages: [
        assistant("openai-completions", completionsImagesModel.id, [
          { type: "toolCall", id: "call_image_1", name: "echo", arguments: { text: "one" } },
          { type: "toolCall", id: "call_image_2", name: "echo", arguments: { text: "two" } },
        ]),
        toolResult("call_image_1", [
          { type: "text", text: "first caption" },
          { type: "image", data: "AAEC", mimeType: "image/png" },
        ]),
        toolResult("call_image_2", [{ type: "image", data: "AQID", mimeType: "image/jpeg" }]),
        {
          role: "user",
          content: [
            { type: "text", text: "also inspect" },
            { type: "image", data: "BAUG", mimeType: "image/webp" },
          ],
          timestamp: FIXED_NOW,
        },
      ],
    },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
  },
  {
    name: "completions-thinking-replay-routing-cache",
    api: "openai-completions",
    model: completionsThinkingModel,
    context: {
      systemPrompt: "Think carefully.",
      messages: [
        assistant(
          "openai-completions",
          completionsThinkingModel.id,
          [
            { type: "thinking", thinking: "prior thought", thinkingSignature: "reasoning_content" },
            { type: "text", text: "prior text" },
          ],
          { provider: completionsThinkingModel.provider },
        ),
        { role: "user", content: "new question", timestamp: FIXED_NOW },
      ],
      tools: [echoTool],
    },
    options: {
      apiKey: "fixture-key",
      cacheRetention: "long",
      sessionId: "completions-openrouter-session",
      reasoningEffort: "high",
    },
  },
  {
    name: "completions-chat-template-kwargs-order",
    api: "openai-completions",
    model: completionsChatTemplateModel,
    context: {
      systemPrompt: "Use the configured chat template.",
      messages: [{ role: "user", content: "map the effort", timestamp: FIXED_NOW }],
    },
    options: {
      apiKey: "fixture-key",
      cacheRetention: "none",
      reasoningEffort: "xhigh",
    },
  },
];

function encodeSSE(events: unknown[]): string {
  return `${events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join("")}data: [DONE]\n\n`;
}

function requestTerminalSSE(api: OpenAIAPI, modelId: string): string {
  if (api === "openai-responses") {
    return encodeSSE([
      {
        type: "response.completed",
        sequence_number: 0,
        response: {
          id: "resp_request_fixture",
          status: "completed",
          model: modelId,
          output: [],
          usage: {
            input_tokens: 1,
            output_tokens: 1,
            total_tokens: 2,
            input_tokens_details: { cached_tokens: 0 },
            output_tokens_details: { reasoning_tokens: 0 },
          },
        },
      },
    ]);
  }
  return encodeSSE([
    {
      id: "chatcmpl_request_fixture",
      object: "chat.completion.chunk",
      created: 0,
      model: modelId,
      choices: [{ index: 0, delta: {}, finish_reason: "stop" }],
    },
  ]);
}

const streamCost = {
  input: 1_000_000,
  output: 2_000_000,
  cacheRead: 3_000_000,
  cacheWrite: 4_000_000,
};

const responsesStreamModel = model("openai-responses", {
  id: "gpt-stream-responses",
  reasoning: true,
  cost: streamCost,
});

const completionsStreamModel = model("openai-completions", {
  id: "gpt-stream-completions",
  reasoning: true,
  cost: streamCost,
});

const responsesRichSSE = encodeSSE([
  { type: "response.created", sequence_number: 0, response: { id: "resp_f2_rich" } },
  {
    type: "response.output_item.added",
    sequence_number: 1,
    output_index: 0,
    item: { type: "reasoning", id: "rs_f2", summary: [] },
  },
  {
    type: "response.reasoning_summary_text.delta",
    sequence_number: 2,
    output_index: 0,
    item_id: "rs_f2",
    summary_index: 0,
    delta: "plan",
  },
  {
    type: "response.output_item.done",
    sequence_number: 3,
    output_index: 0,
    item: {
      type: "reasoning",
      id: "rs_f2",
      summary: [{ type: "summary_text", text: "plan" }],
      encrypted_content: "encrypted-f2",
    },
  },
  {
    type: "response.output_item.added",
    sequence_number: 4,
    output_index: 1,
    item: {
      type: "message",
      id: "msg_f2",
      role: "assistant",
      content: [],
      status: "in_progress",
    },
  },
  {
    type: "response.output_text.delta",
    sequence_number: 5,
    output_index: 1,
    item_id: "msg_f2",
    content_index: 0,
    delta: "hel",
  },
  {
    type: "response.output_text.delta",
    sequence_number: 6,
    output_index: 1,
    item_id: "msg_f2",
    content_index: 0,
    delta: "lo",
  },
  {
    type: "response.output_item.done",
    sequence_number: 7,
    output_index: 1,
    item: {
      type: "message",
      id: "msg_f2",
      role: "assistant",
      content: [{ type: "output_text", text: "hello", annotations: [] }],
      status: "completed",
      phase: "final_answer",
    },
  },
  {
    type: "response.output_item.added",
    sequence_number: 8,
    output_index: 2,
    item: {
      type: "function_call",
      id: "fc_f2",
      call_id: "call_f2",
      name: "echo",
      arguments: "",
    },
  },
  {
    type: "response.function_call_arguments.delta",
    sequence_number: 9,
    output_index: 2,
    item_id: "fc_f2",
    delta: '{"text":"hello","mode":"plain",',
  },
  {
    type: "response.function_call_arguments.delta",
    sequence_number: 10,
    output_index: 2,
    item_id: "fc_f2",
    delta: '"metadata":{"count":2}}',
  },
  {
    type: "response.function_call_arguments.done",
    sequence_number: 11,
    output_index: 2,
    item_id: "fc_f2",
    arguments: '{"text":"hello","mode":"plain","metadata":{"count":2}}',
  },
  {
    type: "response.output_item.done",
    sequence_number: 12,
    output_index: 2,
    item: {
      type: "function_call",
      id: "fc_f2",
      call_id: "call_f2",
      name: "echo",
      arguments: '{"text":"hello","mode":"plain","metadata":{"count":2}}',
    },
  },
  {
    type: "response.completed",
    sequence_number: 13,
    response: {
      id: "resp_f2_rich",
      status: "completed",
      output: [
        {
          type: "reasoning",
          id: "rs_f2",
          summary: [{ type: "summary_text", text: "plan" }],
          encrypted_content: "encrypted-f2",
        },
      ],
      usage: {
        input_tokens: 20,
        output_tokens: 7,
        total_tokens: 27,
        input_tokens_details: { cached_tokens: 2, cache_write_tokens: 3 },
        output_tokens_details: { reasoning_tokens: 4 },
      },
    },
  },
]);

const completionsRichSSE = encodeSSE([
  {
    id: "chatcmpl_f2_rich",
    object: "chat.completion.chunk",
    created: 0,
    model: "routed-fixture-model",
    choices: [
      {
        index: 0,
        delta: {
          reasoning_details: [
            { type: "reasoning.encrypted", id: "call_f2", data: "encrypted-tool-f2" },
          ],
        },
        finish_reason: null,
      },
    ],
  },
  {
    id: "chatcmpl_f2_rich",
    object: "chat.completion.chunk",
    created: 0,
    model: "routed-fixture-model",
    choices: [{ index: 0, delta: { reasoning_content: "plan " }, finish_reason: null }],
  },
  {
    id: "chatcmpl_f2_rich",
    object: "chat.completion.chunk",
    created: 0,
    model: "routed-fixture-model",
    choices: [{ index: 0, delta: { content: "hel" }, finish_reason: null }],
  },
  {
    id: "chatcmpl_f2_rich",
    object: "chat.completion.chunk",
    created: 0,
    model: "routed-fixture-model",
    choices: [
      {
        index: 0,
        delta: {
          tool_calls: [
            {
              index: 0,
              id: "call_f2",
              type: "function",
              function: { name: "echo", arguments: '{"text":"hello","mode":"plain",' },
            },
          ],
        },
        finish_reason: null,
      },
    ],
  },
  {
    id: "chatcmpl_f2_rich",
    object: "chat.completion.chunk",
    created: 0,
    model: "routed-fixture-model",
    choices: [
      {
        index: 0,
        delta: {
          content: "lo",
          tool_calls: [{ index: 0, function: { arguments: '"metadata":{"count":2}}' } }],
        },
        finish_reason: null,
      },
    ],
  },
  {
    id: "chatcmpl_f2_rich",
    object: "chat.completion.chunk",
    created: 0,
    model: "routed-fixture-model",
    choices: [{ index: 0, delta: {}, finish_reason: "tool_calls" }],
  },
  {
    id: "chatcmpl_f2_rich",
    object: "chat.completion.chunk",
    created: 0,
    model: "routed-fixture-model",
    choices: [],
    usage: {
      prompt_tokens: 20,
      completion_tokens: 7,
      prompt_tokens_details: { cached_tokens: 2, cache_write_tokens: 3 },
      completion_tokens_details: { reasoning_tokens: 4 },
    },
  },
]);

const responsesTextStopSSE = encodeSSE([
  { type: "response.created", sequence_number: 0, response: { id: "resp_f2_text_stop" } },
  {
    type: "response.output_item.added",
    sequence_number: 1,
    output_index: 0,
    item: {
      type: "message",
      id: "msg_f2_text_stop",
      role: "assistant",
      content: [],
      status: "in_progress",
    },
  },
  {
    type: "response.output_text.delta",
    sequence_number: 2,
    output_index: 0,
    item_id: "msg_f2_text_stop",
    content_index: 0,
    delta: "plain ",
  },
  {
    type: "response.output_text.delta",
    sequence_number: 3,
    output_index: 0,
    item_id: "msg_f2_text_stop",
    content_index: 0,
    delta: "text",
  },
  {
    type: "response.output_item.done",
    sequence_number: 4,
    output_index: 0,
    item: {
      type: "message",
      id: "msg_f2_text_stop",
      role: "assistant",
      content: [{ type: "output_text", text: "plain text", annotations: [] }],
      status: "completed",
      phase: "final_answer",
    },
  },
  {
    type: "response.completed",
    sequence_number: 5,
    response: {
      id: "resp_f2_text_stop",
      status: "completed",
      output: [],
      usage: {
        input_tokens: 4,
        output_tokens: 2,
        total_tokens: 6,
        input_tokens_details: { cached_tokens: 0 },
        output_tokens_details: { reasoning_tokens: 0 },
      },
    },
  },
]);

const completionsTextStopSSE = encodeSSE([
  {
    id: "chatcmpl_f2_text_stop",
    object: "chat.completion.chunk",
    created: 0,
    model: completionsStreamModel.id,
    choices: [{ index: 0, delta: { role: "assistant", content: "plain " }, finish_reason: null }],
  },
  {
    id: "chatcmpl_f2_text_stop",
    object: "chat.completion.chunk",
    created: 0,
    model: completionsStreamModel.id,
    choices: [{ index: 0, delta: { content: "text" }, finish_reason: "stop" }],
    usage: {
      prompt_tokens: 4,
      completion_tokens: 2,
      prompt_tokens_details: { cached_tokens: 0 },
      completion_tokens_details: { reasoning_tokens: 0 },
    },
  },
]);

const streamDefinitions: StreamDefinition[] = [
  {
    name: "responses-text-stop",
    api: "openai-responses",
    model: responsesStreamModel,
    context: { messages: [{ role: "user", content: "stream text", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    sse: responsesTextStopSSE,
  },
  {
    name: "responses-rich-tool-use",
    api: "openai-responses",
    model: responsesStreamModel,
    context: { messages: [{ role: "user", content: "stream", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    sse: responsesRichSSE,
  },
  {
    name: "responses-incomplete-length",
    api: "openai-responses",
    model: responsesStreamModel,
    context: { messages: [{ role: "user", content: "stream", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    sse: encodeSSE([
      {
        type: "response.incomplete",
        sequence_number: 0,
        response: {
          id: "resp_f2_incomplete",
          status: "incomplete",
          usage: {
            input_tokens: 30,
            output_tokens: 12,
            total_tokens: 42,
            input_tokens_details: { cached_tokens: 5 },
            output_tokens_details: { reasoning_tokens: 2 },
          },
        },
      },
    ]),
  },
  {
    name: "responses-http-403-body",
    api: "openai-responses",
    model: responsesStreamModel,
    context: { messages: [{ role: "user", content: "reject", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    httpStatus: 403,
    httpBody: "denied",
    httpContentType: "text/plain",
  },
  {
    name: "responses-http-403-empty-body",
    api: "openai-responses",
    model: responsesStreamModel,
    context: { messages: [{ role: "user", content: "reject empty", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    httpStatus: 403,
    httpBody: "",
    httpContentType: "text/plain",
  },
  {
    name: "completions-text-stop",
    api: "openai-completions",
    model: completionsStreamModel,
    context: { messages: [{ role: "user", content: "stream text", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    sse: completionsTextStopSSE,
  },
  {
    name: "completions-rich-tool-use",
    api: "openai-completions",
    model: completionsStreamModel,
    context: { messages: [{ role: "user", content: "stream", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    sse: completionsRichSSE,
  },
  {
    name: "completions-length",
    api: "openai-completions",
    model: completionsStreamModel,
    context: { messages: [{ role: "user", content: "stream", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    sse: encodeSSE([
      {
        id: "chatcmpl_f2_length",
        object: "chat.completion.chunk",
        created: 0,
        model: completionsStreamModel.id,
        choices: [{ index: 0, delta: { content: "partial" }, finish_reason: "length" }],
        usage: {
          prompt_tokens: 9,
          completion_tokens: 3,
          prompt_tokens_details: { cached_tokens: 1 },
          completion_tokens_details: { reasoning_tokens: 0 },
        },
      },
    ]),
  },
  {
    name: "completions-http-403-body",
    api: "openai-completions",
    model: completionsStreamModel,
    context: { messages: [{ role: "user", content: "reject", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    httpStatus: 403,
    httpBody: "denied",
    httpContentType: "text/plain",
  },
  {
    name: "completions-http-403-empty-body",
    api: "openai-completions",
    model: completionsStreamModel,
    context: { messages: [{ role: "user", content: "reject empty", timestamp: FIXED_NOW }] },
    options: { apiKey: "fixture-key", cacheRetention: "none" },
    httpStatus: 403,
    httpBody: "",
    httpContentType: "text/plain",
  },
];

function cloneEvent(event: AssistantMessageEvent): AssistantMessageEvent {
  return JSON.parse(JSON.stringify(event)) as AssistantMessageEvent;
}

async function capturedRequest(input: RequestInfo | URL, init?: RequestInit): Promise<CapturedRequest> {
  const request = new Request(input, init);
  const headers: Record<string, string> = {};
  for (const name of SELECTED_HEADERS) {
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

function streamFixtureResponse(definition: StreamDefinition): FixtureResponse {
  if (definition.httpStatus !== undefined) {
    return {
      status: definition.httpStatus,
      body: definition.httpBody ?? "",
      contentType: definition.httpContentType ?? "text/plain",
    };
  }
  if (definition.sse === undefined) {
    throw new Error(`${definition.name}: stream fixture has neither SSE nor an HTTP response`);
  }
  return { status: 200, body: definition.sse, contentType: "text/event-stream" };
}

async function runUpstream(
  definition: RequestDefinition,
  fixtureResponse: FixtureResponse,
): Promise<{ request: CapturedRequest; events: AssistantMessageEvent[] }> {
  let request: CapturedRequest | undefined;
  globalThis.fetch = async (input, init) => {
    request = await capturedRequest(input, init);
    return new Response(fixtureResponse.body, {
      status: fixtureResponse.status,
      headers: { "content-type": fixtureResponse.contentType },
    });
  };

  const eventStream =
    definition.api === "openai-responses"
      ? streamOpenAIResponses(
          definition.model as Model<"openai-responses">,
          definition.context,
          definition.options as OpenAIResponsesOptions,
        )
      : streamOpenAICompletions(
          definition.model as Model<"openai-completions">,
          definition.context,
          definition.options as OpenAICompletionsOptions,
        );
  const events: AssistantMessageEvent[] = [];
  for await (const event of eventStream) events.push(cloneEvent(event));

  const terminal = events.at(-1);
  if (!request) {
    throw new Error(
      `${definition.name}: upstream adapter did not issue a request: ${JSON.stringify(terminal)}`,
    );
  }
  const expectsError = fixtureResponse.status >= 400;
  if (!terminal || (terminal.type === "error") !== expectsError) {
    throw new Error(
      `${definition.name}: upstream adapter terminal event did not match HTTP ${fixtureResponse.status}: ${JSON.stringify(terminal)}`,
    );
  }
  return { request, events };
}

async function extractOpenAIProvider(upstreamRoot: string): Promise<ProviderFixture> {
  // Provider catalogs are generated and gitignored upstream. Materialize the pinned
  // offline baseline in a temporary copy so extraction never dirties .upstream and
  // does not let a changing remote model catalog alter conformance goldens.
  const temporaryRoot = await mkdtemp(path.join(tmpdir(), "pi-go-f2-openai-provider-"));
  const packageRoot = path.join(temporaryRoot, "ai");
  try {
    await cp(path.join(upstreamRoot, "packages/ai"), packageRoot, { recursive: true });
    const disableNetwork = path.join(temporaryRoot, "disable-network.mjs");
    await writeFile(
      disableNetwork,
      'globalThis.fetch = async () => { throw new Error("network disabled for deterministic F2 provider extraction"); };\n',
    );
    await execFileAsync(
      process.execPath,
      [
        "--import",
        pathToFileURL(disableNetwork).href,
        path.join(packageRoot, "scripts/generate-models.ts"),
      ],
      { cwd: packageRoot, maxBuffer: 16 * 1024 * 1024 },
    );

    const providerModule = (await import(
      pathToFileURL(path.join(packageRoot, "src/providers/openai.ts")).href
    )) as {
      openaiProvider(): {
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
            }): Promise<
              | { auth: { apiKey?: string }; source?: string }
              | undefined
            >;
          };
        };
        getModels(): readonly { api: string }[];
      };
    };
    const provider = providerModule.openaiProvider();
    const apiKeyAuth = provider.auth.apiKey;
    if (!apiKeyAuth) throw new Error("openaiProvider() did not expose API-key auth");

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
    if (unresolved !== undefined) {
      throw new Error("openaiProvider() resolved auth without a credential or environment value");
    }
    const resolved = await apiKeyAuth.resolve({
      ctx: {
        env: async () => "fixture-openai-api-key",
        fileExists: async () => false,
      },
    });
    if (!resolved?.auth.apiKey) throw new Error("openaiProvider() did not resolve its environment API key");

    const apis = [...new Set(provider.getModels().map((entry) => entry.api))].sort();
    if (apis.length === 0) throw new Error("openaiProvider() exposed no model API shapes");
    return {
      id: provider.id,
      name: provider.name,
      baseUrl: provider.baseUrl,
      apis,
      auth: {
        kind: "api_key",
        name: apiKeyAuth.name,
        env,
        resolved: resolved.auth,
        source: resolved.source,
      },
    };
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
}

export async function generateF2(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const originalFetch = globalThis.fetch;
  const originalNow = Date.now;
  Date.now = () => FIXED_NOW;
  try {
    const provider = await extractOpenAIProvider(upstreamRoot);
    const anthropic = await extractAnthropicF2(upstreamRoot);
    const google = await extractGoogleF2(upstreamRoot);
    const googleVertex = await extractGoogleVertexF2(upstreamRoot);
    const mistralAzure = await extractMistralAzureF2();
    const authStorage = await extractAuthStorageFixture();
    const requests = [];
    for (const definition of requestDefinitions) {
      const { request } = await runUpstream(
        definition,
        {
          status: 200,
          body: requestTerminalSSE(definition.api, definition.model.id),
          contentType: "text/event-stream",
        },
      );
      requests.push({ ...definition, expected: request });
    }

    const streams = [];
    for (const definition of streamDefinitions) {
      const { events } = await runUpstream(definition, streamFixtureResponse(definition));
      streams.push({ ...definition, expectedEvents: events });
    }

    const familyDir = path.join(outputRoot, "F2");
    await mkdir(familyDir, { recursive: true });
    const manifest = {
      family: "F2",
      upstreamCommit,
      generator: "conformance/extract/f2-openai.ts",
      source:
        "packages/ai/src/api/openai-responses.ts + packages/ai/src/api/openai-responses-shared.ts + packages/ai/src/api/openai-completions.ts + packages/ai/src/api/openai-prompt-cache.ts + packages/ai/src/api/anthropic-messages.ts + packages/ai/src/api/google-generative-ai.ts + packages/ai/src/api/google-vertex.ts + packages/ai/src/api/google-shared.ts + packages/ai/src/api/mistral-conversations.ts + packages/ai/src/api/azure-openai-responses.ts + packages/ai/src/utils/deferred-tools.ts + packages/ai/src/api/transform-messages.ts + packages/ai/src/providers/openai.ts + packages/ai/src/providers/anthropic.ts + packages/ai/src/providers/google.ts + packages/ai/src/providers/google-vertex.ts + packages/ai/src/env-api-keys.ts + packages/ai/src/auth/helpers.ts + packages/ai/src/auth/oauth/oauth-page.ts + packages/ai/src/models.ts + packages/ai/scripts/generate-models.ts + packages/coding-agent/src/core/auth-storage.ts + packages/coding-agent/src/core/resolve-config-value.ts + packages/coding-agent/src/migrations.ts",
      files: [
        "provider.json",
        "anthropic-provider.json",
        "google-provider.json",
        "google-vertex-provider.json",
        "requests.json",
        "anthropic-requests.json",
        "streams.json",
        "anthropic-streams.json",
        "google-requests.json",
        "google-streams.json",
        "google-vertex-requests.json",
        "google-vertex-streams.json",
        "mistral-requests.json",
        "mistral-streams.json",
        "azure-requests.json",
        "azure-streams.json",
        "auth-storage.json",
      ],
    };
    await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
    await writeFile(path.join(familyDir, "provider.json"), `${JSON.stringify(provider, null, 2)}\n`);
    await writeFile(
      path.join(familyDir, "anthropic-provider.json"),
      `${JSON.stringify(anthropic.provider, null, 2)}\n`,
    );
    await writeFile(path.join(familyDir, "google-provider.json"), `${JSON.stringify(google.provider, null, 2)}\n`);
    await writeFile(
      path.join(familyDir, "google-vertex-provider.json"),
      `${JSON.stringify(googleVertex.provider, null, 2)}\n`,
    );
    await writeFile(path.join(familyDir, "requests.json"), `${JSON.stringify({ cases: requests }, null, 2)}\n`);
    await writeFile(path.join(familyDir, "streams.json"), `${JSON.stringify({ cases: streams }, null, 2)}\n`);
    await writeFile(
      path.join(familyDir, "anthropic-requests.json"),
      `${JSON.stringify({ cases: anthropic.requests }, null, 2)}\n`,
    );
    await writeFile(
      path.join(familyDir, "anthropic-streams.json"),
      `${JSON.stringify({ cases: anthropic.streams }, null, 2)}\n`,
    );
    await writeFile(
      path.join(familyDir, "google-requests.json"),
      `${JSON.stringify({ cases: google.requests }, null, 2)}\n`,
    );
    await writeFile(
      path.join(familyDir, "google-streams.json"),
      `${JSON.stringify({ cases: google.streams }, null, 2)}\n`,
    );
    await writeFile(
      path.join(familyDir, "google-vertex-requests.json"),
      `${JSON.stringify({ cases: googleVertex.requests }, null, 2)}\n`,
    );
    await writeFile(
      path.join(familyDir, "google-vertex-streams.json"),
      `${JSON.stringify({ cases: googleVertex.streams }, null, 2)}\n`,
    );
    await writeFile(
      path.join(familyDir, "mistral-requests.json"),
      `${JSON.stringify({ cases: mistralAzure.mistralRequests }, null, 2)}\n`,
    );
    await writeFile(
      path.join(familyDir, "mistral-streams.json"),
      `${JSON.stringify({ cases: mistralAzure.mistralStreams }, null, 2)}\n`,
    );
    await writeFile(
      path.join(familyDir, "azure-requests.json"),
      `${JSON.stringify({ cases: mistralAzure.azureRequests }, null, 2)}\n`,
    );
    await writeFile(
      path.join(familyDir, "azure-streams.json"),
      `${JSON.stringify({ cases: mistralAzure.azureStreams }, null, 2)}\n`,
    );
    await writeFile(path.join(familyDir, "auth-storage.json"), `${JSON.stringify(authStorage, null, 2)}\n`);
  } finally {
    Date.now = originalNow;
    globalThis.fetch = originalFetch;
  }
}
