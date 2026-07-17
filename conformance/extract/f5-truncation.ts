import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

type InputSpec =
  | { kind: "literal"; value: string }
  | { kind: "repeat"; value: string; count: number }
  | { kind: "repeatLines"; value: string; count: number; trailingNewline?: boolean }
  | { kind: "utf16"; units: number[] };

type TruncationCase = {
  name: string;
  operation: "head" | "tail";
  input: InputSpec;
  options?: { maxLines?: number; maxBytes?: number };
};

type LineCase = {
  name: string;
  operation: "line";
  input: InputSpec;
  maxChars?: number;
};

type SizeCase = {
  name: string;
  operation: "size";
  bytes: number;
};

type FixtureCase = TruncationCase | LineCase | SizeCase;

const cases: FixtureCase[] = [
  { name: "head-empty-defaults", operation: "head", input: { kind: "literal", value: "" } },
  { name: "tail-empty-defaults", operation: "tail", input: { kind: "literal", value: "" } },
  {
    name: "head-preserves-trailing-newline",
    operation: "head",
    input: { kind: "literal", value: "alpha\nβeta\n" },
    options: { maxLines: 2, maxBytes: 100 },
  },
  {
    name: "tail-preserves-trailing-newline",
    operation: "tail",
    input: { kind: "literal", value: "alpha\nβeta\n" },
    options: { maxLines: 2, maxBytes: 100 },
  },
  {
    name: "head-line-limit",
    operation: "head",
    input: { kind: "literal", value: "alpha\nbeta\ngamma" },
    options: { maxLines: 2, maxBytes: 100 },
  },
  {
    name: "tail-line-limit",
    operation: "tail",
    input: { kind: "literal", value: "alpha\nbeta\ngamma" },
    options: { maxLines: 2, maxBytes: 100 },
  },
  {
    name: "head-byte-limit-keeps-complete-lines",
    operation: "head",
    input: { kind: "literal", value: "alpha\nβeta\nomega" },
    options: { maxLines: 10, maxBytes: 8 },
  },
  {
    name: "head-first-line-exceeds-limit",
    operation: "head",
    input: { kind: "literal", value: "€€€\nshort" },
    options: { maxLines: 10, maxBytes: 5 },
  },
  {
    name: "tail-partial-last-line-utf8-boundary",
    operation: "tail",
    input: { kind: "literal", value: "prefix😀suffix" },
    options: { maxLines: 10, maxBytes: 7 },
  },
  {
    name: "tail-byte-limit-keeps-complete-lines",
    operation: "tail",
    input: { kind: "literal", value: "one\n二\nthree" },
    options: { maxLines: 10, maxBytes: 7 },
  },
  {
    name: "tail-partial-line-replaces-lone-surrogate",
    operation: "tail",
    input: { kind: "utf16", units: [112, 114, 101, 102, 105, 120, 0xd800] },
    options: { maxLines: 10, maxBytes: 3 },
  },
  {
    name: "head-zero-lines",
    operation: "head",
    input: { kind: "literal", value: "alpha\nbeta" },
    options: { maxLines: 0, maxBytes: 100 },
  },
  {
    name: "tail-zero-lines",
    operation: "tail",
    input: { kind: "literal", value: "alpha\nbeta" },
    options: { maxLines: 0, maxBytes: 100 },
  },
  {
    name: "head-zero-bytes",
    operation: "head",
    input: { kind: "literal", value: "alpha" },
    options: { maxLines: 10, maxBytes: 0 },
  },
  {
    name: "tail-zero-bytes",
    operation: "tail",
    input: { kind: "literal", value: "alpha" },
    options: { maxLines: 10, maxBytes: 0 },
  },
  {
    name: "head-exact-multibyte-byte-limit",
    operation: "head",
    input: { kind: "literal", value: "éé" },
    options: { maxLines: 1, maxBytes: 4 },
  },
  {
    name: "head-default-line-limit",
    operation: "head",
    input: { kind: "repeatLines", value: "x", count: 2001 },
  },
  {
    name: "tail-default-line-limit",
    operation: "tail",
    input: { kind: "repeatLines", value: "x", count: 2001 },
  },
  {
    name: "head-default-byte-limit",
    operation: "head",
    input: { kind: "repeat", value: "x", count: 51201 },
  },
  {
    name: "tail-default-byte-limit",
    operation: "tail",
    input: { kind: "repeat", value: "x", count: 51201 },
  },
  {
    name: "line-under-limit",
    operation: "line",
    input: { kind: "literal", value: "abcdef" },
    maxChars: 6,
  },
  {
    name: "line-over-limit",
    operation: "line",
    input: { kind: "literal", value: "abcdef" },
    maxChars: 3,
  },
  {
    name: "line-uses-utf16-length",
    operation: "line",
    input: { kind: "literal", value: "😀x" },
    maxChars: 2,
  },
  { name: "size-bytes", operation: "size", bytes: 1023 },
  { name: "size-kibibytes", operation: "size", bytes: 1536 },
  { name: "size-kibibytes-half-tie", operation: "size", bytes: 51456 },
  { name: "size-mebibytes", operation: "size", bytes: 1572864 },
];

function materialize(spec: InputSpec): string {
  switch (spec.kind) {
    case "literal":
      return spec.value;
    case "repeat":
      return spec.value.repeat(spec.count);
    case "repeatLines": {
      const value = Array.from({ length: spec.count }, () => spec.value).join("\n");
      return spec.trailingNewline ? `${value}\n` : value;
    }
    case "utf16":
      return String.fromCharCode(...spec.units);
  }
}

export async function generateF5(upstreamRoot: string, outputRoot: string, upstreamCommit: string): Promise<void> {
  const source = "packages/coding-agent/src/core/tools/truncate.ts";
  const moduleURL = pathToFileURL(path.join(upstreamRoot, source)).href;
  const truncate = (await import(moduleURL)) as {
    truncateHead(content: string, options?: { maxLines?: number; maxBytes?: number }): unknown;
    truncateTail(content: string, options?: { maxLines?: number; maxBytes?: number }): unknown;
    truncateLine(line: string, maxChars?: number): unknown;
    formatSize(bytes: number): string;
  };

  const generated = cases.map((fixtureCase) => {
    if (fixtureCase.operation === "size") {
      return { ...fixtureCase, expected: truncate.formatSize(fixtureCase.bytes) };
    }

    const input = materialize(fixtureCase.input);
    if (fixtureCase.operation === "line") {
      return { ...fixtureCase, expected: truncate.truncateLine(input, fixtureCase.maxChars) };
    }

    const expected =
      fixtureCase.operation === "head"
        ? truncate.truncateHead(input, fixtureCase.options)
        : truncate.truncateTail(input, fixtureCase.options);
    return { ...fixtureCase, expected };
  });

  const familyDir = path.join(outputRoot, "F5");
  await mkdir(familyDir, { recursive: true });
  const manifest = {
    family: "F5",
    upstreamCommit,
    generator: "conformance/extract/f5-truncation.ts",
    source,
    files: ["cases.json"],
  };

  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(path.join(familyDir, "cases.json"), `${JSON.stringify({ cases: generated }, null, 2)}\n`);
}
