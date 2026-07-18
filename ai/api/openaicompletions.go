package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonwire"
	"github.com/OrdalieTech/pi-go/internal/partialjson"
)

// OpenAICompletionsOptions contains the Chat Completions-only request options.
type OpenAICompletionsOptions struct {
	ai.StreamOptions
	ToolChoice      any
	ReasoningEffort *ai.ThinkingLevel
}

type resolvedOpenAICompletionsCompat struct {
	supportsStore                               bool
	supportsDeveloperRole                       bool
	supportsReasoningEffort                     bool
	supportsUsageInStreaming                    bool
	maxTokensField                              ai.MaxTokensField
	requiresToolResultName                      bool
	requiresAssistantAfterToolResult            bool
	requiresThinkingAsText                      bool
	requiresReasoningContentOnAssistantMessages bool
	thinkingFormat                              ai.ThinkingFormat
	chatTemplateKwargs                          map[string]any
	chatTemplateKwargOrder                      []string
	openRouterRouting                           *ai.OpenRouterRouting
	vercelGatewayRouting                        *ai.VercelGatewayRouting
	zaiToolStream                               bool
	supportsStrictMode                          bool
	cacheControlFormat                          *ai.CacheControlFormat
	sendSessionAffinityHeaders                  bool
	deferredToolsMode                           *ai.DeferredToolsMode
	sessionAffinityFormat                       ai.SessionAffinityFormat
	supportsLongCacheRetention                  bool
}

type completionsToolState struct {
	block        *ai.ToolCall
	contentIndex int
	partialArgs  string
	streamIndex  *int
}

type completionsStreamState struct {
	output                  *ai.AssistantMessage
	text                    *ai.TextContent
	textIndex               int
	thinking                *ai.ThinkingContent
	thinkingIndex           int
	toolsByIndex            map[int]*completionsToolState
	toolsByID               map[string]*completionsToolState
	toolStates              map[*ai.ToolCall]*completionsToolState
	pendingReasoningDetails map[string]string
	hasFinishReason         bool
}

// StreamOpenAICompletions adapts the provider-neutral request to OpenAI Chat
// Completions. Provider failures are represented by the terminal error event.
func StreamOpenAICompletions(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: OpenAI completions model is nil")
	}
	options := &OpenAICompletionsOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamOpenAICompletionsWithOptions(ctx, request.Model, request.Context, options)
}

// StreamSimpleOpenAICompletions applies the provider-neutral context and
// reasoning clamps before entering the specialized Chat Completions path.
// ToolChoice remains on OpenAICompletionsOptions because SimpleStreamOptions
// has no structurally hidden fields in Go.
func StreamSimpleOpenAICompletions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return StreamOpenAICompletionsWithOptions(ctx, model, requestContext, nil)
	}
	var requestedReasoning *ai.ThinkingLevel
	if options != nil {
		requestedReasoning = options.Reasoning
	}
	return StreamOpenAICompletionsWithOptions(ctx, model, requestContext, &OpenAICompletionsOptions{
		StreamOptions:   buildBaseStreamOptions(model, requestContext, options),
		ReasoningEffort: clampSimpleReasoning(model, requestedReasoning),
	})
}

// StreamOpenAICompletionsWithOptions exposes Chat Completions-specific tool
// choice and reasoning-effort controls.
func StreamOpenAICompletionsWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *OpenAICompletionsOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: OpenAI completions model is nil")
	}
	if options == nil {
		options = &OpenAICompletionsOptions{}
	}

	stream := func(yield func(ai.AssistantMessageEvent, error) bool) {
		output := newAssistantMessage(model)
		if _, err := resolveOpenAIAPIKey(model, &options.StreamOptions); err != nil {
			yield(streamFailure(ctx, output, err, ""), nil)
			return
		}
		compat, err := resolveOpenAICompletionsCompat(model)
		if err != nil {
			yield(streamFailure(ctx, output, err, ""), nil)
			return
		}
		retention := resolveCacheRetention(&options.StreamOptions)
		payload, err := buildOpenAICompletionsPayload(model, requestContext, options, compat, retention)
		if err != nil {
			yield(streamFailure(ctx, output, err, ""), nil)
			return
		}
		payloadValue, err := applyPayloadHook(ctx, model, &options.StreamOptions, payload)
		if err != nil {
			yield(streamFailure(ctx, output, err, ""), nil)
			return
		}
		payloadValue = openAICompletionsWireValue(payloadValue, compat.chatTemplateKwargOrder)

		headers := buildOpenAICompletionsHeaders(model, requestContext, &options.StreamOptions, compat, retention)
		headers, err = applyHeadersHook(ctx, model, &options.StreamOptions, headers)
		if err != nil {
			yield(streamFailure(ctx, output, err, ""), nil)
			return
		}
		response, err := postOpenAIStream(
			ctx,
			model,
			&options.StreamOptions,
			"chat/completions",
			payloadValue,
			headers,
		)
		if err != nil {
			yield(streamFailure(ctx, output, err, ""), nil)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if !yield(ai.StartEvent{Partial: output}, nil) {
			return
		}

		state := newCompletionsStreamState(output)
		err = readSSE(response.Body, func(raw json.RawMessage) error {
			return state.consumeChunk(model, raw, func(event ai.AssistantMessageEvent) error {
				if !yield(event, nil) {
					return errStopSSE
				}
				return nil
			})
		})
		if errors.Is(err, errStopSSE) {
			return
		}
		if err != nil {
			state.clearScratch()
			yield(streamFailure(ctx, output, err, ""), nil)
			return
		}

		if err := state.finishBlocks(func(event ai.AssistantMessageEvent) error {
			if !yield(event, nil) {
				return errStopSSE
			}
			return nil
		}); err != nil {
			return
		}
		if ctx.Err() != nil {
			yield(streamFailure(ctx, output, errors.New("Request was aborted"), ""), nil) //nolint:staticcheck // Exact upstream error text is observable.
			return
		}
		if output.StopReason == ai.StopReasonError {
			message := "Provider returned an error stop reason"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			yield(streamFailure(ctx, output, errors.New(message), ""), nil)
			return
		}
		if !state.hasFinishReason {
			yield(streamFailure(ctx, output, errors.New("Stream ended without finish_reason"), ""), nil) //nolint:staticcheck // Exact upstream error text is observable.
			return
		}
		yield(ai.DoneEvent{Reason: output.StopReason, Message: output}, nil)
	}
	return stream, nil
}

// openAICompletionsWirePayload preserves the property order produced by
// upstream's object construction. Hooks still receive the mutable map above;
// wrapping happens only after the hook has returned.
type openAICompletionsWirePayload struct {
	value map[string]any
}

type openAICompletionsWireObject struct {
	value     map[string]any
	preferred []string
}

func openAICompletionsWireValue(value any, chatTemplateKwargOrder []string) any {
	if object, ok := value.(map[string]any); ok {
		if kwargs, ok := object["chat_template_kwargs"].(map[string]any); ok && len(chatTemplateKwargOrder) > 0 {
			wrapped := make(map[string]any, len(object))
			for key, item := range object {
				wrapped[key] = item
			}
			wrapped["chat_template_kwargs"] = openAICompletionsWireObject{
				value:     kwargs,
				preferred: chatTemplateKwargOrder,
			}
			object = wrapped
		}
		return openAICompletionsWirePayload{value: object}
	}
	return value
}

