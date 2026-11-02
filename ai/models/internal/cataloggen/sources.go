package cataloggen

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// reasoningOption is upstream ModelsDevReasoningOption; effort Values keep
// JSON nulls as nil.
type reasoningOption struct {
	Type   string
	Values []*string
}

// sourceCost is upstream ModelsDevCost: rates models.dev does not list are nil.
type sourceCost struct {
	Input, Output, CacheRead, CacheWrite *float64
	Tiers                                []sourceTier
}

type sourceTier struct {
	Above                                float64
	Input, Output, CacheRead, CacheWrite *float64
}

type sourceAPI struct{ Protocol, BaseURL, AISDK string }

// sourceModel is a models.dev v2 chat model normalized like upstream
// normalizeModelsDevModel (catalog/sources/models-dev.ts).
type sourceModel struct {
	ID, Name, Status                      string
	ToolCall, StructuredOutput, Reasoning bool
	ReasoningOptions                      []reasoningOption // nil when the model does not reason
	ImageInput, TextInput, TextOutput     bool
	ContextWindow, MaxTokens              float64
	Cost                                  sourceCost
	HasCost                               bool // the v2 entry lists a cost object
	APIs                                  []sourceAPI
}

type v2Price struct {
	Unit      *string           `json:"unit"`
	Price     float64           `json:"price"`
	Cache     *v2Cache          `json:"cache"`
	Overrides []json.RawMessage `json:"overrides"`
}

type v2Cache struct {
	Read     *float64        `json:"read"`
	Write    json.RawMessage `json:"write"`
	Explicit *struct {
		Read  *float64        `json:"read"`
		Write json.RawMessage `json:"write"`
	} `json:"explicit"`
}

type v2Model struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Capabilities struct {
		Tools struct {
			Supported bool `json:"supported"`
		} `json:"tools"`
		Reasoning struct {
			Supported bool       `json:"supported"`
			Toggle    bool       `json:"toggle"`
			Effort    *[]*string `json:"effort"`
			Budget    *struct{}  `json:"budget"`
		} `json:"reasoning"`
		StructuredOutput bool `json:"structured_output"`
	} `json:"capabilities"`
	Limit struct {
		Context float64 `json:"context"`
		Output  float64 `json:"output"`
	} `json:"limit"`
	Cost *struct {
		Text *struct {
			Input  *v2Price `json:"input"`
			Output *v2Price `json:"output"`
		} `json:"text"`
	} `json:"cost"`
	API json.RawMessage `json:"api"`
}

