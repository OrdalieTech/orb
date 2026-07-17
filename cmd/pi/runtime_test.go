package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/config"
)

func TestCreateRuntimeInputsUsesResolvedResourcesAndToolSelection(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("project rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)

	model := "gpt-test"
	args := CLIArgs{
		Model:        &model,
		Tools:        []string{"read", "grep", "missing"},
		ExcludeTools: []string{"grep"},
	}
	runtime, err := createRuntimeInputs(cwd, args, agent.AgentMessages{})
	if err != nil {
		t.Fatal(err)
	}
	state := runtime.Agent.State()
	if state.Model == nil || state.Model.ID != model || state.Model.Provider != "openai" {
		t.Fatalf("model = %#v", state.Model)
	}
	if state.ThinkingLevel != ai.ModelThinkingMedium {
		t.Fatalf("thinking level = %q, want %q", state.ThinkingLevel, ai.ModelThinkingMedium)
	}
	if len(state.Tools) != 1 || state.Tools[0].Spec().Name != "read" {
		t.Fatalf("tools = %#v", state.Tools)
	}
	if !strings.Contains(state.SystemPrompt, "project rules") || !strings.Contains(state.SystemPrompt, "- read: Read file contents") {
		t.Fatalf("system prompt omitted resources/tools: %q", state.SystemPrompt)
	}
}

func TestResolveSkeletonModelRequiresSupportedProviderAndModel(t *testing.T) {
	root := t.TempDir()
	manager, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSkeletonModel(CLIArgs{}, manager); err == nil || !strings.Contains(err.Error(), "--model") {
		t.Fatalf("missing model error = %v", err)
	}
	provider := "anthropic"
	model := "claude"
	if _, err := resolveSkeletonModel(CLIArgs{Provider: &provider, Model: &model}, manager); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("provider error = %v", err)
	}
}

func TestNormalizeSkeletonCLIModelSyntax(t *testing.T) {
	tests := []struct {
		name         string
		provider     *string
		model        string
		thinking     *string
		wantProvider *string
		wantModel    string
		wantThinking *string
	}{
		{name: "bare model", model: "gpt-test", wantModel: "gpt-test"},
		{name: "provider prefix", model: "OPENAI/gpt-test", wantProvider: stringValue("openai"), wantModel: "gpt-test"},
		{name: "canonical provider", provider: stringValue("OpenAI"), model: "gpt-test", wantProvider: stringValue("openai"), wantModel: "gpt-test"},
		{name: "matching repeated prefix", provider: stringValue("OPENAI"), model: "openai/gpt-test", wantProvider: stringValue("openai"), wantModel: "gpt-test"},
		{name: "foreign slash belongs to custom id", provider: stringValue("openai"), model: "vendor/name", wantProvider: stringValue("openai"), wantModel: "vendor/name"},
		{name: "thinking suffix", model: "openai/gpt-test:high", wantProvider: stringValue("openai"), wantModel: "gpt-test", wantThinking: stringValue("high")},
		{name: "explicit thinking preserves custom suffix", provider: stringValue("openai"), model: "custom:high", thinking: stringValue("low"), wantProvider: stringValue("openai"), wantModel: "custom:high", wantThinking: stringValue("low")},
		{name: "invalid suffix is model id", model: "custom:banana", wantModel: "custom:banana"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeSkeletonCLIArgs(CLIArgs{Provider: test.provider, Model: &test.model, Thinking: test.thinking})
			if !reflect.DeepEqual(got.Provider, test.wantProvider) || got.Model == nil || *got.Model != test.wantModel || !reflect.DeepEqual(got.Thinking, test.wantThinking) {
				t.Fatalf("normalized = provider %v, model %v, thinking %v", got.Provider, got.Model, got.Thinking)
			}
		})
	}
}

func TestNormalizeSkeletonCLIArgsTreatsEmptyModelAndProviderAsAbsent(t *testing.T) {
	args := normalizeSkeletonCLIArgs(ParseArgs([]string{"--provider", "", "--model", ""}))
	if args.Provider != nil || args.Model != nil {
		t.Fatalf("normalized selection = %v/%v, want absent", args.Provider, args.Model)
	}
}

func TestResolveSkeletonModelUsesPairedSelectionPrecedence(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	settingsPath := filepath.Join(agentDir, "settings.json")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"defaultProvider":"openai","defaultModel":"settings-model"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}

	providerOnly := "anthropic"
	model, err := resolveSkeletonModel(CLIArgs{Provider: &providerOnly}, manager)
	if err != nil || model.Provider != "openai" || model.ID != "settings-model" {
		t.Fatalf("provider-only selection = %#v, %v", model, err)
	}
	cliModel := "gpt-cli"
	model, err = resolveSkeletonModel(CLIArgs{Model: &cliModel}, manager)
	if err != nil || model.Provider != "openai" || model.ID != cliModel {
		t.Fatalf("CLI model selection = %#v, %v", model, err)
	}
	empty := ""
	model, err = resolveSkeletonModel(CLIArgs{Provider: &empty, Model: &empty}, manager)
	if err != nil || model.Provider != "openai" || model.ID != "settings-model" {
		t.Fatalf("empty CLI selection = %#v, %v", model, err)
	}
	model, err = resolveSkeletonModel(CLIArgs{Provider: &empty, Model: &cliModel}, manager)
	if err != nil || model.Provider != "openai" || model.ID != cliModel {
		t.Fatalf("empty provider inference = %#v, %v", model, err)
	}
}

func TestAPIKeyResolverPrecedence(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "environment")
	key, err := apiKeyResolver(CLIArgs{})(context.Background(), ai.ProviderID("openai"))
	if err != nil || key == nil || *key != "environment" {
		t.Fatalf("environment key = %v, %v", key, err)
	}
	cli := "cli"
	key, err = apiKeyResolver(CLIArgs{APIKey: &cli})(context.Background(), ai.ProviderID("openai"))
	if err != nil || key == nil || *key != "cli" {
		t.Fatalf("CLI key = %v, %v", key, err)
	}
	empty := ""
	key, err = apiKeyResolver(CLIArgs{APIKey: &empty})(context.Background(), ai.ProviderID("openai"))
	if err != nil || key == nil || *key != "environment" {
		t.Fatalf("empty CLI key fallback = %v, %v", key, err)
	}
	key, err = apiKeyResolver(CLIArgs{})(context.Background(), ai.ProviderID("other"))
	if err != nil || !reflect.DeepEqual(key, (*string)(nil)) {
		t.Fatalf("other provider key = %v, %v", key, err)
	}
}
