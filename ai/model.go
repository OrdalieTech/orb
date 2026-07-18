package ai

import (
	"context"
	"encoding/json"

	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

type CacheRetention string

const (
	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"
)

type Transport string

const (
	TransportSSE             Transport = "sse"
	TransportWebSocket       Transport = "websocket"
	TransportWebSocketCached Transport = "websocket-cached"
	TransportAuto            Transport = "auto"
)

type SessionAffinityFormat string

const (
	SessionAffinityOpenAI          SessionAffinityFormat = "openai"
	SessionAffinityOpenAINoSession SessionAffinityFormat = "openai-nosession"
	SessionAffinityOpenRouter      SessionAffinityFormat = "openrouter"
)

type InputModality string

const (
	InputText  InputModality = "text"
	InputImage InputModality = "image"
)

type InputModalities []InputModality

func (modalities InputModalities) MarshalJSON() ([]byte, error) {
	return marshalRequiredSlice(modalities)
}

type ThinkingBudgets struct {
	Minimal *int `json:"minimal,omitempty"`
	Low     *int `json:"low,omitempty"`
	Medium  *int `json:"medium,omitempty"`
	High    *int `json:"high,omitempty"`
}

type ModelCostRates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type ModelCostTier struct {
	InputTokensAbove float64 `json:"inputTokensAbove"`
	ModelCostRates
}

type ModelCost struct {
	ModelCostRates
	Tiers *[]ModelCostTier `json:"tiers,omitempty"`
}

type Model struct {
	ID               string                          `json:"id"`
	Name             string                          `json:"name"`
	API              API                             `json:"api"`
	Provider         ProviderID                      `json:"provider"`
	BaseURL          string                          `json:"baseUrl"`
	Reasoning        bool                            `json:"reasoning"`
	ThinkingLevelMap *map[ModelThinkingLevel]*string `json:"thinkingLevelMap,omitempty"`
	Input            InputModalities                 `json:"input"`
	Cost             ModelCost                       `json:"cost"`
	ContextWindow    float64                         `json:"contextWindow"`
	MaxTokens        float64                         `json:"maxTokens"`
	Headers          *map[string]string              `json:"headers,omitempty"`
	Compat           json.RawMessage                 `json:"compat,omitempty"`
}

type ImagesModel struct {
	ID               string                          `json:"id"`
	Name             string                          `json:"name"`
	API              ImagesAPI                       `json:"api"`
	Provider         ImagesProviderID                `json:"provider"`
	BaseURL          string                          `json:"baseUrl"`
	ThinkingLevelMap *map[ModelThinkingLevel]*string `json:"thinkingLevelMap,omitempty"`
	Input            InputModalities                 `json:"input"`
	Output           InputModalities                 `json:"output"`
	Cost             ModelCost                       `json:"cost"`
	Headers          *map[string]string              `json:"headers,omitempty"`
}

type MaxTokensField string

const (
	MaxTokensFieldCompletion MaxTokensField = "max_completion_tokens"
	MaxTokensFieldLegacy     MaxTokensField = "max_tokens"
)

type ThinkingFormat string

const (
	ThinkingFormatOpenAI           ThinkingFormat = "openai"
	ThinkingFormatOpenRouter       ThinkingFormat = "openrouter"
	ThinkingFormatDeepSeek         ThinkingFormat = "deepseek"
	ThinkingFormatTogether         ThinkingFormat = "together"
	ThinkingFormatZAI              ThinkingFormat = "zai"
	ThinkingFormatQwen             ThinkingFormat = "qwen"
	ThinkingFormatChatTemplate     ThinkingFormat = "chat-template"
	ThinkingFormatQwenChatTemplate ThinkingFormat = "qwen-chat-template"
	ThinkingFormatString           ThinkingFormat = "string-thinking"
	ThinkingFormatAntLing          ThinkingFormat = "ant-ling"
)

type OpenAICompletionsCompat struct {
	SupportsStore                               *bool                  `json:"supportsStore,omitempty"`
	SupportsDeveloperRole                       *bool                  `json:"supportsDeveloperRole,omitempty"`
	SupportsReasoningEffort                     *bool                  `json:"supportsReasoningEffort,omitempty"`
	SupportsUsageInStreaming                    *bool                  `json:"supportsUsageInStreaming,omitempty"`
	MaxTokensField                              *MaxTokensField        `json:"maxTokensField,omitempty"`
	RequiresToolResultName                      *bool                  `json:"requiresToolResultName,omitempty"`
	RequiresAssistantAfterToolResult            *bool                  `json:"requiresAssistantAfterToolResult,omitempty"`
	RequiresThinkingAsText                      *bool                  `json:"requiresThinkingAsText,omitempty"`
	RequiresReasoningContentOnAssistantMessages *bool                  `json:"requiresReasoningContentOnAssistantMessages,omitempty"`
	ThinkingFormat                              *ThinkingFormat        `json:"thinkingFormat,omitempty"`
	ChatTemplateKwargs                          *map[string]any        `json:"chatTemplateKwargs,omitempty"`
	OpenRouterRouting                           *OpenRouterRouting     `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting                        *VercelGatewayRouting  `json:"vercelGatewayRouting,omitempty"`
	ZAIToolStream                               *bool                  `json:"zaiToolStream,omitempty"`
	SupportsStrictMode                          *bool                  `json:"supportsStrictMode,omitempty"`
	CacheControlFormat                          *CacheControlFormat    `json:"cacheControlFormat,omitempty"`
	SendSessionAffinityHeaders                  *bool                  `json:"sendSessionAffinityHeaders,omitempty"`
	DeferredToolsMode                           *DeferredToolsMode     `json:"deferredToolsMode,omitempty"`
	SessionAffinityFormat                       *SessionAffinityFormat `json:"sessionAffinityFormat,omitempty"`
	SupportsLongCacheRetention                  *bool                  `json:"supportsLongCacheRetention,omitempty"`
}

type OpenAIResponsesCompat struct {
	SupportsDeveloperRole      *bool                  `json:"supportsDeveloperRole,omitempty"`
	SessionAffinityFormat      *SessionAffinityFormat `json:"sessionAffinityFormat,omitempty"`
	SupportsLongCacheRetention *bool                  `json:"supportsLongCacheRetention,omitempty"`
	SupportsToolSearch         *bool                  `json:"supportsToolSearch,omitempty"`
}

type AnthropicMessagesCompat struct {
	SupportsEagerToolInputStreaming *bool `json:"supportsEagerToolInputStreaming,omitempty"`
	SupportsLongCacheRetention      *bool `json:"supportsLongCacheRetention,omitempty"`
	SendSessionAffinityHeaders      *bool `json:"sendSessionAffinityHeaders,omitempty"`
	SupportsCacheControlOnTools     *bool `json:"supportsCacheControlOnTools,omitempty"`
	SupportsTemperature             *bool `json:"supportsTemperature,omitempty"`
	ForceAdaptiveThinking           *bool `json:"forceAdaptiveThinking,omitempty"`
	AllowEmptySignature             *bool `json:"allowEmptySignature,omitempty"`
	SupportsToolReferences          *bool `json:"supportsToolReferences,omitempty"`
}

type CacheControlFormat string

const CacheControlAnthropic CacheControlFormat = "anthropic"

type DeferredToolsMode string

const DeferredToolsKimi DeferredToolsMode = "kimi"

type OpenRouterRouting struct {
	AllowFallbacks         *bool           `json:"allow_fallbacks,omitempty"`
	RequireParameters      *bool           `json:"require_parameters,omitempty"`
	DataCollection         *string         `json:"data_collection,omitempty"`
	ZDR                    *bool           `json:"zdr,omitempty"`
	EnforceDistillableText *bool           `json:"enforce_distillable_text,omitempty"`
	Order                  *[]string       `json:"order,omitempty"`
	Only                   *[]string       `json:"only,omitempty"`
	Ignore                 *[]string       `json:"ignore,omitempty"`
	Quantizations          *[]string       `json:"quantizations,omitempty"`
	Sort                   json.RawMessage `json:"sort,omitempty"`
	MaxPrice               json.RawMessage `json:"max_price,omitempty"`
	PreferredMinThroughput json.RawMessage `json:"preferred_min_throughput,omitempty"`
	PreferredMaxLatency    json.RawMessage `json:"preferred_max_latency,omitempty"`
}

type VercelGatewayRouting struct {
	Only  *[]string `json:"only,omitempty"`
	Order *[]string `json:"order,omitempty"`
}

type Tool struct {
	Name        string            `json:"name"`
	Label       string            `json:"label,omitempty"`
	Description string            `json:"description"`
	Parameters  jsonschema.Schema `json:"parameters"`
}

type Context struct {
	SystemPrompt *string     `json:"systemPrompt,omitempty"`
	Messages     MessageList `json:"messages"`
	Tools        *[]Tool     `json:"tools,omitempty"`
}

type ProviderHeaders map[string]*string
type ProviderEnv map[string]string

type ProviderResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

type PayloadHook func(ctx context.Context, payload any, model *Model) (replacement any, replace bool, err error)
type HeadersHook func(ctx context.Context, headers ProviderHeaders, model *Model) (ProviderHeaders, error)
type ResponseHook func(ctx context.Context, response ProviderResponse, model *Model) error

type StreamOptions struct {
	Temperature               *float64        `json:"temperature,omitempty"`
	MaxTokens                 *float64        `json:"maxTokens,omitempty"`
	APIKey                    *string         `json:"apiKey,omitempty"`
	Transport                 *Transport      `json:"transport,omitempty"`
	CacheRetention            *CacheRetention `json:"cacheRetention,omitempty"`
	SessionID                 *string         `json:"sessionId,omitempty"`
	OnPayload                 PayloadHook     `json:"-"`
	TransformHeaders          HeadersHook     `json:"-"`
	OnResponse                ResponseHook    `json:"-"`
	Headers                   ProviderHeaders `json:"headers,omitempty"`
	TimeoutMS                 *int64          `json:"timeoutMs,omitempty"`
	WebSocketConnectTimeoutMS *int64          `json:"websocketConnectTimeoutMs,omitempty"`
	MaxRetries                *int            `json:"maxRetries,omitempty"`
	MaxRetryDelayMS           *int64          `json:"maxRetryDelayMs,omitempty"`
	Metadata                  map[string]any  `json:"metadata,omitempty"`
	Env                       ProviderEnv     `json:"env,omitempty"`
}

type SimpleStreamOptions struct {
	StreamOptions
	Reasoning       *ThinkingLevel   `json:"reasoning,omitempty"`
	ThinkingBudgets *ThinkingBudgets `json:"thinkingBudgets,omitempty"`
}

type Request struct {
	Model   *Model
	Context Context
	Options *StreamOptions
}

type ImagesPayloadHook func(ctx context.Context, payload any, model *ImagesModel) (replacement any, replace bool, err error)
type ImagesResponseHook func(ctx context.Context, response ProviderResponse, model *ImagesModel) error

type ImagesOptions struct {
	APIKey          *string            `json:"apiKey,omitempty"`
	Env             ProviderEnv        `json:"env,omitempty"`
	OnPayload       ImagesPayloadHook  `json:"-"`
	OnResponse      ImagesResponseHook `json:"-"`
	Headers         ProviderHeaders    `json:"headers,omitempty"`
	TimeoutMS       *int64             `json:"timeoutMs,omitempty"`
	MaxRetries      *int               `json:"maxRetries,omitempty"`
	MaxRetryDelayMS *int64             `json:"maxRetryDelayMs,omitempty"`
	Metadata        map[string]any     `json:"metadata,omitempty"`
}

type ImagesRequest struct {
	Model   *ImagesModel
	Context ImagesContext
	Options *ImagesOptions
}

type ImagesFunction func(ctx context.Context, request ImagesRequest) (*AssistantImages, error)
