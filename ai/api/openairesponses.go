package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
	"github.com/OrdalieTech/pi-go/internal/partialjson"
)

const openAIResponsesMinOutputTokens float64 = 16

var openAIResponsesToolCallProviders = map[ai.ProviderID]struct{}{
	"openai":                 {},
	"openai-codex":           {},
	"opencode":               {},
	"azure-openai-responses": {},
}

type OpenAIResponsesOptions struct {
	ai.StreamOptions
	ReasoningEffort  *string `json:"reasoningEffort,omitempty"`
	ReasoningSummary *string `json:"reasoningSummary,omitempty"`
	ServiceTier      *string `json:"serviceTier,omitempty"`
	ToolChoice       any     `json:"toolChoice,omitempty"`
}

// OpenAIResponsesPayload is the mutable request value passed to PayloadHook.
type OpenAIResponsesPayload struct {
	Model                string                 `json:"model"`
	Input                []any                  `json:"input"`
	Stream               bool                   `json:"stream"`
	PromptCacheKey       *string                `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention *string                `json:"prompt_cache_retention,omitempty"`
	Store                bool                   `json:"store"`
	MaxOutputTokens      *float64               `json:"max_output_tokens,omitempty"`
	Temperature          *float64               `json:"temperature,omitempty"`
	ServiceTier          *string                `json:"service_tier,omitempty"`
	Tools                []OpenAIResponsesTool  `json:"tools,omitempty"`
	ToolChoice           any                    `json:"tool_choice,omitempty"`
	Reasoning            *OpenAIReasoningParams `json:"reasoning,omitempty"`
	Include              []string               `json:"include,omitempty"`
}

type OpenAIReasoningParams struct {
	Effort  string  `json:"effort"`
	Summary *string `json:"summary,omitempty"`
}

type OpenAIResponsesTool struct {
	Type         string            `json:"type"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Parameters   jsonschema.Schema `json:"parameters"`
	Strict       bool              `json:"strict"`
	DeferLoading *bool             `json:"defer_loading,omitempty"`
}

type responsesInputMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type responsesInputText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesInputImage struct {
	Type     string `json:"type"`
	Detail   string `json:"detail"`
	ImageURL string `json:"image_url"`
}

type responsesOutputText struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}

type responsesOutputMessage struct {
	Type    string                `json:"type"`
	Role    string                `json:"role"`
	Content []responsesOutputText `json:"content"`
	Status  string                `json:"status"`
	ID      string                `json:"id"`
	Phase   *string               `json:"phase,omitempty"`
}

type responsesFunctionCall struct {
	Type      string  `json:"type"`
	ID        *string `json:"id,omitempty"`
	CallID    string  `json:"call_id"`
	Name      string  `json:"name"`
	Arguments string  `json:"arguments"`
}

type responsesFunctionCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output any    `json:"output"`
}

type responsesToolSearchCall struct {
	Type      string                       `json:"type"`
	CallID    string                       `json:"call_id"`
	Execution string                       `json:"execution"`
	Status    string                       `json:"status"`
	Arguments responsesToolSearchArguments `json:"arguments"`
}

type responsesToolSearchArguments struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type responsesToolSearchOutput struct {
	Type      string                `json:"type"`
	CallID    string                `json:"call_id"`
	Execution string                `json:"execution"`
	Status    string                `json:"status"`
	Tools     []OpenAIResponsesTool `json:"tools"`
}

func StreamOpenAIResponses(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: OpenAI Responses model is nil")
	}
	options := &OpenAIResponsesOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamOpenAIResponsesWithOptions(ctx, request.Model, request.Context, options)
}

func StreamSimpleOpenAIResponses(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: OpenAI Responses model is nil")
	}
	base := buildBaseStreamOptions(model, requestContext, options)
	var requestedReasoning *ai.ThinkingLevel
	if options != nil {
		requestedReasoning = options.Reasoning
	}
	reasoning := clampSimpleReasoning(model, requestedReasoning)
	var effort *string
	if reasoning != nil {
		value := string(*reasoning)
		effort = &value
	}
	return StreamOpenAIResponsesWithOptions(ctx, model, requestContext, &OpenAIResponsesOptions{
		StreamOptions:   base,
		ReasoningEffort: effort,
	})
}

func StreamOpenAIResponsesWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *OpenAIResponsesOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: OpenAI Responses model is nil")
	}
	output := newAssistantMessage(model)
	streamOptions := responsesStreamOptions(options)

	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		sink := func(event ai.AssistantMessageEvent) bool { return yield(event, nil) }
		fail := func(err error) {
			clearResponsesStreamingFields(output)
			sink(streamFailure(ctx, output, err, "OpenAI API error"))
		}
		if _, err := resolveOpenAIAPIKey(model, streamOptions); err != nil {
			fail(err)
			return
		}
		payload, compat, err := buildOpenAIResponsesPayload(model, requestContext, options)
		if err != nil {
			fail(err)
			return
		}
		hookedPayload, err := applyPayloadHook(ctx, model, streamOptions, payload)
		if err != nil {
			fail(err)
			return
		}

		headers := buildOpenAIResponsesHeaders(model, requestContext, streamOptions, compat)
		response, err := postOpenAIStream(ctx, model, streamOptions, "responses", hookedPayload, headers)
		if err != nil {
			fail(err)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if !sink(ai.StartEvent{Partial: output}) {
			return
		}

		processor := newOpenAIResponsesProcessor(model, output, options, sink)
		err = readSSE(response.Body, processor.handle)
		if errors.Is(err, errStopSSE) {
			return
		}
		if err == nil && !processor.sawTerminalResponseEvent {
			err = errors.New("OpenAI Responses stream ended before a terminal response event")
		}
		if err == nil && ctx.Err() != nil {
			err = errors.New("Request was aborted") //nolint:staticcheck // Exact upstream error text is observable.
		}
		if err == nil && (output.StopReason == ai.StopReasonAborted || output.StopReason == ai.StopReasonError) {
			err = errors.New("An unknown error occurred") //nolint:staticcheck // Exact upstream error text is observable.
		}
		if err != nil {
			fail(err)
			return
		}
		clearResponsesStreamingFields(output)
		sink(ai.DoneEvent{Reason: output.StopReason, Message: output})
	}, nil
}

func responsesStreamOptions(options *OpenAIResponsesOptions) *ai.StreamOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions
}

func buildOpenAIResponsesPayload(
	model *ai.Model,
	requestContext ai.Context,
	options *OpenAIResponsesOptions,
) (*OpenAIResponsesPayload, openAIResponsesCompat, error) {
	compat, err := getOpenAIResponsesCompat(model)
	if err != nil {
		return nil, compat, err
	}
	placement := splitResponsesTools(requestContext, compat.supportsToolSearch)
	input, err := convertResponsesMessages(model, requestContext, placement.deferred, compat.supportsDeveloperRole)
	if err != nil {
		return nil, compat, err
	}
	streamOptions := responsesStreamOptions(options)
	cacheRetention := resolveCacheRetention(streamOptions)
	payload := &OpenAIResponsesPayload{
		Model:  model.ID,
		Input:  input,
		Stream: true,
		Store:  false,
	}
	if cacheRetention != ai.CacheRetentionNone && streamOptions != nil && streamOptions.SessionID != nil {
		value := clampOpenAIPromptCacheKey(streamOptions.SessionID)
		if key, ok := value.(string); ok {
			payload.PromptCacheKey = &key
		}
	}
	if cacheRetention == ai.CacheRetentionLong && compat.supportsLongCacheRetention {
		retention := "24h"
		payload.PromptCacheRetention = &retention
	}
	if streamOptions != nil {
		if streamOptions.MaxTokens != nil && *streamOptions.MaxTokens != 0 {
			value := max(*streamOptions.MaxTokens, openAIResponsesMinOutputTokens)
			payload.MaxOutputTokens = &value
		}
		payload.Temperature = streamOptions.Temperature
	}
	if options != nil {
		payload.ServiceTier = options.ServiceTier
		payload.ToolChoice = options.ToolChoice
	}
	if len(placement.immediate) > 0 {
		payload.Tools = convertResponsesTools(placement.immediate, false)
	}
	applyResponsesReasoning(payload, model, options)
	return payload, compat, nil
}

func applyResponsesReasoning(payload *OpenAIResponsesPayload, model *ai.Model, options *OpenAIResponsesOptions) {
	if !model.Reasoning {
		return
	}
	effortSet := options != nil && options.ReasoningEffort != nil && *options.ReasoningEffort != ""
	summarySet := options != nil && options.ReasoningSummary != nil && *options.ReasoningSummary != ""
	if effortSet || summarySet {
		effort := "medium"
		if effortSet {
			effort = mappedThinkingLevel(model, *options.ReasoningEffort, *options.ReasoningEffort)
		}
		summary := "auto"
		if summarySet {
			summary = *options.ReasoningSummary
		}
		payload.Reasoning = &OpenAIReasoningParams{Effort: effort, Summary: &summary}
		payload.Include = []string{"reasoning.encrypted_content"}
	} else if model.Provider != "github-copilot" && supportsOffReasoning(model) {
		payload.Reasoning = &OpenAIReasoningParams{Effort: mappedThinkingLevel(model, "off", "none")}
	}
	if model.Provider == "xai" {
		payload.Include = []string{"reasoning.encrypted_content"}
	}
}

func mappedThinkingLevel(model *ai.Model, level, fallback string) string {
	if model.ThinkingLevelMap == nil {
		return fallback
	}
	mapped, ok := (*model.ThinkingLevelMap)[ai.ModelThinkingLevel(level)]
	if !ok || mapped == nil {
		return fallback
	}
	return *mapped
}

func supportsOffReasoning(model *ai.Model) bool {
	if model.ThinkingLevelMap == nil {
		return true
	}
	mapped, ok := (*model.ThinkingLevelMap)[ai.ModelThinkingOff]
	return !ok || mapped != nil
}

type openAIResponsesCompat struct {
	supportsDeveloperRole      bool
	sessionAffinityFormat      ai.SessionAffinityFormat
	supportsLongCacheRetention bool
	supportsToolSearch         bool
}

