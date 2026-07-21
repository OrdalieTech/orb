package providers

import (
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
)

var azureOpenAIResponsesProvider = Provider{
	ID:   "azure-openai-responses",
	Name: "Azure OpenAI",
	API:  ai.APIAzureOpenAIResponses,
	Auth: AuthAPIKey,
	Env:  []string{"AZURE_OPENAI_API_KEY"},
	Methods: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{
		DisplayName: "Azure OpenAI API key",
		EnvVars:     []string{"AZURE_OPENAI_API_KEY"},
	}},
}

func AzureOpenAIResponses() Provider {
	return cloneProvider(azureOpenAIResponsesProvider)
}
