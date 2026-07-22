// Package cataloggen normalizes the models.dev api.json shape into pi models.
package cataloggen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

// Sources aggregates the generator inputs. ModelsDev is required. NvidiaNIM,
// OpenRouter, and Vercel are the raw listings from the live provider APIs;
// when absent, NVIDIA falls back to the curated snapshot below and the
// OpenRouter and Vercel catalogs are omitted entirely (the bundled catalog
// keeps serving them).
type Sources struct {
	ModelsDev  []byte // models.dev api.json snapshot
	NvidiaNIM  []byte // NVIDIA NIM /v1/models listing
	OpenRouter []byte // OpenRouter /api/v1/models listing
	Vercel     []byte // Vercel AI Gateway /v1/models listing
	// GeneratedAt stamps the rendered catalog so stale store overlays lose to
	// a newer bundled catalog (upstream remote-catalog-provider.ts).
	GeneratedAt time.Time
}

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
		Input  []string `json:"input"`
		Output []string `json:"output"`
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
	{"alibaba-token-plan", "qwen-token-plan", ai.APIOpenAICompletions, "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"},
	{"alibaba-token-plan-cn", "qwen-token-plan-cn", ai.APIOpenAICompletions, "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"},
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
	{"openai", "openai", ai.APIOpenAIResponses, "https://api.openai.com/v1"},
	{"togetherai", "together", ai.APIOpenAICompletions, "https://api.together.ai/v1"},
	{"xiaomi", "xiaomi", ai.APIOpenAICompletions, "https://api.xiaomimimo.com/v1"},
	{"xiaomi-token-plan-ams", "xiaomi-token-plan-ams", ai.APIOpenAICompletions, "https://token-plan-ams.xiaomimimo.com/v1"},
	{"xiaomi-token-plan-cn", "xiaomi-token-plan-cn", ai.APIOpenAICompletions, "https://token-plan-cn.xiaomimimo.com/v1"},
	{"xiaomi-token-plan-sgp", "xiaomi-token-plan-sgp", ai.APIOpenAICompletions, "https://token-plan-sgp.xiaomimimo.com/v1"},
	{"zai-coding-plan", "zai", ai.APIOpenAICompletions, "https://api.z.ai/api/coding/paas/v4"},
	{"zai-coding-plan", "zai-coding-cn", ai.APIOpenAICompletions, "https://open.bigmodel.cn/api/coding/paas/v4"},
}

// Generate converts the aggregated source listings. Radius is deliberately absent.
func Generate(sources Sources) (map[string]map[string]ai.Model, error) {
	var source map[string]sourceProvider
	if err := json.Unmarshal(sources.ModelsDev, &source); err != nil {
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
	if err := addNvidia(result, source["nvidia"], sources.NvidiaNIM); err != nil {
		return nil, err
	}
	if err := addOpenRouter(result, sources.OpenRouter); err != nil {
		return nil, err
	}
	if err := addVercelGateway(result, sources.Vercel); err != nil {
		return nil, err
	}
	addCodex(result)
	addAntLing(result)
	addMissingOpenAI(result)
	addQwenTokenPlanPreview(result)
	addAzure(result)
	addProviderAliases(result)
	for _, models := range result {
		for id, model := range models {
			applyGeneratedMetadata(&model)
			models[id] = model
		}
	}
	return result, nil
}

// Render converts the aggregated source listings into the checked-in Go source.
func Render(sources Sources) ([]byte, error) {
	if sources.GeneratedAt.IsZero() {
		return nil, fmt.Errorf("render model catalog: GeneratedAt is required")
	}
	catalog, err := Generate(sources)
	if err != nil {
		return nil, err
	}
	if err := validateCatalog(catalog); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(catalog)
	if err != nil {
		return nil, fmt.Errorf("marshal generated model catalog: %w", err)
	}
	var source bytes.Buffer
	source.WriteString("// Code generated by go generate; DO NOT EDIT.\n\npackage models\n\n")
	source.WriteString("// generatedCatalogLastModified is the UnixMilli catalog build time; models-store\n")
	source.WriteString("// overlays that are not newer lose to the bundled catalog.\n")
	fmt.Fprintf(&source, "const generatedCatalogLastModified int64 = %d\n\n", sources.GeneratedAt.UnixMilli())
	source.WriteString("var generatedCatalogJSON = []byte(")
	source.WriteString(strconv.Quote(string(normalized)))
	source.WriteString(")\n")
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated model catalog: %w", err)
	}
	return formatted, nil
}

