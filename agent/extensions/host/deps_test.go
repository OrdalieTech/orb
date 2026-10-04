package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDependenciesSatisfiedRejectsEscapingPackageNames(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "outside", "package.json"), `{"name":"outside"}`, 0o600)
	for _, name := range []string{"../outside", "@scope/../outside", `scope\outside`, "@/outside"} {
		if dependenciesSatisfied(root, map[string]string{name: "file:anywhere"}) {
			t.Fatalf("dependency %q escaped node_modules", name)
		}
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
