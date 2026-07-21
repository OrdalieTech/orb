package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/providers"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/session"
)

func TestCreateBuiltInToolsHonorsImageAutoResizeSetting(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"images":{"autoResize":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2001, 1))); err != nil {
		t.Fatal(err)
	}
	imageBytes := encoded.Bytes()
	if err := os.WriteFile(filepath.Join(cwd, "fixture.png"), imageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	builtIns, err := createBuiltInTools(cwd, []string{"read"}, settings)
	if err != nil {
		t.Fatal(err)
	}
	result, err := builtIns[0].Execute(context.Background(), "call", map[string]any{"path": "fixture.png"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 2 {
		t.Fatalf("read content = %#v", result.Content)
	}
	image, ok := result.Content[1].(*ai.ImageContent)
	if !ok || image.Data != base64.StdEncoding.EncodeToString(imageBytes) || image.MimeType != "image/png" {
		t.Fatalf("image content = %#v", result.Content[1])
	}
}

func TestCreateRuntimeInputsUsesResolvedResourcesAndToolSelection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	agentDir := filepath.Join(root, "agent")
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("project rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(cwd, ".pi", "skills", "inspect", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("---\nname: inspect\ndescription: Inspect files.\n---\nInspect body."), 0o644); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(cwd, ".pi", "prompts", "review.md")
	if err := os.MkdirAll(filepath.Dir(promptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptPath, []byte("Review $1"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)

	model := "gpt-test"
	provider := "openai"
	// The .pi resources make this a trust-requiring project; headless runs
	// need the --approve override (WP-360 trust flow).
	args := CLIArgs{
		Provider:       &provider,
		Model:          &model,
		Tools:          []string{"read", "grep", "missing"},
		ExcludeTools:   []string{"grep"},
		ProjectTrusted: boolPointer(true),
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
	if len(runtime.BaseTools) != 1 || runtime.BaseTools[0].Spec().Name != "read" {
		t.Fatalf("unused extension base tools = %#v", runtime.BaseTools)
	}
	if !strings.Contains(state.SystemPrompt, "project rules") || !strings.Contains(state.SystemPrompt, "- read: Read file contents") || !strings.Contains(state.SystemPrompt, "<name>inspect</name>") {
		t.Fatalf("system prompt omitted resources/tools: %q", state.SystemPrompt)
	}
	if runtime.SlashResolver == nil {
		t.Fatal("slash resolver is nil")
	}
	if expanded, handled := runtime.SlashResolver.ResolvePrompt("/review file.go"); handled || expanded != "Review file.go" {
		t.Fatalf("prompt template = %q, handled %v", expanded, handled)
	}
}

func TestCreateRuntimeInputsLoadsEnabledPackageThemesWithResolvedSourceInfo(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	agentDir := filepath.Join(root, "agent")
	cwd := filepath.Join(root, "project")
	packageDir := filepath.Join(root, "theme-package")
	if err := os.MkdirAll(filepath.Join(packageDir, "themes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve runtime test path")
	}
	builtin, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "..", "..", "codingagent", "modes", "theme", "dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	themePath := filepath.Join(packageDir, "themes", "package-theme.json")
	encodedTheme := strings.Replace(string(builtin), `"name": "dark"`, `"name": "package-theme"`, 1)
	if err := os.WriteFile(themePath, []byte(encodedTheme), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsJSON, err := json.Marshal(map[string]any{"packages": []string{packageDir}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), settingsJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)

	inputs, err := createRuntimeInputs(cwd, CLIArgs{allowNoModel: true}, agent.AgentMessages{})
	if err != nil {
		t.Fatal(err)
	}
	for _, loaded := range inputs.ResourceLoader.GetThemes().Themes {
		if loaded == nil || loaded.Name != "package-theme" {
			continue
		}
		if loaded.SourcePath != themePath || loaded.SourceInfo == nil || loaded.SourceInfo.Path != themePath ||
			loaded.SourceInfo.Source != packageDir || string(loaded.SourceInfo.Scope) != "user" ||
			string(loaded.SourceInfo.Origin) != "package" || loaded.SourceInfo.BaseDir == nil || *loaded.SourceInfo.BaseDir != packageDir {
			t.Fatalf("package theme source = %#v", loaded)
		}
		return
	}
	t.Fatalf("package theme not loaded: %#v", inputs.ResourceLoader.GetThemes())
}

func TestResolveRuntimeModelKeepsScopeAndUsesSavedDefaultWithinIt(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	models := `{"providers":{"local":{"baseUrl":"http://localhost/v1","api":"openai-completions","apiKey":"dummy","models":[{"id":"one","reasoning":true},{"id":"two","reasoning":true}]}}}`
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(models), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsJSON := `{"defaultProvider":"local","defaultModel":"two","enabledModels":["local/one:low","local/two:high"]}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settingsJSON), 0o600); err != nil {
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

	model, thinking, scoped, diagnostics, err := resolveRuntimeModel(CLIArgs{}, settings, registry)
	if err != nil {
		t.Fatal(err)
	}
	if model == nil || model.Provider != "local" || model.ID != "two" || thinking == nil || *thinking != ai.ModelThinkingHigh {
		t.Fatalf("scoped default = model %#v, thinking %v", model, thinking)
	}
	if len(scoped) != 2 || scoped[0].Model.ID != "one" || scoped[1].Model.ID != "two" || len(diagnostics) != 0 {
		t.Fatalf("scope = %#v, diagnostics = %#v", scoped, diagnostics)
	}

	explicit := "local/one"
	model, thinking, scoped, diagnostics, err = resolveRuntimeModel(CLIArgs{Model: &explicit}, settings, registry)
	if err != nil {
		t.Fatal(err)
	}
	if model == nil || model.ID != "one" || thinking != nil || len(scoped) != 2 || len(diagnostics) != 0 {
		t.Fatalf("explicit scoped selection = model %#v, thinking %v, scope %#v, diagnostics %#v", model, thinking, scoped, diagnostics)
	}
}

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
	inputs, err := createRuntimeInputs(root, CLIArgs{Provider: &provider, Model: &modelID, APIKey: &key}, agent.AgentMessages{})
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

func TestNoBuiltinToolsKeepsBuiltinsDiscoverableForExtensions(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"goExtensions":{"pirate":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)
	provider := "openai"
	model := "gpt-test"
	inputs, err := createRuntimeInputs(root, CLIArgs{Provider: &provider, Model: &model, NoBuiltinTools: true}, agent.AgentMessages{})
	if err != nil {
		t.Fatal(err)
	}
	if inputs.Extensions == nil {
		t.Fatal("compiled extension registry was not loaded")
	}
	if len(inputs.ActiveToolNames) != 0 {
		t.Fatalf("initial active tools = %v, want none", inputs.ActiveToolNames)
	}
	if len(inputs.ExcludedTools) != 0 {
		t.Fatalf("excluded tools = %v, want none", inputs.ExcludedTools)
	}
	if len(inputs.BaseTools) != len(defaultBuiltInTools) {
		t.Fatalf("discoverable builtins = %d, want %d", len(inputs.BaseTools), len(defaultBuiltInTools))
	}
}

func TestAPIKeyResolverPrecedence(t *testing.T) {
	resolveKey := func(args CLIArgs, registry *config.ModelRegistry, credentials aiauth.CredentialStore, providerID ai.ProviderID) (*string, error) {
		request, err := requestAuthResolverForProvider(args, nil, registry, credentials)(context.Background(), providerID)
		if err != nil || request == nil {
			return nil, err
		}
		return request.APIKey, nil
	}
	t.Setenv("OPENAI_API_KEY", "environment")
	key, err := resolveKey(CLIArgs{}, nil, nil, ai.ProviderID("openai"))
	if err != nil || key == nil || *key != "environment" {
		t.Fatalf("environment key = %v, %v", key, err)
	}
	cli := "cli"
	key, err = resolveKey(CLIArgs{APIKey: &cli}, nil, nil, ai.ProviderID("openai"))
	if err != nil || key == nil || *key != "cli" {
		t.Fatalf("CLI key = %v, %v", key, err)
	}
	empty := ""
	key, err = resolveKey(CLIArgs{APIKey: &empty}, nil, nil, ai.ProviderID("openai"))
	if err != nil || key == nil || *key != "environment" {
		t.Fatalf("empty CLI key fallback = %v, %v", key, err)
	}
	key, err = resolveKey(CLIArgs{}, nil, nil, ai.ProviderID("other"))
	if err != nil || !reflect.DeepEqual(key, (*string)(nil)) {
		t.Fatalf("other provider key = %v, %v", key, err)
	}
	stored := aiauth.NewMemoryStore(map[string]*aiauth.Credential{"openai": aiauth.APIKeyCredential("stored")})
	key, err = resolveKey(CLIArgs{}, nil, stored, ai.ProviderID("openai"))
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
	key, err = resolveKey(CLIArgs{}, registry, oauthStore, ai.ProviderID("anthropic"))
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

	resolved, err := requestAuthResolverForProvider(CLIArgs{}, nil, nil, store)(context.Background(), ai.ProviderID("google-vertex"))
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
	model, thinking, _, diagnostics, err := resolveRuntimeModel(CLIArgs{
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

func TestCreateRuntimeInputsAllowsMissingModelOnlyForInteractiveBootstrap(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	for _, provider := range providers.List() {
		for _, name := range provider.Env {
			t.Setenv(name, "")
		}
	}

	if _, err := createRuntimeInputs(root, CLIArgs{}, nil); err == nil || !strings.Contains(err.Error(), "no model available") {
		t.Fatalf("headless missing-model error = %v", err)
	}

	runtime, err := createRuntimeInputs(root, CLIArgs{allowNoModel: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if model := runtime.Agent.State().Model; model == nil || model.Provider != "unknown" || model.ID != "unknown" || model.API != "unknown" {
		t.Fatalf("interactive bootstrap model = %#v, want upstream unknown sentinel", model)
	}
	if thinking := runtime.Agent.State().ThinkingLevel; thinking != ai.ModelThinkingOff {
		t.Fatalf("interactive bootstrap thinking = %q, want off without a model", thinking)
	}
	want := strings.TrimSuffix(formatModelList(nil, ""), "\n")
	if !slices.Contains(runtime.Diagnostics, want) {
		t.Fatalf("interactive diagnostics = %#v, want %q", runtime.Diagnostics, want)
	}

	manager, err := session.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	sessionRuntime, err := buildSessionRuntime(runtime, manager, sessionRuntimeOptions{mode: extensions.ModeTUI})
	if err != nil {
		t.Fatal(err)
	}
	host := newInteractiveSessionHost(
		CLIArgs{allowNoModel: true}, cliDependencies{createRuntime: createRuntimeInputs},
		sessionRuntime, runtime, filepath.Join(root, "agent"), nil,
	)
	defer host.Dispose()
	if err := host.Login(context.Background(), "anthropic", aiauth.AuthTypeAPIKey, fixedPromptInteraction{value: "bootstrap-key"}); err != nil {
		t.Fatal(err)
	}
	// LOG-M4: default-model selection after login belongs to the TUI
	// (completeProviderAuthentication); the host only refreshes credentials,
	// so the sentinel stays and the provider's default becomes available.
	model := host.Session().State().Model
	if model == nil || model.Provider != "unknown" || model.ID != "unknown" {
		t.Fatalf("post-login host model = %#v, want untouched unknown sentinel", model)
	}
	available := host.Session().AvailableModels()
	if !slices.ContainsFunc(available, func(candidate ai.Model) bool {
		return candidate.Provider == "anthropic" && candidate.ID == "claude-opus-4-8"
	}) {
		t.Fatalf("post-login available models missing anthropic default: %#v", available)
	}
}

func TestCreateRuntimeInputsSharesResourceLoaderWithSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	for _, provider := range providers.List() {
		for _, name := range provider.Env {
			t.Setenv(name, "")
		}
	}

	inputs, err := createRuntimeInputs(root, CLIArgs{allowNoModel: true, NoExtensions: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := buildSessionRuntime(inputs, manager, sessionRuntimeOptions{mode: extensions.ModeTUI})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)

	loader := runtime.ResourceLoader()
	if loader == nil {
		t.Fatal("CLI session has no resource loader")
	}
	if loader.GetExtensions() != inputs.Extensions {
		t.Fatal("CLI session and runtime inputs do not share the loader-owned extension registry")
	}
}

func TestResolveRuntimeModelRestoresOnlyExactCatalogEntries(t *testing.T) {
	settings, registry := runtimeModelFixture(t,
		`{}`,
		`{"providers":{"openai":{"apiKey":"dummy"}}}`,
	)
	provider, removedID := "openai", "removed-built-in"
	model, thinking, _, diagnostics, err := resolveRuntimeModel(CLIArgs{
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
	model, thinking, _, diagnostics, err := resolveRuntimeModel(CLIArgs{
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
