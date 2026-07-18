package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func validateRawModelConfig(data []byte, providerOrder []string) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("root must be an object")
	}
	providersValue, exists := root["providers"]
	if !exists {
		return fmt.Errorf("providers is required")
	}
	providers, ok := providersValue.(map[string]any)
	if !ok {
		return fmt.Errorf("providers must be an object")
	}
	for _, providerID := range orderedMapKeys(providers, providerOrder) {
		value := providers[providerID]
		provider, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("providers.%s must be an object", providerID)
		}
		if err := validateProviderJSON("providers."+providerID, provider); err != nil {
			return err
		}
	}
	return nil
}

func validateProviderJSON(path string, provider map[string]any) error {
	for _, name := range []string{"name", "baseUrl", "apiKey", "api"} {
		if err := validateOptionalString(provider, name, path, true); err != nil {
			return err
		}
	}
	if value, exists := provider["oauth"]; exists {
		if text, ok := value.(string); !ok || text != "radius" {
			return fmt.Errorf("%s.oauth must be %q", path, "radius")
		}
	}
	if err := validateOptionalHeaders(provider, "headers", path); err != nil {
		return err
	}
	if err := validateOptionalBool(provider, "authHeader", path); err != nil {
		return err
	}
	if err := validateOptionalCompat(provider, "compat", path); err != nil {
		return err
	}
	if value, exists := provider["models"]; exists {
		models, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s.models must be an array", path)
		}
		for index, value := range models {
			model, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.models.%d must be an object", path, index)
			}
			if err := validateModelDefinitionJSON(fmt.Sprintf("%s.models.%d", path, index), model); err != nil {
				return err
			}
		}
	}
	if value, exists := provider["modelOverrides"]; exists {
		overrides, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.modelOverrides must be an object", path)
		}
		for _, modelID := range orderedMapKeys(overrides, nil) {
			value := overrides[modelID]
			override, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.modelOverrides.%s must be an object", path, modelID)
			}
			if err := validateModelOverrideJSON(path+".modelOverrides."+modelID, override); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateModelDefinitionJSON(path string, model map[string]any) error {
	id, exists := model["id"]
	if !exists {
		return fmt.Errorf("%s.id is required", path)
	}
	if text, ok := id.(string); !ok || text == "" {
		return fmt.Errorf("%s.id must be a non-empty string", path)
	}
	for _, name := range []string{"name", "api", "baseUrl"} {
		if err := validateOptionalString(model, name, path, true); err != nil {
			return err
		}
	}
	if err := validateOptionalBool(model, "reasoning", path); err != nil {
		return err
	}
	if err := validateOptionalThinkingMap(model, "thinkingLevelMap", path); err != nil {
		return err
	}
	if err := validateOptionalInput(model, "input", path); err != nil {
		return err
	}
	if value, exists := model["cost"]; exists {
		if err := validateCostJSON(path+".cost", value, true); err != nil {
			return err
		}
	}
	for _, name := range []string{"contextWindow", "maxTokens"} {
		if err := validateOptionalNumber(model, name, path); err != nil {
			return err
		}
	}
	if err := validateOptionalHeaders(model, "headers", path); err != nil {
		return err
	}
	return validateOptionalCompat(model, "compat", path)
}

func validateModelOverrideJSON(path string, override map[string]any) error {
	if err := validateOptionalString(override, "name", path, true); err != nil {
		return err
	}
	if err := validateOptionalBool(override, "reasoning", path); err != nil {
		return err
	}
	if err := validateOptionalThinkingMap(override, "thinkingLevelMap", path); err != nil {
		return err
	}
	if err := validateOptionalInput(override, "input", path); err != nil {
		return err
	}
	if value, exists := override["cost"]; exists {
		if err := validateCostJSON(path+".cost", value, false); err != nil {
			return err
		}
	}
	for _, name := range []string{"contextWindow", "maxTokens"} {
		if err := validateOptionalNumber(override, name, path); err != nil {
			return err
		}
	}
	if err := validateOptionalHeaders(override, "headers", path); err != nil {
		return err
	}
	return validateOptionalCompat(override, "compat", path)
}

