package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

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
