package providers_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/providers"
	"github.com/OrdalieTech/orb/conformance/runner"
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

func TestGoogleVertexAuthResolutionMatchesUpstreamFixture(t *testing.T) {
	fixture := loadGoogleVertexProviderFixture(t)
	method := mustProvider(t, "google-vertex").Methods.APIKey
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