func (payload openAICompletionsWirePayload) MarshalJSON() ([]byte, error) {
	return marshalOpenAICompletionsObject(payload.value, true)
}

func (object openAICompletionsWireObject) MarshalJSON() ([]byte, error) {
	return marshalOpenAICompletionsObjectWithKeys(
		object.value,
		orderedOpenAICompletionsKeys(object.value, object.preferred),
	)
}

func marshalOpenAICompletionsValue(value any) ([]byte, error) {
	switch typed := value.(type) {
	case string:
		return jsonwire.MarshalString(typed)
	case map[string]any:
		return marshalOpenAICompletionsObject(typed, false)
	case []any:
		var output bytes.Buffer
		output.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				output.WriteByte(',')
			}
			encoded, err := marshalOpenAICompletionsValue(item)
			if err != nil {
				return nil, err
			}
			output.Write(encoded)
		}
		output.WriteByte(']')
		return output.Bytes(), nil
	default:
		return ai.Marshal(value)
	}
}

func marshalOpenAICompletionsObject(object map[string]any, root bool) ([]byte, error) {
	return marshalOpenAICompletionsObjectWithKeys(object, openAICompletionsObjectKeys(object, root))
}

func marshalOpenAICompletionsObjectWithKeys(object map[string]any, keys []string) ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			output.WriteByte(',')
		}
		encodedKey, err := ai.Marshal(key)
		if err != nil {
			return nil, err
		}
		encodedValue, err := marshalOpenAICompletionsValue(object[key])
		if err != nil {
			return nil, fmt.Errorf("encode OpenAI completions field %q: %w", key, err)
		}
		output.Write(encodedKey)
		output.WriteByte(':')
		output.Write(encodedValue)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func openAICompletionsObjectKeys(object map[string]any, root bool) []string {
	if root {
		return orderedOpenAICompletionsKeys(object, []string{
			"model", "messages", "stream", "prompt_cache_key", "prompt_cache_retention",
			"stream_options", "store", "max_tokens", "max_completion_tokens", "temperature",
			"tools", "tool_stream", "tool_choice", "thinking", "enable_thinking",
			"chat_template_kwargs", "reasoning", "reasoning_effort", "provider", "providerOptions",
		})
	}
	if role, ok := object["role"].(string); ok {
		switch role {
		case "assistant":
			preferred := []string{"role", "content"}
			if reasoning, ok := object["reasoning_content"].(string); ok && reasoning != "" {
				preferred = append(preferred, "reasoning_content")
			}
			reserved := map[string]bool{
				"role": true, "content": true, "tool_calls": true,
				"reasoning_details": true, "reasoning_content": true,
			}
			dynamic := make([]string, 0)
			for key := range object {
				if !reserved[key] {
					dynamic = append(dynamic, key)
				}
			}
			sort.Strings(dynamic)
			preferred = append(preferred, dynamic...)
			preferred = append(preferred, "tool_calls", "reasoning_details", "reasoning_content")
			return orderedOpenAICompletionsKeys(object, preferred)
		case "tool":
			return orderedOpenAICompletionsKeys(object, []string{"role", "content", "tool_call_id", "name"})
		default:
			return orderedOpenAICompletionsKeys(object, []string{"role", "content", "tools"})
		}
	}
	if _, hasID := object["id"]; hasID {
		if _, hasFunction := object["function"]; hasFunction {
			return orderedOpenAICompletionsKeys(object, []string{"id", "type", "function"})
		}
	}
	if kind, ok := object["type"].(string); ok {
		switch kind {
		case "text":
			return orderedOpenAICompletionsKeys(object, []string{"type", "text", "cache_control"})
		case "image_url":
			return orderedOpenAICompletionsKeys(object, []string{"type", "image_url"})
		case "function":
			return orderedOpenAICompletionsKeys(object, []string{"type", "function", "cache_control"})
		case "ephemeral":
			return orderedOpenAICompletionsKeys(object, []string{"type", "ttl"})
		case "enabled", "disabled":
			return orderedOpenAICompletionsKeys(object, []string{"type", "clear_thinking"})
		case "reasoning.encrypted":
			return orderedOpenAICompletionsKeys(object, []string{"type", "id", "data"})
		}
	}
	if _, hasName := object["name"]; hasName {
		if _, hasArguments := object["arguments"]; hasArguments {
			return orderedOpenAICompletionsKeys(object, []string{"name", "arguments"})
		}
		if _, hasParameters := object["parameters"]; hasParameters {
			return orderedOpenAICompletionsKeys(object, []string{"name", "description", "parameters", "strict"})
		}
	}
	return orderedOpenAICompletionsKeys(object, nil)
}