// validateCatalog mirrors upstream model-data.ts before generated output can
// replace the checked-in catalog.
func validateCatalog(catalog map[string]map[string]ai.Model) error {
	validAPIs := map[ai.API]bool{
		ai.APIOpenAICompletions: true, ai.APIMistralConversations: true, ai.APIOpenAIResponses: true,
		ai.APIAzureOpenAIResponses: true, ai.APIOpenAICodexResponses: true, ai.APIAnthropicMessages: true,
		ai.APIBedrockConverse: true, ai.APIGoogleGenerativeAI: true, ai.APIGoogleVertex: true, ai.APIPiMessages: true,
	}
	for _, provider := range sortedKeys(catalog) {
		models := catalog[provider]
		for _, id := range sortedKeys(models) {
			model := models[id]
			if model.ID != id {
				return fmt.Errorf("validate model catalog: %s/%s declares id %q", provider, id, model.ID)
			}
			if string(model.Provider) != provider {
				return fmt.Errorf("validate model catalog: %s/%s declares provider %q", provider, id, model.Provider)
			}
			if !validAPIs[model.API] {
				return fmt.Errorf("validate model catalog: %s/%s declares unknown api %q", provider, id, model.API)
			}
			if model.Name == "" {
				return fmt.Errorf("validate model catalog: %s/%s has no model name", provider, id)
			}
			if len(model.Input) == 0 {
				return fmt.Errorf("validate model catalog: %s/%s has invalid input modalities", provider, id)
			}
			for _, modality := range model.Input {
				if modality != ai.InputText && modality != ai.InputImage {
					return fmt.Errorf("validate model catalog: %s/%s has invalid input modality %q", provider, id, modality)
				}
			}
			if math.IsNaN(model.ContextWindow) || math.IsInf(model.ContextWindow, 0) || model.ContextWindow <= 0 {
				return fmt.Errorf("validate model catalog: %s/%s has invalid contextWindow", provider, id)
			}
			if math.IsNaN(model.MaxTokens) || math.IsInf(model.MaxTokens, 0) || model.MaxTokens <= 0 {
				return fmt.Errorf("validate model catalog: %s/%s has invalid maxTokens", provider, id)
			}
			for name, cost := range map[string]float64{
				"input": model.Cost.Input, "output": model.Cost.Output,
				"cacheRead": model.Cost.CacheRead, "cacheWrite": model.Cost.CacheWrite,
			} {
				if math.IsNaN(cost) || math.IsInf(cost, 0) {
					return fmt.Errorf("validate model catalog: %s/%s has invalid cost.%s", provider, id, name)
				}
			}
		}
	}
	return nil
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
		if item.provider == "kimi-coding" && (id == "k2p5" || id == "k2p6" || id == "k2p7") {
			if _, exists := source.Models["kimi-for-coding"]; exists {
				continue
			}
			id, name = "kimi-for-coding", "Kimi For Coding"
		}
		model := normalizedModel(id, name, raw, item.api, item.provider, item.baseURL)
		if item.provider == "fireworks" && strings.Contains(id, "glm-5p2") {
			model.API = ai.APIOpenAICompletions
			model.BaseURL = "https://api.fireworks.ai/inference/v1"
		}
		if item.provider == "mistral" && model.Cost.CacheRead == 0 && model.Cost.Input != 0 {
			model.Cost.CacheRead = roundCost(model.Cost.Input * 0.1)
		}
		if item.provider == "google-vertex" && id == "gemini-2.5-flash" {
			model.Cost.CacheRead = 0.03
			model.Cost.CacheWrite = 0
		}
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
	if item.provider == "deepseek" && !strings.Contains(id, "deepseek-v4") {
		return false
	}
	if item.provider == "together" && slices.Contains([]string{
		"Qwen/Qwen3-235B-A22B-Instruct-2507-tput", "Qwen/Qwen3.5-397B-A17B", "essentialai/Rnj-1-Instruct", "zai-org/GLM-5", "zai-org/GLM-5.1",
	}, id) {
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
	cost := ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: raw.Cost.Input, Output: raw.Cost.Output, CacheRead: raw.Cost.CacheRead, CacheWrite: raw.Cost.CacheWrite}}
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
		upsert(result, model)
	}
}

