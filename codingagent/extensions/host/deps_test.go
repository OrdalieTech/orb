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

// The SDK copy pinned inside pi-coding-agent is the one it was built against, so
// it must win over a hoisted sibling of a different version. Only Bun consumes
// this now; Node reaches the same copy through the loader's resolve hook.
func TestResolveRuntimeSDKPrefersCodingAgentPin(t *testing.T) {
	root := t.TempDir()
	nodeModules := filepath.Join(root, "node_modules")
	hoisted := filepath.Join(nodeModules, "@earendil-works", "pi-ai")
	writeFile(t, filepath.Join(hoisted, "package.json"), `{"name":"@earendil-works/pi-ai","version":"old"}`, 0o600)
	pinned := filepath.Join(nodeModules, "@earendil-works", "pi-coding-agent", "node_modules", "@earendil-works", "pi-ai")
	writeFile(t, filepath.Join(pinned, "package.json"), `{"name":"@earendil-works/pi-ai","version":"pinned"}`, 0o600)

	if got := resolveRuntimeSDK(nodeModules, "@earendil-works/pi-ai"); got != pinned {
		t.Fatalf("resolveRuntimeSDK = %q, want the pinned copy %q", got, pinned)
	}
	if got := resolveRuntimeSDK(nodeModules, "@earendil-works/pi-tui"); got != "" {
		t.Fatalf("resolveRuntimeSDK for an absent package = %q, want empty", got)
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
