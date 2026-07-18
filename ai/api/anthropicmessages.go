package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/partialjson"
	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const (
	claudeCodeVersion                             = "2.1.75"
	anthropicFineGrainedToolStreamingBeta         = "fine-grained-tool-streaming-2025-05-14"
	anthropicInterleavedThinkingBeta              = "interleaved-thinking-2025-05-14"
	defaultAnthropicThinkingBudget        float64 = 1024
)

var anthropicHTTPClient option.HTTPClient = http.DefaultClient

type AnthropicEffort string

const (
	AnthropicEffortLow    AnthropicEffort = "low"
	AnthropicEffortMedium AnthropicEffort = "medium"
	AnthropicEffortHigh   AnthropicEffort = "high"
	AnthropicEffortXHigh  AnthropicEffort = "xhigh"
	AnthropicEffortMax    AnthropicEffort = "max"
)

type AnthropicThinkingDisplay string

const (
	AnthropicThinkingSummarized AnthropicThinkingDisplay = "summarized"
	AnthropicThinkingOmitted    AnthropicThinkingDisplay = "omitted"
)

type AnthropicToolChoice struct {
	Type string  `json:"type"`
	Name *string `json:"name,omitempty"`
}

func (choice *AnthropicToolChoice) UnmarshalJSON(data []byte) error {
	var name string
	if json.Unmarshal(data, &name) == nil {
		choice.Type = name
		choice.Name = nil
		return nil
	}
	type plain AnthropicToolChoice
	return json.Unmarshal(data, (*plain)(choice))
}

type AnthropicMessagesOptions struct {
	ai.StreamOptions
	ThinkingEnabled      *bool                     `json:"thinkingEnabled,omitempty"`
	ThinkingBudgetTokens *float64                  `json:"thinkingBudgetTokens,omitempty"`
	Effort               *AnthropicEffort          `json:"effort,omitempty"`
	ThinkingDisplay      *AnthropicThinkingDisplay `json:"thinkingDisplay,omitempty"`
	InterleavedThinking  *bool                     `json:"interleavedThinking,omitempty"`
	ToolChoice           *AnthropicToolChoice      `json:"toolChoice,omitempty"`
	Client               *anthropic.Client         `json:"-"`
}

type AnthropicMessagesPayload struct {
	Model        string                  `json:"model"`
	Messages     []AnthropicMessageParam `json:"messages"`
	MaxTokens    float64                 `json:"max_tokens"`
	Stream       bool                    `json:"stream"`
	System       []anthropicTextBlock    `json:"system,omitempty"`
	Temperature  *float64                `json:"temperature,omitempty"`
	Tools        []anthropicToolParam    `json:"tools,omitempty"`
	Thinking     any                     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig  `json:"output_config,omitempty"`
	Metadata     *anthropicMetadata      `json:"metadata,omitempty"`
	ToolChoice   *AnthropicToolChoice    `json:"tool_choice,omitempty"`
}

type AnthropicMessageParam struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicCacheControl struct {
	Type string  `json:"type"`
	TTL  *string `json:"ttl,omitempty"`
}

type anthropicTextBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicImageBlock struct {
	Type         string                 `json:"type"`
	Source       anthropicImageSource   `json:"source"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

type anthropicRedactedThinkingBlock struct {
	Type string  `json:"type"`
	Data *string `json:"data,omitempty"`
}

type anthropicToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicToolReferenceBlock struct {
	Type     string `json:"type"`
	ToolName string `json:"tool_name"`
}

type anthropicToolResultBlock struct {
	Type         string                 `json:"type"`
	ToolUseID    string                 `json:"tool_use_id"`
	Content      any                    `json:"content"`
	IsError      bool                   `json:"is_error"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicInputSchema struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
	Required   []string        `json:"required"`
}

type anthropicToolParam struct {
	Name                string                 `json:"name"`
	Description         string                 `json:"description"`
	EagerInputStreaming *bool                  `json:"eager_input_streaming,omitempty"`
	InputSchema         anthropicInputSchema   `json:"input_schema"`
	DeferLoading        *bool                  `json:"defer_loading,omitempty"`
	CacheControl        *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicAdaptiveThinking struct {
	Type    string                   `json:"type"`
	Display AnthropicThinkingDisplay `json:"display"`
}

type anthropicEnabledThinking struct {
	Type         string                   `json:"type"`
	BudgetTokens float64                  `json:"budget_tokens"`
	Display      AnthropicThinkingDisplay `json:"display"`
}

type anthropicDisabledThinking struct {
	Type string `json:"type"`
}

type anthropicOutputConfig struct {
	Effort AnthropicEffort `json:"effort"`
}

type anthropicMetadata struct {
	UserID string `json:"user_id"`
}

type resolvedAnthropicCompat struct {
	supportsEagerToolInputStreaming bool
	supportsLongCacheRetention      bool
	sendSessionAffinityHeaders      bool
	supportsCacheControlOnTools     bool
	supportsTemperature             bool
	forceAdaptiveThinking           *bool
	allowEmptySignature             bool
	supportsToolReferences          bool
}

func StreamAnthropicMessages(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: Anthropic Messages model is nil")
	}
	options := &AnthropicMessagesOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamAnthropicMessagesWithOptions(ctx, request.Model, request.Context, options)
}

func StreamSimpleAnthropicMessages(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Anthropic Messages model is nil")
	}
	base := buildBaseStreamOptions(model, requestContext, options)
	if err := assertAnthropicAuth(model, &base); err != nil {
		return nil, err
	}
	if options == nil || options.Reasoning == nil {
		disabled := false
		return StreamAnthropicMessagesWithOptions(ctx, model, requestContext, &AnthropicMessagesOptions{
			StreamOptions:   base,
			ThinkingEnabled: &disabled,
		})
	}

	compat, err := getAnthropicCompat(model)
	if err != nil {
		return nil, err
	}
	enabled := true
	if compat.forceAdaptiveThinking != nil && *compat.forceAdaptiveThinking {
		effort := mapAnthropicEffort(model, *options.Reasoning)
		return StreamAnthropicMessagesWithOptions(ctx, model, requestContext, &AnthropicMessagesOptions{
			StreamOptions:   base,
			ThinkingEnabled: &enabled,
			Effort:          &effort,
		})
	}

	baseMaxTokens := model.MaxTokens
	if base.MaxTokens != nil {
		baseMaxTokens = *base.MaxTokens
	}
	adjustedMax, budget := adjustMaxTokensForThinking(baseMaxTokens, model.MaxTokens, *options.Reasoning, options.ThinkingBudgets)
	adjustedMax = clampMaxTokensToContext(model, requestContext, adjustedMax)
	budget = min(budget, max(float64(0), adjustedMax-defaultAnthropicThinkingBudget))
	base.MaxTokens = &adjustedMax
	return StreamAnthropicMessagesWithOptions(ctx, model, requestContext, &AnthropicMessagesOptions{
		StreamOptions:        base,
		ThinkingEnabled:      &enabled,
		ThinkingBudgetTokens: &budget,
	})
}

func StreamAnthropicMessagesWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *AnthropicMessagesOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Anthropic Messages model is nil")
	}
	output := newAssistantMessage(model)
	streamOptions := anthropicStreamOptions(options)

	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		sink := func(event ai.AssistantMessageEvent) bool { return yield(event, nil) }
		fail := func(err error) {
			clearAnthropicStreamingFields(output)
			sink(anthropicStreamFailure(ctx, output, err))
		}
		if options == nil || options.Client == nil {
			if err := assertAnthropicAuth(model, streamOptions); err != nil {
				fail(err)
				return
			}
		}
		payload, isOAuth, err := buildAnthropicMessagesPayload(model, requestContext, options)
		if err != nil {
			fail(err)
			return
		}
		hookedPayload, err := applyPayloadHook(ctx, model, streamOptions, payload)
		if err != nil {
			fail(err)
			return
		}
		hookedPayload, err = forceAnthropicStreaming(hookedPayload)
		if err != nil {
			fail(err)
			return
		}
		response, err := postAnthropicStream(ctx, model, requestContext, streamOptions, hookedPayload, isOAuth, options)
		if err != nil {
			fail(err)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if !sink(ai.StartEvent{Partial: output}) {
			return
		}

		processor := newAnthropicStreamProcessor(model, requestContext, output, isOAuth, sink)
		err = readAnthropicSSE(response.Body, processor.handleSSE)
		if errors.Is(err, errStopSSE) {
			return
		}
		if err == nil && processor.sawMessageStart && !processor.sawMessageStop {
			err = errors.New("Anthropic stream ended before message_stop") //nolint:staticcheck // Exact upstream error text is observable.
		}
		if err == nil && ctx.Err() != nil {
			err = errors.New("Request was aborted") //nolint:staticcheck // Exact upstream error text is observable.
		}
		if err == nil && (output.StopReason == ai.StopReasonAborted || output.StopReason == ai.StopReasonError) {
			message := "An unknown error occurred"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			err = errors.New(message)
		}
		if err != nil {
			fail(err)
			return
		}
		sink(ai.DoneEvent{Reason: output.StopReason, Message: output})
	}, nil
}

func anthropicStreamOptions(options *AnthropicMessagesOptions) *ai.StreamOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions
}

func forceAnthropicStreaming(payload any) (any, error) {
	switch value := payload.(type) {
	case nil:
		return map[string]any{"stream": true}, nil
	case *AnthropicMessagesPayload:
		copied := *value
		copied.Stream = true
		return &copied, nil
	case AnthropicMessagesPayload:
		value.Stream = true
		return value, nil
	case map[string]any:
		copied := make(map[string]any, len(value)+1)
		for name, field := range value {
			copied[name] = field
		}
		copied["stream"] = true
		return copied, nil
	}
	encoded, err := ai.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic payload hook result: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		if err == nil {
			err = errors.New("payload is not an object")
		}
		return nil, fmt.Errorf("decode Anthropic payload hook result: %w", err)
	}
	object["stream"] = true
	return object, nil
}

func getAnthropicCompat(model *ai.Model) (resolvedAnthropicCompat, error) {
	raw, err := decodeCompat[ai.AnthropicMessagesCompat](model)
	if err != nil {
		return resolvedAnthropicCompat{}, err
	}
	compat := resolvedAnthropicCompat{
		supportsEagerToolInputStreaming: true,
		supportsLongCacheRetention:      true,
		supportsCacheControlOnTools:     true,
		supportsTemperature:             true,
		forceAdaptiveThinking:           raw.ForceAdaptiveThinking,
		supportsToolReferences:          defaultAnthropicToolReferences(model),
	}
	if raw.SupportsEagerToolInputStreaming != nil {
		compat.supportsEagerToolInputStreaming = *raw.SupportsEagerToolInputStreaming
	}
	if raw.SupportsLongCacheRetention != nil {
		compat.supportsLongCacheRetention = *raw.SupportsLongCacheRetention
	}
	if raw.SendSessionAffinityHeaders != nil {
		compat.sendSessionAffinityHeaders = *raw.SendSessionAffinityHeaders
	}
	if raw.SupportsCacheControlOnTools != nil {
		compat.supportsCacheControlOnTools = *raw.SupportsCacheControlOnTools
	}
	if raw.SupportsTemperature != nil {
		compat.supportsTemperature = *raw.SupportsTemperature
	}
	if raw.AllowEmptySignature != nil {
		compat.allowEmptySignature = *raw.AllowEmptySignature
	}
	if raw.SupportsToolReferences != nil {
		compat.supportsToolReferences = *raw.SupportsToolReferences
	}
	return compat, nil
}

func defaultAnthropicToolReferences(model *ai.Model) bool {
	if model.Provider != "anthropic" || strings.Contains(model.ID, "haiku") {
		return false
	}
	parts := strings.Split(model.ID, "-")
	if len(parts) < 3 || parts[0] != "claude" || (parts[1] != "opus" && parts[1] != "sonnet" && parts[1] != "fable") {
		return false
	}
	major, minor, ok := anthropicVersion(parts[2:])
	return ok && (major > 4 || major == 4 && minor >= 5)
}

func anthropicVersion(parts []string) (int, int, bool) {
	if len(parts) == 0 {
		return 0, 0, false
	}
	major, ok := decimalInt(parts[0])
	if !ok {
		return 0, 0, false
	}
	minor := 0
	if len(parts) > 1 && len(parts[1]) < 8 {
		if parsed, valid := decimalInt(parts[1]); valid {
			minor = parsed
		}
	}
	return major, minor, true
}

func decimalInt(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	result := 0
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, false
		}
		result = result*10 + int(char-'0')
	}
	return result, true
}

