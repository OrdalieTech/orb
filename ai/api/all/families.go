//go:build !orb_nodefaultproviders

package all

import (
	"github.com/OrdalieTech/orb/ai/api"
	"github.com/OrdalieTech/orb/ai/api/bedrock"
)

// Registry returns a registry with every API family.
func Registry() *api.Registry {
	return api.NewRegistry(
		bedrock.Provider(),
		api.AnthropicMessages(),
		api.GoogleGenerativeAI(),
		api.GoogleVertex(),
		api.MistralConversations(),
		api.AzureOpenAIResponses(),
		api.OpenAICodexResponses(),
		api.OpenAIResponses(),
		api.OpenAICompletions(),
		api.PiMessages(),
	)
}
