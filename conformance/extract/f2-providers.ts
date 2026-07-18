import { readFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { withUpstreamModelData } from "./upstream-model-data.ts";

const apiFactories: Record<string, string> = {
  anthropicMessagesApi: "anthropic-messages",
  azureOpenAIResponsesApi: "azure-openai-responses",
  bedrockConverseStreamApi: "bedrock-converse-stream",
  googleGenerativeAIApi: "google-generative-ai",
  googleVertexApi: "google-vertex",
  mistralConversationsApi: "mistral-conversations",
  openAICodexResponsesApi: "openai-codex-responses",
  openAICompletionsApi: "openai-completions",
  openAIResponsesApi: "openai-responses",
};

interface UpstreamProvider {
  id: string;
  name: string;
  baseUrl?: string;
  auth: {
    apiKey?: {
      name: string;
      resolve(input: {
        ctx: {
          env(name: string): Promise<string | undefined>;
          fileExists(path: string): Promise<boolean>;
        };
      }): Promise<
        | {
            auth: { apiKey?: string; headers?: Record<string, string | null> };
            env?: Record<string, string>;
          }
        | undefined
      >;
    };
    oauth?: unknown;
  };
}

function providerApis(source: string): string[] {
  const objectApis = [...source.matchAll(/^\s*"([a-z][a-z-]+)":\s*[A-Za-z]/gm)]
    .map((match) => match[1])
    .filter((value) => Object.values(apiFactories).includes(value));
  if (objectApis.length > 0) return objectApis;

  const singular = source.match(/api:\s*(?:cloudflareStreams\()?([A-Za-z]+Api)\(/);
  if (!singular || !apiFactories[singular[1]]) {
    throw new Error("could not derive provider API from pinned source");
  }
  return [apiFactories[singular[1]]];
}

export async function extractProvidersF2(upstreamRoot: string) {
  return withUpstreamModelData(upstreamRoot, async () => {
    const module = (await import(
      `${pathToFileURL(path.join(upstreamRoot, "packages/ai/src/providers/all.ts")).href}?wp270`
    )) as { builtinProviders(): UpstreamProvider[] };
    const builtin = module.builtinProviders();
    const result = [];
    for (const provider of builtin) {
      if (provider.id === "radius") continue;
      const source = await readFile(
        path.join(upstreamRoot, "packages/ai/src/providers", `${provider.id}.ts`),
        "utf8",
      );
      const env: string[] = [];
      const apiKey = provider.auth.apiKey;
      if (apiKey) {
        await apiKey.resolve({
          ctx: {
            env: async (name) => {
              if (!env.includes(name)) env.push(name);
              return undefined;
            },
            fileExists: async () => false,
          },
        });
      }
      result.push({
        id: provider.id,
        name: provider.name,
        ...(provider.baseUrl === undefined ? {} : { baseUrl: provider.baseUrl }),
        apis: providerApis(source),
        auth: {
          kind: apiKey ? "api_key" : "oauth",
          ...(apiKey ? { name: apiKey.name, env } : {}),
          oauth: provider.auth.oauth !== undefined,
        },
      });
    }
    const cloudflareModule = (await import(
      `${pathToFileURL(path.join(upstreamRoot, "packages/ai/src/providers/cloudflare-stream.ts")).href}?wp270`
    )) as {
      resolveCloudflareModel<T extends { baseUrl: string }>(model: T, env: Record<string, string>): T;
    };
    const cloudflareValues: Record<string, string> = {
      CLOUDFLARE_API_KEY: "fixture-cloudflare-key",
      CLOUDFLARE_ACCOUNT_ID: "fixture-account",
      CLOUDFLARE_GATEWAY_ID: "fixture-gateway",
    };
    const resolveCloudflare = async (providerId: string, baseUrl: string) => {
      const provider = builtin.find((entry) => entry.id === providerId);
      const apiKey = provider?.auth.apiKey;
      if (!apiKey) throw new Error(`${providerId} did not expose API-key auth`);
      const resolved = await apiKey.resolve({
        ctx: {
          env: async (name) => cloudflareValues[name],
          fileExists: async () => false,
        },
      });
      if (!resolved?.env) throw new Error(`${providerId} did not resolve provider environment`);
      return {
        provider: providerId,
        baseUrl,
        env: resolved.env,
        auth: resolved.auth,
        resolvedBaseUrl: cloudflareModule.resolveCloudflareModel({ baseUrl }, resolved.env).baseUrl,
      };
    };
    return {
      providers: result,
      cloudflare: [
        await resolveCloudflare(
          "cloudflare-workers-ai",
          "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1",
        ),
        await resolveCloudflare(
          "cloudflare-ai-gateway",
          "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai",
        ),
      ],
    };
  });
}
