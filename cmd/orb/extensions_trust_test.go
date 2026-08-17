package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
)

// Regression: the project_trust extension event never fired in the shipped CLI
// (ResolveProjectTrusted was always called without a Runner). Upstream consults
// pre-trust extensions ahead of the trust store and the interactive prompt
// (main.ts resolveProjectTrust wiring -> project-trust.ts emitProjectTrustEvent).
func TestLoadStartupExtensionsConsultsProjectTrustExtension(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".pi", "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := config.NewProjectTrustStore(agentDir)
	storedUntrusted := false
	if err := store.Set(cwd, &storedUntrusted); err != nil {
		t.Fatal(err)
	}

	var eventCWDs []string
	previous := compiledExtensions
	t.Cleanup(func() { compiledExtensions = previous })
	compiledExtensions = append(append([]extensions.CompiledExtension(nil), previous...), extensions.CompiledExtension{
		Name: "trust-decider", DefaultEnabled: true,
		Factory: func(api extensions.API) error {
			api.On(extensions.EventProjectTrust, func(_ context.Context, event extensions.Event, _ extensions.Context) (any, error) {
				trustEvent, ok := event.(extensions.ProjectTrustEvent)
				if !ok {
					t.Errorf("unexpected event payload %#v", event)
					return nil, nil
				}
				eventCWDs = append(eventCWDs, trustEvent.CWD)
				return &extensions.ProjectTrustResult{Trusted: extensions.ProjectTrustYes, Remember: true}, nil
			})
			return nil
		},
	})
	t.Cleanup(func() { replaceActiveExtensionHost(nil) })

	registry, diagnostics, trusted, err := loadStartupExtensions(cwd, CLIArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if registry == nil {
		t.Fatalf("no registry; diagnostics=%v", diagnostics)
	}
	if trusted == nil || !*trusted {
		t.Fatalf("resolved trust = %v, want the extension's trusted=yes handed back to the runtime", trusted)
	}
	if len(eventCWDs) == 0 || eventCWDs[0] != cwd {
		t.Fatalf("project_trust event cwds = %#v, want first event for %q", eventCWDs, cwd)
	}
	// The extension is consulted ahead of the stored "untrusted" decision, and
	// remember=true persists its verdict, exactly as upstream does.
	decision, err := store.Get(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if decision == nil || !*decision {
		t.Fatalf("stored trust decision = %v, want extension's trusted=yes remembered", decision)
	}
}

// A CLI trust override must keep bypassing the extension event entirely.
func TestLoadStartupExtensionsSkipsProjectTrustEventOnOverride(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".pi", "settings.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	fired := false
	previous := compiledExtensions
	t.Cleanup(func() { compiledExtensions = previous })
	compiledExtensions = append(append([]extensions.CompiledExtension(nil), previous...), extensions.CompiledExtension{
		Name: "trust-decider", DefaultEnabled: true,
		Factory: func(api extensions.API) error {
			api.On(extensions.EventProjectTrust, func(context.Context, extensions.Event, extensions.Context) (any, error) {
				fired = true
				return &extensions.ProjectTrustResult{Trusted: extensions.ProjectTrustNo}, nil
			})
			return nil
		},
	})
	t.Cleanup(func() { replaceActiveExtensionHost(nil) })

	trusted := true
	if _, _, _, err := loadStartupExtensions(cwd, CLIArgs{ProjectTrusted: &trusted}); err != nil {
		t.Fatal(err)
	}
	if fired {
		t.Fatal("project_trust event fired despite an explicit CLI override")
	}
}
