import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withUpstreamModelData } from "./upstream-model-data.ts";

type FixtureModel = {
  id: string;
  name: string;
  api: "anthropic-messages";
  provider: string;
  baseUrl: string;
  reasoning: boolean;
  input: Array<"text" | "image">;
  cost: {
    input: number;
    output: number;
    cacheRead: number;
    cacheWrite: number;
    tiers?: Array<{
      inputTokensAbove: number;
      input: number;
      output: number;
      cacheRead: number;
      cacheWrite: number;
    }>;
  };
  contextWindow: number;
  maxTokens: number;
};

const models: FixtureModel[] = [
  {
    id: "claude-sonnet-4-5",
    name: "Claude Sonnet 4.5",
    api: "anthropic-messages",
    provider: "anthropic",
    baseUrl: "https://api.anthropic.com",
    reasoning: true,
    input: ["text", "image"],
    cost: { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 },
    contextWindow: 200000,
    maxTokens: 8192,
  },
  {
    id: "gpt-4o",
    name: "GPT-4o",
    api: "anthropic-messages",
    provider: "openai",
    baseUrl: "https://api.openai.com",
    reasoning: false,
    input: ["text", "image"],
    cost: { input: 5, output: 15, cacheRead: 0.5, cacheWrite: 5 },
    contextWindow: 128000,
    maxTokens: 4096,
  },
  {
    id: "qwen/qwen3-coder:exacto",
    name: "Qwen3 Coder Exacto",
    api: "anthropic-messages",
    provider: "openrouter",
    baseUrl: "https://openrouter.ai/api/v1",
    reasoning: true,
    input: ["text"],
    cost: { input: 1, output: 2, cacheRead: 0.1, cacheWrite: 1 },
    contextWindow: 128000,
    maxTokens: 8192,
  },
  {
    id: "openai/gpt-4o:extended",
    name: "GPT-4o Extended",
    api: "anthropic-messages",
    provider: "openrouter",
    baseUrl: "https://openrouter.ai/api/v1",
    reasoning: false,
    input: ["text", "image"],
    cost: { input: 5, output: 15, cacheRead: 0.5, cacheWrite: 5 },
    contextWindow: 1500000,
    maxTokens: 900,
  },
];

const hiddenModels: FixtureModel[] = [
  { ...models[1], provider: "openrouter", id: ".hidden", name: "Hidden Fixture Model" },
];

const localeModels: FixtureModel[] = [
  { ...models[1], provider: "Beta", id: "model", name: "Beta Model" },
  { ...models[1], provider: "alpha", id: "~a", name: "Tilde" },
  { ...models[1], provider: "alpha", id: "a_a", name: "Underscore" },
  { ...models[1], provider: "alpha", id: "a-a", name: "Lowercase" },
  { ...models[1], provider: "alpha", id: "A-a", name: "Uppercase" },
];

const partialLocaleModels = localeModels.filter((model) => model.provider === "alpha");

const alphaNumericModels: FixtureModel[] = [
  { ...models[1], id: "gpt-5.2-codex", name: "GPT-5.2 Codex" },
];

const fixedRoundingModels: FixtureModel[] = [
  { ...models[1], id: "fixed-rounding", name: "Fixed Rounding", contextWindow: 1250, maxTokens: 2550 },
];

const patterns = [
  "claude-sonnet-4-5",
  "sonnet",
  "nonexistent",
  "sonnet:high",
  "gpt-4o:medium",
  "sonnet:off",
  "sonnet:minimal",
  "sonnet:low",
  "sonnet:xhigh",
  "sonnet:max",
  "sonnet:random",
  "qwen/qwen3-coder:exacto",
  "openrouter/qwen/qwen3-coder:exacto",
  "qwen/qwen3-coder:exacto:high",
  "openrouter/qwen/qwen3-coder:exacto:high",
  "openai/gpt-4o:extended",
  "qwen/qwen3-coder:exacto:random",
  "qwen/qwen3-coder:exacto:high:random",
  "",
  "sonnet:",
  " openai / gpt-4o ",
];

function docsExample(markdown: string, heading: string): string {
  const escaped = heading.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = markdown.match(
    new RegExp("## " + escaped + "\\n[\\s\\S]*?```json\\n([\\s\\S]*?)\\n```")
  );
  if (!match) throw new Error(`missing JSON block under ${heading}`);
  return match[1];
}

function normalizedResult(result: {
  model?: FixtureModel;
  thinkingLevel?: string;
  warning?: string;
}): object {
  return {
    model: result.model ? { provider: result.model.provider, id: result.model.id } : null,
    thinkingLevel: result.thinkingLevel ?? null,
    warning: result.warning ?? null,
  };
}

