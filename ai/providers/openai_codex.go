package providers

import (
	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/oauth"
)

var openAICodexProvider = Provider{
	ID: "openai-codex",
	// Sign in with ChatGPT on the openai provider supersedes this one.
	Name:    "OpenAI Codex (legacy)",
	Auth:    AuthOAuth,
	Methods: auth.ProviderAuth{OAuth: oauth.NewOpenAICodex(nil)},
}

func OpenAICodex() Provider { return registered("openai-codex") }
