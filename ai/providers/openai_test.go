package providers_test

import (
	"slices"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type openAIProviderFixture struct {
	ID      ai.ProviderID `json:"id"`
	Name    string        `json:"name"`
	BaseURL string        `json:"baseUrl"`
	APIs    []ai.API      `json:"apis"`
	Auth    struct {
		Kind     providers.AuthKind `json:"kind"`
		Name     string             `json:"name"`
		Env      []string           `json:"env"`
		Resolved struct {
			APIKey string `json:"apiKey"`
		} `json:"resolved"`
		Source string `json:"source"`
	} `json:"auth"`
}

func loadOpenAIProviderFixture(t *testing.T) openAIProviderFixture {
	t.Helper()
	var fixture openAIProviderFixture
	runner.LoadJSON(t, "F2", "provider.json", &fixture)
	if len(fixture.APIs) != 1 {
		t.Fatalf("upstream OpenAI provider API shapes = %v, want exactly one", fixture.APIs)
	}
	if len(fixture.Auth.Env) == 0 || fixture.Auth.Resolved.APIKey == "" {
		t.Fatalf("upstream OpenAI provider auth fixture is incomplete: %#v", fixture.Auth)
	}
	if fixture.Auth.Source != fixture.Auth.Env[0] {
		t.Fatalf("upstream auth source = %q, first resolver environment lookup = %q", fixture.Auth.Source, fixture.Auth.Env[0])
	}
	return fixture
}

func TestOpenAIProvider(t *testing.T) {
	fixture := loadOpenAIProviderFixture(t)
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
}

func TestProviderValuesAreCopies(t *testing.T) {
	fixture := loadOpenAIProviderFixture(t)
	provider := providers.OpenAI()
	provider.Env[0] = "changed"
	if fresh := providers.OpenAI(); !slices.Equal(fresh.Env, fixture.Auth.Env) {
		t.Fatal("OpenAI returned mutable registry storage")
	}
	if _, ok := providers.Get(ai.ProviderID("missing")); ok {
		t.Fatal("unknown provider was found")
	}
}
