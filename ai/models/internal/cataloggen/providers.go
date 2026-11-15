package cataloggen

import (
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/internal/lazyregexp"
)

// One definition per built-in provider (upstream catalog/providers), in
// upstream build order. Radius and the classifier-only TypeSafe catalog are
// not adopted.

const (
	cloudflareGatewayBase = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}"
	copilotBaseURL        = "https://api.individual.githubcopilot.com"
	openAIBaseURL         = "https://api.openai.com/v1"
)

var (
	copilotMessagesPattern  = lazyregexp.New(`^claude-(haiku|sonnet|opus|fable)-[45]([.-]|$)`)
	copilotResponsesPattern = lazyregexp.New(`^(gpt-|grok-|oswe|mai-)`)
	trailingV1Pattern       = lazyregexp.New(`/v1/?$`)
)

func rates(input, output, cacheRead, cacheWrite float64) ai.ModelCost {
	return ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite}}
}

func hasEffort(options []reasoningOption) bool {
	return slices.ContainsFunc(options, func(option reasoningOption) bool { return option.Type == "effort" })
}

func hasToggle(options []reasoningOption) bool {
	return slices.ContainsFunc(options, func(option reasoningOption) bool { return option.Type == "toggle" })
}

// sourcesByID indexes a models.dev provider's chat models; a repeated id keeps the last.
func (ctx *buildContext) sourcesByID(provider string) map[string]sourceModel {
	result := map[string]sourceModel{}
	for _, source := range ctx.modelsDev[provider] {
		result[source.ID] = source
	}
	return result
}

