package providers_test

import (
	"slices"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/ai/providers"
	"github.com/OrdalieTech/pi-go/conformance/runner"
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
	registered := providers.List()
	wantIDs := []ai.ProviderID{"openai", fixture.ID, "google", "google-vertex", "amazon-bedrock", "mistral", "azure-openai-responses"}
	gotIDs := make([]ai.ProviderID, len(registered))
	for index := range registered {
		gotIDs[index] = registered[index].ID
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("registered provider IDs = %v, want %v", gotIDs, wantIDs)
	}
}
