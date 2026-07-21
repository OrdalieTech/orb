package cataloggen

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/OrdalieTech/pigo/ai"
)

var (
	gemini3ProPattern   = regexp.MustCompile(`gemini-3(?:\.[0-9]+)?-pro`)
	gemini3FlashPattern = regexp.MustCompile(`gemini-3(?:\.[0-9]+)?-flash`)
)

func applyCatalogMetadata(model *ai.Model) {
	if model.Name == "" {
		model.Name = model.ID
	}
	applyCorrections(model)
	applyProviderHeaders(model)
	applyThinkingLevelMetadata(model)
	switch model.API {
	case ai.APIOpenAICompletions:
		applyOpenAICompletionsCompat(model)
	case ai.APIAnthropicMessages:
		applyAnthropicCompat(model)
	case ai.APIOpenAIResponses, ai.APIOpenAICodexResponses:
		applyOpenAIResponsesCompat(model)
	}
}

func applyCorrections(model *ai.Model) {
	provider, id := string(model.Provider), model.ID
	if provider == "amazon-bedrock" && strings.HasPrefix(id, "eu.") {
		model.BaseURL = "https://bedrock-runtime.eu-central-1.amazonaws.com"
	}
	if provider == "github-copilot" && slices.Contains([]string{
		"claude-fable-5", "claude-opus-4.6", "claude-opus-4.7", "claude-opus-4.8",
		"claude-sonnet-4.6", "claude-sonnet-5", "gpt-5.3-codex", "gpt-5.4", "gpt-5.5",
	}, id) {
		model.ContextWindow = 1000000
	}
	if slices.Contains([]string{"anthropic", "opencode", "opencode-go"}, provider) &&
		slices.Contains([]string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-opus-4.6", "claude-sonnet-4.6"}, id) {
		model.ContextWindow = 1000000
	}
	if (provider == "opencode" || provider == "opencode-go") && (id == "claude-sonnet-4-5" || id == "claude-sonnet-4") {
		model.ContextWindow = 200000
	}
	if (provider == "opencode" || provider == "opencode-go") && id == "gpt-5.4" {
		model.ContextWindow, model.MaxTokens = 272000, 128000
	}
	if provider == "openai" && slices.Contains([]string{"gpt-5.4", "gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}, id) {
		model.ContextWindow, model.MaxTokens = 272000, 128000
	}
	if (provider == "openai" || provider == "openai-codex") && slices.Contains([]string{
		"gpt-5.4", "gpt-5.4-pro", "gpt-5.5", "gpt-5.5-pro", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
	}, id) {
		rates := model.Cost.ModelCostRates
		tiers := []ai.ModelCostTier{{
			InputTokensAbove: 272000,
			ModelCostRates: ai.ModelCostRates{
				Input: rates.Input * 2, Output: rates.Output * 1.5,
				CacheRead: rates.CacheRead * 2, CacheWrite: rates.CacheWrite * 2,
			},
		}}
		model.Cost.Tiers = &tiers
	}
	if (provider == "openai" || provider == "azure-openai-responses") && id == "gpt-5-pro" {
		model.MaxTokens = 128000
	}
	if provider == "openrouter" && (id == "moonshotai/kimi-k3" || id == "~moonshotai/kimi-latest") ||
		provider == "vercel-ai-gateway" && id == "moonshotai/kimi-k3" {
		model.MaxTokens = 131072
	}
	if provider == "openrouter" && id == "moonshotai/kimi-k2.5" {
		model.Cost.Input, model.Cost.Output, model.Cost.CacheRead, model.MaxTokens = 0.41, 2.06, 0.07, 4096
	}
	if provider == "openrouter" && id == "z-ai/glm-5" {
		model.Cost.Input, model.Cost.Output, model.Cost.CacheRead = 0.6, 1.9, 0.119
	}
	if provider == "kimi-coding" {
		if id == "k3" {
			model.Reasoning, model.MaxTokens = true, 131072
			applyImpliedCost(model, ai.ModelCostRates{Input: 3, Output: 15, CacheRead: .3})
		} else {
			costs := map[string]ai.ModelCostRates{
				"kimi-for-coding":           {Input: .95, Output: 4, CacheRead: .19},
				"kimi-for-coding-highspeed": {Input: 1.9, Output: 8, CacheRead: .38},
				"kimi-k2-thinking":          {Input: .6, Output: 2.5, CacheRead: .15},
			}
			if cost, ok := costs[id]; ok {
				applyImpliedCost(model, cost)
			}
		}
	}
}

func applyImpliedCost(model *ai.Model, fallback ai.ModelCostRates) {
	if model.Cost.Input == 0 {
		model.Cost.Input = fallback.Input
	}
	if model.Cost.Output == 0 {
		model.Cost.Output = fallback.Output
	}
	if model.Cost.CacheRead == 0 {
		model.Cost.CacheRead = fallback.CacheRead
	}
	if model.Cost.CacheWrite == 0 {
		model.Cost.CacheWrite = fallback.CacheWrite
	}
}

func applyProviderHeaders(model *ai.Model) {
	var headers map[string]string
	if model.Headers != nil {
		headers = *model.Headers
	}
	if headers == nil {
		headers = make(map[string]string)
	}
	switch model.Provider {
	case "nvidia":
		headers["NVCF-POLL-SECONDS"] = "3600"
	case "kimi-coding":
		headers["User-Agent"] = "KimiCLI/1.5"
	}
	if len(headers) != 0 {
		model.Headers = &headers
	}
}

func applyThinkingLevelMetadata(model *ai.Model) {
	id, provider := model.ID, string(model.Provider)
	if provider == "together" && model.Reasoning {
		values := map[ai.ModelThinkingLevel]*string{}
		switch {
		case slices.Contains([]string{"openai/gpt-oss-20b", "openai/gpt-oss-120b"}, id):
			values[ai.ModelThinkingOff], values[ai.ModelThinkingMinimal] = nil, nil
		case id == "deepseek-ai/DeepSeek-V4-Pro":
			values[ai.ModelThinkingMinimal], values[ai.ModelThinkingLow], values[ai.ModelThinkingMedium] = nil, nil, nil
			values[ai.ModelThinkingHigh], values[ai.ModelThinkingXHigh] = ptr("high"), nil
		case slices.Contains([]string{"deepseek-ai/DeepSeek-R1", "MiniMaxAI/MiniMax-M2.7"}, id):
			values[ai.ModelThinkingOff], values[ai.ModelThinkingMinimal], values[ai.ModelThinkingLow], values[ai.ModelThinkingMedium] = nil, nil, nil, nil
		default:
			values[ai.ModelThinkingMinimal], values[ai.ModelThinkingLow], values[ai.ModelThinkingMedium] = nil, nil, nil
		}
		mergeThinking(model, values)
	}
	if (provider == "zai" || provider == "zai-coding-cn") && id == "glm-5.2" {
		values := thinkingValues(map[ai.ModelThinkingLevel]string{
			ai.ModelThinkingLow: "high", ai.ModelThinkingMedium: "high", ai.ModelThinkingHigh: "high", ai.ModelThinkingMax: "max",
		})
		values[ai.ModelThinkingMinimal] = nil
		mergeThinking(model, values)
	}
	if (model.API == ai.APIOpenAIResponses || model.API == ai.APIAzureOpenAIResponses) && strings.HasPrefix(id, "gpt-5") {
		mergeThinking(model, map[ai.ModelThinkingLevel]*string{ai.ModelThinkingOff: nil})
	}
	if provider == "github-copilot" && strings.HasPrefix(id, "gpt-5") {
		mergeThinking(model, thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingMinimal: "low"}))
	}
	if provider == "openai" && model.API == ai.APIOpenAIResponses && slices.Contains([]string{
		"gpt-5.1", "gpt-5.2", "gpt-5.3-codex", "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
	}, id) {
		mergeThinking(model, thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingOff: "none"}))
	}
	if provider == "xai" && model.API == ai.APIOpenAIResponses && id == "grok-4.5" {
		mergeThinking(model, map[ai.ModelThinkingLevel]*string{ai.ModelThinkingOff: nil, ai.ModelThinkingMinimal: nil})
	}
	if supportsOpenAIXHigh(id) {
		mergeThinking(model, thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingXHigh: "xhigh"}))
	}
	if strings.Contains(id, "gpt-5.6") && slices.Contains([]ai.API{ai.APIOpenAIResponses, ai.APIAzureOpenAIResponses, ai.APIOpenAICodexResponses, ai.APIOpenAICompletions}, model.API) {
		mergeThinking(model, thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingMax: "max"}))
	}
	if provider == "openai" && id == "gpt-5.5" {
		mergeThinking(model, map[ai.ModelThinkingLevel]*string{ai.ModelThinkingMinimal: nil})
	}
	if strings.HasSuffix(id, "gpt-5.5-pro") {
		mergeThinking(model, map[ai.ModelThinkingLevel]*string{ai.ModelThinkingOff: nil, ai.ModelThinkingMinimal: nil, ai.ModelThinkingLow: nil})
	}
	if containsAny(id, "opus-4-6", "opus-4.6", "sonnet-4-6", "sonnet-4.6") {
		mergeThinking(model, thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingMax: "max"}))
	}
	if containsAny(id, "opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8", "sonnet-5", "sonnet.5") {
		mergeThinking(model, thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingXHigh: "xhigh", ai.ModelThinkingMax: "max"}))
	}
	if strings.Contains(id, "fable-5") {
		values := thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingXHigh: "xhigh", ai.ModelThinkingMax: "max"})
		values[ai.ModelThinkingOff] = nil
		mergeThinking(model, values)
	}
	if model.API == ai.APIOpenAICompletions && strings.Contains(id, "deepseek-v4") {
		values := thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingHigh: "high", ai.ModelThinkingMax: "max"})
		values[ai.ModelThinkingMinimal], values[ai.ModelThinkingLow], values[ai.ModelThinkingMedium] = nil, nil, nil
		if provider == "openrouter" {
			values[ai.ModelThinkingXHigh], values[ai.ModelThinkingMax] = ptr("xhigh"), nil
		}
		mergeThinking(model, values)
	}
	if model.API == ai.APIGoogleGenerativeAI || model.API == ai.APIGoogleVertex {
		lower := strings.ToLower(id)
		switch {
		case gemini3ProPattern.MatchString(lower):
			values := thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingLow: "LOW", ai.ModelThinkingHigh: "HIGH"})
			values[ai.ModelThinkingOff], values[ai.ModelThinkingMinimal], values[ai.ModelThinkingMedium] = nil, nil, nil
			mergeThinking(model, values)
		case gemini3FlashPattern.MatchString(lower) || lower == "gemini-flash-latest" || lower == "gemini-flash-lite-latest":
			mergeThinking(model, map[ai.ModelThinkingLevel]*string{ai.ModelThinkingOff: nil})
		case strings.Contains(lower, "gemma-4") || strings.Contains(lower, "gemma4"):
			values := thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingMinimal: "MINIMAL", ai.ModelThinkingHigh: "HIGH"})
			values[ai.ModelThinkingOff], values[ai.ModelThinkingLow], values[ai.ModelThinkingMedium] = nil, nil, nil
			mergeThinking(model, values)
		}
	}
	if provider == "groq" && id == "qwen/qwen3-32b" {
		values := thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingHigh: "default"})
		values[ai.ModelThinkingMinimal], values[ai.ModelThinkingLow], values[ai.ModelThinkingMedium] = nil, nil, nil
		mergeThinking(model, values)
	}
	if provider == "openai-codex" && supportsOpenAIXHigh(id) {
		mergeThinking(model, thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingMinimal: "low"}))
	}
	if (provider == "moonshotai" || provider == "moonshotai-cn") && (id == "kimi-k2.7-code" || id == "kimi-k2.7-code-highspeed") {
		mergeThinking(model, map[ai.ModelThinkingLevel]*string{ai.ModelThinkingOff: nil})
	}
	if provider == "openrouter" && strings.HasPrefix(id, "inception/mercury-2") {
		mergeThinking(model, map[ai.ModelThinkingLevel]*string{ai.ModelThinkingOff: nil})
	}
	if provider == "openrouter" && id == "z-ai/glm-5.2" {
		mergeThinking(model, thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingXHigh: "xhigh"}))
	}
	if provider == "fireworks" && strings.Contains(id, "glm-5p2") {
		values := thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingOff: "none", ai.ModelThinkingLow: "high", ai.ModelThinkingMedium: "high", ai.ModelThinkingMax: "max"})
		values[ai.ModelThinkingMinimal] = nil
		mergeThinking(model, values)
	}
	if provider == "opencode-go" && id == "glm-5.2" {
		values := thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingHigh: "high", ai.ModelThinkingMax: "max"})
		values[ai.ModelThinkingOff], values[ai.ModelThinkingMinimal], values[ai.ModelThinkingLow], values[ai.ModelThinkingMedium] = nil, nil, nil, nil
		mergeThinking(model, values)
	}
	if provider == "opencode-go" && id == "kimi-k2.6" {
		mergeThinking(model, map[ai.ModelThinkingLevel]*string{ai.ModelThinkingMinimal: nil, ai.ModelThinkingLow: nil, ai.ModelThinkingMedium: nil})
	}
	if provider == "opencode" && id == "grok-build-0.1" {
		mergeThinking(model, map[ai.ModelThinkingLevel]*string{ai.ModelThinkingOff: nil, ai.ModelThinkingMinimal: nil, ai.ModelThinkingLow: nil, ai.ModelThinkingMedium: nil})
	}
	if provider == "ant-ling" && model.Reasoning {
		values := thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingHigh: "high", ai.ModelThinkingXHigh: "xhigh"})
		values[ai.ModelThinkingOff], values[ai.ModelThinkingMinimal], values[ai.ModelThinkingLow], values[ai.ModelThinkingMedium] = nil, nil, nil, nil
		mergeThinking(model, values)
	}
	if provider == "github-copilot" {
		switch id {
		case "claude-opus-4.7", "claude-opus-4.8":
			mergeThinking(model, thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingMinimal: "low"}))
		case "claude-sonnet-4.6":
			mergeThinking(model, thinkingValues(map[ai.ModelThinkingLevel]string{ai.ModelThinkingMinimal: "low", ai.ModelThinkingMax: "max"}))
		}
	}
	if provider == "kimi-coding" && id == "k3" || (provider == "moonshotai" || provider == "moonshotai-cn") && id == "kimi-k3" {
		values := map[ai.ModelThinkingLevel]*string{
			ai.ModelThinkingOff: nil, ai.ModelThinkingMinimal: nil, ai.ModelThinkingLow: ptr("low"),
			ai.ModelThinkingMedium: nil, ai.ModelThinkingHigh: ptr("high"), ai.ModelThinkingXHigh: nil,
			ai.ModelThinkingMax: ptr("max"),
		}
		mergeThinking(model, values)
	}
}

