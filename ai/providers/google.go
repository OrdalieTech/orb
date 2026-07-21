package providers

import (
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
)

var googleProvider = Provider{
	ID:      "google",
	Name:    "Google",
	API:     ai.APIGoogleGenerativeAI,
	BaseURL: "https://generativelanguage.googleapis.com/v1beta",
	Auth:    AuthAPIKey,
	Env:     []string{"GEMINI_API_KEY"},
	Methods: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{
		DisplayName: "Gemini API key",
		EnvVars:     []string{"GEMINI_API_KEY"},
	}},
}

func Google() Provider {
	return cloneProvider(googleProvider)
}
