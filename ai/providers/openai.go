package providers

import (
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/auth"
)

type AuthKind string

const AuthAPIKey AuthKind = "api_key"

type Provider struct {
	ID      ai.ProviderID
	Name    string
	API     ai.API
	BaseURL string
	Auth    AuthKind
	Env     []string
	Methods auth.ProviderAuth
}

var openAI = Provider{
	ID:      "openai",
	Name:    "OpenAI",
	API:     ai.APIOpenAIResponses,
	BaseURL: "https://api.openai.com/v1",
	Auth:    AuthAPIKey,
	Env:     []string{"OPENAI_API_KEY"},
	Methods: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{
		DisplayName: "OpenAI API key",
		EnvVars:     []string{"OPENAI_API_KEY"},
	}},
}

func OpenAI() Provider {
	return cloneProvider(openAI)
}

func Get(id ai.ProviderID) (Provider, bool) {
	switch id {
	case openAI.ID:
		return OpenAI(), true
	case anthropicProvider.ID:
		return Anthropic(), true
	case googleProvider.ID:
		return Google(), true
	case googleVertexProvider.ID:
		return GoogleVertex(), true
	default:
		return Provider{}, false
	}
}

func List() []Provider { return []Provider{OpenAI(), Anthropic(), Google(), GoogleVertex()} }

func cloneProvider(provider Provider) Provider {
	provider.Env = append([]string(nil), provider.Env...)
	if method, ok := provider.Methods.APIKey.(auth.EnvAPIKeyAuth); ok {
		method.EnvVars = append([]string(nil), method.EnvVars...)
		provider.Methods.APIKey = method
	}
	return provider
}
