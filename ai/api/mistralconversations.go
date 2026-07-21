package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
	"github.com/OrdalieTech/pigo/internal/partialjson"
)

const (
	mistralToolCallIDLength = 9
	mistralRequestTimeout   = 60 * time.Second
)

type mistralHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var mistralHTTPClient mistralHTTPDoer = http.DefaultClient

type MistralConversationsOptions struct {
	ai.StreamOptions
	ToolChoice      any     `json:"toolChoice,omitempty"`
	PromptMode      *string `json:"promptMode,omitempty"`
	ReasoningEffort *string `json:"reasoningEffort,omitempty"`
}

// MistralConversationsPayload is the mutable request value passed to PayloadHook.
type MistralConversationsPayload struct {
	Model           string                    `json:"model"`
	Temperature     *float64                  `json:"temperature,omitempty"`
	MaxTokens       *float64                  `json:"max_tokens,omitempty"`
	Stream          bool                      `json:"stream"`
	Messages        []any                     `json:"messages"`
	Tools           []MistralConversationTool `json:"tools,omitempty"`
	ToolChoice      any                       `json:"tool_choice,omitempty"`
	ReasoningEffort *string                   `json:"reasoning_effort,omitempty"`
	PromptMode      *string                   `json:"prompt_mode,omitempty"`
	PromptCacheKey  *string                   `json:"prompt_cache_key,omitempty"`
}

type MistralChatPayload = MistralConversationsPayload

type MistralConversationTool struct {
	Type     string                      `json:"type"`
	Function MistralConversationFunction `json:"function"`
}

type MistralConversationFunction struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Strict      bool              `json:"strict"`
	Parameters  jsonschema.Schema `json:"parameters"`
}

type mistralNamedToolChoice struct {
	Type     string                     `json:"type"`
	Function mistralNamedToolChoiceName `json:"function"`
}

type mistralNamedToolChoiceName struct {
	Name string `json:"name"`
}

