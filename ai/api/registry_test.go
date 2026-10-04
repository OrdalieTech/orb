package api

// testProviders registers every family in this package. Bedrock has no SDK
// backend here (ai/api/bedrock imports this package), so tests that reach its
// transport supply a BedrockBackend of their own.
var testProviders = NewRegistry(
	BedrockConverse(BedrockBackend{}),
	AnthropicMessages(),
	GoogleGenerativeAI(),
	GoogleVertex(),
	MistralConversations(),
	AzureOpenAIResponses(),
	OpenAICodexResponses(),
	OpenAIResponses(),
	OpenAICompletions(),
	PiMessages(),
)
