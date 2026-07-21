package providers

import (
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
)

var anthropicProvider = Provider{
	ID:        "anthropic",
	Name:      "Anthropic",
	API:       ai.APIAnthropicMessages,
	APIs:      []ai.API{ai.APIAnthropicMessages},
	BaseURL:   "https://api.anthropic.com",
	Auth:      AuthAPIKey,
	OAuth:     true,
	Env:       []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
	APIKeyEnv: []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
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
