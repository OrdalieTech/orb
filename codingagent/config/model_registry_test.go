package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

func TestModelRegistryFiltersCopilotModelsFromOAuthCredential(t *testing.T) {
	models := []ai.Model{
		{Provider: "github-copilot", ID: "first"},
		{Provider: "openai", ID: "unrelated"},
		{Provider: "github-copilot", ID: "second"},
	}
	credential := aiauth.OAuthCredential("refresh", "access", 1)
	credential.SetExtra("availableModelIds", json.RawMessage(`["second"]`))
	filtered := filterCredentialModels(models, map[string]*aiauth.Credential{"github-copilot": credential})
	if len(filtered) != 2 || filtered[0].ID != "unrelated" || filtered[1].ID != "second" {
		t.Fatalf("filtered models = %#v", filtered)
	}

	credential.SetExtra("availableModelIds", json.RawMessage(`[]`))
	filtered = filterCredentialModels(models, map[string]*aiauth.Credential{"github-copilot": credential})
	if len(filtered) != 1 || filtered[0].ID != "unrelated" {
		t.Fatalf("empty availability list = %#v", filtered)
	}

	credential.SetExtra("availableModelIds", json.RawMessage(`null`))
	if filtered = filterCredentialModels(models, map[string]*aiauth.Credential{"github-copilot": credential}); len(filtered) != len(models) {
		t.Fatalf("null availability list filtered models = %#v", filtered)
	}
}

func TestModelRegistryOfflineEnvUsesPresence(t *testing.T) {
	original, present := os.LookupEnv("PI_OFFLINE")
	if err := os.Unsetenv("PI_OFFLINE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv("PI_OFFLINE", original)
		} else {
			_ = os.Unsetenv("PI_OFFLINE")
		}
	})
	registry, err := NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !registry.allowModelNetwork {
		t.Fatal("unset PI_OFFLINE disabled model network")
	}

	for _, value := range []string{"", "0", "false", "no", "1", "TRUE", "YeS"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PI_OFFLINE", value)
			registry, err := NewModelRegistry(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if registry.allowModelNetwork {
				t.Fatal("present PI_OFFLINE allowed model network")
			}
		})
	}
}

type registryAPIKeyAuth struct {
	resolve func(context.Context, aiauth.AuthContext, *aiauth.Credential) (*aiauth.AuthResult, error)
}

func (method registryAPIKeyAuth) Name() string { return "Registry test key" }

func (method registryAPIKeyAuth) Resolve(
	ctx context.Context,
	authContext aiauth.AuthContext,
	credential *aiauth.Credential,
) (*aiauth.AuthResult, error) {
	return method.resolve(ctx, authContext, credential)
}

