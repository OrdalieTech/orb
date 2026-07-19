package jsbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

// Port of upstream regression extension-factory-cache.test.ts. Upstream caches
// the imported module per cwd while re-running the default-export factory on
// every cached load; pi-go's analog is the content-hash bundle cache plus one
// fresh isolated VM per factory run (module-level state is deliberately not
// shared between runs). The protected behavior is the same: repeated loads
// must not rebuild unchanged sources, must re-run factories, and must produce
// fresh extension instances scoped to the new registry.
func TestExtensionFactoryCacheBundlesOnceButRerunsFactories(t *testing.T) {
	cwd := t.TempDir()
	counts := filepath.Join(cwd, "counts.txt")
	entry := filepath.Join(cwd, "counting.ts")
	mustWrite(t, entry, `
import { appendFileSync } from "node:fs";
export default function (pi) {
	appendFileSync(`+"`"+strings.ReplaceAll(counts, `\`, `\\`)+"`"+`, "factory\n");
	pi.registerCommand("counted", { description: "counted", handler: async () => {} });
}
`)
	factoryRuns := func() int {
		data, err := os.ReadFile(counts)
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "factory\n")
	}

	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(loader.Close)
	shared := extensions.NewRegistry(cwd)
	result := loader.RegisterInto(context.Background(), shared)
	if len(result.Errors) != 0 {
		t.Fatalf("register errors = %#v", result.Errors)
	}
	if got := factoryRuns(); got != 1 {
		t.Fatalf("factory runs after register = %d, want 1", got)
	}
	if loader.cache.builds != 1 {
		t.Fatalf("bundle builds after register = %d, want 1", loader.cache.builds)
	}
	firstRunner := extensions.NewRunner(shared, extensions.RunnerOptions{})
	firstCommand := firstRunner.Command("counted")
	if firstCommand == nil {
		t.Fatal("first load did not register the command")
	}

	// A fresh registry (the /reload and session-replacement path) re-runs the
	// factory against a new VM without rebuilding the unchanged bundle.
	second, err := shared.Fresh(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got := factoryRuns(); got != 2 {
		t.Fatalf("factory runs after Fresh = %d, want 2", got)
	}
	if loader.cache.builds != 1 {
		t.Fatalf("bundle builds after Fresh = %d, want 1 (cached)", loader.cache.builds)
	}
	secondRunner := extensions.NewRunner(second, extensions.RunnerOptions{})
	secondCommand := secondRunner.Command("counted")
	if secondCommand == nil {
		t.Fatal("fresh load did not register the command")
	}
	if err := secondCommand.Handler(context.Background(), "", secondRunner.CreateCommandContext()); err != nil {
		t.Fatalf("fresh command handler: %v", err)
	}
	// The first load's instances are replaced, not reused: the old VM is gone.
	if err := firstCommand.Handler(context.Background(), "", firstRunner.CreateCommandContext()); err == nil || !strings.Contains(err.Error(), "VM is closed") {
		t.Fatalf("stale command error = %v, want closed VM", err)
	}

	if _, err := shared.Fresh(cwd); err != nil {
		t.Fatal(err)
	}
	if got := factoryRuns(); got != 3 {
		t.Fatalf("factory runs after second Fresh = %d, want 3", got)
	}
	if loader.cache.builds != 1 {
		t.Fatalf("bundle builds after second Fresh = %d, want 1", loader.cache.builds)
	}
}

// The bundle cache is scoped to one loader (upstream scopes its module cache
// to one cwd): a second loader for another cwd builds independently and runs
// the factory for its own registry.
func TestExtensionFactoryCacheIsScopedPerLoader(t *testing.T) {
	root := t.TempDir()
	counts := filepath.Join(root, "counts.txt")
	entry := filepath.Join(root, "counting.ts")
	mustWrite(t, entry, `
import { appendFileSync } from "node:fs";
export default function (pi) {
	appendFileSync(`+"`"+strings.ReplaceAll(counts, `\`, `\\`)+"`"+`, "factory\n");
}
`)
	factoryRuns := func() int {
		data, err := os.ReadFile(counts)
		if err != nil {
			return 0
		}
		return strings.Count(string(data), "factory\n")
	}
	firstCwd := filepath.Join(root, "first")
	secondCwd := filepath.Join(root, "second")
	for _, dir := range []string{firstCwd, secondCwd} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	first := NewLoader(DiscoveryOptions{CWD: firstCwd, AgentDir: filepath.Join(firstCwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(first.Close)
	second := NewLoader(DiscoveryOptions{CWD: secondCwd, AgentDir: filepath.Join(secondCwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(second.Close)

	if result := first.Load(context.Background()); len(result.Errors) != 0 {
		t.Fatalf("first load errors = %#v", result.Errors)
	}
	if result := second.Load(context.Background()); len(result.Errors) != 0 {
		t.Fatalf("second load errors = %#v", result.Errors)
	}
	if result := second.Reload(context.Background()); len(result.Errors) != 0 {
		t.Fatalf("second reload errors = %#v", result.Errors)
	}
	if got := factoryRuns(); got != 3 {
		t.Fatalf("factory runs = %d, want 3", got)
	}
	if first.cache.builds != 1 || second.cache.builds != 1 {
		t.Fatalf("builds = %d/%d, want 1/1 (independent caches, content hash reuse)", first.cache.builds, second.cache.builds)
	}
}
