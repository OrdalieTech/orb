package providers_test

import (
	"slices"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/providers"
)

func TestMistralProviderMetadataAndCopies(t *testing.T) {
	provider := providers.Mistral()
	if provider.ID != "mistral" || provider.Name != "Mistral" || provider.API != ai.APIMistralConversations || provider.BaseURL != "https://api.mistral.ai" {
		t.Fatalf("unexpected Mistral provider: %#v", provider)
	}
	wantEnv := []string{"MISTRAL_API_KEY"}
	if provider.Auth != providers.AuthAPIKey || !slices.Equal(provider.Env, wantEnv) {
		t.Fatalf("unexpected Mistral auth metadata: %#v", provider)
	}
	method, ok := provider.Methods.APIKey.(auth.EnvAPIKeyAuth)
	if !ok || method.Name() != "Mistral API key" || !slices.Equal(method.EnvVars, wantEnv) {
		t.Fatalf("unexpected Mistral auth method: %#v", provider.Methods.APIKey)
	}
	provider.Env[0] = "changed"
	method.EnvVars[0] = "changed"
	fresh := providers.Mistral()
	if !slices.Equal(fresh.Env, wantEnv) || !slices.Equal(fresh.Methods.APIKey.(auth.EnvAPIKeyAuth).EnvVars, wantEnv) {
		t.Fatal("Mistral returned mutable provider storage")
	}
}
