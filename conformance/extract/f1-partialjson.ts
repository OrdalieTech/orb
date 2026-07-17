import { createRequire } from "node:module";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

import {
  parseJsonWithRepair,
  parseStreamingJson,
  repairJson,
} from "../../.upstream/packages/ai/src/utils/json-parse.ts";

type Operation = "partialParse" | "repairJson" | "parseJsonWithRepair" | "parseStreamingJson";

type CaseDefinition = {
  name: string;
  operation: Operation;
  input?: string;
  allow?: number;
};

type PartialJSONModule = {
  Allow: Record<"STR" | "NUM" | "ARR" | "OBJ" | "ALL", number>;
  parse(input: string, allow?: number): unknown;
};

function normalize(value: unknown): unknown {
  if (typeof value === "number") {
    if (Number.isNaN(value)) return { $number: "NaN" };
    if (value === Number.POSITIVE_INFINITY) return { $number: "Infinity" };
    if (value === Number.NEGATIVE_INFINITY) return { $number: "-Infinity" };
    return value;
  }
  if (Array.isArray(value)) return value.map(normalize);
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, normalize(item)]));
  }
  return value;
}

function errorKind(error: unknown): string {
  return error instanceof Error ? error.constructor.name : typeof error;
}

export async function generateF1PartialJSON(
  upstreamRoot: string,
  outputRoot: string,
  _upstreamCommit: string,
): Promise<void> {
  const requireFromUpstream = createRequire(path.join(upstreamRoot, "package.json"));
  const partialJSON = requireFromUpstream("partial-json") as PartialJSONModule;
  const { Allow } = partialJSON;

  // The readme-* inputs are copied from partial-json 0.1.7's published README.
  const definitions: CaseDefinition[] = [
    { name: "readme-complete-object", operation: "partialParse", input: '{"key":"value"}' },
    {
      name: "readme-partial-string-and-object",
      operation: "partialParse",
      input: '{"key": "v',
      allow: Allow.STR | Allow.OBJ,
    },
    { name: "readme-partial-object-only", operation: "partialParse", input: '{"key": "v', allow: Allow.OBJ },
    {
      name: "readme-complete-value-partial-object",
      operation: "partialParse",
      input: '{"key": "value"',
      allow: Allow.OBJ,
    },
    {
      name: "readme-nested-partial",
      operation: "partialParse",
      input: '[ {"key1": "value1", "key2": [ "value2',
    },
    { name: "readme-negative-infinity", operation: "partialParse", input: "-Inf" },
    { name: "readme-malformed", operation: "partialParse", input: "wrong" },
    {
      name: "readme-disallow-partial-object",
      operation: "partialParse",
      input: '[{"a": 1, "b": 2}, {"a": 3,',
      allow: Allow.ARR,
    },
    {
      name: "readme-disallow-partial-string",
      operation: "partialParse",
      input: '["complete string", "incompl',
      allow: ~Allow.STR,
    },
    { name: "partial-disallowed-array", operation: "partialParse", input: "[", allow: Allow.STR },
    { name: "partial-number-exponent", operation: "partialParse", input: "-1.25e+", allow: Allow.NUM },

    { name: "repair-valid-escapes", operation: "repairJson", input: '{"x":"\\\"\\\\\\/\\b\\f\\n\\r\\t\\u12aF"}' },
    { name: "repair-invalid-escapes", operation: "repairJson", input: '{"path":"C:\\q\\z"}' },
    { name: "repair-raw-controls", operation: "repairJson", input: '"a\u0000\b\f\n\r\t\u001fb"' },
    { name: "repair-trailing-backslash", operation: "repairJson", input: '"abc\\' },
    { name: "repair-incomplete-unicode", operation: "repairJson", input: '"\\u12' },

    { name: "parse-repair-valid", operation: "parseJsonWithRepair", input: '{"name":"pi","n":1}' },
    {
      name: "parse-repair-invalid-escape-and-control",
      operation: "parseJsonWithRepair",
      input: '{"path":"C:\\q","line":"a\nb"}',
    },
    { name: "parse-repair-structurally-incomplete", operation: "parseJsonWithRepair", input: '{"a":' },

    { name: "streaming-undefined", operation: "parseStreamingJson" },
    { name: "streaming-whitespace", operation: "parseStreamingJson", input: "\ufeff \n" },
    {
      name: "streaming-u0085-is-not-ecmascript-whitespace",
      operation: "parseStreamingJson",
      input: '\u0085{"a":1}',
    },
    { name: "streaming-complete", operation: "parseStreamingJson", input: '{"name":"pi","n":1}' },
    {
      name: "streaming-nested-partial",
      operation: "parseStreamingJson",
      input: '{"a":1,"b":[true,{"c":"hel',
    },
    { name: "streaming-unfinished-member", operation: "parseStreamingJson", input: '{"a":1,"b":' },
    { name: "streaming-invalid-escape", operation: "parseStreamingJson", input: '{"path":"C:\\q"}' },
    {
      name: "streaming-partial-invalid-escape",
      operation: "parseStreamingJson",
      input: '{"path":"C:\\q',
    },
    { name: "streaming-partial-raw-control", operation: "parseStreamingJson", input: '"line\nnext' },
    { name: "streaming-malformed-fallback", operation: "parseStreamingJson", input: "wrong" },
    { name: "streaming-incomplete-null-fallback", operation: "parseStreamingJson", input: "n" },
    { name: "streaming-complete-null", operation: "parseStreamingJson", input: "null" },
    { name: "streaming-trailing-comma", operation: "parseStreamingJson", input: '{"a":1,}' },
    { name: "streaming-ignored-suffix", operation: "parseStreamingJson", input: '{"a":1}garbage' },
  ];

  const cases = definitions.map((definition) => {
    try {
      let value: unknown;
      switch (definition.operation) {
        case "partialParse":
          value = partialJSON.parse(definition.input ?? "", definition.allow);
          break;
        case "repairJson":
          value = repairJson(definition.input ?? "");
          break;
        case "parseJsonWithRepair":
          value = parseJsonWithRepair(definition.input ?? "");
          break;
        case "parseStreamingJson":
          value = parseStreamingJson(definition.input);
          break;
      }
      return { ...definition, expected: normalize(value) };
    } catch (error) {
      return { ...definition, expectedError: errorKind(error) };
    }
  });

  const familyDir = path.join(outputRoot, "F1");
  await mkdir(familyDir, { recursive: true });
  await writeFile(path.join(familyDir, "partialjson.json"), `${JSON.stringify({ cases }, null, 2)}\n`);
}
