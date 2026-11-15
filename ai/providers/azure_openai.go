package providers

import (
	"github.com/OrdalieTech/orb/ai/auth"
)

var azureProvider = Provider{
	ID:   "azure",
	Name: "Azure",
	Methods: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{
		DisplayName: "Azure OpenAI API key",
		EnvVars:     []string{"AZURE_OPENAI_API_KEY"},
	}},
}