func addOpenCode(result map[string]map[string]ai.Model, source sourceProvider, provider, basePath string) {
	for _, key := range sortedKeys(source.Models) {
		raw := source.Models[key]
		// hy3-free was dropped upstream (890b3547) but the pinned models.dev
		// snapshot still lists it.
		if !raw.ToolCall || raw.Status == "deprecated" || key == "gpt-5.3-codex-spark" || key == "hy3-free" {
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
		case "@ai-sdk/alibaba":
			api = ai.APIOpenAICompletions
		}
		if provider == "opencode-go" && (key == "minimax-m2.7" || key == "qwen3.5-plus" || key == "qwen3.6-plus") {
			api, baseURL = ai.APIOpenAICompletions, basePath+"/v1"
		}
		if (provider == "opencode" || provider == "opencode-go") && key == "grok-4.5" {
			api = ai.APIOpenAIResponses
		}
		model := normalizedModel(key, raw.Name, raw, api, provider, baseURL)
		if raw.Provider.NPM == "@ai-sdk/alibaba" {
			model.Compat = mustCompatJSON(ai.OpenAICompletionsCompat{CacheControlFormat: ptr(ai.CacheControlAnthropic)})
		}
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
		if isCopilotClaude(key) {
			api = ai.APIAnthropicMessages
		} else if strings.HasPrefix(key, "gpt-5") || strings.HasPrefix(key, "oswe") || strings.HasPrefix(key, "mai-") {
			api = ai.APIOpenAIResponses
		}
		model := normalizedModel(key, raw.Name, raw, api, "github-copilot", "https://api.individual.githubcopilot.com")
		model.Cost = modelCostWithTiers(raw)
		headers := map[string]string{"User-Agent": "GitHubCopilotChat/0.35.0", "Editor-Version": "vscode/1.107.0", "Editor-Plugin-Version": "copilot-chat/0.35.0", "Copilot-Integration-Id": "vscode-chat"}
		model.Headers = &headers
		upsert(result, model)
	}
}

func isCopilotClaude(id string) bool {
	for _, family := range []string{"claude-haiku-", "claude-sonnet-", "claude-opus-"} {
		if tail, ok := strings.CutPrefix(id, family); ok {
			return strings.HasPrefix(tail, "4") || strings.HasPrefix(tail, "5")
		}
	}
	return false
}

func modelCostWithTiers(raw sourceModel) ai.ModelCost {
	cost := ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: raw.Cost.Input, Output: raw.Cost.Output, CacheRead: raw.Cost.CacheRead, CacheWrite: raw.Cost.CacheWrite}}
	tiers := make([]ai.ModelCostTier, 0, len(raw.Cost.Tiers))
	for _, tier := range raw.Cost.Tiers {
		if tier.Tier.Type == "context" && tier.Tier.Size != 0 {
			tiers = append(tiers, ai.ModelCostTier{InputTokensAbove: tier.Tier.Size, ModelCostRates: ai.ModelCostRates{Input: tier.Input, Output: tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite}})
		}
	}
	if len(tiers) != 0 {
		cost.Tiers = &tiers
	}
	return cost
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
		upsert(result, model)
	}
}

const nvidiaBaseURL = "https://integrate.api.nvidia.com/v1"

