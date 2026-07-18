// Package cataloggen normalizes the models.dev api.json shape into pi models.
package cataloggen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
)

type sourceProvider struct {
	Models map[string]sourceModel `json:"models"`
}

type sourceModel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ToolCall   bool   `json:"tool_call"`
	Reasoning  bool   `json:"reasoning"`
	Status     string `json:"status"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
	Limit struct {
		Context float64 `json:"context"`
		Output  float64 `json:"output"`
	} `json:"limit"`
	Cost struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
		Tiers      []struct {
			Input, Output, CacheRead, CacheWrite float64
			Tier                                 struct {
				Type string  `json:"type"`
				Size float64 `json:"size"`
			} `json:"tier"`
		} `json:"tiers"`
	} `json:"cost"`
	Provider struct {
		NPM string `json:"npm"`
	} `json:"provider"`
}

func (model *sourceModel) UnmarshalJSON(data []byte) error {
	type rawModel sourceModel
	var value struct {
		rawModel
		Cost struct {
			Input      float64 `json:"input"`
			Output     float64 `json:"output"`
			CacheRead  float64 `json:"cache_read"`
			CacheWrite float64 `json:"cache_write"`
			Tiers      []struct {
				Input      float64 `json:"input"`
				Output     float64 `json:"output"`
				CacheRead  float64 `json:"cache_read"`
				CacheWrite float64 `json:"cache_write"`
				Tier       struct {
					Type string  `json:"type"`
					Size float64 `json:"size"`
				} `json:"tier"`
			} `json:"tiers"`
		} `json:"cost"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*model = sourceModel(value.rawModel)
	model.Cost.Input = value.Cost.Input
	model.Cost.Output = value.Cost.Output
	model.Cost.CacheRead = value.Cost.CacheRead
	model.Cost.CacheWrite = value.Cost.CacheWrite
	for _, tier := range value.Cost.Tiers {
		var normalized struct {
			Input, Output, CacheRead, CacheWrite float64
			Tier                                 struct {
				Type string  `json:"type"`
				Size float64 `json:"size"`
			} `json:"tier"`
		}
		normalized.Input, normalized.Output = tier.Input, tier.Output
		normalized.CacheRead, normalized.CacheWrite = tier.CacheRead, tier.CacheWrite
		normalized.Tier.Type, normalized.Tier.Size = tier.Tier.Type, tier.Tier.Size
		model.Cost.Tiers = append(model.Cost.Tiers, normalized)
	}
	return nil
}

type rule struct {
	source, provider string
	api              ai.API
	baseURL          string
}

