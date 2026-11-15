package providers

import (
	"github.com/OrdalieTech/orb/ai/auth"
)

var googleProvider = Provider{
	ID:   "google",
	Name: "Google",
	Methods: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{
		DisplayName: "Gemini API key",
		EnvVars:     []string{"GEMINI_API_KEY"},
	}},
}
