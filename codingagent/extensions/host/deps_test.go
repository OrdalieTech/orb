package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeDependenciesSkipsSatisfiedNodeModules(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "extension.mjs")
	writeFile(t, entry, "export default () => {};\n", 0o600)
	writeFile(t, filepath.Join(root, "package.json"), `{"dependencies":{"already-here":"1.0.0"}}`, 0o600)
	writeFile(t, filepath.Join(root, "node_modules", "already-here", "package.json"), `{"name":"already-here"}`, 0o600)
	if err := materializeDependencies(context.Background(), Runtime{Name: "node", Path: filepath.Join(root, "missing-node")}, entry, []string{"PATH="}); err != nil {
		t.Fatal(err)
	}
}

func TestDependenciesSatisfiedRejectsEscapingPackageNames(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "outside", "package.json"), `{"name":"outside"}`, 0o600)
	for _, name := range []string{"../outside", "@scope/../outside", `scope\outside`, "@/outside"} {
		if dependenciesSatisfied(root, map[string]string{name: "file:anywhere"}) {
			t.Fatalf("dependency %q escaped node_modules", name)
		}
	}
}

func TestDependenciesSatisfiedFindsHoistedNodeModules(t *testing.T) {
	root := t.TempDir()
	packageDir := filepath.Join(root, "node_modules", "extension")
	writeFile(t, filepath.Join(packageDir, "package.json"), `{"dependencies":{"hoisted":"1.0.0"}}`, 0o600)
	writeFile(t, filepath.Join(root, "node_modules", "hoisted", "package.json"), `{"name":"hoisted"}`, 0o600)
	if !dependenciesSatisfied(packageDir, map[string]string{"hoisted": "1.0.0"}) {
		t.Fatal("hoisted dependency was not resolved from an ancestor node_modules")
	}
}

