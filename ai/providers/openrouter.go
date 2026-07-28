package providers

import (
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
)

var openRouterProvider = Provider{
	ID:   "openrouter",
	Name: "OpenRouter",
	Auth: AuthAPIKey,
	Methods: auth.ProviderAuth{
		APIKey: auth.EnvAPIKeyAuth{
			DisplayName: "OpenRouter API key",
			EnvVars:     []string{"OPENROUTER_API_KEY"},
		},
		OAuth: oauth.NewOpenRouter(nil),
	},
}

func OpenRouter() Provider { return registered("openrouter") }
