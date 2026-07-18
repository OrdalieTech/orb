package providers

import (
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/ai/auth/oauth"
)

var anthropicProvider = Provider{
	ID:      "anthropic",
	Name:    "Anthropic",
	API:     ai.APIAnthropicMessages,
	BaseURL: "https://api.anthropic.com",
	Auth:    AuthAPIKey,
	Env:     []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
	Methods: auth.ProviderAuth{
		APIKey: auth.EnvAPIKeyAuth{
			DisplayName: "Anthropic API key",
			EnvVars:     []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
		},
		OAuth: oauth.NewAnthropic(nil),
	},
}

func Anthropic() Provider {
	return cloneProvider(anthropicProvider)
}
