package codingagent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

// Finding 6: the first resource-loader Reload must adopt the extension registry
// that startup already materialized instead of re-running every factory via
// Fresh, so each factory executes exactly once per startup (upstream
// loadFinalExtensionSet reuses pre-trust-loaded instances). Subsequent reloads
// (/reload) must still re-run factories against a fresh registry.
func TestResourceLoaderAdoptsPreloadedRegistryThenReloadsFresh(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}

	var factoryRuns atomic.Int64
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<inline:counter>", func(extensions.API) error {
		factoryRuns.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The startup load (Register) ran the factory exactly once.
	if got := factoryRuns.Load(); got != 1 {
		t.Fatalf("factory runs after register = %d, want 1", got)
	}

	loader, err := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings,
		ExtensionRegistry: registry, NoSkills: true, NoPromptTemplates: true, NoThemes: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// First Reload adopts the preloaded registry; the factory must not re-run.
	if got := factoryRuns.Load(); got != 1 {
		t.Fatalf("factory runs after first reload = %d, want 1 (startup double-run)", got)
	}
	if loader.GetExtensions() != registry {
		t.Fatal("first reload did not adopt the preloaded registry instance")
	}

	// A later reload (the /reload path) re-runs factories against a fresh registry.
	if err := loader.Reload(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := factoryRuns.Load(); got != 2 {
		t.Fatalf("factory runs after second reload = %d, want 2 (/reload must re-run)", got)
	}
	if loader.GetExtensions() == registry {
		t.Fatal("second reload should build a fresh registry, not reuse the original")
	}
}