var directRules = []rule{
	{"amazon-bedrock", "amazon-bedrock", ai.APIBedrockConverse, "https://bedrock-runtime.us-east-1.amazonaws.com"},
	{"anthropic", "anthropic", ai.APIAnthropicMessages, "https://api.anthropic.com"},
	{"cerebras", "cerebras", ai.APIOpenAICompletions, "https://api.cerebras.ai/v1"},
	{"cloudflare-workers-ai", "cloudflare-workers-ai", ai.APIOpenAICompletions, "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1"},
	{"deepseek", "deepseek", ai.APIOpenAICompletions, "https://api.deepseek.com"},
	{"fireworks-ai", "fireworks", ai.APIAnthropicMessages, "https://api.fireworks.ai/inference"},
	{"google", "google", ai.APIGoogleGenerativeAI, "https://generativelanguage.googleapis.com/v1beta"},
	{"google-vertex", "google-vertex", ai.APIGoogleVertex, "https://{location}-aiplatform.googleapis.com"},
	{"groq", "groq", ai.APIOpenAICompletions, "https://api.groq.com/openai/v1"},
	{"huggingface", "huggingface", ai.APIOpenAICompletions, "https://router.huggingface.co/v1"},
	{"kimi-for-coding", "kimi-coding", ai.APIAnthropicMessages, "https://api.kimi.com/coding"},
	{"minimax", "minimax", ai.APIAnthropicMessages, "https://api.minimax.io/anthropic"},
	{"minimax-cn", "minimax-cn", ai.APIAnthropicMessages, "https://api.minimaxi.com/anthropic"},
	{"mistral", "mistral", ai.APIMistralConversations, "https://api.mistral.ai"},
	{"moonshotai", "moonshotai", ai.APIOpenAICompletions, "https://api.moonshot.ai/v1"},
	{"moonshotai-cn", "moonshotai-cn", ai.APIOpenAICompletions, "https://api.moonshot.cn/v1"},
	{"nvidia", "nvidia", ai.APIOpenAICompletions, "https://integrate.api.nvidia.com/v1"},
	{"openai", "openai", ai.APIOpenAIResponses, "https://api.openai.com/v1"},
	{"openrouter", "openrouter", ai.APIOpenAICompletions, "https://openrouter.ai/api/v1"},
	{"togetherai", "together", ai.APIOpenAICompletions, "https://api.together.ai/v1"},
	{"vercel", "vercel-ai-gateway", ai.APIAnthropicMessages, "https://ai-gateway.vercel.sh"},
	{"xiaomi", "xiaomi", ai.APIOpenAICompletions, "https://api.xiaomimimo.com/v1"},
	{"xiaomi-token-plan-ams", "xiaomi-token-plan-ams", ai.APIOpenAICompletions, "https://token-plan-ams.xiaomimimo.com/v1"},
	{"xiaomi-token-plan-cn", "xiaomi-token-plan-cn", ai.APIOpenAICompletions, "https://token-plan-cn.xiaomimimo.com/v1"},
	{"xiaomi-token-plan-sgp", "xiaomi-token-plan-sgp", ai.APIOpenAICompletions, "https://token-plan-sgp.xiaomimimo.com/v1"},
	{"zai-coding-plan", "zai", ai.APIOpenAICompletions, "https://api.z.ai/api/coding/paas/v4"},
	{"zai-coding-plan", "zai-coding-cn", ai.APIOpenAICompletions, "https://open.bigmodel.cn/api/coding/paas/v4"},
}

// Generate converts a models.dev snapshot. Radius is deliberately absent.
func Generate(data []byte) (map[string]map[string]ai.Model, error) {
	var source map[string]sourceProvider
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, fmt.Errorf("parse models.dev api.json: %w", err)
	}
	result := make(map[string]map[string]ai.Model)
	for _, item := range directRules {
		addRule(result, source[item.source], item)
	}
	addCloudflareGateway(result, source["cloudflare-ai-gateway"])
	addOpenCode(result, source["opencode"], "opencode", "https://opencode.ai/zen")
	addOpenCode(result, source["opencode-go"], "opencode-go", "https://opencode.ai/zen/go")
	addCopilot(result, source["github-copilot"])
	addXAI(result, source["xai"])
	addCodex(result)
	addAntLing(result)
	addAzure(result)
	// TODO(WP-270): apply the remaining provider compat flags and pinned quirk metadata.
	return result, nil
}

// Render converts a models.dev snapshot into the checked-in Go source.
func Render(data []byte) ([]byte, error) {
	catalog, err := Generate(data)
	if err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(catalog)
	if err != nil {
		return nil, fmt.Errorf("marshal generated model catalog: %w", err)
	}
	var source bytes.Buffer
	source.WriteString("// Code generated by go generate; DO NOT EDIT.\n\npackage models\n\n")
	source.WriteString("var generatedCatalogJSON = []byte(")
	source.WriteString(strconv.Quote(string(normalized)))
	source.WriteString(")\n")
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated model catalog: %w", err)
	}
	return formatted, nil
}

