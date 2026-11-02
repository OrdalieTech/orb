package cataloggen

import (
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/internal/lazyregexp"
)

// Facts about a model family that hold on every host (upstream catalog/families).

func containsAny(value string, needles ...string) bool {
	return slices.ContainsFunc(needles, func(needle string) bool { return strings.Contains(value, needle) })
}

// Anthropic Claude ids appear dashed (claude-opus-4-8), dotted (claude-opus-4.8),
// prefixed (anthropic/claude-opus-4.8) and dated (claude-opus-5-20260723).

var (
	claudePrefixPattern         = lazyregexp.New(`^~?anthropic/`)
	claudeMidConvoEffortPattern = lazyregexp.New(`^(?:claude-opus-(?:5|5[.-]5)|claude-(?:sonnet|haiku)-5[.-]5|claude-(?:fable|mythos)-5[.-]1)(?:-\d{8})?$`)
	claudeMidConvoSystemPattern = lazyregexp.New(`^(?:claude-opus-(?:4[.-]8|5(?:[.-]5)?)|claude-(?:sonnet|haiku)-5[.-]5|claude-(?:fable|mythos)-5(?:[.-]1)?)(?:-\d{8})?$`)
	claudeDatedSuffixPattern    = lazyregexp.New(`-\d{8}$`)
	claudeLatestAliasPattern    = lazyregexp.New(`^~anthropic/claude-[a-z]+-latest$`)
	gptMajorPattern             = lazyregexp.New(`^gpt-(\d+)`)
	kimiK3FamilyPattern         = lazyregexp.New(`^kimi-k3(?:\.\d+)?(?:-|$)`)
	gemma4Pattern               = lazyregexp.New(`gemma-?4`)
)

func stripClaudePrefix(id string) string {
	return claudePrefixPattern().ReplaceAllString(strings.ToLower(id), "")
}

// supportsClaudeMidConvoEffort reports models that accept per-message output_config effort changes.
func supportsClaudeMidConvoEffort(id string) bool {
	return claudeMidConvoEffortPattern().MatchString(stripClaudePrefix(id))
}

// supportsClaudeMidConvoSystemMessages reports models that accept system
// messages and tool changes after the conversation starts.
func supportsClaudeMidConvoSystemMessages(id string) bool {
	return claudeMidConvoSystemPattern().MatchString(id)
}

// enableClaudeMidConvoEffort enables per-message effort changes; models that
// cannot disable thinking with it lose "off". Opus 5.5, Sonnet 5.5, Fable 5.1
// and Mythos 5.1 reject thinking disabled.
func enableClaudeMidConvoEffort(m *model) {
	m.mergeCompat(compat("supportsMidConvoEffort", true))
	bare := claudeDatedSuffixPattern().ReplaceAllString(stripClaudePrefix(m.ID), "")
	if !slices.Contains([]string{"claude-opus-5", "claude-haiku-5-5", "claude-haiku-5.5"}, bare) {
		m.mergeThinking(levels("off", ""))
	}
}

// applyClaudeFamilyRules sets adaptive thinking effort levels: "max" on every
// adaptive Claude model, "xhigh" only on Opus 4.7/4.8/5, Sonnet 5, Haiku 5.5
// and Fable 5.
func applyClaudeFamilyRules(m *model) {
	if containsAny(m.ID, "opus-4-6", "opus-4.6", "sonnet-4-6", "sonnet-4.6") {
		m.mergeThinking(levels("max", "max"))
	}
	if containsAny(m.ID, "opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8", "opus-5", "opus.5", "sonnet-5", "sonnet.5", "haiku-5", "haiku.5") {
		m.mergeThinking(levels("xhigh", "xhigh", "max", "max"))
	}
	if strings.Contains(m.ID, "fable-5") {
		m.mergeThinking(levels("off", "", "xhigh", "xhigh", "max", "max"))
	}
	if m.API != ai.APIAnthropicMessages {
		return
	}
	if claudeLatestAliasPattern().MatchString(m.ID) || containsAny(m.ID,
		"opus-4-6", "opus-4.6", "opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8", "opus-5", "opus.5",
		"sonnet-4-6", "sonnet-4.6", "sonnet-5", "sonnet.5", "haiku-5", "haiku.5", "fable-5", "mythos-5") {
		m.mergeCompat(compat("forceAdaptiveThinking", true))
	}
	if containsAny(strings.ToLower(m.ID), "opus-4-7", "opus-4.7", "opus-4-8", "opus-4.8", "opus-5", "opus.5", "sonnet-5-5", "sonnet-5.5", "haiku-5-5", "haiku-5.5") {
		m.mergeCompat(compat("supportsTemperature", false))
	}
}

