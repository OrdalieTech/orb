import { createHash } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

const tinyPng = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl2nWQAAAAASUVORK5CYII=",
  "base64",
);

type ReadModule = {
  createReadToolDefinition(cwd: string, options: {
    autoResizeImages: boolean;
    operations: {
      access(path: string): Promise<void>;
      readFile(path: string): Promise<Buffer>;
      detectImageMimeType(path: string): Promise<string>;
    };
  }): {
    execute: (...args: unknown[]) => Promise<{ content: unknown[]; details?: unknown }>;
  };
};

export async function generateWP440Read(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  const source = "packages/coding-agent/src/core/tools/read.ts";
  const readModule = await import(pathToFileURL(path.join(upstreamRoot, source)).href) as ReadModule;
  const tool = readModule.createReadToolDefinition("/fixture", {
    autoResizeImages: false,
    operations: {
      async access() {},
      async readFile() { return Buffer.from(tinyPng); },
      async detectImageMimeType() { return "image/png"; },
    },
  });
  const cases = [];
  for (const fixtureCase of [
    { name: "vision-model", input: ["text", "image"] },
    { name: "non-vision-model", input: ["text"] },
  ]) {
    const result = await tool.execute(
      "fixture-call",
      { path: "fixture.png" },
      undefined,
      undefined,
      { model: { input: fixtureCase.input } },
    );
    cases.push({
      name: fixtureCase.name,
      modelInput: fixtureCase.input,
      expected: result,
    });
  }

  const familyDir = path.join(outputRoot, "WP440Read");
  await mkdir(familyDir, { recursive: true });
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({
    family: "WP440Read",
    upstreamCommit,
    generator: "conformance/extract/wp440-read.ts",
    source,
    files: ["read.json"],
  }, null, 2)}\n`);
  await writeFile(path.join(familyDir, "read.json"), `${JSON.stringify({
    schemaVersion: 1,
    inputBase64: tinyPng.toString("base64"),
    inputSHA256: createHash("sha256").update(tinyPng).digest("hex"),
    cases,
  }, null, 2)}\n`);
}