func anthropicCacheControlFor(model *ai.Model, options *ai.StreamOptions, compat resolvedAnthropicCompat) *anthropicCacheControl {
	retention := resolveCacheRetention(options)
	if retention == ai.CacheRetentionNone {
		return nil
	}
	control := &anthropicCacheControl{Type: "ephemeral"}
	if retention == ai.CacheRetentionLong && compat.supportsLongCacheRetention {
		ttl := "1h"
		control.TTL = &ttl
	}
	return control
}

func buildAnthropicMessagesPayload(
	model *ai.Model,
	requestContext ai.Context,
	options *AnthropicMessagesOptions,
) (*AnthropicMessagesPayload, bool, error) {
	compat, err := getAnthropicCompat(model)
	if err != nil {
		return nil, false, err
	}
	streamOptions := anthropicStreamOptions(options)
	cacheControl := anthropicCacheControlFor(model, streamOptions, compat)
	apiKey := anthropicAPIKey(streamOptions)
	isOAuth := (options == nil || options.Client == nil) && strings.Contains(apiKey, "sk-ant-oat")
	transformed := transformMessages(requestContext.Messages, model, normalizeAnthropicToolCallID)
	normalizeName := func(name string) string { return name }
	if isOAuth {
		normalizeName = toClaudeCodeToolName
	}
	placement := splitAnthropicTools(ai.Context{Messages: transformed, Tools: requestContext.Tools}, compat.supportsToolReferences, normalizeName)
	if len(placement.immediate) == 0 && len(placement.deferred) > 0 {
		placement.immediate = append(placement.immediate, placement.deferred...)
		placement.deferred = nil
	}
	deferredNames := make(map[string]struct{}, len(placement.deferred))
	for _, tool := range placement.deferred {
		deferredNames[normalizeName(tool.Name)] = struct{}{}
	}
	maxTokens := model.MaxTokens
	if streamOptions != nil && streamOptions.MaxTokens != nil {
		maxTokens = *streamOptions.MaxTokens
	}
	messages, err := convertAnthropicMessages(transformed, isOAuth, cacheControl, compat.allowEmptySignature, deferredNames, normalizeName)
	if err != nil {
		return nil, false, err
	}
	payload := &AnthropicMessagesPayload{
		Model:     model.ID,
		Messages:  messages,
		MaxTokens: maxTokens,
		Stream:    true,
	}

	if isOAuth {
		payload.System = append(payload.System, anthropicTextBlock{
			Type:         "text",
			Text:         "You are Claude Code, Anthropic's official CLI for Claude.",
			CacheControl: cacheControl,
		})
	}
	if requestContext.SystemPrompt != nil && *requestContext.SystemPrompt != "" {
		payload.System = append(payload.System, anthropicTextBlock{
			Type:         "text",
			Text:         sanitizeText(*requestContext.SystemPrompt),
			CacheControl: cacheControl,
		})
	}

	thinkingEnabled := options != nil && options.ThinkingEnabled != nil && *options.ThinkingEnabled
	if streamOptions != nil && streamOptions.Temperature != nil && !thinkingEnabled && compat.supportsTemperature {
		payload.Temperature = streamOptions.Temperature
	}
	if len(placement.immediate) > 0 || len(placement.deferred) > 0 {
		immediateCache := cacheControl
		if !compat.supportsCacheControlOnTools {
			immediateCache = nil
		}
		immediate, err := convertAnthropicTools(placement.immediate, isOAuth, compat.supportsEagerToolInputStreaming, immediateCache, false)
		if err != nil {
			return nil, false, err
		}
		deferred, err := convertAnthropicTools(placement.deferred, isOAuth, compat.supportsEagerToolInputStreaming, nil, true)
		if err != nil {
			return nil, false, err
		}
		payload.Tools = append(payload.Tools, immediate...)
		payload.Tools = append(payload.Tools, deferred...)
	}
	if model.Reasoning {
		switch {
		case thinkingEnabled:
			display := AnthropicThinkingSummarized
			if options.ThinkingDisplay != nil {
				display = *options.ThinkingDisplay
			}
			if compat.forceAdaptiveThinking != nil && *compat.forceAdaptiveThinking {
				payload.Thinking = anthropicAdaptiveThinking{Type: "adaptive", Display: display}
				if options.Effort != nil {
					payload.OutputConfig = &anthropicOutputConfig{Effort: *options.Effort}
				}
			} else {
				budget := defaultAnthropicThinkingBudget
				if options.ThinkingBudgetTokens != nil && *options.ThinkingBudgetTokens != 0 {
					budget = *options.ThinkingBudgetTokens
				}
				payload.Thinking = anthropicEnabledThinking{Type: "enabled", BudgetTokens: budget, Display: display}
			}
		case options != nil && options.ThinkingEnabled != nil && !*options.ThinkingEnabled && anthropicThinkingCanDisable(model):
			payload.Thinking = anthropicDisabledThinking{Type: "disabled"}
		}
	}
	if streamOptions != nil {
		if userID, ok := streamOptions.Metadata["user_id"].(string); ok {
			payload.Metadata = &anthropicMetadata{UserID: userID}
		}
	}
	if options != nil {
		payload.ToolChoice = options.ToolChoice
	}
	return payload, isOAuth, nil
}

func anthropicThinkingCanDisable(model *ai.Model) bool {
	if model.ThinkingLevelMap == nil {
		return true
	}
	value, exists := (*model.ThinkingLevelMap)[ai.ModelThinkingOff]
	return !exists || value != nil
}

func mapAnthropicEffort(model *ai.Model, level ai.ThinkingLevel) AnthropicEffort {
	if model.ThinkingLevelMap != nil {
		if mapped, ok := (*model.ThinkingLevelMap)[ai.ModelThinkingLevel(level)]; ok && mapped != nil {
			return AnthropicEffort(*mapped)
		}
	}
	switch level {
	case ai.ThinkingMinimal, ai.ThinkingLow:
		return AnthropicEffortLow
	case ai.ThinkingMedium:
		return AnthropicEffortMedium
	default:
		return AnthropicEffortHigh
	}
}

func normalizeAnthropicToolCallID(id string, _ *ai.Model, _ *ai.AssistantMessage) string {
	units := utf16.Encode([]rune(id))
	var result strings.Builder
	result.Grow(min(len(units), 64))
	for _, unit := range units[:min(len(units), 64)] {
		if unit <= 0x7f && (unit >= 'a' && unit <= 'z' || unit >= 'A' && unit <= 'Z' || unit >= '0' && unit <= '9' || unit == '_' || unit == '-') {
			result.WriteByte(byte(unit))
		} else {
			result.WriteByte('_')
		}
	}
	return result.String()
}