// claude55ThinkingLevels covers Opus, Sonnet and Haiku 5.5 before models.dev
// lists their effort metadata; only Haiku 5.5 accepts disabled thinking.
func claude55ThinkingLevels(id string) thinking {
	result := levels("minimal", "", "low", "low", "medium", "medium", "high", "high", "xhigh", "xhigh", "max", "max")
	if !strings.Contains(id, "haiku") {
		result[ai.ModelThinkingOff] = nil
	}
	return result
}

// OpenAI GPT models.

const openAILongContextThreshold = 272000

// withOpenAILongContextPricing bills prompts above 272K input tokens at 2x
// input and 1.5x output rates.
func withOpenAILongContextPricing(rates ai.ModelCostRates) ai.ModelCost {
	tiers := []ai.ModelCostTier{{InputTokensAbove: openAILongContextThreshold, ModelCostRates: ai.ModelCostRates{
		Input: roundCost(rates.Input * 2), Output: roundCost(rates.Output * 1.5),
		CacheRead: roundCost(rates.CacheRead * 2), CacheWrite: roundCost(rates.CacheWrite * 2),
	}}}
	return ai.ModelCost{ModelCostRates: rates, Tiers: &tiers}
}

// openAIStandardCosts are OpenAI's list prices, authoritative until models.dev
// and passthrough catalogs catch up (https://developers.openai.com/api/docs/pricing).
var openAIStandardCosts = map[string]ai.ModelCostRates{
	"gpt-5.6-luna":  {Input: .2, Output: 1.2, CacheRead: .02, CacheWrite: .25},
	"gpt-5.6-sol":   {Input: 4, Output: 20, CacheRead: .4, CacheWrite: 5},
	"gpt-5.6-terra": {Input: 2, Output: 12, CacheRead: .2, CacheWrite: 2.5},
	"gpt-6-astra":   {Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
	"gpt-6-luna":    {Input: .1, Output: .5, CacheRead: .01, CacheWrite: .125},
	"gpt-6-sol":     {Input: 2, Output: 10, CacheRead: .2, CacheWrite: 2.5},
	"gpt-6.1-sol":   {Input: 2, Output: 10, CacheRead: .1, CacheWrite: 2.5},
	"gpt-6.1-terra": {Input: 1, Output: 5, CacheRead: .05, CacheWrite: 1.25},
}

// openAIToolSearchModels accept tool search and additional_tools. GPT-5.5 and
// older namespace dynamically loaded tools and reject replayed calls without
// the namespace (#11415).
var openAIToolSearchModels = []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol", "gpt-6.1-terra"}

// openAIMidConvoModels accept mid-conversation developer messages.
var openAIMidConvoModels = append([]string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-pro", "gpt-5.5"}, openAIToolSearchModels...)

func isResponsesAPI(api ai.API) bool {
	return api == ai.APIOpenAIResponses || api == ai.APIAzureOpenAIResponses || api == ai.APIOpenAICodexResponses
}

func supportsOpenAIXHigh(id string) bool {
	return containsAny(id, "gpt-5.2", "gpt-5.3", "gpt-5.4", "gpt-5.5", "gpt-5.6", "gpt-6")
}

// applyOpenAIFamilyRules sets GPT thinking levels on any host.
func applyOpenAIFamilyRules(m *model) {
	if (m.API == ai.APIOpenAIResponses || m.API == ai.APIAzureOpenAIResponses) && strings.HasPrefix(m.ID, "gpt-5") {
		m.mergeThinking(levels("off", ""))
	}
	if slices.Contains([]string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol", "gpt-6.1-terra"}, m.ID) && isResponsesAPI(m.API) {
		// GPT-6 Astra and GPT-6.1 Sol reject reasoning.effort "none"; all reject "minimal".
		off := "none"
		if m.ID == "gpt-6-astra" || m.ID == "gpt-6.1-sol" {
			off = ""
		}
		m.mergeThinking(levels("off", off, "minimal", "", "low", "low", "medium", "medium", "high", "high", "xhigh", "xhigh", "max", "max"))
	}
	if supportsOpenAIXHigh(m.ID) {
		m.mergeThinking(levels("xhigh", "xhigh"))
	}
	if containsAny(m.ID, "gpt-5.6", "gpt-6") && (isResponsesAPI(m.API) || m.API == ai.APIOpenAICompletions) {
		m.mergeThinking(levels("max", "max"))
	}
	if strings.HasSuffix(m.ID, "gpt-5.5-pro") {
		m.mergeThinking(levels("off", "", "minimal", "", "low", ""))
	}
}

// enableOpenAIGrammarTools marks custom grammar tools on Responses endpoints;
// OpenAI rejects them before GPT-5.
func enableOpenAIGrammarTools(m *model) {
	if !isResponsesAPI(m.API) {
		return
	}
	if match := gptMajorPattern().FindStringSubmatch(m.ID); match != nil {
		if major, _ := strconv.Atoi(match[1]); major >= 5 {
			m.mergeCompat(compat("supportsOpenAIGrammarTools", true))
		}
	}
}

// enableOpenAIMidConvoSystemMessages marks newer Responses models that accept
// developer messages mid-conversation; proxies that pass them through also
// pass additional_tools through for the models that accept them.
func enableOpenAIMidConvoSystemMessages(m *model, proxy bool) {
	if m.API != ai.APIOpenAIResponses && m.API != ai.APIOpenAICodexResponses || !slices.Contains(openAIMidConvoModels, m.ID) {
		return
	}
	update := compat("supportsMidConvoSystemMessages", true)
	if proxy && slices.Contains(openAIToolSearchModels, m.ID) {
		update = append(update, jsonwire.OrderedMember{Name: "supportsAdditionalTools", Value: true})
	}
	m.mergeCompat(update)
}

// DeepSeek V4 on OpenAI-compatible hosts.

func deepSeekCompat() jsonwire.OrderedObject {
	return compat("requiresReasoningContentOnAssistantMessages", true, "thinkingFormat", "deepseek")
}

var (
	deepSeekV4Levels      = levels("minimal", "", "low", "", "medium", "", "high", "high", "max", "max")
	deepSeekV4FlashLevels = levels("minimal", "", "low", "low", "medium", "", "high", "high", "max", "max")
)

func isDeepSeekV4(m *model) bool {
	return m.API == ai.APIOpenAICompletions && strings.Contains(m.ID, "deepseek-v4")
}

// applyDeepSeekCompatRules replays DeepSeek V4 reasoning content on every
// host; Qwen Token Plan uses its own thinking format, and OpenRouter and
// OpenCode take native reasoning efforts.
func applyDeepSeekCompatRules(m *model) {
	if !isDeepSeekV4(m) || isQwenTokenPlan(string(m.Provider)) {
		return
	}
	if m.Provider == "openrouter" || m.Provider == "opencode" {
		m.mergeCompat(compat("requiresReasoningContentOnAssistantMessages", true))
		return
	}
	m.mergeCompat(deepSeekCompat())
}

// applyDeepSeekFamilyRules sets DeepSeek V4 effort levels when the host did
// not report its own; Azure Foundry rejects DeepSeek's max.
func applyDeepSeekFamilyRules(m *model) {
	if !isDeepSeekV4(m) || m.ThinkingLevelMap != nil {
		return
	}
	switch {
	case m.Provider == "openrouter":
		m.mergeThinking(deepSeekV4Levels)
		m.mergeThinking(levels("xhigh", "xhigh", "max", ""))
	case m.Provider == "azure":
		m.mergeThinking(levels("minimal", "", "low", "low", "medium", "medium", "high", "high", "xhigh", "", "max", ""))
	case (m.Provider == "deepseek" || m.Provider == "opencode" || m.Provider == "opencode-go") && strings.Contains(m.ID, "deepseek-v4-flash"):
		m.mergeThinking(deepSeekV4FlashLevels)
	default:
		m.mergeThinking(deepSeekV4Levels)
	}
}

func isQwenTokenPlan(provider string) bool {
	return provider == "qwen-token-plan" || provider == "qwen-token-plan-cn" || provider == "qwen-token-plan-individual"
}

// googleThinkingLevels takes Gemini effort levels from models.dev, with a Gemma 4 fallback.
func googleThinkingLevels(id string, options []reasoningOption) thinking {
	if effort := effortThinkingLevels(options); effort != nil {
		return effort
	}
	if gemma4Pattern().MatchString(strings.ToLower(id)) {
		return levels("off", "", "minimal", "MINIMAL", "low", "", "medium", "", "high", "HIGH")
	}
	return nil
}

// Moonshot Kimi models, shared by Moonshot, Kimi Coding and gateways.

// isKimiK3Family covers Kimi K3 and its point releases, which share K3's request format.
func isKimiK3Family(id string) bool { return kimiK3FamilyPattern().MatchString(id) }

const kimiK3MaxTokens = 131072

var kimiK3Cost = ai.ModelCostRates{Input: 3, Output: 15, CacheRead: .3}

func kimiCompat() jsonwire.OrderedObject {
	return compat("supportsStore", false, "supportsDeveloperRole", false, "supportsReasoningEffort", false,
		"maxTokensField", "max_tokens", "supportsStrictMode", false, "thinkingFormat", "deepseek")
}

// kimiK3Compat: K3-style models take reasoning_effort and need earlier reasoning sent back.
func kimiK3Compat() jsonwire.OrderedObject {
	return spread(kimiCompat(), compat("requiresReasoningContentOnAssistantMessages", true, "thinkingFormat", "openai", "supportsReasoningEffort", true))
}
