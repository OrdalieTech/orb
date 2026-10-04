package providers_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/providers"
	"github.com/OrdalieTech/orb/conformance/runner"
)

type bedrockProviderFixture struct {
	ID      ai.ProviderID `json:"id"`
	Name    string        `json:"name"`
	BaseURL string        `json:"baseUrl"`
	APIs    []ai.API      `json:"apis"`
	Auth    struct {
		Kind  providers.AuthKind `json:"kind"`
		Name  string             `json:"name"`
		Env   []string           `json:"env"`
		Login []struct {
			Name          string            `json:"name"`
			Responses     []string          `json:"responses"`
			Credential    auth.Credential   `json:"credential"`
			Prompts       []auth.AuthPrompt `json:"prompts"`
			Notifications []auth.AuthEvent  `json:"notifications"`
		} `json:"login"`
		Cases []struct {
			Name          string            `json:"name"`
			Env           map[string]string `json:"env"`
			Authenticated bool              `json:"authenticated"`
			Source        string            `json:"source"`
			APIKey        string            `json:"apiKey"`
		} `json:"cases"`
	} `json:"auth"`
}

func loadBedrockProviderFixture(t *testing.T) bedrockProviderFixture {
	t.Helper()
	var fixture bedrockProviderFixture
	runner.LoadJSON(t, "F2", "bedrock-provider.json", &fixture)
	return fixture
}

type bedrockAuthContext map[string]string

func (authContext bedrockAuthContext) Env(_ context.Context, name string) (string, bool) {
	value, ok := authContext[name]
	return value, ok
}

func (bedrockAuthContext) FileExists(context.Context, string) bool { return false }

func TestAmazonBedrockAuthResolutionMatchesUpstreamFixture(t *testing.T) {
	fixture := loadBedrockProviderFixture(t)
	provider := mustProvider(t, "amazon-bedrock")
	for _, fixtureCase := range fixture.Auth.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			result, err := auth.ResolveProviderAuth(
				context.Background(), string(provider.ID), provider.Methods,
				auth.NewMemoryStore(nil), bedrockAuthContext(fixtureCase.Env), nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if (result != nil) != fixtureCase.Authenticated {
				t.Fatalf("resolution = %#v, authenticated = %t", result, fixtureCase.Authenticated)
			}
			if result == nil {
				return
			}
			if result.Source != fixtureCase.Source {
				t.Fatalf("source = %q, want %q", result.Source, fixtureCase.Source)
			}
			if fixtureCase.APIKey == "" && result.Auth.APIKey != nil {
				t.Fatalf("API key = %q, want none", *result.Auth.APIKey)
			}
		})
	}
}

func TestAmazonBedrockStoredAuthFeedsRequestEnvironment(t *testing.T) {
	provider := mustProvider(t, "amazon-bedrock")
	profileCredential := auth.APIKeyEnvCredential(map[string]string{"AWS_PROFILE": "stored-profile"}, "AWS_PROFILE")
	store := auth.NewMemoryStore(map[string]*auth.Credential{string(provider.ID): profileCredential})
	result, err := auth.ResolveProviderAuth(
		context.Background(), string(provider.ID), provider.Methods, store,
		bedrockAuthContext{"AWS_PROFILE": "ambient-profile"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Auth.APIKey != nil || result.Source != "stored credential" || !reflect.DeepEqual(result.Env, profileCredential.Env) {
		t.Fatalf("stored profile resolution = %#v", result)
	}
	result.Env["AWS_PROFILE"] = "changed"
	if profileCredential.Env["AWS_PROFILE"] != "stored-profile" {
		t.Fatal("resolved profile environment aliases stored credential")
	}

	bearer := auth.APIKeyCredential("stored-bearer")
	bearer.Env = map[string]string{"AWS_REGION": "eu-west-1"}
	store = auth.NewMemoryStore(map[string]*auth.Credential{string(provider.ID): bearer})
	result, err = auth.ResolveProviderAuth(
		context.Background(), string(provider.ID), provider.Methods, store,
		bedrockAuthContext{"AWS_BEARER_TOKEN_BEDROCK": "ambient-bearer"}, nil,
	)
	if err != nil || result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "stored-bearer" || !reflect.DeepEqual(result.Env, bearer.Env) {
		t.Fatalf("stored bearer resolution = %#v, %v", result, err)
	}
}
