package jsbridge

// Regression tests for the jsbridge cluster of the 6-dimension real-world
// compat sweep: pi-ai event streams + calculateCost, pi-coding-agent
// getAgentDir/getMarkdownTheme/parseFrontmatter, pi-tui Key, Node builtins
// (crypto/http/module) and globals (atob/btoa/TextDecoder), import.meta.url,
// Node-coded fs errors, guarded unknown shim imports, and compact callbacks.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

// --- finding 1: pi-ai createAssistantMessageEventStream + calculateCost ---

func TestAIEventStreamPushEndAndResult(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { createAssistantMessageEventStream, AssistantMessageEventStream, EventStream } from "@earendil-works/pi-ai";
export default async function(pi) {
  if (typeof EventStream !== "function" || typeof AssistantMessageEventStream !== "function") {
    throw new Error("event stream classes missing");
  }
  const stream = createAssistantMessageEventStream();
  if (!(stream instanceof AssistantMessageEventStream)) throw new Error("factory type mismatch");
  const message = { role: "assistant", content: [{ type: "text", text: "hi" }] };
  setTimeout(() => {
    stream.push({ type: "text_delta", contentIndex: 0, delta: "hi" });
    stream.push({ type: "done", reason: "stop", message });
  }, 5);
  const seen = [];
  for await (const event of stream) {
    seen.push(event.type);
  }
  const final = await stream.result();
  if (final !== message) throw new Error("result() did not resolve the done message");
  pi.registerCommand("result", { description: seen.join(","), handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "text_delta,done")
}

func TestAIEventStreamEndWithoutResultEndsIteration(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";
export default async function(pi) {
  const stream = createAssistantMessageEventStream();
  stream.push({ type: "text_delta", contentIndex: 0, delta: "queued" });
  setTimeout(() => stream.end(), 5);
  const seen = [];
  for await (const event of stream) { seen.push(event.type); }
  pi.registerCommand("result", { description: seen.join(",") + ":ended", handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "text_delta:ended")
}

func TestAICalculateCostMirrorsGoAndMutatesUsage(t *testing.T) {
	model := ai.Model{}
	model.Cost.Input = 3
	model.Cost.Output = 15
	model.Cost.CacheRead = 0.3
	model.Cost.CacheWrite = 3.75
	usage := ai.Usage{Input: 1000, Output: 2000, CacheRead: 500, CacheWrite: 300}
	ai.CalculateCost(&model, &usage)
	want := fmt.Sprintf("%.10f|%.10f", usage.Cost.Total, usage.Cost.Output)

	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { calculateCost } from "@earendil-works/pi-ai";
export default function(pi) {
  const usage = { input: 1000, output: 2000, cacheRead: 500, cacheWrite: 300,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } };
  const originalCost = usage.cost;
  const model = { cost: { input: 3, output: 15, cacheRead: 0.3, cacheWrite: 3.75 } };
  const returned = calculateCost(model, usage);
  if (returned !== originalCost) throw new Error("calculateCost did not mutate usage.cost in place");
  pi.registerCommand("result", { description: usage.cost.total.toFixed(10) + "|" + usage.cost.output.toFixed(10), handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", want)
}

func TestProviderStreamSimpleAcceptsEventStream(t *testing.T) {
	cwd := t.TempDir()
	source := `
import { createAssistantMessageEventStream } from "@earendil-works/pi-ai";
export default function(pi) {
  pi.registerProvider("stream-provider", {
    name: "Stream Provider",
    baseUrl: "https://stream.invalid",
    api: "openai-responses",
    models: [{ id: "stream-model", name: "Stream Model", reasoning: false, input: ["text"],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 1000, maxTokens: 100 }],
    streamSimple: (_model, _context, _options) => {
      const stream = createAssistantMessageEventStream();
      const message = { role: "assistant", content: [{ type: "text", text: "pushed" }],
        api: "openai-responses", provider: "stream-provider", model: "stream-model",
        usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2,
          cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
        stopReason: "stop", timestamp: 1 };
      setTimeout(() => {
        stream.push({ type: "text_delta", contentIndex: 0, delta: "pushed", partial: message });
        stream.push({ type: "done", reason: "stop", message });
      }, 5);
      return stream;
    },
  });
}
`
	entry := filepath.Join(cwd, "provider.ts")
	mustWrite(t, entry, source)
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	configs := make(map[string]extensions.ProviderConfig)
	options := extensions.RunnerOptions{Actions: extensions.Actions{
		RegisterProviderConfig: func(id string, config extensions.ProviderConfig) error {
			configs[id] = config
			return nil
		},
	}}
	extensions.NewRunner(loaded.Registry, options)
	config, ok := configs["stream-provider"]
	if !ok || config.Stream == nil {
		t.Fatalf("provider config missing stream: %#v", configs)
	}
	model := ai.Model{ID: "stream-model", Provider: "stream-provider"}
	stream, err := config.Stream(context.Background(), &model, ai.Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for event, streamErr := range stream {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		switch typed := event.(type) {
		case ai.TextDeltaEvent:
			types = append(types, "text_delta:"+typed.Delta)
		case *ai.TextDeltaEvent:
			types = append(types, "text_delta:"+typed.Delta)
		case ai.DoneEvent, *ai.DoneEvent:
			types = append(types, "done")
		default:
			types = append(types, fmt.Sprintf("%T", event))
		}
	}
	if joined := strings.Join(types, ","); joined != "text_delta:pushed,done" {
		t.Fatalf("streamed event types = %q", joined)
	}
}

// --- finding 2: getAgentDir / getMarkdownTheme / parseFrontmatter ---

func TestGetAgentDirReturnsResolvedAgentDir(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { getAgentDir, VERSION } from "@earendil-works/pi-coding-agent";
export default function(pi) {
  pi.registerCommand("result", { description: getAgentDir() + "|" + VERSION, handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", filepath.Join(cwd, "agent")+"|"+upstreamPackageVersion)
}

func TestGetMarkdownThemeShape(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { getMarkdownTheme } from "@earendil-works/pi-coding-agent";
export default function(pi) {
  const theme = getMarkdownTheme();
  const names = ["heading", "link", "linkUrl", "code", "codeBlock", "codeBlockBorder", "quote",
    "quoteBorder", "hr", "listBullet", "bold", "italic", "underline", "strikethrough", "highlightCode"];
  for (const name of names) {
    if (typeof theme[name] !== "function") throw new Error(name + " missing from markdown theme");
  }
  const lines = theme.highlightCode("a\nb", "go");
  if (!Array.isArray(lines) || lines.length !== 2) throw new Error("highlightCode did not split lines");
  pi.registerCommand("result", { description: theme.heading("H") + ":" + lines.join("+"), handler: async () => {} });
}
`)
	// Headless (no current theme): style functions pass text through.
	assertRegisteredDescription(t, result, "result", "H:a+b")
}

func TestParseFrontmatterExport(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { parseFrontmatter, stripFrontmatter } from "@earendil-works/pi-coding-agent";
export default function(pi) {
  const doc = "---\nname: reviewer\ndescription: Reviews code\n---\n\nBody text\n";
  const parsed = parseFrontmatter(doc);
  const stripped = stripFrontmatter(doc);
  const plain = parseFrontmatter("no fences");
  if (Object.keys(plain.frontmatter).length !== 0 || plain.body !== "no fences") throw new Error("plain doc mishandled");
  pi.registerCommand("result", { description: parsed.frontmatter.name + "|" + parsed.frontmatter.description + "|" + parsed.body + "|" + stripped, handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "reviewer|Reviews code|Body text|Body text")
}

func TestToolOverrideExampleBlocksEnvRead(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, ".env"), "TOPSECRET")
	result := loadAndRunExtension(t, cwd, fixtureSource(t, "tool-override.ts"))
	runner := extensions.NewRunner(result.Registry, extensions.RunnerOptions{CWD: cwd})
	tool := runner.ToolDefinition("read")
	if tool == nil {
		t.Fatal("overridden read tool was not registered")
	}
	blocked, err := tool.Execute(context.Background(), "read-1", map[string]any{"path": ".env"}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	text := blocked.Content[0].(*ai.TextContent).Text
	if !strings.HasPrefix(text, `Access denied: ".env" matches a blocked pattern`) {
		t.Fatalf("blocked read = %q", text)
	}
	if strings.Contains(text, "TOPSECRET") {
		t.Fatal("blocked read leaked file contents")
	}
	allowedPath := filepath.Join(cwd, "notes.txt")
	mustWrite(t, allowedPath, "safe content")
	allowed, err := tool.Execute(context.Background(), "read-2", map[string]any{"path": "notes.txt"}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	if got := allowed.Content[0].(*ai.TextContent).Text; got != "safe content" {
		t.Fatalf("allowed read = %q", got)
	}
}

// --- finding 3: pi-tui Key builder ---

func TestKeyBuilderSurface(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { Key, isKeyRelease } from "@earendil-works/pi-tui";
export default function(pi) {
  const checks = [
    [Key.ctrlAlt("p"), "ctrl+alt+p"],
    [Key.ctrl("c"), "ctrl+c"],
    [Key.shift("tab"), "shift+tab"],
    [Key.alt("x"), "alt+x"],
    [Key.super("k"), "super+k"],
    [Key.ctrlShift("p"), "ctrl+shift+p"],
    [Key.ctrlShiftAlt("z"), "ctrl+shift+alt+z"],
    [Key.escape, "escape"],
    [Key.enter, "enter"],
    [Key.pageUp, "pageUp"],
    [Key.f12, "f12"],
    [Key.backtick, String.fromCharCode(96)],
    [Key.question, "?"],
  ];
  for (const [got, want] of checks) {
    if (got !== want) throw new Error("Key mismatch: " + got + " != " + want);
  }
  if (typeof isKeyRelease !== "function" || isKeyRelease("a") !== false) throw new Error("isKeyRelease missing");
  pi.registerCommand("result", { description: "keys-ok", handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "keys-ok")
}

func TestPlanModeDirectoryExampleLoads(t *testing.T) {
	loadUpstreamDirectoryExample(t, "plan-mode")
}

func TestSubagentDirectoryExampleLoads(t *testing.T) {
	cwd, runner := loadUpstreamDirectoryExampleRunner(t, "subagent")
	tool := runner.ToolDefinition("subagent")
	if tool == nil {
		t.Fatal("subagent tool was not registered")
	}
	if want := filepath.Join(cwd, "agent", "agents"); !strings.Contains(tool.Description, want) {
		t.Fatalf("subagent description %q does not embed getAgentDir() path %q", tool.Description, want)
	}
}

// --- finding 4: node builtins + globals ---

func TestNodeCryptoModule(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { randomUUID, randomBytes, createHash, createHmac } from "node:crypto";
export default function(pi) {
  const uuid = randomUUID();
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(uuid)) {
    throw new Error("randomUUID format: " + uuid);
  }
  if (randomBytes(16).length !== 16) throw new Error("randomBytes length");
  const hex = createHash("sha256").update("abc").digest("hex");
  const b64 = createHash("sha256").update("ab").update("c").digest("base64");
  const mac = createHmac("sha256", "key").update("The quick brown fox jumps over the lazy dog").digest("hex");
  pi.registerCommand("result", { description: [hex, b64, mac].join("|"), handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", strings.Join([]string{
		"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		"ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=",
		"f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8",
	}, "|"))
}

func TestNodeHTTPServerAndRequest(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { createServer, request } from "node:http";
export default async function(pi) {
  const outcome = await new Promise((resolve, reject) => {
    const server = createServer((req, res) => {
      let body = "";
      req.on("data", (chunk) => { body += chunk.toString(); });
      req.on("end", () => {
        res.statusCode = 201;
        res.setHeader("X-Echo", req.headers["x-in"] || "");
        res.end("pong:" + req.method + ":" + req.url + ":" + body);
      });
    });
    server.listen(0, () => {
      const port = server.address().port;
      const client = request({ hostname: "127.0.0.1", port: String(port), path: "/probe", method: "POST", headers: { "X-In": "marker" } }, (res) => {
        let payload = "";
        res.on("data", (chunk) => { payload += chunk.toString(); });
        res.on("end", () => {
          server.close();
          resolve(res.statusCode + "|" + res.headers["x-echo"] + "|" + payload);
        });
      });
      client.on("error", reject);
      client.write("ping");
      client.end();
    });
  });
  pi.registerCommand("result", { description: outcome, handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "201|marker|pong:POST:/probe:ping")
}

func TestNodeModuleCreateRequire(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { createRequire } from "node:module";
export default function(pi) {
  const localRequire = createRequire(import.meta.url);
  const joined = localRequire("node:path").join("a", "b");
  const os = localRequire("os");
  pi.registerCommand("result", { description: joined + "|" + typeof os.platform, handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "a/b|function")
}

func TestEncodingGlobals(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
export default function(pi) {
  const encoded = btoa("hello");
  const decoded = atob(encoded);
  const unpadded = atob("aGVsbG8");
  const text = new TextDecoder().decode(new TextEncoder().encode("héllo"));
  const original = { list: [1, { deep: true }], when: new Date(42) };
  const cloned = structuredClone(original);
  const clonedOk = cloned !== original && cloned.list[1] !== original.list[1] &&
    cloned.list[1].deep === true && cloned.when.getTime() === 42;
  pi.registerCommand("result", { description: [encoded, decoded, unpadded, text, clonedOk].join("|"), handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "aGVsbG8=|hello|hello|héllo|true")
}

func TestNodeHTTPSModuleResolves(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { request, get } from "node:https";
export default function(pi) {
  pi.registerCommand("result", { description: typeof request + "|" + typeof get, handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "function|function")
}

func TestEcosystemPrototypePatchStubs(t *testing.T) {
	// pi-skillful patches InteractiveMode.prototype.showLoadedResources and
	// @zigai/pi-footer patches TUI.prototype.render, both guarded by a
	// "typeof original !== 'function'" check. The bridge exports bare classes
	// so those guards skip gracefully instead of crashing on undefined.
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { InteractiveMode } from "@earendil-works/pi-coding-agent";
import { TUI } from "@earendil-works/pi-tui";
export default function(pi) {
  function tryPatch(target, method) {
    const prototype = target.prototype;
    if (!prototype) return "no-prototype";
    if (typeof prototype[method] !== "function") return "skipped";
    prototype[method] = function () {};
    return "patched";
  }
  const one = tryPatch(InteractiveMode, "showLoadedResources");
  const two = tryPatch(TUI, "render");
  pi.registerCommand("result", { description: one + "|" + two, handler: async () => {} });
}
`)
	assertRegisteredDescription(t, result, "result", "skipped|skipped")
}

func TestNativeAddonAndWasmBuildErrors(t *testing.T) {
	cwd := t.TempDir()
	mustWrite(t, filepath.Join(cwd, "native.node"), "\x00binary")
	entry := filepath.Join(cwd, "uses-addon.ts")
	mustWrite(t, entry, `import addon from "./native.node";
export default function(pi) { void addon; }
`)
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 1 || !strings.Contains(loaded.Errors[0].Error, "native Node addons are not supported by the pigo extension runtime") {
		t.Fatalf("native addon load errors = %#v", loaded.Errors)
	}

	mustWrite(t, filepath.Join(cwd, "mod.wasm"), "\x00asm")
	wasmEntry := filepath.Join(cwd, "uses-wasm.ts")
	mustWrite(t, wasmEntry, `import mod from "./mod.wasm";
export default function(pi) { void mod; }
`)
	wasmLoader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{wasmEntry}})
	t.Cleanup(wasmLoader.Close)
	wasmLoaded := wasmLoader.Load(context.Background())
	if len(wasmLoaded.Errors) != 1 || !strings.Contains(wasmLoaded.Errors[0].Error, "WebAssembly modules are not supported by the pigo extension runtime") {
		t.Fatalf("wasm load errors = %#v", wasmLoaded.Errors)
	}
}

// --- finding 5: import.meta.url ---

func TestImportMetaURLPointsAtEntry(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
export default function(pi) {
  pi.registerCommand("result", { description: import.meta.url + "|" + dirname(fileURLToPath(import.meta.url)), handler: async () => {} });
}
`)
	entry := filepath.Join(cwd, "extension.ts")
	assertRegisteredDescription(t, result, "result", "file://"+entry+"|"+cwd)
}

func TestDynamicResourcesExampleDiscoversBundledFiles(t *testing.T) {
	_, runner, extensionDir := loadUpstreamDirectoryExampleWithDir(t, "dynamic-resources")
	resources := runner.EmitResourcesDiscover(context.Background(), extensionDir, extensions.ResourcesDiscoverStartup)
	if len(resources.SkillPaths) != 1 || len(resources.PromptPaths) != 1 || len(resources.ThemePaths) != 1 {
		t.Fatalf("discovered resources = %#v", resources)
	}
	skill := resources.SkillPaths[0].Path
	if skill != filepath.Join(extensionDir, "SKILL.md") {
		t.Fatalf("skill path = %q, want under %q", skill, extensionDir)
	}
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("discovered skill does not exist: %v", err)
	}
}

// --- finding 6: fs error codes ---

func TestFSErrorsCarryNodeCodes(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	result := loadAndRunExtension(t, cwd, `
import { statSync } from "node:fs";
import { readFile, mkdir } from "node:fs/promises";
export default async function(pi) {
  // pi-skillful's readSettingsDocument pattern (config.ts:99-110).
  async function readSettings(path) {
    try {
      return await readFile(path, "utf-8");
    } catch (error) {
      if (error.code !== "ENOENT") throw error;
      return "defaults";
    }
  }
  const settings = await readSettings("`+filepath.Join(cwd, "missing", "settings.json")+`");
  let exist = "none";
  try { await mkdir("`+filepath.Join(cwd, "existing")+`"); } catch (error) { exist = error.code; }
  let isdir = "none";
  try { await readFile("`+filepath.Join(cwd, "existing")+`", "utf-8"); } catch (error) { isdir = error.code; }
  let syncInfo = "none";
  try { statSync("`+filepath.Join(cwd, "gone.txt")+`"); } catch (error) {
    syncInfo = error.code + ";" + (error instanceof Error) + ";" + error.message + ";" + (typeof error.errno) + ";" + error.syscall + ";" + error.path;
  }
  pi.registerCommand("result", { description: [settings, exist, isdir, syncInfo].join("|"), handler: async () => {} });
}
`)
	gone := filepath.Join(cwd, "gone.txt")
	assertRegisteredDescription(t, result, "result",
		"defaults|EEXIST|EISDIR|ENOENT;true;ENOENT: no such file or directory, stat '"+gone+"';number;stat;"+gone)
}

// --- finding 7: unknown shim imports fail loudly ---

func TestUnknownShimImportFailsAtModuleScope(t *testing.T) {
	cwd := t.TempDir()
	entry := filepath.Join(cwd, "uses-editor.ts")
	mustWrite(t, entry, `
import { Editor } from "@earendil-works/pi-tui";
const instance = new Editor();
export default function(pi) { void instance; }
`)
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 1 || !strings.Contains(loaded.Errors[0].Error, "'Editor' is not exported by @earendil-works/pi-tui (pigo shim)") {
		t.Fatalf("unknown import load errors = %#v", loaded.Errors)
	}
}

func TestUnknownShimImportFailsClearlyAtUseTime(t *testing.T) {
	cwd := t.TempDir()
	result := loadAndRunExtension(t, cwd, `
import * as tui from "@earendil-works/pi-tui";
import { SettingsList } from "@earendil-works/pi-tui";
export default function(pi) {
  // Honest has(): feature detection stays quiet.
  const detected = "SettingsList" in tui;
  pi.registerCommand("probe", { description: String(detected), handler: async (_args, ctx) => {
    new SettingsList();
  }});
}
`)
	runner := extensions.NewRunner(result.Registry, extensions.RunnerOptions{CWD: cwd})
	command := runner.Command("probe")
	if command == nil || command.Description != "false" {
		t.Fatalf("feature detection command = %#v", command)
	}
	err := command.Handler(context.Background(), "", runner.CreateCommandContext())
	if err == nil || !strings.Contains(err.Error(), "'SettingsList' is not exported by @earendil-works/pi-tui (pigo shim)") {
		t.Fatalf("use-time error = %v", err)
	}
}

func TestQuestionExampleFailsAtLoadWithClearError(t *testing.T) {
	// docs/sync/extension-matrix.md marks question.ts unsupported (pi-tui
	// Editor); it must fail loudly instead of registering a broken tool.
	source, err := os.ReadFile(filepath.Join(upstreamExamplesDir(t), "question.ts"))
	if err != nil {
		t.Skipf("upstream question.ts unavailable: %v", err)
	}
	cwd := t.TempDir()
	entry := filepath.Join(cwd, "question.ts")
	mustWrite(t, entry, string(source))
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		// Acceptable outcome: a load-time diagnostic naming the missing export.
		if !strings.Contains(loaded.Errors[0].Error, "is not exported by @earendil-works/pi-tui (pigo shim)") {
			t.Fatalf("question.ts load errors = %#v", loaded.Errors)
		}
		return
	}
	// Editor is only touched inside execute (CommonJS defers named access),
	// so the tool registers but invoking it must fail with the clear error.
	runner := extensions.NewRunner(result0(loaded), extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModeTUI, UI: newScriptedUI()})
	tool := runner.ToolDefinition("question")
	if tool == nil {
		t.Fatal("question tool missing")
	}
	_, execErr := tool.Execute(context.Background(), "q-1", map[string]any{
		"question": "Pick one", "options": []any{map[string]any{"label": "a"}, map[string]any{"label": "b"}},
	}, nil, runner.CreateContext())
	if execErr == nil || !strings.Contains(execErr.Error(), "is not exported by @earendil-works/pi-tui (pigo shim)") {
		t.Fatalf("question execute error = %v", execErr)
	}
}

// --- finding 8: compact onComplete/onError ---

func TestCompactCallbacksFireAfterEventContextCancelled(t *testing.T) {
	cwd := t.TempDir()
	source := `
export default function (pi) {
  pi.on("turn_end", async (_event, ctx) => {
    ctx.compact({
      onComplete: (result) => pi.appendEntry("compact-done", { summary: result.summary }),
      onError: (error) => pi.appendEntry("compact-error", { message: error.message }),
    });
  });
}
`
	type entry struct {
		kind string
		data map[string]any
	}
	entries := make(chan entry, 4)
	var captured []*extensions.CompactOptions
	actions := extensions.Actions{AppendEntry: func(_ context.Context, customType string, data any) error {
		object, _ := data.(map[string]any)
		entries <- entry{kind: customType, data: object}
		return nil
	}}
	contextActions := extensions.ContextActions{Compact: func(options *extensions.CompactOptions) {
		captured = append(captured, options)
	}}
	runner := loadBridgeRunner(t, cwd, []bridgeSource{{"compactor.ts", source}}, extensions.RunnerOptions{
		CWD: cwd, Actions: actions, ContextActions: contextActions,
	})
	dispatch, cancel := context.WithCancel(context.Background())
	runner.Emit(dispatch, extensions.TurnEndEvent{TurnIndex: 0})
	cancel() // The event context dies before compaction resolves.
	if len(captured) != 1 || captured[0].OnComplete == nil || captured[0].OnError == nil {
		t.Fatalf("captured compact options = %#v", captured)
	}
	captured[0].OnComplete(harness.CompactionResult{Summary: "squashed", TokensBefore: 10})
	select {
	case got := <-entries:
		if got.kind != "compact-done" || fmt.Sprint(got.data["summary"]) != "squashed" {
			t.Fatalf("onComplete entry = %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("compact onComplete never fired after event context cancellation")
	}
	captured[0].OnError(fmt.Errorf("Nothing to compact"))
	select {
	case got := <-entries:
		if got.kind != "compact-error" || fmt.Sprint(got.data["message"]) != "Nothing to compact" {
			t.Fatalf("onError entry = %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("compact onError never fired after event context cancellation")
	}
}

// --- helpers ---

func result0(loaded LoadResult) *extensions.Registry { return loaded.Registry }

func upstreamExamplesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", ".upstream", "packages", "coding-agent", "examples", "extensions"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func copyDirectory(t *testing.T, source, target string) {
	t.Helper()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Skipf("upstream example directory unavailable: %v", err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, item := range entries {
		from := filepath.Join(source, item.Name())
		to := filepath.Join(target, item.Name())
		if item.IsDir() {
			copyDirectory(t, from, to)
			continue
		}
		content, readErr := os.ReadFile(from)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(to, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func loadUpstreamDirectoryExample(t *testing.T, name string) (string, *extensions.Runner) {
	t.Helper()
	cwd, runner := loadUpstreamDirectoryExampleRunner(t, name)
	return cwd, runner
}

func loadUpstreamDirectoryExampleRunner(t *testing.T, name string) (string, *extensions.Runner) {
	t.Helper()
	cwd, runner, _ := loadUpstreamDirectoryExampleWithDir(t, name)
	return cwd, runner
}

func loadUpstreamDirectoryExampleWithDir(t *testing.T, name string) (string, *extensions.Runner, string) {
	t.Helper()
	cwd := t.TempDir()
	extensionDir := filepath.Join(cwd, ".pi", "extensions", name)
	copyDirectory(t, filepath.Join(upstreamExamplesDir(t), name), extensionDir)
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ProjectTrusted: true})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("upstream %s load errors = %#v", name, loaded.Errors)
	}
	if len(loaded.Registry.Extensions()) == 0 {
		t.Fatalf("upstream %s registered no extensions", name)
	}
	runner := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModeTUI, UI: newScriptedUI()})
	return cwd, runner, extensionDir
}
