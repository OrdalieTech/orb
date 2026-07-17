import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
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

type AccumulatorChunk =
  | { kind: "utf8"; value: string }
  | { kind: "bytes"; values: number[] }
  | { kind: "repeatLines"; value: string; count: number; trailingNewline?: boolean };

type AccumulatorCase = {
  name: string;
  options?: { maxLines?: number; maxBytes?: number; tempFilePrefix?: string };
  chunks: AccumulatorChunk[];
};

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

const accumulatorCases: AccumulatorCase[] = [
  {
    name: "empty-finish-is-idempotent",
    options: { maxLines: 3, maxBytes: 16, tempFilePrefix: "pi-f5-empty" },
    chunks: [],
  },
  {
    name: "streaming-line-counts-and-trailing-newline",
    options: { maxLines: 3, maxBytes: 100, tempFilePrefix: "pi-f5-lines" },
    chunks: [
      { kind: "utf8", value: "alpha\n" },
      { kind: "utf8", value: "beta" },
      { kind: "utf8", value: "\ngamma\n" },
    ],
  },
  {
    name: "split-utf8-sequence",
    options: { maxLines: 3, maxBytes: 100, tempFilePrefix: "pi-f5-split" },
    chunks: [
      { kind: "bytes", values: [0xe2] },
      { kind: "bytes", values: [0x82] },
      { kind: "bytes", values: [0xac, 0x0a] },
    ],
  },
  {
    name: "finish-flushes-incomplete-utf8",
    options: { maxLines: 3, maxBytes: 2, tempFilePrefix: "pi-f5-incomplete" },
    chunks: [{ kind: "bytes", values: [0xe2] }],
  },
  {
    name: "invalid-utf8-spills-original-raw-bytes",
    options: { maxLines: 10, maxBytes: 3, tempFilePrefix: "pi-f5-invalid" },
    chunks: [
      { kind: "bytes", values: [0xff, 0x0a] },
      { kind: "bytes", values: [0x61] },
    ],
  },
  {
    name: "invalid-utf8-replaces-maximal-subpart",
    options: { maxLines: 10, maxBytes: 100, tempFilePrefix: "pi-f5-invalid-subpart" },
    chunks: [{ kind: "bytes", values: [0xe1, 0x80, 0x41] }],
  },
  {
    name: "line-limit-does-not-count-trailing-newline",
    options: { maxLines: 2, maxBytes: 100, tempFilePrefix: "pi-f5-line-limit" },
    chunks: [{ kind: "repeatLines", value: "line", count: 4, trailingNewline: true }],
  },
  {
    name: "byte-limit-keeps-partial-last-line",
    options: { maxLines: 10, maxBytes: 7, tempFilePrefix: "pi-f5-byte-limit" },
    chunks: [{ kind: "utf8", value: "prefix😀suffix" }],
  },
  {
    name: "rolling-tail-drops-incomplete-leading-line",
    options: { maxLines: 4, maxBytes: 20, tempFilePrefix: "pi-f5-rolling" },
    chunks: [{ kind: "repeatLines", value: "0123456789", count: 20, trailingNewline: true }],
  },
  {
    name: "zero-line-limit",
    options: { maxLines: 0, maxBytes: 100, tempFilePrefix: "pi-f5-zero-lines" },
    chunks: [{ kind: "utf8", value: "alpha\nbeta\n" }],
  },
  {
    name: "zero-byte-limit",
    options: { maxLines: 10, maxBytes: 0, tempFilePrefix: "pi-f5-zero-bytes" },
    chunks: [{ kind: "utf8", value: "alpha" }],
  },
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

function materializeChunk(spec: AccumulatorChunk): Buffer {
  switch (spec.kind) {
    case "utf8":
      return Buffer.from(spec.value, "utf-8");
    case "bytes":
      return Buffer.from(spec.values);
    case "repeatLines": {
      const value = Array.from({ length: spec.count }, () => spec.value).join("\n");
      return Buffer.from(spec.trailingNewline ? `${value}\n` : value, "utf-8");
    }
  }
}

function canonicalizeAccumulatorSnapshot(snapshot: {
  content: string;
  truncation: unknown;
  fullOutputPath?: string;
}) {
  return {
    content: snapshot.content,
    truncation: snapshot.truncation,
    hasFullOutputPath: snapshot.fullOutputPath !== undefined,
  };
}

async function generateAccumulatorCases(moduleURL: string) {
  const outputAccumulator = (await import(moduleURL)) as {
    OutputAccumulator: new (options?: {
      maxLines?: number;
      maxBytes?: number;
      tempFilePrefix?: string;
    }) => {
      append(data: Buffer): void;
      finish(): void;
      snapshot(options?: { persistIfTruncated?: boolean }): {
        content: string;
        truncation: unknown;
        fullOutputPath?: string;
      };
      closeTempFile(): Promise<void>;
      getLastLineBytes(): number;
    };
  };

  return Promise.all(
    accumulatorCases.map(async (fixtureCase) => {
      const accumulator = new outputAccumulator.OutputAccumulator(fixtureCase.options);
      const chunkSnapshots = [];
      let fullOutputPath: string | undefined;
      try {
        for (const chunk of fixtureCase.chunks) {
          accumulator.append(materializeChunk(chunk));
          const snapshot = accumulator.snapshot();
          fullOutputPath = snapshot.fullOutputPath ?? fullOutputPath;
          chunkSnapshots.push(canonicalizeAccumulatorSnapshot(snapshot));
        }

        accumulator.finish();
        const finalSnapshot = accumulator.snapshot({ persistIfTruncated: true });
        fullOutputPath = finalSnapshot.fullOutputPath ?? fullOutputPath;
        accumulator.finish();
        const idempotentFinishSnapshot = accumulator.snapshot({ persistIfTruncated: true });
        fullOutputPath = idempotentFinishSnapshot.fullOutputPath ?? fullOutputPath;

        let appendAfterFinishError: string | null = null;
        try {
          accumulator.append(Buffer.alloc(0));
        } catch (error) {
          appendAfterFinishError = error instanceof Error ? error.message : String(error);
        }

        await accumulator.closeTempFile();
        const persistedOutputBase64 = fullOutputPath
          ? (await readFile(fullOutputPath)).toString("base64")
          : null;

        return {
          ...fixtureCase,
          expected: {
            chunkSnapshots,
            finalSnapshot: canonicalizeAccumulatorSnapshot(finalSnapshot),
            idempotentFinishSnapshot: canonicalizeAccumulatorSnapshot(idempotentFinishSnapshot),
            lastLineBytes: accumulator.getLastLineBytes(),
            appendAfterFinishError,
            persistedOutputBase64,
          },
        };
      } finally {
        await accumulator.closeTempFile();
        if (fullOutputPath) await rm(fullOutputPath, { force: true });
      }
    }),
  );
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

  const accumulatorSource = "packages/coding-agent/src/core/tools/output-accumulator.ts";
  const accumulatorModuleURL = pathToFileURL(path.join(upstreamRoot, accumulatorSource)).href;
  const generatedAccumulatorCases = await generateAccumulatorCases(accumulatorModuleURL);

  const familyDir = path.join(outputRoot, "F5");
  await mkdir(familyDir, { recursive: true });
  const manifest = {
    family: "F5",
    upstreamCommit,
    generator: "conformance/extract/f5-truncation.ts",
    source,
    additionalSources: [accumulatorSource],
    files: ["cases.json", "accumulator.json"],
  };

  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  await writeFile(path.join(familyDir, "cases.json"), `${JSON.stringify({ cases: generated }, null, 2)}\n`);
  await writeFile(
    path.join(familyDir, "accumulator.json"),
    `${JSON.stringify({ cases: generatedAccumulatorCases }, null, 2)}\n`,
  );
}
