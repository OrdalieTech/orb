package providers_test

import (
	"slices"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	aimodels "github.com/OrdalieTech/pi-go/ai/models"
	"github.com/OrdalieTech/pi-go/ai/providers"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type providerFixture struct {
	ID      ai.ProviderID `json:"id"`
	Name    string        `json:"name"`
	BaseURL string        `json:"baseUrl"`
	APIs    []ai.API      `json:"apis"`
	Auth    struct {
		Kind  providers.AuthKind `json:"kind"`
		Env   []string           `json:"env"`
		OAuth bool               `json:"oauth"`
	} `json:"auth"`
}

type providersFixture struct {
	Providers []providerFixture `json:"providers"`
}

func loadProvidersFixture(t *testing.T) providersFixture {
	t.Helper()
	var fixture providersFixture
	runner.LoadJSON(t, "F2", "providers.json", &fixture)
	return fixture
}

func findProviderFixture(t *testing.T, id ai.ProviderID) providerFixture {
	t.Helper()
	fixture := loadProvidersFixture(t)
	for _, provider := range fixture.Providers {
		if provider.ID == id {
			return provider
		}
	}
	t.Fatalf("pinned provider fixture has no %q entry", id)
	return providerFixture{}
}

func TestRegistryMatchesPinnedUpstream(t *testing.T) {
	fixture := loadProvidersFixture(t)
	actual := providers.List()
	if len(actual) != len(fixture.Providers) {
		t.Fatalf("provider count = %d, upstream excluding Radius = %d", len(actual), len(fixture.Providers))
	}
	for index, expected := range fixture.Providers {
		got := actual[index]
		if got.ID != expected.ID || got.Name != expected.Name || got.BaseURL != expected.BaseURL || got.Auth != expected.Auth.Kind || got.OAuth != expected.Auth.OAuth || !slices.Equal(got.APIs, expected.APIs) || !slices.Equal(got.Env, expected.Auth.Env) {
			t.Fatalf("provider %d mismatch\n got: %#v\nwant: %#v", index, got, expected)
		}
		if _, ok := providers.Get(expected.ID); !ok {
			t.Fatalf("provider %q is not addressable", expected.ID)
		}
	}
	if _, ok := providers.Get("radius"); ok {
		t.Fatal("Radius must remain excluded")
	}
}

func TestEveryRegisteredProviderHasBuiltinModels(t *testing.T) {
	catalog, err := aimodels.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[ai.ProviderID]int)
	for _, model := range catalog.Models() {
		counts[model.Provider]++
	}
	for _, provider := range providers.List() {
		if counts[provider.ID] == 0 {
			t.Errorf("registered provider %q has no --list-models entry", provider.ID)
		}
	}
}