type anthropicToolPlacement struct {
	immediate []ai.Tool
	deferred  []ai.Tool
}

func splitAnthropicTools(requestContext ai.Context, enabled bool, normalizeName func(string) string) anthropicToolPlacement {
	byName := make(map[string]ai.Tool)
	order := make([]string, 0)
	if requestContext.Tools != nil {
		for _, tool := range *requestContext.Tools {
			name := normalizeName(tool.Name)
			if _, exists := byName[name]; !exists {
				order = append(order, name)
			}
			byName[name] = tool
		}
	}
	if !enabled {
		placement := anthropicToolPlacement{immediate: make([]ai.Tool, 0, len(order))}
		for _, name := range order {
			placement.immediate = append(placement.immediate, byName[name])
		}
		return placement
	}

	deferred := make(map[string]struct{})
	used := make(map[string]struct{})
	for _, message := range requestContext.Messages {
		switch message := message.(type) {
		case *ai.AssistantMessage:
			for _, block := range message.Content {
				if call, ok := block.(*ai.ToolCall); ok {
					used[normalizeName(call.Name)] = struct{}{}
				}
			}
		case *ai.ToolResultMessage:
			if message.AddedToolNames == nil {
				continue
			}
			for _, name := range *message.AddedToolNames {
				normalized := normalizeName(name)
				if _, alreadyUsed := used[normalized]; !alreadyUsed {
					deferred[normalized] = struct{}{}
				}
			}
		}
	}
	placement := anthropicToolPlacement{}
	for _, name := range order {
		if _, isDeferred := deferred[name]; isDeferred {
			placement.deferred = append(placement.deferred, byName[name])
		} else {
			placement.immediate = append(placement.immediate, byName[name])
		}
	}
	return placement
}

func convertAnthropicTools(tools []ai.Tool, oauth, eager bool, cacheControl *anthropicCacheControl, deferLoading bool) ([]anthropicToolParam, error) {
	result := make([]anthropicToolParam, 0, len(tools))
	for index, tool := range tools {
		name := tool.Name
		if oauth {
			name = toClaudeCodeToolName(name)
		}
		properties, required, err := anthropicSchemaParts(tool)
		if err != nil {
			return nil, err
		}
		converted := anthropicToolParam{
			Name:        name,
			Description: tool.Description,
			InputSchema: anthropicInputSchema{Type: "object", Properties: properties, Required: required},
		}
		if eager {
			value := true
			converted.EagerInputStreaming = &value
		}
		if deferLoading {
			value := true
			converted.DeferLoading = &value
		}
		if cacheControl != nil && index == len(tools)-1 {
			converted.CacheControl = cacheControl
		}
		result = append(result, converted)
	}
	return result, nil
}

func anthropicSchemaParts(tool ai.Tool) (json.RawMessage, []string, error) {
	var fields struct {
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	data, err := json.Marshal(tool.Parameters)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal Anthropic tool %q schema: %w", tool.Name, err)
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, nil, fmt.Errorf("decode Anthropic tool %q schema: %w", tool.Name, err)
	}
	if len(fields.Properties) == 0 || string(fields.Properties) == "null" {
		fields.Properties = json.RawMessage(`{}`)
	}
	if fields.Required == nil {
		fields.Required = []string{}
	}
	return fields.Properties, fields.Required, nil
}

func convertAnthropicMessages(
	messages ai.MessageList,
	oauth bool,
	cacheControl *anthropicCacheControl,
	allowEmptySignature bool,
	deferredToolNames map[string]struct{},
	normalizeName func(string) string,
) ([]AnthropicMessageParam, error) {
	result := make([]AnthropicMessageParam, 0, len(messages))
	loadedToolNames := make(map[string]struct{})
	for index := 0; index < len(messages); index++ {
		switch message := messages[index].(type) {
		case *ai.UserMessage:
			if message.Content.Text != nil {
				if strings.TrimSpace(*message.Content.Text) != "" {
					result = append(result, AnthropicMessageParam{Role: "user", Content: sanitizeText(*message.Content.Text)})
				}
				continue
			}
			blocks := make([]any, 0, len(message.Content.Blocks))
			for _, content := range message.Content.Blocks {
				switch block := content.(type) {
				case *ai.TextContent:
					if strings.TrimSpace(block.Text) != "" {
						blocks = append(blocks, anthropicTextBlock{Type: "text", Text: sanitizeText(block.Text)})
					}
				case *ai.ImageContent:
					blocks = append(blocks, anthropicImageBlock{Type: "image", Source: anthropicImageSource{
						Type: "base64", MediaType: block.MimeType, Data: block.Data,
					}})
				}
			}
			if len(blocks) > 0 {
				result = append(result, AnthropicMessageParam{Role: "user", Content: blocks})
			}
		case *ai.AssistantMessage:
			blocks := make([]any, 0, len(message.Content))
			for _, content := range message.Content {
				switch block := content.(type) {
				case *ai.TextContent:
					if strings.TrimSpace(block.Text) != "" {
						blocks = append(blocks, anthropicTextBlock{Type: "text", Text: sanitizeText(block.Text)})
					}
				case *ai.ThinkingContent:
					if block.Redacted != nil && *block.Redacted {
						blocks = append(blocks, anthropicRedactedThinkingBlock{Type: "redacted_thinking", Data: block.ThinkingSignature})
						continue
					}
					signature := ""
					if block.ThinkingSignature != nil {
						signature = *block.ThinkingSignature
					}
					hasSignature := strings.TrimSpace(signature) != ""
					if strings.TrimSpace(block.Thinking) == "" && !hasSignature {
						continue
					}
					if hasSignature || allowEmptySignature {
						if !hasSignature {
							signature = ""
						}
						blocks = append(blocks, anthropicThinkingBlock{Type: "thinking", Thinking: sanitizeText(block.Thinking), Signature: signature})
					} else {
						blocks = append(blocks, anthropicTextBlock{Type: "text", Text: sanitizeText(block.Thinking)})
					}
				case *ai.ToolCall:
					name := block.Name
					if oauth {
						name = toClaudeCodeToolName(name)
					}
					arguments, err := ai.MarshalToolCallArguments(block)
					if err != nil {
						return nil, fmt.Errorf("marshal Anthropic tool arguments: %w", err)
					}
					blocks = append(blocks, anthropicToolUseBlock{Type: "tool_use", ID: block.ID, Name: name, Input: arguments})
				}
			}
			if len(blocks) > 0 {
				result = append(result, AnthropicMessageParam{Role: "assistant", Content: blocks})
			}
		case *ai.ToolResultMessage:
			toolResults := make([]any, 0)
			siblingContent := make([]any, 0)
			cursor := index
			for cursor < len(messages) {
				toolResult, ok := messages[cursor].(*ai.ToolResultMessage)
				if !ok {
					break
				}
				converted, siblings := convertAnthropicToolResult(toolResult, oauth, deferredToolNames, loadedToolNames, normalizeName)
				toolResults = append(toolResults, converted)
				siblingContent = append(siblingContent, siblings...)
				cursor++
			}
			index = cursor - 1
			result = append(result, AnthropicMessageParam{Role: "user", Content: append(toolResults, siblingContent...)})
		}
	}
	applyAnthropicLastUserCache(result, cacheControl)
	return result, nil
}

