package providers_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/ai/providers"
	"github.com/OrdalieTech/pi-go/conformance/runner"
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

type bedrockInteraction struct {
	responses []string
	prompts   []auth.AuthPrompt
	events    []auth.AuthEvent
}

func (interaction *bedrockInteraction) Prompt(_ context.Context, prompt auth.AuthPrompt) (string, error) {
	interaction.prompts = append(interaction.prompts, prompt)
	response := interaction.responses[0]
	interaction.responses = interaction.responses[1:]
	return response, nil
}

func (interaction *bedrockInteraction) Notify(event auth.AuthEvent) {
	interaction.events = append(interaction.events, event)
}

func TestAmazonBedrockProvider(t *testing.T) {
	fixture := loadBedrockProviderFixture(t)
	if len(fixture.APIs) != 1 {
		t.Fatalf("upstream Bedrock provider API shapes = %v, want exactly one", fixture.APIs)
	}
	provider, ok := providers.Get(fixture.ID)
	if !ok {
		t.Fatalf("%s provider is not registered", fixture.ID)
	}
	if provider.ID != fixture.ID || provider.Name != fixture.Name || provider.API != fixture.APIs[0] || provider.BaseURL != fixture.BaseURL {
		t.Fatalf("unexpected provider: %#v", provider)
	}
	registryFixture := findProviderFixture(t, fixture.ID)
	if provider.Auth != fixture.Auth.Kind || !slices.Equal(provider.Env, registryFixture.Auth.Env) {
		t.Fatalf("unexpected auth metadata: %#v", provider)
	}
	if provider.Methods.APIKey == nil || provider.Methods.APIKey.Name() != fixture.Auth.Name {
		t.Fatalf("unexpected auth methods: %#v", provider.Methods)
	}

	provider.Env[0] = "changed"
	if fresh := providers.AmazonBedrock(); !slices.Equal(fresh.Env, registryFixture.Auth.Env) {
		t.Fatal("AmazonBedrock returned mutable registry storage")
	}
}

func TestAmazonBedrockLoginMatchesUpstream(t *testing.T) {
	fixture := loadBedrockProviderFixture(t)
	method := providers.AmazonBedrock().Methods.APIKey
	login, ok := method.(auth.APIKeyLogin)
	if !ok {
		t.Fatal("Amazon Bedrock auth does not implement login")
	}
	info := auth.AuthEvent{
		Type:    auth.EventInfo,
		Message: "Amazon Bedrock supports AWS profiles, IAM credentials, and role-based credentials.",
		Links: []auth.AuthInfoLink{{
			Label: "AWS credential provider chain",
			URL:   "https://docs.aws.amazon.com/sdkref/latest/guide/standardized-credentials.html",
		}},
	}
	if len(fixture.Auth.Login) != 3 {
		t.Fatalf("upstream login cases = %d, want 3", len(fixture.Auth.Login))
	}
	for _, fixtureCase := range fixture.Auth.Login {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			interaction := &bedrockInteraction{responses: append([]string(nil), fixtureCase.Responses...)}
			credential, err := login.Login(context.Background(), interaction)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(credential)
			if err != nil {
				t.Fatal(err)
			}
			wantEncoded, err := json.Marshal(fixtureCase.Credential)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != string(wantEncoded) {
				t.Fatalf("credential = %s, want %s", encoded, wantEncoded)
			}
			eventsEqual := slices.EqualFunc(interaction.events, fixtureCase.Notifications, func(left, right auth.AuthEvent) bool {
				return reflect.DeepEqual(left, right)
			})
			if !reflect.DeepEqual(interaction.prompts, fixtureCase.Prompts) || !eventsEqual {
				t.Fatalf("interaction = prompts %#v, events %#v", interaction.prompts, interaction.events)
			}
		})
	}

	interaction := &bedrockInteraction{responses: []string{"unknown"}}
	if _, err := login.Login(context.Background(), interaction); err == nil || !strings.Contains(err.Error(), "Unknown Amazon Bedrock auth method: unknown") {
		t.Fatalf("unknown login error = %v", err)
	}
	if !reflect.DeepEqual(interaction.events, []auth.AuthEvent{info}) {
		t.Fatalf("unknown login notifications = %#v", interaction.events)
	}
}

func TestAmazonBedrockAuthResolutionMatchesUpstreamFixture(t *testing.T) {
	fixture := loadBedrockProviderFixture(t)
	provider := providers.AmazonBedrock()
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
	provider := providers.AmazonBedrock()
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