type mistralInputMessage struct {
	Role       string            `json:"role"`
	Content    any               `json:"content,omitempty"`
	ToolCalls  []mistralToolCall `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Name       string            `json:"name,omitempty"`
	Prefix     *bool             `json:"prefix,omitempty"`
}

type mistralContentChunk struct {
	Type     string                `json:"type"`
	Text     *string               `json:"text,omitempty"`
	ImageURL *string               `json:"image_url,omitempty"`
	Thinking []mistralThinkingPart `json:"thinking,omitempty"`
}

type mistralThinkingPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mistralToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function mistralToolFunction `json:"function"`
	Index    int                 `json:"index"`
}

type mistralToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func StreamMistralConversations(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: Mistral Conversations model is nil")
	}
	options := &MistralConversationsOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamMistralConversationsWithOptions(ctx, request.Model, request.Context, options)
}

func StreamSimpleMistralConversations(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Mistral Conversations model is nil")
	}
	base := buildBaseStreamOptions(model, requestContext, options)
	if err := assertMistralAPIKey(model, &base); err != nil {
		return nil, err
	}
	result := &MistralConversationsOptions{StreamOptions: base}
	var requested *ai.ThinkingLevel
	if options != nil {
		requested = options.Reasoning
	}
	level := clampSimpleReasoning(model, requested)
	if !model.Reasoning || level == nil {
		return StreamMistralConversationsWithOptions(ctx, model, requestContext, result)
	}
	if usesMistralReasoningEffort(model) {
		value := mappedThinkingLevel(model, string(*level), "high")
		result.ReasoningEffort = &value
	} else {
		value := "reasoning"
		result.PromptMode = &value
	}
	return StreamMistralConversationsWithOptions(ctx, model, requestContext, result)
}

func StreamMistralConversationsWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *MistralConversationsOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Mistral Conversations model is nil")
	}
	output := newAssistantMessage(model)
	streamOptions := mistralStreamOptions(options)

	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		httpContext := ctx
		cancel := func() {}
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			httpContext, cancel = context.WithTimeout(ctx, mistralRequestTimeout)
		}
		defer cancel()
		sink := func(event ai.AssistantMessageEvent) bool { return yield(event, nil) }
		fail := func(err error) {
			clearMistralStreamingFields(output)
			reason := ai.StopReasonError
			if ctx.Err() != nil {
				reason = ai.StopReasonAborted
			}
			output.StopReason = reason
			message := formatMistralError(err)
			output.ErrorMessage = &message
			sink(ai.ErrorEvent{Reason: reason, Error: output})
		}

		apiKey, err := mistralAPIKey(model, streamOptions)
		if err != nil {
			fail(err)
			return
		}
		payload, err := buildMistralPayload(model, requestContext, options)
		if err != nil {
			fail(err)
			return
		}
		hookedPayload, err := applyPayloadHook(ctx, model, streamOptions, payload)
		if err != nil {
			fail(err)
			return
		}
		response, err := postMistralStream(httpContext, model, streamOptions, apiKey, hookedPayload)
		if err != nil {
			fail(err)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if !sink(ai.StartEvent{Partial: output}) {
			return
		}

		processor := newMistralStreamProcessor(model, output, sink)
		err = readSSE(response.Body, processor.handle)
		if errors.Is(err, errStopSSE) {
			return
		}
		if err == nil {
			err = processor.finish()
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
		clearMistralStreamingFields(output)
		sink(ai.DoneEvent{Reason: output.StopReason, Message: output})
	}, nil
}

func mistralStreamOptions(options *MistralConversationsOptions) *ai.StreamOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions
}

func assertMistralAPIKey(model *ai.Model, options *ai.StreamOptions) error {
	_, err := mistralAPIKey(model, options)
	return err
}

func mistralAPIKey(model *ai.Model, options *ai.StreamOptions) (string, error) {
	if options != nil && options.APIKey != nil && *options.APIKey != "" {
		return *options.APIKey, nil
	}
	return "", fmt.Errorf("No API key for provider: %s", model.Provider) //nolint:staticcheck // Exact upstream error text is observable.
}

func buildMistralPayload(
	model *ai.Model,
	requestContext ai.Context,
	options *MistralConversationsOptions,
) (*MistralConversationsPayload, error) {
	normalize := newMistralToolCallIDNormalizer()
	messages := transformMessages(requestContext.Messages, model, func(id string, _ *ai.Model, _ *ai.AssistantMessage) string {
		return normalize(id)
	})
	converted, err := toMistralMessages(messages, modelSupportsImage(model))
	if err != nil {
		return nil, err
	}
	if requestContext.SystemPrompt != nil && *requestContext.SystemPrompt != "" {
		converted = append([]any{mistralInputMessage{Role: "system", Content: sanitizeText(*requestContext.SystemPrompt)}}, converted...)
	}
	payload := &MistralConversationsPayload{Model: model.ID, Stream: true, Messages: converted}
	if requestContext.Tools != nil && len(*requestContext.Tools) > 0 {
		payload.Tools = make([]MistralConversationTool, 0, len(*requestContext.Tools))
		for _, tool := range *requestContext.Tools {
			payload.Tools = append(payload.Tools, MistralConversationTool{
				Type: "function",
				Function: MistralConversationFunction{
					Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters, Strict: false,
				},
			})
		}
	}
	if options != nil {
		payload.Temperature = options.Temperature
		payload.MaxTokens = options.MaxTokens
		payload.ToolChoice = normalizeMistralToolChoice(options.ToolChoice)
		payload.PromptMode = options.PromptMode
		payload.ReasoningEffort = options.ReasoningEffort
		if shouldUseMistralPromptCaching(&options.StreamOptions) {
			payload.PromptCacheKey = options.SessionID
		}
	}
	return payload, nil
}

func normalizeMistralToolChoice(choice any) any {
	object, ok := choice.(map[string]any)
	if !ok {
		return choice
	}
	function, ok := object["function"].(map[string]any)
	if !ok {
		return choice
	}
	name, ok := function["name"].(string)
	if !ok {
		return choice
	}
	return mistralNamedToolChoice{Type: "function", Function: mistralNamedToolChoiceName{Name: name}}
}

func shouldUseMistralPromptCaching(options *ai.StreamOptions) bool {
	return options != nil && options.SessionID != nil && *options.SessionID != "" &&
		(options.CacheRetention == nil || *options.CacheRetention != ai.CacheRetentionNone)
}

func toMistralMessages(messages ai.MessageList, supportsImages bool) ([]any, error) {
	result := make([]any, 0, len(messages))
	for _, message := range messages {
		switch value := message.(type) {
		case *ai.UserMessage:
			if value.Content.Text != nil {
				result = append(result, mistralInputMessage{Role: "user", Content: sanitizeText(*value.Content.Text)})
				continue
			}
			hadImages := false
			content := make([]mistralContentChunk, 0, len(value.Content.Blocks))
			for _, item := range value.Content.Blocks {
				switch block := item.(type) {
				case *ai.TextContent:
					text := sanitizeText(block.Text)
					content = append(content, mistralContentChunk{Type: "text", Text: &text})
				case *ai.ImageContent:
					hadImages = true
					if supportsImages {
						imageURL := "data:" + block.MimeType + ";base64," + block.Data
						content = append(content, mistralContentChunk{Type: "image_url", ImageURL: &imageURL})
					}
				}
			}
			if len(content) > 0 {
				result = append(result, mistralInputMessage{Role: "user", Content: content})
			} else if hadImages && !supportsImages {
				result = append(result, mistralInputMessage{Role: "user", Content: "(image omitted: model does not support images)"})
			}
		case *ai.AssistantMessage:
			content := make([]mistralContentChunk, 0, len(value.Content))
			calls := make([]mistralToolCall, 0)
			for _, item := range value.Content {
				switch block := item.(type) {
				case *ai.TextContent:
					if strings.TrimSpace(block.Text) != "" {
						text := sanitizeText(block.Text)
						content = append(content, mistralContentChunk{Type: "text", Text: &text})
					}
				case *ai.ThinkingContent:
					if strings.TrimSpace(block.Thinking) != "" {
						content = append(content, mistralContentChunk{Type: "thinking", Thinking: []mistralThinkingPart{{Type: "text", Text: sanitizeText(block.Thinking)}}})
					}
				case *ai.ToolCall:
					arguments, err := ai.MarshalToolCallArguments(block)
					if err != nil {
						return nil, fmt.Errorf("marshal Mistral tool arguments: %w", err)
					}
					calls = append(calls, mistralToolCall{ID: block.ID, Type: "function", Function: mistralToolFunction{Name: block.Name, Arguments: string(arguments)}, Index: 0})
				}
			}
			if len(content) > 0 || len(calls) > 0 {
				prefix := false
				var messageContent any
				if len(content) > 0 {
					messageContent = content
				}
				result = append(result, mistralInputMessage{Role: "assistant", Content: messageContent, ToolCalls: calls, Prefix: &prefix})
			}
		case *ai.ToolResultMessage:
			textParts := make([]string, 0)
			hasImages := false
			images := make([]mistralContentChunk, 0)
			for _, item := range value.Content {
				switch block := item.(type) {
				case *ai.TextContent:
					textParts = append(textParts, sanitizeText(block.Text))
				case *ai.ImageContent:
					hasImages = true
					if supportsImages {
						imageURL := "data:" + block.MimeType + ";base64," + block.Data
						images = append(images, mistralContentChunk{Type: "image_url", ImageURL: &imageURL})
					}
				}
			}
			text := buildMistralToolResultText(strings.Join(textParts, "\n"), hasImages, supportsImages, value.IsError)
			content := append([]mistralContentChunk{{Type: "text", Text: &text}}, images...)
			result = append(result, mistralInputMessage{Role: "tool", ToolCallID: value.ToolCallID, Name: value.ToolName, Content: content})
		}
	}
	return result, nil
}

func buildMistralToolResultText(text string, hasImages, supportsImages, isError bool) string {
	trimmed := strings.TrimSpace(text)
	prefix := ""
	if isError {
		prefix = "[tool error] "
	}
	if trimmed != "" {
		suffix := ""
		if hasImages && !supportsImages {
			suffix = "\n[tool image omitted: model does not support images]"
		}
		return prefix + trimmed + suffix
	}
	if hasImages {
		if supportsImages {
			return prefix + "(see attached image)"
		}
		return prefix + "(image omitted: model does not support images)"
	}
	return prefix + "(no tool output)"
}

func newMistralToolCallIDNormalizer() func(string) string {
	forward := make(map[string]string)
	reverse := make(map[string]string)
	return func(id string) string {
		if value, ok := forward[id]; ok {
			return value
		}
		for attempt := 0; ; attempt++ {
			candidate := deriveMistralToolCallID(id, attempt)
			owner, used := reverse[candidate]
			if !used || owner == id {
				forward[id] = candidate
				reverse[candidate] = id
				return candidate
			}
		}
	}
}

func createMistralToolCallIDNormalizer() func(string) string {
	return newMistralToolCallIDNormalizer()
}

func deriveMistralToolCallID(id string, attempt int) string {
	normalized := mistralAlphanumeric(id)
	if attempt == 0 && len(normalized) == mistralToolCallIDLength {
		return normalized
	}
	seedBase := normalized
	if seedBase == "" {
		seedBase = id
	}
	seed := seedBase
	if attempt != 0 {
		seed = fmt.Sprintf("%s:%d", seedBase, attempt)
	}
	candidate := mistralAlphanumeric(shortHash(seed))
	if len(candidate) > mistralToolCallIDLength {
		candidate = candidate[:mistralToolCallIDLength]
	}
	return candidate
}

func mistralAlphanumeric(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func usesMistralReasoningEffort(model *ai.Model) bool {
	switch model.ID {
	case "mistral-small-2603", "mistral-small-latest", "mistral-medium-3.5":
		return true
	default:
		return false
	}
}

func postMistralStream(
	ctx context.Context,
	model *ai.Model,
	options *ai.StreamOptions,
	apiKey string,
	payload any,
) (*http.Response, error) {
	body, err := ai.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Mistral request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(model.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header = make(http.Header)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	for name, values := range copyModelHeaders(model) {
		request.Header[name] = append([]string(nil), values...)
	}
	if options != nil {
		mergeProviderHeaders(request.Header, options.Headers)
	}
	if shouldUseMistralPromptCaching(options) && request.Header.Get("x-affinity") == "" {
		request.Header.Set("x-affinity", *options.SessionID)
	}
	request.Header, err = applyHeadersHook(ctx, model, options, request.Header)
	if err != nil {
		return nil, err
	}
	response, err := mistralHTTPClient.Do(request)
	if err != nil {
		return response, err
	}
	if response == nil {
		return nil, errors.New("ai/api: Mistral API returned no HTTP response")
	}
	if response.StatusCode >= http.StatusBadRequest {
		contents, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			return response, readErr
		}
		return response, &mistralStatusError{
			status: response.StatusCode, body: strings.TrimSpace(string(contents)), contentType: response.Header.Get("Content-Type"),
		}
	}
	return response, nil
}

type mistralStatusError struct {
	status      int
	body        string
	contentType string
}

func (err *mistralStatusError) Error() string {
	if err.body != "" {
		return fmt.Sprintf("Mistral API error (%d): %s", err.status, truncateOpenAIErrorText(err.body))
	}
	contentType := err.contentType
	if contentType == "" {
		contentType = `""`
	} else if strings.Contains(contentType, " ") {
		contentType = strconv.Quote(contentType)
	}
	contentTypeMessage := ""
	if contentType != "application/json" {
		contentTypeMessage = " Content-Type " + contentType
	}
	return fmt.Sprintf(`Mistral API error (%d): API error occurred: Status %d%s. Body: ""`, err.status, err.status, contentTypeMessage)
}

func formatMistralError(err error) string {
	if err == nil {
		return "null"
	}
	return err.Error()
}

type mistralCompletionChunk struct {
	ID      string                     `json:"id"`
	Choices []mistralStreamChoice      `json:"choices"`
	Usage   map[string]json.RawMessage `json:"usage"`
}

type mistralStreamChoice struct {
	FinishReason *string            `json:"finish_reason"`
	Delta        mistralStreamDelta `json:"delta"`
}

type mistralStreamDelta struct {
	Content   json.RawMessage         `json:"content"`
	ToolCalls []mistralStreamToolCall `json:"tool_calls"`
}

type mistralStreamToolCall struct {
	ID       *string                   `json:"id"`
	Index    *int                      `json:"index"`
	Function mistralStreamToolFunction `json:"function"`
}

type mistralStreamToolFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mistralStreamProcessor struct {
	model        *ai.Model
	output       *ai.AssistantMessage
	sink         eventSink
	currentKind  string
	currentIndex int
	toolIndices  map[string]int
	toolOrder    []int
}

func newMistralStreamProcessor(model *ai.Model, output *ai.AssistantMessage, sink eventSink) *mistralStreamProcessor {
	return &mistralStreamProcessor{model: model, output: output, sink: sink, currentIndex: -1, toolIndices: make(map[string]int)}
}

func (processor *mistralStreamProcessor) handle(raw json.RawMessage) error {
	queued := make([]ai.AssistantMessageEvent, 0, 4)
	sink := processor.sink
	processor.sink = func(event ai.AssistantMessageEvent) bool {
		queued = append(queued, event)
		return true
	}
	err := processor.handleChunk(raw)
	processor.sink = sink
	if err != nil {
		return err
	}
	// Upstream queues every event produced from a completion chunk before the
	// consumer resumes, so all partial pointers observe that chunk's final state.
	for _, event := range queued {
		if !sink(event) {
			return errStopSSE
		}
	}
	return nil
}

func (processor *mistralStreamProcessor) handleChunk(raw json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("[DONE]")) {
		return nil
	}
	var chunk mistralCompletionChunk
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return err
	}
	if processor.output.ResponseID == nil && chunk.ID != "" {
		processor.output.ResponseID = &chunk.ID
	}
	if chunk.Usage != nil {
		processor.applyUsage(chunk.Usage)
	}
	if len(chunk.Choices) == 0 {
		return nil
	}
	choice := chunk.Choices[0]
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		processor.output.StopReason = mapMistralStopReason(choice.FinishReason)
	}
	if len(choice.Delta.Content) > 0 && string(choice.Delta.Content) != "null" {
		if err := processor.consumeContent(choice.Delta.Content); err != nil {
			return err
		}
	}
	for _, call := range choice.Delta.ToolCalls {
		if err := processor.consumeToolCall(call); err != nil {
			return err
		}
	}
	return nil
}

func (processor *mistralStreamProcessor) consumeContent(raw json.RawMessage) error {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return processor.appendText(text)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	for _, rawItem := range items {
		var stringItem string
		if json.Unmarshal(rawItem, &stringItem) == nil {
			if err := processor.appendText(stringItem); err != nil {
				return err
			}
			continue
		}
		var item struct {
			Type     string                `json:"type"`
			Text     string                `json:"text"`
			Thinking []mistralThinkingPart `json:"thinking"`
		}
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return err
		}
		switch item.Type {
		case "text":
			if err := processor.appendText(item.Text); err != nil {
				return err
			}
		case "thinking":
			var builder strings.Builder
			for _, part := range item.Thinking {
				if part.Text != "" {
					builder.WriteString(part.Text)
				}
			}
			if builder.Len() > 0 {
				if err := processor.appendThinking(builder.String()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (processor *mistralStreamProcessor) appendText(delta string) error {
	if processor.currentKind != "text" {
		if err := processor.finishCurrent(); err != nil {
			return err
		}
		block := &ai.TextContent{}
		processor.output.Content = append(processor.output.Content, block)
		processor.currentKind = "text"
		processor.currentIndex = len(processor.output.Content) - 1
		if !processor.sink(ai.TextStartEvent{ContentIndex: processor.currentIndex, Partial: processor.output}) {
			return errStopSSE
		}
	}
	block := processor.output.Content[processor.currentIndex].(*ai.TextContent)
	delta = sanitizeText(delta)
	block.Text += delta
	if !processor.sink(ai.TextDeltaEvent{ContentIndex: processor.currentIndex, Delta: delta, Partial: processor.output}) {
		return errStopSSE
	}
	return nil
}

func (processor *mistralStreamProcessor) appendThinking(delta string) error {
	if processor.currentKind != "thinking" {
		if err := processor.finishCurrent(); err != nil {
			return err
		}
		block := &ai.ThinkingContent{}
		processor.output.Content = append(processor.output.Content, block)
		processor.currentKind = "thinking"
		processor.currentIndex = len(processor.output.Content) - 1
		if !processor.sink(ai.ThinkingStartEvent{ContentIndex: processor.currentIndex, Partial: processor.output}) {
			return errStopSSE
		}
	}
	block := processor.output.Content[processor.currentIndex].(*ai.ThinkingContent)
	delta = sanitizeText(delta)
	block.Thinking += delta
	if !processor.sink(ai.ThinkingDeltaEvent{ContentIndex: processor.currentIndex, Delta: delta, Partial: processor.output}) {
		return errStopSSE
	}
	return nil
}

func (processor *mistralStreamProcessor) finishCurrent() error {
	if processor.currentIndex < 0 {
		return nil
	}
	index := processor.currentIndex
	processor.currentIndex = -1
	kind := processor.currentKind
	processor.currentKind = ""
	switch block := processor.output.Content[index].(type) {
	case *ai.TextContent:
		if kind == "text" && !processor.sink(ai.TextEndEvent{ContentIndex: index, Content: block.Text, Partial: processor.output}) {
			return errStopSSE
		}
	case *ai.ThinkingContent:
		if kind == "thinking" && !processor.sink(ai.ThinkingEndEvent{ContentIndex: index, Content: block.Thinking, Partial: processor.output}) {
			return errStopSSE
		}
	}
	return nil
}

func (processor *mistralStreamProcessor) consumeToolCall(raw mistralStreamToolCall) error {
	if err := processor.finishCurrent(); err != nil {
		return err
	}
	index := 0
	if raw.Index != nil {
		index = *raw.Index
	}
	callID := ""
	if raw.ID != nil {
		callID = *raw.ID
	}
	if callID == "" || callID == "null" {
		callID = deriveMistralToolCallID(fmt.Sprintf("toolcall:%d", index), 0)
	}
	key := fmt.Sprintf("%s:%d", callID, index)
	contentIndex, ok := processor.toolIndices[key]
	if !ok {
		partial := ""
		block := &ai.ToolCall{ID: callID, Name: raw.Function.Name, Arguments: map[string]any{}, PartialArgs: &partial}
		processor.output.Content = append(processor.output.Content, block)
		contentIndex = len(processor.output.Content) - 1
		processor.toolIndices[key] = contentIndex
		processor.toolOrder = append(processor.toolOrder, contentIndex)
		if !processor.sink(ai.ToolCallStartEvent{ContentIndex: contentIndex, Partial: processor.output}) {
			return errStopSSE
		}
	}
	block, ok := processor.output.Content[contentIndex].(*ai.ToolCall)
	if !ok {
		return nil
	}
	argsDelta := mistralArgumentsText(raw.Function.Arguments)
	if block.PartialArgs == nil {
		value := ""
		block.PartialArgs = &value
	}
	*block.PartialArgs += argsDelta
	block.Arguments = parseMistralArguments(*block.PartialArgs)
	if !processor.sink(ai.ToolCallDeltaEvent{ContentIndex: contentIndex, Delta: argsDelta, Partial: processor.output}) {
		return errStopSSE
	}
	return nil
}

func mistralArgumentsText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	normalized, err := ai.NormalizeJSONStringifyJSON(raw)
	if err != nil {
		return string(raw)
	}
	return string(normalized)
}

func parseMistralArguments(value string) map[string]any {
	parsed := partialjson.ParseStreamingJSON(value)
	if object, ok := parsed.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

func (processor *mistralStreamProcessor) finish() error {
	if err := processor.finishCurrent(); err != nil {
		return err
	}
	for _, index := range processor.toolOrder {
		block, ok := processor.output.Content[index].(*ai.ToolCall)
		if !ok {
			continue
		}
		if block.PartialArgs != nil {
			arguments, err := partialjson.StringifyStreamingJSON(*block.PartialArgs)
			if err != nil {
				return err
			}
			if err := ai.SetToolCallArgumentsJSON(block, arguments); err != nil {
				return err
			}
		}
		block.PartialArgs = nil
		if !processor.sink(ai.ToolCallEndEvent{ContentIndex: index, ToolCall: block, Partial: processor.output}) {
			return errStopSSE
		}
	}
	return nil
}

func (processor *mistralStreamProcessor) applyUsage(raw map[string]json.RawMessage) {
	prompt := mistralUsageNumber(raw, "prompt_tokens", "promptTokens")
	completion := mistralUsageNumber(raw, "completion_tokens", "completionTokens")
	total := mistralUsageNumber(raw, "total_tokens", "totalTokens")
	cached := mistralCachedPromptTokens(raw, prompt)
	processor.output.Usage = ai.Usage{
		Input: max(0, prompt-cached), Output: completion, CacheRead: cached, CacheWrite: 0,
		TotalTokens: total, Cost: ai.Cost{},
	}
	if processor.output.Usage.TotalTokens == 0 {
		processor.output.Usage.TotalTokens = processor.output.Usage.Input + processor.output.Usage.Output + processor.output.Usage.CacheRead
	}
	calculateCost(processor.model, &processor.output.Usage)
}

func parseMistralUsage(raw json.RawMessage, model *ai.Model) ai.Usage {
	var values map[string]json.RawMessage
	_ = json.Unmarshal(raw, &values)
	output := &ai.AssistantMessage{}
	processor := &mistralStreamProcessor{model: model, output: output}
	processor.applyUsage(values)
	return output.Usage
}

func mistralUsageNumber(raw map[string]json.RawMessage, keys ...string) int64 {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		var number float64
		if json.Unmarshal(value, &number) == nil && number >= 0 {
			return int64(number)
		}
	}
	return 0
}

func mistralCachedPromptTokens(raw map[string]json.RawMessage, prompt int64) int64 {
	for _, detailKey := range []string{"prompt_tokens_details", "promptTokensDetails", "prompt_token_details", "promptTokenDetails"} {
		var details map[string]json.RawMessage
		if value, ok := raw[detailKey]; ok && json.Unmarshal(value, &details) == nil {
			cached := mistralUsageNumber(details, "cached_tokens", "cachedTokens")
			return min(prompt, max(0, cached))
		}
	}
	return min(prompt, max(0, mistralUsageNumber(raw, "num_cached_tokens", "numCachedTokens")))
}

func mapMistralStopReason(reason *string) ai.StopReason {
	if reason == nil {
		return ai.StopReasonStop
	}
	switch *reason {
	case "length", "model_length":
		return ai.StopReasonLength
	case "tool_calls":
		return ai.StopReasonToolUse
	case "error":
		return ai.StopReasonError
	default:
		return ai.StopReasonStop
	}
}

func clearMistralStreamingFields(output *ai.AssistantMessage) {
	for _, item := range output.Content {
		if block, ok := item.(*ai.ToolCall); ok {
			block.PartialArgs = nil
			block.PartialJSON = nil
			block.StreamIndex = nil
		}
	}
}
