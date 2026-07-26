package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
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

// stubPiAI mirrors the published layout: the root entry carries only the core
// surface, the legacy global API lives on "/compat", and the type is declared
// with no runtime counterpart.
func stubPiAI(t *testing.T, packageDir string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(packageDir, "package.json"), `{"name":"@earendil-works/pi-ai","version":"0.81.1","type":"module",`+
		`"exports":{".":{"types":"./index.d.ts","import":"./index.js"},"./compat":{"types":"./compat.d.ts","import":"./compat.js"}}}`)
	writeFixtureFile(t, filepath.Join(packageDir, "index.js"), "export const core = \"core\";\n")
	writeFixtureFile(t, filepath.Join(packageDir, "index.d.ts"), "export interface ApiKeyCredential { type: \"api_key\" }\nexport declare const core: string;\n")
	writeFixtureFile(t, filepath.Join(packageDir, "compat.js"), "export * from \"./index.js\";\nexport function complete() { return \"compat\"; }\n")
	writeFixtureFile(t, filepath.Join(packageDir, "compat.d.ts"), "export * from \"./index.js\";\nexport declare function complete(): string;\n")
}

func startRuntimeManager(t *testing.T, runtime Runtime, entry string) (*extensions.Runner, LoadResult) {
	t.Helper()
	cwd := t.TempDir()
	manager := NewManager(Options{
		AgentDir: t.TempDir(), CWD: cwd, Version: "test", Runtime: &runtime,
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
	return extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd}), result
}

// isolateSDKRoot points the host at a stub SDK through the explicit override,
// and empties PATH so the test states plainly that no installed `pi` is part of
// the setup.
func isolateSDKRoot(t *testing.T, root string) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv(piSDKRootEnv, root)
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

// Node's type stripping keeps every named import, so a type imported from a bare
// specifier fails to link, and its root entry no longer carries the legacy API.
// Upstream loads extensions through jiti, which elides the first and never sees
// the second. Both are repaired in loader.mjs.
func TestNodeLoaderResolvesTypeOnlyAndLegacySDKImports(t *testing.T) {
	runtime := requireNamedRuntime(t, "node")
	extensionDir := t.TempDir()
	stubPiAI(t, filepath.Join(extensionDir, "node_modules", "@earendil-works", "pi-ai"))
	writeFixtureFile(t, filepath.Join(extensionDir, "package.json"), `{"name":"pigo-sdk-fixture","version":"1.0.0","type":"module"}`)
	entry := filepath.Join(extensionDir, "extension.ts")
	writeFixtureFile(t, entry, `import { ApiKeyCredential, complete, core } from "@earendil-works/pi-ai";

export default function (pi: any) {
	const credential: ApiKeyCredential = { type: "api_key" };
	pi.registerTool({
		name: "sdk_probe",
		label: "SDK Probe",
		description: `+"`${complete()}:${core}:${credential.type}`"+`,
		parameters: { type: "object", properties: {} },
		async execute() {
			return { content: [{ type: "text", text: "ok" }] };
		},
	});
}
`)
	runner, result := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	definition := runner.ToolDefinition("sdk_probe")
	if definition == nil {
		t.Fatal("sdk_probe was not registered")
	}
	if got := definition.Description; got != "compat:core:api_key" {
		t.Fatalf("description = %q, want %q", got, "compat:core:api_key")
	}
}

// legacySurface redirects "@earendil-works/pi-ai" to its "/compat" subpath
// through the caller's own context, so a copy the extension installed for
// itself wins and resolves successfully — even when it is too old for the
// import, which then fails on the missing export. Nothing throws during
// resolution, so the PIGO_PI_SDK_ROOT fallback, reachable only from the catch
// in resolve, never sees the specifier whatever that root holds. This is the
// shape of the measured `unsupported_sdk_export isRetryableAssistantError`
// regression, and it is unaffected by which copy the root names.
func TestNodeKeepsTheExtensionsOwnSDKCopyOverTheManagedRoot(t *testing.T) {
	runtime := requireNamedRuntime(t, "node")
	sdkRoot := t.TempDir()
	writeFixtureFile(t, filepath.Join(sdkRoot, "package.json"), `{"name":"@earendil-works/pi-coding-agent","version":"0.81.1","type":"module"}`)
	current := filepath.Join(sdkRoot, "node_modules", "@earendil-works", "pi-ai")
	stubPiAI(t, current)
	writeFixtureFile(t, filepath.Join(current, "compat.js"), "export * from \"./index.js\";\nexport function isRetryableAssistantError() { return true; }\n")
	isolateSDKRoot(t, sdkRoot)

	extensionDir := t.TempDir()
	writeFixtureFile(t, filepath.Join(extensionDir, "package.json"), `{"name":"pigo-old-sdk-fixture","version":"1.0.0","type":"module"}`)
	stubPiAI(t, filepath.Join(extensionDir, "node_modules", "@earendil-works", "pi-ai"))
	entry := filepath.Join(extensionDir, "extension.mjs")
	writeFixtureFile(t, entry, `import { isRetryableAssistantError } from "@earendil-works/pi-ai";

export default function () {
	isRetryableAssistantError();
}
`)
	_, result := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error, "isRetryableAssistantError") {
		t.Fatalf("load errors = %#v, want the extension's own older copy to lose the export", result.Errors)
	}
}