func orderedOpenAICompletionsKeys(object map[string]any, preferred []string) []string {
	keys := make([]string, 0, len(object))
	seen := make(map[string]bool, len(preferred))
	for _, key := range preferred {
		if _, exists := object[key]; exists && !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	remainder := make([]string, 0, len(object)-len(keys))
	for key := range object {
		if !seen[key] {
			remainder = append(remainder, key)
		}
	}
	sort.Strings(remainder)
	return append(keys, remainder...)
}

func resolveOpenAICompletionsCompat(model *ai.Model) (resolvedOpenAICompletionsCompat, error) {
	resolved := detectOpenAICompletionsCompat(model)
	overrides, err := decodeCompat[ai.OpenAICompletionsCompat](model)
	if err != nil {
		return resolved, err
	}
	if overrides.SupportsStore != nil {
		resolved.supportsStore = *overrides.SupportsStore
	}
	if overrides.SupportsDeveloperRole != nil {
		resolved.supportsDeveloperRole = *overrides.SupportsDeveloperRole
	}
	if overrides.SupportsReasoningEffort != nil {
		resolved.supportsReasoningEffort = *overrides.SupportsReasoningEffort
	}
	if overrides.SupportsUsageInStreaming != nil {
		resolved.supportsUsageInStreaming = *overrides.SupportsUsageInStreaming
	}
	if overrides.MaxTokensField != nil {
		resolved.maxTokensField = *overrides.MaxTokensField
	}
	if overrides.RequiresToolResultName != nil {
		resolved.requiresToolResultName = *overrides.RequiresToolResultName
	}
	if overrides.RequiresAssistantAfterToolResult != nil {
		resolved.requiresAssistantAfterToolResult = *overrides.RequiresAssistantAfterToolResult
	}
	if overrides.RequiresThinkingAsText != nil {
		resolved.requiresThinkingAsText = *overrides.RequiresThinkingAsText
	}
	if overrides.RequiresReasoningContentOnAssistantMessages != nil {
		resolved.requiresReasoningContentOnAssistantMessages = *overrides.RequiresReasoningContentOnAssistantMessages
	}
	if overrides.ThinkingFormat != nil {
		resolved.thinkingFormat = *overrides.ThinkingFormat
	}
	if overrides.ChatTemplateKwargs != nil {
		resolved.chatTemplateKwargs = *overrides.ChatTemplateKwargs
		resolved.chatTemplateKwargOrder, err = openAICompletionsChatTemplateKwargOrder(model.Compat)
		if err != nil {
			return resolved, fmt.Errorf("decode %s compat chatTemplateKwargs order: %w", model.Provider, err)
		}
	}
	resolved.openRouterRouting = overrides.OpenRouterRouting
	resolved.vercelGatewayRouting = overrides.VercelGatewayRouting
	if overrides.ZAIToolStream != nil {
		resolved.zaiToolStream = *overrides.ZAIToolStream
	}
	if overrides.SupportsStrictMode != nil {
		resolved.supportsStrictMode = *overrides.SupportsStrictMode
	}
	if overrides.CacheControlFormat != nil {
		resolved.cacheControlFormat = overrides.CacheControlFormat
	}
	if overrides.SendSessionAffinityHeaders != nil {
		resolved.sendSessionAffinityHeaders = *overrides.SendSessionAffinityHeaders
	}
	if overrides.DeferredToolsMode != nil {
		resolved.deferredToolsMode = overrides.DeferredToolsMode
	}
	if overrides.SessionAffinityFormat != nil {
		resolved.sessionAffinityFormat = *overrides.SessionAffinityFormat
	}
	if overrides.SupportsLongCacheRetention != nil {
		resolved.supportsLongCacheRetention = *overrides.SupportsLongCacheRetention
	}
	return resolved, nil
}

func openAICompletionsChatTemplateKwargOrder(compat json.RawMessage) ([]string, error) {
	var raw struct {
		ChatTemplateKwargs json.RawMessage `json:"chatTemplateKwargs"`
	}
	if err := json.Unmarshal(compat, &raw); err != nil {
		return nil, err
	}
	if len(raw.ChatTemplateKwargs) == 0 || string(raw.ChatTemplateKwargs) == "null" {
		return nil, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(raw.ChatTemplateKwargs))
	opening, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("chatTemplateKwargs is not an object")
	}

	order := make([]string, 0)
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("chatTemplateKwargs key is not a string")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if !seen[name] {
			seen[name] = true
			order = append(order, name)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}

	sort.SliceStable(order, func(left, right int) bool {
		leftIndex, leftIsIndex := openAICompletionsArrayIndex(order[left])
		rightIndex, rightIsIndex := openAICompletionsArrayIndex(order[right])
		if leftIsIndex && rightIsIndex {
			return leftIndex < rightIndex
		}
		return leftIsIndex && !rightIsIndex
	})
	return order, nil
}

func openAICompletionsArrayIndex(name string) (uint64, bool) {
	if name == "0" {
		return 0, true
	}
	if name == "" || name[0] == '0' {
		return 0, false
	}
	value, err := strconv.ParseUint(name, 10, 32)
	if err != nil || value == uint64(1<<32)-1 || strconv.FormatUint(value, 10) != name {
		return 0, false
	}
	return value, true
}

func detectOpenAICompletionsCompat(model *ai.Model) resolvedOpenAICompletionsCompat {
	provider := string(model.Provider)
	baseURL := model.BaseURL
	isZAI := provider == "zai" || provider == "zai-coding-cn" || strings.Contains(baseURL, "api.z.ai") || strings.Contains(baseURL, "open.bigmodel.cn")
	isTogether := provider == "together" || strings.Contains(baseURL, "api.together.ai") || strings.Contains(baseURL, "api.together.xyz")
	isMoonshot := provider == "moonshotai" || provider == "moonshotai-cn" || strings.Contains(baseURL, "api.moonshot.")
	isOpenRouter := provider == "openrouter" || strings.Contains(baseURL, "openrouter.ai")
	isCloudflareWorkers := provider == "cloudflare-workers-ai" || strings.Contains(baseURL, "api.cloudflare.com")
	isCloudflareGateway := provider == "cloudflare-ai-gateway" || strings.Contains(baseURL, "gateway.ai.cloudflare.com")
	isNVIDIA := provider == "nvidia" || strings.Contains(baseURL, "integrate.api.nvidia.com")
	isAntLing := provider == "ant-ling" || strings.Contains(baseURL, "api.ant-ling.com")
	isNonStandard := isNVIDIA || provider == "cerebras" || strings.Contains(baseURL, "cerebras.ai") ||
		provider == "xai" || strings.Contains(baseURL, "api.x.ai") || isTogether || strings.Contains(baseURL, "chutes.ai") ||
		strings.Contains(baseURL, "deepseek.com") || isZAI || isMoonshot || provider == "opencode" ||
		strings.Contains(baseURL, "opencode.ai") || isCloudflareWorkers || isCloudflareGateway || isAntLing
	useLegacyMax := strings.Contains(baseURL, "chutes.ai") || isMoonshot || isCloudflareGateway || isTogether || isNVIDIA || isAntLing
	isGrok := provider == "xai" || strings.Contains(baseURL, "api.x.ai")
	isDeepSeek := provider == "deepseek" || strings.Contains(baseURL, "deepseek.com")
	isOpenRouterDeveloperModel := isOpenRouter && (strings.HasPrefix(model.ID, "anthropic/") || strings.HasPrefix(model.ID, "openai/"))

	thinkingFormat := ai.ThinkingFormatOpenAI
	switch {
	case isDeepSeek:
		thinkingFormat = ai.ThinkingFormatDeepSeek
	case isZAI:
		thinkingFormat = ai.ThinkingFormatZAI
	case isTogether:
		thinkingFormat = ai.ThinkingFormatTogether
	case isAntLing:
		thinkingFormat = ai.ThinkingFormatAntLing
	case isOpenRouter:
		thinkingFormat = ai.ThinkingFormatOpenRouter
	}
	maxTokensField := ai.MaxTokensFieldCompletion
	if useLegacyMax {
		maxTokensField = ai.MaxTokensFieldLegacy
	}
	sessionFormat := ai.SessionAffinityOpenAI
	if isOpenRouter {
		sessionFormat = ai.SessionAffinityOpenRouter
	}
	var cacheControl *ai.CacheControlFormat
	if provider == "openrouter" && strings.HasPrefix(model.ID, "anthropic/") {
		value := ai.CacheControlAnthropic
		cacheControl = &value
	}
	return resolvedOpenAICompletionsCompat{
		supportsStore:                               !isNonStandard,
		supportsDeveloperRole:                       isOpenRouterDeveloperModel || (!isNonStandard && !isOpenRouter),
		supportsReasoningEffort:                     !isGrok && !isZAI && !isMoonshot && !isTogether && !isCloudflareGateway && !isNVIDIA && !isAntLing,
		supportsUsageInStreaming:                    true,
		maxTokensField:                              maxTokensField,
		thinkingFormat:                              thinkingFormat,
		chatTemplateKwargs:                          map[string]any{},
		supportsStrictMode:                          !isMoonshot && !isTogether && !isCloudflareGateway && !isNVIDIA,
		cacheControlFormat:                          cacheControl,
		sessionAffinityFormat:                       sessionFormat,
		supportsLongCacheRetention:                  !isTogether && !isCloudflareWorkers && !isCloudflareGateway && !isNVIDIA && !isAntLing,
		requiresReasoningContentOnAssistantMessages: isDeepSeek,
	}
}