func convertAnthropicToolResult(
	message *ai.ToolResultMessage,
	oauth bool,
	deferredToolNames, loadedToolNames map[string]struct{},
	normalizeName func(string) string,
) (anthropicToolResultBlock, []any) {
	references := make([]any, 0)
	if message.AddedToolNames != nil {
		for _, name := range *message.AddedToolNames {
			normalized := normalizeName(name)
			if _, deferred := deferredToolNames[normalized]; !deferred {
				continue
			}
			if _, loaded := loadedToolNames[normalized]; loaded {
				continue
			}
			loadedToolNames[normalized] = struct{}{}
			if oauth {
				name = toClaudeCodeToolName(name)
			}
			references = append(references, anthropicToolReferenceBlock{Type: "tool_reference", ToolName: name})
		}
	}
	content := convertAnthropicResultContent(message.Content)
	if len(references) == 0 {
		return anthropicToolResultBlock{Type: "tool_result", ToolUseID: message.ToolCallID, Content: content, IsError: message.IsError}, nil
	}
	siblings := make([]any, 0)
	if text, ok := content.(string); ok {
		siblings = append(siblings, anthropicTextBlock{Type: "text", Text: text})
	} else if blocks, ok := content.([]any); ok {
		siblings = append(siblings, blocks...)
	}
	return anthropicToolResultBlock{Type: "tool_result", ToolUseID: message.ToolCallID, Content: references, IsError: message.IsError}, siblings
}

func convertAnthropicResultContent(content ai.ToolResultContent) any {
	hasImages := false
	for _, item := range content {
		if _, ok := item.(*ai.ImageContent); ok {
			hasImages = true
			break
		}
	}
	if !hasImages {
		texts := make([]string, 0, len(content))
		for _, item := range content {
			if text, ok := item.(*ai.TextContent); ok {
				texts = append(texts, text.Text)
			}
		}
		return sanitizeText(strings.Join(texts, "\n"))
	}
	blocks := make([]any, 0, len(content)+1)
	hasText := false
	for _, item := range content {
		switch block := item.(type) {
		case *ai.TextContent:
			hasText = true
			blocks = append(blocks, anthropicTextBlock{Type: "text", Text: sanitizeText(block.Text)})
		case *ai.ImageContent:
			blocks = append(blocks, anthropicImageBlock{Type: "image", Source: anthropicImageSource{
				Type: "base64", MediaType: block.MimeType, Data: block.Data,
			}})
		}
	}
	if !hasText {
		blocks = append([]any{anthropicTextBlock{Type: "text", Text: "(see attached image)"}}, blocks...)
	}
	return blocks
}

func applyAnthropicLastUserCache(messages []AnthropicMessageParam, cacheControl *anthropicCacheControl) {
	if cacheControl == nil || len(messages) == 0 || messages[len(messages)-1].Role != "user" {
		return
	}
	last := &messages[len(messages)-1]
	if text, ok := last.Content.(string); ok {
		last.Content = []any{anthropicTextBlock{Type: "text", Text: text, CacheControl: cacheControl}}
		return
	}
	blocks, ok := last.Content.([]any)
	if !ok || len(blocks) == 0 {
		return
	}
	switch block := blocks[len(blocks)-1].(type) {
	case anthropicTextBlock:
		block.CacheControl = cacheControl
		blocks[len(blocks)-1] = block
	case anthropicImageBlock:
		block.CacheControl = cacheControl
		blocks[len(blocks)-1] = block
	case anthropicToolResultBlock:
		block.CacheControl = cacheControl
		blocks[len(blocks)-1] = block
	}
}

var claudeCodeToolNames = map[string]string{
	"read": "Read", "write": "Write", "edit": "Edit", "bash": "Bash", "grep": "Grep", "glob": "Glob",
	"askuserquestion": "AskUserQuestion", "enterplanmode": "EnterPlanMode", "exitplanmode": "ExitPlanMode",
	"killshell": "KillShell", "notebookedit": "NotebookEdit", "skill": "Skill", "task": "Task",
	"taskoutput": "TaskOutput", "todowrite": "TodoWrite", "webfetch": "WebFetch", "websearch": "WebSearch",
}

func toClaudeCodeToolName(name string) string {
	if canonical, ok := claudeCodeToolNames[strings.ToLower(name)]; ok {
		return canonical
	}
	return name
}

func fromClaudeCodeToolName(name string, tools *[]ai.Tool) string {
	if tools != nil {
		for _, tool := range *tools {
			if strings.EqualFold(tool.Name, name) {
				return tool.Name
			}
		}
	}
	return name
}

func assertAnthropicAuth(model *ai.Model, options *ai.StreamOptions) error {
	if anthropicAPIKey(options) != "" || hasAnthropicAuthHeader(options) {
		return nil
	}
	return fmt.Errorf("No API key for provider: %s", model.Provider) //nolint:staticcheck // Exact upstream error text is observable.
}

func anthropicAPIKey(options *ai.StreamOptions) string {
	if options != nil && options.APIKey != nil {
		return *options.APIKey
	}
	return ""
}

func hasAnthropicAuthHeader(options *ai.StreamOptions) bool {
	if options == nil {
		return false
	}
	for name, value := range options.Headers {
		if value == nil || strings.TrimSpace(*value) == "" {
			continue
		}
		if strings.EqualFold(name, "authorization") || strings.EqualFold(name, "x-api-key") || strings.EqualFold(name, "cf-aig-authorization") {
			return true
		}
	}
	return false
}