func validateCostJSON(path string, value any, ratesRequired bool) error {
	cost, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", path)
	}
	for _, name := range []string{"input", "output", "cacheRead", "cacheWrite"} {
		value, exists := cost[name]
		if !exists {
			if ratesRequired {
				return fmt.Errorf("%s.%s is required", path, name)
			}
			continue
		}
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("%s.%s must be a number", path, name)
		}
	}
	if tiersValue, exists := cost["tiers"]; exists {
		tiers, ok := tiersValue.([]any)
		if !ok {
			return fmt.Errorf("%s.tiers must be an array", path)
		}
		for index, value := range tiers {
			tier, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.tiers.%d must be an object", path, index)
			}
			for _, name := range []string{"inputTokensAbove", "input", "output", "cacheRead", "cacheWrite"} {
				value, exists := tier[name]
				if !exists {
					return fmt.Errorf("%s.tiers.%d.%s is required", path, index, name)
				}
				if _, ok := value.(json.Number); !ok {
					return fmt.Errorf("%s.tiers.%d.%s must be a number", path, index, name)
				}
			}
		}
	}
	return nil
}

func validateOptionalThinkingMap(object map[string]any, name, path string) error {
	value, exists := object[name]
	if !exists {
		return nil
	}
	mapping, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s.%s must be an object", path, name)
	}
	valid := map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	for _, level := range orderedMapKeys(mapping, nil) {
		value := mapping[level]
		if !valid[level] {
			continue
		}
		if value != nil {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s.%s.%s must be a string or null", path, name, level)
			}
		}
	}
	return nil
}

func validateOptionalInput(object map[string]any, name, path string) error {
	value, exists := object[name]
	if !exists {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return fmt.Errorf("%s.%s must be an array", path, name)
	}
	for index, value := range items {
		text, ok := value.(string)
		if !ok || text != "text" && text != "image" {
			return fmt.Errorf("%s.%s.%d must be %q or %q", path, name, index, "text", "image")
		}
	}
	return nil
}

func validateOptionalHeaders(object map[string]any, name, path string) error {
	value, exists := object[name]
	if !exists {
		return nil
	}
	headers, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s.%s must be an object", path, name)
	}
	for _, header := range orderedMapKeys(headers, nil) {
		value := headers[header]
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s.%s.%s must be a string", path, name, header)
		}
	}
	return nil
}

func validateOptionalCompat(object map[string]any, name, path string) error {
	value, exists := object[name]
	if !exists {
		return nil
	}
	compat, ok := value.(map[string]any)
	if !ok || !validCompatObject(compat) {
		return fmt.Errorf("%s.%s does not match a supported API compatibility shape", path, name)
	}
	return nil
}

func validCompatObject(compat map[string]any) bool {
	return validOpenAICompletionsCompat(compat) || validOpenAIResponsesCompat(compat) || validAnthropicCompat(compat)
}

func validOpenAICompletionsCompat(compat map[string]any) bool {
	if !optionalBools(compat, "supportsStore", "supportsDeveloperRole", "supportsReasoningEffort", "supportsUsageInStreaming", "requiresToolResultName", "requiresAssistantAfterToolResult", "requiresThinkingAsText", "requiresReasoningContentOnAssistantMessages", "supportsStrictMode", "sendSessionAffinityHeaders", "supportsLongCacheRetention") {
		return false
	}
	if !optionalEnum(compat, "maxTokensField", "max_completion_tokens", "max_tokens") ||
		!optionalEnum(compat, "thinkingFormat", "openai", "openrouter", "together", "deepseek", "zai", "qwen", "chat-template", "qwen-chat-template", "string-thinking", "ant-ling") ||
		!optionalEnum(compat, "cacheControlFormat", "anthropic") || !optionalEnum(compat, "deferredToolsMode", "kimi") ||
		!optionalEnum(compat, "sessionAffinityFormat", "openai", "openai-nosession", "openrouter") {
		return false
	}
	if value, exists := compat["chatTemplateKwargs"]; exists && !validChatTemplateKwargs(value) {
		return false
	}
	if value, exists := compat["openRouterRouting"]; exists && !validOpenRouterRouting(value) {
		return false
	}
	if value, exists := compat["vercelGatewayRouting"]; exists && !validStringArrayObject(value, "only", "order") {
		return false
	}
	return true
}