// nvidiaNIMUnsupportedModels mirrors upstream NVIDIA_NIM_UNSUPPORTED_MODELS
// (generate-models.ts:195-214) and is matched against the live NIM listing id.
var nvidiaNIMUnsupportedModels = []string{
	"abacusai/dracarys-llama-3.1-70b-instruct", "bytedance/seed-oss-36b-instruct",
	"deepseek-ai/deepseek-v4-flash", "deepseek-ai/deepseek-v4-pro", "google/gemma-2-2b-it",
	"google/gemma-3n-e2b-it", "google/gemma-3n-e4b-it", "google/gemma-4-31b-it",
	"meta/llama-3.2-1b-instruct", "meta/llama-4-maverick-17b-128e-instruct",
	"microsoft/phi-4-mini-instruct", "minimaxai/minimax-m2.7", "mistralai/mistral-nemotron",
	"nvidia/nemotron-mini-4b-instruct", "qwen/qwen3-next-80b-a3b-instruct",
	"qwen/qwen3.5-397b-a17b", "sarvamai/sarvam-m", "upstage/solar-10.7b-instruct",
	// pigo-only entry: not in the upstream denylist and currently absent from
	// the live NIM listing; kept so a NIM rollout cannot resurrect it silently.
	"qwen/qwen3.5-122b-a10b",
}

// addNvidia intersects models.dev nvidia with the live NIM /v1/models listing,
// normalizing IDs before the denylist check (generate-models.ts:764-766,
// 1414-1446). Without the intersection models.dev leaks models NIM never serves.
func addNvidia(result map[string]map[string]ai.Model, source sourceProvider, listing []byte) error {
	liveIDs, err := nvidiaLiveIDs(listing)
	if err != nil {
		return err
	}
	for _, key := range sortedKeys(source.Models) {
		raw := source.Models[key]
		if !raw.ToolCall || !slices.Contains(raw.Modalities.Input, "text") || !slices.Contains(raw.Modalities.Output, "text") {
			continue
		}
		liveID, ok := liveIDs[key]
		if !ok {
			liveID, ok = liveIDs[normalizeNvidiaModelID(key)]
		}
		if !ok || slices.Contains(nvidiaNIMUnsupportedModels, liveID) {
			continue
		}
		name := raw.Name
		if name == "" {
			name = liveID
		}
		upsert(result, normalizedModel(liveID, name, raw, ai.APIOpenAICompletions, "nvidia", nvidiaBaseURL))
	}
	return nil
}