func applyOpenAICompletionsCompat(model *ai.Model) {
	provider, baseURL := string(model.Provider), model.BaseURL
	isZAI := provider == "zai" || provider == "zai-coding-cn" || strings.Contains(baseURL, "api.z.ai") || strings.Contains(baseURL, "open.bigmodel.cn")
	isTogether := provider == "together" || strings.Contains(baseURL, "api.together.ai") || strings.Contains(baseURL, "api.together.xyz")
	isMoonshot := provider == "moonshotai" || provider == "moonshotai-cn" || strings.Contains(baseURL, "api.moonshot.")
	isOpenRouter := provider == "openrouter" || strings.Contains(baseURL, "openrouter.ai")
	isWorkers := provider == "cloudflare-workers-ai" || strings.Contains(baseURL, "api.cloudflare.com")
	isGateway := provider == "cloudflare-ai-gateway" || strings.Contains(baseURL, "gateway.ai.cloudflare.com")
	isNVIDIA := provider == "nvidia" || strings.Contains(baseURL, "integrate.api.nvidia.com")
	isAntLing := provider == "ant-ling" || strings.Contains(baseURL, "api.ant-ling.com")
	isNonStandard := isNVIDIA || provider == "cerebras" || strings.Contains(baseURL, "cerebras.ai") || provider == "xai" || strings.Contains(baseURL, "api.x.ai") || isTogether || strings.Contains(baseURL, "chutes.ai") || strings.Contains(baseURL, "deepseek.com") || isZAI || isMoonshot || provider == "opencode" || strings.Contains(baseURL, "opencode.ai") || isWorkers || isGateway || isAntLing
	isGrok := provider == "xai" || strings.Contains(baseURL, "api.x.ai")
	isDeepSeek := provider == "deepseek" || strings.Contains(baseURL, "deepseek.com")
	isOpenRouterDeveloperModel := isOpenRouter && (strings.HasPrefix(model.ID, "anthropic/") || strings.HasPrefix(model.ID, "openai/"))

	var compat ai.OpenAICompletionsCompat
	if isNonStandard {
		compat.SupportsStore = ptr(false)
	}
	if (isNonStandard || isOpenRouter) && !isOpenRouterDeveloperModel {
		compat.SupportsDeveloperRole = ptr(false)
	}
	if isGrok || isZAI || isMoonshot || isTogether || isGateway || isNVIDIA || isAntLing {
		compat.SupportsReasoningEffort = ptr(false)
	}
	if strings.Contains(baseURL, "chutes.ai") || isMoonshot || isGateway || isTogether || isNVIDIA || isAntLing {
		compat.MaxTokensField = ptr(ai.MaxTokensFieldLegacy)
	}
	if isDeepSeek {
		compat.RequiresReasoningContentOnAssistantMessages = ptr(true)
		compat.ThinkingFormat = ptr(ai.ThinkingFormatDeepSeek)
	} else if isZAI {
		compat.ThinkingFormat = ptr(ai.ThinkingFormatZAI)
	} else if isTogether && !slices.Contains([]string{"deepseek-ai/DeepSeek-R1", "MiniMaxAI/MiniMax-M2.7"}, model.ID) {
		compat.ThinkingFormat = ptr(ai.ThinkingFormatTogether)
	} else if isAntLing {
		compat.ThinkingFormat = ptr(ai.ThinkingFormatAntLing)
	} else if isOpenRouter {
		compat.ThinkingFormat = ptr(ai.ThinkingFormatOpenRouter)
	}
	if isOpenRouter && strings.HasPrefix(model.ID, "anthropic/") {
		compat.CacheControlFormat = ptr(ai.CacheControlAnthropic)
	}
	if isMoonshot || isTogether || isGateway || isNVIDIA {
		compat.SupportsStrictMode = ptr(false)
	}
	if isTogether || isWorkers || isGateway || isNVIDIA || isAntLing {
		compat.SupportsLongCacheRetention = ptr(false)
	}

	var explicit ai.OpenAICompletionsCompat
	if len(model.Compat) != 0 {
		_ = json.Unmarshal(model.Compat, &explicit)
		mergeCompletionsCompat(&compat, explicit)
	}
	applyExplicitCompletionsCompat(model, &compat)
	model.Compat = mustCompatJSON(compat)
}

