import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

import type {
  AssistantImages,
  AssistantMessage,
  AssistantMessageEvent,
  Context,
  ImagesModel,
  Message,
  Model,
  Tool,
} from "../../.upstream/packages/ai/src/types.ts";
import { parseStreamingJson } from "../../.upstream/packages/ai/src/utils/json-parse.ts";

type FixtureKind = "message" | "context" | "event" | "images" | "model" | "imagesModel" | "partialToolEvent";

const zeroCost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 };
const zeroModelCost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
const zeroUsage = {
  input: 0,
  output: 0,
  cacheRead: 0,
  cacheWrite: 0,
  totalTokens: 0,
  cost: zeroCost,
};

const fullAssistant: AssistantMessage = {
  role: "assistant",
  content: [
    { type: "text", text: "answer", textSignature: "text-signature" },
    { type: "thinking", thinking: "reasoning", thinkingSignature: "thinking-signature", redacted: false },
    {
      type: "toolCall",
      id: "tool-1",
      name: "edit",
      arguments: { path: "README.md", count: 2, enabled: true, nested: { value: null }, items: [1, "two"] },
      thoughtSignature: "thought-signature",
    },
  ],
  api: "openai-responses",
  provider: "openai",
  model: "gpt-test",
  responseModel: "gpt-test-2026-07-17",
  responseId: "resp-1",
  diagnostics: [
    {
      type: "retry",
      timestamp: 12,
      error: { name: "Error", message: "timed out", stack: "stack", code: "ETIMEDOUT" },
      details: { attempt: 2 },
    },
    { type: "rate_limit", timestamp: 13, error: { message: "slow down", code: 429 } },
  ],
  usage: {
    input: 10,
    output: 8,
    cacheRead: 4,
    cacheWrite: 3,
    cacheWrite1h: 2,
    reasoning: 5,
    totalTokens: 25,
    cost: { input: 0.1, output: 0.2, cacheRead: 0.03, cacheWrite: 0.04, total: 0.37 },
  },
  stopReason: "toolUse",
  timestamp: 1700000000002,
};

const minimalAssistant: AssistantMessage = {
  role: "assistant",
  content: [],
  api: "custom-api",
  provider: "custom-provider",
  model: "custom-model",
  responseModel: undefined,
  responseId: undefined,
  diagnostics: undefined,
  usage: zeroUsage,
  stopReason: "stop",
  errorMessage: undefined,
  timestamp: 1700000000003,
};

const explicitEmptyAssistant: AssistantMessage = {
  role: "assistant",
  content: [
    { type: "text", text: "", textSignature: "" },
    { type: "thinking", thinking: "", thinkingSignature: "", redacted: false },
    { type: "toolCall", id: "", name: "", arguments: {}, thoughtSignature: "" },
  ],
  api: "openai-responses",
  provider: "openai",
  model: "",
  responseModel: "",
  responseId: "",
  diagnostics: [],
  usage: { ...zeroUsage, cacheWrite1h: 0, reasoning: 0 },
  stopReason: "error",
  errorMessage: "",
  timestamp: 0,
};

const lengthAssistant: AssistantMessage = {
  ...minimalAssistant,
  content: [
    {
      type: "text",
      text: "astral 🧪, nul \u0000, newline\n, html <>&",
      textSignature: JSON.stringify({ v: 1, id: "msg_123", phase: "commentary" }),
    },
  ],
  diagnostics: [
    {
      type: "numeric-zero-code",
      timestamp: 0,
      error: { message: "zero", code: 0 },
      details: {},
    },
  ],
  stopReason: "length",
  timestamp: 1700000000007,
};

const abortedAssistant: AssistantMessage = {
  ...minimalAssistant,
  stopReason: "aborted",
  errorMessage: "",
  timestamp: 1700000000008,
};