func addRule(result map[string]map[string]ai.Model, source sourceProvider, item rule) {
	for _, key := range sortedKeys(source.Models) {
		raw := source.Models[key]
		if !include(item, key, raw) {
			continue
		}
		id, name := key, raw.Name
		if raw.ID != "" {
			id = raw.ID
		}
		if name == "" {
			name = id
		}
		if item.provider == "kimi-coding" && (id == "k2p5" || id == "k2p6") {
			if _, exists := source.Models["kimi-for-coding"]; exists {
				continue
			}
			id, name = "kimi-for-coding", "Kimi For Coding"
		}
		model := normalizedModel(id, name, raw, item.api, item.provider, item.baseURL)
		applyGeneratedMetadata(&model)
		upsert(result, model)
	}
}

func include(item rule, id string, model sourceModel) bool {
	if !model.ToolCall {
		return false
	}
	if item.provider == "together" && model.Status == "deprecated" {
		return false
	}
	if item.provider == "amazon-bedrock" && (strings.HasPrefix(id, "ai21.jamba") || strings.HasPrefix(id, "mistral.mistral-7b-instruct-v0")) {
		return false
	}
	if item.provider == "google-vertex" && (!strings.HasPrefix(id, "gemini-") || id == "gemini-3.1-flash-lite-preview") {
		return false
	}
	if (item.provider == "minimax" || item.provider == "minimax-cn") && !slices.Contains([]string{"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M3"}, id) {
		return false
	}
	return item.provider != "openai" || id != "gpt-5.6"
}

func normalizedModel(id, name string, raw sourceModel, api ai.API, provider, baseURL string) ai.Model {
	contextWindow, maxTokens := raw.Limit.Context, raw.Limit.Output
	if contextWindow == 0 {
		contextWindow = 4096
	}
	if maxTokens == 0 {
		maxTokens = 4096
	}
	input := ai.InputModalities{ai.InputText}
	if slices.Contains(raw.Modalities.Input, "image") {
		input = append(input, ai.InputImage)
	}
	tiers := make([]ai.ModelCostTier, 0, len(raw.Cost.Tiers))
	for _, tier := range raw.Cost.Tiers {
		if tier.Tier.Type == "context" && tier.Tier.Size != 0 {
			tiers = append(tiers, ai.ModelCostTier{InputTokensAbove: tier.Tier.Size, ModelCostRates: ai.ModelCostRates{Input: tier.Input, Output: tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite}})
		}
	}
	cost := ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: raw.Cost.Input, Output: raw.Cost.Output, CacheRead: raw.Cost.CacheRead, CacheWrite: raw.Cost.CacheWrite}}
	if len(tiers) > 0 {
		cost.Tiers = &tiers
	}
	return ai.Model{ID: id, Name: name, API: api, Provider: ai.ProviderID(provider), BaseURL: baseURL, Reasoning: raw.Reasoning, Input: input, Cost: cost, ContextWindow: contextWindow, MaxTokens: maxTokens}
}

func addCloudflareGateway(result map[string]map[string]ai.Model, source sourceProvider) {
	for _, key := range sortedKeys(source.Models) {
		raw := source.Models[key]
		if !raw.ToolCall {
			continue
		}
		upstream, id, found := strings.Cut(key, "/")
		if !found {
			continue
		}
		var api ai.API
		var baseURL string
		switch upstream {
		case "openai":
			api, baseURL = ai.APIOpenAIResponses, "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai"
		case "anthropic":
			api, baseURL = ai.APIAnthropicMessages, "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic"
		case "workers-ai":
			api, baseURL, id = ai.APIOpenAICompletions, "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat", key
		default:
			continue
		}
		model := normalizedModel(id, raw.Name, raw, api, "cloudflare-ai-gateway", baseURL)
		applyGeneratedMetadata(&model)
		upsert(result, model)
	}
}