func applyExplicitCompletionsCompat(model *ai.Model, compat *ai.OpenAICompletionsCompat) {
	provider, id := string(model.Provider), model.ID
	if strings.Contains(id, "deepseek-v4") {
		compat.RequiresReasoningContentOnAssistantMessages = ptr(true)
		if provider != "openrouter" && provider != "opencode" {
			compat.ThinkingFormat = ptr(ai.ThinkingFormatDeepSeek)
		}
	}
	switch provider {
	case "cloudflare-workers-ai":
		compat.SendSessionAffinityHeaders = ptr(true)
	case "cloudflare-ai-gateway":
		compat.SendSessionAffinityHeaders = ptr(true)
	case "huggingface":
		compat.SupportsDeveloperRole = ptr(false)
	case "github-copilot":
		compat.SupportsStore, compat.SupportsDeveloperRole, compat.SupportsReasoningEffort = ptr(false), ptr(false), ptr(false)
	case "moonshotai", "moonshotai-cn":
		compat.ThinkingFormat = ptr(ai.ThinkingFormatDeepSeek)
		if id == "kimi-k3" {
			compat.RequiresReasoningContentOnAssistantMessages = ptr(true)
			compat.DeferredToolsMode = ptr(ai.DeferredToolsKimi)
			// Upstream HEAD switches kimi-k3 to the OpenAI thinking format and
			// enables reasoning effort (generate-models.ts:1761-1766).
			compat.ThinkingFormat = ptr(ai.ThinkingFormatOpenAI)
			compat.SupportsReasoningEffort = ptr(true)
		}
	case "qwen-token-plan", "qwen-token-plan-cn":
		compat.SupportsStore, compat.SupportsDeveloperRole = ptr(false), ptr(false)
		compat.ThinkingFormat = ptr(ai.ThinkingFormatQwen)
	case "xiaomi", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp":
		compat.RequiresReasoningContentOnAssistantMessages = ptr(true)
		compat.ThinkingFormat = ptr(ai.ThinkingFormatDeepSeek)
	case "zai", "zai-coding-cn":
		compat.SupportsDeveloperRole = ptr(false)
		compat.ThinkingFormat = ptr(ai.ThinkingFormatZAI)
		if id == "glm-5.2" {
			compat.SupportsReasoningEffort = ptr(true)
		}
		if !slices.Contains([]string{"glm-4.5", "glm-4.5-air", "glm-4.5-flash", "glm-4.5v"}, id) {
			compat.ZAIToolStream = ptr(true)
		}
	case "opencode", "opencode-go":
		compat.MaxTokensField = ptr(ai.MaxTokensFieldLegacy)
		if provider == "opencode" && id == "grok-build-0.1" {
			compat.SupportsReasoningEffort = ptr(false)
		}
		if id == "kimi-k2.6" {
			compat.ThinkingFormat, compat.SupportsReasoningEffort = ptr(ai.ThinkingFormatDeepSeek), ptr(false)
		}
		if provider == "opencode-go" && (id == "qwen3.5-plus" || id == "qwen3.6-plus") {
			compat.ThinkingFormat = ptr(ai.ThinkingFormatQwen)
		}
		if slices.Contains([]string{
			"opencode:deepseek-v4-flash", "opencode:deepseek-v4-pro", "opencode:kimi-k2.5", "opencode:kimi-k2.6", "opencode:minimax-m2.7", "opencode-go:kimi-k2.6",
		}, provider+":"+id) {
			compat.SupportsLongCacheRetention = ptr(false)
		}
	case "together":
		if slices.Contains([]string{"openai/gpt-oss-20b", "openai/gpt-oss-120b"}, id) {
			compat.SupportsReasoningEffort, compat.ThinkingFormat = ptr(true), ptr(ai.ThinkingFormatOpenAI)
		}
		if id == "deepseek-ai/DeepSeek-V4-Pro" {
			compat.SupportsReasoningEffort, compat.ThinkingFormat = ptr(true), ptr(ai.ThinkingFormatTogether)
		}
	case "fireworks":
		if strings.Contains(id, "glm-5p2") {
			compat.SupportsStore, compat.SupportsDeveloperRole = ptr(false), ptr(false)
		}
	case "openrouter":
		if strings.HasPrefix(id, "moonshotai/kimi-k2.6") {
			compat.SupportsDeveloperRole = ptr(false)
			compat.RequiresReasoningContentOnAssistantMessages = ptr(true)
		}
	}
}

