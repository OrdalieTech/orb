import { access, writeFile } from "node:fs/promises";

import { AuthStorage } from "../../.upstream/packages/coding-agent/src/core/auth-storage.ts";

const authPath = process.argv[2];
if (!authPath) throw new Error("auth.json path is required");
const mode = process.argv[3];

if (mode === "hold-lock") {
  const readyPath = process.argv[4];
  const releasePath = process.argv[5];
  if (!readyPath || !releasePath) throw new Error("lock marker paths are required");
  const storage = AuthStorage.create(authPath);
  await storage.modify("typescript-lock", async () => {
    await writeFile(readyPath, "ready", "utf8");
    for (;;) {
      try {
        await access(releasePath);
        break;
      } catch {
        await new Promise((resolve) => setTimeout(resolve, 10));
      }
    }
    return { type: "api_key", key: "typescript" };
  });
} else {
  delete process.env.AUTH_FIXTURE_AMBIENT;

  const storage = AuthStorage.create(authPath);
  const listed = await storage.list();
  const anthropic = await storage.read("anthropic");
  const custom = await storage.read("custom");

  if (JSON.stringify(listed) !== JSON.stringify([
    { providerId: "anthropic", type: "oauth" },
    { providerId: "github-copilot", type: "oauth" },
    { providerId: "custom", type: "api_key" },
  ])) {
    throw new Error(`TS pi list mismatch: ${JSON.stringify(listed)}`);
  }
  const anthropicAccount = anthropic?.type === "oauth" ? anthropic.account as { plan?: string } | undefined : undefined;
  if (anthropic?.type !== "oauth" || anthropic.access !== "sk-ant-oat-fixture" || anthropicAccount?.plan !== "max") {
    throw new Error(`TS pi Anthropic credential mismatch: ${JSON.stringify(anthropic)}`);
  }
  if (custom?.type !== "api_key" || custom.key !== undefined || custom.env?.EXTRA !== "preserved") {
    throw new Error(`TS pi custom credential mismatch: ${JSON.stringify(custom)}`);
  }
}
