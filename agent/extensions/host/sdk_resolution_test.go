package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
)

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// plantRealPiPackage lays down a real-looking installed pi SDK package whose
// module records evaluation in a sentinel file, so tests can assert not just
// that the copy lost resolution but that not one line of it ever ran.
func plantRealPiPackage(t *testing.T, packageDir string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(packageDir, "package.json"),
		`{"name":"@earendil-works/pi-coding-agent","version":"0.84.1","type":"module","exports":"./index.js"}`)
	writeFixtureFile(t, filepath.Join(packageDir, "index.js"), `import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
writeFileSync(fileURLToPath(new URL("./evaluated", import.meta.url)), "x");
export const real = true;
`)
}

func startRuntimeManager(t *testing.T, runtime Runtime, entry string) (*extensions.Runner, LoadResult, string) {
	t.Helper()
	cwd := t.TempDir()
	agentDir := t.TempDir()
	manager := NewManager(Options{
		AgentDir: agentDir, CWD: cwd, Version: "test", Runtime: &runtime,
		RequestTimeout: 60 * time.Second, ShutdownTimeout: time.Second,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	registry := extensions.NewRegistry(cwd)
	result := manager.RegisterInto(context.Background(), registry, []string{entry})
	return extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd}), result, agentDir
}

func requireNamedRuntime(t *testing.T, name string) Runtime {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not on PATH", name)
	}
	version, err := commandVersion(context.Background(), path)
	if err != nil {
		t.Skipf("%s --version failed: %v", name, err)
	}
	version = strings.TrimPrefix(version, "v")
	runtime := Runtime{Name: name, Version: version, Path: path}
	if name == "bun" {
		runtime.Args = []string{"--no-install"}
	}
	if name == "node" {
		if !nodeAtLeast226(version) {
			t.Skipf("node %s is below 22.6", version)
		}
		runtime.Args = nodeRuntimeArgs(context.Background(), path, version)
	}
	return runtime
}

