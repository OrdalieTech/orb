package providers

import (
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
)

var openRouterProvider = Provider{
	ID:      "openrouter",
	Name:    "OpenRouter",
	API:     ai.APIOpenAICompletions,
	BaseURL: "https://openrouter.ai/api/v1",
	Auth:    AuthAPIKey,
	Env:     []string{"OPENROUTER_API_KEY"},
	Methods: auth.ProviderAuth{
		APIKey: auth.EnvAPIKeyAuth{
			DisplayName: "OpenRouter API key",
			EnvVars:     []string{"OPENROUTER_API_KEY"},
		},
		OAuth: oauth.NewOpenRouter(nil),
	},
}

func OpenRouter() Provider { return cloneProvider(openRouterProvider) }