func buildOpenAICompletionsHeaders(
	model *ai.Model,
	requestContext ai.Context,
	options *ai.StreamOptions,
	compat resolvedOpenAICompletionsCompat,
	retention ai.CacheRetention,
) http.Header {
	headers := copyModelHeaders(model)
	addCopilotHeaders(headers, model, requestContext)
	if options != nil && options.SessionID != nil && *options.SessionID != "" && retention != ai.CacheRetentionNone && compat.sendSessionAffinityHeaders {
		sessionID := *options.SessionID
		switch compat.sessionAffinityFormat {
		case ai.SessionAffinityOpenRouter:
			headers.Set("x-session-id", sessionID)
		default:
			if compat.sessionAffinityFormat == ai.SessionAffinityOpenAI {
				headers.Set("session_id", sessionID)
			}
			headers.Set("x-client-request-id", sessionID)
			headers.Set("x-session-affinity", sessionID)
		}
	}
	if options != nil {
		mergeProviderHeaders(headers, options.Headers)
		for name, value := range options.Headers {
			if value == nil {
				headers[http.CanonicalHeaderKey(name)] = nil
			}
		}
	}
	return headers
}

func buildOpenAICompletionsPayload(
	model *ai.Model,
	requestContext ai.Context,
	options *OpenAICompletionsOptions,
	compat resolvedOpenAICompletionsCompat,
	retention ai.CacheRetention,
) (map[string]any, error) {
	messages, err := convertOpenAICompletionsMessages(model, requestContext, compat)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"model":    model.ID,
		"messages": messages,
		"stream":   true,
	}
	if (strings.Contains(model.BaseURL, "api.openai.com") && retention != ai.CacheRetentionNone) ||
		(retention == ai.CacheRetentionLong && compat.supportsLongCacheRetention) {
		if value := clampOpenAIPromptCacheKey(options.SessionID); value != nil {
			payload["prompt_cache_key"] = value
		}
	}
	if retention == ai.CacheRetentionLong && compat.supportsLongCacheRetention {
		payload["prompt_cache_retention"] = "24h"
	}
	if compat.supportsUsageInStreaming {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	if compat.supportsStore {
		payload["store"] = false
	}
	if options.MaxTokens != nil && *options.MaxTokens != 0 {
		payload[string(compat.maxTokensField)] = *options.MaxTokens
	}
	if options.Temperature != nil {
		payload["temperature"] = *options.Temperature
	}

	deferredNames := deferredOpenAICompletionsToolNames(requestContext.Messages, compat)
	activeTools := activeOpenAICompletionsTools(requestContext.Tools, deferredNames)
	var tools []any
	if len(activeTools) > 0 {
		tools = convertOpenAICompletionsTools(activeTools, compat)
		payload["tools"] = tools
		if compat.zaiToolStream {
			payload["tool_stream"] = true
		}
	} else if hasOpenAICompletionsToolHistory(requestContext.Messages) {
		tools = []any{}
		payload["tools"] = tools
	}
	if cacheControl := openAICompletionsCacheControl(compat, retention); cacheControl != nil {
		applyOpenAICompletionsCacheControl(messages, tools, cacheControl)
	}
	if options.ToolChoice != nil {
		payload["tool_choice"] = options.ToolChoice
	}
	applyOpenAICompletionsThinking(payload, model, options, compat)
	if compat.openRouterRouting != nil {
		payload["provider"] = compat.openRouterRouting
	}
	if compat.vercelGatewayRouting != nil && (compat.vercelGatewayRouting.Only != nil || compat.vercelGatewayRouting.Order != nil) {
		gateway := map[string]any{}
		if compat.vercelGatewayRouting.Only != nil {
			gateway["only"] = *compat.vercelGatewayRouting.Only
		}
		if compat.vercelGatewayRouting.Order != nil {
			gateway["order"] = *compat.vercelGatewayRouting.Order
		}
		payload["providerOptions"] = map[string]any{"gateway": gateway}
	}
	return payload, nil
}

func normalizeOpenAICompletionsToolCallID(id string, model *ai.Model, _ *ai.AssistantMessage) string {
	if separator := strings.IndexByte(id, '|'); separator >= 0 {
		units := utf16.Encode([]rune(id[:separator]))
		var normalized strings.Builder
		for _, unit := range units {
			if (unit >= 'a' && unit <= 'z') || (unit >= 'A' && unit <= 'Z') ||
				(unit >= '0' && unit <= '9') || unit == '_' || unit == '-' {
				normalized.WriteByte(byte(unit))
			} else {
				normalized.WriteByte('_')
			}
		}
		return truncateASCII(normalized.String(), 40)
	}
	if model.Provider == "openai" {
		return truncateOpenAICompletionsUTF16(id, 40)
	}
	return id
}

func truncateOpenAICompletionsUTF16(value string, limit int) string {
	units := utf16.Encode([]rune(value))
	if len(units) <= limit {
		return value
	}
	units = units[:limit]
	var result strings.Builder
	for index := 0; index < len(units); index++ {
		unit := units[index]
		if unit >= 0xd800 && unit <= 0xdbff && index+1 < len(units) && units[index+1] >= 0xdc00 && units[index+1] <= 0xdfff {
			result.WriteRune(utf16.DecodeRune(rune(unit), rune(units[index+1])))
			index++
			continue
		}
		if unit >= 0xd800 && unit <= 0xdfff {
			result.WriteByte(byte(0xe0 | unit>>12))
			result.WriteByte(byte(0x80 | unit>>6&0x3f))
			result.WriteByte(byte(0x80 | unit&0x3f))
			continue
		}
		result.WriteRune(rune(unit))
	}
	return result.String()
}

