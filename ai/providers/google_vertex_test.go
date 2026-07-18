package providers_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/ai/providers"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type googleVertexProviderFixture struct {
	ID   ai.ProviderID `json:"id"`
	Name string        `json:"name"`
	APIs []ai.API      `json:"apis"`
	Auth struct {
		Kind  providers.AuthKind `json:"kind"`
		Name  string             `json:"name"`
		Login struct {
			APIKey struct {
				Type auth.CredentialType `json:"type"`
				Key  string              `json:"key"`
			} `json:"apiKey"`
			ADC struct {
				Type auth.CredentialType `json:"type"`
				Env  map[string]string   `json:"env"`
			} `json:"adc"`
			ServiceAccount struct {
				Type auth.CredentialType `json:"type"`
				Env  map[string]string   `json:"env"`
			} `json:"serviceAccount"`
			Notifications []auth.AuthEvent `json:"notifications"`
		} `json:"login"`
		EnvAPIKeys struct {
			Found []string `json:"found"`
		} `json:"envAPIKeys"`
		Resolutions []struct {
			Name        string           `json:"name"`
			Result      *auth.AuthResult `json:"result"`
			EnvLookups  []string         `json:"envLookups"`
			FileLookups []string         `json:"fileLookups"`
		} `json:"resolutions"`
	} `json:"auth"`
}

func loadGoogleVertexProviderFixture(t *testing.T) googleVertexProviderFixture {
	t.Helper()
	var fixture googleVertexProviderFixture
	runner.LoadJSON(t, "F2", "google-vertex-provider.json", &fixture)
	return fixture
}

type vertexAuthContext struct {
	env   map[string]string
	files map[string]bool
}

type recordingVertexAuthContext struct {
	env         map[string]string
	files       map[string]bool
	envLookups  []string
	fileLookups []string
}

func (authContext *recordingVertexAuthContext) Env(_ context.Context, name string) (string, bool) {
	authContext.envLookups = append(authContext.envLookups, name)
	value, ok := authContext.env[name]
	return value, ok
}

func (authContext *recordingVertexAuthContext) FileExists(_ context.Context, path string) bool {
	authContext.fileLookups = append(authContext.fileLookups, path)
	return authContext.files[path]
}

func (authContext vertexAuthContext) Env(_ context.Context, name string) (string, bool) {
	value, ok := authContext.env[name]
	return value, ok
}

func (authContext vertexAuthContext) FileExists(_ context.Context, path string) bool {
	return authContext.files[path]
}

type vertexInteraction struct {
	responses []string
	prompts   []auth.AuthPrompt
	events    []auth.AuthEvent
}

func (interaction *vertexInteraction) Prompt(_ context.Context, prompt auth.AuthPrompt) (string, error) {
	interaction.prompts = append(interaction.prompts, prompt)
	response := interaction.responses[0]
	interaction.responses = interaction.responses[1:]
	return response, nil
}

func (interaction *vertexInteraction) Notify(event auth.AuthEvent) {
	interaction.events = append(interaction.events, event)
}

func TestGoogleVertexProviderMetadata(t *testing.T) {
	fixture := loadGoogleVertexProviderFixture(t)
	provider, ok := providers.Get(fixture.ID)
	if !ok {
		t.Fatal("Google Vertex provider is not registered")
	}
	if len(fixture.APIs) != 1 {
		t.Fatalf("upstream Google Vertex API shapes = %v, want exactly one", fixture.APIs)
	}
	registryFixture := findProviderFixture(t, fixture.ID)
	if provider.ID != fixture.ID || provider.Name != fixture.Name || provider.API != fixture.APIs[0] || provider.BaseURL != registryFixture.BaseURL {
		t.Fatalf("unexpected provider: %#v", provider)
	}
	if provider.Auth != fixture.Auth.Kind || !slices.Equal(provider.Env, registryFixture.Auth.Env) {
		t.Fatalf("unexpected auth metadata: %#v", provider)
	}
	if provider.Methods.APIKey == nil || provider.Methods.APIKey.Name() != fixture.Auth.Name {
		t.Fatalf("unexpected auth methods: %#v", provider.Methods)
	}
	provider.Env[0] = "changed"
	if fresh := providers.GoogleVertex(); !slices.Equal(fresh.Env, registryFixture.Auth.Env) {
		t.Fatal("GoogleVertex returned mutable registry storage")
	}
}

