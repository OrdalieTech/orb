package providers

import (
	"github.com/OrdalieTech/orb/ai/auth"
)

var azureOpenAIResponsesProvider = Provider{
	ID:   "azure-openai-responses",
	Name: "Azure OpenAI",
	Auth: AuthAPIKey,
	Methods: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{
		DisplayName: "Azure OpenAI API key",
		EnvVars:     []string{"AZURE_OPENAI_API_KEY"},
	}},
}

func AzureOpenAIResponses() Provider { return registered("azure-openai-responses") }