func truncateASCII(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func convertOpenAICompletionsMessages(
	model *ai.Model,
	requestContext ai.Context,
	compat resolvedOpenAICompletionsCompat,
) ([]any, error) {
	transformed := transformMessages(requestContext.Messages, model, normalizeOpenAICompletionsToolCallID)
	messages := make([]any, 0, len(transformed)+1)
	if requestContext.SystemPrompt != nil && *requestContext.SystemPrompt != "" {
		role := "system"
		if model.Reasoning && compat.supportsDeveloperRole {
			role = "developer"
		}
		messages = append(messages, map[string]any{"role": role, "content": sanitizeText(*requestContext.SystemPrompt)})
	}

	lastRole := ""
	for index := 0; index < len(transformed); index++ {
		switch message := transformed[index].(type) {
		case *ai.UserMessage:
			if compat.requiresAssistantAfterToolResult && lastRole == "toolResult" {
				messages = append(messages, map[string]any{"role": "assistant", "content": "I have processed the tool results."})
			}
			converted, include := convertOpenAICompletionsUserMessage(message)
			if !include {
				continue
			}
			messages = append(messages, converted)
			lastRole = "user"
		case *ai.AssistantMessage:
			converted, include, err := convertOpenAICompletionsAssistantMessage(model, message, compat)
			if err != nil {
				return nil, err
			}
			if !include {
				continue
			}
			messages = append(messages, converted)
			lastRole = "assistant"
		case *ai.ToolResultMessage:
			end := index
			imageParts := make([]any, 0)
			deferredNames := make([]string, 0)
			seenDeferred := map[string]bool{}
			for end < len(transformed) {
				toolResult, ok := transformed[end].(*ai.ToolResultMessage)
				if !ok {
					break
				}
				converted, images := convertOpenAICompletionsToolResult(model, toolResult, compat)
				messages = append(messages, converted)
				imageParts = append(imageParts, images...)
				if compat.deferredToolsMode != nil && *compat.deferredToolsMode == ai.DeferredToolsKimi && toolResult.AddedToolNames != nil {
					for _, name := range *toolResult.AddedToolNames {
						if !seenDeferred[name] {
							seenDeferred[name] = true
							deferredNames = append(deferredNames, name)
						}
					}
				}
				end++
			}
			index = end - 1
			if len(imageParts) > 0 {
				if compat.requiresAssistantAfterToolResult {
					messages = append(messages, map[string]any{"role": "assistant", "content": "I have processed the tool results."})
				}
				content := []any{map[string]any{"type": "text", "text": "Attached image(s) from tool result:"}}
				content = append(content, imageParts...)
				messages = append(messages, map[string]any{"role": "user", "content": content})
				lastRole = "user"
			} else {
				lastRole = "toolResult"
			}
			if len(deferredNames) > 0 {
				deferredTools := toolsByName(requestContext.Tools, deferredNames)
				if len(deferredTools) > 0 {
					messages = append(messages, map[string]any{
						"role":  "system",
						"tools": convertOpenAICompletionsTools(deferredTools, compat),
					})
				}
			}
		}
	}
	return messages, nil
}

func convertOpenAICompletionsUserMessage(message *ai.UserMessage) (map[string]any, bool) {
	if message.Content.Text != nil {
		return map[string]any{"role": "user", "content": sanitizeText(*message.Content.Text)}, true
	}
	if len(message.Content.Blocks) == 0 {
		return nil, false
	}
	content := make([]any, 0, len(message.Content.Blocks))
	for _, rawBlock := range message.Content.Blocks {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			content = append(content, map[string]any{"type": "text", "text": sanitizeText(block.Text)})
		case *ai.ImageContent:
			content = append(content, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": "data:" + block.MimeType + ";base64," + block.Data},
			})
		}
	}
	if len(content) == 0 {
		return nil, false
	}
	return map[string]any{"role": "user", "content": content}, true
}

func convertOpenAICompletionsAssistantMessage(
	model *ai.Model,
	message *ai.AssistantMessage,
	compat resolvedOpenAICompletionsCompat,
) (map[string]any, bool, error) {
	contentValue := any(nil)
	if compat.requiresAssistantAfterToolResult {
		contentValue = ""
	}
	converted := map[string]any{"role": "assistant", "content": contentValue}
	textParts := make([]any, 0)
	textValues := make([]string, 0)
	thinkingBlocks := make([]*ai.ThinkingContent, 0)
	toolCalls := make([]*ai.ToolCall, 0)
	for _, rawBlock := range message.Content {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			if strings.TrimSpace(block.Text) != "" {
				text := sanitizeText(block.Text)
				textParts = append(textParts, map[string]any{"type": "text", "text": text})
				textValues = append(textValues, text)
			}
		case *ai.ThinkingContent:
			if strings.TrimSpace(block.Thinking) != "" {
				thinkingBlocks = append(thinkingBlocks, block)
			}
		case *ai.ToolCall:
			toolCalls = append(toolCalls, block)
		}
	}
	assistantText := strings.Join(textValues, "")
	if len(thinkingBlocks) > 0 {
		if compat.requiresThinkingAsText {
			thinkingValues := make([]string, 0, len(thinkingBlocks))
			for _, block := range thinkingBlocks {
				thinkingValues = append(thinkingValues, sanitizeText(block.Thinking))
			}
			parts := []any{map[string]any{"type": "text", "text": strings.Join(thinkingValues, "\n\n")}}
			parts = append(parts, textParts...)
			converted["content"] = parts
		} else {
			if assistantText != "" {
				converted["content"] = assistantText
			}
			signature := thinkingBlocks[0].ThinkingSignature
			if signature != nil && model.Provider == "opencode-go" && *signature == "reasoning" {
				value := "reasoning_content"
				signature = &value
			}
			if signature != nil && *signature != "" {
				values := make([]string, 0, len(thinkingBlocks))
				for _, block := range thinkingBlocks {
					values = append(values, block.Thinking)
				}
				converted[*signature] = strings.Join(values, "\n")
			}
		}
	} else if assistantText != "" {
		converted["content"] = assistantText
	}

	if len(toolCalls) > 0 {
		convertedCalls := make([]any, 0, len(toolCalls))
		reasoningDetails := make([]any, 0)
		for _, call := range toolCalls {
			encoded, err := ai.MarshalToolCallArguments(call)
			if err != nil {
				return nil, false, fmt.Errorf("marshal tool call %s arguments: %w", call.ID, err)
			}
			convertedCalls = append(convertedCalls, map[string]any{
				"id":       call.ID,
				"type":     "function",
				"function": map[string]any{"name": call.Name, "arguments": string(encoded)},
			})
			if call.ThoughtSignature != nil {
				var detail any
				if json.Unmarshal([]byte(*call.ThoughtSignature), &detail) == nil && jsTruthy(detail) {
					reasoningDetails = append(reasoningDetails, detail)
				}
			}
		}
		converted["tool_calls"] = convertedCalls
		if len(reasoningDetails) > 0 {
			converted["reasoning_details"] = reasoningDetails
		}
	}
	if compat.requiresReasoningContentOnAssistantMessages && model.Reasoning {
		if _, exists := converted["reasoning_content"]; !exists {
			converted["reasoning_content"] = ""
		}
	}
	hasContent := false
	switch content := converted["content"].(type) {
	case string:
		hasContent = content != ""
	case []any:
		hasContent = len(content) > 0
	}
	_, hasToolCalls := converted["tool_calls"]
	return converted, hasContent || hasToolCalls, nil
}

func convertOpenAICompletionsToolResult(
	model *ai.Model,
	message *ai.ToolResultMessage,
	compat resolvedOpenAICompletionsCompat,
) (map[string]any, []any) {
	texts := make([]string, 0)
	hasImages := false
	images := make([]any, 0)
	for _, rawBlock := range message.Content {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			texts = append(texts, block.Text)
		case *ai.ImageContent:
			hasImages = true
			if modelSupportsImage(model) {
				images = append(images, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:" + block.MimeType + ";base64," + block.Data},
				})
			}
		}
	}
	text := strings.Join(texts, "\n")
	if text == "" {
		if hasImages {
			text = "(see attached image)"
		} else {
			text = "(no tool output)"
		}
	}
	converted := map[string]any{"role": "tool", "content": sanitizeText(text), "tool_call_id": message.ToolCallID}
	if compat.requiresToolResultName && message.ToolName != "" {
		converted["name"] = message.ToolName
	}
	return converted, images
}

func jsTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed != ""
	default:
		return true
	}
}