// Every legacy pi SDK specifier — both scopes, root and subpaths — resolves by
// realpath into the embedded orb-extension-sdk the manager materialized under
// the agent directory, even when the extension carries its own real install:
// the alias is unconditional, so the installed copy is never consulted (and,
// never being resolved, never evaluated). Type-only names classify against the
// SDK's declaration files, so mixed imports link under Node's type stripping.
func TestNodeLegacySpecifiersResolveToEmbeddedSDKByRealpath(t *testing.T) {
	runtime := requireNamedRuntime(t, "node")
	extensionDir := t.TempDir()
	writeFixtureFile(t, filepath.Join(extensionDir, "package.json"), `{"name":"orb-sdk-alias-fixture","version":"1.0.0","type":"module"}`)
	planted := filepath.Join(extensionDir, "node_modules", "@earendil-works", "pi-coding-agent")
	plantRealPiPackage(t, planted)
	entry := filepath.Join(extensionDir, "extension.ts")
	writeFixtureFile(t, entry, `import { realpathSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { Model, modelsAreEqual } from "@earendil-works/pi-ai";
import { visibleWidth } from "@mariozechner/pi-tui";

export default function (pi: any) {
	const model: Model = { id: "m", provider: "p" } as any;
	const paths = [
		"@earendil-works/pi-ai",
		"@earendil-works/pi-ai/compat",
		"@earendil-works/pi-coding-agent",
		"@mariozechner/pi-tui",
	].map((name) => realpathSync(fileURLToPath(import.meta.resolve(name))));
	pi.registerTool({
		name: "sdk_paths",
		label: "SDK Paths",
		description: [String(modelsAreEqual(model, { id: "m", provider: "p" } as any)), String(visibleWidth("abc")), ...paths].join("|"),
		parameters: { type: "object", properties: {} },
		async execute() {
			return { content: [{ type: "text", text: "ok" }] };
		},
	});
}
`)
	runner, result, agentDir := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	definition := runner.ToolDefinition("sdk_paths")
	if definition == nil {
		t.Fatal("sdk_paths was not registered")
	}
	parts := strings.Split(definition.Description, "|")
	if len(parts) != 6 {
		t.Fatalf("description = %q, want 6 parts", definition.Description)
	}
	if parts[0] != "true" || parts[1] != "3" {
		t.Fatalf("implemented symbols returned %q, %q; want \"true\", \"3\"", parts[0], parts[1])
	}
	realAgentDir, err := filepath.EvalSymlinks(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	sdkPrefix := filepath.Join(realAgentDir, "host", "sdk-")
	for index, wantBase := range []string{"ai.mjs", "ai-compat.mjs", "coding-agent.mjs", "tui.mjs"} {
		got := parts[2+index]
		if !strings.HasPrefix(got, sdkPrefix) {
			t.Fatalf("resolved path %q is not under the materialized SDK %q*", got, sdkPrefix)
		}
		if filepath.Base(got) != wantBase {
			t.Fatalf("resolved path %q, want module %q", got, wantBase)
		}
	}
	if _, err := os.Stat(filepath.Join(planted, "evaluated")); !os.IsNotExist(err) {
		t.Fatal("the extension's own real pi SDK install was evaluated")
	}
}

// A legacy specifier the alias map does not serve resolves nowhere — never
// from node_modules — and the failure names both the specifier and the
// importing module.
func TestNodeUnmappedLegacySubpathFailsPrecisely(t *testing.T) {
	runtime := requireNamedRuntime(t, "node")
	extensionDir := t.TempDir()
	entry := filepath.Join(extensionDir, "extension.mjs")
	writeFixtureFile(t, entry, `import "@earendil-works/pi-coding-agent/rpc-entry";

export default function () {}
`)
	_, result, _ := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 1 {
		t.Fatalf("load errors = %#v", result.Errors)
	}
	for _, fragment := range []string{
		`"@earendil-works/pi-coding-agent/rpc-entry"`,
		"extension.mjs",
		"not part of the orb extension SDK surface",
	} {
		if !strings.Contains(result.Errors[0].Error, fragment) {
			t.Fatalf("load error %q does not mention %q", result.Errors[0].Error, fragment)
		}
	}
}

// The realpath guard behind the alias map: a route the aliases cannot see — a
// symlinked package name reaching an installed pi SDK, imported transitively —
// is refused at resolution, before a line of the real SDK runs, with a
// diagnostic naming the specifier and the import chain.
func TestNodeGuardRefusesRealSDKRealpath(t *testing.T) {
	runtime := requireNamedRuntime(t, "node")
	extensionDir := t.TempDir()
	writeFixtureFile(t, filepath.Join(extensionDir, "package.json"), `{"name":"orb-guard-fixture","version":"1.0.0","type":"module"}`)
	planted := filepath.Join(extensionDir, "node_modules", "@earendil-works", "pi-coding-agent")
	plantRealPiPackage(t, planted)
	if err := os.Symlink(filepath.Join("@earendil-works", "pi-coding-agent"), filepath.Join(extensionDir, "node_modules", "innocent-shim")); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(extensionDir, "helper.mjs"), `import "innocent-shim";
`)
	entry := filepath.Join(extensionDir, "extension.mjs")
	writeFixtureFile(t, entry, `import "./helper.mjs";

export default function () {}
`)
	_, result, _ := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 1 {
		t.Fatalf("load errors = %#v", result.Errors)
	}
	for _, fragment := range []string{
		`refusing to load "innocent-shim"`,
		"real pi SDK install",
		filepath.Join("node_modules", "@earendil-works", "pi-coding-agent"),
		"helper.mjs",
		"extension.mjs",
		"import chain",
	} {
		if !strings.Contains(result.Errors[0].Error, fragment) {
			t.Fatalf("load error %q does not mention %q", result.Errors[0].Error, fragment)
		}
	}
	if _, err := os.Stat(filepath.Join(planted, "evaluated")); !os.IsNotExist(err) {
		t.Fatal("the real pi SDK module was evaluated despite the guard")
	}
}

