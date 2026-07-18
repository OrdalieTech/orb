package providers

import (
	"context"
	"fmt"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/auth"
)

const googleVertexADCPath = "~/.config/gcloud/application_default_credentials.json"

type googleVertexAuth struct{}

func (googleVertexAuth) Name() string { return "Google Cloud credentials" }

func (googleVertexAuth) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	method, err := interaction.Prompt(ctx, auth.AuthPrompt{
		Type: auth.PromptSelect, Message: "Select Google Vertex AI authentication method:",
		Options: []auth.PromptOption{
			{ID: "api-key", Label: "Google Cloud API key"},
			{ID: "adc", Label: "Application Default Credentials"},
			{ID: "service-account", Label: "Service account credentials file"},
		},
	})
	if err != nil {
		return nil, err
	}
	if method == "api-key" {
		key, err := interaction.Prompt(ctx, auth.AuthPrompt{Type: auth.PromptSecret, Message: "Enter Google Cloud API key"})
		if err != nil {
			return nil, err
		}
		return auth.APIKeyCredential(key), nil
	}
	if method != "adc" && method != "service-account" {
		return nil, fmt.Errorf("Unknown Google Vertex AI auth method: %s", method) //nolint:staticcheck // Exact upstream text.
	}
	message := "Run `gcloud auth application-default login`, then provide the project and location."
	if method == "service-account" {
		message = "Provide a service account credentials file, project, and location."
	}
	interaction.Notify(auth.AuthEvent{
		Type: auth.EventInfo, Message: message,
		Links: []auth.AuthInfoLink{{
			Label: "Application Default Credentials",
			URL:   "https://cloud.google.com/docs/authentication/provide-credentials-adc",
		}},
	})
	project, err := interaction.Prompt(ctx, auth.AuthPrompt{Type: auth.PromptText, Message: "Enter Google Cloud project ID"})
	if err != nil {
		return nil, err
	}
	location, err := interaction.Prompt(ctx, auth.AuthPrompt{Type: auth.PromptText, Message: "Enter Google Cloud location"})
	if err != nil {
		return nil, err
	}
	env := map[string]string{"GOOGLE_CLOUD_PROJECT": project, "GOOGLE_CLOUD_LOCATION": location}
	order := []string{"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION"}
	if method == "service-account" {
		path, err := interaction.Prompt(ctx, auth.AuthPrompt{Type: auth.PromptText, Message: "Enter service account credentials file path"})
		if err != nil {
			return nil, err
		}
		if path != "" {
			env["GOOGLE_APPLICATION_CREDENTIALS"] = path
			order = append(order, "GOOGLE_APPLICATION_CREDENTIALS")
		}
	}
	return auth.APIKeyEnvCredential(env, order...), nil
}

func (googleVertexAuth) Resolve(
	ctx context.Context,
	authContext auth.AuthContext,
	credential *auth.Credential,
) (*auth.AuthResult, error) {
	var key string
	if credential != nil && credential.Key != nil {
		key = *credential.Key
	} else if value, ok := authContext.Env(ctx, "GOOGLE_CLOUD_API_KEY"); ok {
		key = value
	}
	if key != "" {
		return &auth.AuthResult{
			Auth: auth.ModelAuth{APIKey: &key},
			Source: func() string {
				if credential != nil && credential.Key != nil {
					return "stored credential"
				}
				return "GOOGLE_CLOUD_API_KEY"
			}(),
		}, nil
	}
	credentialEnv := map[string]string(nil)
	if credential != nil {
		credentialEnv = credential.Env
	}
	adcPath, adcPathSet := credentialEnvValue(credentialEnv, "GOOGLE_APPLICATION_CREDENTIALS")
	if !adcPathSet {
		adcPath, adcPathSet = authContext.Env(ctx, "GOOGLE_APPLICATION_CREDENTIALS")
	}
	pathToCheck := adcPath
	if !adcPathSet {
		pathToCheck = googleVertexADCPath
	}
	project, projectSet := credentialEnvValue(credentialEnv, "GOOGLE_CLOUD_PROJECT")
	if !projectSet {
		project, projectSet = authContext.Env(ctx, "GOOGLE_CLOUD_PROJECT")
	}
	if !projectSet {
		project, _ = authContext.Env(ctx, "GCLOUD_PROJECT")
	}
	location, locationSet := credentialEnvValue(credentialEnv, "GOOGLE_CLOUD_LOCATION")
	if !locationSet {
		location, _ = authContext.Env(ctx, "GOOGLE_CLOUD_LOCATION")
	}
	if authContext.FileExists(ctx, pathToCheck) && project != "" && location != "" {
		source := "gcloud application default credentials"
		if credential != nil {
			source = "stored credential"
		}
		return &auth.AuthResult{Auth: auth.ModelAuth{}, Env: cloneProviderEnv(credentialEnv), Source: source}, nil
	}
	return nil, nil
}

func credentialEnvValue(env map[string]string, name string) (string, bool) {
	if env == nil {
		return "", false
	}
	value, ok := env[name]
	return value, ok
}

func cloneProviderEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	cloned := make(map[string]string, len(env))
	for name, value := range env {
		cloned[name] = value
	}
	return cloned
}

var googleVertexProvider = Provider{
	ID:   "google-vertex",
	Name: "Google Vertex AI",
	API:  ai.APIGoogleVertex,
	APIs: []ai.API{ai.APIGoogleVertex},
	Auth: AuthAPIKey,
	Env: []string{
		"GOOGLE_CLOUD_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION",
	},
	APIKeyEnv: []string{"GOOGLE_CLOUD_API_KEY"},
	Methods:   auth.ProviderAuth{APIKey: googleVertexAuth{}},
}

func GoogleVertex() Provider { return cloneProvider(googleVertexProvider) }
