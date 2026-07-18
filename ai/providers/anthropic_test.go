package providers_test

import (
	"slices"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type anthropicProviderFixture struct {
	ID      ai.ProviderID `json:"id"`
	Name    string        `json:"name"`
	BaseURL string        `json:"baseUrl"`
	APIs    []ai.API      `json:"apis"`
	Auth    struct {
		Kind providers.AuthKind `json:"kind"`
		Name string             `json:"name"`
		Env  []string           `json:"env"`
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

	provider.Env[0] = "changed"
	if fresh := providers.Anthropic(); !slices.Equal(fresh.Env, fixture.Auth.Env) {
		t.Fatal("Anthropic returned mutable registry storage")
	}
	registered := providers.List()
	if len(registered) != 2 || registered[1].ID != fixture.ID {
		t.Fatalf("registered providers = %#v, want OpenAI then Anthropic", registered)
	}
}