func postAnthropicStream(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.StreamOptions,
	payload any,
	oauth bool,
	anthropicOptions *AnthropicMessagesOptions,
) (*http.Response, error) {
	body, err := ai.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Anthropic request: %w", err)
	}
	requestOptions := []option.RequestOption{option.WithMaxRetries(0)}
	if options != nil {
		if options.MaxRetries != nil {
			requestOptions[0] = option.WithMaxRetries(*options.MaxRetries)
		}
		if options.TimeoutMS != nil {
			requestOptions = append(requestOptions, option.WithRequestTimeout(time.Duration(*options.TimeoutMS)*time.Millisecond))
		}
	}
	var client *anthropic.Client
	if anthropicOptions != nil {
		client = anthropicOptions.Client
	}
	if client == nil {
		clientOptions := []option.RequestOption{
			option.WithBaseURL(model.BaseURL),
			option.WithHTTPClient(anthropicHTTPClient),
			option.WithAPIKey(""),
			option.WithAuthToken(""),
			option.WithHeaderDel("X-Api-Key"),
			option.WithHeaderDel("Authorization"),
		}
		apiKey := anthropicAPIKey(options)
		if oauth {
			clientOptions = append(clientOptions, option.WithAuthToken(apiKey))
		} else if apiKey != "" {
			clientOptions = append(clientOptions, option.WithAPIKey(apiKey))
		}
		for name, values := range anthropicHeaders(model, requestContext, options, anthropicOptions) {
			if len(values) == 0 {
				clientOptions = append(clientOptions, option.WithHeaderDel(name))
			} else {
				clientOptions = append(clientOptions, option.WithHeader(name, values[len(values)-1]))
			}
		}
		created := anthropic.NewClient(clientOptions...)
		client = &created
	}
	var response *http.Response
	if err := client.Post(ctx, "v1/messages", json.RawMessage(body), &response, requestOptions...); err != nil {
		return response, normalizeAnthropicRequestError(response, err)
	}
	if response == nil {
		return nil, errors.New("anthropic API returned no HTTP response")
	}
	if options != nil && options.OnResponse != nil {
		if err := options.OnResponse(ctx, providerResponse(response), model); err != nil {
			_ = response.Body.Close()
			return nil, err
		}
	}
	return response, nil
}

func normalizeAnthropicRequestError(response *http.Response, err error) error {
	if err == nil || response == nil {
		return err
	}
	var apiError *anthropic.Error
	if errors.As(err, &apiError) && strings.TrimSpace(apiError.RawJSON()) != "" {
		return fmt.Errorf("%d %s", response.StatusCode, strings.TrimSpace(apiError.RawJSON()))
	}
	contents, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return err
	}
	response.Body = io.NopCloser(strings.NewReader(string(contents)))
	if len(contents) == 0 {
		return fmt.Errorf("%d status code (no body)", response.StatusCode)
	}
	return fmt.Errorf("%d %s", response.StatusCode, strings.TrimSpace(string(contents)))
}

func anthropicHeaders(
	model *ai.Model,
	requestContext ai.Context,
	options *ai.StreamOptions,
	anthropicOptions *AnthropicMessagesOptions,
) http.Header {
	headers := make(http.Header)
	headers.Set("accept", "application/json")
	headers.Set("anthropic-dangerous-direct-browser-access", "true")
	compat, _ := getAnthropicCompat(model)
	interleaved := true
	if anthropicOptions != nil && anthropicOptions.InterleavedThinking != nil {
		interleaved = *anthropicOptions.InterleavedThinking
	}
	betas := make([]string, 0, 4)
	if requestContext.Tools != nil && len(*requestContext.Tools) > 0 && !compat.supportsEagerToolInputStreaming {
		betas = append(betas, anthropicFineGrainedToolStreamingBeta)
	}
	if interleaved && (compat.forceAdaptiveThinking == nil || !*compat.forceAdaptiveThinking) {
		betas = append(betas, anthropicInterleavedThinkingBeta)
	}
	oauth := strings.Contains(anthropicAPIKey(options), "sk-ant-oat")
	if oauth {
		betas = append([]string{"claude-code-20250219", "oauth-2025-04-20"}, betas...)
		headers.Set("user-agent", "claude-cli/"+claudeCodeVersion)
		headers.Set("x-app", "cli")
	}
	if len(betas) > 0 {
		headers.Set("anthropic-beta", strings.Join(betas, ","))
	}
	if !oauth && options != nil && resolveCacheRetention(options) != ai.CacheRetentionNone && options.SessionID != nil && *options.SessionID != "" && compat.sendSessionAffinityHeaders {
		headers.Set("x-session-affinity", *options.SessionID)
	}
	modelHeaders := copyModelHeaders(model)
	for name, values := range modelHeaders {
		headers[name] = append([]string(nil), values...)
	}
	if options != nil {
		for name, value := range options.Headers {
			if value == nil {
				headers[http.CanonicalHeaderKey(name)] = nil
			} else {
				headers.Set(name, *value)
			}
		}
	}
	// TODO(WP-241): add GitHub Copilot bearer authentication and its dynamic headers.
	return headers
}

type anthropicStreamProcessor struct {
	model           *ai.Model
	requestContext  ai.Context
	output          *ai.AssistantMessage
	oauth           bool
	sink            eventSink
	blocks          map[int]anthropicOutputBlock
	sawMessageStart bool
	sawMessageStop  bool
}

type anthropicOutputBlock struct {
	contentIndex int
	text         *ai.TextContent
	thinking     *ai.ThinkingContent
	toolCall     *ai.ToolCall
	partialJSON  string
}

type anthropicRawEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID    string            `json:"id"`
		Usage anthropicRawUsage `json:"usage"`
	} `json:"message"`
	ContentBlock struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
		Data  string          `json:"data"`
	} `json:"content_block"`
	Delta struct {
		Type        string                   `json:"type"`
		Text        string                   `json:"text"`
		Thinking    string                   `json:"thinking"`
		PartialJSON string                   `json:"partial_json"`
		Signature   string                   `json:"signature"`
		StopReason  string                   `json:"stop_reason"`
		StopDetails *anthropicRawStopDetails `json:"stop_details"`
	} `json:"delta"`
	Usage *anthropicRawUsage `json:"usage"`
}

