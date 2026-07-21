package providers_test

import (
	"slices"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

func TestWP241ProviderDefinitions(t *testing.T) {
	t.Parallel()
	var fixture struct {
		Providers []struct {
			ID      ai.ProviderID `json:"id"`
			Name    string        `json:"name"`
			BaseURL string        `json:"baseUrl"`
			APIs    []ai.API      `json:"apis"`
			Auth    struct {
				APIKeyName string `json:"apiKeyName"`
				OAuthName  string `json:"oauthName"`
			} `json:"auth"`
		} `json:"providers"`
	}
	runner.LoadJSON(t, "F2", "subscription-providers.json", &fixture)
	expectedAuth := map[ai.ProviderID]struct {
		kind providers.AuthKind
		env  []string
	}{
		"openai-codex":   {providers.AuthOAuth, nil},
		"github-copilot": {providers.AuthAPIKey, []string{"COPILOT_GITHUB_TOKEN"}},
		"xai":            {providers.AuthAPIKey, []string{"XAI_API_KEY"}},
	}
	for _, expected := range fixture.Providers {
		expected := expected
		t.Run(string(expected.ID), func(t *testing.T) {
			t.Parallel()
			provider, ok := providers.Get(expected.ID)
			if !ok {
				t.Fatalf("provider %q is not registered", expected.ID)
			}
			authMetadata := expectedAuth[expected.ID]
			if len(expected.APIs) == 0 || provider.Name != expected.Name || provider.BaseURL != expected.BaseURL || provider.API != expected.APIs[0] || provider.Auth != authMetadata.kind || !slices.Equal(provider.Env, authMetadata.env) {
				t.Fatalf("provider = %#v", provider)
			}
			if provider.Methods.OAuth == nil || provider.Methods.OAuth.Name() != expected.Auth.OAuthName {
				t.Fatalf("OAuth method = %#v", provider.Methods.OAuth)
			}
			if expected.Auth.APIKeyName == "" {
				if provider.Methods.APIKey != nil {
					t.Fatalf("API-key method = %#v, want nil", provider.Methods.APIKey)
				}
			} else if provider.Methods.APIKey == nil || provider.Methods.APIKey.Name() != expected.Auth.APIKeyName {
				t.Fatalf("API-key method = %#v", provider.Methods.APIKey)
			}
		})
	}
}