func providers() []provider {
	return []provider{
		{
			id: "amazon-bedrock",
			models: func(ctx *buildContext) []*model {
				return ctx.modelsDevModels("amazon-bedrock", devOptions{
					api: ai.APIBedrockConverse,
					include: func(source sourceModel) bool {
						// Opus 5 is only callable through cross-region inference profiles, Jamba
						// does not stream tool use, and Mistral 7B Instruct rejects system messages.
						return source.ID != "anthropic.claude-opus-5" && !strings.HasPrefix(source.ID, "ai21.jamba") &&
							!strings.HasPrefix(source.ID, "mistral.mistral-7b-instruct-v0")
					},
					mapModel: func(m *model, source sourceModel) *model {
						m.BaseURL = "https://bedrock-runtime.us-east-1.amazonaws.com"
						if strings.HasPrefix(m.ID, "eu.") {
							m.BaseURL = "https://bedrock-runtime.eu-central-1.amazonaws.com"
						}
						if source.StructuredOutput {
							m.compat = compat("supportsStrictMode", true)
						}
						return m
					},
				})
			},
			finalize: func(m *model, _ *buildContext) { m.withInputLimits(0, 0, 20) },
		},
		{
			id: "ant-ling",
			models: func(*buildContext) []*model {
				var models []*model
				for _, item := range []struct {
					id, name  string
					reasoning bool
					cost      ai.ModelCost
				}{
					{"Ling-2.6-flash", "Ling 2.6 Flash", false, rates(.01, .02, 0, 0)},
					{"Ling-2.6-1T", "Ling 2.6 1T", false, rates(.06, .25, 0, 0)},
					{"Ring-2.6-1T", "Ring 2.6 1T", true, rates(.06, .25, 0, 0)},
				} {
					m := &model{Model: ai.Model{
						ID: item.id, Name: item.name, API: ai.APIOpenAICompletions, BaseURL: "https://api.ant-ling.com/v1", Provider: "ant-ling",
						Reasoning: item.reasoning, Input: ai.InputModalities{ai.InputText}, Cost: item.cost, ContextWindow: 262144, MaxTokens: 65536,
					}, compat: compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false,
						"maxTokensField", "max_tokens", "supportsLongCacheRetention", false)}
					if item.reasoning {
						m.mergeCompat(compat("thinkingFormat", "ant-ling"))
					}
					models = append(models, m)
				}
				return models
			},
			completionsCompat: static(compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false,
				"maxTokensField", "max_tokens", "thinkingFormat", "ant-ling", "supportsStrictMode", true, "supportsLongCacheRetention", false)),
			finalize: func(m *model, _ *buildContext) {
				// Ring reasons by default; only high and xhigh have documented effort controls.
				if m.Reasoning {
					m.mergeThinking(levels("off", "", "minimal", "", "low", "", "medium", "", "high", "high", "xhigh", "xhigh"))
				}
			},
		},
		{
			id: "anthropic",
			models: func(ctx *buildContext) []*model {
				return addMissing(ctx.modelsDevModels("anthropic", devOptions{
					api: ai.APIAnthropicMessages, baseURL: "https://api.anthropic.com",
					mapModel: func(m *model, _ sourceModel) *model {
						if slices.Contains([]string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}, m.ID) {
							m.mergeThinking(claude55ThinkingLevels(m.ID))
						}
						if m.ID == "claude-opus-4-6" || m.ID == "claude-sonnet-4-6" {
							m.ContextWindow = 1000000
						}
						return m
					},
				}),
					// Released 2026-11-09; models.dev does not list it for Anthropic yet.
					&model{Model: ai.Model{ID: "claude-fable-5-2", Name: "Claude Fable 5.2", API: ai.APIAnthropicMessages, BaseURL: "https://api.anthropic.com",
						Provider: "anthropic", Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage}, Cost: rates(8, 40, .2, 10),
						ContextWindow: 1000000, MaxTokens: 128000}})
			},
			finalize: func(m *model, _ *buildContext) {
				if m.API != ai.APIAnthropicMessages {
					return
				}
				if supportsClaudeMidConvoEffort(m.ID) {
					enableClaudeMidConvoEffort(m)
				}
				if supportsClaudeMidConvoSystemMessages(m.ID) {
					m.mergeCompat(compat("supportsMidConvoSystemMessages", true, "supportsMidConvoToolChanges", true))
				}
				m.mergeCompat(compat("supportsStrictTools", true))
				// Only direct Anthropic is annotated so cache warming does not assume
				// the same five-minute and one-hour lifetimes through proxies.
				m.PromptCache = &ai.ModelPromptCache{Short: ptr(300.0), Long: ptr(3600.0)}
				perRequest := 600.0
				if m.ContextWindow == 200000 {
					perRequest = 100
				}
				m.withInputLimits(32*1024*1024, perRequest, 0)
			},
			finalizeAll: func(models []*model) {
				byID := map[string]*model{}
				for _, m := range models {
					byID[m.ID] = m
				}
				// Models the API may fall back to, with the price the caller is billed.
				for _, item := range []struct {
					id        string
					fallbacks []string
				}{{"claude-fable-5", []string{"claude-opus-4-8", "claude-opus-5"}}, {"claude-fable-5-2", []string{"claude-opus-4-8", "claude-opus-5"}}, {"claude-opus-5", []string{"claude-opus-4-8"}}} {
					m := byID[item.id]
					if m == nil {
						continue
					}
					var allowed []ai.AnthropicAllowedFallbackModel
					for _, id := range item.fallbacks {
						if m.compatValue("supportsMidConvoEffort") == true && !supportsClaudeMidConvoEffort(id) {
							continue
						}
						if fallback := byID[id]; fallback != nil {
							allowed = append(allowed, ai.AnthropicAllowedFallbackModel{Provider: fallback.Provider, Model: fallback.ID, Cost: fallback.Cost})
						}
					}
					if len(allowed) > 0 {
						m.mergeCompat(compat("allowedFallbackModels", allowed))
					}
				}
			},
		},
		{
			id: "azure",
			models: func(ctx *buildContext) []*model {
				var models []*model
				for _, m := range ctx.providerModels("openai") {
					if m.API != ai.APIOpenAIResponses {
						continue
					}
					// Azure keeps OpenAI's long-context tier and deploys these with larger windows.
					m.API, m.Provider, m.BaseURL = ai.APIAzureOpenAIResponses, "azure", ""
					if slices.Contains([]string{"gpt-5.4", "gpt-5.5", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra",
						"gpt-6-astra", "gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol", "gpt-6.1-terra"}, m.ID) {
						m.ContextWindow = 1050000
					}
					models = append(models, m)
				}
				for _, m := range ctx.providerModels("deepseek") {
					if m.ID == "deepseek-v4-pro" {
						// Azure resells DeepSeek at its own rates (US data zone, checked 2026-09-16).
						m.Provider, m.BaseURL, m.Cost = "azure", "", rates(1.925, 3.828, .165, 0)
						models = append(models, m)
					}
				}
				return models
			},
			completionsCompat: static(compat("supportsStrictMode", true)),
			finalize: func(m *model, _ *buildContext) {
				enableOpenAIGrammarTools(m)
				if m.API == ai.APIOpenAICompletions {
					// Azure 400s on DeepSeek's thinking field and on every prompt cache parameter, and
					// discards a developer system message once reasoning_effort is set (#9645).
					m.mergeCompat(compat("supportsDeveloperRole", false, "supportsMidConvoSystemMessages", true,
						"thinkingFormat", "openai", "supportsLongCacheRetention", false))
				}
			},
		},
		{
			id: "baseten",
			models: func(ctx *buildContext) []*model {
				base := compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false,
					"supportsUsageInStreaming", true, "maxTokensField", "max_tokens", "supportsStrictMode", true,
					// Automatic prompt caching needs session affinity to land on the same replica.
					"sendSessionAffinityHeaders", true, "supportsLongCacheRetention", false)
				effort := spread(base, compat("supportsReasoningEffort", true, "thinkingFormat", "openai"))
				toggleArgs := map[string]any{"enable_thinking": map[string]any{"$var": "thinking.enabled"}}
				return ctx.modelsDevModels("baseten", devOptions{
					api: ai.APIOpenAICompletions, baseURL: "https://inference.baseten.co/v1", skipDeprecated: true, skipReasoning: true,
					mapModel: func(m *model, source sourceModel) *model {
						isGLM52 := m.ID == "zai-org/GLM-5.2" || m.ID == "zai-org/GLM-5.2-Fast"
						toggle, supportsEffort := isGLM52 || hasToggle(source.ReasoningOptions), isGLM52 || hasEffort(source.ReasoningOptions)
						switch {
						case toggle && supportsEffort:
							m.compat = spread(effort, compat("thinkingFormat", "baseten", "chatTemplateArgs", toggleArgs))
						case toggle:
							m.compat = spread(base, compat("thinkingFormat", "baseten", "chatTemplateArgs", toggleArgs))
						case supportsEffort:
							m.compat = effort
						default:
							m.compat = base
						}
						switch {
						case isGLM52:
							m.setThinking(levels("off", "none", "minimal", "", "low", "", "medium", "", "high", "high", "xhigh", "", "max", "max"))
						case toggle:
							m.setThinking(levels("off", "off", "minimal", "", "low", "", "medium", "", "high", "high", "xhigh", "", "max", ""))
						default:
							m.setThinking(effortThinkingLevels(source.ReasoningOptions))
						}
						// Baseten's GLM-5.2 endpoints are text-only despite models.dev reporting image input.
						if isGLM52 {
							m.Input = ai.InputModalities{ai.InputText}
						}
						return m
					},
				})
			},
			completionsCompat: static(compat("supportsStrictMode", true)),
		},
		{
			id: "cerebras",
			models: func(ctx *buildContext) []*model {
				return ctx.modelsDevModels("cerebras", devOptions{api: ai.APIOpenAICompletions, baseURL: "https://api.cerebras.ai/v1"})
			},
			completionsCompat: static(compat("supportsStore", false, "supportsDeveloperRole", false)),
		},
		{
			id: "cloudflare-ai-gateway",
			models: func(ctx *buildContext) []*model {
				models := ctx.modelsDevModels("cloudflare-ai-gateway", devOptions{
					api: ai.APIOpenAICompletions, baseURL: cloudflareGatewayBase + "/compat",
					mapModel: func(m *model, _ sourceModel) *model {
						upstream, id, found := strings.Cut(m.ID, "/")
						if !found {
							return nil
						}
						switch upstream {
						case "openai":
							// The gateway passes OpenAI usage through at OpenAI list prices.
							m.ID, m.API, m.BaseURL = id, ai.APIOpenAIResponses, cloudflareGatewayBase+"/openai"
							if standard, ok := openAIStandardCosts[id]; ok {
								m.Cost = withOpenAILongContextPricing(standard)
							}
						case "anthropic":
							// The /anthropic passthrough forwards the id unchanged and Anthropic only accepts dashed ones.
							m.ID, m.API, m.BaseURL = strings.ReplaceAll(id, ".", "-"), ai.APIAnthropicMessages, cloudflareGatewayBase+"/anthropic"
							m.compat = compat("sendSessionAffinityHeaders", true)
						case "workers-ai":
							m.compat = compat("sendSessionAffinityHeaders", true)
						default:
							return nil
						}
						return m
					},
				})
				// models.dev may drop the gateway's workers-ai/* entries; mirror Workers AI under that prefix.
				listed := map[string]bool{}
				for _, m := range models {
					listed[m.ID] = true
				}
				return append(models, ctx.modelsDevModels("cloudflare-workers-ai", devOptions{
					api: ai.APIOpenAICompletions, baseURL: cloudflareGatewayBase + "/compat", compat: compat("sendSessionAffinityHeaders", true),
					include:  func(source sourceModel) bool { return !listed["workers-ai/"+source.ID] },
					mapModel: func(m *model, _ sourceModel) *model { m.ID = "workers-ai/" + m.ID; return m },
				})...)
			},
			completionsCompat: static(compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false,
				"maxTokensField", "max_tokens", "supportsLongCacheRetention", false)),
			finalize: func(m *model, _ *buildContext) {
				if m.API == ai.APIOpenAIResponses {
					m.mergeCompat(compat("supportsStrictMode", true))
				}
				enableOpenAIGrammarTools(m)
			},
		},
		{
			id: "cloudflare-workers-ai",
			models: func(ctx *buildContext) []*model {
				return ctx.modelsDevModels("cloudflare-workers-ai", devOptions{
					api: ai.APIOpenAICompletions, baseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1",
					compat: compat("sendSessionAffinityHeaders", true),
				})
			},
			completionsCompat: static(compat("supportsStore", false, "supportsDeveloperRole", false, "supportsStrictMode", true, "supportsLongCacheRetention", false)),
		},
		{
			id: "deepseek",
			models: func(*buildContext) []*model {
				// models.dev lags the API (https://api-docs.deepseek.com/quick_start/pricing).
				flash := &model{Model: ai.Model{
					ID: "deepseek-flash", Name: "DeepSeek V4.1 Flash", API: ai.APIOpenAICompletions, BaseURL: "https://api.deepseek.com", Provider: "deepseek",
					Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage}, Cost: rates(.3, 1.2, .006, 0), ContextWindow: 1000000, MaxTokens: 384000,
				}, compat: deepSeekCompat()}
				flash.setThinking(deepSeekV4FlashLevels)
				pro := &model{Model: ai.Model{
					ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", API: ai.APIOpenAICompletions, BaseURL: "https://api.deepseek.com", Provider: "deepseek",
					Reasoning: true, Input: ai.InputModalities{ai.InputText}, Cost: rates(1.32, 3.96, .044, 0), ContextWindow: 1000000, MaxTokens: 384000,
				}, compat: deepSeekCompat()}
				return []*model{flash, pro}
			},
			completionsCompat: static(compat("supportsStore", false, "supportsDeveloperRole", false, "maxTokensField", "max_tokens",
				"requiresReasoningContentOnAssistantMessages", true, "thinkingFormat", "deepseek", "supportsStrictMode", true)),
			finalize: func(m *model, _ *buildContext) {
				// DeepSeek V4 Pro accepts plain system text in place mid-conversation.
				if m.ID == "deepseek-v4-pro" {
					m.mergeCompat(compat("supportsMidConvoSystemMessages", true))
				}
			},
		},
		fireworks(),
		githubCopilot(),
		{
			id:     "google-vertex",
			models: func(ctx *buildContext) []*model { return googleModels(ctx, "google-vertex") },
		},
		{
			id:       "google",
			models:   func(ctx *buildContext) []*model { return googleModels(ctx, "google") },
			finalize: func(m *model, _ *buildContext) { m.withInputLimits(20*1024*1024, 3600, 0) },
		},
		{
			id: "groq",
			models: func(ctx *buildContext) []*model {
				return ctx.modelsDevModels("groq", devOptions{api: ai.APIOpenAICompletions, baseURL: "https://api.groq.com/openai/v1"})
			},
			completionsCompat: static(compat("supportsStrictMode", true)),
			finalize: func(m *model, _ *buildContext) {
				if m.ID == "qwen/qwen3.6-27b" {
					m.mergeThinking(levels("minimal", "", "low", "", "medium", "", "high", "default"))
				}
			},
		},
		{
			id: "huggingface",
			models: func(ctx *buildContext) []*model {
				return ctx.modelsDevModels("huggingface", devOptions{
					api: ai.APIOpenAICompletions, baseURL: "https://router.huggingface.co/v1", compat: compat("supportsDeveloperRole", false),
				})
			},
			completionsCompat: static(compat("supportsStrictMode", true)),
		},
		kimiCoding(),
		{
			id: "meta",
			models: func(ctx *buildContext) []*model {
				return ctx.modelsDevModels("meta", devOptions{api: ai.APIOpenAIResponses, baseURL: "https://api.meta.ai/v1"})
			},
		},
		minimax("minimax-cn", "https://api.minimaxi.com/anthropic"),
		minimax("minimax", "https://api.minimax.io/anthropic"),
		mistral(),
		moonshot("moonshotai-cn", "https://api.moonshot.cn/v1"),
		moonshot("moonshotai", "https://api.moonshot.ai/v1"),
		nvidia(),
		openAICodex(),
		openAI(),
		opencode("opencode-go"),
		opencode("opencode"),
		openRouter(),
		qwenTokenPlan("qwen-token-plan-cn", "alibaba-token-plan-cn", qwenCNBaseURL, nil),
		qwenTokenPlan("qwen-token-plan-individual", "alibaba-token-plan", qwenInternationalBaseURL, qwenIndividualModelIDs),
		qwenTokenPlan("qwen-token-plan-individual-cn", "alibaba-token-plan-cn", qwenCNBaseURL, qwenIndividualModelIDs),
		qwenTokenPlan("qwen-token-plan", "alibaba-token-plan", qwenInternationalBaseURL, nil),
		together(),
		{
			id: "vercel-ai-gateway",
			models: func(ctx *buildContext) []*model {
				var models []*model
				for _, m := range ctx.vercel {
					m = m.clone()
					// Keep Kimi K3's canonical output limit when gateway metadata is missing or incorrect.
					if id, ok := strings.CutPrefix(m.ID, "moonshotai/"); ok && isKimiK3Family(id) {
						m.MaxTokens = kimiK3MaxTokens
					}
					models = append(models, m)
				}
				return models
			},
		},
		{
			id: "xai",
			models: func(ctx *buildContext) []*model {
				excluded := []string{"grok-3", "grok-3-fast", "grok-4.20-0309-non-reasoning", "grok-4.20-0309-reasoning", "grok-build-0.1", "grok-code-fast-1"}
				return ctx.modelsDevModels("xai", devOptions{
					api: ai.APIOpenAIResponses, baseURL: "https://api.x.ai/v1", compat: compat("supportsLongCacheRetention", false),
					include: func(source sourceModel) bool { return !slices.Contains(excluded, source.ID) },
				})
			},
			finalize: func(m *model, _ *buildContext) {
				// Models without verified effort options must not send the undocumented none/minimal efforts.
				if m.API == ai.APIOpenAIResponses && m.ThinkingLevelMap == nil {
					m.mergeThinking(levels("off", "", "minimal", ""))
				}
			},
		},
		xiaomi("xiaomi-token-plan-ams", "https://token-plan-ams.xiaomimimo.com/v1"),
		xiaomi("xiaomi-token-plan-cn", "https://token-plan-cn.xiaomimimo.com/v1"),
		xiaomi("xiaomi-token-plan-sgp", "https://token-plan-sgp.xiaomimimo.com/v1"),
		xiaomi("xiaomi", "https://api.xiaomimimo.com/v1"),
		zai("zai-coding-cn", "zhipuai-coding-plan", "https://open.bigmodel.cn/api/coding/paas/v4"),
		zai("zai", "zai-coding-plan", "https://api.z.ai/api/coding/paas/v4"),
	}
}