func validOpenAIResponsesCompat(compat map[string]any) bool {
	return optionalBools(compat, "supportsDeveloperRole", "supportsLongCacheRetention", "supportsToolSearch") &&
		optionalEnum(compat, "sessionAffinityFormat", "openai", "openai-nosession", "openrouter")
}

func validAnthropicCompat(compat map[string]any) bool {
	return optionalBools(compat, "supportsEagerToolInputStreaming", "supportsLongCacheRetention", "sendSessionAffinityHeaders", "supportsCacheControlOnTools", "forceAdaptiveThinking", "supportsToolReferences")
}

func validChatTemplateKwargs(value any) bool {
	kwargs, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, value := range kwargs {
		switch value := value.(type) {
		case nil, string, json.Number, bool:
			continue
		case map[string]any:
			variable, ok := value["$var"].(string)
			if !ok || variable != "thinking.enabled" && variable != "thinking.effort" || !optionalBools(value, "omitWhenOff") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validOpenRouterRouting(value any) bool {
	routing, ok := value.(map[string]any)
	if !ok || !optionalBools(routing, "allow_fallbacks", "require_parameters", "zdr", "enforce_distillable_text") ||
		!optionalEnum(routing, "data_collection", "deny", "allow") {
		return false
	}
	for _, name := range []string{"order", "only", "ignore", "quantizations"} {
		if value, exists := routing[name]; exists && !validStringArray(value) {
			return false
		}
	}
	if value, exists := routing["sort"]; exists {
		if _, ok := value.(string); !ok {
			sortObject, ok := value.(map[string]any)
			if !ok || !optionalStringOrNull(sortObject, "partition") || validateOptionalString(sortObject, "by", "sort", false) != nil {
				return false
			}
		}
	}
	if value, exists := routing["max_price"]; exists {
		price, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, name := range []string{"prompt", "completion", "image", "audio", "request"} {
			if value, exists := price[name]; exists {
				if _, number := value.(json.Number); !number {
					if _, text := value.(string); !text {
						return false
					}
				}
			}
		}
	}
	for _, name := range []string{"preferred_min_throughput", "preferred_max_latency"} {
		if value, exists := routing[name]; exists && !validPercentileValue(value) {
			return false
		}
	}
	return true
}

func validPercentileValue(value any) bool {
	if _, ok := value.(json.Number); ok {
		return true
	}
	percentiles, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, name := range []string{"p50", "p75", "p90", "p99"} {
		if value, exists := percentiles[name]; exists {
			if _, ok := value.(json.Number); !ok {
				return false
			}
		}
	}
	return true
}

func validStringArrayObject(value any, names ...string) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, name := range names {
		if value, exists := object[name]; exists && !validStringArray(value) {
			return false
		}
	}
	return true
}

func validStringArray(value any) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if _, ok := item.(string); !ok {
			return false
		}
	}
	return true
}

func optionalBools(object map[string]any, names ...string) bool {
	for _, name := range names {
		if value, exists := object[name]; exists {
			if _, ok := value.(bool); !ok {
				return false
			}
		}
	}
	return true
}

func optionalEnum(object map[string]any, name string, values ...string) bool {
	value, exists := object[name]
	if !exists {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, candidate := range values {
		if text == candidate {
			return true
		}
	}
	return false
}

func optionalStringOrNull(object map[string]any, name string) bool {
	value, exists := object[name]
	if !exists || value == nil {
		return true
	}
	_, ok := value.(string)
	return ok
}

func validateOptionalString(object map[string]any, name, path string, nonEmpty bool) error {
	value, exists := object[name]
	if !exists {
		return nil
	}
	text, ok := value.(string)
	if !ok || nonEmpty && text == "" {
		return fmt.Errorf("%s.%s must be a non-empty string", path, name)
	}
	return nil
}

func validateOptionalBool(object map[string]any, name, path string) error {
	value, exists := object[name]
	if !exists {
		return nil
	}
	if _, ok := value.(bool); !ok {
		return fmt.Errorf("%s.%s must be a boolean", path, name)
	}
	return nil
}

func validateOptionalNumber(object map[string]any, name, path string) error {
	value, exists := object[name]
	if !exists {
		return nil
	}
	if _, ok := value.(json.Number); !ok {
		return fmt.Errorf("%s.%s must be a number", path, name)
	}
	return nil
}