func getOpenAIResponsesCompat(model *ai.Model) (openAIResponsesCompat, error) {
	raw, err := decodeCompat[ai.OpenAIResponsesCompat](model)
	if err != nil {
		return openAIResponsesCompat{}, err
	}
	compat := openAIResponsesCompat{
		supportsDeveloperRole:      true,
		sessionAffinityFormat:      detectResponsesSessionAffinity(model),
		supportsLongCacheRetention: true,
	}
	if raw.SupportsDeveloperRole != nil {
		compat.supportsDeveloperRole = *raw.SupportsDeveloperRole
	}
	if raw.SessionAffinityFormat != nil {
		compat.sessionAffinityFormat = *raw.SessionAffinityFormat
	}
	if raw.SupportsLongCacheRetention != nil {
		compat.supportsLongCacheRetention = *raw.SupportsLongCacheRetention
	}
	if raw.SupportsToolSearch != nil {
		compat.supportsToolSearch = *raw.SupportsToolSearch
	}
	return compat, nil
}

func detectResponsesSessionAffinity(model *ai.Model) ai.SessionAffinityFormat {
	if model.Provider == "openrouter" || strings.Contains(model.BaseURL, "openrouter.ai") {
		return ai.SessionAffinityOpenRouter
	}
	return ai.SessionAffinityOpenAI
}