func applyAnthropicCompat(model *ai.Model) {
	var compat ai.AnthropicMessagesCompat
	if len(model.Compat) != 0 {
		_ = json.Unmarshal(model.Compat, &compat)
	}
	provider, id := string(model.Provider), model.ID
	if provider == "fireworks" {
		compat.SendSessionAffinityHeaders = ptr(true)
		compat.SupportsEagerToolInputStreaming = ptr(false)
		compat.SupportsCacheControlOnTools = ptr(false)
		compat.SupportsLongCacheRetention = ptr(false)
	}
	if provider == "cloudflare-ai-gateway" {
		compat.SendSessionAffinityHeaders = ptr(true)
	}
	if provider == "github-copilot" && slices.Contains([]string{"claude-haiku-4.5", "claude-sonnet-4", "claude-sonnet-4.5"}, id) {
		compat.SupportsEagerToolInputStreaming = ptr(false)
	}
	if provider == "kimi-coding" {
		compat.ForceAdaptiveThinking = ptr(true)
		if id == "k3" || id == "kimi-for-coding" {
			compat.AllowEmptySignature = ptr(true)
		}
	}
	if isAnthropicAdaptiveThinkingModel(id) {
		compat.ForceAdaptiveThinking = ptr(true)
	}
	if containsAny(strings.ToLower(id), "opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8") {
		compat.SupportsTemperature = ptr(false)
	}
	if provider == "fireworks" {
		ordered := struct {
			SendSessionAffinityHeaders      *bool `json:"sendSessionAffinityHeaders,omitempty"`
			SupportsEagerToolInputStreaming *bool `json:"supportsEagerToolInputStreaming,omitempty"`
			SupportsCacheControlOnTools     *bool `json:"supportsCacheControlOnTools,omitempty"`
			SupportsLongCacheRetention      *bool `json:"supportsLongCacheRetention,omitempty"`
			ForceAdaptiveThinking           *bool `json:"forceAdaptiveThinking,omitempty"`
			SupportsTemperature             *bool `json:"supportsTemperature,omitempty"`
		}{
			compat.SendSessionAffinityHeaders,
			compat.SupportsEagerToolInputStreaming,
			compat.SupportsCacheControlOnTools,
			compat.SupportsLongCacheRetention,
			compat.ForceAdaptiveThinking,
			compat.SupportsTemperature,
		}
		model.Compat = mustCompatJSON(ordered)
		return
	}
	model.Compat = mustCompatJSON(compat)
}

