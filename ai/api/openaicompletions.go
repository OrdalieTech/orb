package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonschema"
	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/internal/partialjson"
)

// OpenAICompletionsOptions contains the Chat Completions-only request options.
type OpenAICompletionsOptions struct {
	ai.StreamOptions
	ToolChoice      any
	ReasoningEffort *ai.ThinkingLevel
	ThinkingBudgets *ai.ThinkingBudgets
}

type resolvedOpenAICompletionsCompat struct {
	thinkingTokenBudgetField                    string
	vllmPriority                                *float64
	supportsStore                               bool
	supportsDeveloperRole                       bool
	supportsReasoningEffort                     bool
	supportsUsageInStreaming                    bool
	supportsFinishReason                        bool
	maxTokensField                              ai.MaxTokensField
	requiresToolResultName                      bool
	requiresAssistantAfterToolResult            bool
	requiresThinkingAsText                      bool
	requiresReasoningContentOnAssistantMessages bool
	thinkingFormat                              ai.ThinkingFormat
	chatTemplateKwargs                          map[string]any
	chatTemplateKwargOrder                      []string
	chatTemplateArgs                            map[string]any
	chatTemplateArgOrder                        []string
	openRouterRouting                           *ai.OpenRouterRouting
	vercelGatewayRouting                        *ai.VercelGatewayRouting
	zaiToolStream                               bool
	supportsOpenAIGrammarTools                  bool
	supportsMidConvoSystemMessages              bool
	supportsMidConvoToolAdditions               bool
	supportsStrictMode                          bool
	cacheControlFormat                          *ai.CacheControlFormat
	sendSessionAffinityHeaders                  bool
	sessionAffinityFormat                       ai.SessionAffinityFormat
	supportsLongCacheRetention                  bool
}

type completionsToolState struct {
	block        *ai.ToolCall
	contentIndex int
	partialArgs  string
	customInput  *responsesCustomInput
	argsBuffer   streamBuffer
	streamIndex  *int
}

type completionsStreamState struct {
	output                     *ai.AssistantMessage
	text                       *ai.TextContent
	textIndex                  int
	thinking                   *ai.ThinkingContent
	thinkingIndex              int
	textBuffer                 streamBuffer
	thinkingBuffer             streamBuffer
	toolsByIndex               map[int]*completionsToolState
	toolsByID                  map[string]*completionsToolState
	toolStates                 map[*ai.ToolCall]*completionsToolState
	reasoningDetails           []json.RawMessage
	grammarToolInputProperties map[string]string
	hasFinishReason            bool
}

// StreamSimpleOpenAICompletions applies the provider-neutral context and
// options before entering the specialized Chat Completions path.
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
	var budgets *ai.ThinkingBudgets
	if options != nil {
		requestedReasoning = options.Reasoning
		budgets = options.ThinkingBudgets
	}
	return StreamOpenAICompletionsWithOptions(ctx, model, requestContext, &OpenAICompletionsOptions{
		StreamOptions:   buildBaseStreamOptions(model, requestContext, options),
		ToolChoice:      simpleToolChoiceAny(options, "required"),
		ReasoningEffort: clampSimpleReasoning(model, requestedReasoning),
		ThinkingBudgets: budgets,
	})
}

