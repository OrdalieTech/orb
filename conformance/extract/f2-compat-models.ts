import { execFile } from "node:child_process";
import { cp, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

export async function extractCompatModelsF2(upstreamRoot: string) {
  const temporaryRoot = await mkdtemp(path.join(tmpdir(), "orb-f2-compat-models-"));
  const packageRoot = path.join(temporaryRoot, "ai");
  const outputRoot = path.join(temporaryRoot, "catalog");
  const snapshot = path.resolve(upstreamRoot, "../ai/models/testdata/api.json");
  try {
    await cp(path.join(upstreamRoot, "packages/ai"), packageRoot, { recursive: true });
    const preload = path.join(temporaryRoot, "fixed-fetch.mjs");
    await writeFile(
      preload,
      `import { readFileSync } from "node:fs";
const snapshot = JSON.parse(readFileSync(process.env.ORB_MODEL_SNAPSHOT, "utf8"));
// Upstream >=0.84 asserts an exact qwen-token-plan-individual catalog; the
// pinned snapshot predates some of those IDs, so synthesize the missing ones
// from a tool-capable sibling. At revisions without the constant this no-ops.
const generator = readFileSync(process.env.ORB_GENERATE_MODELS_SOURCE, "utf8");
const expected = generator.match(/QWEN_TOKEN_PLAN_INDIVIDUAL_MODEL_IDS\\s*=\\s*new Set[^([]*\\(\\[([^\\]]*)\\]/);
if (expected) {
  const models = snapshot["alibaba-token-plan"]?.models ?? {};
  const template = Object.values(models).find((model) => model.tool_call === true);
  for (const match of expected[1].matchAll(/"([^"]+)"/g)) {
    const id = match[1];
    if (!models[id]) models[id] = { ...template, id, name: id };
  }
}
globalThis.fetch = async (input) => {
  const url = String(input instanceof Request ? input.url : input);
  if (url === "https://models.dev/api.json") return Response.json(snapshot);
  if (url === "https://integrate.api.nvidia.com/v1/models") {
    return Response.json({ data: Object.keys(snapshot.nvidia?.models ?? {}).map((id) => ({ id })) });
  }
  if (url === "https://openrouter.ai/api/v1/models") return Response.json(JSON.parse(readFileSync(process.env.ORB_OPENROUTER_SNAPSHOT, "utf8")));
  if (url === "https://ai-gateway.vercel.sh/v1/models") return Response.json({ data: [] });
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
        env: {
          ...process.env,
          ORB_MODEL_SNAPSHOT: snapshot,
          ORB_OPENROUTER_SNAPSHOT: path.resolve(upstreamRoot, "../ai/models/testdata/openrouter.json"),
          ORB_GENERATE_MODELS_SOURCE: path.join(packageRoot, "scripts/generate-models.ts"),
        },
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
        ...await Promise.all([
          ["openrouter", "anthropic/claude-opus-5"], ["openrouter", "anthropic/claude-sonnet-4.5"],
          ["xai", "grok-4.3"], ["xai", "grok-4.5"],
          ["anthropic", "claude-opus-5"], ["anthropic", "claude-fable-5"],
          ["zai-coding-cn", "glm-4.6v"], ["zai-coding-cn", "glm-5.2"],
          ["qwen-token-plan", "glm-5.2"], ["qwen-token-plan", "glm-5"],
          ["qwen-token-plan-individual", "qwen3.8-flash"],
          ["deepseek", "deepseek-v4-flash-vision-exp"],
          ["github-copilot", "claude-fable-5"],
        ].map(async ([provider, id]) => ({ name: `${provider}-${id}`, model: await readModel(provider, id) }))),
      ],
    };
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
}