func fireworks() provider {
	// Fireworks' Anthropic-compatible API caches by prefix with session
	// affinity and rejects cache_control on tools and eager input streaming.
	anthropicCompat := compat("allowEmptySignature", true, "sendSessionAffinityHeaders", true, "supportsEagerToolInputStreaming", false,
		"supportsCacheControlOnTools", false, "supportsLongCacheRetention", false)
	openAICompat := compat("supportsStore", false, "supportsDeveloperRole", false, "sendSessionAffinityHeaders", true, "supportsLongCacheRetention", false)
	kimiK3 := spread(openAICompat, compat("requiresReasoningContentOnAssistantMessages", true, "thinkingFormat", "openai"))
	// Verified adaptive thinking where models.dev omits effort metadata (#9323); not an allowlist.
	adaptiveFallback := []string{
		"accounts/fireworks/models/deepseek-v4-flash-0731", "accounts/fireworks/models/deepseek-v4-flash-vision-exp",
		"accounts/fireworks/models/deepseek-v4-pro-0813", "accounts/fireworks/models/qwen3p8-max", "accounts/fireworks/models/qwen3p8-2p4t-a95b",
	}
	return provider{
		id: "fireworks",
		models: func(ctx *buildContext) []*model {
			return ctx.modelsDevModels("fireworks-ai", devOptions{
				api: ai.APIAnthropicMessages, baseURL: "https://api.fireworks.ai/inference",
				mapModel: func(m *model, source sourceModel) *model {
					if strings.Contains(m.ID, "glm-") || strings.Contains(m.ID, "kimi-k3") {
						m.API, m.BaseURL, m.compat = ai.APIOpenAICompletions, "https://api.fireworks.ai/inference/v1", openAICompat
						if strings.Contains(m.ID, "kimi-k3") {
							m.compat = kimiK3
						}
						return m
					}
					m.compat = anthropicCompat
					if hasEffort(source.ReasoningOptions) || slices.Contains(adaptiveFallback, m.ID) {
						m.mergeCompat(compat("forceAdaptiveThinking", true))
					}
					return m
				},
			})
		},
		completionsCompat: static(compat("supportsStrictMode", true)),
		finalize: func(m *model, ctx *buildContext) {
			if m.API == ai.APIAnthropicMessages && m.compatValue("forceAdaptiveThinking") == true {
				// Qwen Max advertises only a toggle; upstream effort metadata wins once listed.
				if m.ID == "accounts/fireworks/models/qwen3p8-max" && m.ThinkingLevelMap == nil {
					m.setThinking(effortThinkingLevels([]reasoningOption{{Type: "effort", Values: []*string{ptr("low"), ptr("medium"), ptr("xhigh")}}}))
				}
				source, _ := ctx.modelsDevSource(m)
				// The 2.4T alias omits the verified toggle in models.dev.
				if hasToggle(source.ReasoningOptions) || m.ID == "accounts/fireworks/models/qwen3p8-2p4t-a95b" {
					m.mergeThinking(levels("off", "none"))
				}
				if m.ID == "accounts/fireworks/models/deepseek-v4-pro-0813" {
					m.mergeThinking(levels("low", "low"))
				}
			}
			if strings.Contains(m.ID, "glm-5p2") {
				// Fireworks maps low and medium to high on GLM 5.2.
				m.mergeThinking(levels("off", "none", "minimal", "", "low", "", "medium", "", "max", "max"))
			}
			if strings.Contains(m.ID, "kimi-k3") {
				// Fireworks maps medium to high on both APIs.
				m.mergeThinking(levels("medium", ""))
				if m.API == ai.APIOpenAICompletions {
					m.mergeCompat(compat("supportsMidConvoSystemMessages", true, "supportsMidConvoToolAdditions", true))
				}
			}
		},
	}
}