func nvidiaLiveIDs(listing []byte) (map[string]string, error) {
	if len(listing) == 0 {
		return map[string]string{}, nil
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listing, &parsed); err != nil {
		return nil, fmt.Errorf("parse NVIDIA NIM models listing: %w", err)
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, model := range parsed.Data {
		ids = append(ids, model.ID)
	}
	result := make(map[string]string, 2*len(ids))
	for _, id := range ids {
		result[id] = id
		result[normalizeNvidiaModelID(id)] = id
	}
	return result, nil
}

func normalizeNvidiaModelID(id string) string {
	return strings.ReplaceAll(strings.ToLower(id), "_", ".")
}

// addOpenRouter builds the OpenRouter catalog from the live /api/v1/models
// listing filtered on tools support (generate-models.ts:818-878). When the
// listing is absent the catalog is omitted and the bundled one keeps serving.
func addOpenRouter(result map[string]map[string]ai.Model, listing []byte) error {
	if len(listing) == 0 {
		return nil
	}
	var parsed struct {
		Data []struct {
			ID                  string   `json:"id"`
			Name                string   `json:"name"`
			SupportedParameters []string `json:"supported_parameters"`
			ContextLength       float64  `json:"context_length"`
			Architecture        struct {
				Modality string `json:"modality"`
			} `json:"architecture"`
			Pricing struct {
				Prompt          string `json:"prompt"`
				Completion      string `json:"completion"`
				InputCacheRead  string `json:"input_cache_read"`
				InputCacheWrite string `json:"input_cache_write"`
			} `json:"pricing"`
			TopProvider struct {
				ContextLength       float64 `json:"context_length"`
				MaxCompletionTokens float64 `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listing, &parsed); err != nil {
		return fmt.Errorf("parse OpenRouter models listing: %w", err)
	}
	for _, model := range parsed.Data {
		if !slices.Contains(model.SupportedParameters, "tools") {
			continue
		}
		input := ai.InputModalities{ai.InputText}
		if strings.Contains(model.Architecture.Modality, "image") {
			input = append(input, ai.InputImage)
		}
		contextWindow := model.TopProvider.ContextLength
		if contextWindow == 0 {
			contextWindow = model.ContextLength
		}
		if contextWindow == 0 {
			contextWindow = 4096
		}
		maxTokens := model.TopProvider.MaxCompletionTokens
		if maxTokens == 0 {
			maxTokens = 4096
		}
		upsert(result, ai.Model{
			ID: model.ID, Name: model.Name, API: ai.APIOpenAICompletions, Provider: "openrouter",
			BaseURL:   "https://openrouter.ai/api/v1",
			Reasoning: slices.Contains(model.SupportedParameters, "reasoning"),
			Input:     input,
			Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{
				Input: perMillionTokens(model.Pricing.Prompt), Output: perMillionTokens(model.Pricing.Completion),
				CacheRead: perMillionTokens(model.Pricing.InputCacheRead), CacheWrite: perMillionTokens(model.Pricing.InputCacheWrite),
			}},
			ContextWindow: contextWindow, MaxTokens: maxTokens,
		})
	}
	return nil
}

// addVercelGateway builds the Vercel AI Gateway catalog from the live
// /v1/models listing filtered on the tool-use tag; every model rides the
// anthropic-messages API (generate-models.ts:880-938). When the listing is
// absent the catalog is omitted and the bundled one keeps serving.
func addVercelGateway(result map[string]map[string]ai.Model, listing []byte) error {
	if len(listing) == 0 {
		return nil
	}
	var parsed struct {
		Data []struct {
			ID            string   `json:"id"`
			Name          string   `json:"name"`
			Tags          []string `json:"tags"`
			ContextWindow float64  `json:"context_window"`
			MaxTokens     float64  `json:"max_tokens"`
			Pricing       struct {
				Input           flexibleNumber `json:"input"`
				Output          flexibleNumber `json:"output"`
				InputCacheRead  flexibleNumber `json:"input_cache_read"`
				InputCacheWrite flexibleNumber `json:"input_cache_write"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listing, &parsed); err != nil {
		return fmt.Errorf("parse Vercel AI Gateway models listing: %w", err)
	}
	for _, model := range parsed.Data {
		if !slices.Contains(model.Tags, "tool-use") {
			continue
		}
		input := ai.InputModalities{ai.InputText}
		if slices.Contains(model.Tags, "vision") {
			input = append(input, ai.InputImage)
		}
		name := model.Name
		if name == "" {
			name = model.ID
		}
		contextWindow := model.ContextWindow
		if contextWindow == 0 {
			contextWindow = 4096
		}
		maxTokens := model.MaxTokens
		if maxTokens == 0 {
			maxTokens = 4096
		}
		upsert(result, ai.Model{
			ID: model.ID, Name: name, API: ai.APIAnthropicMessages, Provider: "vercel-ai-gateway",
			BaseURL:   "https://ai-gateway.vercel.sh",
			Reasoning: slices.Contains(model.Tags, "reasoning"),
			Input:     input,
			Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{
				Input: roundCost(float64(model.Pricing.Input) * 1000000), Output: roundCost(float64(model.Pricing.Output) * 1000000),
				CacheRead: roundCost(float64(model.Pricing.InputCacheRead) * 1000000), CacheWrite: roundCost(float64(model.Pricing.InputCacheWrite) * 1000000),
			}},
			ContextWindow: contextWindow, MaxTokens: maxTokens,
		})
	}
	return nil
}

// perMillionTokens converts an OpenRouter $/token price string to $/1M tokens.
func perMillionTokens(value string) float64 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return roundCost(parsed * 1000000)
}

// flexibleNumber decodes a JSON number or numeric string; anything else is 0
// (upstream toNumber in generate-models.ts:888-894).
type flexibleNumber float64

func (number *flexibleNumber) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*number = 0
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			*number = 0
			return nil
		}
		*number = flexibleNumber(parsed)
		return nil
	}
	var parsed float64
	if err := json.Unmarshal(data, &parsed); err != nil {
		*number = 0
		return nil
	}
	*number = flexibleNumber(parsed)
	return nil
}

