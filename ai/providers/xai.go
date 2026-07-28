package providers

import (
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
)

var xAIProvider = Provider{
	ID:   "xai",
	Name: "xAI",
	Auth: AuthAPIKey,
	Methods: auth.ProviderAuth{
		APIKey: auth.EnvAPIKeyAuth{
			DisplayName: "xAI API key",
			EnvVars:     []string{"XAI_API_KEY"},
		},
		OAuth: oauth.NewXAI(nil),
	},
}

func XAI() Provider { return registered("xai") }
