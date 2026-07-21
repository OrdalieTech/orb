package providers_test

import (
	"slices"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/providers"
)

func TestAzureOpenAIProviderMetadataAndCopies(t *testing.T) {
	provider := providers.AzureOpenAIResponses()
	if provider.ID != "azure-openai-responses" || provider.Name != "Azure OpenAI" || provider.API != ai.APIAzureOpenAIResponses || provider.BaseURL != "" {
		t.Fatalf("unexpected Azure OpenAI provider: %#v", provider)
	}
	wantEnv := []string{"AZURE_OPENAI_API_KEY"}
	if provider.Auth != providers.AuthAPIKey || !slices.Equal(provider.Env, wantEnv) {
		t.Fatalf("unexpected Azure OpenAI auth metadata: %#v", provider)
	}
	method, ok := provider.Methods.APIKey.(auth.EnvAPIKeyAuth)
	if !ok || method.Name() != "Azure OpenAI API key" || !slices.Equal(method.EnvVars, wantEnv) {
		t.Fatalf("unexpected Azure OpenAI auth method: %#v", provider.Methods.APIKey)
	}
	provider.Env[0] = "changed"
	method.EnvVars[0] = "changed"
	fresh := providers.AzureOpenAIResponses()
	if !slices.Equal(fresh.Env, wantEnv) || !slices.Equal(fresh.Methods.APIKey.(auth.EnvAPIKeyAuth).EnvVars, wantEnv) {
		t.Fatal("Azure OpenAI returned mutable provider storage")
	}
}
