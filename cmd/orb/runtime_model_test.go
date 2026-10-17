package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
)

// --model with an id several providers offer picks the one with credentials, but --api-key goes to whichever
// provider is picked, so with it stored credentials do not pick: the id is ambiguous.
func TestAPIKeyDoesNotPickTheProviderOfASharedModel(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	t.Setenv("OPENAI_API_KEY", "stored")
	t.Setenv("AZURE_OPENAI_API_KEY", "") // the other providers of gpt-6-sol stay without credentials
	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	pattern, key := "gpt-6-sol", "inline"
	model, _, _, _, err := resolveRuntimeModel(CLIArgs{Model: &pattern}, settings, registry)
	if err != nil || model == nil || model.Provider != "openai" {
		t.Fatalf("model = %#v, err = %v", model, err)
	}
	if _, _, _, _, err = resolveRuntimeModel(CLIArgs{Model: &pattern, APIKey: &key}, settings, registry); err == nil || !strings.Contains(err.Error(), "--api-key does not pick one") {
		t.Fatalf("with --api-key: err = %v", err)
	}
}