// addQwenTokenPlanPreview mirrors the upstream hardcoded qwen3.8-max-preview
// injection for both Qwen Token Plan providers until models.dev includes it
// (generate-models.ts:2281-2303). Compat (thinkingFormat qwen,
// supportsDeveloperRole false, supportsStore false) comes from the shared
// qwen-token-plan metadata pass.
func addQwenTokenPlanPreview(result map[string]map[string]ai.Model) {
	baseURLs := map[string]string{
		"qwen-token-plan":    "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1",
		"qwen-token-plan-cn": "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
	}
	for _, provider := range []string{"qwen-token-plan", "qwen-token-plan-cn"} {
		if _, exists := result[provider]["qwen3.8-max-preview"]; exists {
			continue
		}
		upsert(result, ai.Model{
			ID: "qwen3.8-max-preview", Name: "Qwen3.8 Max Preview", API: ai.APIOpenAICompletions,
			Provider: ai.ProviderID(provider), BaseURL: baseURLs[provider], Reasoning: true,
			Input: ai.InputModalities{ai.InputText, ai.InputImage},
			Cost:  ai.ModelCost{}, ContextWindow: 1000000, MaxTokens: 65536,
		})
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
		{"gpt-5.6-luna", "GPT-5.6 Luna", 272000, ai.InputModalities{ai.InputText, ai.InputImage}, ai.ModelCostRates{Input: 1, Output: 6, CacheRead: .1, CacheWrite: 1.25}},
		{"gpt-5.6-sol", "GPT-5.6 Sol", 272000, ai.InputModalities{ai.InputText, ai.InputImage}, ai.ModelCostRates{Input: 5, Output: 30, CacheRead: .5, CacheWrite: 6.25}},
		{"gpt-5.6-terra", "GPT-5.6 Terra", 272000, ai.InputModalities{ai.InputText, ai.InputImage}, ai.ModelCostRates{Input: 2.5, Output: 15, CacheRead: .25, CacheWrite: 3.125}},
	}
	for _, item := range items {
		model := ai.Model{ID: item.id, Name: item.name, API: ai.APIOpenAICodexResponses, Provider: "openai-codex", BaseURL: "https://chatgpt.com/backend-api", Reasoning: true, Input: item.input, Cost: ai.ModelCost{ModelCostRates: item.cost}, ContextWindow: item.context, MaxTokens: 128000}
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
		clone.Compat = nil
		clone.Cost.Tiers = nil
		if slices.Contains([]string{"gpt-5.4", "gpt-5.5", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra"}, clone.ID) {
			clone.ContextWindow = 1050000
		}
		upsert(result, clone)
	}
}

func addMissingOpenAI(result map[string]map[string]ai.Model) {
	upsert(result, ai.Model{
		ID: "gpt-5-chat-latest", Name: "GPT-5 Chat Latest", API: ai.APIOpenAIResponses,
		Provider: "openai", BaseURL: "https://api.openai.com/v1", Input: ai.InputModalities{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1.25, Output: 10, CacheRead: .125}},
		ContextWindow: 128000, MaxTokens: 16384,
	})
}

func applyGeneratedMetadata(model *ai.Model) {
	applyCatalogMetadata(model)
}

func addProviderAliases(result map[string]map[string]ai.Model) {
	if _, ok := result["openrouter"]["auto"]; !ok {
		upsert(result, ai.Model{ID: "auto", Name: "Auto", API: ai.APIOpenAICompletions, Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage}, Cost: ai.ModelCost{}, ContextWindow: 2000000, MaxTokens: 30000})
	}
	if _, ok := result["openrouter"]["openrouter/fusion"]; !ok {
		upsert(result, ai.Model{ID: "openrouter/fusion", Name: "OpenRouter: Fusion", API: ai.APIOpenAICompletions, Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Reasoning: true, Input: ai.InputModalities{ai.InputText}, Cost: ai.ModelCost{}, ContextWindow: 1000000, MaxTokens: 30000})
	}
	if _, ok := result["mistral"]["mistral-medium-3.5"]; !ok {
		upsert(result, ai.Model{ID: "mistral-medium-3.5", Name: "Mistral Medium 3.5", API: ai.APIMistralConversations, Provider: "mistral", BaseURL: "https://api.mistral.ai", Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage}, Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1.5, Output: 7.5}}, ContextWindow: 262144, MaxTokens: 262144})
	}
}

// roundCost matches upstream Number(value.toFixed(6)): half away from zero so
// negative sentinel prices (e.g. OpenRouter auto routers) survive intact.
func roundCost(value float64) float64 {
	return math.Round(value*1000000) / 1000000
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