func applyOpenAIResponsesCompat(model *ai.Model) {
	var compat ai.OpenAIResponsesCompat
	if len(model.Compat) != 0 {
		_ = json.Unmarshal(model.Compat, &compat)
	}
	provider, id := string(model.Provider), model.ID
	if provider == "xai" && id == "grok-4.5" {
		compat.SupportsLongCacheRetention = ptr(false)
	}
	if provider == "opencode" || provider == "opencode-go" {
		compat.SessionAffinityFormat = ptr(ai.SessionAffinityOpenAINoSession)
	}
	if (provider == "openai" && model.API == ai.APIOpenAIResponses || provider == "openai-codex" && model.API == ai.APIOpenAICodexResponses) && slices.Contains([]string{
		"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-pro", "gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
	}, id) {
		compat.SupportsToolSearch = ptr(true)
	}
	model.Compat = mustCompatJSON(compat)
}

func mergeCompletionsCompat(dst *ai.OpenAICompletionsCompat, src ai.OpenAICompletionsCompat) {
	if src.SupportsStore != nil {
		dst.SupportsStore = src.SupportsStore
	}
	if src.SupportsDeveloperRole != nil {
		dst.SupportsDeveloperRole = src.SupportsDeveloperRole
	}
	if src.SupportsReasoningEffort != nil {
		dst.SupportsReasoningEffort = src.SupportsReasoningEffort
	}
	if src.SupportsUsageInStreaming != nil {
		dst.SupportsUsageInStreaming = src.SupportsUsageInStreaming
	}
	if src.MaxTokensField != nil {
		dst.MaxTokensField = src.MaxTokensField
	}
	if src.RequiresToolResultName != nil {
		dst.RequiresToolResultName = src.RequiresToolResultName
	}
	if src.RequiresAssistantAfterToolResult != nil {
		dst.RequiresAssistantAfterToolResult = src.RequiresAssistantAfterToolResult
	}
	if src.RequiresThinkingAsText != nil {
		dst.RequiresThinkingAsText = src.RequiresThinkingAsText
	}
	if src.RequiresReasoningContentOnAssistantMessages != nil {
		dst.RequiresReasoningContentOnAssistantMessages = src.RequiresReasoningContentOnAssistantMessages
	}
	if src.ThinkingFormat != nil {
		dst.ThinkingFormat = src.ThinkingFormat
	}
	if src.ChatTemplateKwargs != nil {
		dst.ChatTemplateKwargs = src.ChatTemplateKwargs
	}
	if src.OpenRouterRouting != nil {
		dst.OpenRouterRouting = src.OpenRouterRouting
	}
	if src.VercelGatewayRouting != nil {
		dst.VercelGatewayRouting = src.VercelGatewayRouting
	}
	if src.ZAIToolStream != nil {
		dst.ZAIToolStream = src.ZAIToolStream
	}
	if src.SupportsStrictMode != nil {
		dst.SupportsStrictMode = src.SupportsStrictMode
	}
	if src.CacheControlFormat != nil {
		dst.CacheControlFormat = src.CacheControlFormat
	}
	if src.SendSessionAffinityHeaders != nil {
		dst.SendSessionAffinityHeaders = src.SendSessionAffinityHeaders
	}
	if src.DeferredToolsMode != nil {
		dst.DeferredToolsMode = src.DeferredToolsMode
	}
	if src.SessionAffinityFormat != nil {
		dst.SessionAffinityFormat = src.SessionAffinityFormat
	}
	if src.SupportsLongCacheRetention != nil {
		dst.SupportsLongCacheRetention = src.SupportsLongCacheRetention
	}
}