func buildOpenAIResponsesHeaders(
	model *ai.Model,
	requestContext ai.Context,
	options *ai.StreamOptions,
	compat openAIResponsesCompat,
) http.Header {
	headers := copyModelHeaders(model)
	addCopilotHeaders(headers, model, requestContext)
	if resolveCacheRetention(options) != ai.CacheRetentionNone && options != nil && options.SessionID != nil && *options.SessionID != "" {
		sessionID := *options.SessionID
		switch compat.sessionAffinityFormat {
		case ai.SessionAffinityOpenRouter:
			headers.Set("x-session-id", sessionID)
		case ai.SessionAffinityOpenAI:
			headers.Set("session_id", sessionID)
			headers.Set("x-client-request-id", sessionID)
		case ai.SessionAffinityOpenAINoSession:
			headers.Set("x-client-request-id", sessionID)
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

type responsesToolPlacement struct {
	immediate []ai.Tool
	deferred  map[string]ai.Tool
}

func splitResponsesTools(requestContext ai.Context, enabled bool) responsesToolPlacement {
	unique := make(map[string]ai.Tool)
	order := make([]string, 0)
	if requestContext.Tools != nil {
		for _, tool := range *requestContext.Tools {
			if _, ok := unique[tool.Name]; !ok {
				order = append(order, tool.Name)
			}
			unique[tool.Name] = tool
		}
	}
	if !enabled {
		immediate := make([]ai.Tool, 0, len(order))
		for _, name := range order {
			immediate = append(immediate, unique[name])
		}
		return responsesToolPlacement{immediate: immediate, deferred: map[string]ai.Tool{}}
	}

	deferredNames := make(map[string]struct{})
	usedNames := make(map[string]struct{})
	for _, message := range requestContext.Messages {
		switch value := message.(type) {
		case *ai.AssistantMessage:
			for _, content := range value.Content {
				if call, ok := content.(*ai.ToolCall); ok {
					usedNames[call.Name] = struct{}{}
				}
			}
		case *ai.ToolResultMessage:
			if value.AddedToolNames == nil {
				continue
			}
			for _, name := range *value.AddedToolNames {
				if _, used := usedNames[name]; !used {
					deferredNames[name] = struct{}{}
				}
			}
		}
	}
	placement := responsesToolPlacement{deferred: make(map[string]ai.Tool)}
	for _, name := range order {
		tool := unique[name]
		if _, deferred := deferredNames[name]; deferred {
			placement.deferred[name] = tool
		} else {
			placement.immediate = append(placement.immediate, tool)
		}
	}
	return placement
}

func convertResponsesTools(tools []ai.Tool, deferLoading bool) []OpenAIResponsesTool {
	result := make([]OpenAIResponsesTool, 0, len(tools))
	for _, tool := range tools {
		converted := OpenAIResponsesTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
			Strict:      false,
		}
		if deferLoading {
			value := true
			converted.DeferLoading = &value
		}
		result = append(result, converted)
	}
	return result
}

func convertResponsesMessages(
	model *ai.Model,
	requestContext ai.Context,
	deferredTools map[string]ai.Tool,
	supportsDeveloperRole bool,
) ([]any, error) {
	messages := transformMessages(requestContext.Messages, model, normalizeResponsesToolCallID)
	result := make([]any, 0, len(messages)+1)
	loadedToolNames := make(map[string]struct{})
	if requestContext.SystemPrompt != nil && *requestContext.SystemPrompt != "" {
		role := "system"
		if model.Reasoning && supportsDeveloperRole {
			role = "developer"
		}
		result = append(result, responsesInputMessage{Role: role, Content: sanitizeText(*requestContext.SystemPrompt)})
	}

	messageIndex := 0
	for _, message := range messages {
		switch value := message.(type) {
		case *ai.UserMessage:
			content := make([]any, 0, len(value.Content.Blocks))
			if value.Content.Text != nil {
				content = append(content, responsesInputText{Type: "input_text", Text: sanitizeText(*value.Content.Text)})
			} else {
				for _, item := range value.Content.Blocks {
					switch block := item.(type) {
					case *ai.TextContent:
						content = append(content, responsesInputText{Type: "input_text", Text: sanitizeText(block.Text)})
					case *ai.ImageContent:
						content = append(content, responsesInputImage{
							Type:     "input_image",
							Detail:   "auto",
							ImageURL: "data:" + block.MimeType + ";base64," + block.Data,
						})
					}
				}
			}
			if len(content) == 0 {
				continue
			}
			result = append(result, responsesInputMessage{Role: "user", Content: content})
		case *ai.AssistantMessage:
			output := make([]any, 0, len(value.Content))
			isDifferentModel := value.Model != model.ID && value.Provider == model.Provider && value.API == model.API
			textBlockIndex := 0
			for _, content := range value.Content {
				switch block := content.(type) {
				case *ai.ThinkingContent:
					if block.ThinkingSignature == nil {
						continue
					}
					raw := json.RawMessage(*block.ThinkingSignature)
					if !json.Valid(raw) {
						return nil, fmt.Errorf("parse OpenAI reasoning signature: invalid JSON")
					}
					output = append(output, raw)
				case *ai.TextContent:
					id, phase := parseResponsesTextSignature(block.TextSignature)
					fallback := fmt.Sprintf("msg_pi_%d", messageIndex)
					if textBlockIndex > 0 {
						fallback += fmt.Sprintf("_%d", textBlockIndex)
					}
					textBlockIndex++
					if id == "" {
						id = fallback
					} else if len(utf16.Encode([]rune(id))) > 64 {
						id = "msg_" + shortHash(id)
					}
					output = append(output, responsesOutputMessage{
						Type:   "message",
						Role:   "assistant",
						Status: "completed",
						ID:     id,
						Phase:  phase,
						Content: []responsesOutputText{{
							Type:        "output_text",
							Text:        sanitizeText(block.Text),
							Annotations: []any{},
						}},
					})
				case *ai.ToolCall:
					callID, itemID := splitResponsesToolCallID(block.ID)
					if isDifferentModel && itemID != nil && strings.HasPrefix(*itemID, "fc_") {
						itemID = nil
					}
					encoded, err := ai.MarshalToolCallArguments(block)
					if err != nil {
						return nil, fmt.Errorf("marshal OpenAI tool arguments: %w", err)
					}
					output = append(output, responsesFunctionCall{
						Type:      "function_call",
						ID:        itemID,
						CallID:    callID,
						Name:      block.Name,
						Arguments: string(encoded),
					})
				}
			}
			if len(output) == 0 {
				continue
			}
			result = append(result, output...)
		case *ai.ToolResultMessage:
			texts := make([]string, 0)
			hasImages := false
			for _, content := range value.Content {
				switch block := content.(type) {
				case *ai.TextContent:
					texts = append(texts, block.Text)
				case *ai.ImageContent:
					hasImages = true
				}
			}
			textResult := strings.Join(texts, "\n")
			hasText := textResult != ""
			callID, _ := splitResponsesToolCallID(value.ToolCallID)
			var output any
			if hasImages && modelSupportsImage(model) {
				parts := make([]any, 0, len(value.Content))
				if hasText {
					parts = append(parts, responsesInputText{Type: "input_text", Text: sanitizeText(textResult)})
				}
				for _, content := range value.Content {
					if block, ok := content.(*ai.ImageContent); ok {
						parts = append(parts, responsesInputImage{
							Type:     "input_image",
							Detail:   "auto",
							ImageURL: "data:" + block.MimeType + ";base64," + block.Data,
						})
					}
				}
				output = parts
			} else {
				text := textResult
				if text == "" {
					if hasImages {
						text = "(see attached image)"
					} else {
						text = "(no tool output)"
					}
				}
				output = sanitizeText(text)
			}
			result = append(result, responsesFunctionCallOutput{Type: "function_call_output", CallID: callID, Output: output})

			newTools := make([]ai.Tool, 0)
			if value.AddedToolNames != nil {
				for _, name := range *value.AddedToolNames {
					tool, ok := deferredTools[name]
					if !ok {
						continue
					}
					if _, loaded := loadedToolNames[name]; loaded {
						continue
					}
					loadedToolNames[name] = struct{}{}
					newTools = append(newTools, tool)
				}
			}
			if len(newTools) > 0 {
				names := make([]string, len(newTools))
				for index, tool := range newTools {
					names[index] = tool.Name
				}
				searchCallID := "pi_tool_load_" + shortHash(value.ToolCallID+":"+strings.Join(names, ","))
				result = append(result,
					responsesToolSearchCall{
						Type:      "tool_search_call",
						CallID:    searchCallID,
						Execution: "client",
						Status:    "completed",
						Arguments: responsesToolSearchArguments{Query: strings.Join(names, " "), Limit: len(names)},
					},
					responsesToolSearchOutput{
						Type:      "tool_search_output",
						CallID:    searchCallID,
						Execution: "client",
						Status:    "completed",
						Tools:     convertResponsesTools(newTools, true),
					},
				)
			}
		}
		messageIndex++
	}
	return result, nil
}

func parseResponsesTextSignature(signature *string) (string, *string) {
	if signature == nil || *signature == "" {
		return "", nil
	}
	if strings.HasPrefix(*signature, "{") {
		var parsed struct {
			Version int    `json:"v"`
			ID      string `json:"id"`
			Phase   string `json:"phase"`
		}
		if json.Unmarshal([]byte(*signature), &parsed) == nil && parsed.Version == 1 && parsed.ID != "" {
			if parsed.Phase == "commentary" || parsed.Phase == "final_answer" {
				return parsed.ID, &parsed.Phase
			}
			return parsed.ID, nil
		}
	}
	return *signature, nil
}

func splitResponsesToolCallID(id string) (string, *string) {
	parts := strings.Split(id, "|")
	if len(parts) < 2 {
		return parts[0], nil
	}
	itemID := parts[1]
	return parts[0], &itemID
}

func normalizeResponsesToolCallID(id string, model *ai.Model, source *ai.AssistantMessage) string {
	if _, allowed := openAIResponsesToolCallProviders[model.Provider]; !allowed {
		return normalizeResponsesIDPart(id)
	}
	if !strings.Contains(id, "|") {
		return normalizeResponsesIDPart(id)
	}
	parts := strings.Split(id, "|")
	callID := normalizeResponsesIDPart(parts[0])
	itemID := parts[1]
	if source.Provider != model.Provider || source.API != model.API {
		itemID = "fc_" + shortHash(itemID)
	} else {
		itemID = normalizeResponsesIDPart(itemID)
	}
	if !strings.HasPrefix(itemID, "fc_") {
		itemID = normalizeResponsesIDPart("fc_" + itemID)
	}
	return callID + "|" + itemID
}

func normalizeResponsesIDPart(value string) string {
	units := utf16.Encode([]rune(value))
	var builder strings.Builder
	for _, unit := range units {
		char := byte('_')
		if unit <= 0x7f {
			candidate := byte(unit)
			if candidate >= 'a' && candidate <= 'z' || candidate >= 'A' && candidate <= 'Z' ||
				candidate >= '0' && candidate <= '9' || candidate == '_' || candidate == '-' {
				char = candidate
			}
		}
		builder.WriteByte(char)
	}
	result := builder.String()
	if len(result) > 64 {
		result = result[:64]
	}
	return strings.TrimRight(result, "_")
}

func shortHash(value string) string {
	var h1 uint32 = 0xdeadbeef
	var h2 uint32 = 0x41c6ce57
	for _, char := range utf16.Encode([]rune(value)) {
		h1 = (h1 ^ uint32(char)) * uint32(2654435761)
		h2 = (h2 ^ uint32(char)) * uint32(1597334677)
	}
	h1 = (h1^(h1>>16))*uint32(2246822507) ^ (h2^(h2>>13))*uint32(3266489909)
	h2 = (h2^(h2>>16))*uint32(2246822507) ^ (h1^(h1>>13))*uint32(3266489909)
	return strconv.FormatUint(uint64(h2), 36) + strconv.FormatUint(uint64(h1), 36)
}

type openAIResponsesProcessor struct {
	model                    *ai.Model
	output                   *ai.AssistantMessage
	options                  *OpenAIResponsesOptions
	sink                     eventSink
	slots                    map[int]*responsesOutputSlot
	reasoningBlocksByID      map[string]*ai.ThinkingContent
	sawTerminalResponseEvent bool
}

type responsesOutputSlot struct {
	kind         string
	contentIndex int
	thinking     *ai.ThinkingContent
	text         *ai.TextContent
	toolCall     *ai.ToolCall
	partialJSON  string
}

type responsesStreamEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Delta       string          `json:"delta"`
	Arguments   string          `json:"arguments"`
	Code        string          `json:"code"`
	Message     string          `json:"message"`
	Item        json.RawMessage `json:"item"`
	Response    json.RawMessage `json:"response"`
}

type responsesOutputItemEnvelope struct {
	Type      string             `json:"type"`
	ID        *string            `json:"id"`
	CallID    *string            `json:"call_id"`
	Name      string             `json:"name"`
	Arguments string             `json:"arguments"`
	Summary   []responsesText    `json:"summary"`
	Content   []responsesContent `json:"content"`
	Phase     *string            `json:"phase"`
}

type responsesText struct {
	Text string `json:"text"`
}

type responsesContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

func newOpenAIResponsesProcessor(
	model *ai.Model,
	output *ai.AssistantMessage,
	options *OpenAIResponsesOptions,
	sink eventSink,
) *openAIResponsesProcessor {
	return &openAIResponsesProcessor{
		model:               model,
		output:              output,
		options:             options,
		sink:                sink,
		slots:               make(map[int]*responsesOutputSlot),
		reasoningBlocksByID: make(map[string]*ai.ThinkingContent),
	}
}

func (processor *openAIResponsesProcessor) handle(raw json.RawMessage) error {
	var event responsesStreamEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	switch event.Type {
	case "response.created":
		var response struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(event.Response, &response) == nil && response.ID != "" {
			processor.output.ResponseID = &response.ID
		}
	case "response.output_item.added":
		if _, err := processor.createSlot(event.OutputIndex, event.Item); err != nil {
			return err
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		slot := processor.slot(event.OutputIndex, "thinking")
		if slot == nil {
			return nil
		}
		slot.thinking.Thinking += event.Delta
		if !processor.sink(ai.ThinkingDeltaEvent{ContentIndex: slot.contentIndex, Delta: event.Delta, Partial: processor.output}) {
			return errStopSSE
		}
	case "response.reasoning_summary_part.done":
		slot := processor.slot(event.OutputIndex, "thinking")
		if slot == nil {
			return nil
		}
		slot.thinking.Thinking += "\n\n"
		if !processor.sink(ai.ThinkingDeltaEvent{ContentIndex: slot.contentIndex, Delta: "\n\n", Partial: processor.output}) {
			return errStopSSE
		}
	case "response.output_text.delta", "response.refusal.delta":
		slot := processor.slot(event.OutputIndex, "text")
		if slot == nil {
			return nil
		}
		slot.text.Text += event.Delta
		if !processor.sink(ai.TextDeltaEvent{ContentIndex: slot.contentIndex, Delta: event.Delta, Partial: processor.output}) {
			return errStopSSE
		}
	case "response.function_call_arguments.delta":
		slot := processor.slot(event.OutputIndex, "toolCall")
		if slot == nil {
			return nil
		}
		slot.partialJSON += event.Delta
		slot.toolCall.Arguments = parseResponsesArguments(slot.partialJSON)
		if !processor.sink(ai.ToolCallDeltaEvent{ContentIndex: slot.contentIndex, Delta: event.Delta, Partial: processor.output}) {
			return errStopSSE
		}
	case "response.function_call_arguments.done":
		slot := processor.slot(event.OutputIndex, "toolCall")
		if slot == nil {
			return nil
		}
		previous := slot.partialJSON
		slot.partialJSON = event.Arguments
		slot.toolCall.Arguments = parseResponsesArguments(slot.partialJSON)
		if strings.HasPrefix(event.Arguments, previous) {
			delta := strings.TrimPrefix(event.Arguments, previous)
			if delta != "" && !processor.sink(ai.ToolCallDeltaEvent{ContentIndex: slot.contentIndex, Delta: delta, Partial: processor.output}) {
				return errStopSSE
			}
		}
	case "response.output_item.done":
		return processor.finishItem(event.OutputIndex, event.Item)
	case "response.completed", "response.incomplete":
		processor.sawTerminalResponseEvent = true
		return processor.finalizeResponse(event.Response)
	case "error":
		return fmt.Errorf("Error Code %s: %s", event.Code, event.Message)
	case "response.failed":
		processor.sawTerminalResponseEvent = true
		return responsesFailedError(event.Response)
	}
	return nil
}

func (processor *openAIResponsesProcessor) slot(outputIndex int, kind string) *responsesOutputSlot {
	slot := processor.slots[outputIndex]
	if slot == nil || slot.kind != kind {
		return nil
	}
	return slot
}

func (processor *openAIResponsesProcessor) createSlot(outputIndex int, raw json.RawMessage) (*responsesOutputSlot, error) {
	var item responsesOutputItemEnvelope
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	var slot *responsesOutputSlot
	switch item.Type {
	case "reasoning":
		block := &ai.ThinkingContent{Thinking: ""}
		processor.output.Content = append(processor.output.Content, block)
		slot = &responsesOutputSlot{kind: "thinking", contentIndex: len(processor.output.Content) - 1, thinking: block}
		processor.slots[outputIndex] = slot
		if !processor.sink(ai.ThinkingStartEvent{ContentIndex: slot.contentIndex, Partial: processor.output}) {
			return nil, errStopSSE
		}
	case "message":
		block := &ai.TextContent{Text: ""}
		processor.output.Content = append(processor.output.Content, block)
		slot = &responsesOutputSlot{kind: "text", contentIndex: len(processor.output.Content) - 1, text: block}
		processor.slots[outputIndex] = slot
		if !processor.sink(ai.TextStartEvent{ContentIndex: slot.contentIndex, Partial: processor.output}) {
			return nil, errStopSSE
		}
	case "function_call":
		callID := "undefined"
		if item.CallID != nil {
			callID = *item.CallID
		}
		itemID := "undefined"
		if item.ID != nil {
			itemID = *item.ID
		}
		block := &ai.ToolCall{ID: callID + "|" + itemID, Name: item.Name, Arguments: map[string]any{}}
		processor.output.Content = append(processor.output.Content, block)
		slot = &responsesOutputSlot{
			kind:         "toolCall",
			contentIndex: len(processor.output.Content) - 1,
			toolCall:     block,
			partialJSON:  item.Arguments,
		}
		block.PartialJSON = &slot.partialJSON
		processor.slots[outputIndex] = slot
		if !processor.sink(ai.ToolCallStartEvent{ContentIndex: slot.contentIndex, Partial: processor.output}) {
			return nil, errStopSSE
		}
	}
	return slot, nil
}

func (processor *openAIResponsesProcessor) getOrCreateSlot(outputIndex int, raw json.RawMessage) (*responsesOutputSlot, error) {
	if slot := processor.slots[outputIndex]; slot != nil {
		return slot, nil
	}
	return processor.createSlot(outputIndex, raw)
}

func (processor *openAIResponsesProcessor) finishItem(outputIndex int, raw json.RawMessage) error {
	var item responsesOutputItemEnvelope
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	slot, err := processor.getOrCreateSlot(outputIndex, raw)
	if err != nil || slot == nil {
		return err
	}
	switch item.Type {
	case "reasoning":
		if slot.kind != "thinking" {
			return nil
		}
		summary := joinResponsesText(item.Summary)
		content := joinResponsesContentText(item.Content)
		if summary != "" {
			slot.thinking.Thinking = summary
		} else if content != "" {
			slot.thinking.Thinking = content
		}
		signature, err := compactResponsesJSON(raw)
		if err != nil {
			return err
		}
		slot.thinking.ThinkingSignature = &signature
		if item.ID != nil {
			processor.reasoningBlocksByID[*item.ID] = slot.thinking
		}
		if !processor.sink(ai.ThinkingEndEvent{ContentIndex: slot.contentIndex, Content: slot.thinking.Thinking, Partial: processor.output}) {
			return errStopSSE
		}
	case "message":
		if slot.kind != "text" {
			return nil
		}
		var builder strings.Builder
		for _, content := range item.Content {
			if content.Type == "output_text" {
				builder.WriteString(content.Text)
			} else {
				builder.WriteString(content.Refusal)
			}
		}
		slot.text.Text = builder.String()
		id := ""
		if item.ID != nil {
			id = *item.ID
		}
		signaturePayload := struct {
			Version int     `json:"v"`
			ID      string  `json:"id"`
			Phase   *string `json:"phase,omitempty"`
		}{Version: 1, ID: id, Phase: item.Phase}
		encoded, err := ai.Marshal(signaturePayload)
		if err != nil {
			return err
		}
		signature := string(encoded)
		slot.text.TextSignature = &signature
		if !processor.sink(ai.TextEndEvent{ContentIndex: slot.contentIndex, Content: slot.text.Text, Partial: processor.output}) {
			return errStopSSE
		}
	case "function_call":
		if slot.kind != "toolCall" {
			return nil
		}
		arguments := item.Arguments
		if arguments == "" {
			arguments = slot.partialJSON
		}
		if arguments == "" {
			arguments = "{}"
		}
		if err := ai.SetToolCallArgumentsJSON(slot.toolCall, []byte(arguments)); err != nil {
			slot.toolCall.Arguments = parseResponsesArguments(arguments)
		}
		slot.toolCall.PartialJSON = nil
		if !processor.sink(ai.ToolCallEndEvent{ContentIndex: slot.contentIndex, ToolCall: slot.toolCall, Partial: processor.output}) {
			return errStopSSE
		}
	}
	delete(processor.slots, outputIndex)
	return nil
}

func clearResponsesStreamingFields(output *ai.AssistantMessage) {
	for _, content := range output.Content {
		if call, ok := content.(*ai.ToolCall); ok {
			call.PartialJSON = nil
			call.PartialArgs = nil
			call.StreamIndex = nil
		}
	}
}

func joinResponsesText(items []responsesText) string {
	values := make([]string, len(items))
	for index, item := range items {
		values[index] = item.Text
	}
	return strings.Join(values, "\n\n")
}

func joinResponsesContentText(items []responsesContent) string {
	values := make([]string, len(items))
	for index, item := range items {
		values[index] = item.Text
	}
	return strings.Join(values, "\n\n")
}

func parseResponsesArguments(input string) map[string]any {
	parsed := partialjson.ParseStreamingJSON(input)
	if result, ok := parsed.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func compactResponsesJSON(raw json.RawMessage) (string, error) {
	normalized, err := ai.NormalizeJSONStringifyJSON(raw)
	if err != nil {
		return "", err
	}
	return string(normalized), nil
}

type responsesTerminalResponse struct {
	ID                string                      `json:"id"`
	Status            string                      `json:"status"`
	ServiceTier       *string                     `json:"service_tier"`
	Usage             *responsesUsage             `json:"usage"`
	Output            []json.RawMessage           `json:"output"`
	Error             *responsesTerminalError     `json:"error"`
	IncompleteDetails *responsesIncompleteDetails `json:"incomplete_details"`
}

type responsesUsage struct {
	InputTokens        int64                        `json:"input_tokens"`
	OutputTokens       int64                        `json:"output_tokens"`
	TotalTokens        int64                        `json:"total_tokens"`
	InputTokenDetails  *responsesInputTokenDetails  `json:"input_tokens_details"`
	OutputTokenDetails *responsesOutputTokenDetails `json:"output_tokens_details"`
}

type responsesInputTokenDetails struct {
	CachedTokens     int64 `json:"cached_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

type responsesOutputTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

type responsesTerminalError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type responsesIncompleteDetails struct {
	Reason string `json:"reason"`
}

func (processor *openAIResponsesProcessor) finalizeResponse(raw json.RawMessage) error {
	var response responsesTerminalResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	processor.backfillReasoningSignatures(response.Output)
	if response.ID != "" {
		processor.output.ResponseID = &response.ID
	}
	if response.Usage != nil {
		cacheRead := int64(0)
		cacheWrite := int64(0)
		if response.Usage.InputTokenDetails != nil {
			cacheRead = response.Usage.InputTokenDetails.CachedTokens
			cacheWrite = response.Usage.InputTokenDetails.CacheWriteTokens
		}
		reasoning := int64(0)
		if response.Usage.OutputTokenDetails != nil {
			reasoning = response.Usage.OutputTokenDetails.ReasoningTokens
		}
		processor.output.Usage = ai.Usage{
			Input:       max(0, response.Usage.InputTokens-cacheRead-cacheWrite),
			Output:      response.Usage.OutputTokens,
			CacheRead:   cacheRead,
			CacheWrite:  cacheWrite,
			Reasoning:   &reasoning,
			TotalTokens: response.Usage.TotalTokens,
			Cost:        ai.Cost{},
		}
	}
	calculateCost(processor.model, &processor.output.Usage)
	serviceTier := response.ServiceTier
	if serviceTier == nil && processor.options != nil {
		serviceTier = processor.options.ServiceTier
	}
	if serviceTier != nil {
		applyResponsesServiceTierPricing(&processor.output.Usage, *serviceTier, processor.model)
	}
	stopReason, err := mapResponsesStopReason(response.Status)
	if err != nil {
		return err
	}
	processor.output.StopReason = stopReason
	if stopReason == ai.StopReasonStop {
		for _, content := range processor.output.Content {
			if _, ok := content.(*ai.ToolCall); ok {
				processor.output.StopReason = ai.StopReasonToolUse
				break
			}
		}
	}
	return nil
}

func mapResponsesStopReason(status string) (ai.StopReason, error) {
	switch status {
	case "", "completed", "in_progress", "queued":
		return ai.StopReasonStop, nil
	case "incomplete":
		return ai.StopReasonLength, nil
	case "failed", "cancelled":
		return ai.StopReasonError, nil
	default:
		return "", fmt.Errorf("Unhandled stop reason: %s", status) //nolint:staticcheck // Exact upstream error text is observable.
	}
}

func applyResponsesServiceTierPricing(usage *ai.Usage, serviceTier string, model *ai.Model) {
	multiplier := 1.0
	switch serviceTier {
	case "flex":
		multiplier = 0.5
	case "priority":
		multiplier = 2
		if model.ID == "gpt-5.5" {
			multiplier = 2.5
		}
	}
	if multiplier == 1 {
		return
	}
	usage.Cost.Input *= multiplier
	usage.Cost.Output *= multiplier
	usage.Cost.CacheRead *= multiplier
	usage.Cost.CacheWrite *= multiplier
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
}

func responsesFailedError(raw json.RawMessage) error {
	var response responsesTerminalResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return err
	}
	if response.Error != nil {
		code := response.Error.Code
		if code == "" {
			code = "unknown"
		}
		message := response.Error.Message
		if message == "" {
			message = "no message"
		}
		return fmt.Errorf("%s: %s", code, message)
	}
	if response.IncompleteDetails != nil && response.IncompleteDetails.Reason != "" {
		return fmt.Errorf("incomplete: %s", response.IncompleteDetails.Reason)
	}
	return errors.New("Unknown error (no error details in response)") //nolint:staticcheck // Exact upstream error text is observable.
}

func (processor *openAIResponsesProcessor) backfillReasoningSignatures(items []json.RawMessage) {
	for _, raw := range items {
		var item struct {
			Type             string  `json:"type"`
			ID               string  `json:"id"`
			EncryptedContent *string `json:"encrypted_content"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Type != "reasoning" || item.EncryptedContent == nil || *item.EncryptedContent == "" {
			continue
		}
		block := processor.reasoningBlocksByID[item.ID]
		if block == nil || block.ThinkingSignature == nil || *block.ThinkingSignature == "" {
			continue
		}
		var stored map[string]json.RawMessage
		if json.Unmarshal([]byte(*block.ThinkingSignature), &stored) != nil {
			continue
		}
		if current, ok := stored["encrypted_content"]; ok {
			var value string
			if json.Unmarshal(current, &value) == nil && value != "" {
				continue
			}
		}
		encoded, err := ai.Marshal(*item.EncryptedContent)
		if err != nil {
			continue
		}
		updated, ok := setResponsesObjectField(*block.ThinkingSignature, "encrypted_content", encoded)
		if ok {
			block.ThinkingSignature = &updated
		}
	}
}

func setResponsesObjectField(object, name string, value []byte) (string, bool) {
	normalized, err := ai.NormalizeJSONStringifyJSON([]byte(object))
	if err != nil || len(normalized) < 2 || normalized[0] != '{' {
		return "", false
	}
	type member struct {
		name  string
		value json.RawMessage
	}
	decoder := json.NewDecoder(strings.NewReader(string(normalized)))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return "", false
	}
	members := make([]member, 0)
	indexes := make(map[string]int)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return "", false
		}
		memberName, ok := token.(string)
		if !ok {
			return "", false
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return "", false
		}
		if index, exists := indexes[memberName]; exists {
			members[index].value = raw
			continue
		}
		indexes[memberName] = len(members)
		members = append(members, member{name: memberName, value: raw})
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || !json.Valid(value) {
		return "", false
	}
	if index, exists := indexes[name]; exists {
		members[index].value = value
	} else {
		members = append(members, member{name: name, value: value})
	}
	var output strings.Builder
	output.WriteByte('{')
	for index, member := range members {
		if index > 0 {
			output.WriteByte(',')
		}
		encodedName, err := ai.Marshal(member.name)
		if err != nil {
			return "", false
		}
		output.Write(encodedName)
		output.WriteByte(':')
		output.Write(member.value)
	}
	output.WriteByte('}')
	return output.String(), true
}