const tool: Tool = {
  name: "echo",
  description: "Echo text",
  parameters: {
    type: "object",
    properties: { text: { type: "string" } },
    required: ["text"],
    additionalProperties: false,
  } as Tool["parameters"],
};

const messages: Message[] = [
  { role: "user", content: "hello", timestamp: 1700000000000 },
  {
    role: "user",
    content: [
      { type: "text", text: "look", textSignature: "" },
      { type: "image", data: "aGVsbG8=", mimeType: "image/png" },
    ],
    timestamp: 1700000000001,
  },
  { role: "user", content: [], timestamp: 0 },
  fullAssistant,
  minimalAssistant,
  explicitEmptyAssistant,
  {
    role: "toolResult",
    toolCallId: "tool-1",
    toolName: "read",
    content: [
      { type: "text", text: "contents", textSignature: "result-signature" },
      { type: "image", data: "AAEC", mimeType: "image/jpeg" },
    ],
    details: { path: "/tmp/file", lines: [1, 2] },
    addedToolNames: ["late_tool"],
    isError: false,
    timestamp: 1700000000004,
  },
  {
    role: "toolResult",
    toolCallId: "tool-2",
    toolName: "bash",
    content: [],
    isError: true,
    timestamp: 1700000000005,
  },
  {
    role: "toolResult",
    toolCallId: "tool-3",
    toolName: "custom",
    content: [{ type: "text", text: "null details" }],
    details: null,
    addedToolNames: [],
    isError: false,
    timestamp: 1700000000006,
  },
  { role: "user", content: "", timestamp: 0 },
  { role: "user", content: "astral 🧪, nul \u0000, newline\n, html <>&", timestamp: 1700000000009 },
  lengthAssistant,
  abortedAssistant,
];

const contexts: Context[] = [
  { systemPrompt: "", messages: [messages[0], minimalAssistant], tools: [] },
  { messages: [messages[1], fullAssistant, messages[6]], tools: [tool] },
  { messages: [] },
];

const events: AssistantMessageEvent[] = [
  { type: "start", partial: minimalAssistant },
  { type: "text_start", contentIndex: 0, partial: minimalAssistant },
  { type: "text_delta", contentIndex: 0, delta: "hi", partial: minimalAssistant },
  { type: "text_delta", contentIndex: 2, delta: "", partial: minimalAssistant },
  { type: "text_end", contentIndex: 0, content: "hi", partial: minimalAssistant },
  { type: "text_end", contentIndex: 2, content: "", partial: minimalAssistant },
  { type: "thinking_start", contentIndex: 0, partial: minimalAssistant },
  { type: "thinking_delta", contentIndex: 0, delta: "why", partial: minimalAssistant },
  { type: "thinking_end", contentIndex: 0, content: "why", partial: minimalAssistant },
  { type: "toolcall_start", contentIndex: 0, partial: minimalAssistant },
  { type: "toolcall_delta", contentIndex: 0, delta: '{"x":', partial: minimalAssistant },
  {
    type: "toolcall_end",
    contentIndex: 0,
    toolCall: { type: "toolCall", id: "tool-9", name: "echo", arguments: { x: 1 } },
    partial: fullAssistant,
  },
  { type: "done", reason: "stop", message: minimalAssistant },
  { type: "done", reason: "length", message: lengthAssistant },
  { type: "done", reason: "toolUse", message: fullAssistant },
  { type: "error", reason: "aborted", error: abortedAssistant },
  { type: "error", reason: "error", error: explicitEmptyAssistant },
];

const partialToolInput = '{"value":Inf';
const partialToolAssistant: AssistantMessage = {
  ...minimalAssistant,
  content: [
    {
      type: "toolCall",
      id: "tool-partial",
      name: "numbers",
      arguments: parseStreamingJson<Record<string, unknown>>(partialToolInput),
    },
  ],
  stopReason: "toolUse",
};
const partialToolEvent: AssistantMessageEvent = {
  type: "toolcall_delta",
  contentIndex: 0,
  delta: "Inf",
  partial: partialToolAssistant,
};

