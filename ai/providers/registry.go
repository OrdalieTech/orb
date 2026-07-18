package providers

import (
	"slices"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/auth"
)

// The order matches builtinProviders() at the pinned upstream commit. Radius
// is intentionally absent under the divergence ledger.
var registry = []Provider{
	withProviderMetadata(amazonBedrockProvider, "", []string{
		"AWS_BEARER_TOKEN_BEDROCK", "AWS_PROFILE", "AWS_ACCESS_KEY_ID",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
		"AWS_WEB_IDENTITY_TOKEN_FILE",
	}, []string{"AWS_BEARER_TOKEN_BEDROCK"}, ai.APIBedrockConverse),
	envProvider("ant-ling", "Ant Ling", "https://api.ant-ling.com/v1", "Ant Ling API key", []string{"ANT_LING_API_KEY"}, ai.APIOpenAICompletions),
	withProviderMetadata(anthropicProvider, "https://api.anthropic.com", []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}, []string{"ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}, ai.APIAnthropicMessages),
	withProviderMetadata(azureOpenAIResponsesProvider, "", []string{"AZURE_OPENAI_API_KEY"}, []string{"AZURE_OPENAI_API_KEY"}, ai.APIAzureOpenAIResponses),
	envProvider("cerebras", "Cerebras", "https://api.cerebras.ai/v1", "Cerebras API key", []string{"CEREBRAS_API_KEY"}, ai.APIOpenAICompletions),
	withProviderMetadata(cloudflareAIGatewayProvider, "", []string{"CLOUDFLARE_API_KEY", "CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_GATEWAY_ID"}, []string{"CLOUDFLARE_API_KEY"}, ai.APIAnthropicMessages, ai.APIOpenAICompletions, ai.APIOpenAIResponses),
	withProviderMetadata(cloudflareWorkersAIProvider, "", []string{"CLOUDFLARE_API_KEY", "CLOUDFLARE_ACCOUNT_ID"}, []string{"CLOUDFLARE_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("deepseek", "DeepSeek", "https://api.deepseek.com", "DeepSeek API key", []string{"DEEPSEEK_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("fireworks", "Fireworks", "https://api.fireworks.ai/inference", "Fireworks API key", []string{"FIREWORKS_API_KEY"}, ai.APIAnthropicMessages, ai.APIOpenAICompletions),
	withProviderMetadata(githubCopilotProvider, "https://api.individual.githubcopilot.com", []string{"COPILOT_GITHUB_TOKEN"}, []string{"COPILOT_GITHUB_TOKEN"}, ai.APIAnthropicMessages, ai.APIOpenAICompletions, ai.APIOpenAIResponses),
	withProviderMetadata(googleProvider, "https://generativelanguage.googleapis.com/v1beta", []string{"GEMINI_API_KEY"}, []string{"GEMINI_API_KEY"}, ai.APIGoogleGenerativeAI),
	withProviderMetadata(googleVertexProvider, "", []string{"GOOGLE_CLOUD_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION"}, []string{"GOOGLE_CLOUD_API_KEY"}, ai.APIGoogleVertex),
	envProvider("groq", "Groq", "https://api.groq.com/openai/v1", "Groq API key", []string{"GROQ_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("huggingface", "Hugging Face", "https://router.huggingface.co/v1", "Hugging Face token", []string{"HF_TOKEN"}, ai.APIOpenAICompletions),
	envProvider("kimi-coding", "Kimi For Coding", "https://api.kimi.com/coding", "Kimi API key", []string{"KIMI_API_KEY"}, ai.APIAnthropicMessages),
	envProvider("minimax", "MiniMax", "https://api.minimax.io/anthropic", "MiniMax API key", []string{"MINIMAX_API_KEY"}, ai.APIAnthropicMessages),
	envProvider("minimax-cn", "MiniMax CN", "https://api.minimaxi.com/anthropic", "MiniMax CN API key", []string{"MINIMAX_CN_API_KEY"}, ai.APIAnthropicMessages),
	withProviderMetadata(mistralProvider, "https://api.mistral.ai", []string{"MISTRAL_API_KEY"}, []string{"MISTRAL_API_KEY"}, ai.APIMistralConversations),
	envProvider("moonshotai", "Moonshot AI", "https://api.moonshot.ai/v1", "Moonshot AI API key", []string{"MOONSHOT_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("moonshotai-cn", "Moonshot AI CN", "https://api.moonshot.cn/v1", "Moonshot AI API key", []string{"MOONSHOT_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("nvidia", "NVIDIA", "https://integrate.api.nvidia.com/v1", "NVIDIA API key", []string{"NVIDIA_API_KEY"}, ai.APIOpenAICompletions),
	withProviderMetadata(openAI, "https://api.openai.com/v1", []string{"OPENAI_API_KEY"}, []string{"OPENAI_API_KEY"}, ai.APIOpenAIResponses),
	withProviderMetadata(openAICodexProvider, "https://chatgpt.com/backend-api", nil, nil, ai.APIOpenAICodexResponses),
	envProvider("opencode", "OpenCode Zen", "", "OpenCode API key", []string{"OPENCODE_API_KEY"}, ai.APIAnthropicMessages, ai.APIGoogleGenerativeAI, ai.APIOpenAICompletions, ai.APIOpenAIResponses),
	envProvider("opencode-go", "OpenCode Zen Go", "", "OpenCode API key", []string{"OPENCODE_API_KEY"}, ai.APIAnthropicMessages, ai.APIOpenAICompletions),
	envProvider("openrouter", "OpenRouter", "https://openrouter.ai/api/v1", "OpenRouter API key", []string{"OPENROUTER_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("together", "Together", "https://api.together.ai/v1", "Together API key", []string{"TOGETHER_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("vercel-ai-gateway", "Vercel AI Gateway", "https://ai-gateway.vercel.sh", "Vercel AI Gateway API key", []string{"AI_GATEWAY_API_KEY"}, ai.APIAnthropicMessages),
	withProviderMetadata(xAIProvider, "https://api.x.ai/v1", []string{"XAI_API_KEY"}, []string{"XAI_API_KEY"}, ai.APIOpenAICompletions, ai.APIOpenAIResponses),
	envProvider("xiaomi", "Xiaomi", "https://api.xiaomimimo.com/v1", "Xiaomi API key", []string{"XIAOMI_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("xiaomi-token-plan-ams", "Xiaomi Token Plan AMS", "https://token-plan-ams.xiaomimimo.com/v1", "Xiaomi Token Plan AMS API key", []string{"XIAOMI_TOKEN_PLAN_AMS_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("xiaomi-token-plan-cn", "Xiaomi Token Plan CN", "https://token-plan-cn.xiaomimimo.com/v1", "Xiaomi Token Plan CN API key", []string{"XIAOMI_TOKEN_PLAN_CN_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("xiaomi-token-plan-sgp", "Xiaomi Token Plan SGP", "https://token-plan-sgp.xiaomimimo.com/v1", "Xiaomi Token Plan SGP API key", []string{"XIAOMI_TOKEN_PLAN_SGP_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("zai", "Z.AI", "https://api.z.ai/api/coding/paas/v4", "Z.AI API key", []string{"ZAI_API_KEY"}, ai.APIOpenAICompletions),
	envProvider("zai-coding-cn", "Z.AI Coding CN", "https://open.bigmodel.cn/api/coding/paas/v4", "Z.AI Coding CN API key", []string{"ZAI_CODING_CN_API_KEY"}, ai.APIOpenAICompletions),
}

func envProvider(id ai.ProviderID, name, baseURL, authName string, env []string, apis ...ai.API) Provider {
	return withProviderMetadata(Provider{
		ID: id, Name: name, Auth: AuthAPIKey,
		Methods: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{DisplayName: authName, EnvVars: env}},
	}, baseURL, env, env, apis...)
}

func withProviderMetadata(value Provider, baseURL string, env, apiKeyEnv []string, apis ...ai.API) Provider {
	value.BaseURL = baseURL
	value.Env = env
	value.APIKeyEnv = apiKeyEnv
	value.APIs = apis
	value.OAuth = value.Methods.OAuth != nil
	if len(apis) != 0 {
		value.API = apis[0]
	}
	return value
}

func Get(id ai.ProviderID) (Provider, bool) {
	index := slices.IndexFunc(registry, func(provider Provider) bool { return provider.ID == id })
	if index < 0 {
		return Provider{}, false
	}
	return cloneProvider(registry[index]), true
}

func List() []Provider {
	result := make([]Provider, len(registry))
	for index := range registry {
		result[index] = cloneProvider(registry[index])
	}
	return result
}
