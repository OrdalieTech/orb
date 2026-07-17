import { mkdir, readdir, rmdir, unlink, writeFile } from "node:fs/promises";
import path from "node:path";

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
  for (const entry of entries) {
    if (!entry.endsWith(".models.ts")) continue;
    const filePath = path.join(dataDir, `${entry.slice(0, -".models.ts".length)}.json`);
    try {
      await writeFile(filePath, "{}\n", { flag: "wx" });
      created.push(filePath);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
    }
  }

  try {
    return await run();
  } finally {
    await Promise.all(created.map((filePath) => unlink(filePath)));
    await rmdir(dataDir).catch((error: NodeJS.ErrnoException) => {
      if (error.code !== "ENOTEMPTY" && error.code !== "ENOENT") throw error;
    });
  }
}