const models: Model<"openai-completions">[] = [
  {
    id: "compat-model",
    name: "Compatibility Model",
    api: "openai-completions",
    provider: "custom-provider",
    baseUrl: "https://example.invalid/v1",
    reasoning: true,
    thinkingLevelMap: { off: null, minimal: "none", low: "low", max: null },
    input: ["text", "image"],
    cost: {
      input: 1,
      output: 2,
      cacheRead: 0.1,
      cacheWrite: 0.2,
      tiers: [
        {
          input: 3,
          output: 4,
          cacheRead: 0.3,
          cacheWrite: 0.4,
          inputTokensAbove: 200000,
        },
      ],
    },
    contextWindow: 1000000,
    maxTokens: 65536,
    headers: {},
    compat: {
      supportsStore: false,
      supportsDeveloperRole: true,
      chatTemplateKwargs: {},
      openRouterRouting: { allow_fallbacks: false, only: [] },
      vercelGatewayRouting: { order: [] },
    },
  },
  {
    id: "minimal-model",
    name: "Minimal Model",
    api: "openai-completions",
    provider: "openai",
    baseUrl: "",
    reasoning: false,
    input: [],
    cost: zeroModelCost,
    contextWindow: 0,
    maxTokens: 0,
  },
];

const imagesModels: ImagesModel<"openrouter-images">[] = [
  {
    id: "image-model",
    name: "Image Model",
    api: "openrouter-images",
    provider: "openrouter",
    baseUrl: "https://example.invalid/images",
    thinkingLevelMap: { off: null, high: "high" },
    input: ["text", "image"],
    output: ["text", "image"],
    cost: zeroModelCost,
    headers: {},
  },
];

const images: AssistantImages[] = [
  {
    api: "openrouter-images",
    provider: "openrouter",
    model: "image-test",
    output: [
      { type: "text", text: "caption" },
      { type: "image", data: "AQID", mimeType: "image/webp" },
    ],
    responseId: "image-1",
    usage: zeroUsage,
    stopReason: "stop",
    timestamp: 1700000000007,
  },
  {
    api: "custom-images",
    provider: "custom",
    model: "minimal",
    output: [],
    stopReason: "aborted",
    errorMessage: "aborted",
    timestamp: 0,
  },
];

function serialize(name: string, kind: FixtureKind, value: unknown, input?: string) {
  return { name, kind, json: JSON.stringify(value), ...(input === undefined ? {} : { input }) };
}

export async function generateF1(_upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const cases = [
    ...messages.map((message, index) => serialize(`message-${index + 1}`, "message", message)),
    ...contexts.map((context, index) => serialize(`context-${index + 1}`, "context", context)),
    ...events.map((event, index) => serialize(`event-${event.type}-${index + 1}`, "event", event)),
    ...images.map((image, index) => serialize(`images-${index + 1}`, "images", image)),
    ...models.map((model, index) => serialize(`model-${index + 1}`, "model", model)),
    ...imagesModels.map((model, index) => serialize(`images-model-${index + 1}`, "imagesModel", model)),
    serialize("partial-tool-event-non-finite", "partialToolEvent", partialToolEvent, partialToolInput),
  ];
  const familyDir = path.join(outputRoot, "F1");
  await mkdir(familyDir, { recursive: true });
  const manifest = {
    family: "F1",
    upstreamCommit,
    generator:
      "conformance/extract/f1-messages.ts + conformance/extract/f1-partialjson.ts + conformance/extract/f1-schema.ts",
    source:
      "packages/ai/src/types.ts + packages/ai/src/utils/json-parse.ts + packages/ai/src/utils/typebox-helpers.ts",
    files: ["cases.json", "partialjson.json", "schema.json"],
  };
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(path.join(familyDir, "cases.json"), `${JSON.stringify({ cases }, null, 2)}\n`);
}
