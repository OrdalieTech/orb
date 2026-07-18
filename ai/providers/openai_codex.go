package providers

import (
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/ai/auth/oauth"
)

var openAICodexProvider = Provider{
	ID:      "openai-codex",
	Name:    "OpenAI Codex",
	API:     ai.APIOpenAICodexResponses,
	BaseURL: "https://chatgpt.com/backend-api",
	Auth:    AuthOAuth,
	Methods: auth.ProviderAuth{OAuth: oauth.NewOpenAICodex(nil)},
}

func OpenAICodex() Provider { return cloneProvider(openAICodexProvider) }
