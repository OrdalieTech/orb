package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/modes"
)

type fixedInteractiveAPIKeyAuth struct{ key string }

func (method fixedInteractiveAPIKeyAuth) Name() string { return "Extension API key" }

func (method fixedInteractiveAPIKeyAuth) Resolve(_ context.Context, _ aiauth.AuthContext, credential *aiauth.Credential) (*aiauth.AuthResult, error) {
	if credential == nil || credential.Key == nil {
		return nil, nil
	}
	return &aiauth.AuthResult{Auth: aiauth.ModelAuth{APIKey: credential.Key}, Source: "stored credential"}, nil
}

func (method fixedInteractiveAPIKeyAuth) Login(context.Context, aiauth.AuthInteraction) (*aiauth.Credential, error) {
	return aiauth.APIKeyCredential(method.key), nil
}

func TestInteractiveHostEnumeratesAndRunsComposedAuthMethods(t *testing.T) {
	fixture := newHostFixture(t)
	registry, err := config.NewModelRegistry(fixture.agentDir)
	if err != nil {
		t.Fatal(err)
	}
	stream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return nil, nil
	}
	if err := registry.RegisterProvider(extensions.Provider{
		ID: "extension-auth", Name: "Extension Auth",
		Auth: aiauth.ProviderAuth{APIKey: fixedInteractiveAPIKeyAuth{key: "extension-secret"}},
		GetModels: func() ([]ai.Model, error) {
			return []ai.Model{{ID: "extension-model", Provider: "extension-auth", API: ai.APIOpenAIResponses}}, nil
		},
		Stream: stream, StreamSimple: stream,
	}); err != nil {
		t.Fatal(err)
	}
	storage, err := config.NewAuthStorage(filepath.Join(fixture.agentDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.host.mu.Lock()
	fixture.host.inputs.ModelRegistry = registry
	fixture.host.inputs.Auth = storage
	fixture.host.mu.Unlock()

	options, err := fixture.host.AuthOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	has := func(id string, authType aiauth.AuthType) bool {
		for _, option := range options.Login {
			if option.ID == id && option.AuthType == authType {
				return true
			}
		}
		return false
	}
	for _, expected := range []struct {
		id       string
		authType aiauth.AuthType
	}{
		{id: "groq", authType: aiauth.AuthTypeAPIKey},
		{id: "anthropic", authType: aiauth.AuthTypeAPIKey},
		{id: "anthropic", authType: aiauth.AuthTypeOAuth},
		{id: "extension-auth", authType: aiauth.AuthTypeAPIKey},
	} {
		if !has(expected.id, expected.authType) {
			t.Fatalf("login options missing %s/%s: %#v", expected.id, expected.authType, options.Login)
		}
	}

	if err := fixture.host.Login(context.Background(), "groq", aiauth.AuthTypeAPIKey, fixedPromptInteraction{value: "groq-secret"}); err != nil {
		t.Fatal(err)
	}
	credential, err := storage.Read(context.Background(), "groq")
	if err != nil || credential == nil || credential.Type != aiauth.CredentialAPIKey || credential.Key == nil || *credential.Key != "groq-secret" {
		t.Fatalf("stored groq credential = %#v, err=%v", credential, err)
	}
	if err := fixture.host.Login(context.Background(), "extension-auth", aiauth.AuthTypeAPIKey, fixedPromptInteraction{}); err != nil {
		t.Fatal(err)
	}
	credential, err = storage.Read(context.Background(), "extension-auth")
	if err != nil || credential == nil || credential.Key == nil || *credential.Key != "extension-secret" {
		t.Fatalf("stored extension credential = %#v, err=%v", credential, err)
	}
}

type fixedPromptInteraction struct{ value string }

func (interaction fixedPromptInteraction) Prompt(context.Context, aiauth.AuthPrompt) (string, error) {
	return interaction.value, nil
}

func (fixedPromptInteraction) Notify(aiauth.AuthEvent) {}

func TestInteractiveHostEnumeratesModelsJSONProviderWithoutModels(t *testing.T) {
	fixture := newHostFixture(t)
	modelsPath := filepath.Join(fixture.agentDir, "models.json")
	if err := os.MkdirAll(fixture.agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"config-auth":{"name":"Config Auth","baseUrl":"https://config-auth.invalid/v1","apiKey":"configured"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(fixture.agentDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.host.mu.Lock()
	fixture.host.inputs.ModelRegistry = registry
	fixture.host.mu.Unlock()

	options, err := fixture.host.AuthOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range options.Login {
		if option.ID == "config-auth" && option.AuthType == aiauth.AuthTypeAPIKey {
			return
		}
	}
	t.Fatalf("models.json-only provider missing from login options: %#v", options.Login)
}

func TestInteractiveHostDescribesConfiguredAuthPerMethod(t *testing.T) {
	fixture := newHostFixture(t)
	t.Setenv("GROQ_API_KEY", "ambient-groq-key")
	storage, err := config.NewAuthStorage(filepath.Join(fixture.agentDir, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Modify(context.Background(), "anthropic", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return aiauth.APIKeyCredential("stored-anthropic-key"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Modify(context.Background(), "xai", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return aiauth.OAuthCredential("refresh", "access", 1), nil
	}); err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(fixture.agentDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.host.mu.Lock()
	fixture.host.inputs.ModelRegistry = registry
	fixture.host.inputs.Auth = storage
	fixture.host.mu.Unlock()

	options, err := fixture.host.AuthOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	find := func(id string, authType aiauth.AuthType) modes.InteractiveAuthProvider {
		t.Helper()
		for _, option := range options.Login {
			if option.ID == id && option.AuthType == authType {
				return option
			}
		}
		t.Fatalf("missing auth option %s/%s", id, authType)
		return modes.InteractiveAuthProvider{}
	}
	// LOG-m3: statuses surface the raw runtime sources like upstream
	// getProviderAuthStatus ("stored", env label, ...), not invented labels.
	for _, option := range []modes.InteractiveAuthProvider{
		find("anthropic", aiauth.AuthTypeOAuth),
		find("anthropic", aiauth.AuthTypeAPIKey),
	} {
		if option.Status == nil || option.Status.Type != aiauth.AuthTypeAPIKey || option.Status.Source != "stored" {
			t.Fatalf("anthropic %s status = %#v", option.AuthType, option.Status)
		}
	}
	groq := find("groq", aiauth.AuthTypeAPIKey)
	if groq.Status == nil || groq.Status.Type != aiauth.AuthTypeAPIKey || groq.Status.Source != "GROQ_API_KEY" {
		t.Fatalf("groq status = %#v", groq.Status)
	}
	for _, option := range []modes.InteractiveAuthProvider{
		find("xai", aiauth.AuthTypeOAuth),
		find("xai", aiauth.AuthTypeAPIKey),
	} {
		if option.Status == nil || option.Status.Type != aiauth.AuthTypeOAuth || option.Status.Source != "stored" {
			t.Fatalf("xAI %s status = %#v", option.AuthType, option.Status)
		}
	}
}

// LOG-m3: --api-key is a runtime credential upstream, so it must surface as
// source "runtime", participate in /logout, and disappear from both request
// auth and the stored credential beneath it when logged out.
func TestLOGm3InteractiveHostRuntimeAPIKeyStatusAndLogout(t *testing.T) {
	fixture := newHostFixture(t)
	registry, err := config.NewModelRegistry(fixture.agentDir)
	if err != nil {
		t.Fatal(err)
	}
	stream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return nil, nil
	}
	if err := registry.RegisterProvider(extensions.Provider{
		ID: "runtime-auth", Name: "Runtime Auth",
		Auth: aiauth.ProviderAuth{APIKey: fixedInteractiveAPIKeyAuth{}},
		GetModels: func() ([]ai.Model, error) {
			return []ai.Model{{ID: "runtime-model", Provider: "runtime-auth", API: ai.APIOpenAIResponses}}, nil
		},
		Stream: stream, StreamSimple: stream,
	}); err != nil {
		t.Fatal(err)
	}
	baseStore := aiauth.NewMemoryStore(map[string]*aiauth.Credential{
		"runtime-auth": aiauth.APIKeyCredential("stored-under-runtime"),
	})
	runtimeAuth := newRuntimeCredentials(baseStore)
	runtimeAuth.SetRuntimeAPIKey("runtime-auth", "runtime-key")
	resolver := requestAuthResolverWithCredentials(registry, runtimeAuth)
	cliKey := "runtime-key"
	fixture.host.mu.Lock()
	fixture.host.args.APIKey = &cliKey
	fixture.host.inputs.RuntimeAuth = runtimeAuth
	fixture.host.inputs.ModelRegistry = registry
	fixture.host.inputs.GetRequestAuth = resolver
	fixture.host.mu.Unlock()

	options, err := fixture.host.AuthOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var loginStatus *modes.InteractiveAuthStatus
	for _, option := range options.Login {
		if option.ID == "runtime-auth" && option.AuthType == aiauth.AuthTypeAPIKey {
			loginStatus = option.Status
		}
	}
	if loginStatus == nil || loginStatus.Type != aiauth.AuthTypeAPIKey || loginStatus.Source != "runtime" {
		t.Fatalf("runtime login status = %#v in %#v", loginStatus, options.Login)
	}
	if len(options.Logout) != 1 || options.Logout[0].ID != "runtime-auth" || options.Logout[0].AuthType != aiauth.AuthTypeAPIKey {
		t.Fatalf("runtime logout options = %#v", options.Logout)
	}
	resolved, err := resolver(context.Background(), "runtime-auth")
	if err != nil || resolved == nil || resolved.APIKey == nil || *resolved.APIKey != "runtime-key" {
		t.Fatalf("runtime request auth = %#v, %v", resolved, err)
	}

	if err := fixture.host.Logout(context.Background(), "runtime-auth"); err != nil {
		t.Fatal(err)
	}
	if runtimeAuth.HasRuntimeAPIKey("runtime-auth") {
		t.Fatal("runtime API key survived logout")
	}
	credential, err := baseStore.Read(context.Background(), "runtime-auth")
	if err != nil || credential != nil {
		t.Fatalf("stored credential after runtime logout = %#v, %v", credential, err)
	}
	resolved, err = resolver(context.Background(), "runtime-auth")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != nil && resolved.APIKey != nil && *resolved.APIKey == "runtime-key" {
		t.Fatalf("request auth retained runtime key after logout: %#v", resolved)
	}
	fixture.host.mu.Lock()
	remainingCLIKey := fixture.host.args.APIKey
	fixture.host.mu.Unlock()
	if remainingCLIKey != nil {
		t.Fatalf("replacement runtime would restore --api-key: %q", *remainingCLIKey)
	}
	options, err = fixture.host.AuthOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range options.Logout {
		if option.ID == "runtime-auth" {
			t.Fatalf("runtime credential remained in logout list: %#v", options.Logout)
		}
	}
	originalCreateRuntime := fixture.host.dependencies.createRuntime
	replacementCalled := false
	fixture.host.dependencies.createRuntime = func(cwd string, args CLIArgs, prior agent.AgentMessages) (runtimeInputs, error) {
		replacementCalled = true
		if args.APIKey != nil {
			t.Fatalf("replacement runtime received logged-out --api-key: %q", *args.APIKey)
		}
		return originalCreateRuntime(cwd, args, prior)
	}
	if _, err := fixture.host.NewSession(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !replacementCalled {
		t.Fatal("session replacement did not rebuild the runtime")
	}
}

func TestInteractiveHostPreservesComposedOAuthLoginLabel(t *testing.T) {
	fixture := newHostFixture(t)
	if err := os.MkdirAll(fixture.agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(fixture.agentDir, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"xai":{"baseUrl":"https://xai-proxy.invalid/v1","headers":{"X-Proxy":"enabled"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(fixture.agentDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.host.mu.Lock()
	fixture.host.inputs.ModelRegistry = registry
	fixture.host.mu.Unlock()

	options, err := fixture.host.AuthOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range options.Login {
		if option.ID == "xai" && option.AuthType == aiauth.AuthTypeOAuth {
			if option.LoginLabel != "Sign in with SuperGrok or X Premium" {
				t.Fatalf("xAI login label = %q", option.LoginLabel)
			}
			return
		}
	}
	t.Fatal("xAI OAuth option missing")
}

func TestInteractiveHostMapsConfiguredAuthSources(t *testing.T) {
	fixture := newHostFixture(t)
	if err := os.MkdirAll(fixture.agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(fixture.agentDir, "models.json")
	modelsJSON := `{"providers":{"key-auth":{"baseUrl":"https://key.invalid/v1","apiKey":"literal"},"command-auth":{"baseUrl":"https://command.invalid/v1","apiKey":"!credential-command"}}}`
	if err := os.WriteFile(modelsPath, []byte(modelsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	registry, err := config.NewModelRegistry(fixture.agentDir)
	if err != nil {
		t.Fatal(err)
	}
	fixture.host.mu.Lock()
	fixture.host.inputs.ModelRegistry = registry
	fixture.host.mu.Unlock()

	options, err := fixture.host.AuthOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// LOG-m3: raw upstream sources (provider-composer.ts
	// configuredRequestAuthStatus), not invented friendly labels.
	want := map[string]string{"key-auth": "models_json_key", "command-auth": "models_json_command"}
	for _, option := range options.Login {
		source, expected := want[option.ID]
		if !expected || option.AuthType != aiauth.AuthTypeAPIKey {
			continue
		}
		if option.Status == nil || option.Status.Source != source {
			t.Fatalf("%s status = %#v, want source %q", option.ID, option.Status, source)
		}
		delete(want, option.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing configured auth statuses: %#v", want)
	}
}
