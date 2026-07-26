package providers

import (
	"context"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
)

// Upstream 7b52cef2/a5afc3f1: openrouter and kimi-coding expose OAuth login
// flows alongside their env API-key auth.
func TestOpenRouterAndKimiCodingExposeOAuth(t *testing.T) {
	for _, test := range []struct {
		id        ai.ProviderID
		oauthName string
		label     string
	}{
		{"openrouter", "OpenRouter OAuth", "Sign in with OpenRouter"},
		{"kimi-coding", "Kimi Code (subscription)", "Sign in with Kimi Code"},
	} {
		provider, ok := Get(test.id)
		if !ok || !provider.OAuth || provider.Methods.APIKey == nil || provider.Methods.OAuth == nil {
			t.Fatalf("%s provider = %#v", test.id, provider)
		}
		if provider.Methods.OAuth.Name() != test.oauthName {
			t.Fatalf("%s OAuth name = %q, want %q", test.id, provider.Methods.OAuth.Name(), test.oauthName)
		}
		labelled, ok := provider.Methods.OAuth.(auth.OAuthLoginLabel)
		if !ok || labelled.LoginLabel() != test.label {
			t.Fatalf("%s login label = %#v", test.id, provider.Methods.OAuth)
		}
	}

	images := OpenRouterImages()
	if images.Auth().APIKey == nil || images.Auth().OAuth == nil || images.Auth().OAuth.Name() != "OpenRouter OAuth" {
		t.Fatalf("OpenRouter images auth = %#v", images.Auth())
	}
}

// The stored OAuth credential is a permanent key: both the text and image
// providers resolve it as plain apiKey auth without ever refreshing.
func TestOpenRouterStoredOAuthKeyResolvesForTextAndImages(t *testing.T) {
	ctx := context.Background()
	store := auth.NewMemoryStore(map[string]*auth.Credential{
		"openrouter": auth.OAuthCredentialAccessFirst("sk-or-stored", "", 9007199254740991),
	})
	provider, ok := Get("openrouter")
	if !ok {
		t.Fatal("openrouter provider is not registered")
	}
	resolved, err := auth.ResolveProviderAuth(ctx, "openrouter", provider.Methods, store, auth.EnvironmentContext{}, nil)
	if err != nil || resolved == nil || resolved.Auth.APIKey == nil || *resolved.Auth.APIKey != "sk-or-stored" {
		t.Fatalf("text provider auth = %#v, %v", resolved, err)
	}
	imagesResolved, err := auth.ResolveProviderAuth(ctx, "openrouter", OpenRouterImages().Auth(), store, auth.EnvironmentContext{}, nil)
	if err != nil || imagesResolved == nil || imagesResolved.Auth.APIKey == nil || *imagesResolved.Auth.APIKey != "sk-or-stored" {
		t.Fatalf("images provider auth = %#v, %v", imagesResolved, err)
	}
}
