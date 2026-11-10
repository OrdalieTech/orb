package providers

import (
	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/oauth"
)

var openRouterProvider = Provider{
	ID:   "openrouter",
	Name: "OpenRouter",
	Methods: auth.ProviderAuth{
		APIKey: auth.EnvAPIKeyAuth{
			DisplayName: "OpenRouter API key",
			EnvVars:     []string{"OPENROUTER_API_KEY"},
		},
		OAuth: oauth.NewOpenRouter(nil),
	},
}
