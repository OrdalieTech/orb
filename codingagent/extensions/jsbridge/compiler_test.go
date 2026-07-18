package jsbridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

func TestCompilerTargetsES2017AndEmbedsSourceMap(t *testing.T) {
	entry := filepath.Join(t.TempDir(), "generator.ts")
	mustWrite(t, entry, `
async function* sequence() { yield 1; }
export default function(pi) { void sequence; pi.registerCommand("ok", {handler: async () => {}}); }
`)
	cache := newBuildCache()
	built, err := cache.build(entry)
	if err != nil {
		t.Fatal(err)
	}
	code := string(built.code)
	if strings.Contains(code, "async function*") {
		t.Fatal("async generator was not lowered for ES2017")
	}
	if !strings.Contains(code, "sourceMappingURL=data:application/json;base64,") {
		t.Fatal("bundle has no inline source map")
	}
}

func TestCompilerBundlesExtensionLocalPureJSDependency(t *testing.T) {
	cwd := t.TempDir()
	entry := filepath.Join(cwd, "extension.ts")
	mustWrite(t, filepath.Join(cwd, "node_modules", "local-dep", "package.json"), `{"main":"index.js"}`)
	mustWrite(t, filepath.Join(cwd, "node_modules", "local-dep", "index.js"), `exports.value = "bundled";`)
	mustWrite(t, filepath.Join(cwd, "node_modules", "local-dep", "other.js"), `exports.value = "changed";`)
	mustWrite(t, entry, `
import { value } from "local-dep";
export default function(pi) { pi.registerCommand("dependency", {description: value, handler: async () => {}}); }
`)
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	command := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{}).Command("dependency")
	if command == nil || command.Description != "bundled" {
		t.Fatalf("command = %#v", command)
	}
	builds := loader.cache.builds
	for _, cached := range loader.cache.artifacts {
		if strings.Contains(string(cached.code), `require("local-dep")`) {
			t.Fatal("extension-local dependency was left external")
		}
	}
	mustWrite(t, filepath.Join(cwd, "node_modules", "local-dep", "package.json"), `{"main":"other.js"}`)
	reloaded := loader.Reload(context.Background())
	if len(reloaded.Errors) != 0 {
		t.Fatalf("reload errors = %#v", reloaded.Errors)
	}
	command = extensions.NewRunner(reloaded.Registry, extensions.RunnerOptions{}).Command("dependency")
	if command == nil || command.Description != "changed" || loader.cache.builds != builds+1 {
		t.Fatalf("package metadata reload command = %#v, builds = %d", command, loader.cache.builds)
	}
}

func TestLoadIsolatesExtensionErrors(t *testing.T) {
	cwd := t.TempDir()
	bad := filepath.Join(cwd, "a-bad.ts")
	good := filepath.Join(cwd, "b-good.ts")
	mustWrite(t, bad, `export default function() { throw new Error("broken"); }`)
	mustWrite(t, good, `export default function(pi) { pi.registerCommand("good", {handler: async () => {}}); }`)
	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{bad, good},
	})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 1 || !strings.Contains(loaded.Errors[0].Error, "broken") {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	if command := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{}).Command("good"); command == nil {
		t.Fatal("good extension did not load after isolated failure")
	}
}

func TestEachExtensionHasAnIsolatedVM(t *testing.T) {
	cwd := t.TempDir()
	source := `
export default function(pi) {
  globalThis.extensionCount = (globalThis.extensionCount ?? 0) + 1;
  pi.registerCommand("isolation", {description: String(globalThis.extensionCount), handler: async () => {}});
}
`
	first := filepath.Join(cwd, "first.ts")
	second := filepath.Join(cwd, "second.ts")
	mustWrite(t, first, source)
	mustWrite(t, second, source)
	loader := NewLoader(DiscoveryOptions{
		CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{first, second},
	})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	commands := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{}).RegisteredCommands()
	if len(commands) != 2 || commands[0].Description != "1" || commands[1].Description != "1" {
		t.Fatalf("commands = %#v", commands)
	}
}
