package providers

import (
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
)

var mistralProvider = Provider{
	ID:      "mistral",
	Name:    "Mistral",
	API:     ai.APIMistralConversations,
	BaseURL: "https://api.mistral.ai",
	Auth:    AuthAPIKey,
	Env:     []string{"MISTRAL_API_KEY"},
	Methods: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{
		DisplayName: "Mistral API key",
		EnvVars:     []string{"MISTRAL_API_KEY"},
	}},
}

func Mistral() Provider {
	return cloneProvider(mistralProvider)
}
