package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
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
	provider := "openai"
	args := CLIArgs{
		Provider:     &provider,
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
	key, err := apiKeyResolver(CLIArgs{}, nil, nil)(context.Background(), ai.ProviderID("openai"))
	if err != nil || key == nil || *key != "environment" {
		t.Fatalf("environment key = %v, %v", key, err)
	}
	cli := "cli"
	key, err = apiKeyResolver(CLIArgs{APIKey: &cli}, nil, nil)(context.Background(), ai.ProviderID("openai"))
	if err != nil || key == nil || *key != "cli" {
		t.Fatalf("CLI key = %v, %v", key, err)
	}
	empty := ""
	key, err = apiKeyResolver(CLIArgs{APIKey: &empty}, nil, nil)(context.Background(), ai.ProviderID("openai"))
	if err != nil || key == nil || *key != "environment" {
		t.Fatalf("empty CLI key fallback = %v, %v", key, err)
	}
	key, err = apiKeyResolver(CLIArgs{}, nil, nil)(context.Background(), ai.ProviderID("other"))
	if err != nil || !reflect.DeepEqual(key, (*string)(nil)) {
		t.Fatalf("other provider key = %v, %v", key, err)
	}
	stored := aiauth.NewMemoryStore(map[string]*aiauth.Credential{"openai": aiauth.APIKeyCredential("stored")})
	key, err = apiKeyResolver(CLIArgs{}, nil, stored)(context.Background(), ai.ProviderID("openai"))
	if err != nil || key == nil || *key != "stored" {
		t.Fatalf("stored key = %v, %v", key, err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "models.json"), []byte(`{"providers":{"anthropic":{"apiKey":"configured"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	oauthStore := aiauth.NewMemoryStore(map[string]*aiauth.Credential{
		"anthropic": aiauth.OAuthCredential("refresh", "stored-oauth", time.Now().Add(time.Hour).UnixMilli()),
	})
	key, err = apiKeyResolver(CLIArgs{}, registry, oauthStore)(context.Background(), ai.ProviderID("anthropic"))
	if err != nil || key == nil || *key != "stored-oauth" {
		t.Fatalf("stored OAuth ownership = %v, %v", key, err)
	}
}

func TestRequestAuthResolverPreservesStoredVertexADCEnvironment(t *testing.T) {
	credentialsPath := filepath.Join(t.TempDir(), "service-account.json")
	if err := os.WriteFile(credentialsPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	storedEnv := map[string]string{
		"GOOGLE_CLOUD_PROJECT":           "fixture-project",
		"GOOGLE_CLOUD_LOCATION":          "us-central1",
		"GOOGLE_APPLICATION_CREDENTIALS": credentialsPath,
	}
	store := aiauth.NewMemoryStore(map[string]*aiauth.Credential{
		"google-vertex": aiauth.APIKeyEnvCredential(
			storedEnv,
			"GOOGLE_CLOUD_PROJECT",
			"GOOGLE_CLOUD_LOCATION",
			"GOOGLE_APPLICATION_CREDENTIALS",
		),
	})

	resolved, err := requestAuthResolver(CLIArgs{}, nil, store)(context.Background(), ai.ProviderID("google-vertex"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved == nil || resolved.APIKey != nil || !reflect.DeepEqual(map[string]string(resolved.Env), storedEnv) {
		t.Fatalf("resolved request auth = %#v, want stored ADC environment", resolved)
	}
	resolved.Env["GOOGLE_CLOUD_PROJECT"] = "changed"
	credential, err := store.Read(context.Background(), "google-vertex")
	if err != nil {
		t.Fatal(err)
	}
	if credential.Env["GOOGLE_CLOUD_PROJECT"] != "fixture-project" {
		t.Fatal("resolved request auth aliases credential storage")
	}
}

func TestCreateRuntimeInputsResolvesConfiguredHeadersAgainstStoredAuthEnvironment(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("GOOGLE_CLOUD_API_KEY", "")
	credentialsPath := filepath.Join(root, "service-account.json")
	if err := os.WriteFile(credentialsPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	authJSON, err := json.Marshal(map[string]any{
		"google-vertex": map[string]any{
			"type": "api_key",
			"env": map[string]string{
				"GOOGLE_CLOUD_PROJECT":           "stored-project",
				"GOOGLE_CLOUD_LOCATION":          "us-central1",
				"GOOGLE_APPLICATION_CREDENTIALS": credentialsPath,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "auth.json"), authJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	modelsJSON := `{"providers":{"google-vertex":{"api":"google-vertex","baseUrl":"https://{location}-aiplatform.googleapis.com","apiKey":"gcp-vertex-credentials","headers":{"X-Project":"$GOOGLE_CLOUD_PROJECT"},"models":[{"id":"fixture-vertex"}]}}}`
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	providerID, modelID := "google-vertex", "fixture-vertex"
	runtime, err := createRuntimeInputs(root, CLIArgs{
		Provider: &providerID, Model: &modelID, Tools: []string{"read"},
	}, agent.AgentMessages{})
	if err != nil {
		t.Fatal(err)
	}
	requestAuth, err := runtime.GetRequestAuth(context.Background(), ai.ProviderID(providerID))
	if err != nil {
		t.Fatal(err)
	}
	if requestAuth == nil || requestAuth.Env["GOOGLE_CLOUD_PROJECT"] != "stored-project" {
		t.Fatalf("request auth = %#v", requestAuth)
	}
	headers, err := runtime.GetModelHeaders(context.Background(), runtime.Agent.State().Model, requestAuth.APIKey, requestAuth.Env)
	if err != nil {
		t.Fatal(err)
	}
	if headers == nil || (*headers)["X-Project"] != "stored-project" {
		t.Fatalf("configured headers = %#v", headers)
	}
}

func TestResolveRuntimeModelPrefersSavedDefaultWithinScope(t *testing.T) {
	settings, registry := runtimeModelFixture(t,
		`{"defaultProvider":"fixture","defaultModel":"saved","defaultThinkingLevel":"medium"}`,
		`{"providers":{"fixture":{"baseUrl":"https://example.invalid/v1","api":"openai-completions","apiKey":"dummy","models":[{"id":"first"},{"id":"saved"}]}}}`,
	)
	model, thinking, diagnostics, err := resolveRuntimeModel(CLIArgs{
		Models: []string{"fixture/first:low", "fixture/saved:high"},
	}, settings, registry)
	if err != nil {
		t.Fatal(err)
	}
	if model == nil || model.Provider != "fixture" || model.ID != "saved" {
		t.Fatalf("selected model = %#v", model)
	}
	if thinking == nil || *thinking != ai.ModelThinkingHigh {
		t.Fatalf("scoped thinking = %v, want high", thinking)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
}

func TestResolveRuntimeModelRestoresOnlyExactCatalogEntries(t *testing.T) {
	settings, registry := runtimeModelFixture(t,
		`{}`,
		`{"providers":{"openai":{"apiKey":"dummy"}}}`,
	)
	provider, removedID := "openai", "removed-built-in"
	model, thinking, diagnostics, err := resolveRuntimeModel(CLIArgs{
		Provider:      &provider,
		Model:         &removedID,
		RestoredModel: true,
	}, settings, registry)
	if err != nil {
		t.Fatal(err)
	}
	if model == nil || model.Provider != "openai" || model.ID == removedID {
		t.Fatalf("restore fallback = %#v", model)
	}
	if thinking != nil {
		t.Fatalf("restore fallback thinking = %v, want nil", thinking)
	}
	wantDiagnostic := "Could not restore model openai/removed-built-in. Using " + string(model.Provider) + "/" + model.ID
	if len(diagnostics) != 1 || diagnostics[0] != wantDiagnostic {
		t.Fatalf("restore diagnostics = %v", diagnostics)
	}
}

func TestResolveRuntimeModelRestoredModelWithoutAuthUsesJoinedFallbackMessage(t *testing.T) {
	settings, registry := runtimeModelFixture(t,
		`{}`,
		`{"providers":{"saved":{"baseUrl":"https://saved.invalid/v1","api":"openai-completions","models":[{"id":"kept"}]},"fallback":{"baseUrl":"https://fallback.invalid/v1","api":"openai-completions","apiKey":"dummy","models":[{"id":"fallback-model"}]}}}`,
	)
	provider, savedID := "saved", "kept"
	model, thinking, diagnostics, err := resolveRuntimeModel(CLIArgs{
		Provider:      &provider,
		Model:         &savedID,
		RestoredModel: true,
	}, settings, registry)
	if err != nil {
		t.Fatal(err)
	}
	if model == nil || model.Provider != "fallback" || model.ID != "fallback-model" {
		t.Fatalf("restore fallback = %#v", model)
	}
	if thinking != nil {
		t.Fatalf("restore fallback thinking = %v, want nil", thinking)
	}
	want := "Could not restore model saved/kept. Using fallback/fallback-model"
	if len(diagnostics) != 1 || diagnostics[0] != want {
		t.Fatalf("restore diagnostics = %v, want %q", diagnostics, want)
	}
}

func runtimeModelFixture(t *testing.T, settingsJSON, modelsJSON string) (*config.SettingsManager, *config.ModelRegistry) {
	t.Helper()
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settingsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(modelsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	return settings, registry
}