func githubCopilot() provider {
	headers := map[string]string{
		"User-Agent": "GitHubCopilotChat/0.35.0", "Editor-Version": "vscode/1.107.0",
		"Editor-Plugin-Version": "copilot-chat/0.35.0", "Copilot-Integration-Id": "vscode-chat",
	}
	// GitHub's extended-capabilities table lists these with the 1M context window.
	extendedContext := []string{
		"claude-fable-5", "claude-opus-4.8", "claude-opus-5", "claude-opus-5.5", "claude-sonnet-4.6", "claude-sonnet-5",
		"gpt-5.3-codex", "gpt-6-astra", "gpt-6-luna", "gpt-6-sol",
	}
	// Checked against the authenticated Copilot /models endpoint on 2026-06-15.
	overrides := map[string]thinking{
		"claude-opus-4.8":   levels("minimal", "low"),
		"claude-opus-5":     levels("minimal", "low"),
		"claude-sonnet-4.6": levels("minimal", "low", "max", "max"),
	}
	return provider{
		id: "github-copilot",
		models: func(ctx *buildContext) []*model {
			models := ctx.modelsDevModels("github-copilot", devOptions{
				api: ai.APIOpenAICompletions, baseURL: copilotBaseURL, headers: headers, skipDeprecated: true,
				defaultContext: 128000, defaultMaxTokens: 8192,
				mapModel: func(m *model, _ sourceModel) *model {
					// Claude 4.x and 5.x use Messages; GPT, Grok, OSWE and MAI-Code are only served on /responses.
					switch {
					case copilotMessagesPattern().MatchString(m.ID):
						m.API = ai.APIAnthropicMessages
					case copilotResponsesPattern().MatchString(m.ID):
						m.API = ai.APIOpenAIResponses
					default:
						m.compat = compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false)
					}
					if m.ID == "claude-opus-5.5" {
						m.mergeThinking(claude55ThinkingLevels(m.ID))
					}
					return m
				},
			})
			// The authenticated Copilot catalog advertised GPT-6 Luna on 2026-09-22 before models.dev.
			luna := &model{Model: ai.Model{
				ID: "gpt-6-luna", Name: "GPT-6 Luna", API: ai.APIOpenAIResponses, Provider: "github-copilot", BaseURL: copilotBaseURL,
				Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage}, Cost: withOpenAILongContextPricing(openAIStandardCosts["gpt-6-luna"]),
				ContextWindow: 1000000, MaxTokens: 128000,
			}}
			lunaHeaders := map[string]string{}
			for name, value := range headers {
				lunaHeaders[name] = value
			}
			luna.Headers = &lunaHeaders
			models = addMissing(models, luna)
			for _, m := range models {
				if slices.Contains(extendedContext, m.ID) {
					m.ContextWindow = 1000000
				}
			}
			return models
		},
		completionsCompat: static(compat("supportsStrictMode", true)),
		finalize: func(m *model, _ *buildContext) {
			if strings.HasPrefix(m.ID, "gpt-5") {
				m.mergeThinking(levels("minimal", "low"))
			}
			if override, ok := overrides[m.ID]; ok {
				m.mergeThinking(override)
			}
			if m.API == ai.APIAnthropicMessages {
				// Copilot forwards mid-conversation system messages but rejects tool_addition/tool_removal blocks.
				if supportsClaudeMidConvoSystemMessages(m.ID) {
					m.mergeCompat(compat("supportsMidConvoSystemMessages", true))
				}
				if slices.Contains([]string{"claude-haiku-4.5", "claude-sonnet-4", "claude-sonnet-4.5"}, m.ID) {
					m.mergeCompat(compat("supportsEagerToolInputStreaming", false))
				}
			}
			// Copilot forwards Kimi K3's system text but silently drops its tool-bearing message.
			if m.API == ai.APIOpenAICompletions && m.ID == "kimi-k3" {
				m.mergeCompat(compat("supportsMidConvoSystemMessages", true))
			}
			enableOpenAIMidConvoSystemMessages(m, true)
			enableOpenAIGrammarTools(m)
		},
	}
}

// googleAliasTargets: rolling aliases take their metadata from the model they point to.
var googleAliasTargets = map[string]string{"gemini-flash-latest": "gemini-3.5-flash", "gemini-flash-lite-latest": "gemini-3.1-flash-lite"}

func googleModels(ctx *buildContext, provider string) []*model {
	sources := ctx.sourcesByID(provider)
	options := devOptions{
		api: ai.APIGoogleGenerativeAI, baseURL: "https://generativelanguage.googleapis.com/v1beta", skipReasoning: true,
		mapModel: func(m *model, aliasSource sourceModel) *model {
			source := aliasSource
			if target, ok := sources[googleAliasTargets[m.ID]]; ok {
				source = target
			}
			m.Reasoning = source.Reasoning
			if mapped := googleThinkingLevels(m.ID, source.ReasoningOptions); mapped != nil {
				m.setThinking(mapped)
			}
			m.Input = ai.InputModalities{ai.InputText}
			if source.ImageInput {
				m.Input = append(m.Input, ai.InputImage)
			}
			m.Cost = modelsDevCost(source.Cost)
			if provider == "google-vertex" {
				// models.dev's Vertex cache prices for Gemini 2.5 Flash do not match the
				// Gemini API table, and only cachedContentTokenCount is accounted as cacheRead.
				m.Cost = rates(rate(source.Cost.Input), rate(source.Cost.Output), rate(source.Cost.CacheRead), 0)
				if m.ID == "gemini-2.5-flash" {
					m.Cost.CacheRead = .03
				}
			}
			m.ContextWindow, m.MaxTokens = firstNonZero(source.ContextWindow, 4096), firstNonZero(source.MaxTokens, 4096)
			return m
		},
	}
	if provider == "google-vertex" {
		options.api, options.baseURL = ai.APIGoogleVertex, "https://{location}-aiplatform.googleapis.com"
		// Vertex also lists Claude, OpenAI and other MaaS models outside the Gemini streaming path.
		options.include = func(source sourceModel) bool {
			return strings.HasPrefix(source.ID, "gemini-") && source.ID != "gemini-3.1-flash-lite-preview"
		}
	}
	return ctx.modelsDevModels(provider, options)
}

