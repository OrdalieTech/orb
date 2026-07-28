package providers

import (
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
)

var kimiCodingProvider = Provider{
	ID:   "kimi-coding",
	Name: "Kimi For Coding",
	Auth: AuthAPIKey,
	Methods: auth.ProviderAuth{
		APIKey: auth.EnvAPIKeyAuth{
			DisplayName: "Kimi API key",
			EnvVars:     []string{"KIMI_API_KEY"},
		},
		OAuth: oauth.NewKimiCoding(nil),
	},
}

func KimiCoding() Provider { return registered("kimi-coding") }
