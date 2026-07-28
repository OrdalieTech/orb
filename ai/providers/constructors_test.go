package providers

import (
	"reflect"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

// Exported constructors must return the registry-completed metadata, not the
// bare per-provider structs (which only carry identity and auth wiring).
func TestExportedConstructorsReturnRegistryMetadata(t *testing.T) {
	constructors := map[ai.ProviderID]func() Provider{
		"amazon-bedrock":         AmazonBedrock,
		"anthropic":              Anthropic,
		"azure-openai-responses": AzureOpenAIResponses,
		"github-copilot":         GitHubCopilot,
		"google":                 Google,
		"google-vertex":          GoogleVertex,
		"kimi-coding":            KimiCoding,
		"mistral":                Mistral,
		"openai":                 OpenAI,
		"openai-codex":           OpenAICodex,
		"openrouter":             OpenRouter,
		"xai":                    XAI,
	}
	for id, constructor := range constructors {
		want, ok := Get(id)
		if !ok {
			t.Fatalf("provider %s missing from registry", id)
		}
		got := constructor()
		if got.ID != want.ID || got.Name != want.Name || got.API != want.API ||
			got.BaseURL != want.BaseURL || got.Auth != want.Auth || got.OAuth != want.OAuth ||
			!reflect.DeepEqual(got.APIs, want.APIs) ||
			!reflect.DeepEqual(got.Env, want.Env) ||
			!reflect.DeepEqual(got.APIKeyEnv, want.APIKeyEnv) {
			t.Fatalf("%s constructor metadata diverges from registry:\n got %#v\nwant %#v", id, got, want)
		}
		if len(got.APIs) == 0 {
			t.Fatalf("%s constructor returned no APIs", id)
		}
	}
}