func kimiCoding() provider {
	// Kimi Coding is subscription-backed and models.dev reports zero cost; the
	// equivalent Moonshot API rates estimate the value of subscription usage.
	implied := map[string]ai.ModelCostRates{
		"k3": kimiK3Cost, "k3-256k": kimiK3Cost,
		"k3.1-pro":                  {Input: 2, Output: 10, CacheRead: .2},
		"k3.1-pro-highspeed":        {Input: 4, Output: 20, CacheRead: .2},
		"kimi-for-coding":           {Input: .95, Output: 4, CacheRead: .19},
		"kimi-for-coding-highspeed": {Input: 1.9, Output: 8, CacheRead: .38},
	}
	orImplied := func(value *float64, fallback float64) float64 { return firstNonZero(rate(value), fallback) }
	return provider{
		id: "kimi-coding",
		models: func(ctx *buildContext) []*model {
			return ctx.modelsDevModels("kimi-code-plan-global", devOptions{
				api: ai.APIOpenAICompletions, baseURL: "https://api.kimi.com/coding/v1", skipReasoning: true,
				mapModel: func(m *model, source sourceModel) *model {
					// K3-style models report effort levels and dynamic tool loading; the
					// others reject tool-bearing system messages. All accept system text mid-conversation.
					k3Style := hasEffort(source.ReasoningOptions)
					m.compat = spread(kimiCompat(), compat("supportsMidConvoSystemMessages", true))
					effort := thinking{}
					if k3Style {
						m.compat = spread(kimiK3Compat(), compat("supportsMidConvoSystemMessages", true, "supportsMidConvoToolAdditions", true))
						if mapped := effortThinkingLevels(source.ReasoningOptions); mapped != nil {
							effort = mapped
						}
					}
					cost := implied[m.ID]
					m.Cost = rates(orImplied(source.Cost.Input, cost.Input), orImplied(source.Cost.Output, cost.Output),
						orImplied(source.Cost.CacheRead, cost.CacheRead), orImplied(source.Cost.CacheWrite, cost.CacheWrite))
					// Kimi Code reports every model as always-thinking.
					effort[ai.ModelThinkingOff] = nil
					m.setThinking(effort)
					return m
				},
			})
		},
		completionsCompat: static(compat("supportsStrictMode", true)),
	}
}

func minimax(id, baseURL string) provider {
	// MiniMax asked to hide older models from its direct providers (#2596).
	hidden := []string{"MiniMax-M2", "MiniMax-M2.1", "MiniMax-M2.5", "MiniMax-M2.5-highspeed"}
	return provider{
		id: id,
		models: func(ctx *buildContext) []*model {
			return ctx.modelsDevModels(id, devOptions{
				api: ai.APIAnthropicMessages, baseURL: baseURL, skipReasoning: true,
				include: func(source sourceModel) bool { return !slices.Contains(hidden, source.ID) },
				mapModel: func(m *model, source sourceModel) *model {
					// Models with effort levels take adaptive thinking and reject thinking disabled.
					if effort := effortThinkingLevels(source.ReasoningOptions); effort != nil {
						m.setThinking(effort)
						m.compat = compat("forceAdaptiveThinking", true)
					}
					return m
				},
			})
		},
	}
}

func mistral() provider {
	return provider{
		id: "mistral",
		models: func(ctx *buildContext) []*model {
			models := ctx.modelsDevModels("mistral", devOptions{
				api: ai.APIMistralConversations, baseURL: "https://api.mistral.ai", skipReasoning: true,
				mapModel: func(m *model, source sourceModel) *model {
					// Models with effort values use reasoning_effort; reasoning models without them (Magistral) use prompt_mode.
					if effort := effortThinkingLevels(source.ReasoningOptions); effort != nil {
						m.setThinking(effort)
					}
					// Mistral bills cached input at 10% when models.dev does not say otherwise.
					cacheRead := roundCost(rate(source.Cost.Input) * 0.1)
					if source.Cost.CacheRead != nil {
						cacheRead = *source.Cost.CacheRead
					}
					m.Cost = rates(rate(source.Cost.Input), rate(source.Cost.Output), cacheRead, rate(source.Cost.CacheWrite))
					return m
				},
			})
			medium := &model{Model: ai.Model{
				ID: "mistral-medium-3.5", Name: "Mistral Medium 3.5", API: ai.APIMistralConversations, Provider: "mistral", BaseURL: "https://api.mistral.ai",
				Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage}, Cost: rates(1.5, 7.5, 0, 0), ContextWindow: 262144, MaxTokens: 262144,
			}}
			medium.setThinking(effortThinkingLevels([]reasoningOption{{Type: "effort", Values: []*string{ptr("none"), ptr("high")}}}))
			return addMissing(models, medium)
		},
	}
}

func moonshot(id, baseURL string) provider {
	alwaysThinking := []string{"kimi-k2.7-code", "kimi-k2.7-code-highspeed"}
	return provider{
		id: id,
		models: func(ctx *buildContext) []*model {
			return ctx.modelsDevModels(id, devOptions{
				api: ai.APIOpenAICompletions, baseURL: baseURL,
				mapModel: func(m *model, source sourceModel) *model {
					k3 := isKimiK3Family(m.ID)
					m.Reasoning = k3 || source.Reasoning
					// Moonshot does not bill cache writes; models.dev lists the input rate as the K3 family's cache_write.
					cacheWrite := rate(source.Cost.CacheWrite)
					if k3 {
						cacheWrite = 0
					}
					m.Cost = rates(rate(source.Cost.Input), rate(source.Cost.Output), rate(source.Cost.CacheRead), cacheWrite)
					if m.ID == "kimi-k3" {
						m.Cost = ai.ModelCost{ModelCostRates: kimiK3Cost}
					}
					m.compat = kimiCompat()
					if k3 {
						m.compat = kimiK3Compat()
					}
					return m
				},
			})
		},
		completionsCompat: static(compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false, "maxTokensField", "max_tokens")),
		finalize: func(m *model, _ *buildContext) {
			// Kimi K2.7 Code is always-thinking and rejects thinking disabled.
			if slices.Contains(alwaysThinking, m.ID) {
				m.mergeThinking(levels("off", ""))
			}
			// Kimi K2.6/K2.7 accept system text mid-conversation but reject tool-bearing
			// system messages; the K3 family accepts both.
			if k3 := isKimiK3Family(m.ID); k3 || m.ID == "kimi-k2.6" || slices.Contains(alwaysThinking, m.ID) {
				update := compat("supportsMidConvoSystemMessages", true)
				if k3 {
					update = append(update, jsonwire.OrderedMember{Name: "supportsMidConvoToolAdditions", Value: true})
				}
				m.mergeCompat(update)
			}
		},
	}
}