func TestPrepareRuntimeEntryPrefersCodingAgentSDKDependencies(t *testing.T) {
	root := t.TempDir()
	packageDir := filepath.Join(root, "node_modules", "extension")
	entry := filepath.Join(packageDir, "index.ts")
	writeFile(t, entry, "export default () => {};", 0o600)
	writeFile(t, filepath.Join(packageDir, "package.json"), `{"dependencies":{"declared":"1.0.0"}}`, 0o600)
	declared := filepath.Join(root, "node_modules", "declared")
	writeFile(t, filepath.Join(declared, "package.json"), `{"name":"declared"}`, 0o600)
	oldSDK := filepath.Join(root, "node_modules", "@earendil-works", "pi-ai")
	writeFile(t, filepath.Join(oldSDK, "package.json"), `{"name":"@earendil-works/pi-ai","version":"old"}`, 0o600)
	pinnedSDK := filepath.Join(root, "node_modules", "@earendil-works", "pi-coding-agent", "node_modules", "@earendil-works", "pi-ai")
	writeFile(t, filepath.Join(pinnedSDK, "package.json"), `{"name":"@earendil-works/pi-ai","version":"pinned"}`, 0o600)
	pinnedTypeBox := filepath.Join(root, "node_modules", "@earendil-works", "pi-coding-agent", "node_modules", "typebox")
	writeFile(t, filepath.Join(pinnedTypeBox, "package.json"), `{"name":"typebox","version":"pinned"}`, 0o600)

	agentDir := t.TempDir()
	prepared, err := prepareRuntimeEntry(agentDir, Runtime{Name: "node"}, extensionEntry{ID: "ext-1", Path: entry})
	if err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Dir(filepath.Dir(prepared.RuntimePath))
	for name, want := range map[string]string{
		"declared":              declared,
		"@earendil-works/pi-ai": pinnedSDK,
		"@mariozechner/pi-ai":   pinnedSDK,
		"@sinclair/typebox":     pinnedTypeBox,
	} {
		got, err := os.Readlink(filepath.Join(stageDir, "node_modules", filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read staged %s: %v", name, err)
		}
		if got != want {
			t.Fatalf("staged %s = %q, want %q", name, got, want)
		}
	}
}

func TestPrepareRuntimeEntryKeepsDeclaredSDKDependency(t *testing.T) {
	root := t.TempDir()
	packageDir := filepath.Join(root, "node_modules", "extension")
	entry := filepath.Join(packageDir, "index.ts")
	writeFile(t, entry, "export default () => {};", 0o600)
	writeFile(t, filepath.Join(packageDir, "package.json"), `{"dependencies":{"@earendil-works/pi-ai":"^0.74.0"}}`, 0o600)
	declaredSDK := filepath.Join(root, "node_modules", "@earendil-works", "pi-ai")
	writeFile(t, filepath.Join(declaredSDK, "package.json"), `{"name":"@earendil-works/pi-ai","version":"0.74.2"}`, 0o600)
	pinnedSDK := filepath.Join(root, "node_modules", "@earendil-works", "pi-coding-agent", "node_modules", "@earendil-works", "pi-ai")
	writeFile(t, filepath.Join(pinnedSDK, "package.json"), `{"name":"@earendil-works/pi-ai","version":"0.81.1"}`, 0o600)

	prepared, err := prepareRuntimeEntry(t.TempDir(), Runtime{Name: "node"}, extensionEntry{ID: "ext-1", Path: entry})
	if err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Dir(filepath.Dir(prepared.RuntimePath))
	got, err := os.Readlink(filepath.Join(stageDir, "node_modules", "@earendil-works", "pi-ai"))
	if err != nil {
		t.Fatal(err)
	}
	if got != declaredSDK {
		t.Fatalf("declared SDK target = %q, want %q", got, declaredSDK)
	}
}

func TestRealHostMaterializesLocalFileDependencyOffline(t *testing.T) {
	runtime := requireRuntime(t)
	if runtime.Name == "node" {
		if _, _, err := dependencyInstallCommand(runtime, os.Environ()); err != nil {
			t.Skip(err)
		}
		t.Setenv("npm_config_offline", "true")
	}
	root := t.TempDir()
	dependencyDir := filepath.Join(root, "dependency")
	extensionDir := filepath.Join(root, "extension")
	writeFile(t, filepath.Join(dependencyDir, "package.json"), `{"name":"offline-local-dep","version":"1.0.0","type":"module","exports":"./index.mjs"}`, 0o600)
	writeFile(t, filepath.Join(dependencyDir, "index.mjs"), `export const localValue = "offline-ok";`, 0o600)
	writeFile(t, filepath.Join(extensionDir, "package.json"), `{"type":"module","dependencies":{"offline-local-dep":"file:../dependency"}}`, 0o600)
	entry := filepath.Join(extensionDir, "index.mjs")
	writeFile(t, entry, `
import { localValue } from "offline-local-dep";
export default function (pi) {
  pi.registerTool({
    name: "offline_dependency",
    label: "Offline dependency",
    description: "Returns a local dependency value",
    parameters: { type: "object", properties: {} },
    async execute() { return { content: [{ type: "text", text: localValue }], details: {} }; }
  });
}
`, 0o600)

	_, _, runner, result, _ := startFixtureManager(t, entry)
	if len(result.Diagnostics) != 0 || len(result.Errors) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	definition := runner.ToolDefinition("offline_dependency")
	if definition == nil {
		t.Fatal("offline dependency tool was not registered")
	}
	value, err := definition.Execute(context.Background(), "offline-call", map[string]any{}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	if got := toolText(value); got != "offline-ok" {
		t.Fatalf("dependency result = %q", got)
	}
	if _, err := os.Stat(filepath.Join(extensionDir, "node_modules", "offline-local-dep", "package.json")); err != nil {
		t.Fatalf("materialized dependency: %v", err)
	}
}

func TestDependencyInstallFailureIsEntryLocal(t *testing.T) {
	runtime := requireRuntime(t)
	if runtime.Name == "node" {
		if _, _, err := dependencyInstallCommand(runtime, os.Environ()); err != nil {
			t.Skip(err)
		}
		t.Setenv("npm_config_offline", "true")
	}
	root := t.TempDir()
	badDir := filepath.Join(root, "bad")
	writeFile(t, filepath.Join(badDir, "package.json"), `{"type":"module","dependencies":{"missing-local":"file:../does-not-exist"}}`, 0o600)
	badEntry := filepath.Join(badDir, "index.mjs")
	writeFile(t, badEntry, `export default () => {};`, 0o600)

	_, _, runner, result, _ := startFixtureManager(t, badEntry, fixturePath(t, "working.mjs"))
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error, "install dependencies") {
		t.Fatalf("load errors = %#v", result.Errors)
	}
	if runner.ToolDefinition("host_echo") == nil {
		t.Fatal("later extension did not load after dependency failure")
	}
}

func writeFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}
