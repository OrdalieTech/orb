package providers

import (
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
)

var xAIProvider = Provider{
	ID:      "xai",
	Name:    "xAI",
	API:     ai.APIOpenAICompletions,
	BaseURL: "https://api.x.ai/v1",
	Auth:    AuthAPIKey,
	Env:     []string{"XAI_API_KEY"},
	Methods: auth.ProviderAuth{
		APIKey: auth.EnvAPIKeyAuth{
			DisplayName: "xAI API key",
			EnvVars:     []string{"XAI_API_KEY"},
		},
		OAuth: oauth.NewXAI(nil),
	},
}

func XAI() Provider { return cloneProvider(xAIProvider) }