func nvidia() provider {
	// Listed by NIM but unusable (no tool calls, broken streaming, or retired).
	unsupported := []string{
		"abacusai/dracarys-llama-3.1-70b-instruct", "bytedance/seed-oss-36b-instruct",
		"deepseek-ai/deepseek-v4-flash", "deepseek-ai/deepseek-v4-pro", "google/gemma-2-2b-it",
		"google/gemma-3n-e2b-it", "google/gemma-3n-e4b-it", "google/gemma-4-31b-it",
		"meta/llama-3.2-1b-instruct", "meta/llama-4-maverick-17b-128e-instruct",
		"microsoft/phi-4-mini-instruct", "minimaxai/minimax-m2.7", "mistralai/mistral-nemotron",
		"nvidia/nemotron-mini-4b-instruct", "qwen/qwen3-next-80b-a3b-instruct",
		"qwen/qwen3.5-397b-a17b", "sarvamai/sarvam-m", "upstage/solar-10.7b-instruct",
		// Orb-only: absent from the live NIM listing; kept so a NIM rollout cannot resurrect it silently.
		"qwen/qwen3.5-122b-a10b",
	}
	completions := compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false,
		"maxTokensField", "max_tokens", "supportsLongCacheRetention", false)
	return provider{
		id: "nvidia",
		models: func(ctx *buildContext) []*model {
			liveID := func(id string) (string, bool) {
				if live, ok := ctx.nvidiaIDs[id]; ok {
					return live, true
				}
				live, ok := ctx.nvidiaIDs[normalizeNvidiaModelID(id)]
				return live, ok
			}
			return ctx.modelsDevModels("nvidia", devOptions{
				api: ai.APIOpenAICompletions, baseURL: "https://integrate.api.nvidia.com/v1", headers: map[string]string{"NVCF-POLL-SECONDS": "3600"},
				compat: compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false,
					"maxTokensField", "max_tokens", "supportsStrictMode", false, "supportsLongCacheRetention", false),
				// Only models the live NIM API serves, under the id it uses.
				include: func(source sourceModel) bool {
					id, ok := liveID(source.ID)
					return source.TextInput && source.TextOutput && ok && !slices.Contains(unsupported, id)
				},
				mapModel: func(m *model, _ sourceModel) *model {
					if id, ok := liveID(m.ID); ok {
						if m.Name == m.ID {
							m.Name = id
						}
						m.ID = id
					}
					return m
				},
			})
		},
		completionsCompat: static(completions),
	}
}

func openAICodex() provider {
	return provider{
		id: "openai-codex",
		models: func(*buildContext) []*model {
			// ChatGPT OAuth models the Codex backend accepts for ChatGPT accounts. GPT-6.1
			// uses the 500k default of /backend-api/codex/models, GPT-5.6 and GPT-6 Codex's 272k.
			var models []*model
			for _, item := range []struct {
				id, name string
				context  float64
			}{
				{"gpt-6.1-sol", "GPT-6.1 Sol", 500000}, {"gpt-6.1-terra", "GPT-6.1 Terra", 500000},
				{"gpt-6-astra", "GPT-6 Astra", 272000}, {"gpt-6-sol", "GPT-6 Sol", 272000}, {"gpt-6-luna", "GPT-6 Luna", 272000},
				{"gpt-5.6-luna", "GPT-5.6 Luna", 272000}, {"gpt-5.6-sol", "GPT-5.6 Sol", 272000}, {"gpt-5.6-terra", "GPT-5.6 Terra", 272000},
			} {
				models = append(models, &model{Model: ai.Model{
					ID: item.id, Name: item.name, API: ai.APIOpenAICodexResponses, Provider: "openai-codex", BaseURL: "https://chatgpt.com/backend-api",
					Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage}, Cost: withOpenAILongContextPricing(openAIStandardCosts[item.id]),
					ContextWindow: item.context, MaxTokens: 128000,
				}})
			}
			return models
		},
		finalize: func(m *model, _ *buildContext) {
			if supportsOpenAIXHigh(m.ID) {
				m.mergeThinking(levels("minimal", "low"))
			}
			enableOpenAIGrammarTools(m)
			// Codex CLI keeps additional_tools behind its own model list, today the tool search one.
			if slices.Contains(openAIToolSearchModels, m.ID) {
				m.mergeCompat(compat("supportsAdditionalTools", true, "supportsToolSearch", true))
			}
			enableOpenAIMidConvoSystemMessages(m, false)
		},
	}
}

func openAI() provider {
	// Direct requests stay in the short-context pricing tier by default; users
	// opt into the larger context through model overrides.
	shortContext := []string{
		"gpt-5.4", "gpt-5.5", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol", "gpt-6.1-terra",
	}
	longContextPricing := append(slices.Clone(shortContext), "gpt-5.4-pro", "gpt-5.5-pro")
	noneReasoning := []string{
		"gpt-5.1", "gpt-5.2", "gpt-5.3-codex", "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.5",
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-sol", "gpt-6-luna",
	}
	// OpenAI documents configuration_update for the GPT-6 family.
	midConvoEffort := []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol", "gpt-6.1-terra"}
	return provider{
		id: "openai",
		models: func(ctx *buildContext) []*model {
			models := ctx.modelsDevModels("openai", devOptions{
				// Audio models are only served on Chat Completions; models.dev's gpt-5.6 alias is not accepted.
				api: ai.APIOpenAIResponses, baseURL: openAIBaseURL, protocol: "responses",
				include: func(source sourceModel) bool { return source.ID != "gpt-5.6" },
				mapModel: func(m *model, _ sourceModel) *model {
					if slices.Contains(shortContext, m.ID) {
						m.ContextWindow, m.MaxTokens = openAILongContextThreshold, 128000
					}
					if slices.Contains(longContextPricing, m.ID) {
						standard, ok := openAIStandardCosts[m.ID]
						if !ok {
							standard = m.Cost.ModelCostRates
						}
						m.Cost = withOpenAILongContextPricing(standard)
					}
					// models.dev repeats the 272K input sub-limit as gpt-5-pro's output limit.
					if m.ID == "gpt-5-pro" {
						m.MaxTokens = 128000
					}
					return m
				},
			})
			return addMissing(models, &model{Model: ai.Model{
				ID: "gpt-5-chat-latest", Name: "GPT-5 Chat Latest", API: ai.APIOpenAIResponses, BaseURL: openAIBaseURL, Provider: "openai",
				Input: ai.InputModalities{ai.InputText, ai.InputImage}, Cost: rates(1.25, 10, .125, 0), ContextWindow: 128000, MaxTokens: 16384,
			}})
		},
		finalize: func(m *model, _ *buildContext) {
			if m.API != ai.APIOpenAIResponses {
				return
			}
			if slices.Contains(noneReasoning, m.ID) {
				m.mergeThinking(levels("off", "none"))
			}
			if m.ID == "gpt-5.5" {
				m.mergeThinking(levels("minimal", ""))
			}
			m.mergeCompat(compat("supportsStrictMode", true))
			enableOpenAIGrammarTools(m)
			if slices.Contains(openAIToolSearchModels, m.ID) {
				m.mergeCompat(compat("supportsAdditionalTools", true, "supportsToolSearch", true))
			}
			enableOpenAIMidConvoSystemMessages(m, false)
			// Prompt-cache writes are billed from GPT-5.6 on, and exactly those models accept prompt_cache_options.
			if m.Cost.CacheWrite > 0 {
				m.mergeCompat(compat("supportsExplicitPromptCacheMode", true))
			}
			if slices.Contains(midConvoEffort, m.ID) {
				m.mergeCompat(compat("supportsMidConvoEffort", true))
			}
			m.withInputLimits(512*1024*1024, 1500, 0)
		},
	}
}

