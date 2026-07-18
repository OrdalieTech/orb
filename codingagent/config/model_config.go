package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
)

type ModelConfig struct {
	Providers     map[string]ModelProviderConfig `json:"providers"`
	loadError     string
	providerOrder []string
}

type ModelProviderConfig struct {
	Name           *string                  `json:"name,omitempty"`
	BaseURL        *string                  `json:"baseUrl,omitempty"`
	APIKey         *string                  `json:"apiKey,omitempty"`
	API            *ai.API                  `json:"api,omitempty"`
	OAuth          *string                  `json:"oauth,omitempty"`
	Headers        map[string]string        `json:"headers,omitempty"`
	Compat         json.RawMessage          `json:"compat,omitempty"`
	AuthHeader     *bool                    `json:"authHeader,omitempty"`
	Models         []ModelDefinition        `json:"models,omitempty"`
	ModelOverrides map[string]ModelOverride `json:"modelOverrides,omitempty"`
}

type ModelDefinition struct {
	ID               string                             `json:"id"`
	Name             *string                            `json:"name,omitempty"`
	API              *ai.API                            `json:"api,omitempty"`
	BaseURL          *string                            `json:"baseUrl,omitempty"`
	Reasoning        *bool                              `json:"reasoning,omitempty"`
	ThinkingLevelMap *map[ai.ModelThinkingLevel]*string `json:"thinkingLevelMap,omitempty"`
	Input            *ai.InputModalities                `json:"input,omitempty"`
	Cost             *ai.ModelCost                      `json:"cost,omitempty"`
	ContextWindow    *float64                           `json:"contextWindow,omitempty"`
	MaxTokens        *float64                           `json:"maxTokens,omitempty"`
	Headers          map[string]string                  `json:"headers,omitempty"`
	Compat           json.RawMessage                    `json:"compat,omitempty"`
}

type ModelOverride struct {
	Name             *string                            `json:"name,omitempty"`
	Reasoning        *bool                              `json:"reasoning,omitempty"`
	ThinkingLevelMap *map[ai.ModelThinkingLevel]*string `json:"thinkingLevelMap,omitempty"`
	Input            *ai.InputModalities                `json:"input,omitempty"`
	Cost             *ModelCostOverride                 `json:"cost,omitempty"`
	ContextWindow    *float64                           `json:"contextWindow,omitempty"`
	MaxTokens        *float64                           `json:"maxTokens,omitempty"`
	Headers          map[string]string                  `json:"headers,omitempty"`
	Compat           json.RawMessage                    `json:"compat,omitempty"`
}

type ModelCostOverride struct {
	Input      *float64            `json:"input,omitempty"`
	Output     *float64            `json:"output,omitempty"`
	CacheRead  *float64            `json:"cacheRead,omitempty"`
	CacheWrite *float64            `json:"cacheWrite,omitempty"`
	Tiers      *[]ai.ModelCostTier `json:"tiers,omitempty"`
}

// LoadModelConfig loads one immutable models.json snapshot. Missing files are empty configs.
func LoadModelConfig(path string) (*ModelConfig, error) {
	if path == "" {
		return &ModelConfig{Providers: map[string]ModelProviderConfig{}}, nil
	}
	normalized, err := NormalizePath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(normalized)
	if errors.Is(err, os.ErrNotExist) {
		return &ModelConfig{Providers: map[string]ModelProviderConfig{}}, nil
	}
	if err != nil {
		return failedModelConfig(fmt.Sprintf("Failed to load models.json: %v\n\nFile: %s", err, normalized)), nil
	}
	data = stripJSONComments(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return failedModelConfig(fmt.Sprintf("Failed to parse models.json: %v\n\nFile: %s", err, normalized)), nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return failedModelConfig(fmt.Sprintf("Failed to parse models.json: %v\n\nFile: %s", err, normalized)), nil
	}
	providerOrder, err := decodeModelProviderOrder(raw)
	if err != nil {
		return nil, err
	}
	if err := validateRawModelConfig(raw, providerOrder); err != nil {
		return failedModelConfig(fmt.Sprintf("Invalid models.json schema:\n  - %v\n\nFile: %s", err, normalized)), nil
	}
	var config ModelConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return failedModelConfig(fmt.Sprintf("Invalid models.json schema:\n  - %v\n\nFile: %s", err, normalized)), nil
	}
	config.providerOrder = providerOrder
	if err := validateModelConfig(&config); err != nil {
		return failedModelConfig(fmt.Sprintf("Invalid models.json schema:\n  - %v\n\nFile: %s", err, normalized)), nil
	}
	return &config, nil
}