func deferredOpenAICompletionsToolNames(messages ai.MessageList, compat resolvedOpenAICompletionsCompat) map[string]bool {
	result := map[string]bool{}
	if compat.deferredToolsMode == nil || *compat.deferredToolsMode != ai.DeferredToolsKimi {
		return result
	}
	for _, message := range messages {
		toolResult, ok := message.(*ai.ToolResultMessage)
		if !ok || toolResult.AddedToolNames == nil {
			continue
		}
		for _, name := range *toolResult.AddedToolNames {
			result[name] = true
		}
	}
	return result
}

func activeOpenAICompletionsTools(tools *[]ai.Tool, deferred map[string]bool) []ai.Tool {
	if tools == nil {
		return nil
	}
	result := make([]ai.Tool, 0, len(*tools))
	for _, tool := range *tools {
		if !deferred[tool.Name] {
			result = append(result, tool)
		}
	}
	return result
}

func toolsByName(tools *[]ai.Tool, names []string) []ai.Tool {
	if tools == nil {
		return nil
	}
	byName := make(map[string]ai.Tool, len(*tools))
	for _, tool := range *tools {
		byName[tool.Name] = tool
	}
	result := make([]ai.Tool, 0, len(names))
	for _, name := range names {
		if tool, ok := byName[name]; ok {
			result = append(result, tool)
		}
	}
	return result
}

func convertOpenAICompletionsTools(tools []ai.Tool, compat resolvedOpenAICompletionsCompat) []any {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		function := map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		}
		if compat.supportsStrictMode {
			function["strict"] = false
		}
		result = append(result, map[string]any{"type": "function", "function": function})
	}
	return result
}

func hasOpenAICompletionsToolHistory(messages ai.MessageList) bool {
	for _, message := range messages {
		switch typed := message.(type) {
		case *ai.ToolResultMessage:
			return true
		case *ai.AssistantMessage:
			for _, block := range typed.Content {
				if _, ok := block.(*ai.ToolCall); ok {
					return true
				}
			}
		}
	}
	return false
}

func openAICompletionsCacheControl(
	compat resolvedOpenAICompletionsCompat,
	retention ai.CacheRetention,
) map[string]any {
	if compat.cacheControlFormat == nil || *compat.cacheControlFormat != ai.CacheControlAnthropic || retention == ai.CacheRetentionNone {
		return nil
	}
	result := map[string]any{"type": "ephemeral"}
	if retention == ai.CacheRetentionLong && compat.supportsLongCacheRetention {
		result["ttl"] = "1h"
	}
	return result
}

func applyOpenAICompletionsCacheControl(messages []any, tools []any, cacheControl map[string]any) {
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok || (message["role"] != "system" && message["role"] != "developer") {
			continue
		}
		if addOpenAICompletionsCacheControlToText(message, cacheControl) {
			break
		}
	}
	if len(tools) > 0 {
		if tool, ok := tools[len(tools)-1].(map[string]any); ok {
			tool["cache_control"] = cacheControl
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message, ok := messages[index].(map[string]any)
		if !ok || (message["role"] != "user" && message["role"] != "assistant") {
			continue
		}
		if addOpenAICompletionsCacheControlToText(message, cacheControl) {
			break
		}
	}
}

func addOpenAICompletionsCacheControlToText(message map[string]any, cacheControl map[string]any) bool {
	switch content := message["content"].(type) {
	case string:
		if content == "" {
			return false
		}
		message["content"] = []any{map[string]any{"type": "text", "text": content, "cache_control": cacheControl}}
		return true
	case []any:
		for index := len(content) - 1; index >= 0; index-- {
			part, ok := content[index].(map[string]any)
			if ok && part["type"] == "text" {
				part["cache_control"] = cacheControl
				return true
			}
		}
	}
	return false
}

func applyOpenAICompletionsThinking(
	payload map[string]any,
	model *ai.Model,
	options *OpenAICompletionsOptions,
	compat resolvedOpenAICompletionsCompat,
) {
	if !model.Reasoning {
		return
	}
	effort := ""
	if options.ReasoningEffort != nil {
		effort = string(*options.ReasoningEffort)
	}
	switch compat.thinkingFormat {
	case ai.ThinkingFormatZAI:
		if effort != "" {
			payload["thinking"] = map[string]any{"type": "enabled", "clear_thinking": false}
			if compat.supportsReasoningEffort {
				if value, ok := mappedThinkingString(model, ai.ModelThinkingLevel(effort)); ok {
					payload["reasoning_effort"] = value
				}
			}
		} else {
			payload["thinking"] = map[string]any{"type": "disabled"}
		}
	case ai.ThinkingFormatQwen:
		payload["enable_thinking"] = effort != ""
	case ai.ThinkingFormatQwenChatTemplate:
		payload["chat_template_kwargs"] = map[string]any{"enable_thinking": effort != "", "preserve_thinking": true}
	case ai.ThinkingFormatChatTemplate:
		if kwargs := buildOpenAICompletionsChatTemplateKwargs(model, effort, compat.chatTemplateKwargs); len(kwargs) > 0 {
			payload["chat_template_kwargs"] = kwargs
		}
	case ai.ThinkingFormatDeepSeek:
		if effort != "" {
			payload["thinking"] = map[string]any{"type": "enabled"}
		} else if !thinkingLevelIsExplicitNull(model, ai.ModelThinkingOff) {
			payload["thinking"] = map[string]any{"type": "disabled"}
		}
		if effort != "" && compat.supportsReasoningEffort {
			payload["reasoning_effort"] = mappedThinkingOr(model, ai.ModelThinkingLevel(effort), effort)
		}
	case ai.ThinkingFormatOpenRouter:
		if effort != "" {
			payload["reasoning"] = map[string]any{"effort": mappedThinkingOr(model, ai.ModelThinkingLevel(effort), effort)}
		} else if !thinkingLevelIsExplicitNull(model, ai.ModelThinkingOff) {
			payload["reasoning"] = map[string]any{"effort": mappedThinkingOr(model, ai.ModelThinkingOff, "none")}
		}
	case ai.ThinkingFormatAntLing:
		if effort != "" {
			if value, ok := explicitThinkingString(model, ai.ModelThinkingLevel(effort)); ok {
				payload["reasoning"] = map[string]any{"effort": value}
			}
		}
	case ai.ThinkingFormatTogether:
		payload["reasoning"] = map[string]any{"enabled": effort != ""}
		if effort != "" && compat.supportsReasoningEffort {
			payload["reasoning_effort"] = mappedThinkingOr(model, ai.ModelThinkingLevel(effort), effort)
		}
	case ai.ThinkingFormatString:
		if effort != "" {
			payload["thinking"] = mappedThinkingOr(model, ai.ModelThinkingLevel(effort), effort)
		} else if !thinkingLevelIsExplicitNull(model, ai.ModelThinkingOff) {
			payload["thinking"] = mappedThinkingOr(model, ai.ModelThinkingOff, "none")
		}
	default:
		if effort != "" && compat.supportsReasoningEffort {
			payload["reasoning_effort"] = mappedThinkingOr(model, ai.ModelThinkingLevel(effort), effort)
		} else if effort == "" && compat.supportsReasoningEffort {
			if value, ok := explicitThinkingString(model, ai.ModelThinkingOff); ok {
				payload["reasoning_effort"] = value
			}
		}
	}
}