func opencode(id string) provider {
	// OpenAI-compatible endpoints reject the long prompt-cache retention parameter for these models.
	noLongCache := []string{
		"opencode:deepseek-v4-flash", "opencode:deepseek-v4.1-flash", "opencode:deepseek-v4-pro", "opencode:kimi-k2.5",
		"opencode:kimi-k2.6", "opencode:minimax-m2.7", "opencode-go:kimi-k2.6",
	}
	return provider{
		id: id,
		models: func(ctx *buildContext) []*model {
			return ctx.modelsDevModels(id, devOptions{
				api: ai.APIOpenAICompletions, skipDeprecated: true,
				// Codex Spark is not usable through OpenCode.
				include: func(source sourceModel) bool { return source.ID != "gpt-5.3-codex-spark" },
				mapModel: func(m *model, source sourceModel) *model {
					// One protocol-specific endpoint per model; native protocols come before Chat Completions.
					var entry *sourceAPI
					for _, protocol := range []string{"responses", "messages", "generate-content", "chat-completions"} {
						if index := slices.IndexFunc(source.APIs, func(api sourceAPI) bool { return api.Protocol == protocol }); index >= 0 {
							entry = &source.APIs[index]
							break
						}
					}
					if entry == nil {
						return nil
					}
					var served jsonwire.OrderedObject
					m.BaseURL = entry.BaseURL
					switch entry.Protocol {
					case "responses":
						m.API, served = ai.APIOpenAIResponses, compat("sessionAffinityFormat", "openai-nosession")
					case "messages":
						// The Anthropic SDK appends /v1/messages itself.
						m.API, m.BaseURL = ai.APIAnthropicMessages, trailingV1Pattern().ReplaceAllString(entry.BaseURL, "")
					case "generate-content":
						m.API = ai.APIGoogleGenerativeAI
					default:
						m.API = ai.APIOpenAICompletions
						if entry.AISDK == "@ai-sdk/alibaba" {
							served = compat("cacheControlFormat", "anthropic")
						}
					}
					if id == "opencode" && m.ID == "grok-build-0.1" {
						served = spread(served, compat("supportsReasoningEffort", false))
					}
					if m.ID == "kimi-k2.6" {
						// OpenCode Kimi K2.6 takes Anthropic-style thinking objects and rejects reasoning_effort.
						served = spread(served, compat("thinkingFormat", "deepseek", "supportsReasoningEffort", false))
					}
					// models.dev lists these on Messages, but OpenCode Go serves them on /openai/v1/chat/completions.
					if id == "opencode-go" && m.API == ai.APIAnthropicMessages && (m.ID == "qwen3.5-plus" || m.ID == "qwen3.6-plus") {
						m.API = ai.APIOpenAICompletions
						if trimmed, ok := strings.CutSuffix(m.BaseURL, "/anthropic"); ok {
							m.BaseURL = trimmed + "/openai/v1"
						}
						served = spread(served, compat("thinkingFormat", "qwen"))
					}
					if m.API == ai.APIOpenAICompletions {
						served = spread(served, compat("maxTokensField", "max_tokens"))
						if slices.Contains(noLongCache, id+":"+m.ID) {
							served = spread(served, compat("supportsLongCacheRetention", false))
						}
					}
					switch {
					case m.API == ai.APIGoogleGenerativeAI:
						if mapped := googleThinkingLevels(m.ID, source.ReasoningOptions); mapped != nil {
							m.setThinking(mapped)
						}
					case id == "opencode-go" && m.ID == "deepseek-v4.1-flash":
						if effort := effortThinkingLevels(source.ReasoningOptions); effort != nil {
							m.setThinking(effort)
						}
					}
					if served != nil {
						m.compat = served
					}
					// OpenCode lists Claude Sonnet 4/4.5 with 1M context; the limit is 200K.
					if m.ID == "claude-sonnet-4-5" || m.ID == "claude-sonnet-4" {
						m.ContextWindow = 200000
					}
					if slices.Contains([]string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-opus-4.6", "claude-sonnet-4.6"}, m.ID) {
						m.ContextWindow = 1000000
					}
					if m.ID == "gpt-5.4" {
						m.ContextWindow, m.MaxTokens = 272000, 128000
					}
					return m
				},
			})
		},
		completionsCompat: static(compat("supportsStore", false, "supportsDeveloperRole", false, "supportsStrictMode", true)),
		finalize: func(m *model, _ *buildContext) {
			if id == "opencode-go" && m.ID == "glm-5.2" {
				m.mergeThinking(levels("off", "", "minimal", "", "low", "", "medium", "", "high", "high", "max", "max"))
			}
			// OpenCode Go exposes Kimi K2.6 thinking as on/off.
			if id == "opencode-go" && m.ID == "kimi-k2.6" {
				m.mergeThinking(levels("minimal", "", "low", "", "medium", ""))
			}
			// OpenCode Zen Grok Build reasons by default but rejects explicit reasoningEffort.
			if id == "opencode" && m.ID == "grok-build-0.1" {
				m.mergeThinking(levels("off", "", "minimal", "", "low", "", "medium", ""))
			}
			if m.API == ai.APIAnthropicMessages {
				// OpenCode Zen forwards mid-conversation system messages but rejects tool_addition/tool_removal blocks.
				if id == "opencode" && supportsClaudeMidConvoSystemMessages(m.ID) {
					m.mergeCompat(compat("supportsMidConvoSystemMessages", true))
				}
				// Zen and Go forward per-message effort and thinking.block_binding. Without
				// drop_block, Go rejects replayed thinking after the system prompt or tools change.
				if supportsClaudeMidConvoEffort(m.ID) {
					enableClaudeMidConvoEffort(m)
				}
				// OpenCode Qwen 3.8 Flash emits and accepts thinking blocks with empty signatures.
				if m.ID == "qwen3.8-flash" {
					m.mergeCompat(compat("allowEmptySignature", true))
				}
			}
			if m.API == ai.APIOpenAICompletions && isKimiK3Family(m.ID) {
				m.mergeCompat(compat("supportsMidConvoSystemMessages", true, "supportsMidConvoToolAdditions", true))
			}
			enableOpenAIMidConvoSystemMessages(m, true)
			if id == "opencode" {
				enableOpenAIGrammarTools(m)
			}
		},
	}
}

func openRouter() provider {
	const baseURL = "https://openrouter.ai/api/v1"
	return provider{
		id: "openrouter",
		models: func(ctx *buildContext) []*model {
			var models []*model
			for _, m := range ctx.openRouter {
				m = m.clone()
				// Keep selected metadata stable until upstream settles.
				if id, ok := strings.CutPrefix(m.ID, "moonshotai/"); m.ID == "~moonshotai/kimi-latest" || ok && isKimiK3Family(id) {
					m.MaxTokens = kimiK3MaxTokens
				}
				if m.ID == "moonshotai/kimi-k2.5" {
					m.Cost.Input, m.Cost.Output, m.Cost.CacheRead, m.MaxTokens = .41, 2.06, .07, 4096
				}
				if strings.HasPrefix(m.ID, "moonshotai/kimi-k2.6") {
					m.mergeCompat(compat("supportsDeveloperRole", false, "requiresReasoningContentOnAssistantMessages", true))
				}
				if m.ID == "z-ai/glm-5" {
					m.Cost.Input, m.Cost.Output, m.Cost.CacheRead = .6, 1.9, .119
				}
				models = append(models, m)
			}
			// Router aliases: their price depends on the model they route to. Fusion's
			// metadata does not advertise tools, but it resolves to a model that can call them.
			return addMissing(models,
				&model{Model: ai.Model{ID: "auto", Name: "Auto", API: ai.APIOpenAICompletions, Provider: "openrouter", BaseURL: baseURL,
					Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage}, ContextWindow: 2000000, MaxTokens: 30000}},
				&model{Model: ai.Model{ID: "openrouter/fusion", Name: "OpenRouter: Fusion", API: ai.APIOpenAICompletions, Provider: "openrouter", BaseURL: baseURL,
					Reasoning: true, Input: ai.InputModalities{ai.InputText}, ContextWindow: 1000000, MaxTokens: 30000}})
		},
		completionsCompat: func(m *model) jsonwire.OrderedObject {
			// OpenRouter passes developer messages through to Anthropic and OpenAI models only.
			defaults := jsonwire.OrderedObject{}
			if !strings.HasPrefix(m.ID, "anthropic/") && !strings.HasPrefix(m.ID, "openai/") {
				defaults = compat("supportsDeveloperRole", false)
			}
			defaults = spread(defaults, compat("thinkingFormat", "openrouter", "supportsStrictMode", true))
			if strings.HasPrefix(strings.TrimPrefix(m.ID, "~"), "anthropic/") {
				defaults = spread(defaults, compat("cacheControlFormat", "anthropic"))
			}
			return spread(defaults, compat("sendSessionAffinityHeaders", true))
		},
		finalize: func(m *model, _ *buildContext) {
			// OpenRouter rejects configuration_update system messages on Opus 5.
			if m.API == ai.APIAnthropicMessages && supportsClaudeMidConvoEffort(m.ID) && m.ID != "anthropic/claude-opus-5" {
				enableClaudeMidConvoEffort(m)
			}
			// Mercury 2's instant mode (effort none) disables tool calling.
			if strings.HasPrefix(m.ID, "inception/mercury-2") {
				m.mergeThinking(levels("off", ""))
			}
			if m.ID == "z-ai/glm-5.2" {
				m.mergeThinking(levels("xhigh", "xhigh"))
			}
			// OpenAI models behind OpenRouter accept plain system text in place.
			if id, ok := strings.CutPrefix(m.ID, "openai/"); ok && m.API == ai.APIOpenAICompletions && slices.Contains(openAIMidConvoModels, id) {
				m.mergeCompat(compat("supportsMidConvoSystemMessages", true))
			}
		},
	}
}