// parseModelsDev reads the models.dev v2 catalog into each provider's chat
// models, in listing order: the first model listing an id wins downstream.
func parseModelsDev(data []byte) (map[string][]sourceModel, error) {
	if !jsonwire.Valid(data) {
		return nil, fmt.Errorf("parse models.dev catalog: invalid JSON")
	}
	result := make(map[string][]sourceModel)
	var failure error
	jsonwire.EachMember(data, func(provider, value []byte) bool {
		var entry struct {
			Models json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(value, &entry); err != nil {
			failure = fmt.Errorf("parse models.dev provider %s: %w", provider, err)
			return false
		}
		var models []sourceModel
		jsonwire.EachMember(entry.Models, func(_, raw []byte) bool {
			var model v2Model
			if err := json.Unmarshal(raw, &model); err != nil {
				failure = fmt.Errorf("parse models.dev provider %s: %w", provider, err)
				return false
			}
			// Specialized types (image, speech, realtime, decision, ...) only appear in v2.
			if model.Type == "chat" {
				models = append(models, normalizeModelsDevModel(model))
			}
			return true
		})
		result[string(provider)] = models
		return failure == nil
	})
	return result, failure
}

func normalizeModelsDevModel(model v2Model) sourceModel {
	reasoning := model.Capabilities.Reasoning
	normalized := sourceModel{
		ID: model.ID, Name: model.Name, Status: model.Status,
		ToolCall: model.Capabilities.Tools.Supported, StructuredOutput: model.Capabilities.StructuredOutput,
		Reasoning: reasoning.Supported,
		ImageInput: slices.ContainsFunc(model.Modalities.Input, func(modality string) bool {
			return modality == "image" || strings.HasPrefix(modality, "image/")
		}),
		TextInput: slices.Contains(model.Modalities.Input, "text"), TextOutput: slices.Contains(model.Modalities.Output, "text"),
		ContextWindow: model.Limit.Context, MaxTokens: model.Limit.Output,
		HasCost: model.Cost != nil,
	}
	if reasoning.Supported {
		normalized.ReasoningOptions = []reasoningOption{}
		if reasoning.Toggle {
			normalized.ReasoningOptions = append(normalized.ReasoningOptions, reasoningOption{Type: "toggle"})
		}
		if reasoning.Effort != nil {
			normalized.ReasoningOptions = append(normalized.ReasoningOptions, reasoningOption{Type: "effort", Values: *reasoning.Effort})
		}
		if reasoning.Budget != nil {
			normalized.ReasoningOptions = append(normalized.ReasoningOptions, reasoningOption{Type: "budget_tokens"})
		}
	}
	if model.Cost != nil && model.Cost.Text != nil {
		normalized.Cost = normalizeCost(model.Cost.Text.Input, model.Cost.Text.Output)
	}
	jsonwire.EachMember(model.API, func(protocol, raw []byte) bool {
		var entry struct {
			BaseURL string `json:"base_url"`
			SDK     struct {
				AISDK string `json:"aisdk"`
			} `json:"sdk"`
		}
		_ = json.Unmarshal(raw, &entry)
		normalized.APIs = append(normalized.APIs, sourceAPI{Protocol: string(protocol), BaseURL: entry.BaseURL, AISDK: entry.SDK.AISDK})
		return true
	})
	return normalized
}

func normalizeCost(input, output *v2Price) sourceCost {
	perToken := func(unit *string) bool { return unit == nil || *unit == "token" }
	if input == nil || output == nil || !perToken(input.Unit) || !perToken(output.Unit) {
		return sourceCost{}
	}
	// Only context-size overrides map onto cost tiers; region, service-tier and
	// time-based prices have no equivalent.
	var tiers []sourceTier
	tier := func(above float64) *sourceTier {
		for index := range tiers {
			if tiers[index].Above == above {
				return &tiers[index]
			}
		}
		tiers = append(tiers, sourceTier{Above: above})
		return &tiers[len(tiers)-1]
	}
	type override struct {
		When  map[string]json.RawMessage `json:"when"`
		Price *float64                   `json:"price"`
		Cache *v2Cache                   `json:"cache"`
	}
	for _, raw := range input.Overrides {
		var item override
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if above, ok := contextThreshold(item.When); ok {
			entry := tier(above)
			if item.Price != nil {
				entry.Input = item.Price
			}
			if item.Cache != nil && item.Cache.Read != nil {
				entry.CacheRead = item.Cache.Read
			}
			if write := cacheWritePrice(item.Cache); write != nil {
				entry.CacheWrite = write
			}
		}
	}
	for _, raw := range output.Overrides {
		var item override
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if above, ok := contextThreshold(item.When); ok && item.Price != nil {
			tier(above).Output = item.Price
		}
	}
	cost := sourceCost{Input: &input.Price, Output: &output.Price, CacheWrite: cacheWritePrice(input.Cache), Tiers: tiers}
	if input.Cache != nil {
		cost.CacheRead = input.Cache.Read
	}
	return cost
}

func contextThreshold(when map[string]json.RawMessage) (float64, bool) {
	raw, ok := when["context"]
	if len(when) != 1 || !ok {
		return 0, false
	}
	var context struct {
		GT  json.RawMessage `json:"gt"`
		GTE json.RawMessage `json:"gte"`
	}
	_ = json.Unmarshal(raw, &context)
	size := context.GT
	if len(size) == 0 || string(size) == "null" {
		size = context.GTE
	}
	var value float64
	if json.Unmarshal(size, &value) != nil {
		return 0, false
	}
	return value, true
}

// cacheWritePrice takes the first listed TTL price, the host's default.
func cacheWritePrice(cache *v2Cache) *float64 {
	if cache == nil {
		return nil
	}
	if price := firstPrice(cache.Write); price != nil {
		return price
	}
	if cache.Explicit != nil {
		return firstPrice(cache.Explicit.Write)
	}
	return nil
}

func firstPrice(raw json.RawMessage) *float64 {
	var price *float64
	if len(raw) == 0 || json.Unmarshal(raw, &price) == nil {
		return price
	}
	jsonwire.EachMember(raw, func(_, value []byte) bool {
		_ = json.Unmarshal(value, &price)
		return false
	})
	return price
}

func rate(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// modelsDevCost is upstream modelsDevCost: missing rates are free and tiers
// keep the base rate for rates they omit.
func modelsDevCost(cost sourceCost) ai.ModelCost {
	base := ai.ModelCostRates{Input: rate(cost.Input), Output: rate(cost.Output), CacheRead: rate(cost.CacheRead), CacheWrite: rate(cost.CacheWrite)}
	result := ai.ModelCost{ModelCostRates: base}
	if len(cost.Tiers) == 0 {
		return result
	}
	orBase := func(value *float64, fallback float64) float64 {
		if value == nil {
			return fallback
		}
		return *value
	}
	tiers := make([]ai.ModelCostTier, 0, len(cost.Tiers))
	for _, tier := range cost.Tiers {
		tiers = append(tiers, ai.ModelCostTier{InputTokensAbove: tier.Above, ModelCostRates: ai.ModelCostRates{
			Input: orBase(tier.Input, base.Input), Output: orBase(tier.Output, base.Output),
			CacheRead: orBase(tier.CacheRead, base.CacheRead), CacheWrite: orBase(tier.CacheWrite, base.CacheWrite),
		}})
	}
	result.Tiers = &tiers
	return result
}

// roundCost matches upstream Number(value.toFixed(6)): half away from zero so
// negative sentinel prices survive intact.
func roundCost(value float64) float64 {
	return math.Round(value*1000000) / 1000000
}

func normalizeNvidiaModelID(id string) string {
	return strings.ReplaceAll(strings.ToLower(id), "_", ".")
}

// parseNvidiaIDs keys the live NIM ids by both the raw and the normalized id.
func parseNvidiaIDs(listing []byte) (map[string]string, error) {
	result := map[string]string{}
	if len(listing) == 0 {
		return result, nil
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listing, &parsed); err != nil {
		return nil, fmt.Errorf("parse NVIDIA NIM models listing: %w", err)
	}
	for _, model := range parsed.Data {
		result[model.ID] = model.ID
		result[normalizeNvidiaModelID(model.ID)] = model.ID
	}
	return result, nil
}

// parseOpenRouter is upstream buildOpenRouterCatalog's chat half.
func parseOpenRouter(listing []byte) ([]*model, error) {
	var parsed struct {
		Data []struct {
			ID                  string   `json:"id"`
			Name                string   `json:"name"`
			SupportedParameters []string `json:"supported_parameters"`
			Reasoning           *struct {
				Mandatory        bool      `json:"mandatory"`
				SupportedEfforts []*string `json:"supported_efforts"`
			} `json:"reasoning"`
			ContextLength float64 `json:"context_length"`
			Architecture  struct {
				Modality string `json:"modality"`
			} `json:"architecture"`
			Pricing     openRouterPricing `json:"pricing"`
			TopProvider struct {
				ContextLength       float64 `json:"context_length"`
				MaxCompletionTokens float64 `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listing, &parsed); err != nil {
		return nil, fmt.Errorf("parse OpenRouter models listing: %w", err)
	}
	var models []*model
	for _, item := range parsed.Data {
		if !slices.Contains(item.SupportedParameters, "tools") {
			continue
		}
		input := ai.InputModalities{ai.InputText}
		if strings.Contains(item.Architecture.Modality, "image") {
			input = append(input, ai.InputImage)
		}
		api, baseURL := ai.APIOpenAICompletions, "https://openrouter.ai/api/v1"
		// Includes the ~anthropic/claude-*-latest aliases, which OpenRouter's Messages endpoint accepts.
		if strings.HasPrefix(strings.TrimPrefix(item.ID, "~"), "anthropic/") && !strings.HasSuffix(item.ID, ":batch") {
			api, baseURL = ai.APIAnthropicMessages, "https://openrouter.ai/api"
		}
		var levels thinking
		if reasoning := item.Reasoning; reasoning != nil {
			if mapped := effortThinkingLevels([]reasoningOption{{Type: "effort", Values: reasoning.SupportedEfforts}}); mapped != nil {
				mapped[ai.ModelThinkingOff] = nil
				if !reasoning.Mandatory {
					mapped[ai.ModelThinkingOff] = ptr("none")
				}
				levels = mapped
			} else if reasoning.Mandatory {
				levels = thinking{ai.ModelThinkingOff: nil}
			}
		}
		models = append(models, &model{Model: ai.Model{
			ID: item.ID, Name: item.Name, API: api, Provider: "openrouter", BaseURL: baseURL,
			Reasoning: slices.Contains(item.SupportedParameters, "reasoning"), ThinkingLevelMap: levels.pointer(), Input: input,
			Cost:          item.Pricing.cost(),
			ContextWindow: firstNonZero(item.TopProvider.ContextLength, item.ContextLength, 4096),
			MaxTokens:     firstNonZero(item.TopProvider.MaxCompletionTokens, 4096),
		}})
	}
	return models, nil
}

// openRouterPricing is OpenRouter's $/token prices as decimal strings, and its conditional overrides.
type openRouterPricing struct {
	openRouterRates
	Overrides []struct {
		openRouterRates
		MinPromptTokens *float64 `json:"min_prompt_tokens"`
		UTCStart        *float64 `json:"utc_start"`
		UTCEnd          *float64 `json:"utc_end"`
		UTCDays         []string `json:"utc_days"`
	} `json:"overrides"`
}

type openRouterRates struct {
	Prompt          string `json:"prompt"`
	Completion      string `json:"completion"`
	InputCacheRead  string `json:"input_cache_read"`
	InputCacheWrite string `json:"input_cache_write"`
}

// cost is upstream openRouterCost: negative placeholder prices (router models) fall back to 0, prompt-length
// overrides become tiers that keep the base price for rates they omit, and time-of-day overrides are skipped.
func (pricing openRouterPricing) cost() ai.ModelCost {
	base := pricing.rates(ai.ModelCostRates{})
	cost := ai.ModelCost{ModelCostRates: base}
	var tiers []ai.ModelCostTier
	for _, override := range pricing.Overrides {
		if override.MinPromptTokens != nil && override.UTCStart == nil && override.UTCEnd == nil && override.UTCDays == nil {
			tiers = append(tiers, ai.ModelCostTier{InputTokensAbove: *override.MinPromptTokens, ModelCostRates: override.rates(base)})
		}
	}
	if len(tiers) != 0 {
		cost.Tiers = &tiers
	}
	return cost
}

func (rates openRouterRates) rates(fallback ai.ModelCostRates) ai.ModelCostRates {
	return ai.ModelCostRates{
		Input: perMillionTokens(rates.Prompt, fallback.Input), Output: perMillionTokens(rates.Completion, fallback.Output),
		CacheRead: perMillionTokens(rates.InputCacheRead, fallback.CacheRead), CacheWrite: perMillionTokens(rates.InputCacheWrite, fallback.CacheWrite),
	}
}

// perMillionTokens converts an OpenRouter $/token price string to $/1M tokens; a missing, invalid or negative price is fallback.
func perMillionTokens(value string, fallback float64) float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 {
		return fallback
	}
	return roundCost(parsed * 1000000)
}

// vercelPriceTier is a prompt-length bracket: Min inclusive, Max exclusive.
type vercelPriceTier struct {
	Cost flexibleNumber `json:"cost"`
	Min  *float64       `json:"min"`
	Max  *float64       `json:"max"`
}

type vercelPricing struct {
	Input           flexibleNumber    `json:"input"`
	Output          flexibleNumber    `json:"output"`
	InputCacheRead  flexibleNumber    `json:"input_cache_read"`
	InputCacheWrite flexibleNumber    `json:"input_cache_write"`
	InputTiers      []vercelPriceTier `json:"input_tiers"`
	OutputTiers     []vercelPriceTier `json:"output_tiers"`
	CacheReadTiers  []vercelPriceTier `json:"input_cache_read_tiers"`
	CacheWriteTiers []vercelPriceTier `json:"input_cache_write_tiers"`
}

// cost is upstream getAiGatewayCost: every bracket start above zero becomes a
// request-wide tier with the rates in effect there.
func (pricing vercelPricing) cost() ai.ModelCost {
	base := ai.ModelCostRates{
		Input: pricing.Input.perMillion(), Output: pricing.Output.perMillion(),
		CacheRead: pricing.InputCacheRead.perMillion(), CacheWrite: pricing.InputCacheWrite.perMillion(),
	}
	fields := [][]vercelPriceTier{pricing.InputTiers, pricing.OutputTiers, pricing.CacheReadTiers, pricing.CacheWriteTiers}
	var starts []float64
	for _, brackets := range fields {
		for _, bracket := range brackets {
			if bracket.Min != nil && *bracket.Min > 0 && !slices.Contains(starts, *bracket.Min) {
				starts = append(starts, *bracket.Min)
			}
		}
	}
	slices.Sort(starts)
	cost := ai.ModelCost{ModelCostRates: base}
	if len(starts) == 0 {
		return cost
	}
	tiers := make([]ai.ModelCostTier, 0, len(starts))
	for _, start := range starts {
		rates := base
		targets := []*float64{&rates.Input, &rates.Output, &rates.CacheRead, &rates.CacheWrite}
		for index, brackets := range fields {
			for _, bracket := range brackets {
				if (bracket.Min == nil || *bracket.Min <= start) && (bracket.Max == nil || start < *bracket.Max) {
					if bracket.Cost.set {
						*targets[index] = bracket.Cost.perMillion()
					}
					break
				}
			}
		}
		tiers = append(tiers, ai.ModelCostTier{InputTokensAbove: start - 1, ModelCostRates: rates})
	}
	cost.Tiers = &tiers
	return cost
}

// parseVercel is upstream fetchAiGatewayCatalog's chat half; every model rides
// the anthropic-messages API.
func parseVercel(listing []byte) ([]*model, error) {
	var parsed struct {
		Data []struct {
			ID            string        `json:"id"`
			Name          string        `json:"name"`
			Type          string        `json:"type"`
			Tags          []string      `json:"tags"`
			ContextWindow float64       `json:"context_window"`
			MaxTokens     float64       `json:"max_tokens"`
			Pricing       vercelPricing `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(listing, &parsed); err != nil {
		return nil, fmt.Errorf("parse Vercel AI Gateway models listing: %w", err)
	}
	var models []*model
	for _, item := range parsed.Data {
		if item.Type == "evaluation" || !slices.Contains(item.Tags, "tool-use") {
			continue
		}
		input := ai.InputModalities{ai.InputText}
		if slices.Contains(item.Tags, "vision") {
			input = append(input, ai.InputImage)
		}
		name := item.Name
		if name == "" {
			name = item.ID
		}
		models = append(models, &model{
			Model: ai.Model{
				ID: item.ID, Name: name, API: ai.APIAnthropicMessages, Provider: "vercel-ai-gateway", BaseURL: "https://ai-gateway.vercel.sh",
				Reasoning: slices.Contains(item.Tags, "reasoning"), Input: input, Cost: item.Pricing.cost(),
				ContextWindow: firstNonZero(item.ContextWindow, 4096), MaxTokens: firstNonZero(item.MaxTokens, 4096),
			},
			compat: compat("allowEmptySignature", true),
		})
	}
	return models, nil
}

// flexibleNumber decodes a JSON number or numeric string; anything else is 0.
type flexibleNumber struct {
	value float64
	set   bool
}

func (number *flexibleNumber) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	number.set = true
	if text, err := strconv.Unquote(trimmed); err == nil {
		trimmed = text
	}
	number.value, _ = strconv.ParseFloat(trimmed, 64)
	return nil
}

func (number flexibleNumber) perMillion() float64 {
	if math.IsNaN(number.value) || math.IsInf(number.value, 0) {
		return 0
	}
	return roundCost(number.value * 1000000)
}

func firstNonZero(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