func TestModelRegistryHotReloadMatchesErrorSnapshotSemantics(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "models.json")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"providers":{"local":{"baseUrl":"http://localhost/v1","api":"openai-completions","apiKey":"dummy","models":[{"id":"first"}]}}}`)
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("local", "first"); !ok || registry.Error() != "" {
		t.Fatalf("initial registry missing model or has error: %q", registry.Error())
	}

	write(`{"providers":`)
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("local", "first"); ok || !strings.Contains(registry.Error(), "Failed to parse models.json") {
		t.Fatalf("malformed reload did not publish builtin-only error snapshot: %q", registry.Error())
	}

	write(`{"providers":{"local":{"baseUrl":"http://localhost/v1","api":"openai-completions","apiKey":"dummy","models":[{"id":"second"}]}}}`)
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("local", "second"); !ok || registry.Error() != "" {
		t.Fatalf("fixed config did not hot reload: %q", registry.Error())
	}
}

func TestModelRegistryBedrockCredentialChain(t *testing.T) {
	var fixture struct {
		Auth struct {
			Env   []string `json:"env"`
			Cases []struct {
				Name          string            `json:"name"`
				Env           map[string]string `json:"env"`
				Authenticated bool              `json:"authenticated"`
			} `json:"cases"`
		} `json:"auth"`
	}
	runner.LoadJSON(t, "F2", "bedrock-provider.json", &fixture)
	for _, name := range fixture.Auth.Env {
		t.Setenv(name, "")
	}
	registry, err := NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, fixtureCase := range fixture.Auth.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			if got := registry.HasConfiguredAuth("amazon-bedrock", fixtureCase.Env); got != fixtureCase.Authenticated {
				t.Fatalf("HasConfiguredAuth = %t, want %t", got, fixtureCase.Authenticated)
			}
			key, err := registry.ResolveAPIKey(context.Background(), "amazon-bedrock", fixtureCase.Env)
			if err != nil {
				t.Fatal(err)
			}
			if key != nil {
				t.Fatalf("ambient AWS credential source leaked into explicit API key: %q", *key)
			}
		})
	}
}

func TestModelRegistryBedrockStoredProfile(t *testing.T) {
	for _, name := range []string{
		"AWS_BEARER_TOKEN_BEDROCK", "AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_WEB_IDENTITY_TOKEN_FILE",
	} {
		t.Setenv(name, "")
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "auth.json"), []byte(`{"amazon-bedrock":{"type":"api_key","env":{"AWS_PROFILE":"stored-profile"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !registry.HasConfiguredAuth("amazon-bedrock", nil) {
		t.Fatal("stored AWS profile did not make Amazon Bedrock available")
	}
	key, err := registry.ResolveAPIKey(context.Background(), "amazon-bedrock", nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != nil {
		t.Fatalf("stored AWS profile leaked into explicit API key: %q", *key)
	}
}

func TestModelRegistryKeepsOtherProvidersOnCompositionError(t *testing.T) {
	directory := t.TempDir()
	content := `{"providers":{"bad":{"models":[{"id":"broken"}]},"good":{"baseUrl":"http://localhost/v1","api":"openai-completions","apiKey":"dummy","models":[{"id":"working"}]}}}`
	if err := os.WriteFile(filepath.Join(directory, "models.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("good", "working"); !ok {
		t.Fatal("valid provider was dropped after a sibling composition error")
	}
	if _, ok := registry.Find("bad", "broken"); ok || !strings.Contains(registry.Error(), `Provider "bad"`) {
		t.Fatalf("bad provider result/error = %q", registry.Error())
	}
}

func TestModelRegistryMatchesUpstreamCompositionCases(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/WP250/composition-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name                       string          `json:"name"`
		Config                     json.RawMessage `json:"config"`
		GoodModels                 []ai.Model      `json:"goodModels"`
		BadModels                  []ai.Model      `json:"badModels"`
		NonpositiveModels          []ai.Model      `json:"nonpositiveModels"`
		EmptyModels                []ai.Model      `json:"emptyModels"`
		AnthropicProviderPreserved bool            `json:"anthropicProviderPreserved"`
		Error                      string          `json:"error"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "models.json"), fixture.Config, 0o600); err != nil {
				t.Fatal(err)
			}
			registry, err := NewModelRegistry(directory)
			if err != nil {
				t.Fatal(err)
			}
			modelsFor := func(provider string) []ai.Model {
				models := make([]ai.Model, 0)
				for _, model := range registry.Models() {
					if string(model.Provider) == provider {
						models = append(models, model)
					}
				}
				return models
			}
			if !sameJSON(t, modelsFor("good"), fixture.GoodModels) || !sameJSON(t, modelsFor("bad"), fixture.BadModels) ||
				!sameJSON(t, modelsFor("nonpositive"), fixture.NonpositiveModels) || !sameJSON(t, modelsFor("empty"), fixture.EmptyModels) {
				t.Fatalf("provider models differ: good=%#v bad=%#v nonpositive=%#v empty=%#v", modelsFor("good"), modelsFor("bad"), modelsFor("nonpositive"), modelsFor("empty"))
			}
			anthropicPreserved := len(modelsFor("anthropic")) > 0
			if anthropicPreserved != fixture.AnthropicProviderPreserved {
				t.Fatalf("anthropic preserved = %t, want %t", anthropicPreserved, fixture.AnthropicProviderPreserved)
			}
			if registry.Error() != fixture.Error {
				t.Fatalf("error = %q, want %q", registry.Error(), fixture.Error)
			}
		})
	}
}

func TestModelRegistryAvailabilityIncludesStoredCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_OAUTH_TOKEN", "")
	directory := t.TempDir()
	authPath := filepath.Join(directory, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"anthropic":{"type":"oauth","refresh":"r","access":"a","expires":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !registry.HasConfiguredAuth("anthropic", nil) {
		t.Fatal("stored OAuth credential did not make Anthropic available")
	}
	if err := os.WriteFile(authPath, []byte(`{"openai":{"type":"oauth","refresh":"r","access":"a","expires":1},"custom":{"type":"oauth","refresh":"r","access":"a","expires":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	if registry.HasConfiguredAuth("openai", nil) || registry.HasConfiguredAuth("custom", nil) {
		t.Fatal("stored OAuth credential without a matching OAuth handler reported available")
	}
	if err := os.WriteFile(authPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	if registry.HasConfiguredAuth("anthropic", nil) {
		t.Fatal("removed stored credential remained available after reload")
	}
	if !registry.HasConfiguredAuth("anthropic", map[string]string{"ANTHROPIC_OAUTH_TOKEN": "oauth-token"}) {
		t.Fatal("ambient Anthropic OAuth token did not make Anthropic available")
	}
}

func TestModelRegistryAvailabilityResolvesStoredAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("WP211_MISSING_KEY", "")
	directory := t.TempDir()
	authPath := filepath.Join(directory, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"openai":{"type":"api_key","key":"$WP211_MISSING_KEY"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if registry.HasConfiguredAuth("openai", nil) {
		t.Fatal("unresolved stored API key reported OpenAI available")
	}
	if !registry.HasConfiguredAuth("openai", map[string]string{"OPENAI_API_KEY": "ambient"}) {
		t.Fatal("stored API-key handler did not use its ambient fallback")
	}
	t.Setenv("WP211_MISSING_KEY", "resolved")
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	if !registry.HasConfiguredAuth("openai", nil) {
		t.Fatal("resolved stored API key did not make OpenAI available")
	}
}

func TestModelRegistryGoogleUsesGeminiAPIKeyOnly(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	registry, err := NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if registry.HasConfiguredAuth("google", map[string]string{"GOOGLE_API_KEY": "legacy"}) {
		t.Fatal("GOOGLE_API_KEY unexpectedly made Google available")
	}
	if !registry.HasConfiguredAuth("google", map[string]string{"GEMINI_API_KEY": "gemini"}) {
		t.Fatal("GEMINI_API_KEY did not make Google available")
	}
	key, err := registry.ResolveAPIKey(context.Background(), "google", map[string]string{"GOOGLE_API_KEY": "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if key != nil {
		t.Fatalf("GOOGLE_API_KEY resolved unexpectedly: %q", *key)
	}
}

func TestModelRegistryExtensionRegistrationMergeOverrideUnregisterAndReload(t *testing.T) {
	registry, err := NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := extensions.ProviderModelConfig{
		ID: "custom", Name: "Custom", API: ai.APIOpenAIResponses, BaseURL: "https://first.invalid/v1",
		Input: ai.InputModalities{ai.InputText}, ContextWindow: 1000, MaxTokens: 100,
	}
	if err := registry.RegisterProviderConfig("extension", extensions.ProviderConfig{
		Name: "Extension", BaseURL: "https://first.invalid/v1", APIKey: "secret", API: ai.APIOpenAIResponses, Models: []extensions.ProviderModelConfig{model},
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterProviderConfig("extension", extensions.ProviderConfig{Headers: map[string]string{"X-Extension": "yes"}}); err != nil {
		t.Fatal(err)
	}
	registered, ok := registry.RegisteredProviderConfig("extension")
	if !ok || registered.BaseURL != "https://first.invalid/v1" || len(registered.Models) != 1 || registered.Headers["X-Extension"] != "yes" {
		t.Fatalf("merged provider config = %#v, ok=%v", registered, ok)
	}
	if _, ok := registry.Find("extension", "custom"); !ok {
		t.Fatal("partial re-registration discarded prior models")
	}
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("extension", "custom"); !ok {
		t.Fatal("reload discarded runtime registration")
	}

	nativeModel := ai.Model{ID: "native", Name: "Native", API: ai.APIOpenAIResponses, Provider: "extension", BaseURL: "https://native.invalid/v1", Input: ai.InputModalities{ai.InputText}, ContextWindow: 1000, MaxTokens: 100}
	nativeStream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return func(func(ai.AssistantMessageEvent, error) bool) {}, nil
	}
	native := extensions.Provider{
		ID: "extension", Name: "Native Extension", Auth: aiauth.ProviderAuth{APIKey: aiauth.EnvAPIKeyAuth{DisplayName: "Native key", EnvVars: []string{"NATIVE_KEY"}}},
		GetModels: func() ([]ai.Model, error) { return []ai.Model{nativeModel}, nil }, Stream: nativeStream, StreamSimple: nativeStream,
	}
	if err := registry.RegisterProvider(native); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.RegisteredProviderConfig("extension"); ok {
		t.Fatal("native registration did not replace legacy registration")
	}
	if found, ok := registry.Find("extension", "native"); !ok || found.BaseURL != nativeModel.BaseURL {
		t.Fatalf("native model = %#v, ok=%v", found, ok)
	}
	if err := registry.RegisterProviderConfig("extension", extensions.ProviderConfig{BaseURL: "https://config.invalid/v1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.RegisteredNativeProvider("extension"); ok {
		t.Fatal("legacy registration did not replace native registration")
	}
	if _, ok := registry.Find("extension", "native"); ok {
		t.Fatal("native model remained after config override")
	}
	if err := registry.UnregisterProvider("extension"); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Provider("extension"); ok {
		t.Fatal("custom provider remained after unregister")
	}

	invalidStream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return nil, nil
	}
	if err := registry.RegisterProviderConfig("broken", extensions.ProviderConfig{Stream: invalidStream}); err == nil {
		t.Fatal("invalid streamSimple registration succeeded")
	}
	if _, ok := registry.RegisteredProviderConfig("broken"); ok {
		t.Fatal("failed registration mutated registry state")
	}
	badNative := native
	badNative.ID = "broken-native"
	badNative.FilterModels = func([]ai.Model, *aiauth.Credential) ([]ai.Model, error) { return nil, errors.New("filter failed") }
	if err := registry.RegisterProvider(badNative); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NATIVE_KEY", "configured")
	if _, err := registry.AvailableWithError(nil); err == nil || !strings.Contains(err.Error(), "filter failed") {
		t.Fatalf("native filter error = %v", err)
	}
	refreshing := native
	refreshing.ID = "refresh-error"
	refreshing.RefreshModels = func(extensions.RefreshModelsContext) error { return errors.New("refresh failed") }
	if err := registry.RegisterProvider(refreshing); err != nil {
		t.Fatal(err)
	}
	if err := registry.Reload(); err == nil || !strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("native refresh error = %v", err)
	}
	if _, ok := registry.RegisteredNativeProvider("refresh-error"); !ok {
		t.Fatal("refresh failure removed the registered provider")
	}
}

func TestModelRegistryExtensionPrecedenceOverModelsJSONWithFinalModelOverrides(t *testing.T) {
	directory := t.TempDir()
	content := `{"providers":{"extension":{"name":"Static","baseUrl":"https://static.invalid/v1","apiKey":"static-key","api":"openai-responses","headers":{"X-Layer":"static-provider"},"models":[{"id":"custom","name":"Static Model","headers":{"X-Layer":"static-model"}}],"modelOverrides":{"custom":{"name":"Final Name","headers":{"X-Override":"static-override"}}}}}}`
	if err := os.WriteFile(filepath.Join(directory, "models.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterProviderConfig("extension", extensions.ProviderConfig{
		Name: "Extension", BaseURL: "https://extension.invalid/v1", APIKey: "extension-key",
		Headers: map[string]string{"X-Provider": "extension-provider"},
		Models:  []extensions.ProviderModelConfig{{ID: "custom", Name: "Extension Model", Headers: map[string]string{"X-Layer": "extension-model"}}},
	}); err != nil {
		t.Fatal(err)
	}
	model, ok := registry.Find("extension", "custom")
	if !ok || model.Name != "Final Name" || model.BaseURL != "https://extension.invalid/v1" {
		t.Fatalf("composed extension model = %#v, ok=%v", model, ok)
	}
	key, err := registry.ResolveConfiguredAPIKey(context.Background(), "extension", nil)
	if err != nil || key == nil || *key != "extension-key" {
		t.Fatalf("extension API key = %v, err=%v", key, err)
	}
	headers, err := registry.ResolveModelHeaders(context.Background(), model, nil, key)
	if err != nil || headers == nil || (*headers)["X-Layer"] != "extension-model" || (*headers)["X-Provider"] != "extension-provider" || (*headers)["X-Override"] != "static-override" {
		t.Fatalf("extension headers = %#v, err=%v", headers, err)
	}
	if got := registry.ProviderDisplayName("extension"); got != "Extension" {
		t.Fatalf("provider display name = %q", got)
	}
}

func TestModelRegistryRegistrationOrderTracksOverridesAndReinsertions(t *testing.T) {
	directory := t.TempDir()
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := registry.RegisterProviderConfig(id, extensions.ProviderConfig{Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	stream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return func(func(ai.AssistantMessageEvent, error) bool) {}, nil
	}
	native := extensions.Provider{
		ID: "first", Name: "native", Auth: aiauth.ProviderAuth{APIKey: registeredAPIKeyAuth{name: "key", value: "key"}},
		GetModels: func() ([]ai.Model, error) { return nil, nil }, Stream: stream, StreamSimple: stream,
	}
	if err := registry.RegisterProvider(native); err != nil {
		t.Fatal(err)
	}
	if got := registry.RegisteredProviderIDs(); !slices.Equal(got, []string{"second", "first"}) {
		t.Fatalf("native override order = %#v", got)
	}
	if err := registry.RegisterProviderConfig("first", extensions.ProviderConfig{Name: "config"}); err != nil {
		t.Fatal(err)
	}
	if err := registry.UnregisterProvider("second"); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterProviderConfig("second", extensions.ProviderConfig{Name: "second again"}); err != nil {
		t.Fatal(err)
	}
	if got := registry.RegisteredProviderIDs(); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("reinserted config order = %#v", got)
	}
}

func TestRegisteredProviderModelsUseMatchingDefaultsWithoutPublishingConfiguredHeaders(t *testing.T) {
	all := []ai.Model{
		{ID: "first", Provider: "fixture", API: ai.APIOpenAIResponses, BaseURL: "https://first.invalid"},
		{ID: "second", Provider: "fixture", API: ai.APIAnthropicMessages, BaseURL: "https://second.invalid"},
	}
	updated, err := applyRegisteredConfig(all, "fixture", extensions.ProviderConfig{
		Defined: map[string]bool{"models": true},
		Models: []extensions.ProviderModelConfig{{
			ID: "second", Name: "Second", Headers: map[string]string{"X-Model": "configured"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].API != ai.APIAnthropicMessages || updated[0].BaseURL != "https://second.invalid" {
		t.Fatalf("registered model = %#v", updated)
	}
	if updated[0].Headers != nil {
		t.Fatalf("configured request headers leaked into model catalog: %#v", *updated[0].Headers)
	}
}

func TestModelRegistryProviderRefreshAndNativeModelsJSONComposition(t *testing.T) {
	previousOffline, hadOffline := os.LookupEnv("PI_OFFLINE")
	if err := os.Unsetenv("PI_OFFLINE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadOffline {
			_ = os.Setenv("PI_OFFLINE", previousOffline)
		} else {
			_ = os.Unsetenv("PI_OFFLINE")
		}
	})

	directory := t.TempDir()
	content := `{"providers":{"dynamic":{"name":"Configured Native","baseUrl":"https://configured.invalid/v1","apiKey":"configured-key","headers":{"X-Provider":"configured","x-native-auth":"configured-override"},"authHeader":true,"modelOverrides":{"dynamic-model":{"name":"Configured Model","headers":{"X-Model":"override"}}}}}}`
	if err := os.WriteFile(filepath.Join(directory, "models.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}

	model := ai.Model{
		ID: "dynamic-model", Name: "Native Model", API: ai.APIOpenAIResponses, Provider: "dynamic",
		BaseURL: "https://native.invalid/v1", Input: ai.InputModalities{ai.InputText}, ContextWindow: 1000, MaxTokens: 100,
	}
	refreshNetwork := make(chan bool, 2)
	stream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return func(func(ai.AssistantMessageEvent, error) bool) {}, nil
	}
	native := extensions.Provider{
		ID: "dynamic", Name: "Native", Auth: aiauth.ProviderAuth{APIKey: registryAPIKeyAuth{resolve: func(_ context.Context, _ aiauth.AuthContext, credential *aiauth.Credential) (*aiauth.AuthResult, error) {
			if credential == nil || credential.Key == nil || *credential.Key != "configured-key" {
				return nil, fmt.Errorf("native auth credential = %#v", credential)
			}
			key, header := *credential.Key, "resolved"
			return &aiauth.AuthResult{Auth: aiauth.ModelAuth{APIKey: &key, Headers: ai.ProviderHeaders{"X-Native-Auth": &header}}, Source: "native"}, nil
		}}},
		GetModels: func() ([]ai.Model, error) { return []ai.Model{model}, nil }, Stream: stream, StreamSimple: stream,
		RefreshModels: func(ctx extensions.RefreshModelsContext) error {
			refreshNetwork <- ctx.AllowNetwork
			if ctx.Credential == nil || ctx.Credential.Key == nil || *ctx.Credential.Key != "configured-key" {
				return fmt.Errorf("refresh credential = %#v", ctx.Credential)
			}
			return nil
		},
	}
	if err := registry.RegisterProvider(native); err != nil {
		t.Fatal(err)
	}
	select {
	case allowNetwork := <-refreshNetwork:
		if allowNetwork {
			t.Fatal("registration refresh allowed network access")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registration refresh was not scheduled")
	}
	configured, ok := registry.Find("dynamic", "dynamic-model")
	if !ok || configured.Name != "Configured Model" || configured.BaseURL != "https://configured.invalid/v1" {
		t.Fatalf("configured native model = %#v, ok=%v", configured, ok)
	}
	if got := registry.ProviderDisplayName("dynamic"); got != "Configured Native" {
		t.Fatalf("provider display name = %q", got)
	}
	if !registry.HasConfiguredAuth("dynamic", nil) {
		t.Fatal("models.json API key did not configure native provider")
	}
	key, err := registry.ResolveConfiguredAPIKey(context.Background(), "dynamic", nil)
	if err != nil || key == nil || *key != "configured-key" {
		t.Fatalf("configured key = %v, err=%v", key, err)
	}
	resolvedAuth, err := registry.ResolveProviderAuth(context.Background(), "dynamic", nil)
	if err != nil || resolvedAuth == nil || resolvedAuth.Auth.APIKey == nil || *resolvedAuth.Auth.APIKey != "configured-key" || providerHeaderValue(resolvedAuth.Auth.Headers, "x-native-auth") != "configured-override" || providerHeaderValue(resolvedAuth.Auth.Headers, "X-Provider") != "configured" || providerHeaderValue(resolvedAuth.Auth.Headers, "Authorization") != "Bearer configured-key" || len(resolvedAuth.Auth.Headers) != 3 {
		t.Fatalf("composed native auth = %#v, err=%v", resolvedAuth, err)
	}
	headers, err := registry.ResolveModelHeaders(context.Background(), configured, nil, key)
	if err != nil || headers == nil || (*headers)["X-Provider"] != "configured" || (*headers)["X-Model"] != "override" || (*headers)["Authorization"] != "Bearer configured-key" {
		t.Fatalf("configured native headers = %#v, err=%v", headers, err)
	}
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	select {
	case allowNetwork := <-refreshNetwork:
		if !allowNetwork {
			t.Fatal("online reload disabled provider network access")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not refresh provider")
	}
}

func TestModelRegistryConfigProviderRefreshPublishesModels(t *testing.T) {
	directory := t.TempDir()
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	refreshNetwork := make(chan bool, 1)
	providerModel := extensions.ProviderModelConfig{
		ID: "refreshed", Name: "Refreshed", API: ai.APIOpenAIResponses, BaseURL: "https://dynamic.invalid/v1",
		Input: ai.InputModalities{ai.InputText}, ContextWindow: 1000, MaxTokens: 100,
	}
	if err := registry.RegisterProviderConfig("dynamic-config", extensions.ProviderConfig{
		APIKey: "key", API: ai.APIOpenAIResponses, BaseURL: "https://dynamic.invalid/v1",
		RefreshModels: func(ctx extensions.RefreshModelsContext) ([]extensions.ProviderModelConfig, error) {
			refreshNetwork <- ctx.AllowNetwork
			return []extensions.ProviderModelConfig{providerModel}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case allowNetwork := <-refreshNetwork:
		if allowNetwork {
			t.Fatal("registration refresh allowed network access")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registration refresh was not scheduled")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := registry.Find("dynamic-config", "refreshed"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("refreshed config model was not published at registration")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestModelRegistryExplicitRefreshWinsOverOverlappingAutomaticRefresh(t *testing.T) {
	registry, err := NewModelRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	automaticStarted := make(chan struct{})
	explicitStarted := make(chan struct{})
	releaseAutomatic := make(chan struct{})
	releaseExplicit := make(chan struct{})
	var automaticOnce, explicitOnce sync.Once
	model := func(name string) extensions.ProviderModelConfig {
		return extensions.ProviderModelConfig{
			ID: "overlap", Name: name, API: ai.APIOpenAIResponses, BaseURL: "https://overlap.invalid/v1",
			Input: ai.InputModalities{ai.InputText}, ContextWindow: 1000, MaxTokens: 100,
		}
	}
	if err := registry.RegisterProviderConfig("overlap", extensions.ProviderConfig{
		APIKey: "key", API: ai.APIOpenAIResponses, BaseURL: "https://overlap.invalid/v1", Models: []extensions.ProviderModelConfig{model("Initial")},
		RefreshModels: func(ctx extensions.RefreshModelsContext) ([]extensions.ProviderModelConfig, error) {
			if ctx.AllowNetwork {
				explicitOnce.Do(func() { close(explicitStarted) })
				<-releaseExplicit
				return []extensions.ProviderModelConfig{model("Network")}, nil
			}
			automaticOnce.Do(func() { close(automaticStarted) })
			<-releaseAutomatic
			return []extensions.ProviderModelConfig{model("Cached")}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-automaticStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("automatic refresh did not start")
	}
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- registry.Reload() }()
	select {
	case <-explicitStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("explicit refresh did not overlap automatic refresh")
	}
	close(releaseAutomatic)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if current, ok := registry.Find("overlap", "overlap"); ok && current.Name == "Cached" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("automatic refresh did not publish before explicit refresh")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseExplicit)
	if err := <-reloadDone; err != nil {
		t.Fatal(err)
	}
	current, ok := registry.Find("overlap", "overlap")
	if !ok || current.Name != "Network" {
		t.Fatalf("explicit refresh model = %#v, ok=%v", current, ok)
	}
}

func TestProviderOAuthAuthIncludesConfiguredHeaders(t *testing.T) {
	authHeader := true
	methods := providerAuthFromLayers(
		"oauth-configured",
		&ModelConfig{Providers: map[string]ModelProviderConfig{"oauth-configured": {
			Headers: map[string]string{"X-Tenant": "static", "X-Env": "$TENANT"}, AuthHeader: &authHeader,
		}}},
		map[string]extensions.ProviderConfig{"oauth-configured": {
			OAuth:   &extensions.OAuthProvider{Name: "OAuth", GetAPIKey: func(extensions.OAuthCredentials) (string, error) { return "oauth-key", nil }},
			Headers: map[string]string{"x-tenant": "extension"}, Defined: map[string]bool{"oauth": true, "headers": true},
		}},
		nil,
	)
	credential := aiauth.OAuthCredential("refresh", "access", time.Now().Add(time.Hour).UnixMilli())
	credential.Env = map[string]string{"TENANT": "tenant-env"}
	auth, err := methods.OAuth.ToAuth(credential)
	if err != nil || auth.APIKey == nil || *auth.APIKey != "oauth-key" || providerHeaderValue(auth.Headers, "x-tenant") != "extension" || providerHeaderValue(auth.Headers, "X-Env") != "tenant-env" || providerHeaderValue(auth.Headers, "Authorization") != "Bearer oauth-key" || len(auth.Headers) != 3 {
		t.Fatalf("configured OAuth auth = %#v, err=%v", auth, err)
	}
}

func TestConfiguredAuthHeadersPreserveSuppressionsAndCloneValues(t *testing.T) {
	apiKey, original := "key", "native"
	source := aiauth.ModelAuth{
		APIKey: &apiKey,
		Headers: ai.ProviderHeaders{
			"X-Native": &original,
			"X-Remove": nil,
		},
	}

	configured, err := withConfiguredModelAuth(source, map[string]string{
		"x-native": "configured",
		"X-Added":  "added",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := configured.Headers["X-Native"]; exists {
		t.Fatal("case-insensitive overlay retained the superseded header spelling")
	}
	if providerHeaderValue(configured.Headers, "x-native") != "configured" || providerHeaderValue(configured.Headers, "X-Added") != "added" || providerHeaderValue(configured.Headers, "Authorization") != "Bearer key" {
		t.Fatalf("configured headers = %#v", configured.Headers)
	}
	if value, exists := configured.Headers["X-Remove"]; !exists || value != nil {
		t.Fatalf("nullable suppression = %#v, exists=%v", value, exists)
	}
	*configured.Headers["x-native"] = "mutated"
	if original != "native" || providerHeaderValue(source.Headers, "X-Native") != "native" {
		t.Fatalf("configured headers mutated the source: %#v", source.Headers)
	}
}

func TestRegistryRequestAuthClonesNullableHeaders(t *testing.T) {
	value := "source"
	resolved := &aiauth.AuthResult{Auth: aiauth.ModelAuth{Headers: ai.ProviderHeaders{"X-Value": &value, "X-Remove": nil}}}
	request := registryRequestAuth(resolved)
	*resolved.Auth.Headers["X-Value"] = "changed"
	delete(resolved.Auth.Headers, "X-Remove")
	if providerHeaderValue(request.Headers, "X-Value") != "source" {
		t.Fatalf("request headers shared source pointers: %#v", request.Headers)
	}
	if removed, exists := request.Headers["X-Remove"]; !exists || removed != nil {
		t.Fatalf("request lost nullable suppression: %#v, exists=%v", removed, exists)
	}
}

func providerHeaderValue(headers ai.ProviderHeaders, name string) string {
	value := headers[name]
	if value == nil {
		return ""
	}
	return *value
}

func TestModelRegistrySerializesExplicitReloadSnapshots(t *testing.T) {
	directory := t.TempDir()
	writeOverride := func(name string) {
		t.Helper()
		content := fmt.Sprintf(`{"providers":{"serial":{"modelOverrides":{"serial-model":{"name":%q}}}}}`, name)
		if err := os.WriteFile(filepath.Join(directory, "models.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeOverride("First")
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	model := func(name string) extensions.ProviderModelConfig {
		return extensions.ProviderModelConfig{
			ID: "serial-model", Name: name, API: ai.APIOpenAIResponses, BaseURL: "https://serial.invalid/v1",
			Input: ai.InputModalities{ai.InputText}, ContextWindow: 1000, MaxTokens: 100,
		}
	}
	explicitStarted := make(chan int32, 2)
	releaseFirst := make(chan struct{})
	var explicitCalls atomic.Int32
	if err := registry.RegisterProviderConfig("serial", extensions.ProviderConfig{
		APIKey: "key", API: ai.APIOpenAIResponses, BaseURL: "https://serial.invalid/v1", Models: []extensions.ProviderModelConfig{model("Initial")},
		RefreshModels: func(ctx extensions.RefreshModelsContext) ([]extensions.ProviderModelConfig, error) {
			if !ctx.AllowNetwork {
				return []extensions.ProviderModelConfig{model("Automatic")}, nil
			}
			call := explicitCalls.Add(1)
			explicitStarted <- call
			if call == 1 {
				<-releaseFirst
			}
			return []extensions.ProviderModelConfig{model(fmt.Sprintf("Explicit %d", call))}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		registered, ok := registry.RegisteredProviderConfig("serial")
		if ok && len(registered.Models) == 1 && registered.Models[0].Name == "Automatic" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("automatic refresh did not settle before explicit reloads")
		}
		time.Sleep(time.Millisecond)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- registry.Reload() }()
	select {
	case call := <-explicitStarted:
		if call != 1 {
			t.Fatalf("first explicit refresh call = %d", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first explicit reload did not start")
	}
	writeOverride("Second")
	secondDone := make(chan error, 1)
	go func() { secondDone <- registry.Reload() }()
	overlapped := false
	select {
	case <-explicitStarted:
		overlapped = true
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if overlapped {
		t.Fatal("explicit reload callbacks overlapped")
	}
	current, ok := registry.Find("serial", "serial-model")
	if !ok || current.Name != "Second" {
		t.Fatalf("serialized reload model = %#v, ok=%v", current, ok)
	}
}