// qwenInternationalBaseURL is Alibaba Cloud Model Studio Token Plan's
// international endpoint; China uses its own endpoint and keys.
const (
	qwenInternationalBaseURL = "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"
	qwenCNBaseURL            = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
)

// QwenCloud Token Plan Individual allowlist, verified 2026-11-01: each region's
// endpoint with a narrower catalog.
var qwenIndividualModelIDs = []string{
	"deepseek-v4-flash-0731", "deepseek-v4-pro", "deepseek-v4-pro-0813", "glm-5.2", "glm-5.3", "qwen3.6-flash",
	"qwen3.7-max", "qwen3.7-plus", "qwen3.8-flash", "qwen3.8-max",
}

func qwenTokenPlan(id, source, baseURL string, allowed []string) provider {
	return provider{
		id: id,
		models: func(ctx *buildContext) []*model {
			return ctx.modelsDevModels(source, devOptions{
				api: ai.APIOpenAICompletions, baseURL: baseURL, skipReasoning: true,
				// The retired preview id may outlive GA on models.dev.
				include: func(source sourceModel) bool {
					return source.ID != "qwen3.8-max-preview" && (allowed == nil || slices.Contains(allowed, source.ID))
				},
				mapModel: func(m *model, source sourceModel) *model {
					effort := effortThinkingLevels(source.ReasoningOptions)
					if effort == nil && (m.ID == "glm-5" || m.ID == "glm-5.1") {
						effort = thinking{ai.ModelThinkingMinimal: nil, ai.ModelThinkingLow: nil, ai.ModelThinkingMedium: nil,
							ai.ModelThinkingHigh: ptr("high"), ai.ModelThinkingXHigh: nil, ai.ModelThinkingMax: ptr("max")}
					}
					m.compat = compat("thinkingFormat", "qwen", "supportsDeveloperRole", false, "supportsStore", false, "supportsReasoningEffort", effort != nil)
					if effort != nil {
						m.setThinking(effort)
					}
					return m
				},
			})
		},
		completionsCompat: static(compat("supportsStrictMode", true)),
	}
}

func together() provider {
	base := compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false,
		"maxTokensField", "max_tokens", "supportsStrictMode", false, "supportsLongCacheRetention", false)
	toggle := spread(base, compat("thinkingFormat", "together"))
	return provider{
		id: "together",
		models: func(ctx *buildContext) []*model {
			return ctx.modelsDevModels("togetherai", devOptions{
				api: ai.APIOpenAICompletions, baseURL: "https://api.together.ai/v1", skipDeprecated: true,
				mapModel: func(m *model, _ sourceModel) *model {
					switch {
					case !m.Reasoning:
						m.compat = base
					case m.ID == "openai/gpt-oss-20b" || m.ID == "openai/gpt-oss-120b":
						m.compat = spread(base, compat("supportsReasoningEffort", true, "thinkingFormat", "openai"))
						m.setThinking(levels("off", "", "minimal", ""))
					case m.ID == "deepseek-ai/DeepSeek-V4-Pro-0813":
						m.compat = spread(toggle, compat("supportsReasoningEffort", true))
						m.setThinking(levels("minimal", "", "low", "", "medium", "", "high", "high", "xhigh", ""))
					default:
						m.compat = toggle
						m.setThinking(levels("minimal", "", "low", "", "medium", ""))
					}
					return m
				},
			})
		},
		completionsCompat: static(compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false,
			"maxTokensField", "max_tokens", "thinkingFormat", "together", "supportsLongCacheRetention", false)),
	}
}

// xiaomi is MiMo's API billing endpoint or a regional prepaid Token Plan
// endpoint. MiMo documents only thinking enabled/disabled, rejects some
// reasoning_effort values ("minimal") and treats all effort levels alike.
func xiaomi(id, baseURL string) provider {
	return provider{
		id: id,
		models: func(ctx *buildContext) []*model {
			return ctx.modelsDevModels(id, devOptions{
				api: ai.APIOpenAICompletions, baseURL: baseURL, skipDeprecated: true, skipReasoning: true,
				compat: compat("supportsReasoningEffort", false, "requiresReasoningContentOnAssistantMessages", true, "thinkingFormat", "deepseek"),
				mapModel: func(m *model, _ sourceModel) *model {
					if m.Reasoning {
						m.setThinking(levels("off", "off", "minimal", "", "low", "", "medium", "", "high", "high", "xhigh", "", "max", ""))
					}
					return m
				},
			})
		},
		completionsCompat: static(compat("supportsStrictMode", true)),
	}
}

// zai covers the Z.AI coding plans; the international and China plans list the same models.
func zai(id, source, baseURL string) provider {
	// Tool-call streaming is rejected by the GLM-4.5 generation.
	noToolStream := []string{"glm-4.5", "glm-4.5-air", "glm-4.5-flash", "glm-4.5v"}
	return provider{
		id: id,
		models: func(ctx *buildContext) []*model {
			// Coding-plan entries carry plan prices; show the pay-as-you-go API rates.
			apiModels := ctx.sourcesByID("zai")
			return ctx.modelsDevModels(source, devOptions{
				api: ai.APIOpenAICompletions, baseURL: baseURL,
				mapModel: func(m *model, source sourceModel) *model {
					effort := effortThinkingLevels(source.ReasoningOptions)
					if effort != nil && (m.ID == "glm-5.2" || m.ID == "glm-5.2-highspeed") {
						effort[ai.ModelThinkingOff] = ptr("none")
					}
					if effort != nil {
						m.setThinking(effort)
					}
					m.Cost = modelsDevCost(source.Cost)
					if api, ok := apiModels[m.ID]; ok && api.HasCost {
						m.Cost = modelsDevCost(api.Cost)
					}
					m.compat = compat("supportsDeveloperRole", false, "thinkingFormat", "zai")
					if effort != nil {
						m.mergeCompat(compat("supportsReasoningEffort", true))
					}
					if !slices.Contains(noToolStream, m.ID) {
						m.mergeCompat(compat("zaiToolStream", true))
					}
					return m
				},
			})
		},
		completionsCompat: static(compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false,
			"maxTokensField", "max_tokens", "thinkingFormat", "zai", "supportsStrictMode", true)),
	}
}
