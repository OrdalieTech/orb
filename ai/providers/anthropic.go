package providers

import "github.com/OrdalieTech/pi-go/ai"

var anthropicProvider = Provider{
	ID:      "anthropic",
	Name:    "Anthropic",
	API:     ai.APIAnthropicMessages,
	BaseURL: "https://api.anthropic.com",
	Auth:    AuthAPIKey,
	Env:     []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
}

func Anthropic() Provider {
	return cloneProvider(anthropicProvider)
}