func addOpenCode(result map[string]map[string]ai.Model, source sourceProvider, provider, basePath string) {
	for _, key := range sortedKeys(source.Models) {
		raw := source.Models[key]
		if !raw.ToolCall || raw.Status == "deprecated" || key == "gpt-5.3-codex-spark" {
			continue
		}
		api, baseURL := ai.APIOpenAICompletions, basePath+"/v1"
		switch raw.Provider.NPM {
		case "@ai-sdk/openai":
			api = ai.APIOpenAIResponses
		case "@ai-sdk/anthropic":
			api, baseURL = ai.APIAnthropicMessages, basePath
		case "@ai-sdk/google":
			api = ai.APIGoogleGenerativeAI
		}
		if provider == "opencode-go" && (key == "minimax-m2.7" || key == "qwen3.5-plus" || key == "qwen3.6-plus") {
			api, baseURL = ai.APIOpenAICompletions, basePath+"/v1"
		}
		model := normalizedModel(key, raw.Name, raw, api, provider, baseURL)
		applyGeneratedMetadata(&model)
		upsert(result, model)
	}
}

func addCopilot(result map[string]map[string]ai.Model, source sourceProvider) {
	for _, key := range sortedKeys(source.Models) {
		raw := source.Models[key]
		if !raw.ToolCall || raw.Status == "deprecated" {
			continue
		}
		api := ai.APIOpenAICompletions
		if strings.HasPrefix(key, "claude-") && (strings.Contains(key, "-4") || strings.Contains(key, "-5")) {
			api = ai.APIAnthropicMessages
		} else if strings.HasPrefix(key, "gpt-5") || strings.HasPrefix(key, "oswe") || strings.HasPrefix(key, "mai-") {
			api = ai.APIOpenAIResponses
		}
		model := normalizedModel(key, raw.Name, raw, api, "github-copilot", "https://api.individual.githubcopilot.com")
		headers := map[string]string{"User-Agent": "GitHubCopilotChat/0.35.0", "Editor-Version": "vscode/1.107.0", "Editor-Plugin-Version": "copilot-chat/0.35.0", "Copilot-Integration-Id": "vscode-chat"}
		model.Headers = &headers
		applyGeneratedMetadata(&model)
		upsert(result, model)
	}
}

func addXAI(result map[string]map[string]ai.Model, source sourceProvider) {
	excluded := map[string]bool{"grok-3": true, "grok-3-fast": true, "grok-4.20-0309-non-reasoning": true, "grok-4.20-0309-reasoning": true, "grok-code-fast-1": true}
	for _, key := range sortedKeys(source.Models) {
		raw := source.Models[key]
		if !raw.ToolCall || excluded[key] {
			continue
		}
		api := ai.APIOpenAICompletions
		if key == "grok-4.5" {
			api = ai.APIOpenAIResponses
		}
		model := normalizedModel(key, raw.Name, raw, api, "xai", "https://api.x.ai/v1")
		applyGeneratedMetadata(&model)
		upsert(result, model)
	}
}

