package providers

import (
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
)

type AuthKind string

const AuthAPIKey AuthKind = "api_key"

const AuthOAuth AuthKind = "oauth"

type Provider struct {
	ID      ai.ProviderID
	Name    string
	API     ai.API
	APIs    []ai.API
	BaseURL string
	Auth    AuthKind
	OAuth   bool
	Env     []string
	// APIKeyEnv is the subset of Env whose value is request authentication.
	// The remaining names configure ambient credentials or provider endpoints.
	APIKeyEnv []string
	Methods   auth.ProviderAuth
}

var openAI = Provider{
	ID:        "openai",
	Name:      "OpenAI",
	API:       ai.APIOpenAIResponses,
	APIs:      []ai.API{ai.APIOpenAIResponses},
	BaseURL:   "https://api.openai.com/v1",
	Auth:      AuthAPIKey,
	Env:       []string{"OPENAI_API_KEY"},
	APIKeyEnv: []string{"OPENAI_API_KEY"},
	Methods: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{
		DisplayName: "OpenAI API key",
		EnvVars:     []string{"OPENAI_API_KEY"},
	}},
}

func OpenAI() Provider {
	return cloneProvider(openAI)
}

func cloneProvider(provider Provider) Provider {
	provider.Env = append([]string(nil), provider.Env...)
	if method, ok := provider.Methods.APIKey.(auth.EnvAPIKeyAuth); ok {
		method.EnvVars = append([]string(nil), method.EnvVars...)
		provider.Methods.APIKey = method
	}
	provider.APIKeyEnv = append([]string(nil), provider.APIKeyEnv...)
	provider.APIs = append([]ai.API(nil), provider.APIs...)
	return provider
}