// With no SDK in pigo's own npm root there is nothing to fall back to — pigo
// does not borrow the copy inside an installed pi — so the import has to name
// what is missing and how to install it instead of failing with Node's bare
// "Cannot find package".
func TestNodeReportsMissingSDKWithInstallGuidance(t *testing.T) {
	runtime := requireNamedRuntime(t, "node")
	t.Setenv(piSDKRootEnv, "")
	extensionDir := t.TempDir()
	writeFixtureFile(t, filepath.Join(extensionDir, "package.json"), `{"name":"pigo-missing-sdk-fixture","version":"1.0.0","type":"module"}`)
	entry := filepath.Join(extensionDir, "extension.mjs")
	writeFixtureFile(t, entry, `import { complete } from "@earendil-works/pi-ai";

export default function (pi) {
	pi.registerTool({
		name: "missing_probe",
		label: "Missing Probe",
		description: complete(),
		parameters: { type: "object", properties: {} },
		async execute() {
			return { content: [{ type: "text", text: "ok" }] };
		},
	});
}
`)
	_, result := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 1 {
		t.Fatalf("load errors = %#v", result.Errors)
	}
	for _, fragment := range []string{"@earendil-works/pi-ai", "pigo's own npm root", "npm i --prefix", piSDKRootEnv} {
		if !strings.Contains(result.Errors[0].Error, fragment) {
			t.Fatalf("load error %q does not mention %q", result.Errors[0].Error, fragment)
		}
	}
}

// Bun never staged an entry, so nothing aliased the SDK specifiers that
// extensions import under their historical names. NODE_PATH is consulted after
// the node_modules walk, so the links only fill gaps.
func TestBunResolvesAliasedSDKSpecifier(t *testing.T) {
	runtime := requireNamedRuntime(t, "bun")
	sdkRoot := t.TempDir()
	writeFixtureFile(t, filepath.Join(sdkRoot, "package.json"), `{"name":"@earendil-works/pi-coding-agent","version":"0.82.0","type":"module"}`)
	tui := filepath.Join(sdkRoot, "node_modules", "@earendil-works", "pi-tui")
	writeFixtureFile(t, filepath.Join(tui, "package.json"), `{"name":"@earendil-works/pi-tui","version":"0.82.0","type":"module","main":"./index.js"}`)
	writeFixtureFile(t, filepath.Join(tui, "index.js"), "export const label = \"aliased-tui\";\n")
	isolateSDKRoot(t, sdkRoot)

	extensionDir := t.TempDir()
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
	runner, result := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	definition := runner.ToolDefinition("alias_probe")
	if definition == nil {
		t.Fatal("alias_probe was not registered")
	}
	if got := definition.Description; got != "aliased-tui" {
		t.Fatalf("description = %q, want %q", got, "aliased-tui")
	}
}

// A copy the extension resolves itself outranks the alias, so pinned installs
// keep their own module.
func TestBunAliasDoesNotShadowInstalledPackage(t *testing.T) {
	runtime := requireNamedRuntime(t, "bun")
	sdkRoot := t.TempDir()
	writeFixtureFile(t, filepath.Join(sdkRoot, "package.json"), `{"name":"@earendil-works/pi-coding-agent","version":"0.82.0","type":"module"}`)
	tui := filepath.Join(sdkRoot, "node_modules", "@earendil-works", "pi-tui")
	writeFixtureFile(t, filepath.Join(tui, "package.json"), `{"name":"@earendil-works/pi-tui","version":"0.82.0","type":"module","main":"./index.js"}`)
	writeFixtureFile(t, filepath.Join(tui, "index.js"), "export const label = \"aliased-tui\";\n")
	isolateSDKRoot(t, sdkRoot)

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
	runner, result := startRuntimeManager(t, runtime, entry)
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
