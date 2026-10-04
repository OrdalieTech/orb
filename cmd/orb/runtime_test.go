package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
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