func TestGoogleVertexLoginMethods(t *testing.T) {
	fixture := loadGoogleVertexProviderFixture(t)
	method := providers.GoogleVertex().Methods.APIKey
	login, ok := method.(auth.APIKeyLogin)
	if !ok {
		t.Fatal("Google Vertex auth does not implement login")
	}
	tests := []struct {
		name       string
		responses  []string
		wantKey    *string
		wantEnv    map[string]string
		prompts    []auth.PromptType
		wantEvents []auth.AuthEvent
	}{
		{
			name: "API key", responses: []string{"api-key", fixture.Auth.Login.APIKey.Key}, wantKey: vertexString(fixture.Auth.Login.APIKey.Key),
			prompts: []auth.PromptType{auth.PromptSelect, auth.PromptSecret},
		},
		{
			name: "ADC", responses: []string{"adc", "fixture-project", "us-central1"},
			wantEnv:    fixture.Auth.Login.ADC.Env,
			prompts:    []auth.PromptType{auth.PromptSelect, auth.PromptText, auth.PromptText},
			wantEvents: fixture.Auth.Login.Notifications[:1],
		},
		{
			name: "service account", responses: []string{
				"service-account",
				fixture.Auth.Login.ServiceAccount.Env["GOOGLE_CLOUD_PROJECT"],
				fixture.Auth.Login.ServiceAccount.Env["GOOGLE_CLOUD_LOCATION"],
				fixture.Auth.Login.ServiceAccount.Env["GOOGLE_APPLICATION_CREDENTIALS"],
			},
			wantEnv:    fixture.Auth.Login.ServiceAccount.Env,
			prompts:    []auth.PromptType{auth.PromptSelect, auth.PromptText, auth.PromptText, auth.PromptText},
			wantEvents: fixture.Auth.Login.Notifications[1:],
		},
		{
			name: "service account empty path", responses: []string{"service-account", "fixture-project", "us-central1", ""},
			wantEnv: map[string]string{
				"GOOGLE_CLOUD_PROJECT": "fixture-project", "GOOGLE_CLOUD_LOCATION": "us-central1",
			},
			prompts:    []auth.PromptType{auth.PromptSelect, auth.PromptText, auth.PromptText, auth.PromptText},
			wantEvents: fixture.Auth.Login.Notifications[1:],
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			interaction := &vertexInteraction{responses: append([]string(nil), test.responses...)}
			credential, err := login.Login(context.Background(), interaction)
			if err != nil {
				t.Fatal(err)
			}
			if credential.Type != auth.CredentialAPIKey || !reflect.DeepEqual(credential.Key, test.wantKey) || !reflect.DeepEqual(credential.Env, test.wantEnv) {
				t.Fatalf("credential = %#v", credential)
			}
			gotPrompts := make([]auth.PromptType, 0, len(interaction.prompts))
			for _, prompt := range interaction.prompts {
				gotPrompts = append(gotPrompts, prompt.Type)
			}
			if !slices.Equal(gotPrompts, test.prompts) || !reflect.DeepEqual(interaction.events, test.wantEvents) {
				t.Fatalf("interaction = prompts %v, events %#v", gotPrompts, interaction.events)
			}
		})
	}
}

