package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/toolenv"
)

func TestCreateRuntimeInputsScopesCLIAPIKeyToSelectedProvider(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	models := `{"providers":{"local":{"baseUrl":"http://localhost/v1","api":"openai-completions","models":[{"id":"one"},{"id":"two"}]},"other":{"baseUrl":"http://localhost/v1","api":"openai-completions","models":[{"id":"foreign"}]}}}`
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(models), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)
	provider, modelID, key := "local", "one", "runtime-key"
	inputs, err := createRuntimeInputs(root, CLIArgs{Provider: &provider, Model: &modelID, APIKey: &key}, engine.AgentMessages{})
	if err != nil {
		t.Fatal(err)
	}
	available := inputs.AvailableModels()
	if !modelListContains(available, "local", "one") || !modelListContains(available, "local", "two") || modelListContains(available, "other", "foreign") {
		t.Fatalf("runtime-key models = %#v", available)
	}
	resolved, err := inputs.GetAPIKey(context.Background(), "local")
	if err != nil || resolved == nil || *resolved != key {
		t.Fatalf("selected-provider key = %v, %v", resolved, err)
	}
	resolved, err = inputs.GetAPIKey(context.Background(), "other")
	if err != nil || resolved != nil {
		t.Fatalf("foreign-provider key = %v, %v", resolved, err)
	}
}

func modelListContains(models []ai.Model, provider, id string) bool {
	for _, model := range models {
		if string(model.Provider) == provider && model.ID == id {
			return true
		}
	}
	return false
}

// Claude Code's native tools stand in for Orb's, so a Claude session gets the
// tools' environment: an agent's credentials stay out, and what Orb exports to
// its tools, such as the buzz shim's socket, comes in.
func TestClaudeRunsWithTheToolEnvironment(t *testing.T) {
	t.Setenv(toolenv.Allow, "PATH,HOME")
	t.Setenv("BUZZ_PRIVATE_KEY", "agent-secret")
	t.Setenv("CLAUDE_CONFIG_DIR", "/claude")
	toolenv.Export("ORB_TEST_EXPORT", "socket")
	env := claudeEnv()
	for _, want := range []string{"ORB_TEST_EXPORT=socket", "CLAUDE_CONFIG_DIR=/claude", "PATH=" + os.Getenv("PATH")} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %s: %v", want, env)
		}
	}
	if toolenv.Get(env, "BUZZ_PRIVATE_KEY") != "" {
		t.Errorf("env holds the agent's key: %v", env)
	}
}
