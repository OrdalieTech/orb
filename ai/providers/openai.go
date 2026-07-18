package providers

import "github.com/OrdalieTech/pi-go/ai"

type AuthKind string

const AuthAPIKey AuthKind = "api_key"

type Provider struct {
	ID      ai.ProviderID
	Name    string
	API     ai.API
	BaseURL string
	Auth    AuthKind
	Env     []string
}

var openAI = Provider{
	ID:      "openai",
	Name:    "OpenAI",
	API:     ai.APIOpenAIResponses,
	BaseURL: "https://api.openai.com/v1",
	Auth:    AuthAPIKey,
	Env:     []string{"OPENAI_API_KEY"},
}

func OpenAI() Provider {
	return cloneProvider(openAI)
}

func Get(id ai.ProviderID) (Provider, bool) {
	switch id {
	case openAI.ID:
		return OpenAI(), true
	case anthropicProvider.ID:
		return Anthropic(), true
	default:
		return Provider{}, false
	}
}

func List() []Provider {
	return []Provider{OpenAI(), Anthropic()}
}

func cloneProvider(provider Provider) Provider {
	provider.Env = append([]string(nil), provider.Env...)
	return provider
}