type anthropicRawStopDetails struct {
	Explanation string `json:"explanation"`
}

type anthropicRawUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheCreation            *struct {
		Ephemeral1hInputTokens *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	OutputTokensDetails *struct {
		ThinkingTokens *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

func newAnthropicStreamProcessor(
	model *ai.Model,
	requestContext ai.Context,
	output *ai.AssistantMessage,
	oauth bool,
	sink eventSink,
) *anthropicStreamProcessor {
	return &anthropicStreamProcessor{
		model: model, requestContext: requestContext, output: output, oauth: oauth, sink: sink,
		blocks: make(map[int]anthropicOutputBlock),
	}
}

func (processor *anthropicStreamProcessor) handle(eventName string, data []byte) error {
	return processor.handleSSE(eventName, data, nil)
}

func (processor *anthropicStreamProcessor) handleSSE(eventName string, data []byte, raw []string) error {
	if eventName == "error" {
		return errors.New(string(data))
	}
	switch eventName {
	case "message_start", "message_delta", "message_stop", "content_block_start", "content_block_delta", "content_block_stop":
	default:
		return nil
	}
	var event anthropicRawEvent
	if err := json.Unmarshal(data, &event); err != nil {
		repaired := partialjson.RepairJSON(string(data))
		if repaired != string(data) {
			err = json.Unmarshal([]byte(repaired), &event)
		}
		if err != nil {
			return fmt.Errorf("Could not parse Anthropic SSE event %s: %s; data=%s; raw=%s", eventName, anthropicJSONErrorMessage(err), data, strings.Join(raw, `\n`)) //nolint:staticcheck // Exact upstream prefix is observable.
		}
	}
	switch event.Type {
	case "message_start":
		processor.sawMessageStart = true
		if event.Message.ID != "" {
			processor.output.ResponseID = &event.Message.ID
		}
		processor.applyStartUsage(event.Message.Usage)
	case "message_stop":
		processor.sawMessageStop = true
	case "content_block_start":
		return processor.startBlock(event)
	case "content_block_delta":
		return processor.updateBlock(event)
	case "content_block_stop":
		return processor.stopBlock(event.Index)
	case "message_delta":
		if event.Delta.StopReason != "" {
			reason, message, err := mapAnthropicStopReason(event.Delta.StopReason, event.Delta.StopDetails)
			if err != nil {
				return err
			}
			processor.output.StopReason = reason
			if message != "" {
				processor.output.ErrorMessage = &message
			}
		}
		if event.Usage != nil {
			processor.applyDeltaUsage(*event.Usage)
		}
	}
	return nil
}

func (processor *anthropicStreamProcessor) applyStartUsage(usage anthropicRawUsage) {
	processor.output.Usage.Input = pointerValue(usage.InputTokens)
	processor.output.Usage.Output = pointerValue(usage.OutputTokens)
	processor.output.Usage.CacheRead = pointerValue(usage.CacheReadInputTokens)
	processor.output.Usage.CacheWrite = pointerValue(usage.CacheCreationInputTokens)
	cacheWrite1h := int64(0)
	if usage.CacheCreation != nil {
		cacheWrite1h = pointerValue(usage.CacheCreation.Ephemeral1hInputTokens)
	}
	processor.output.Usage.CacheWrite1h = &cacheWrite1h
	processor.finishUsage()
}

func (processor *anthropicStreamProcessor) applyDeltaUsage(usage anthropicRawUsage) {
	if usage.InputTokens != nil {
		processor.output.Usage.Input = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		processor.output.Usage.Output = *usage.OutputTokens
	}
	if usage.CacheReadInputTokens != nil {
		processor.output.Usage.CacheRead = *usage.CacheReadInputTokens
	}
	if usage.CacheCreationInputTokens != nil {
		processor.output.Usage.CacheWrite = *usage.CacheCreationInputTokens
	}
	if usage.OutputTokensDetails != nil && usage.OutputTokensDetails.ThinkingTokens != nil {
		processor.output.Usage.Reasoning = usage.OutputTokensDetails.ThinkingTokens
	}
	processor.finishUsage()
}

func (processor *anthropicStreamProcessor) finishUsage() {
	usage := &processor.output.Usage
	usage.TotalTokens = usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
	calculateCost(processor.model, usage)
}

func pointerValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func (processor *anthropicStreamProcessor) startBlock(event anthropicRawEvent) error {
	index := event.Index
	slot := anthropicOutputBlock{contentIndex: len(processor.output.Content)}
	switch event.ContentBlock.Type {
	case "text":
		slot.text = &ai.TextContent{Text: "", Index: &index}
		processor.output.Content = append(processor.output.Content, slot.text)
		processor.blocks[index] = slot
		if !processor.sink(ai.TextStartEvent{ContentIndex: slot.contentIndex, Partial: processor.output}) {
			return errStopSSE
		}
	case "thinking":
		signature := ""
		slot.thinking = &ai.ThinkingContent{Thinking: "", ThinkingSignature: &signature, Index: &index}
		processor.output.Content = append(processor.output.Content, slot.thinking)
		processor.blocks[index] = slot
		if !processor.sink(ai.ThinkingStartEvent{ContentIndex: slot.contentIndex, Partial: processor.output}) {
			return errStopSSE
		}
	case "redacted_thinking":
		signature := event.ContentBlock.Data
		redacted := true
		slot.thinking = &ai.ThinkingContent{Thinking: "[Reasoning redacted]", ThinkingSignature: &signature, Redacted: &redacted, Index: &index}
		processor.output.Content = append(processor.output.Content, slot.thinking)
		processor.blocks[index] = slot
		if !processor.sink(ai.ThinkingStartEvent{ContentIndex: slot.contentIndex, Partial: processor.output}) {
			return errStopSSE
		}
	case "tool_use":
		name := event.ContentBlock.Name
		if processor.oauth {
			name = fromClaudeCodeToolName(name, processor.requestContext.Tools)
		}
		scratch := ""
		slot.toolCall = &ai.ToolCall{ID: event.ContentBlock.ID, Name: name, Arguments: map[string]any{}, PartialJSON: &scratch, Index: &index}
		if len(event.ContentBlock.Input) > 0 {
			_ = ai.SetToolCallArgumentsJSON(slot.toolCall, event.ContentBlock.Input)
		}
		processor.output.Content = append(processor.output.Content, slot.toolCall)
		processor.blocks[index] = slot
		if !processor.sink(ai.ToolCallStartEvent{ContentIndex: slot.contentIndex, Partial: processor.output}) {
			return errStopSSE
		}
	}
	return nil
}

func (processor *anthropicStreamProcessor) updateBlock(event anthropicRawEvent) error {
	slot, ok := processor.blocks[event.Index]
	if !ok {
		return nil
	}
	switch event.Delta.Type {
	case "text_delta":
		if slot.text != nil {
			slot.text.Text += event.Delta.Text
			if !processor.sink(ai.TextDeltaEvent{ContentIndex: slot.contentIndex, Delta: event.Delta.Text, Partial: processor.output}) {
				return errStopSSE
			}
		}
	case "thinking_delta":
		if slot.thinking != nil {
			slot.thinking.Thinking += event.Delta.Thinking
			if !processor.sink(ai.ThinkingDeltaEvent{ContentIndex: slot.contentIndex, Delta: event.Delta.Thinking, Partial: processor.output}) {
				return errStopSSE
			}
		}
	case "signature_delta":
		if slot.thinking != nil {
			if slot.thinking.ThinkingSignature == nil {
				empty := ""
				slot.thinking.ThinkingSignature = &empty
			}
			*slot.thinking.ThinkingSignature += event.Delta.Signature
		}
	case "input_json_delta":
		if slot.toolCall != nil {
			slot.partialJSON += event.Delta.PartialJSON
			*slot.toolCall.PartialJSON = slot.partialJSON
			setAnthropicStreamingArguments(slot.toolCall, slot.partialJSON)
			processor.blocks[event.Index] = slot
			if !processor.sink(ai.ToolCallDeltaEvent{ContentIndex: slot.contentIndex, Delta: event.Delta.PartialJSON, Partial: processor.output}) {
				return errStopSSE
			}
		}
	}
	return nil
}

func (processor *anthropicStreamProcessor) stopBlock(index int) error {
	slot, ok := processor.blocks[index]
	if !ok {
		return nil
	}
	delete(processor.blocks, index)
	switch {
	case slot.text != nil:
		slot.text.Index = nil
		if !processor.sink(ai.TextEndEvent{ContentIndex: slot.contentIndex, Content: slot.text.Text, Partial: processor.output}) {
			return errStopSSE
		}
	case slot.thinking != nil:
		slot.thinking.Index = nil
		if !processor.sink(ai.ThinkingEndEvent{ContentIndex: slot.contentIndex, Content: slot.thinking.Thinking, Partial: processor.output}) {
			return errStopSSE
		}
	case slot.toolCall != nil:
		setAnthropicStreamingArguments(slot.toolCall, slot.partialJSON)
		slot.toolCall.PartialJSON = nil
		slot.toolCall.Index = nil
		if !processor.sink(ai.ToolCallEndEvent{ContentIndex: slot.contentIndex, ToolCall: slot.toolCall, Partial: processor.output}) {
			return errStopSSE
		}
	}
	return nil
}

func setAnthropicStreamingArguments(call *ai.ToolCall, partial string) {
	encoded, err := partialjson.StringifyStreamingJSON(partial)
	if err != nil || ai.SetToolCallArgumentsJSON(call, encoded) != nil {
		_ = ai.SetToolCallArgumentsJSON(call, []byte(`{}`))
	}
}

func mapAnthropicStopReason(reason string, details *anthropicRawStopDetails) (ai.StopReason, string, error) {
	switch reason {
	case "end_turn", "pause_turn", "stop_sequence":
		return ai.StopReasonStop, "", nil
	case "max_tokens":
		return ai.StopReasonLength, "", nil
	case "tool_use":
		return ai.StopReasonToolUse, "", nil
	case "refusal":
		message := "The model refused to complete the request"
		if details != nil && details.Explanation != "" {
			message = details.Explanation
		}
		return ai.StopReasonError, message, nil
	case "sensitive":
		return ai.StopReasonError, "", nil
	default:
		return "", "", fmt.Errorf("Unhandled stop reason: %s", reason) //nolint:staticcheck // Exact upstream error text is observable.
	}
}

func anthropicJSONErrorMessage(err error) string {
	if errors.Is(err, io.ErrUnexpectedEOF) || err.Error() == "unexpected end of JSON input" {
		return "Unexpected end of JSON input"
	}
	return err.Error()
}

func readAnthropicSSE(reader io.Reader, handle func(string, []byte, []string) error) error {
	buffered := bufio.NewReader(reader)
	eventName := ""
	data := make([]string, 0)
	raw := make([]string, 0)
	flush := func() error {
		if eventName == "" && len(data) == 0 {
			return nil
		}
		err := handle(eventName, []byte(strings.Join(data, "\n")), raw)
		eventName = ""
		data = data[:0]
		raw = raw[:0]
		return err
	}
	line := make([]byte, 0, 256)
	consumeLine := func() error {
		value := string(line)
		line = line[:0]
		if value == "" {
			return flush()
		}
		raw = append(raw, value)
		if strings.HasPrefix(value, ":") {
			return nil
		}
		name, value, found := strings.Cut(value, ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		switch name {
		case "event":
			eventName = value
		case "data":
			data = append(data, value)
		}
		return nil
	}
	for {
		char, err := buffered.ReadByte()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			if len(line) > 0 {
				if err := consumeLine(); err != nil {
					return err
				}
			}
			return flush()
		}
		if char != '\r' && char != '\n' {
			line = append(line, char)
			continue
		}
		if char == '\r' {
			if next, peekErr := buffered.Peek(1); peekErr == nil && next[0] == '\n' {
				_, _ = buffered.ReadByte()
			}
		}
		if err := consumeLine(); err != nil {
			return err
		}
	}
}

func clearAnthropicStreamingFields(output *ai.AssistantMessage) {
	for _, content := range output.Content {
		switch block := content.(type) {
		case *ai.TextContent:
			block.Index = nil
		case *ai.ThinkingContent:
			block.Index = nil
		case *ai.ToolCall:
			block.Index = nil
			block.PartialJSON = nil
		}
	}
}

func anthropicStreamFailure(ctx context.Context, output *ai.AssistantMessage, err error) ai.ErrorEvent {
	reason := ai.StopReasonError
	message := err.Error()
	if ctx.Err() != nil {
		reason = ai.StopReasonAborted
		message = "Request was aborted"
	}
	output.StopReason = reason
	output.ErrorMessage = &message
	return ai.ErrorEvent{Reason: reason, Error: output}
}
