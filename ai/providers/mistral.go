package providers

import (
	"github.com/OrdalieTech/orb/ai/auth"
)

var mistralProvider = Provider{
	ID:   "mistral",
	Name: "Mistral",
	Auth: AuthAPIKey,
	Methods: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{
		DisplayName: "Mistral API key",
		EnvVars:     []string{"MISTRAL_API_KEY"},
	}},
}

func Mistral() Provider { return registered("mistral") }