func mergeThinking(model *ai.Model, updates map[ai.ModelThinkingLevel]*string) {
	values := make(map[ai.ModelThinkingLevel]*string)
	if model.ThinkingLevelMap != nil {
		for level, value := range *model.ThinkingLevelMap {
			values[level] = value
		}
	}
	for level, value := range updates {
		values[level] = value
	}
	model.ThinkingLevelMap = &values
}

func thinkingValues(values map[ai.ModelThinkingLevel]string) map[ai.ModelThinkingLevel]*string {
	result := make(map[ai.ModelThinkingLevel]*string, len(values))
	for level, value := range values {
		result[level] = ptr(value)
	}
	return result
}

func supportsOpenAIXHigh(id string) bool {
	return containsAny(id, "gpt-5.2", "gpt-5.3", "gpt-5.4", "gpt-5.5", "gpt-5.6")
}

func isAnthropicAdaptiveThinkingModel(id string) bool {
	return containsAny(id, "opus-4-6", "opus-4.6", "opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8", "sonnet-4-6", "sonnet-4.6", "sonnet-5", "sonnet.5", "fable-5")
}

func containsAny(value string, needles ...string) bool {
	return slices.ContainsFunc(needles, func(needle string) bool { return strings.Contains(value, needle) })
}

func ptr[T any](value T) *T { return &value }

func mustCompatJSON[T any](value T) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	if string(data) == "{}" {
		return nil
	}
	return data
}
