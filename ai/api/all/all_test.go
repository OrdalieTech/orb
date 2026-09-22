//go:build !orb_nodefaultproviders

package all

import (
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestRegistryHasEveryAPIFamily(t *testing.T) {
	registry := Registry()
	for _, api := range []ai.API{
		ai.APIBedrockConverse, ai.APIAnthropicMessages, ai.APIGoogleGenerativeAI, ai.APIGoogleVertex,
		ai.APIMistralConversations, ai.APIAzureOpenAIResponses, ai.APIOpenAICodexResponses,
		ai.APIOpenAIResponses, ai.APIOpenAICompletions, ai.APIPiMessages,
	} {
		if !registry.Has(api) {
			t.Errorf("default registry lacks %s", api)
		}
	}
}