func TestGoogleVertexAuthResolution(t *testing.T) {
	method := providers.GoogleVertex().Methods.APIKey
	ctx := context.Background()
	storedKey := auth.APIKeyCredential("stored-key")
	result, err := method.Resolve(ctx, vertexAuthContext{env: map[string]string{"GOOGLE_CLOUD_API_KEY": "ambient-key"}}, storedKey)
	if err != nil || result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "stored-key" || result.Source != "stored credential" {
		t.Fatalf("stored key result = %#v, %v", result, err)
	}
	result, err = method.Resolve(ctx, vertexAuthContext{env: map[string]string{"GOOGLE_CLOUD_API_KEY": "ambient-key"}}, nil)
	if err != nil || result == nil || result.Auth.APIKey == nil || *result.Auth.APIKey != "ambient-key" || result.Source != "GOOGLE_CLOUD_API_KEY" {
		t.Fatalf("ambient key result = %#v, %v", result, err)
	}

	credentialEnv := map[string]string{
		"GOOGLE_CLOUD_PROJECT": "stored-project", "GOOGLE_CLOUD_LOCATION": "us-central1",
		"GOOGLE_APPLICATION_CREDENTIALS": "/fixture/adc.json",
	}
	credential := auth.APIKeyEnvCredential(credentialEnv)
	result, err = method.Resolve(ctx, vertexAuthContext{files: map[string]bool{"/fixture/adc.json": true}}, credential)
	if err != nil || result == nil || result.Auth.APIKey != nil || result.Source != "stored credential" || !reflect.DeepEqual(result.Env, credentialEnv) {
		t.Fatalf("stored ADC result = %#v, %v", result, err)
	}
	result.Env["GOOGLE_CLOUD_PROJECT"] = "changed"
	if credential.Env["GOOGLE_CLOUD_PROJECT"] != "stored-project" {
		t.Fatal("resolved ADC environment aliases stored credential")
	}

	ambient := vertexAuthContext{
		env:   map[string]string{"GCLOUD_PROJECT": "ambient-project", "GOOGLE_CLOUD_LOCATION": "global"},
		files: map[string]bool{"~/.config/gcloud/application_default_credentials.json": true},
	}
	result, err = method.Resolve(ctx, ambient, nil)
	if err != nil || result == nil || result.Source != "gcloud application default credentials" || result.Env != nil {
		t.Fatalf("ambient ADC result = %#v, %v", result, err)
	}
	ambient.env["GOOGLE_CLOUD_LOCATION"] = ""
	result, err = method.Resolve(ctx, ambient, nil)
	if err != nil || result != nil {
		t.Fatalf("incomplete ADC result = %#v, %v", result, err)
	}
}

func TestGoogleVertexAuthResolutionMatchesUpstreamFixture(t *testing.T) {
	fixture := loadGoogleVertexProviderFixture(t)
	method := providers.GoogleVertex().Methods.APIKey
	for _, fixtureCase := range fixture.Auth.Resolutions {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			authContext := &recordingVertexAuthContext{env: make(map[string]string), files: make(map[string]bool)}
			var credential *auth.Credential
			switch fixtureCase.Name {
			case "stored-api-key-wins":
				credential = auth.APIKeyCredential("stored-key")
				authContext.env["GOOGLE_CLOUD_API_KEY"] = "environment-key"
			case "environment-api-key":
				authContext.env["GOOGLE_CLOUD_API_KEY"] = "environment-key"
			case "stored-service-account-adc":
				credential = auth.APIKeyEnvCredential(map[string]string{
					"GOOGLE_CLOUD_PROJECT": "fixture-project", "GOOGLE_CLOUD_LOCATION": "us-central1",
					"GOOGLE_APPLICATION_CREDENTIALS": "/fixture/service-account.json",
				})
				authContext.files["/fixture/service-account.json"] = true
			case "ambient-default-adc":
				authContext.env["GOOGLE_CLOUD_PROJECT"] = "fixture-project"
				authContext.env["GOOGLE_CLOUD_LOCATION"] = "us-central1"
				authContext.files["~/.config/gcloud/application_default_credentials.json"] = true
			case "adc-missing-location":
				authContext.env["GOOGLE_CLOUD_PROJECT"] = "fixture-project"
				authContext.files["~/.config/gcloud/application_default_credentials.json"] = true
			case "api-key-wins-over-adc":
				authContext.env["GOOGLE_CLOUD_API_KEY"] = "winning-key"
				authContext.env["GOOGLE_CLOUD_PROJECT"] = "fixture-project"
				authContext.env["GOOGLE_CLOUD_LOCATION"] = "us-central1"
			default:
				t.Fatalf("unhandled upstream resolution case %q", fixtureCase.Name)
			}
			got, err := method.Resolve(context.Background(), authContext, credential)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, fixtureCase.Result) {
				t.Fatalf("resolution = %#v, want %#v", got, fixtureCase.Result)
			}
			if !slices.Equal(authContext.envLookups, fixtureCase.EnvLookups) || !slices.Equal(authContext.fileLookups, fixtureCase.FileLookups) {
				t.Fatalf("lookups = env %v, files %v; want env %v, files %v", authContext.envLookups, authContext.fileLookups, fixtureCase.EnvLookups, fixtureCase.FileLookups)
			}
		})
	}
}

func vertexString(value string) *string { return &value }