func buildOpenAICompletionsChatTemplateKwargs(model *ai.Model, effort string, values map[string]any) map[string]any {
	result := map[string]any{}
	for name, value := range values {
		object, isObject := value.(map[string]any)
		if !isObject {
			result[name] = value
			continue
		}
		if effort == "" {
			if omit, _ := object["omitWhenOff"].(bool); omit {
				continue
			}
		}
		if variable, _ := object["$var"].(string); variable == "thinking.enabled" {
			result[name] = effort != ""
			continue
		}
		level := ai.ModelThinkingOff
		if effort != "" {
			level = ai.ModelThinkingLevel(effort)
		}
		if value, exists, nonNull := thinkingMapValue(model, level); exists {
			if nonNull {
				result[name] = value
			}
		} else if effort != "" {
			result[name] = effort
		}
	}
	return result
}

func thinkingMapValue(model *ai.Model, level ai.ModelThinkingLevel) (string, bool, bool) {
	if model.ThinkingLevelMap == nil {
		return "", false, false
	}
	value, exists := (*model.ThinkingLevelMap)[level]
	if !exists {
		return "", false, false
	}
	if value == nil {
		return "", true, false
	}
	return *value, true, true
}

func explicitThinkingString(model *ai.Model, level ai.ModelThinkingLevel) (string, bool) {
	value, exists, nonNull := thinkingMapValue(model, level)
	return value, exists && nonNull
}

func mappedThinkingString(model *ai.Model, level ai.ModelThinkingLevel) (string, bool) {
	if value, exists, nonNull := thinkingMapValue(model, level); exists {
		return value, nonNull
	}
	return string(level), true
}

func mappedThinkingOr(model *ai.Model, level ai.ModelThinkingLevel, fallback string) string {
	if value, _, nonNull := thinkingMapValue(model, level); nonNull {
		return value
	}
	return fallback
}

func thinkingLevelIsExplicitNull(model *ai.Model, level ai.ModelThinkingLevel) bool {
	_, exists, nonNull := thinkingMapValue(model, level)
	return exists && !nonNull
}

func newCompletionsStreamState(output *ai.AssistantMessage) *completionsStreamState {
	return &completionsStreamState{
		output:                  output,
		textIndex:               -1,
		thinkingIndex:           -1,
		toolsByIndex:            map[int]*completionsToolState{},
		toolsByID:               map[string]*completionsToolState{},
		toolStates:              map[*ai.ToolCall]*completionsToolState{},
		pendingReasoningDetails: map[string]string{},
	}
}

func (state *completionsStreamState) consumeChunk(
	model *ai.Model,
	raw json.RawMessage,
	emit func(ai.AssistantMessageEvent) error,
) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || trimmed[0] != '{' {
		return nil
	}
	var chunk map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &chunk); err != nil {
		return err
	}
	if state.output.ResponseID == nil {
		if id, ok := rawJSONString(chunk["id"]); ok && id != "" {
			state.output.ResponseID = &id
		}
	}
	if state.output.ResponseModel == nil {
		if responseModel, ok := rawJSONString(chunk["model"]); ok && responseModel != "" && responseModel != model.ID {
			state.output.ResponseModel = &responseModel
		}
	}
	if rawJSTruthy(chunk["usage"]) {
		state.output.Usage = parseOpenAICompletionsUsage(chunk["usage"], model)
	}
	choices := rawJSONArray(chunk["choices"])
	if len(choices) == 0 {
		return nil
	}
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(choices[0], &choice); err != nil {
		return nil
	}
	if !rawJSTruthy(chunk["usage"]) && rawJSTruthy(choice["usage"]) {
		state.output.Usage = parseOpenAICompletionsUsage(choice["usage"], model)
	}
	if reason, ok := rawJSONString(choice["finish_reason"]); ok && reason != "" {
		stopReason, errorMessage := mapOpenAICompletionsStopReason(reason)
		state.output.StopReason = stopReason
		if errorMessage != nil {
			state.output.ErrorMessage = errorMessage
		}
		state.hasFinishReason = true
	}
	queued := make([]ai.AssistantMessageEvent, 0, 4)
	if err := state.consumeDelta(model, choice["delta"], func(event ai.AssistantMessageEvent) error {
		queued = append(queued, event)
		return nil
	}); err != nil {
		return err
	}
	// Upstream queues every event produced by a chunk before the async consumer
	// resumes, so all partials from that chunk observe its completed mutations.
	for _, event := range queued {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

func (state *completionsStreamState) consumeDelta(
	model *ai.Model,
	raw json.RawMessage,
	emit func(ai.AssistantMessageEvent) error,
) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var delta map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &delta); err != nil {
		return err
	}
	if content, ok := rawJSONString(delta["content"]); ok && content != "" {
		if state.text == nil {
			state.text = &ai.TextContent{}
			state.output.Content = append(state.output.Content, state.text)
			state.textIndex = len(state.output.Content) - 1
			if err := emit(ai.TextStartEvent{ContentIndex: state.textIndex, Partial: state.output}); err != nil {
				return err
			}
		}
		state.text.Text += content
		if err := emit(ai.TextDeltaEvent{ContentIndex: state.textIndex, Delta: content, Partial: state.output}); err != nil {
			return err
		}
	}
	reasoningField, reasoning := firstOpenAICompletionsReasoning(delta)
	if reasoning != "" {
		if state.thinking == nil {
			signature := reasoningField
			if model.Provider == "opencode-go" && reasoningField == "reasoning" {
				signature = "reasoning_content"
			}
			state.thinking = &ai.ThinkingContent{ThinkingSignature: &signature}
			state.output.Content = append(state.output.Content, state.thinking)
			state.thinkingIndex = len(state.output.Content) - 1
			if err := emit(ai.ThinkingStartEvent{ContentIndex: state.thinkingIndex, Partial: state.output}); err != nil {
				return err
			}
		}
		state.thinking.Thinking += reasoning
		if err := emit(ai.ThinkingDeltaEvent{ContentIndex: state.thinkingIndex, Delta: reasoning, Partial: state.output}); err != nil {
			return err
		}
	}
	for _, rawCall := range rawJSONArray(delta["tool_calls"]) {
		if err := state.consumeToolCall(rawCall, emit); err != nil {
			return err
		}
	}
	for _, rawDetail := range rawJSONArray(delta["reasoning_details"]) {
		state.consumeReasoningDetail(rawDetail)
	}
	return nil
}

func firstOpenAICompletionsReasoning(delta map[string]json.RawMessage) (string, string) {
	for _, name := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
		if value, ok := rawJSONString(delta[name]); ok && value != "" {
			return name, value
		}
	}
	return "", ""
}