// Legacy subpaths with no implemented Orb module — pi-agent-core, pi-ai/oauth,
// pi-ai/providers/all — still link: every upstream export name exists as a
// stub that throws the precise OrbUnsupportedCapability diagnostic on use.
func TestNodeUnsupportedSubpathExportsLinkAndThrowOnUse(t *testing.T) {
	runtime := requireNamedRuntime(t, "node")
	extensionDir := t.TempDir()
	entry := filepath.Join(extensionDir, "extension.mjs")
	writeFixtureFile(t, entry, `import { agentLoop } from "@earendil-works/pi-agent-core";
import { OAuthCredentials } from "@earendil-works/pi-ai/oauth";
import { getBuiltinModel } from "@earendil-works/pi-ai/providers/all";

export default function (pi) {
	const messages = [];
	for (const probe of [agentLoop, OAuthCredentials, getBuiltinModel]) {
		try {
			probe();
			messages.push("no error");
		} catch (error) {
			messages.push(error.name + ": " + error.message);
		}
	}
	pi.registerTool({
		name: "unsupported_probe",
		label: "Unsupported Probe",
		description: messages.join(" // "),
		parameters: { type: "object", properties: {} },
		async execute() {
			return { content: [{ type: "text", text: "ok" }] };
		},
	});
}
`)
	runner, result, _ := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	definition := runner.ToolDefinition("unsupported_probe")
	if definition == nil {
		t.Fatal("unsupported_probe was not registered — stub imports failed to link")
	}
	for _, fragment := range []string{
		"OrbUnsupportedCapability: agent-core#agentLoop is not implemented by orb-extension-sdk",
		"OrbUnsupportedCapability: ai/oauth#OAuthCredentials is not implemented by orb-extension-sdk",
		"OrbUnsupportedCapability: ai/providers/all#getBuiltinModel is not implemented by orb-extension-sdk",
		"supported exports: none",
	} {
		if !strings.Contains(definition.Description, fragment) {
			t.Fatalf("description %q does not contain %q", definition.Description, fragment)
		}
	}
	if strings.Contains(definition.Description, "no error") {
		t.Fatalf("a stub export did not throw: %q", definition.Description)
	}
}

// Bun has no resolve hook, so the legacy names reach the embedded SDK through
// NODE_PATH wrapper packages written by prepareRuntimeAliases.
func TestBunResolvesAliasedSDKSpecifier(t *testing.T) {
	runtime := requireNamedRuntime(t, "bun")
	extensionDir := t.TempDir()
	entry := filepath.Join(extensionDir, "extension.mjs")
	writeFixtureFile(t, entry, `import { visibleWidth } from "@mariozechner/pi-tui";

export default function (pi) {
	pi.registerTool({
		name: "alias_probe",
		label: "Alias Probe",
		description: String(visibleWidth("abc")),
		parameters: { type: "object", properties: {} },
		async execute() {
			return { content: [{ type: "text", text: "ok" }] };
		},
	});
}
`)
	runner, result, _ := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	definition := runner.ToolDefinition("alias_probe")
	if definition == nil {
		t.Fatal("alias_probe was not registered")
	}
	if got := definition.Description; got != "3" {
		t.Fatalf("description = %q, want %q", got, "3")
	}
}

// Bun consults NODE_PATH only after the node_modules walk, so an extension's
// own installed copy still outranks the embedded SDK there. This is the
// documented Bun gap in the no-real-SDK guarantee — the loader guard is
// Node-only — pinned here so a change in Bun's resolution order is noticed.
func TestBunOwnInstallStillOutranksEmbeddedSDK(t *testing.T) {
	runtime := requireNamedRuntime(t, "bun")
	extensionDir := t.TempDir()
	installed := filepath.Join(extensionDir, "node_modules", "@mariozechner", "pi-tui")
	writeFixtureFile(t, filepath.Join(installed, "package.json"), `{"name":"@mariozechner/pi-tui","version":"0.1.0","type":"module","main":"./index.js"}`)
	writeFixtureFile(t, filepath.Join(installed, "index.js"), "export const label = \"installed-tui\";\n")
	entry := filepath.Join(extensionDir, "extension.mjs")
	writeFixtureFile(t, entry, `import { label } from "@mariozechner/pi-tui";

export default function (pi) {
	pi.registerTool({
		name: "alias_probe",
		label: "Alias Probe",
		description: label,
		parameters: { type: "object", properties: {} },
		async execute() {
			return { content: [{ type: "text", text: "ok" }] };
		},
	});
}
`)
	runner, result, _ := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	definition := runner.ToolDefinition("alias_probe")
	if definition == nil {
		t.Fatal("alias_probe was not registered")
	}
	if got := definition.Description; got != "installed-tui" {
		t.Fatalf("description = %q, want %q", got, "installed-tui")
	}
}
