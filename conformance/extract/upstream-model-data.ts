import { mkdir, readdir, readFile, rmdir, unlink, writeFile } from "node:fs/promises";
import path from "node:path";

// Upstream <0.82.0 generated `data/<provider>.json` as a flat `id -> model` map;
// >=0.82.0 groups it by API and flattens it through `flattenModelCatalog`. The
// generated `<provider>.models.ts` names whichever helper the revision uses, so
// synthesized catalogs follow the checked-out shape instead of a hardcoded one.
export async function writeProviderModelData(
  providersDir: string,
  provider: string,
  models: Record<string, { api?: string }>,
): Promise<string> {
  const source = await readFile(path.join(providersDir, `${provider}.models.ts`), "utf8");
  let values: unknown = models;
  if (source.includes("flattenModelCatalog")) {
    const grouped: Record<string, Record<string, unknown>> = {};
    for (const [id, model] of Object.entries(models)) {
      const api = model.api ?? "unknown";
      (grouped[api] ??= {})[id] = model;
    }
    values = grouped;
  }
  const filePath = path.join(providersDir, "data", `${provider}.json`);
  await mkdir(path.dirname(filePath), { recursive: true });
  await writeFile(filePath, `${JSON.stringify(values)}\n`);
  return filePath;
}

// The pinned source contains generated *.models.ts catalogs but intentionally
// omits their adjacent JSON values. Session and resource modules only reach
// those catalogs through unrelated barrel imports, so empty values preserve
// the exercised behavior while keeping fixture extraction offline.
export async function withUpstreamModelData<T>(upstreamRoot: string, run: () => Promise<T>): Promise<T> {
  const providersDir = path.join(upstreamRoot, "packages/ai/src/providers");
  const dataDir = path.join(providersDir, "data");
  const entries = await readdir(providersDir);
  const created: string[] = [];

  await mkdir(dataDir, { recursive: true });
  const write = async (filePath: string, contents: string): Promise<void> => {
    try {
      await writeFile(filePath, contents, { flag: "wx" });
      created.push(filePath);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
    }
  };

  for (const entry of entries) {
    if (!entry.endsWith(".models.ts")) continue;
    await write(path.join(dataDir, `${entry.slice(0, -".models.ts".length)}.json`), "{}\n");
  }
  // Upstream >=0.82.0 statically imports the generated manifest from all.ts.
  // Only `generatedAt` is read, and a fixed epoch keeps extraction deterministic.
  await write(
    path.join(dataDir, ".manifest.json"),
    `${JSON.stringify({
      schemaVersion: 3,
      generatedAt: "1970-01-01T00:00:00.000Z",
      structureHash: "",
      files: {},
    })}\n`,
  );

  try {
    return await run();
  } finally {
    await Promise.all(created.map((filePath) => unlink(filePath)));
    await rmdir(dataDir).catch((error: NodeJS.ErrnoException) => {
      if (error.code !== "ENOTEMPTY" && error.code !== "ENOENT") throw error;
    });
  }
}