func (state *completionsStreamState) consumeToolCall(raw json.RawMessage, emit func(ai.AssistantMessageEvent) error) error {
	var delta map[string]json.RawMessage
	if json.Unmarshal(raw, &delta) != nil {
		return nil
	}
	streamIndex, hasIndex := rawJSONInt(delta["index"])
	id, _ := rawJSONString(delta["id"])
	var function map[string]json.RawMessage
	_ = json.Unmarshal(delta["function"], &function)
	name, _ := rawJSONString(function["name"])
	arguments, _ := rawJSONString(function["arguments"])

	var stateForCall *completionsToolState
	if hasIndex {
		stateForCall = state.toolsByIndex[streamIndex]
	}
	if stateForCall == nil && id != "" {
		stateForCall = state.toolsByID[id]
	}
	if stateForCall == nil {
		partialArgs := ""
		call := &ai.ToolCall{ID: id, Name: name, Arguments: map[string]any{}, PartialArgs: &partialArgs}
		stateForCall = &completionsToolState{block: call, contentIndex: len(state.output.Content)}
		if hasIndex {
			value := streamIndex
			stateForCall.streamIndex = &value
			stateForCall.block.StreamIndex = &value
			state.toolsByIndex[streamIndex] = stateForCall
		}
		if id != "" {
			state.toolsByID[id] = stateForCall
		}
		state.output.Content = append(state.output.Content, call)
		state.toolStates[call] = stateForCall
		if err := emit(ai.ToolCallStartEvent{ContentIndex: stateForCall.contentIndex, Partial: state.output}); err != nil {
			return err
		}
	}
	if hasIndex && stateForCall.streamIndex == nil {
		value := streamIndex
		stateForCall.streamIndex = &value
		stateForCall.block.StreamIndex = &value
		state.toolsByIndex[streamIndex] = stateForCall
	}
	if id != "" {
		state.toolsByID[id] = stateForCall
	}
	state.applyPendingReasoningDetail(stateForCall)
	if stateForCall.block.ID == "" && id != "" {
		stateForCall.block.ID = id
		state.toolsByID[id] = stateForCall
	}
	if stateForCall.block.Name == "" && name != "" {
		stateForCall.block.Name = name
	}
	if arguments != "" {
		stateForCall.partialArgs += arguments
		partialArgs := stateForCall.partialArgs
		stateForCall.block.PartialArgs = &partialArgs
		stateForCall.block.Arguments = parseOpenAICompletionsToolArguments(stateForCall.partialArgs)
	}
	return emit(ai.ToolCallDeltaEvent{ContentIndex: stateForCall.contentIndex, Delta: arguments, Partial: state.output})
}

func (state *completionsStreamState) consumeReasoningDetail(raw json.RawMessage) {
	var detail struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Data string `json:"data"`
	}
	if json.Unmarshal(raw, &detail) != nil || detail.Type != "reasoning.encrypted" || detail.ID == "" || detail.Data == "" {
		return
	}
	normalized, err := ai.NormalizeJSONStringifyJSON(raw)
	if err != nil {
		return
	}
	serialized := string(normalized)
	if tool := state.toolsByID[detail.ID]; tool != nil {
		tool.block.ThoughtSignature = &serialized
	} else {
		state.pendingReasoningDetails[detail.ID] = serialized
	}
}

func (state *completionsStreamState) applyPendingReasoningDetail(tool *completionsToolState) {
	if tool.block.ID == "" {
		return
	}
	if detail, ok := state.pendingReasoningDetails[tool.block.ID]; ok {
		tool.block.ThoughtSignature = &detail
		delete(state.pendingReasoningDetails, tool.block.ID)
	}
}

func (state *completionsStreamState) finishBlocks(emit func(ai.AssistantMessageEvent) error) error {
	// Upstream queues the complete end-event batch before its async consumer
	// resumes, so even earlier end events observe finalized tool-call blocks.
	for _, rawBlock := range state.output.Content {
		block, ok := rawBlock.(*ai.ToolCall)
		if !ok {
			continue
		}
		if tool := state.toolStates[block]; tool != nil {
			if err := ai.SetToolCallArgumentsJSON(block, []byte(tool.partialArgs)); err != nil {
				block.Arguments = parseOpenAICompletionsToolArguments(tool.partialArgs)
			}
		}
		block.PartialArgs = nil
		block.StreamIndex = nil
	}
	for index, rawBlock := range state.output.Content {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			if err := emit(ai.TextEndEvent{ContentIndex: index, Content: block.Text, Partial: state.output}); err != nil {
				return err
			}
		case *ai.ThinkingContent:
			if err := emit(ai.ThinkingEndEvent{ContentIndex: index, Content: block.Thinking, Partial: state.output}); err != nil {
				return err
			}
		case *ai.ToolCall:
			if err := emit(ai.ToolCallEndEvent{ContentIndex: index, ToolCall: block, Partial: state.output}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (state *completionsStreamState) clearScratch() {
	for block := range state.toolStates {
		block.PartialArgs = nil
		block.StreamIndex = nil
	}
}

func parseOpenAICompletionsToolArguments(value string) map[string]any {
	parsed := partialjson.ParseStreamingJSON(value)
	if object, ok := parsed.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

func parseOpenAICompletionsUsage(raw json.RawMessage, model *ai.Model) ai.Usage {
	var usage map[string]json.RawMessage
	_ = json.Unmarshal(raw, &usage)
	promptTokens, _ := rawJSONInt64(usage["prompt_tokens"])
	completionTokens, _ := rawJSONInt64(usage["completion_tokens"])
	promptCacheHit, _ := rawJSONInt64(usage["prompt_cache_hit_tokens"])
	var promptDetails map[string]json.RawMessage
	_ = json.Unmarshal(usage["prompt_tokens_details"], &promptDetails)
	cacheRead := promptCacheHit
	if value, ok := rawJSONInt64(promptDetails["cached_tokens"]); ok {
		cacheRead = value
	}
	cacheWrite, _ := rawJSONInt64(promptDetails["cache_write_tokens"])
	var completionDetails map[string]json.RawMessage
	_ = json.Unmarshal(usage["completion_tokens_details"], &completionDetails)
	reasoning, _ := rawJSONInt64(completionDetails["reasoning_tokens"])
	input := promptTokens - cacheRead - cacheWrite
	if input < 0 {
		input = 0
	}
	result := ai.Usage{
		Input:       input,
		Output:      completionTokens,
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		Reasoning:   &reasoning,
		TotalTokens: input + completionTokens + cacheRead + cacheWrite,
		Cost:        ai.Cost{},
	}
	calculateCost(model, &result)
	return result
}

func mapOpenAICompletionsStopReason(reason string) (ai.StopReason, *string) {
	switch reason {
	case "stop", "end":
		return ai.StopReasonStop, nil
	case "length":
		return ai.StopReasonLength, nil
	case "function_call", "tool_calls":
		return ai.StopReasonToolUse, nil
	default:
		message := "Provider finish_reason: " + reason
		return ai.StopReasonError, &message
	}
}

func rawJSONArray(raw json.RawMessage) []json.RawMessage {
	var values []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil {
		return nil
	}
	return values
}

func rawJSONString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func rawJSONInt(raw json.RawMessage) (int, bool) {
	value, ok := rawJSONInt64(raw)
	return int(value), ok
}

func rawJSONInt64(raw json.RawMessage) (int64, bool) {
	var value int64
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func rawJSTruthy(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("false")) ||
		bytes.Equal(trimmed, []byte("0")) || bytes.Equal(trimmed, []byte(`""`)) {
		return false
	}
	return true
}
