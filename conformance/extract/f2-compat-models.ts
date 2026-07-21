import { execFile } from "node:child_process";
import { cp, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

export async function extractCompatModelsF2(upstreamRoot: string) {
  const temporaryRoot = await mkdtemp(path.join(tmpdir(), "pigo-f2-compat-models-"));
  const packageRoot = path.join(temporaryRoot, "ai");
  const outputRoot = path.join(temporaryRoot, "catalog");
  const snapshot = path.resolve(upstreamRoot, "../ai/models/testdata/api.json");
  try {
    await cp(path.join(upstreamRoot, "packages/ai"), packageRoot, { recursive: true });
    const preload = path.join(temporaryRoot, "fixed-fetch.mjs");
    await writeFile(
      preload,
      `import { readFileSync } from "node:fs";
const snapshot = JSON.parse(readFileSync(process.env.PIGO_MODEL_SNAPSHOT, "utf8"));
globalThis.fetch = async (input) => {
  const url = String(input instanceof Request ? input.url : input);
  if (url === "https://models.dev/api.json") return Response.json(snapshot);
  if (url === "https://integrate.api.nvidia.com/v1/models") {
    return Response.json({ data: Object.keys(snapshot.nvidia?.models ?? {}).map((id) => ({ id })) });
  }
  if (url === "https://openrouter.ai/api/v1/models" || url === "https://ai-gateway.vercel.sh/v1/models") {
    return Response.json({ data: [] });
  }
  throw new Error(\`unexpected F2 compat model fetch: \${url}\`);
};
`,
    );
    await execFileAsync(
      process.execPath,
      [
        "--import",
        pathToFileURL(preload).href,
        path.join(packageRoot, "scripts/generate-models.ts"),
        "--strict",
        "--json-only",
        "--json-output",
        outputRoot,
        "--pretty",
      ],
      {
        cwd: packageRoot,
        env: { ...process.env, PIGO_MODEL_SNAPSHOT: snapshot },
        maxBuffer: 16 * 1024 * 1024,
      },
    );
    const readModel = async (provider: string, id: string) => {
      const models = JSON.parse(
        await readFile(path.join(outputRoot, "providers", `${provider}.json`), "utf8"),
      ) as Record<string, unknown>;
      const model = models[id];
      if (!model) throw new Error(`pinned generator omitted ${provider}/${id}`);
      return model;
    };
    return {
      cases: [
        { name: "together-reasoning", model: await readModel("together", "deepseek-ai/DeepSeek-V4-Pro") },
        { name: "zai-tool-stream", model: await readModel("zai", "glm-5.2") },
        {
          name: "fireworks-session-cache",
          model: await readModel("fireworks", "accounts/fireworks/models/minimax-m3"),
        },
      ],
    };
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
}
