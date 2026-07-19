import { cp, mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { withUpstreamModelData } from "./upstream-model-data.ts";

const exampleNames = [
  "hello",
  "pirate",
  "todo",
  "summarize",
  "commands",
  "send-user-message",
  "dynamic-tools",
  "structured-output",
  "tool-override",
  "event-bus",
  "file-trigger",
  "protected-paths",
  "git-checkpoint",
  "dirty-repo-guard",
  "claude-rules",
  "confirm-destructive",
  "custom-footer",
  "custom-header",
  "github-issue-autocomplete",
  "hidden-thinking-label",
  "mac-system-theme",
  "model-status",
  "permission-gate",
  "project-trust",
  "rpc-demo",
  "status-line",
  "system-prompt-header",
  "timed-confirm",
  "titlebar-spinner",
  "widget-placement",
  "working-indicator",
  "working-message-test",
  "border-status-editor",
  "interactive-shell",
  "modal-editor",
  "rainbow-editor",
  "snake",
  "space-invaders",
  "tools",
];

export async function generateF11JSBridge(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  await withUpstreamModelData(upstreamRoot, async () => {
    const loaderSource = "packages/coding-agent/src/core/extensions/loader.ts";
    const loader = (await import(pathToFileURL(path.join(upstreamRoot, loaderSource)).href)) as any;
    const temporaryRoot = await mkdtemp(path.join(os.tmpdir(), "pi-go-f11-jsbridge-"));
    try {
      const cwd = path.join(temporaryRoot, "project");
      const agentDir = path.join(temporaryRoot, "agent");
      const localDir = path.join(cwd, ".pi", "extensions");
      const globalDir = path.join(agentDir, "extensions");
      const configuredDir = path.join(temporaryRoot, "configured");
      const missing = path.join(temporaryRoot, "missing.ts");
      const extension = "export default function () {}\n";

      await mkdir(path.join(localDir, "bundle"), { recursive: true });
      await mkdir(globalDir, { recursive: true });
      await mkdir(path.join(configuredDir, "src"), { recursive: true });
      await writeFile(path.join(localDir, "a.ts"), extension);
      await writeFile(path.join(localDir, "bundle", "index.ts"), extension);
      await writeFile(path.join(globalDir, "global.js"), extension);
      await writeFile(path.join(configuredDir, "src", "configured.ts"), extension);
      await writeFile(path.join(configuredDir, "package.json"), JSON.stringify({
        pi: { extensions: ["src/configured.ts", "missing.ts"] },
      }));

      const discovered = await loader.discoverAndLoadExtensions([configuredDir, missing], cwd, agentDir);
      const normalize = (value: string) => value.replaceAll(temporaryRoot, "<root>").split(path.sep).join("/");
      const discovery = {
        paths: discovered.extensions.map((item: any) => normalize(item.resolvedPath)),
        errors: discovered.errors.map((item: any) => ({
          path: normalize(item.path),
          prefix: String(item.error).split(":")[0],
        })),
      };

      const syntaxPath = path.join(temporaryRoot, "syntax-error.ts");
      await writeFile(syntaxPath, "export default function (pi) {\n  const valid = 1;\n  const broken: = 2;\n}\n");
      const syntax = await loader.loadExtensions([syntaxPath], temporaryRoot);
      if (syntax.errors.length !== 1) throw new Error("upstream syntax-error fixture did not fail once");
      const syntaxError = String(syntax.errors[0].error);
      const syntaxLocation = syntaxError.match(/syntax-error\.ts:(\d+):/);
      if (!syntaxLocation) throw new Error(`upstream syntax error has no source location: ${syntaxError}`);
      const syntaxLine = Number(syntaxLocation[1]);

      const noDefaultPath = path.join(temporaryRoot, "no-default.ts");
      await writeFile(noDefaultPath, "export function named() {}\n");
      const noDefault = await loader.loadExtensions([noDefaultPath], temporaryRoot);
      if (noDefault.errors.length !== 1) throw new Error("upstream no-default fixture did not fail once");
      const loadErrors = {
        syntax: { prefix: syntaxError.split(":")[0], line: syntaxLine },
        invalidFactoryPrefix: String(noDefault.errors[0].error).split(":")[0],
      };

      const familyDir = path.join(outputRoot, "F11-jsbridge");
      await mkdir(familyDir, { recursive: true });
      await writeFile(path.join(familyDir, "discovery.json"), `${JSON.stringify(discovery, null, 2)}\n`);
      await writeFile(path.join(familyDir, "load-errors.json"), `${JSON.stringify(loadErrors, null, 2)}\n`);
      await generateF11JSBridgeExampleSeeds(upstreamRoot, outputRoot, upstreamCommit);
    } finally {
      await rm(temporaryRoot, { recursive: true, force: true });
    }
  });
}

export async function generateF11JSBridgeExampleSeeds(
  upstreamRoot: string,
  outputRoot: string,
  upstreamCommit: string,
): Promise<void> {
  const loaderSource = "packages/coding-agent/src/core/extensions/loader.ts";
  const familyDir = path.join(outputRoot, "F11-jsbridge");
  await mkdir(familyDir, { recursive: true });
  for (const name of exampleNames) {
    await cp(
      path.join(upstreamRoot, `packages/coding-agent/examples/extensions/${name}.ts`),
      path.join(familyDir, `${name}.ts`),
    );
  }
  await writeFile(path.join(familyDir, "manifest.json"), `${JSON.stringify({
    family: "F11-jsbridge",
    upstreamCommit,
    generator: "conformance/extract/f11-jsbridge.ts",
    source: `${loaderSource}; packages/coding-agent/examples/extensions/{${exampleNames.join(",")}}.ts`,
    files: [...exampleNames.map((name) => `${name}.ts`), "discovery.json", "load-errors.json"],
  }, null, 2)}\n`);
}