// completionsStreamFailure augments the shared failure formatting with
// OpenRouter's error.metadata.raw, which upstream appends to the completions
// error message when it is not already present (OA-m1).
func completionsStreamFailure(ctx context.Context, output *ai.AssistantMessage, err error) ai.ErrorEvent {
	event := streamFailure(ctx, output, err, "")
	if raw := openRouterErrorMetadataRaw(err); raw != "" && output.ErrorMessage != nil && !strings.Contains(*output.ErrorMessage, raw) {
		message := *output.ErrorMessage + "\n" + raw
		output.ErrorMessage = &message
	}
	return event
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
			yield(completionsStreamFailure(ctx, output, err), nil)
			return
		}
		compat, err := resolveOpenAICompletionsCompat(model)
		if err != nil {
			yield(completionsStreamFailure(ctx, output, err), nil)
			return
		}
		grammarToolInputProperties, err := createGrammarToolInputProperties(
			requestContext.Tools, compat.supportsOpenAIGrammarTools,
		)
		if err != nil {
			yield(completionsStreamFailure(ctx, output, err), nil)
			return
		}
		retention := resolveCacheRetention(&options.StreamOptions)
		payload, err := buildOpenAICompletionsPayload(model, requestContext, options, compat, retention)
		if err != nil {
			yield(completionsStreamFailure(ctx, output, err), nil)
			return
		}
		payloadValue, err := applyPayloadHook(ctx, model, &options.StreamOptions, payload)
		if err != nil {
			yield(completionsStreamFailure(ctx, output, err), nil)
			return
		}
		payloadValue = openAICompletionsWireValue(payloadValue, compat)

		headers := buildOpenAICompletionsHeaders(model, requestContext, &options.StreamOptions, compat, retention)
		headers, err = applyHeadersHook(ctx, model, &options.StreamOptions, headers)
		if err != nil {
			yield(completionsStreamFailure(ctx, output, err), nil)
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
			yield(completionsStreamFailure(ctx, output, err), nil)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if !yield(ai.StartEvent{Partial: output}, nil) {
			return
		}

		state := newCompletionsStreamState(output)
		state.grammarToolInputProperties = grammarToolInputProperties
		err = readSSE(response.Body, withStreamEvents(ctx, &options.StreamOptions, model, func(raw json.RawMessage) error {
			return state.consumeChunk(model, raw, func(event ai.AssistantMessageEvent) error {
				if !yield(event, nil) {
					return errStopSSE
				}
				return nil
			})
		}))
		if errors.Is(err, errStopSSE) {
			return
		}
		if err != nil {
			state.clearScratch()
			yield(completionsStreamFailure(ctx, output, err), nil)
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
			yield(completionsStreamFailure(ctx, output, errors.New("Request was aborted")), nil) //nolint:staticcheck // Exact upstream error text is observable.
			return
		}
		if !state.hasFinishReason && !compat.supportsFinishReason {
			output.StopReason = ai.StopReasonStop
			for _, block := range output.Content {
				if _, ok := block.(*ai.ToolCall); ok {
					output.StopReason = ai.StopReasonToolUse
					break
				}
			}
		}
		if output.StopReason == ai.StopReasonError {
			message := "Provider returned an error stop reason"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			yield(completionsStreamFailure(ctx, output, errors.New(message)), nil)
			return
		}
		if (compat.supportsFinishReason && !state.hasFinishReason) || output.StopReason == ai.StopReasonPending {
			yield(completionsStreamFailure(ctx, output, errors.New("Stream ended without finish_reason")), nil) //nolint:staticcheck // Exact upstream error text is observable.
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

func openAICompletionsWireValue(value any, compat resolvedOpenAICompletionsCompat) any {
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}
	for _, field := range []struct {
		key   string
		order []string
	}{
		{"chat_template_kwargs", compat.chatTemplateKwargOrder},
		{"chat_template_args", compat.chatTemplateArgOrder},
	} {
		values, ok := object[field.key].(map[string]any)
		if !ok || len(field.order) == 0 {
			continue
		}
		wrapped := make(map[string]any, len(object))
		for key, item := range object {
			wrapped[key] = item
		}
		wrapped[field.key] = openAICompletionsWireObject{
			value:     values,
			preferred: field.order,
		}
		object = wrapped
	}
	return openAICompletionsWirePayload{value: object}
}

func (payload openAICompletionsWirePayload) MarshalJSON() ([]byte, error) {
	// Sized for the encoded messages and tools, so the buffer is not regrown
	// and copied as the conversation lengthens.
	tools, _ := payload.value["tools"].(completionsWireJSON)
	size := 1024 + len(tools)
	messages, _ := payload.value["messages"].([]any)
	for _, message := range messages {
		switch message := message.(type) {
		case completionsWireJSON:
			size += len(message) + 1
		case map[string]any:
			content, _ := message["content"].(string)
			size += len(content) + 64
		}
	}
	return appendOpenAICompletionsObject(make([]byte, 0, size), payload.value, openAICompletionsObjectKeys(payload.value, true))
}

func (object openAICompletionsWireObject) MarshalJSON() ([]byte, error) {
	return appendOpenAICompletionsObject(nil, object.value, orderedOpenAICompletionsKeys(object.value, object.preferred))
}

func marshalOpenAICompletionsObjectWithKeys(object map[string]any, keys []string) ([]byte, error) {
	return appendOpenAICompletionsObject(nil, object, keys)
}

// appendOpenAICompletionsValue encodes into one buffer: per-value encoding
// allocated for every string and re-validated every nested object.
func appendOpenAICompletionsValue(dst []byte, value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return append(dst, "null"...), nil
	case string:
		return jsonwire.AppendString(dst, typed), nil
	case completionsWireJSON:
		return append(dst, typed...), nil
	case map[string]any:
		return appendOpenAICompletionsObject(dst, typed, openAICompletionsObjectKeys(typed, false))
	case openAICompletionsWireObject:
		return appendOpenAICompletionsObject(dst, typed.value, orderedOpenAICompletionsKeys(typed.value, typed.preferred))
	case []any:
		dst = append(dst, '[')
		for index, item := range typed {
			if index > 0 {
				dst = append(dst, ',')
			}
			var err error
			if dst, err = appendOpenAICompletionsValue(dst, item); err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
	case bool:
		return strconv.AppendBool(dst, typed), nil
	case jsonschema.Schema:
		// A schema encodes as its compacted bytes, {} when empty; ai.Marshal
		// reports an invalid one.
		if len(typed) == 0 {
			typed = jsonschema.Schema("{}")
		}
		if encoded, err := jsonwire.AppendCompact(dst, typed); err == nil {
			return encoded, nil
		}
		encoded, err := ai.Marshal(value)
		if err != nil {
			return nil, err
		}
		return append(dst, encoded...), nil
	default:
		encoded, err := ai.Marshal(value)
		if err != nil {
			return nil, err
		}
		return append(dst, encoded...), nil
	}
}

func appendOpenAICompletionsObject(dst []byte, object map[string]any, keys []string) ([]byte, error) {
	dst = append(dst, '{')
	for index, key := range keys {
		if index > 0 {
			dst = append(dst, ',')
		}
		dst = append(jsonwire.AppendString(dst, key), ':')
		var err error
		if dst, err = appendOpenAICompletionsValue(dst, object[key]); err != nil {
			return nil, fmt.Errorf("encode OpenAI completions field %q: %w", key, err)
		}
	}
	return append(dst, '}'), nil
}

var openAICompletionsRootKeys = []string{
	"model", "messages", "stream", "prompt_cache_key", "prompt_cache_retention",
	"stream_options", "store", "max_tokens", "max_completion_tokens", "temperature",
	"tools", "tool_stream", "tool_choice", "priority", "thinking", "enable_thinking",
	"chat_template_kwargs", "chat_template_args", "reasoning", "reasoning_effort", "thinking_token_budget", "thinking_budget", "thinking_budget_tokens", "provider", "providerOptions",
}

func openAICompletionsObjectKeys(object map[string]any, root bool) []string {
	if root {
		return orderedOpenAICompletionsKeys(object, openAICompletionsRootKeys)
	}
	if role, ok := object["role"].(string); ok {
		switch role {
		case "assistant":
			preferred := []string{"role", "content"}
			if reasoning, ok := object["reasoning_content"].(string); ok && reasoning != "" {
				preferred = append(preferred, "reasoning_content")
			}
			dynamic := make([]string, 0)
			for key := range object {
				switch key {
				case "role", "content", "tool_calls", "reasoning_details", "reasoning_content":
				default:
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
		if _, hasCustom := object["custom"]; hasCustom {
			return orderedOpenAICompletionsKeys(object, []string{"id", "type", "custom"})
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
		case "custom":
			return orderedOpenAICompletionsKeys(object, []string{"type", "custom", "cache_control"})
		case "grammar":
			return orderedOpenAICompletionsKeys(object, []string{"type", "grammar"})
		case "ephemeral":
			return orderedOpenAICompletionsKeys(object, []string{"type", "ttl"})
		case "enabled", "disabled":
			return orderedOpenAICompletionsKeys(object, []string{"type", "clear_thinking"})
		case "reasoning.encrypted":
			return orderedOpenAICompletionsKeys(object, []string{"type", "id", "data"})
		}
	}
	if _, hasName := object["name"]; hasName {
		if _, hasInput := object["input"]; hasInput {
			return orderedOpenAICompletionsKeys(object, []string{"name", "input"})
		}
		if _, hasFormat := object["format"]; hasFormat {
			return orderedOpenAICompletionsKeys(object, []string{"name", "description", "format"})
		}
		if _, hasArguments := object["arguments"]; hasArguments {
			return orderedOpenAICompletionsKeys(object, []string{"name", "arguments"})
		}
		if _, hasParameters := object["parameters"]; hasParameters {
			return orderedOpenAICompletionsKeys(object, []string{"name", "description", "parameters", "strict"})
		}
	}
	if _, hasSyntax := object["syntax"]; hasSyntax {
		if _, hasDefinition := object["definition"]; hasDefinition {
			return orderedOpenAICompletionsKeys(object, []string{"syntax", "definition"})
		}
	}
	return orderedOpenAICompletionsKeys(object, nil)
}

// orderedOpenAICompletionsKeys lists the preferred keys present, then the
// rest sorted. Preferred lists are short, so membership is a linear scan.
func orderedOpenAICompletionsKeys(object map[string]any, preferred []string) []string {
	keys := make([]string, 0, len(object))
	for _, key := range preferred {
		if _, exists := object[key]; exists && !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	listed := len(keys)
	for key := range object {
		if !slices.Contains(keys[:listed], key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys[listed:])
	return keys
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
	if overrides.SupportsFinishReason != nil {
		resolved.supportsFinishReason = *overrides.SupportsFinishReason
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
	resolved.vllmPriority = overrides.VLLMPriority
	if overrides.ThinkingTokenBudgetField != nil {
		resolved.thinkingTokenBudgetField = string(*overrides.ThinkingTokenBudgetField)
	} else if overrides.SupportsThinkingTokenBudget != nil && *overrides.SupportsThinkingTokenBudget {
		resolved.thinkingTokenBudgetField = "thinking_token_budget"
	}
	if overrides.ChatTemplateKwargs != nil {
		resolved.chatTemplateKwargs = *overrides.ChatTemplateKwargs
		resolved.chatTemplateKwargOrder, err = openAICompletionsChatTemplateValueOrder(model.Compat, "chatTemplateKwargs")
		if err != nil {
			return resolved, fmt.Errorf("decode %s compat chatTemplateKwargs order: %w", model.Provider, err)
		}
	}
	if overrides.ChatTemplateArgs != nil {
		resolved.chatTemplateArgs = *overrides.ChatTemplateArgs
		resolved.chatTemplateArgOrder, err = openAICompletionsChatTemplateValueOrder(model.Compat, "chatTemplateArgs")
		if err != nil {
			return resolved, fmt.Errorf("decode %s compat chatTemplateArgs order: %w", model.Provider, err)
		}
	}
	resolved.openRouterRouting = overrides.OpenRouterRouting
	resolved.vercelGatewayRouting = overrides.VercelGatewayRouting
	if overrides.ZAIToolStream != nil {
		resolved.zaiToolStream = *overrides.ZAIToolStream
	}
	if overrides.SupportsOpenAIGrammarTools != nil {
		resolved.supportsOpenAIGrammarTools = *overrides.SupportsOpenAIGrammarTools
	}
	if overrides.SupportsMidConvoSystemMessages != nil {
		resolved.supportsMidConvoSystemMessages = *overrides.SupportsMidConvoSystemMessages
	}
	if overrides.SupportsMidConvoToolAdditions != nil {
		resolved.supportsMidConvoToolAdditions = *overrides.SupportsMidConvoToolAdditions
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
	if overrides.SessionAffinityFormat != nil {
		resolved.sessionAffinityFormat = *overrides.SessionAffinityFormat
	}
	if overrides.SupportsLongCacheRetention != nil {
		resolved.supportsLongCacheRetention = *overrides.SupportsLongCacheRetention
	}
	return resolved, nil
}

func openAICompletionsChatTemplateValueOrder(compat json.RawMessage, field string) ([]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(compat, &raw); err != nil {
		return nil, err
	}
	values := raw[field]
	if len(values) == 0 || string(values) == "null" {
		return nil, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(values))
	opening, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := opening.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New(field + " is not an object")
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
			return nil, errors.New(field + " key is not a string")
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
	isDeepSeek := provider == "deepseek" || strings.Contains(strings.ToLower(baseURL), "deepseek.com")
	isNonStandard := isNVIDIA || provider == "cerebras" || strings.Contains(baseURL, "cerebras.ai") ||
		provider == "xai" || strings.Contains(baseURL, "api.x.ai") || isTogether || strings.Contains(baseURL, "chutes.ai") ||
		isDeepSeek || isZAI || isMoonshot || provider == "opencode" ||
		strings.Contains(baseURL, "opencode.ai") || isCloudflareWorkers || isCloudflareGateway || isAntLing
	useLegacyMax := strings.Contains(baseURL, "chutes.ai") || isDeepSeek || isMoonshot || isCloudflareGateway ||
		isTogether || isNVIDIA || isAntLing || isZAI
	isGrok := provider == "xai" || strings.Contains(baseURL, "api.x.ai")
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
	if provider == "openrouter" && (strings.HasPrefix(model.ID, "anthropic/") || strings.HasPrefix(model.ID, "~anthropic/")) {
		value := ai.CacheControlAnthropic
		cacheControl = &value
	}
	return resolvedOpenAICompletionsCompat{
		supportsStore:              !isNonStandard,
		supportsDeveloperRole:      isOpenRouterDeveloperModel || (!isNonStandard && !isOpenRouter),
		supportsReasoningEffort:    !isGrok && !isZAI && !isMoonshot && !isTogether && !isCloudflareGateway && !isNVIDIA && !isAntLing,
		supportsUsageInStreaming:   true,
		supportsFinishReason:       true,
		maxTokensField:             maxTokensField,
		thinkingFormat:             thinkingFormat,
		chatTemplateKwargs:         map[string]any{},
		chatTemplateArgs:           map[string]any{},
		supportsOpenAIGrammarTools: false,
		// OpenAI compatibility alone does not imply strict JSON-schema tool
		// support; capable built-in models advertise it in their compat.
		supportsStrictMode:                          false,
		cacheControlFormat:                          cacheControl,
		sendSessionAffinityHeaders:                  isOpenRouter,
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
	if _, exists := headers["User-Agent"]; !exists {
		headers.Set("User-Agent", piUserAgent())
	}
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
	addOpenCodeSessionHeader(headers, model, options)
	return headers
}

// plainContext reports a context whose transcript holds no system message
// and whose tools have distinct names: the transcript projection, which
// merges system messages and deduplicates tools, changes nothing in it.
func plainContext(context ai.Context) bool {
	for _, message := range context.Messages {
		if _, system := message.(*ai.SystemMessage); system {
			return false
		}
	}
	if context.Tools != nil {
		tools := *context.Tools
		for index := range tools {
			for _, other := range tools[:index] {
				if other.Name == tools[index].Name {
					return false
				}
			}
		}
	}
	return true
}

func buildOpenAICompletionsPayload(
	model *ai.Model,
	requestContext ai.Context,
	options *OpenAICompletionsOptions,
	compat resolvedOpenAICompletionsCompat,
	retention ai.CacheRetention,
) (map[string]any, error) {
	if plainContext(requestContext) {
		// Without system messages in the transcript, normalizing the context
		// and projecting it back yields its own prompt and tools.
		prompt := ""
		if requestContext.SystemPrompt != nil {
			prompt = *requestContext.SystemPrompt
		}
		var tools []ai.Tool
		if requestContext.Tools != nil {
			tools = slices.Clone(*requestContext.Tools)
		}
		requestContext.SystemPrompt, requestContext.Tools = nil, nil
		if prompt != "" || len(tools) > 0 {
			requestContext.SystemPrompt = &prompt
		}
		if len(tools) > 0 {
			requestContext.Tools = &tools
		}
		compat.supportsMidConvoToolAdditions = compat.supportsMidConvoSystemMessages && compat.supportsMidConvoToolAdditions
	} else {
		transcript := ai.NormalizeContext(requestContext)
		if !compat.supportsMidConvoSystemMessages {
			transcript = ai.CollapseSystemMessages(transcript)
		}
		requestTools, _, anchorsAdditions := transcriptToolPlacement(
			transcript.Messages, compat.supportsMidConvoSystemMessages && compat.supportsMidConvoToolAdditions,
		)
		requestContext = projectTranscriptContext(transcript, true)
		if len(requestTools) > 0 {
			requestContext.Tools = &requestTools
		} else {
			requestContext.Tools = nil
		}
		compat.supportsMidConvoToolAdditions = anchorsAdditions
	}
	grammarToolInputProperties, err := createGrammarToolInputProperties(
		requestContext.Tools, compat.supportsOpenAIGrammarTools,
	)
	if err != nil {
		return nil, err
	}
	cacheControl := openAICompletionsCacheControl(compat, retention)
	// Hooks and cache anchors edit message objects; otherwise each message
	// goes out as its remembered encoding.
	wire := options.OnPayload == nil && cacheControl == nil && len(grammarToolInputProperties) == 0
	var session string
	if options.SessionID != nil {
		session = *options.SessionID
	}
	messages, err := convertOpenAICompletionsMessages(model, requestContext, compat, grammarToolInputProperties, wire, session)
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

	activeTools := activeOpenAICompletionsTools(requestContext.Tools, nil)
	var tools []any
	if len(activeTools) > 0 {
		if wire {
			payload["tools"], err = wireOpenAICompletionsTools(activeTools, compat)
		} else {
			tools, err = convertOpenAICompletionsTools(activeTools, compat)
			payload["tools"] = tools
		}
		if err != nil {
			return nil, err
		}
		if compat.zaiToolStream {
			payload["tool_stream"] = true
		}
	} else if hasOpenAICompletionsToolHistory(requestContext.Messages) {
		tools = []any{}
		payload["tools"] = tools
	}
	if cacheControl != nil {
		applyOpenAICompletionsCacheControl(messages, tools, cacheControl)
	}
	if options.ToolChoice != nil {
		payload["tool_choice"] = options.ToolChoice
	}
	if compat.vllmPriority != nil {
		payload["priority"] = *compat.vllmPriority
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
	maps.Copy(payload, mergedSamplingParams(model, &options.StreamOptions))
	return payload, nil
}

func normalizeOpenAICompletionsToolCallID(id string, model *ai.Model, _ *ai.AssistantMessage) string {
	if separator := strings.IndexByte(id, '|'); separator >= 0 {
		normalize := func(value string) string {
			units := utf16.Encode([]rune(value))
			var normalized strings.Builder
			for _, unit := range units {
				if (unit >= 'a' && unit <= 'z') || (unit >= 'A' && unit <= 'Z') ||
					(unit >= '0' && unit <= '9') || unit == '_' || unit == '-' {
					normalized.WriteByte(byte(unit))
				} else {
					normalized.WriteByte('_')
				}
			}
			return normalized.String()
		}
		callID, itemID := normalize(id[:separator]), normalize(id[separator+1:])
		combined := callID
		if itemID != "" {
			combined += "_" + itemID
		}
		if len(combined) <= 40 {
			return combined
		}
		hash := truncateASCII(shortHash(id), 8)
		return truncateASCII(callID, max(1, 40-len(hash)-1)) + "_" + hash
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
	grammarToolInputProperties map[string]string,
	wire bool,
	session string,
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
	settings := completionsMessageSettings{
		assistantAfterToolResult: compat.requiresAssistantAfterToolResult,
		thinkingAsText:           compat.requiresThinkingAsText,
		reasoningContent:         compat.requiresReasoningContentOnAssistantMessages && model.Reasoning,
		toolResultName:           compat.requiresToolResultName,
		openCodeGo:               model.Provider == "opencode-go",
	}
	// For the wire, a message the last request sent at the same place reuses
	// that request's encoding.
	var previous, current completionsWireMessages
	if wire {
		completionsWireLast.Lock()
		previous = completionsWireLast.sessions[session]
		completionsWireLast.Unlock()
		current = completionsWireMessages{settings, transformed, make([]any, len(transformed))}
	}
	encode := func(index int, convert func() (map[string]any, bool, error)) (any, bool, error) {
		if !wire {
			return convert()
		}
		if previous.settings == settings && index < len(previous.messages) && previous.messages[index] == transformed[index] {
			current.encoded[index] = previous.encoded[index]
		} else if converted, include, err := convert(); err != nil || !include {
			return nil, false, err
		} else {
			value, err := appendOpenAICompletionsValue(nil, converted)
			if err != nil {
				return nil, false, err
			}
			current.encoded[index] = completionsWireJSON(value)
		}
		return current.encoded[index], current.encoded[index] != nil, nil
	}

	lastRole := ""
	for index := 0; index < len(transformed); index++ {
		switch message := transformed[index].(type) {
		case *ai.SystemMessage:
			if compat.supportsMidConvoToolAdditions && len(message.ToolsAdded) > 0 {
				convertedTools, err := convertOpenAICompletionsTools(message.ToolsAdded, compat)
				if err != nil {
					return nil, err
				}
				messages = append(messages, map[string]any{"role": "system", "tools": convertedTools})
			}
			if text := ai.SystemMessageText(message); text != "" {
				role := "system"
				if model.Reasoning && compat.supportsDeveloperRole {
					role = "developer"
				}
				messages = append(messages, map[string]any{"role": role, "content": sanitizeText(text)})
				lastRole = "system"
			}
		case *ai.UserMessage:
			if compat.requiresAssistantAfterToolResult && lastRole == "toolResult" {
				messages = append(messages, map[string]any{"role": "assistant", "content": "I have processed the tool results."})
			}
			converted, include, err := encode(index, func() (map[string]any, bool, error) {
				converted, include := convertOpenAICompletionsUserMessage(message)
				return converted, include, nil
			})
			if err != nil {
				return nil, err
			}
			if !include {
				continue
			}
			messages = append(messages, converted)
			lastRole = "user"
		case *ai.AssistantMessage:
			converted, include, err := encode(index, func() (map[string]any, bool, error) {
				return convertOpenAICompletionsAssistantMessageWithGrammar(settings, message, grammarToolInputProperties)
			})
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
			for end < len(transformed) {
				toolResult, ok := transformed[end].(*ai.ToolResultMessage)
				if !ok {
					break
				}
				converted, _, err := encode(end, func() (map[string]any, bool, error) {
					return convertOpenAICompletionsToolResult(settings, toolResult), true, nil
				})
				if err != nil {
					return nil, err
				}
				messages = append(messages, converted)
				// transformMessages left images only for a model that sees them.
				for _, block := range toolResult.Content {
					if image, ok := block.(*ai.ImageContent); ok {
						imageParts = append(imageParts, openAICompletionsImagePart(image))
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
		}
	}
	if wire {
		completionsWireLast.Lock()
		if _, known := completionsWireLast.sessions[session]; !known && len(completionsWireLast.sessions) >= 64 {
			for evicted := range completionsWireLast.sessions {
				delete(completionsWireLast.sessions, evicted)
				break
			}
		}
		completionsWireLast.sessions[session] = current
		completionsWireLast.Unlock()
	}
	return messages, nil
}

// completionsMessageSettings is all that converting one message reads besides
// the message, so a message encodes the same way under equal settings.
type completionsMessageSettings struct {
	assistantAfterToolResult, thinkingAsText, reasoningContent, toolResultName, openCodeGo bool
}

// completionsWireJSON is a message's encoded wire object.
type completionsWireJSON []byte

// completionsWireMessages are a request's transformed messages and their
// encodings, nil for a message left out.
type completionsWireMessages struct {
	settings completionsMessageSettings
	messages ai.MessageList
	encoded  []any
}

// completionsWireLast is each session's last request. A conversation's next
// request repeats its messages in order and adds a few; messages are not
// changed once sent and transformMessages returns those it leaves alone, so a
// long conversation encodes only what is new. The Worker runs many objects'
// sessions in one runtime, with their requests interleaved.
var completionsWireLast = struct {
	sync.Mutex
	sessions map[string]completionsWireMessages
}{sessions: map[string]completionsWireMessages{}}

func openAICompletionsImagePart(image *ai.ImageContent) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + image.MimeType + ";base64," + image.Data}}
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
			// Some providers reject image-only messages that carry an empty text part.
			if block.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": sanitizeText(block.Text)})
			}
		case *ai.ImageContent:
			content = append(content, openAICompletionsImagePart(block))
		}
	}
	if len(content) == 0 {
		return nil, false
	}
	return map[string]any{"role": "user", "content": content}, true
}

func convertOpenAICompletionsAssistantMessageWithGrammar(
	settings completionsMessageSettings,
	message *ai.AssistantMessage,
	grammarToolInputProperties map[string]string,
) (map[string]any, bool, error) {
	contentValue := any(nil)
	if settings.assistantAfterToolResult {
		contentValue = ""
	}
	converted := map[string]any{"role": "assistant", "content": contentValue}
	textParts := make([]any, 0)
	textValues := make([]string, 0)
	thinkingBlocks := make([]*ai.ThinkingContent, 0)
	var signedDetails []json.RawMessage
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
			if signedDetails == nil && block.ThinkingSignature != nil {
				signedDetails = parseOpenAIReasoningDetails(*block.ThinkingSignature)
			}
			if strings.TrimSpace(block.Thinking) != "" {
				thinkingBlocks = append(thinkingBlocks, block)
			}
		case *ai.ToolCall:
			toolCalls = append(toolCalls, block)
		}
	}
	assistantText := strings.Join(textValues, "")
	if len(thinkingBlocks) > 0 {
		if settings.thinkingAsText {
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
			if signature != nil && settings.openCodeGo && *signature == "reasoning" {
				value := "reasoning_content"
				signature = &value
			}
			if signature != nil && signedDetails == nil && (*signature == "reasoning" || *signature == "reasoning_content" || *signature == "reasoning_text") {
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
			if inputProperty, custom := grammarToolInputProperties[call.Name]; custom {
				input, err := getGrammarToolInput(call.Name, call.Arguments, inputProperty)
				if err != nil {
					return nil, false, err
				}
				convertedCalls = append(convertedCalls, map[string]any{
					"id": call.ID, "type": "custom",
					"custom": map[string]any{"name": call.Name, "input": sanitizeText(input)},
				})
			} else {
				encoded, err := ai.MarshalToolCallArguments(call)
				if err != nil {
					return nil, false, fmt.Errorf("marshal tool call %s arguments: %w", call.ID, err)
				}
				convertedCalls = append(convertedCalls, map[string]any{
					"id":       call.ID,
					"type":     "function",
					"function": map[string]any{"name": call.Name, "arguments": string(encoded)},
				})
			}
			if call.ThoughtSignature != nil {
				raw := json.RawMessage(*call.ThoughtSignature)
				var detail map[string]json.RawMessage
				if validOpenAIReasoningDetail(raw) && json.Unmarshal(raw, &detail) == nil {
					kind, _ := rawJSONString(detail["type"])
					id, _ := rawJSONString(detail["id"])
					data, _ := rawJSONString(detail["data"])
					if kind == "reasoning.encrypted" && id != "" && data != "" {
						normalized, _ := ai.NormalizeJSONStringifyJSON(raw)
						reasoningDetails = append(reasoningDetails, json.RawMessage(normalized))
					}
				}
			}
		}
		converted["tool_calls"] = convertedCalls
		if len(reasoningDetails) > 0 {
			converted["reasoning_details"] = reasoningDetails
		}
	}
	if signedDetails != nil {
		converted["reasoning_details"] = signedDetails
	}
	if settings.reasoningContent {
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

func convertOpenAICompletionsToolResult(settings completionsMessageSettings, message *ai.ToolResultMessage) map[string]any {
	texts := make([]string, 0)
	hasImages := false
	for _, rawBlock := range message.Content {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			texts = append(texts, block.Text)
		case *ai.ImageContent:
			hasImages = true
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
	if settings.toolResultName && message.ToolName != "" {
		converted["name"] = message.ToolName
	}
	return converted
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

func convertOpenAICompletionsTools(tools []ai.Tool, compat resolvedOpenAICompletionsCompat) ([]any, error) {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		grammar, err := resolveGrammarConstrainedSampling(tool, compat.supportsOpenAIGrammarTools)
		if err != nil {
			return nil, err
		}
		if grammar != nil {
			result = append(result, map[string]any{
				"type": "custom",
				"custom": map[string]any{
					"name": tool.Name, "description": tool.Description,
					"format": map[string]any{
						"type":    "grammar",
						"grammar": map[string]any{"syntax": grammar.format, "definition": grammar.definition},
					},
				},
			})
			continue
		}
		strict, err := resolveJSONSchemaStrictSampling(tool, compat.supportsStrictMode)
		if err != nil {
			return nil, err
		}
		parameters, err := getJSONSchemaToolParameters(tool.Parameters, strict != nil && *strict)
		if err != nil {
			return nil, err
		}
		function := map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  parameters,
		}
		if compat.supportsStrictMode {
			if strict == nil {
				function["strict"] = false
			} else {
				function["strict"] = *strict
			}
		}
		result = append(result, map[string]any{"type": "function", "function": function})
	}
	return result, nil
}

// completionsWireTools is the last tools encoding. Requests repeat the same
// tools, whose shared fields compare equal without being read.
var completionsWireTools struct {
	sync.Mutex
	tools           []ai.Tool
	strict, grammar bool
	encoded         completionsWireJSON
}

func wireOpenAICompletionsTools(tools []ai.Tool, compat resolvedOpenAICompletionsCompat) (completionsWireJSON, error) {
	cache := &completionsWireTools
	cache.Lock()
	defer cache.Unlock()
	if cache.encoded != nil && cache.strict == compat.supportsStrictMode && cache.grammar == compat.supportsOpenAIGrammarTools &&
		slices.EqualFunc(cache.tools, tools, ai.ToolDeclarationsEqual) {
		return cache.encoded, nil
	}
	converted, err := convertOpenAICompletionsTools(tools, compat)
	if err != nil {
		return nil, err
	}
	encoded, err := appendOpenAICompletionsValue(nil, converted)
	if err != nil {
		return nil, err
	}
	cache.tools, cache.strict, cache.grammar, cache.encoded = slices.Clone(tools), compat.supportsStrictMode, compat.supportsOpenAIGrammarTools, encoded
	return encoded, nil
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
		// Upstream returns after the first system/developer message whether or
		// not the cache anchor could be attached (OA-m4).
		addOpenAICompletionsCacheControlToText(message, cacheControl)
		break
	}
	if len(tools) > 0 {
		if tool, ok := tools[len(tools)-1].(map[string]any); ok {
			tool["cache_control"] = cacheControl
		}
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message, ok := messages[index].(map[string]any)
		if !ok || (message["role"] != "user" && message["role"] != "assistant" && message["role"] != "tool") {
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
	var budget *float64
	if effort != "" {
		ceiling := model.MaxTokens
		if options.MaxTokens != nil && *options.MaxTokens != 0 {
			ceiling = *options.MaxTokens
		}
		_, requested := adjustMaxTokensForThinking(0, 1e15, *options.ReasoningEffort, options.ThinkingBudgets)
		value := min(requested, max(0, ceiling-1024))
		if value > 0 {
			budget = &value
		}
	}
	if compat.thinkingTokenBudgetField != "" && budget != nil {
		payload[compat.thinkingTokenBudgetField] = *budget
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
		if effort != "" && compat.supportsReasoningEffort {
			payload["reasoning_effort"] = mappedThinkingOr(model, ai.ModelThinkingLevel(effort), effort)
		}
	case ai.ThinkingFormatQwenChatTemplate:
		payload["chat_template_kwargs"] = map[string]any{"enable_thinking": effort != "", "preserve_thinking": true}
	case ai.ThinkingFormatChatTemplate:
		if kwargs := buildOpenAICompletionsChatTemplateKwargs(model, effort, compat.chatTemplateKwargs, budget); len(kwargs) > 0 {
			payload["chat_template_kwargs"] = kwargs
		}
	case ai.ThinkingFormatBaseten:
		if args := buildOpenAICompletionsChatTemplateKwargs(model, effort, compat.chatTemplateArgs, budget); len(args) > 0 {
			payload["chat_template_args"] = args
		}
		if compat.supportsReasoningEffort {
			level := ai.ModelThinkingOff
			if effort != "" {
				level = ai.ModelThinkingLevel(effort)
			}
			if value, exists, nonNull := thinkingMapValue(model, level); exists {
				if nonNull {
					payload["reasoning_effort"] = value
				}
			} else if effort != "" {
				payload["reasoning_effort"] = effort
			}
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

func buildOpenAICompletionsChatTemplateKwargs(model *ai.Model, effort string, values map[string]any, budget ...*float64) map[string]any {
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
		if object["$var"] == "thinking.budget" {
			if len(budget) > 0 && budget[0] != nil {
				result[name] = *budget[0]
			}
			continue
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
		output:        output,
		textIndex:     -1,
		thinkingIndex: -1,
		toolsByIndex:  map[int]*completionsToolState{},
		toolsByID:     map[string]*completionsToolState{},
		toolStates:    map[*ai.ToolCall]*completionsToolState{},
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
	var chunk struct{ ID, Model, Usage, Choices json.RawMessage }
	readMembers(trimmed, rawMember{"id", &chunk.ID}, rawMember{"model", &chunk.Model}, rawMember{"usage", &chunk.Usage}, rawMember{"choices", &chunk.Choices})
	if state.output.ResponseID == nil {
		if id, ok := rawJSONString(chunk.ID); ok && id != "" {
			state.output.ResponseID = &id
		}
	}
	if state.output.ResponseModel == nil {
		if responseModel, ok := rawJSONString(chunk.Model); ok && responseModel != "" && responseModel != model.ID {
			state.output.ResponseModel = &responseModel
		}
	}
	if rawJSTruthy(chunk.Usage) {
		state.output.Usage = parseOpenAICompletionsUsage(chunk.Usage, model)
	}
	choices := rawJSONArray(chunk.Choices)
	if len(choices) == 0 {
		return nil
	}
	var choice struct{ Delta, FinishReason, Usage json.RawMessage }
	readMembers(choices[0], rawMember{"delta", &choice.Delta}, rawMember{"finish_reason", &choice.FinishReason}, rawMember{"usage", &choice.Usage})
	if !rawJSTruthy(chunk.Usage) && rawJSTruthy(choice.Usage) {
		state.output.Usage = parseOpenAICompletionsUsage(choice.Usage, model)
	}
	if reason, ok := rawJSONString(choice.FinishReason); ok && reason != "" {
		state.output.RawStopReason = &reason
		stopReason, errorMessage := mapOpenAICompletionsStopReason(reason)
		state.output.StopReason = stopReason
		if errorMessage != nil {
			state.output.ErrorMessage = errorMessage
		}
		state.hasFinishReason = true
	}
	queued := make([]ai.AssistantMessageEvent, 0, 4)
	if err := state.consumeDelta(model, choice.Delta, func(event ai.AssistantMessageEvent) error {
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
	var delta openAICompletionsDelta
	readMembers(trimmed, rawMember{"content", &delta.Content}, rawMember{"reasoning_content", &delta.ReasoningContent},
		rawMember{"reasoning", &delta.Reasoning}, rawMember{"reasoning_text", &delta.ReasoningText},
		rawMember{"tool_calls", &delta.ToolCalls}, rawMember{"reasoning_details", &delta.ReasoningDetails})
	if content, ok := rawJSONString(delta.Content); ok && content != "" {
		if state.text == nil {
			state.text = &ai.TextContent{}
			state.output.Content = append(state.output.Content, state.text)
			state.textIndex = len(state.output.Content) - 1
			if err := emit(ai.TextStartEvent{ContentIndex: state.textIndex, Partial: state.output}); err != nil {
				return err
			}
		}
		state.text.Text = state.textBuffer.append(state.text.Text, content)
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
		state.thinking.Thinking = state.thinkingBuffer.append(state.thinking.Thinking, reasoning)
		if err := emit(ai.ThinkingDeltaEvent{ContentIndex: state.thinkingIndex, Delta: reasoning, Partial: state.output}); err != nil {
			return err
		}
	}
	for _, rawCall := range rawJSONArray(delta.ToolCalls) {
		if err := state.consumeToolCall(rawCall, emit); err != nil {
			return err
		}
	}
	for _, rawDetail := range rawJSONArray(delta.ReasoningDetails) {
		if err := state.consumeReasoningDetail(rawDetail, emit); err != nil {
			return err
		}
	}
	return nil
}

type openAICompletionsDelta struct {
	Content, ReasoningContent, Reasoning, ReasoningText, ToolCalls, ReasoningDetails json.RawMessage
}

func firstOpenAICompletionsReasoning(delta openAICompletionsDelta) (string, string) {
	for _, field := range []struct {
		name string
		raw  json.RawMessage
	}{{"reasoning_content", delta.ReasoningContent}, {"reasoning", delta.Reasoning}, {"reasoning_text", delta.ReasoningText}} {
		if value, ok := rawJSONString(field.raw); ok && value != "" {
			return field.name, value
		}
	}
	return "", ""
}

// rawMember names a member readMembers reads into value.
type rawMember struct {
	name  string
	value *json.RawMessage
}

// readMembers reads the members of the JSON object data, which readSSE has
// validated, by exact name, keeping the last duplicate as a map decode does.
func readMembers(data []byte, members ...rawMember) {
	jsonwire.EachMember(data, func(name, value []byte) bool {
		for _, member := range members {
			if string(name) == member.name {
				*member.value = value
			}
		}
		return true
	})
}

func (state *completionsStreamState) consumeToolCall(raw json.RawMessage, emit func(ai.AssistantMessageEvent) error) error {
	// A call that is not an object, or null, is skipped as a failed map decode.
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' && string(trimmed) != "null" {
		return nil
	}
	var rawIndex, rawID, rawFunction, rawCustom, rawName, rawArguments, rawCustomName, rawCustomInput json.RawMessage
	readMembers(raw, rawMember{"index", &rawIndex}, rawMember{"id", &rawID}, rawMember{"function", &rawFunction}, rawMember{"custom", &rawCustom})
	readMembers(rawFunction, rawMember{"name", &rawName}, rawMember{"arguments", &rawArguments})
	readMembers(rawCustom, rawMember{"name", &rawCustomName}, rawMember{"input", &rawCustomInput})
	streamIndex, hasIndex := rawJSONInt(rawIndex)
	id, _ := rawJSONString(rawID)
	hasFunction, hasCustom := len(rawFunction) > 0 && rawFunction[0] == '{', len(rawCustom) > 0 && rawCustom[0] == '{'
	name, _ := rawJSONString(rawName)
	arguments, _ := rawJSONString(rawArguments)
	customName, _ := rawJSONString(rawCustomName)
	customInput, _ := rawJSONString(rawCustomInput)
	if name == "" {
		name = customName
	}

	var stateForCall *completionsToolState
	if hasIndex {
		stateForCall = state.toolsByIndex[streamIndex]
	}
	if stateForCall == nil && id != "" {
		stateForCall = state.toolsByID[id]
	}
	if stateForCall == nil {
		inputProperty := ""
		if hasCustom && !hasFunction {
			inputProperty = state.grammarToolInputProperties[name]
			if inputProperty == "" {
				inputProperty = "input"
			}
		}
		argumentsValue := map[string]any{}
		var partialArgs *string
		var customState *responsesCustomInput
		if inputProperty != "" {
			argumentsValue[inputProperty] = ""
			customState = &responsesCustomInput{property: inputProperty}
		} else {
			value := ""
			partialArgs = &value
		}
		call := &ai.ToolCall{ID: id, Name: name, Arguments: argumentsValue, PartialArgs: partialArgs}
		stateForCall = &completionsToolState{block: call, contentIndex: len(state.output.Content)}
		stateForCall.customInput = customState
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

	if stateForCall.block.ID == "" && id != "" {
		stateForCall.block.ID = id
		state.toolsByID[id] = stateForCall
	}
	if stateForCall.block.Name == "" && name != "" {
		stateForCall.block.Name = name
	}
	if hasCustom && !hasFunction && stateForCall.customInput == nil {
		inputProperty := state.grammarToolInputProperties[stateForCall.block.Name]
		if inputProperty == "" {
			inputProperty = "input"
		}
		stateForCall.block.Arguments = map[string]any{inputProperty: ""}
		stateForCall.block.PartialArgs = nil
		stateForCall.customInput = &responsesCustomInput{property: inputProperty}
		stateForCall.partialArgs = ""
	}
	if arguments != "" {
		stateForCall.partialArgs = stateForCall.argsBuffer.append(stateForCall.partialArgs, arguments)
		partialArgs := stateForCall.partialArgs
		stateForCall.block.PartialArgs = &partialArgs
		if stateForCall.argsBuffer.shouldParse() {
			stateForCall.block.Arguments = parseOpenAICompletionsToolArguments(stateForCall.partialArgs)
		}
	} else if customInput != "" && stateForCall.customInput != nil {
		current, _ := stateForCall.block.Arguments[stateForCall.customInput.property].(string)
		delta, err := appendGrammarToolInputJSONDelta(
			&stateForCall.customInput.buffer,
			stateForCall.customInput.property,
			current+customInput,
			false,
		)
		if err != nil {
			return err
		}
		stateForCall.block.Arguments = map[string]any{stateForCall.customInput.property: current + customInput}
		if delta != nil {
			arguments = *delta
		}
	}
	return emit(ai.ToolCallDeltaEvent{ContentIndex: stateForCall.contentIndex, Delta: arguments, Partial: state.output})
}

func parseOpenAIReasoningDetails(signature string) []json.RawMessage {
	var details []json.RawMessage
	if json.Unmarshal([]byte(signature), &details) != nil || len(details) == 0 {
		return nil
	}
	for i, detail := range details {
		if !validOpenAIReasoningDetail(detail) {
			return nil
		}
		details[i], _ = ai.NormalizeJSONStringifyJSON(detail)
	}
	return details
}

func validOpenAIReasoningDetail(raw json.RawMessage) bool {
	var d map[string]json.RawMessage
	if json.Unmarshal(raw, &d) != nil || d == nil {
		return false
	}
	kind, _ := rawJSONString(d["type"])
	keys := []string{"id", "format"}
	if kind == "reasoning.text" {
		keys = append(keys, "signature")
	}
	for _, key := range keys {
		if value, ok := d[key]; ok {
			if string(value) == "null" && key != "format" {
				continue
			}
			if _, ok := rawJSONString(value); !ok {
				return false
			}
		}
	}
	if value, ok := d["index"]; ok {
		var n float64
		if json.Unmarshal(value, &n) != nil || string(value) == "null" {
			return false
		}
	}
	field := map[string]string{"reasoning.text": "text", "reasoning.summary": "summary", "reasoning.encrypted": "data"}[kind]
	_, ok := rawJSONString(d[field])
	return field != "" && ok
}

func (state *completionsStreamState) consumeReasoningDetail(raw json.RawMessage, emit func(ai.AssistantMessageEvent) error) error {
	if !validOpenAIReasoningDetail(raw) {
		return nil
	}
	if state.thinking == nil {
		signature := ""
		state.thinking = &ai.ThinkingContent{ThinkingSignature: &signature}
		state.output.Content = append(state.output.Content, state.thinking)
		state.thinkingIndex = len(state.output.Content) - 1
		if err := emit(ai.ThinkingStartEvent{ContentIndex: state.thinkingIndex, Partial: state.output}); err != nil {
			return err
		}
	}
	normalized, _ := ai.NormalizeJSONStringifyJSON(raw)
	var current map[string]json.RawMessage
	_ = json.Unmarshal(normalized, &current)
	kind, _ := rawJSONString(current["type"])
	if len(state.reasoningDetails) > 0 && (kind == "reasoning.text" || kind == "reasoning.summary") {
		index := len(state.reasoningDetails) - 1
		var previous map[string]json.RawMessage
		_ = json.Unmarshal(state.reasoningDetails[index], &previous)
		priorKind, _ := rawJSONString(previous["type"])
		if priorKind == kind {
			field := "text"
			if kind == "reasoning.summary" {
				field = "summary"
			}
			a, _ := rawJSONString(previous[field])
			b, _ := rawJSONString(current[field])
			previous[field], _ = jsonwire.MarshalString(a + b)
			for _, key := range []string{"signature", "id", "format", "index"} {
				if key == "signature" && kind != "reasoning.text" {
					continue
				}
				prior, exists := previous[key]
				if !exists || string(prior) == "null" || ((key == "signature" || key == "format") && string(prior) == `""`) {
					if next, ok := current[key]; ok {
						previous[key] = next
					}
				}
			}
			// Existing JSON member order comes from the provider replay metadata.
			var ordered []string
			dec := json.NewDecoder(bytes.NewReader(state.reasoningDetails[index]))
			_, _ = dec.Token()
			for dec.More() {
				k, _ := dec.Token()
				var ignored json.RawMessage
				_ = dec.Decode(&ignored)
				ordered = append(ordered, k.(string))
			}
			values := map[string]any{}
			for k, v := range previous {
				values[k] = v
			}
			for _, k := range []string{"signature", "id", "format", "index"} {
				if _, ok := previous[k]; ok {
					found := false
					for _, old := range ordered {
						found = found || old == k
					}
					if !found {
						ordered = append(ordered, k)
					}
				}
			}
			state.reasoningDetails[index], _ = marshalOpenAICompletionsObjectWithKeys(values, ordered)
			return nil
		}
	}
	state.reasoningDetails = append(state.reasoningDetails, normalized)
	return nil
}

func (state *completionsStreamState) applyReasoningDetails() {
	if state.thinking != nil && len(state.reasoningDetails) > 0 {
		encoded, _ := ai.Marshal(state.reasoningDetails)
		signature := string(encoded)
		state.thinking.ThinkingSignature = &signature
	}
}

func (state *completionsStreamState) finishBlocks(emit func(ai.AssistantMessageEvent) error) error {
	state.applyReasoningDetails()
	// Upstream queues the complete end-event batch before its async consumer
	// resumes, so even earlier end events observe finalized tool-call blocks.
	finalCustomDeltas := make(map[*ai.ToolCall]string)
	for _, rawBlock := range state.output.Content {
		block, ok := rawBlock.(*ai.ToolCall)
		if !ok {
			continue
		}
		if tool := state.toolStates[block]; tool != nil {
			if tool.customInput != nil {
				current, _ := block.Arguments[tool.customInput.property].(string)
				delta, err := appendGrammarToolInputJSONDelta(
					&tool.customInput.buffer, tool.customInput.property, current, true,
				)
				if err != nil {
					return err
				}
				block.Arguments = map[string]any{tool.customInput.property: current}
				if delta != nil {
					finalCustomDeltas[block] = *delta
				}
				tool.customInput = nil
			} else if err := ai.SetToolCallArgumentsJSON(block, []byte(tool.partialArgs)); err != nil {
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
			if delta, ok := finalCustomDeltas[block]; ok {
				if err := emit(ai.ToolCallDeltaEvent{ContentIndex: index, Delta: delta, Partial: state.output}); err != nil {
					return err
				}
			}
			if err := emit(ai.ToolCallEndEvent{ContentIndex: index, ToolCall: block, Partial: state.output}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (state *completionsStreamState) clearScratch() {
	state.applyReasoningDetails()
	for block, tool := range state.toolStates {
		block.PartialArgs = nil
		block.StreamIndex = nil
		tool.customInput = nil
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
	var rawPrompt, rawCompletion, rawCacheHit, rawCached, rawPromptDetails, rawCompletionDetails, rawCost json.RawMessage
	readMembers(raw, rawMember{"prompt_tokens", &rawPrompt}, rawMember{"completion_tokens", &rawCompletion},
		rawMember{"prompt_cache_hit_tokens", &rawCacheHit}, rawMember{"cached_tokens", &rawCached},
		rawMember{"prompt_tokens_details", &rawPromptDetails}, rawMember{"completion_tokens_details", &rawCompletionDetails}, rawMember{"cost", &rawCost})
	var rawDetailsCached, rawCacheWrite, rawReasoning json.RawMessage
	readMembers(rawPromptDetails, rawMember{"cached_tokens", &rawDetailsCached}, rawMember{"cache_write_tokens", &rawCacheWrite})
	readMembers(rawCompletionDetails, rawMember{"reasoning_tokens", &rawReasoning})
	promptTokens, _ := rawJSONInt64(rawPrompt)
	completionTokens, _ := rawJSONInt64(rawCompletion)
	promptCacheHit, ok := rawJSONInt64(rawCacheHit)
	if !ok {
		promptCacheHit, _ = rawJSONInt64(rawCached)
	}
	cacheRead := promptCacheHit
	if value, ok := rawJSONInt64(rawDetailsCached); ok {
		cacheRead = value
	}
	cacheWrite, _ := rawJSONInt64(rawCacheWrite)
	reasoning, _ := rawJSONInt64(rawReasoning)
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
	if model.Provider == "openrouter" || openRouterHost(model.BaseURL) {
		var reportedCost *float64
		if json.Unmarshal(rawCost, &reportedCost) == nil && reportedCost != nil {
			// Component costs remain estimates; the reported total includes routing and discounts.
			result.Cost.Total = *reportedCost
		}
	}
	return result
}

// openRouterHost matches the endpoint's host, so a proxy path that merely
// mentions openrouter.ai can't make its reported cost authoritative.
func openRouterHost(baseURL string) bool {
	// Without escapes, the host is part of the URL as written.
	if !strings.Contains(baseURL, "openrouter.ai") && !strings.Contains(baseURL, "%") {
		return false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	return host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai")
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

// rawJSONArray is the elements of an array readSSE has validated.
func rawJSONArray(raw json.RawMessage) []json.RawMessage { return jsonwire.Elements(raw) }

func rawJSONString(raw json.RawMessage) (string, bool) {
	// A string of valid UTF-8 decodes to its bytes without escapes, and as
	// jsonwire decodes it without \u escapes (the only place jsonwire differs:
	// it keeps lone surrogates).
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' && utf8.Valid(raw) {
		if bytes.IndexByte(raw, '\\') < 0 {
			return string(raw[1 : len(raw)-1]), true
		}
		if !bytes.Contains(raw, []byte(`\u`)) {
			value, err := jsonwire.UnmarshalString(raw)
			return value, err == nil
		}
	}
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
	// A plain JSON integer parses directly; anything else takes the decoder's rules.
	if digits := bytes.TrimPrefix(raw, []byte("-")); len(digits) > 0 && len(digits) < 19 && (digits[0] != '0' || len(digits) == 1) &&
		!bytes.ContainsFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) {
		value, _ := strconv.ParseInt(string(raw), 10, 64)
		return value, true
	}
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
