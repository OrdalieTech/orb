import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

import { AuthStorage } from "../../.upstream/packages/coding-agent/src/core/auth-storage.ts";
import { migrateAuthToAuthJson } from "../../.upstream/packages/coding-agent/src/migrations.ts";
import { oauthErrorHtml, oauthSuccessHtml } from "../../.upstream/packages/ai/src/auth/oauth/oauth-page.ts";

const FIXED_EXPIRES = 1_700_003_300_000;

async function extractAuthMigrationFixture(temporaryRoot: string): Promise<unknown> {
  const migrationRoot = path.join(temporaryRoot, "migration");
  const oauthPath = path.join(migrationRoot, "oauth.json");
  const settingsPath = path.join(migrationRoot, "settings.json");
  const authPath = path.join(migrationRoot, "auth.json");
  const initialOAuthRaw = `{
  "zeta": { "access": "zeta-access", "refresh": "zeta-refresh", "expires": 1700000100000 },
  "10": { "access": "ten-access", "refresh": "ten-refresh", "expires": 1700000200000 },
  "2": { "access": "two-access", "refresh": "two-refresh", "expires": 1700000300000 },
  "anthropic": { "access": "anthropic-access", "refresh": "anthropic-refresh", "expires": 1700000400000, "account": { "plan": "max" } }
}`;
  const initialSettingsRaw = `{
  "theme": "dark",
  "10": "ten-setting",
  "2": "two-setting",
  "apiKeys": {
    "z-provider": "z-key",
    "3": "three-key",
    "1": "one-key",
    "anthropic": "duplicate-key",
    "invalid": { "ignored": true },
    "openai": "openai-key"
  },
  "negativeZero": -0
}`;

  await mkdir(migrationRoot, { recursive: true });
  await writeFile(oauthPath, initialOAuthRaw, { encoding: "utf8", mode: 0o600 });
  await writeFile(settingsPath, initialSettingsRaw, { encoding: "utf8", mode: 0o600 });
  const previousAgentDir = process.env.PI_CODING_AGENT_DIR;
  process.env.PI_CODING_AGENT_DIR = migrationRoot;
  try {
    const providers = migrateAuthToAuthJson();
    return {
      initialOAuthRaw,
      initialSettingsRaw,
      expectedProviders: providers,
      expectedAuthRaw: await readFile(authPath, "utf8"),
      expectedSettingsRaw: await readFile(settingsPath, "utf8"),
      oauthRenamed: await readFile(`${oauthPath}.migrated`, "utf8"),
    };
  } finally {
    if (previousAgentDir === undefined) delete process.env.PI_CODING_AGENT_DIR;
    else process.env.PI_CODING_AGENT_DIR = previousAgentDir;
  }
}

export async function extractAuthStorageFixture(): Promise<unknown> {
  const temporaryRoot = await mkdtemp(path.join(tmpdir(), "pi-go-f2-auth-"));
  const authPath = path.join(temporaryRoot, "auth.json");
  const previousAmbient = process.env.AUTH_FIXTURE_AMBIENT;
  process.env.AUTH_FIXTURE_AMBIENT = "ambient-key";
  try {
    const initial = {
      anthropic: {
        type: "api_key",
        key: "$AUTH_FIXTURE_SCOPED",
        env: { AUTH_FIXTURE_SCOPED: "scoped-key", REGION: "fixture-region" },
      },
      openai: { type: "api_key", key: "!printf 'command-key'" },
      "github-copilot": {
        type: "oauth",
        access: "copilot-access",
        refresh: "copilot-refresh",
        expires: 1_700_000_100_000,
        enterpriseUrl: "https://github.example.test",
        availableModelIds: ["model-a", "model-b"],
        env: { Z_LAST: "z", A_FIRST: "a" },
      },
    } as const;
    const initialRaw = JSON.stringify(initial, null, 2);
    await writeFile(authPath, initialRaw, { encoding: "utf8", mode: 0o600 });
    const storage = AuthStorage.create(authPath);
    const reads = {
      anthropic: await storage.read("anthropic"),
      openai: await storage.read("openai"),
      "github-copilot": await storage.read("github-copilot"),
    };
    const initialList = await storage.list();

    const anthropicCredential = {
      type: "oauth",
      refresh: "anthropic-refresh",
      access: "sk-ant-oat-fixture",
      expires: FIXED_EXPIRES,
      account: { id: "account-fixture", plan: "max" },
    } as const;
    const customCredential = {
      type: "api_key",
      key: "${AUTH_FIXTURE_AMBIENT}",
      env: { EXTRA: "preserved" },
    } as const;
    await storage.modify("anthropic", async () => anthropicCredential);
    await storage.modify("custom", async () => customCredential);
    await storage.delete("openai");

    return {
      initialRaw,
      initialReads: reads,
      initialList,
      operations: [
        { type: "modify", provider: "anthropic", credential: anthropicCredential },
        { type: "modify", provider: "custom", credential: customCredential },
        { type: "delete", provider: "openai" },
      ],
      expectedRaw: await readFile(authPath, "utf8"),
      expectedList: await storage.list(),
      oauthPages: {
        success: oauthSuccessHtml(`A&B <done> "quoted" 'single'`),
        error: oauthErrorHtml(`A&B <failed> "quoted" 'single'`, `detail & <trace> "quoted" 'single'`),
      },
      migration: await extractAuthMigrationFixture(temporaryRoot),
    };
  } finally {
    if (previousAmbient === undefined) delete process.env.AUTH_FIXTURE_AMBIENT;
    else process.env.AUTH_FIXTURE_AMBIENT = previousAmbient;
    await rm(temporaryRoot, { recursive: true, force: true });
  }
}