func decodeModelProviderOrder(raw json.RawMessage) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, nil
	}
	var order []string
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		name, ok := nameToken.(string)
		if !ok || name != "providers" {
			continue
		}
		order, err = decodeObjectKeyOrder(value)
		if err != nil {
			return nil, err
		}
	}
	return order, nil
}

func decodeObjectKeyOrder(raw json.RawMessage) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, nil
	}
	order := make([]string, 0)
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if ok {
			if _, exists := seen[key]; !exists {
				order = append(order, key)
				seen[key] = struct{}{}
			}
		}
		var discard json.RawMessage
		if err := decoder.Decode(&discard); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func failedModelConfig(message string) *ModelConfig {
	return &ModelConfig{Providers: map[string]ModelProviderConfig{}, loadError: message}
}

func (config *ModelConfig) Error() string {
	if config == nil {
		return ""
	}
	return config.loadError
}

func (config *ModelConfig) providerIDs() []string {
	return orderedMapKeys(config.Providers, config.providerOrder)
}

func orderedMapKeys[T any](values map[string]T, preferred []string) []string {
	keys := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, key := range preferred {
		if _, exists := values[key]; !exists {
			continue
		}
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	missing := make([]string, 0, len(values)-len(keys))
	for key := range values {
		if _, exists := seen[key]; !exists {
			missing = append(missing, key)
		}
	}
	slices.Sort(missing)
	return append(keys, missing...)
}

func validateModelConfig(config *ModelConfig) error {
	for _, providerID := range config.providerIDs() {
		provider := config.Providers[providerID]
		for _, field := range []struct {
			name  string
			value *string
		}{
			{name: "name", value: provider.Name},
			{name: "baseUrl", value: provider.BaseURL},
			{name: "apiKey", value: provider.APIKey},
		} {
			if field.value != nil && *field.value == "" {
				return fmt.Errorf("providers.%s.%s must not be empty", providerID, field.name)
			}
		}
		if provider.OAuth != nil {
			return fmt.Errorf("providers.%s.oauth: Radius is not part of pi-go", providerID)
		}
		for index, model := range provider.Models {
			path := fmt.Sprintf("providers.%s.models.%d", providerID, index)
			if model.ID == "" {
				return fmt.Errorf("%s.id must not be empty", path)
			}
			if model.Name != nil && *model.Name == "" {
				return fmt.Errorf("%s.name must not be empty", path)
			}
		}
	}
	return nil
}

// ApplyModelConfig overlays models.json using upstream's built-in/custom upsert order.
func ApplyModelConfig(base []ai.Model, config *ModelConfig) ([]ai.Model, error) {
	if config == nil {
		return append([]ai.Model(nil), base...), nil
	}
	byProvider := make(map[string][]ai.Model)
	providerOrder := make([]string, 0)
	for _, model := range base {
		providerID := string(model.Provider)
		if _, exists := byProvider[providerID]; !exists {
			providerOrder = append(providerOrder, providerID)
		}
		byProvider[providerID] = append(byProvider[providerID], model)
	}
	customProviders := make([]string, 0)
	for _, providerID := range config.providerIDs() {
		if _, exists := byProvider[providerID]; !exists {
			customProviders = append(customProviders, providerID)
		}
	}
	providerOrder = append(providerOrder, customProviders...)

	result := make([]ai.Model, 0, len(base))
	for _, providerID := range providerOrder {
		provider, configured := config.Providers[providerID]
		models, err := applyProviderConfig(providerID, byProvider[providerID], provider, configured)
		if err != nil {
			return nil, err
		}
		result = append(result, models...)
	}
	return result, nil
}

func applyProviderConfig(providerID string, base []ai.Model, provider ModelProviderConfig, configured bool) ([]ai.Model, error) {
	if !configured {
		return append([]ai.Model(nil), base...), nil
	}
	if len(provider.Models) == 0 && provider.BaseURL == nil && provider.Headers == nil && len(provider.Compat) == 0 &&
		len(provider.ModelOverrides) == 0 && provider.APIKey == nil && provider.AuthHeader == nil {
		//nolint:staticcheck // Exact upstream error text is part of WP-250 conformance.
		return nil, fmt.Errorf(`Provider %s: must specify "baseUrl", "headers", "compat", "modelOverrides", or "models".`, providerID)
	}
	models := make([]ai.Model, len(base))
	for index, model := range base {
		models[index] = model
		if provider.BaseURL != nil {
			models[index].BaseURL = *provider.BaseURL
		}
		models[index].Compat = mergeCompat(models[index].Compat, provider.Compat)
	}
	for _, definition := range provider.Models {
		existing := slices.IndexFunc(models, func(model ai.Model) bool { return model.ID == definition.ID })
		var defaults *ai.Model
		if existing >= 0 {
			defaults = &models[existing]
		} else if len(models) > 0 {
			defaults = &models[0]
		}
		model, err := modelFromConfig(providerID, definition, provider, defaults)
		if err != nil {
			return nil, err
		}
		if existing >= 0 {
			models[existing] = model
		} else {
			models = append(models, model)
		}
	}
	for index, model := range models {
		if override, exists := provider.ModelOverrides[model.ID]; exists {
			models[index] = applyModelOverride(model, override)
		}
	}
	return models, nil
}

func modelFromConfig(providerID string, definition ModelDefinition, provider ModelProviderConfig, defaults *ai.Model) (ai.Model, error) {
	api := ai.APIUnknown
	if defaults != nil {
		api = defaults.API
	}
	if provider.API != nil {
		api = *provider.API
	}
	if definition.API != nil {
		api = *definition.API
	}
	if api == ai.APIUnknown {
		//nolint:staticcheck // Exact upstream error text is part of WP-250 conformance.
		return ai.Model{}, fmt.Errorf(`Provider %s, model %s: no "api" specified. Set at provider or model level.`, providerID, definition.ID)
	}
	baseURL := ""
	if defaults != nil {
		baseURL = defaults.BaseURL
	}
	if provider.BaseURL != nil {
		baseURL = *provider.BaseURL
	}
	if definition.BaseURL != nil {
		baseURL = *definition.BaseURL
	}
	if baseURL == "" {
		//nolint:staticcheck // Exact upstream error text is part of WP-250 conformance.
		return ai.Model{}, fmt.Errorf(`Provider %s: "baseUrl" is required when defining custom models.`, providerID)
	}
	if definition.ContextWindow != nil && *definition.ContextWindow <= 0 {
		//nolint:staticcheck // Exact upstream error text is part of WP-250 conformance.
		return ai.Model{}, fmt.Errorf("Provider %s, model %s: invalid contextWindow", providerID, definition.ID)
	}
	if definition.MaxTokens != nil && *definition.MaxTokens <= 0 {
		//nolint:staticcheck // Exact upstream error text is part of WP-250 conformance.
		return ai.Model{}, fmt.Errorf("Provider %s, model %s: invalid maxTokens", providerID, definition.ID)
	}
	name, reasoning := definition.ID, false
	if definition.Name != nil {
		name = *definition.Name
	}
	if definition.Reasoning != nil {
		reasoning = *definition.Reasoning
	}
	input := ai.InputModalities{ai.InputText}
	if definition.Input != nil {
		input = append(ai.InputModalities(nil), (*definition.Input)...)
	}
	cost := ai.ModelCost{}
	if definition.Cost != nil {
		cost = *definition.Cost
	}
	contextWindow, maxTokens := float64(128000), float64(16384)
	if definition.ContextWindow != nil {
		contextWindow = *definition.ContextWindow
	}
	if definition.MaxTokens != nil {
		maxTokens = *definition.MaxTokens
	}
	return ai.Model{ID: definition.ID, Name: name, API: api, Provider: ai.ProviderID(providerID), BaseURL: baseURL, Reasoning: reasoning,
		ThinkingLevelMap: cloneThinkingMap(definition.ThinkingLevelMap), Input: input, Cost: cost, ContextWindow: contextWindow,
		MaxTokens: maxTokens, Compat: mergeCompat(provider.Compat, definition.Compat)}, nil
}

func applyModelOverride(model ai.Model, override ModelOverride) ai.Model {
	if override.Name != nil {
		model.Name = *override.Name
	}
	if override.Reasoning != nil {
		model.Reasoning = *override.Reasoning
	}
	if override.ThinkingLevelMap != nil {
		mapping := map[ai.ModelThinkingLevel]*string{}
		if model.ThinkingLevelMap != nil {
			for level, value := range *model.ThinkingLevelMap {
				mapping[level] = value
			}
		}
		for level, value := range *override.ThinkingLevelMap {
			mapping[level] = value
		}
		model.ThinkingLevelMap = &mapping
	}
	if override.Input != nil {
		model.Input = append(ai.InputModalities(nil), (*override.Input)...)
	}
	if override.Cost != nil {
		if override.Cost.Input != nil {
			model.Cost.Input = *override.Cost.Input
		}
		if override.Cost.Output != nil {
			model.Cost.Output = *override.Cost.Output
		}
		if override.Cost.CacheRead != nil {
			model.Cost.CacheRead = *override.Cost.CacheRead
		}
		if override.Cost.CacheWrite != nil {
			model.Cost.CacheWrite = *override.Cost.CacheWrite
		}
		if override.Cost.Tiers != nil {
			tiers := append([]ai.ModelCostTier(nil), (*override.Cost.Tiers)...)
			model.Cost.Tiers = &tiers
		}
	}
	if override.ContextWindow != nil {
		model.ContextWindow = *override.ContextWindow
	}
	if override.MaxTokens != nil {
		model.MaxTokens = *override.MaxTokens
	}
	model.Compat = mergeCompat(model.Compat, override.Compat)
	return model
}

func cloneThinkingMap(source *map[ai.ModelThinkingLevel]*string) *map[ai.ModelThinkingLevel]*string {
	if source == nil {
		return nil
	}
	result := make(map[ai.ModelThinkingLevel]*string, len(*source))
	for level, value := range *source {
		result[level] = value
	}
	return &result
}

func mergeCompat(base, override json.RawMessage) json.RawMessage {
	if len(override) == 0 {
		return append(json.RawMessage(nil), base...)
	}
	var baseObject, overrideObject map[string]json.RawMessage
	_ = json.Unmarshal(base, &baseObject)
	if err := json.Unmarshal(override, &overrideObject); err != nil {
		return append(json.RawMessage(nil), override...)
	}
	if baseObject == nil {
		baseObject = map[string]json.RawMessage{}
	}
	for key, value := range overrideObject {
		if key == "openRouterRouting" || key == "vercelGatewayRouting" || key == "chatTemplateKwargs" {
			var left, right map[string]json.RawMessage
			_ = json.Unmarshal(baseObject[key], &left)
			_ = json.Unmarshal(value, &right)
			if left != nil || right != nil {
				if left == nil {
					left = map[string]json.RawMessage{}
				}
				for nestedKey, nestedValue := range right {
					left[nestedKey] = nestedValue
				}
				value, _ = json.Marshal(left)
			}
		}
		baseObject[key] = value
	}
	merged, _ := json.Marshal(baseObject)
	return merged
}

// HasConfiguredAPIKey checks configured auth presence without executing shell commands.
func (config *ModelConfig) HasConfiguredAPIKey(providerID string, env map[string]string) bool {
	provider, exists := config.Providers[providerID]
	if !exists || provider.APIKey == nil {
		return false
	}
	return strings.HasPrefix(*provider.APIKey, "!") || len(missingConfigEnv(*provider.APIKey, env)) == 0
}

// ResolveAPIKey resolves one models.json key at request time.
func (config *ModelConfig) ResolveAPIKey(ctx context.Context, providerID string, env map[string]string) (*string, error) {
	provider, exists := config.Providers[providerID]
	if !exists || provider.APIKey == nil {
		return nil, nil
	}
	value, err := ResolveConfigValue(ctx, *provider.APIKey, env)
	if err != nil {
		return nil, fmt.Errorf("API key for provider %q: %w", providerID, err)
	}
	return &value, nil
}

// ResolveModelHeaders resolves provider and model header values without caching commands.
func (config *ModelConfig) ResolveModelHeaders(ctx context.Context, model ai.Model, env map[string]string, apiKeys ...*string) (*map[string]string, error) {
	headers := make(map[string]string)
	if model.Headers != nil {
		for name, value := range *model.Headers {
			setHeader(headers, name, value)
		}
	}
	provider, exists := config.Providers[string(model.Provider)]
	if !exists {
		if len(headers) == 0 {
			return nil, nil
		}
		return &headers, nil
	}
	raw := make(map[string]string)
	for name, value := range provider.Headers {
		setHeader(raw, name, value)
	}
	if override, ok := provider.ModelOverrides[model.ID]; ok {
		for name, value := range override.Headers {
			setHeader(raw, name, value)
		}
	}
	if index := slices.IndexFunc(provider.Models, func(definition ModelDefinition) bool { return definition.ID == model.ID }); index >= 0 {
		for name, value := range provider.Models[index].Headers {
			setHeader(raw, name, value)
		}
	}
	for name, value := range raw {
		resolved, err := ResolveConfigValue(ctx, value, env)
		if err != nil {
			return nil, fmt.Errorf("model %q header %q: %w", string(model.Provider)+"/"+model.ID, name, err)
		}
		setHeader(headers, name, resolved)
	}
	if provider.AuthHeader != nil && *provider.AuthHeader {
		if len(apiKeys) == 0 || apiKeys[0] == nil || *apiKeys[0] == "" {
			return nil, errors.New("authHeader requires a resolved API key")
		}
		setHeader(headers, "Authorization", "Bearer "+*apiKeys[0])
	}
	if len(headers) == 0 {
		return nil, nil
	}
	return &headers, nil
}

func setHeader(headers map[string]string, name, value string) {
	for existing := range headers {
		if strings.EqualFold(existing, name) {
			delete(headers, existing)
		}
	}
	headers[name] = value
}

func missingConfigEnv(value string, env map[string]string) []string {
	return GetMissingConfigValueEnvVarNames(value, env)
}

func stripJSONComments(data []byte) []byte {
	result := make([]byte, 0, len(data))
	inString, escaped := false, false
	for index := 0; index < len(data); index++ {
		character := data[index]
		if inString {
			result = append(result, character)
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		if character == '"' {
			inString = true
			result = append(result, character)
			continue
		}
		if character == '/' && index+1 < len(data) && data[index+1] == '/' {
			for index < len(data) && data[index] != '\n' {
				index++
			}
			if index < len(data) {
				result = append(result, '\n')
			}
			continue
		}
		if character == ',' {
			next := index + 1
			for next < len(data) && strings.ContainsRune(" \t\r\n", rune(data[next])) {
				next++
			}
			if next < len(data) && (data[next] == '}' || data[next] == ']') {
				continue
			}
		}
		result = append(result, character)
	}
	return result
}
