package providers

import (
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
)

var openAICodexProvider = Provider{
	ID:      "openai-codex",
	Name:    "OpenAI Codex",
	Auth:    AuthOAuth,
	Methods: auth.ProviderAuth{OAuth: oauth.NewOpenAICodex(nil)},
}

func OpenAICodex() Provider { return registered("openai-codex") }