func addCodex(result map[string]map[string]ai.Model) {
	items := []struct {
		id, name string
		context  float64
		input    ai.InputModalities
		cost     ai.ModelCostRates
	}{
		{"gpt-5.3-codex-spark", "GPT-5.3 Codex Spark", 128000, ai.InputModalities{ai.InputText}, ai.ModelCostRates{Input: 1.75, Output: 14, CacheRead: .175}},
		{"gpt-5.4", "GPT-5.4", 272000, ai.InputModalities{ai.InputText, ai.InputImage}, ai.ModelCostRates{Input: 2.5, Output: 15, CacheRead: .25}},
		{"gpt-5.4-mini", "GPT-5.4 mini", 272000, ai.InputModalities{ai.InputText, ai.InputImage}, ai.ModelCostRates{Input: .75, Output: 4.5, CacheRead: .075}},
		{"gpt-5.5", "GPT-5.5", 272000, ai.InputModalities{ai.InputText, ai.InputImage}, ai.ModelCostRates{Input: 5, Output: 30, CacheRead: .5}},
		{"gpt-5.6-luna", "GPT-5.6 Luna", 372000, ai.InputModalities{ai.InputText, ai.InputImage}, ai.ModelCostRates{Input: 1, Output: 6, CacheRead: .1, CacheWrite: 1.25}},
		{"gpt-5.6-sol", "GPT-5.6 Sol", 372000, ai.InputModalities{ai.InputText, ai.InputImage}, ai.ModelCostRates{Input: 5, Output: 30, CacheRead: .5, CacheWrite: 6.25}},
		{"gpt-5.6-terra", "GPT-5.6 Terra", 372000, ai.InputModalities{ai.InputText, ai.InputImage}, ai.ModelCostRates{Input: 2.5, Output: 15, CacheRead: .25, CacheWrite: 3.125}},
	}
	for _, item := range items {
		model := ai.Model{ID: item.id, Name: item.name, API: ai.APIOpenAICodexResponses, Provider: "openai-codex", BaseURL: "https://chatgpt.com/backend-api", Reasoning: true, Input: item.input, Cost: ai.ModelCost{ModelCostRates: item.cost}, ContextWindow: item.context, MaxTokens: 128000}
		applyGeneratedMetadata(&model)
		upsert(result, model)
	}
}

func addAntLing(result map[string]map[string]ai.Model) {
	for _, model := range []ai.Model{
		{ID: "Ling-2.6-flash", Name: "Ling 2.6 Flash", Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: .01, Output: .02}}},
		{ID: "Ling-2.6-1T", Name: "Ling 2.6 1T", Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: .06, Output: .25}}},
		{ID: "Ring-2.6-1T", Name: "Ring 2.6 1T", Reasoning: true, Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: .06, Output: .25}}},
	} {
		model.API, model.Provider, model.BaseURL = ai.APIOpenAICompletions, "ant-ling", "https://api.ant-ling.com/v1"
		model.Input, model.ContextWindow, model.MaxTokens = ai.InputModalities{ai.InputText}, 262144, 65536
		upsert(result, model)
	}
}

func addAzure(result map[string]map[string]ai.Model) {
	for _, model := range result["openai"] {
		clone := model
		clone.API, clone.Provider, clone.BaseURL = ai.APIAzureOpenAIResponses, "azure-openai-responses", ""
		if slices.Contains([]string{"gpt-5.4", "gpt-5.5", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"}, clone.ID) {
			clone.ContextWindow = 1050000
		}
		upsert(result, clone)
	}
}

func applyGeneratedMetadata(model *ai.Model) {
	if model.Name == "" {
		model.Name = model.ID
	}
	if model.API == ai.APIOpenAIResponses && strings.HasPrefix(model.ID, "gpt-5") {
		mapping := map[ai.ModelThinkingLevel]*string{ai.ModelThinkingOff: nil}
		if strings.Contains(model.ID, "5.2") || strings.Contains(model.ID, "5.3") || strings.Contains(model.ID, "5.4") || strings.Contains(model.ID, "5.5") || strings.Contains(model.ID, "5.6") {
			value := "xhigh"
			mapping[ai.ModelThinkingXHigh] = &value
		}
		if strings.Contains(model.ID, "5.6") {
			value := "max"
			mapping[ai.ModelThinkingMax] = &value
		}
		model.ThinkingLevelMap = &mapping
	}
	if model.Provider == "amazon-bedrock" && strings.HasPrefix(model.ID, "eu.") {
		model.BaseURL = "https://bedrock-runtime.eu-central-1.amazonaws.com"
	}
}

func upsert(result map[string]map[string]ai.Model, model ai.Model) {
	provider := string(model.Provider)
	if result[provider] == nil {
		result[provider] = make(map[string]ai.Model)
	}
	if _, exists := result[provider][model.ID]; !exists {
		result[provider][model.ID] = model
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