function normalizeModel(model: FixtureModel): object {
  return {
    id: model.id,
    name: model.name,
    api: model.api,
    provider: model.provider,
    baseUrl: model.baseUrl,
    reasoning: model.reasoning,
    input: model.input,
    cost: model.cost,
    contextWindow: model.contextWindow,
    maxTokens: model.maxTokens,
  };
}

async function captureList(
  listModels: (runtime: unknown, search?: string) => Promise<void>,
  search?: string,
  available: FixtureModel[] = models,
): Promise<string> {
  const lines: string[] = [];
  const originalLog = console.log;
  console.log = (...values: unknown[]) => lines.push(values.map(String).join(" "));
  try {
    await listModels({ getError: () => undefined, getAvailable: async () => available }, search);
  } finally {
    console.log = originalLog;
  }
  return `${lines.join("\n")}\n`;
}

export async function generateWP250(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  await withUpstreamModelData(upstreamRoot, async () => {
    const resolverPath = pathToFileURL(
      path.join(upstreamRoot, "packages/coding-agent/src/core/model-resolver.ts"),
    ).href;
    const listPath = pathToFileURL(
      path.join(upstreamRoot, "packages/coding-agent/src/cli/list-models.ts"),
    ).href;
    const runtimePath = pathToFileURL(
      path.join(upstreamRoot, "packages/coding-agent/src/core/model-runtime.ts"),
    ).href;
    const storePath = pathToFileURL(
      path.join(upstreamRoot, "packages/coding-agent/src/core/models-store.ts"),
    ).href;
    const simpleOptionsPath = pathToFileURL(
      path.join(upstreamRoot, "packages/ai/src/api/simple-options.ts"),
    ).href;
    const [{ parseModelPattern, resolveModelScopeWithDiagnostics }, { listModels }, { ModelRuntime }, { FileModelsStore }, { buildBaseOptions }] = await Promise.all([
      import(resolverPath),
      import(listPath),
      import(runtimePath),
      import(storePath),
      import(simpleOptionsPath),
    ]);

    const patternCases: object[] = patterns.map((pattern) => ({
      pattern,
      expected: normalizedResult(parseModelPattern(pattern, models)),
    }));
    patternCases.push({
      pattern: "a",
      models: partialLocaleModels,
      expected: normalizedResult(parseModelPattern("a", partialLocaleModels)),
    });

    const scopeCases: object[] = [];
    for (const definition of [
      { patterns: ["openrouter/**"], models },
      { patterns: ["openrouter/*"], models },
      { patterns: ["**/qwen/**:high"], models },
      { patterns: ["openrouter/**", "openrouter/qwen/**"], models },
      { patterns: ["{ANTHROPIC,OPENROUTER}/**"], models },
      { patterns: ["@(ANTHROPIC|OPENROUTER)/**"], models },
      { patterns: ["OPENROUTER/!(QWEN)/**:high"], models },
      { patterns: ["openrouter/**"], models: hiddenModels },
    ]) {
      const scopePatterns = definition.patterns;
      const result = await resolveModelScopeWithDiagnostics(scopePatterns, {
        getAvailable: async () => definition.models,
      });
      scopeCases.push({
        patterns: scopePatterns,
        ...(definition.models === models ? {} : { models: definition.models }),
        expected: {
          models: result.scopedModels.map((entry: { model: FixtureModel; thinkingLevel?: string }) => ({
            provider: entry.model.provider,
            id: entry.model.id,
            thinkingLevel: entry.thinkingLevel ?? null,
          })),
          diagnostics: result.diagnostics,
        },
      });
    }

    const listOutput = await captureList(listModels);
    const listCases: Array<{
      name: string;
      search: string;
      expected: string;
      empty?: boolean;
      models?: FixtureModel[];
    }> = [];
    for (const search of ["qwen", "g4", "4g", "openrouter exacto", "missing"]) {
      listCases.push({ name: `search-${search}`, search, expected: await captureList(listModels, search) });
    }
    const emptyList = (await captureList(listModels, undefined, []))
      .replace(/  .*\/docs\/providers\.md/u, "  docs/providers.md")
      .replace(/  .*\/docs\/models\.md/u, "  docs/models.md");
    listCases.push({ name: "empty", search: "", empty: true, expected: emptyList });
    listCases.push({
      name: "locale-compare-order",
      search: "",
      models: localeModels,
      expected: await captureList(listModels, undefined, localeModels),
    });
    listCases.push({
      name: "alpha-numeric-fuzzy-swap",
      search: "codex52",
      models: alphaNumericModels,
      expected: await captureList(listModels, "codex52", alphaNumericModels),
    });
    listCases.push({
      name: "javascript-fixed-rounding",
      search: "",
      models: fixedRoundingModels,
      expected: await captureList(listModels, undefined, fixedRoundingModels),
    });

    const docsPath = path.join(upstreamRoot, "packages/coding-agent/docs/models.md");
    const markdown = await readFile(docsPath, "utf8");
    const tempRoot = await mkdtemp(path.join(os.tmpdir(), "pi-go-wp250-"));
    const docsCases: object[] = [];
    const validationCases: object[] = [];
    const compositionCases: object[] = [];
    let fractionalNumbers: object = {};
    let storeFixture = "";
    try {
      for (const heading of ["Minimal Example", "Full Example"]) {
        const source = docsExample(markdown, heading);
        const modelsPath = path.join(tempRoot, `${heading.replaceAll(" ", "-").toLowerCase()}.json`);
        await writeFile(modelsPath, `${source}\n`);
        const runtime = await ModelRuntime.create({
          authPath: path.join(tempRoot, "auth.json"),
          modelsPath,
          modelsStorePath: path.join(tempRoot, "models-store.json"),
          allowModelNetwork: false,
        });
        docsCases.push({
          heading,
          config: JSON.parse(source),
          models: runtime.getModels("ollama").map(normalizeModel),
          error: runtime.getError() ?? null,
        });
      }
      for (const [name, config] of Object.entries({
        "incomplete cost": { providers: { p: { models: [{ id: "m", cost: { input: 1 } }] } } },
        "invalid input": { providers: { p: { models: [{ id: "m", input: ["audio"] }] } } },
        "invalid thinking value": { providers: { p: { models: [{ id: "m", thinkingLevelMap: { off: 3 } }] } } },
        "invalid common compat": { providers: { p: { compat: { supportsLongCacheRetention: "yes" } } } },
        "unknown thinking key": { providers: { p: { baseUrl: "https://example.invalid/v1", api: "openai-completions", apiKey: "test", models: [{ id: "m", thinkingLevelMap: { ultra: "high" } }] } } },
        "fractional compat numbers": {
          providers: {
            p: {
              baseUrl: "https://example.invalid/v1",
              api: "openai-completions",
              apiKey: "test",
              compat: {
                chatTemplateKwargs: { fractional: 1.125 },
                openRouterRouting: {
                  max_price: { prompt: 0.5 },
                  preferred_min_throughput: { p50: 1.25, p75: 2.5 },
                },
              },
            },
          },
        },
      })) {
        const modelsPath = path.join(tempRoot, `validation-${name.replaceAll(" ", "-")}.json`);
        await writeFile(modelsPath, JSON.stringify(config));
        const runtime = await ModelRuntime.create({
          authPath: path.join(tempRoot, "auth.json"),
          modelsPath,
          modelsStorePath: path.join(tempRoot, "models-store.json"),
          allowModelNetwork: false,
        });
        validationCases.push({ name, config, accepted: runtime.getError() === undefined });
      }
      const compositionConfig = {
        providers: {
          anthropic: {},
          empty: {},
          bad: { models: [{ id: "broken" }] },
          nonpositive: {
            baseUrl: "https://example.invalid/v1",
            api: "openai-completions",
            models: [{ id: "zero", contextWindow: 0 }],
          },
          good: {
            baseUrl: "https://example.invalid/v1",
            api: "openai-completions",
            apiKey: "test",
            models: [{ id: "working" }],
          },
        },
      };
      const compositionPath = path.join(tempRoot, "composition-siblings.json");
      await writeFile(compositionPath, JSON.stringify(compositionConfig));
      const compositionRuntime = await ModelRuntime.create({
        authPath: path.join(tempRoot, "auth.json"),
        modelsPath: compositionPath,
        modelsStorePath: path.join(tempRoot, "models-store.json"),
        allowModelNetwork: false,
      });
      compositionCases.push({
        name: "invalid-provider-keeps-valid-sibling",
        config: compositionConfig,
        goodModels: compositionRuntime.getModels("good").map(normalizeModel),
        badModels: compositionRuntime.getModels("bad").map(normalizeModel),
        nonpositiveModels: compositionRuntime.getModels("nonpositive").map(normalizeModel),
        emptyModels: compositionRuntime.getModels("empty").map(normalizeModel),
        anthropicProviderPreserved: compositionRuntime.getProvider("anthropic") !== undefined,
        error: compositionRuntime.getError() ?? null,
      });
      const fractionalConfig = {
        providers: {
          fractional: {
            baseUrl: "https://example.invalid/v1",
            api: "openai-completions",
            apiKey: "test",
            models: [
              {
                id: "tiny",
                contextWindow: 1.5,
                maxTokens: 2.25,
                cost: {
                  input: 0,
                  output: 0,
                  cacheRead: 0,
                  cacheWrite: 0,
                  tiers: [{ inputTokensAbove: 10.5, input: 1, output: 2, cacheRead: 0.1, cacheWrite: 0.2 }],
                },
              },
              { id: "runtime", contextWindow: 5000.5, maxTokens: 2.25 },
            ],
            modelOverrides: {
              runtime: {
                cost: {
                  tiers: [{ inputTokensAbove: 20.25, input: 3, output: 4, cacheRead: 0.3, cacheWrite: 0.4 }],
                },
              },
            },
          },
        },
      };
      const fractionalPath = path.join(tempRoot, "fractional-numbers.json");
      await writeFile(fractionalPath, JSON.stringify(fractionalConfig));
      const fractionalRuntime = await ModelRuntime.create({
        authPath: path.join(tempRoot, "auth.json"),
        modelsPath: fractionalPath,
        modelsStorePath: path.join(tempRoot, "models-store.json"),
        allowModelNetwork: false,
      });
      const fractionalModels = fractionalRuntime.getModels("fractional");
      const runtimeModel = fractionalModels.find((model: FixtureModel) => model.id === "runtime");
      if (!runtimeModel) throw new Error("fractional runtime model was not composed");
      listCases.push({
        name: "fractional-token-counts",
        search: "",
        models: fractionalModels,
        expected: await captureList(listModels, undefined, fractionalModels),
      });
      fractionalNumbers = {
        config: fractionalConfig,
        models: fractionalModels.map(normalizeModel),
        list: await captureList(listModels, undefined, fractionalModels),
        simpleOptions: {
          defaultMaxTokens: buildBaseOptions(runtimeModel, { messages: [] }).maxTokens,
          requestedMaxTokens: buildBaseOptions(runtimeModel, { messages: [] }, { maxTokens: 1.75 }).maxTokens,
        },
        modelJSON: fractionalModels.map((model: FixtureModel) => JSON.stringify(normalizeModel(model))),
        integerModelJSON: JSON.stringify(normalizeModel(models[0])),
        error: fractionalRuntime.getError() ?? null,
      };
      const persistedPath = path.join(tempRoot, "fixture-models-store.json");
      const store = new FileModelsStore(persistedPath);
      await store.write("z-preserved", {
        models: [{ ...models[1], provider: "z-preserved" }],
        checkedAt: 111,
      });
      await store.write("a-refreshed", {
        models: [{ ...models[0], provider: "a-refreshed" }],
        checkedAt: 123456789,
      });
      storeFixture = await readFile(persistedPath, "utf8");
    } finally {
      await rm(tempRoot, { recursive: true, force: true });
    }

    const directory = path.join(outputRoot, "WP250");
    await mkdir(directory, { recursive: true });
    await Promise.all([
      writeFile(
        path.join(directory, "patterns.json"),
        `${JSON.stringify({ models, cases: patternCases, scopes: scopeCases }, null, 2)}\n`,
      ),
      writeFile(path.join(directory, "list.txt"), listOutput),
      writeFile(path.join(directory, "list-cases.json"), `${JSON.stringify(listCases, null, 2)}\n`),
      writeFile(path.join(directory, "docs-examples.json"), `${JSON.stringify(docsCases, null, 2)}\n`),
      writeFile(path.join(directory, "validation-cases.json"), `${JSON.stringify(validationCases, null, 2)}\n`),
      writeFile(path.join(directory, "composition-cases.json"), `${JSON.stringify(compositionCases, null, 2)}\n`),
      writeFile(path.join(directory, "fractional-numbers.json"), `${JSON.stringify(fractionalNumbers, null, 2)}\n`),
      writeFile(path.join(directory, "models-store.json"), storeFixture),
      writeFile(
        path.join(directory, "manifest.json"),
        `${JSON.stringify(
          {
            family: "WP250",
            upstreamCommit,
            generator: "conformance/extract/wp250-models.ts",
            source: "packages/coding-agent/src/core/model-resolver.ts",
            additionalSources: [
              "packages/coding-agent/src/cli/list-models.ts",
              "packages/coding-agent/src/core/model-runtime.ts",
              "packages/coding-agent/src/core/model-config.ts",
              "packages/coding-agent/src/core/provider-composer.ts",
              "packages/coding-agent/src/core/models-store.ts",
              "packages/ai/src/api/simple-options.ts",
              "packages/coding-agent/docs/models.md",
            ],
            files: ["patterns.json", "list.txt", "list-cases.json", "docs-examples.json", "validation-cases.json", "composition-cases.json", "fractional-numbers.json", "models-store.json"],
          },
          null,
          2,
        )}\n`,
      ),
    ]);
  });
}
