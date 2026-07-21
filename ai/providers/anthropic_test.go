package providers_test

import (
	"slices"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/providers"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type anthropicProviderFixture struct {
	ID      ai.ProviderID `json:"id"`
	Name    string        `json:"name"`
	BaseURL string        `json:"baseUrl"`
	APIs    []ai.API      `json:"apis"`
	Auth    struct {
		Kind      providers.AuthKind `json:"kind"`
		Name      string             `json:"name"`
		OAuthName string             `json:"oauthName"`
		Env       []string           `json:"env"`
	} `json:"auth"`
}

func TestAnthropicProvider(t *testing.T) {
	var fixture anthropicProviderFixture
	runner.LoadJSON(t, "F2", "anthropic-provider.json", &fixture)
	if len(fixture.APIs) != 1 {
		t.Fatalf("upstream Anthropic provider API shapes = %v, want exactly one", fixture.APIs)
	}
	provider, ok := providers.Get(fixture.ID)
	if !ok {
		t.Fatalf("%s provider is not registered", fixture.ID)
	}
	if provider.ID != fixture.ID || provider.Name != fixture.Name || provider.API != fixture.APIs[0] || provider.BaseURL != fixture.BaseURL {
		t.Fatalf("unexpected provider: %#v", provider)
	}
	if provider.Auth != fixture.Auth.Kind || !slices.Equal(provider.Env, fixture.Auth.Env) {
		t.Fatalf("unexpected auth metadata: %#v", provider)
	}
	if provider.Methods.APIKey == nil || provider.Methods.APIKey.Name() != fixture.Auth.Name || provider.Methods.OAuth == nil || provider.Methods.OAuth.Name() != fixture.Auth.OAuthName {
		t.Fatalf("unexpected auth methods: %#v", provider.Methods)
	}

	provider.Env[0] = "changed"
	method := provider.Methods.APIKey.(auth.EnvAPIKeyAuth)
	method.EnvVars[0] = "changed"
	if fresh := providers.Anthropic(); !slices.Equal(fresh.Env, fixture.Auth.Env) {
		t.Fatal("Anthropic returned mutable registry storage")
	} else if freshMethod := fresh.Methods.APIKey.(auth.EnvAPIKeyAuth); !slices.Equal(freshMethod.EnvVars, fixture.Auth.Env) {
		t.Fatal("Anthropic returned mutable auth-method storage")
	}
	if !slices.ContainsFunc(providers.List(), func(provider providers.Provider) bool { return provider.ID == fixture.ID }) {
		t.Fatalf("registered providers do not contain %q", fixture.ID)
	}
}
