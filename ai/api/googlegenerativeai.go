package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/OrdalieTech/pi-go/ai"
)

type GoogleToolChoice string

const (
	GoogleToolChoiceAuto GoogleToolChoice = "auto"
	GoogleToolChoiceNone GoogleToolChoice = "none"
	GoogleToolChoiceAny  GoogleToolChoice = "any"
)

type GoogleThinkingLevel string

const (
	GoogleThinkingUnspecified GoogleThinkingLevel = "THINKING_LEVEL_UNSPECIFIED"
	GoogleThinkingMinimal     GoogleThinkingLevel = "MINIMAL"
	GoogleThinkingLow         GoogleThinkingLevel = "LOW"
	GoogleThinkingMedium      GoogleThinkingLevel = "MEDIUM"
	GoogleThinkingHigh        GoogleThinkingLevel = "HIGH"
)

type GoogleThinkingOptions struct {
	Enabled      bool                 `json:"enabled"`
	BudgetTokens *int64               `json:"budgetTokens,omitempty"`
	Level        *GoogleThinkingLevel `json:"level,omitempty"`
}

type GoogleOptions struct {
	ai.StreamOptions
	ToolChoice GoogleToolChoice       `json:"toolChoice,omitempty"`
	Thinking   *GoogleThinkingOptions `json:"thinking,omitempty"`
}

var (
	googleHTTPClient      = http.DefaultClient
	googleToolCallCounter atomic.Int64
	gemma4Pattern         = regexp.MustCompile(`gemma-?4`)
	gemini3ProPattern     = regexp.MustCompile(`gemini-3(?:\.\d+)?-pro`)
	gemini3FlashPattern   = regexp.MustCompile(`gemini-3(?:\.\d+)?-flash`)
)

type googleGenerateContentResponse struct {
	ResponseID    string               `json:"responseId"`
	Candidates    []googleCandidate    `json:"candidates"`
	UsageMetadata *googleUsageMetadata `json:"usageMetadata"`
}

type googleCandidate struct {
	Content      *GoogleContent `json:"content"`
	FinishReason string         `json:"finishReason"`
}

type googleUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
}

func StreamGoogleGenerativeAI(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: Google Generative AI model is nil")
	}
	options := &GoogleOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamGoogleGenerativeAIWithOptions(ctx, request.Model, request.Context, options)
}

func StreamSimpleGoogleGenerativeAI(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Google Generative AI model is nil")
	}
	base := buildBaseStreamOptions(model, requestContext, options)
	if err := assertGoogleAuth(model, &base); err != nil {
		return nil, err
	}
	if options == nil || options.Reasoning == nil {
		return StreamGoogleGenerativeAIWithOptions(ctx, model, requestContext, &GoogleOptions{
			StreamOptions: base, Thinking: &GoogleThinkingOptions{Enabled: false},
		})
	}
	effort := clampGoogleReasoning(model, *options.Reasoning)
	if effort == ai.ThinkingLevel(ai.ModelThinkingOff) {
		effort = ai.ThinkingHigh
	}
	thinking := &GoogleThinkingOptions{Enabled: true}
	if isGemini3Pro(model) || isGemini3Flash(model) || isGemma4(model) {
		level := googleThinkingLevel(effort, model)
		thinking.Level = &level
	} else {
		budget := googleThinkingBudget(model, effort, options.ThinkingBudgets)
		thinking.BudgetTokens = &budget
	}
	return StreamGoogleGenerativeAIWithOptions(ctx, model, requestContext, &GoogleOptions{
		StreamOptions: base, Thinking: thinking,
	})
}

func StreamGoogleGenerativeAIWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *GoogleOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Google Generative AI model is nil")
	}
	return streamGoogleWithOptions(ctx, model, requestContext, options, func() error {
		return assertGoogleAuth(model, googleStreamOptions(options))
	}, postGoogleStream)
}

type googleStreamPoster func(
	context.Context,
	*ai.Model,
	*ai.StreamOptions,
	googleDecodedParameters,
) (*http.Response, error)

func streamGoogleWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *GoogleOptions,
	checkAuth func() error,
	post googleStreamPoster,
) (ai.AssistantMessageEventStream, error) {
	output := newAssistantMessage(model)
	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		fail := func(err error) {
			reason := ai.StopReasonError
			if ctx.Err() != nil {
				reason = ai.StopReasonAborted
			}
			output.StopReason = reason
			message := err.Error()
			output.ErrorMessage = &message
			yield(ai.ErrorEvent{Reason: reason, Error: output}, nil)
		}
		streamOptions := googleStreamOptions(options)
		if checkAuth != nil {
			if err := checkAuth(); err != nil {
				fail(err)
				return
			}
		}
		if ctx.Err() != nil {
			fail(errors.New("Request aborted")) //nolint:staticcheck // Exact upstream text.
			return
		}
		built, err := buildGoogleParameters(model, requestContext, options)
		if err != nil {
			fail(err)
			return
		}
		hooked, err := applyPayloadHook(ctx, model, streamOptions, built)
		if err != nil {
			fail(err)
			return
		}
		parameters, err := decodeGoogleParameters(hooked)
		if err != nil {
			fail(err)
			return
		}
		if err := validateGoogleParameters(parameters); err != nil {
			fail(err)
			return
		}
		response, err := post(ctx, model, streamOptions, parameters)
		if err != nil {
			fail(err)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if !yield(ai.StartEvent{Partial: output}, nil) {
			return
		}
		processor := googleStreamProcessor{model: model, output: output}
		err = readGoogleSSE(response.Body, func(raw json.RawMessage) error {
			var chunk googleGenerateContentResponse
			if err := json.Unmarshal(raw, &chunk); err != nil {
				return err
			}
			for _, event := range processor.process(chunk) {
				if !yield(event, nil) {
					return errStopSSE
				}
			}
			return nil
		})
		if errors.Is(err, errStopSSE) {
			return
		}
		if err != nil {
			fail(err)
			return
		}
		if event := processor.finish(); event != nil && !yield(event, nil) {
			return
		}
		if ctx.Err() != nil {
			fail(errors.New("Request was aborted")) //nolint:staticcheck // Exact upstream text.
			return
		}
		if output.StopReason == ai.StopReasonAborted || output.StopReason == ai.StopReasonError {
			fail(errors.New("An unknown error occurred")) //nolint:staticcheck // Exact upstream text.
			return
		}
		yield(ai.DoneEvent{Reason: output.StopReason, Message: output}, nil)
	}, nil
}

func googleStreamOptions(options *GoogleOptions) *ai.StreamOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions
}

func assertGoogleAuth(model *ai.Model, options *ai.StreamOptions) error {
	if options == nil || options.APIKey == nil || *options.APIKey == "" {
		return fmt.Errorf("No API key for provider: %s", model.Provider) //nolint:staticcheck // Exact upstream text.
	}
	return nil
}

func decodeGoogleParameters(value any) (googleDecodedParameters, error) {
	if typed, ok := value.(*GoogleGenerateContentParameters); ok && typed == nil {
		return googleDecodedParameters{}, errors.New("Google payload hook returned nil parameters") //nolint:staticcheck // Public hook diagnostic.
	}
	encoded, err := ai.Marshal(value)
	if err != nil {
		return googleDecodedParameters{}, err
	}
	var parameters googleDecodedParameters
	if err := json.Unmarshal(encoded, &parameters); err != nil {
		return googleDecodedParameters{}, err
	}
	return parameters, nil
}

func postGoogleStream(
	ctx context.Context,
	model *ai.Model,
	options *ai.StreamOptions,
	parameters googleDecodedParameters,
) (*http.Response, error) {
	wire, err := googleWirePayload(parameters)
	if err != nil {
		return nil, err
	}
	payload, err := ai.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode Google request: %w", err)
	}
	base := strings.TrimRight(model.BaseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	endpoint := base + "/" + googleModelPath(parameters.Model) + ":streamGenerateContent?alt=sse"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	headers := googleProviderHeaders(model, options)
	if !googleHeaderPresent(headers, "X-Goog-Api-Key") {
		headers.Set("X-Goog-Api-Key", *options.APIKey)
	}
	headers, err = applyHeadersHook(ctx, model, options, headers)
	if err != nil {
		return nil, err
	}
	request.Header = headers
	return doGoogleRequest(request)
}

func doGoogleRequest(request *http.Request) (*http.Response, error) {
	response, err := googleHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("google API returned no HTTP response")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		return nil, googleHTTPError(response, body)
	}
	if response.Body == nil {
		return nil, errors.New("google response has no body")
	}
	return response, nil
}

func googleHTTPError(response *http.Response, body []byte) error {
	if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		encoded, err := ai.NormalizeJSONStringifyJSON(body)
		if err != nil {
			return err
		}
		return errors.New(string(encoded))
	}
	statusText := http.StatusText(response.StatusCode)
	if prefix := fmt.Sprintf("%d ", response.StatusCode); strings.HasPrefix(response.Status, prefix) {
		statusText = strings.TrimPrefix(response.Status, prefix)
	}
	encoded, err := ai.Marshal(struct {
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
			Status  string `json:"status"`
		} `json:"error"`
	}{Error: struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
		Status  string `json:"status"`
	}{Message: string(body), Code: response.StatusCode, Status: statusText}})
	if err != nil {
		return err
	}
	return errors.New(string(encoded))
}

func googleModelPath(model string) string {
	if strings.HasPrefix(model, "models/") || strings.HasPrefix(model, "tunedModels/") {
		return model
	}
	return "models/" + model
}

func googleHeaderPresent(headers http.Header, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func googleProviderHeaders(model *ai.Model, options *ai.StreamOptions) http.Header {
	custom := make(ai.ProviderHeaders)
	if model.Headers != nil {
		for name, value := range *model.Headers {
			copy := value
			custom[name] = &copy
		}
	}
	if options != nil {
		for name, value := range options.Headers {
			custom[name] = value
		}
	}
	headers := http.Header{"Content-Type": []string{"application/json"}}
	for name, value := range custom {
		if value != nil {
			headers[name] = []string{*value}
		}
	}
	return headers
}

var googleSSEDelimiters = [][]byte{[]byte("\n\n"), []byte("\r\r"), []byte("\r\n\r\n")}

func readGoogleSSE(reader io.Reader, handle func(json.RawMessage) error) error {
	buffer := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		read, readErr := reader.Read(chunk)
		if read > 0 {
			if err := googleSSEChunkError(chunk[:read]); err != nil {
				return err
			}
			buffer = append(buffer, chunk[:read]...)
			for {
				index, length := nextGoogleSSEDelimiter(buffer)
				if index < 0 {
					break
				}
				if err := handleGoogleSSEEvent(buffer[:index], handle); err != nil {
					return err
				}
				buffer = append(buffer[:0], buffer[index+length:]...)
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return readErr
			}
			if len(bytes.TrimSpace(buffer)) != 0 {
				return errors.New("Incomplete JSON segment at the end") //nolint:staticcheck // Exact upstream text.
			}
			return nil
		}
	}
}

func googleSSEChunkError(chunk []byte) error {
	normalized, err := ai.NormalizeJSONStringifyJSON(chunk)
	if err != nil {
		return nil
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(normalized, &envelope) != nil {
		return nil
	}
	rawError, ok := envelope["error"]
	if !ok {
		return nil
	}
	var detail map[string]json.RawMessage
	if json.Unmarshal(rawError, &detail) != nil || detail == nil {
		return nil
	}
	code, ok := googleJSNumber(detail["code"])
	if !ok || code < 400 || code >= 600 {
		return nil
	}
	return fmt.Errorf("got status: %s. %s", googleJSString(detail, "status"), normalized) //nolint:staticcheck // Exact pinned SDK text.
}

func googleJSNumber(value json.RawMessage) (float64, bool) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return 0, false
	}
	switch trimmed[0] {
	case '"':
		var text string
		if json.Unmarshal(trimmed, &text) != nil {
			return 0, false
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return 0, true
		}
		number, err := strconv.ParseFloat(text, 64)
		return number, err == nil
	case 't':
		return 1, true
	case 'f', 'n':
		return 0, true
	default:
		number, err := strconv.ParseFloat(string(trimmed), 64)
		return number, err == nil
	}
}

func googleJSString(object map[string]json.RawMessage, name string) string {
	value, ok := object[name]
	if !ok {
		return "undefined"
	}
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return "undefined"
	}
	if trimmed[0] == '"' {
		var text string
		if json.Unmarshal(trimmed, &text) == nil {
			return text
		}
	}
	return string(trimmed)
}

func nextGoogleSSEDelimiter(buffer []byte) (index, length int) {
	index = -1
	for _, delimiter := range googleSSEDelimiters {
		candidate := bytes.Index(buffer, delimiter)
		if candidate >= 0 && (index < 0 || candidate < index) {
			index, length = candidate, len(delimiter)
		}
	}
	return index, length
}

func handleGoogleSSEEvent(event []byte, handle func(json.RawMessage) error) error {
	trimmed := bytes.TrimSpace(event)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil
	}
	return handle(json.RawMessage(bytes.TrimSpace(trimmed[len("data:"):])))
}

type googleStreamProcessor struct {
	model        *ai.Model
	output       *ai.AssistantMessage
	currentText  *ai.TextContent
	currentThink *ai.ThinkingContent
}

func (processor *googleStreamProcessor) process(chunk googleGenerateContentResponse) []ai.AssistantMessageEvent {
	if processor.output.ResponseID == nil && chunk.ResponseID != "" {
		value := chunk.ResponseID
		processor.output.ResponseID = &value
	}
	events := make([]ai.AssistantMessageEvent, 0)
	if len(chunk.Candidates) > 0 {
		candidate := chunk.Candidates[0]
		if candidate.Content != nil {
			for _, part := range candidate.Content.Parts {
				if part.Text != nil {
					thinking := part.Thought
					if (thinking && processor.currentThink == nil) || (!thinking && processor.currentText == nil) {
						events = append(events, processor.closeCurrent())
						if thinking {
							block := &ai.ThinkingContent{}
							processor.output.Content = append(processor.output.Content, block)
							processor.currentThink = block
							events = append(events, ai.ThinkingStartEvent{ContentIndex: len(processor.output.Content) - 1, Partial: processor.output})
						} else {
							block := &ai.TextContent{}
							processor.output.Content = append(processor.output.Content, block)
							processor.currentText = block
							events = append(events, ai.TextStartEvent{ContentIndex: len(processor.output.Content) - 1, Partial: processor.output})
						}
					}
					if thinking {
						processor.currentThink.Thinking += *part.Text
						processor.currentThink.ThinkingSignature = retainGoogleThoughtSignature(processor.currentThink.ThinkingSignature, part.ThoughtSignature)
						events = append(events, ai.ThinkingDeltaEvent{ContentIndex: len(processor.output.Content) - 1, Delta: *part.Text, Partial: processor.output})
					} else {
						processor.currentText.Text += *part.Text
						processor.currentText.TextSignature = retainGoogleThoughtSignature(processor.currentText.TextSignature, part.ThoughtSignature)
						events = append(events, ai.TextDeltaEvent{ContentIndex: len(processor.output.Content) - 1, Delta: *part.Text, Partial: processor.output})
					}
				}
				if part.FunctionCall != nil {
					events = append(events, processor.closeCurrent())
					call := processor.googleToolCall(part)
					processor.output.Content = append(processor.output.Content, call)
					index := len(processor.output.Content) - 1
					events = append(events, ai.ToolCallStartEvent{ContentIndex: index, Partial: processor.output})
					arguments, err := ai.MarshalToolCallArguments(call)
					if err != nil {
						arguments = []byte(`{}`)
					}
					events = append(events,
						ai.ToolCallDeltaEvent{ContentIndex: index, Delta: string(arguments), Partial: processor.output},
						ai.ToolCallEndEvent{ContentIndex: index, ToolCall: call, Partial: processor.output},
					)
				}
			}
		}
		if candidate.FinishReason != "" {
			processor.output.StopReason = mapGoogleStopReason(candidate.FinishReason)
			if googleOutputHasToolCall(processor.output) {
				processor.output.StopReason = ai.StopReasonToolUse
			}
		}
	}
	if chunk.UsageMetadata != nil {
		metadata := chunk.UsageMetadata
		reasoning := metadata.ThoughtsTokenCount
		processor.output.Usage = ai.Usage{
			Input:     metadata.PromptTokenCount - metadata.CachedContentTokenCount,
			Output:    metadata.CandidatesTokenCount + metadata.ThoughtsTokenCount,
			CacheRead: metadata.CachedContentTokenCount, CacheWrite: 0,
			Reasoning: &reasoning, TotalTokens: metadata.TotalTokenCount, Cost: ai.Cost{},
		}
		calculateCost(processor.model, &processor.output.Usage)
	}
	return compactGoogleEvents(events)
}

func (processor *googleStreamProcessor) closeCurrent() ai.AssistantMessageEvent {
	index := len(processor.output.Content) - 1
	if processor.currentText != nil {
		block := processor.currentText
		processor.currentText = nil
		return ai.TextEndEvent{ContentIndex: index, Content: block.Text, Partial: processor.output}
	}
	if processor.currentThink != nil {
		block := processor.currentThink
		processor.currentThink = nil
		return ai.ThinkingEndEvent{ContentIndex: index, Content: block.Thinking, Partial: processor.output}
	}
	return nil
}

func (processor *googleStreamProcessor) finish() ai.AssistantMessageEvent {
	return processor.closeCurrent()
}

func (processor *googleStreamProcessor) googleToolCall(part GooglePart) *ai.ToolCall {
	call := part.FunctionCall
	id := call.ID
	if id == "" || googleOutputHasToolCallID(processor.output, id) {
		id = fmt.Sprintf("%s_%d_%d", call.Name, openAINowUnixMilli(), googleToolCallCounter.Add(1))
	}
	var thoughtSignature *string
	if part.ThoughtSignature != nil && *part.ThoughtSignature != "" {
		value := *part.ThoughtSignature
		thoughtSignature = &value
	}
	toolCall := &ai.ToolCall{ID: id, Name: call.Name, Arguments: map[string]any{}, ThoughtSignature: thoughtSignature}
	arguments := call.Args
	if len(arguments) == 0 || bytes.Equal(bytes.TrimSpace(arguments), []byte("null")) {
		arguments = []byte(`{}`)
	}
	if err := ai.SetToolCallArgumentsJSON(toolCall, arguments); err != nil {
		_ = ai.SetToolCallArgumentsJSON(toolCall, []byte(`{}`))
	}
	return toolCall
}

func compactGoogleEvents(events []ai.AssistantMessageEvent) []ai.AssistantMessageEvent {
	result := events[:0]
	for _, event := range events {
		if event != nil {
			result = append(result, event)
		}
	}
	return result
}

func googleOutputHasToolCall(output *ai.AssistantMessage) bool {
	for _, block := range output.Content {
		if _, ok := block.(*ai.ToolCall); ok {
			return true
		}
	}
	return false
}

func googleOutputHasToolCallID(output *ai.AssistantMessage, id string) bool {
	for _, block := range output.Content {
		if call, ok := block.(*ai.ToolCall); ok && call.ID == id {
			return true
		}
	}
	return false
}

func clampGoogleReasoning(model *ai.Model, requested ai.ThinkingLevel) ai.ThinkingLevel {
	clamped := clampSimpleReasoning(model, &requested)
	if clamped != nil {
		return *clamped
	}
	return ai.ThinkingLevel(ai.ModelThinkingOff)
}

func isGemma4(model *ai.Model) bool {
	return gemma4Pattern.MatchString(strings.ToLower(model.ID))
}

func isGemini3Pro(model *ai.Model) bool {
	return gemini3ProPattern.MatchString(strings.ToLower(model.ID))
}

func isGemini3Flash(model *ai.Model) bool {
	id := strings.ToLower(model.ID)
	return gemini3FlashPattern.MatchString(id) || id == "gemini-flash-latest" || id == "gemini-flash-lite-latest"
}

func disabledGoogleThinkingConfig(model *ai.Model) *GoogleThinkingConfig {
	if isGemini3Pro(model) {
		level := GoogleThinkingLow
		return &GoogleThinkingConfig{ThinkingLevel: &level}
	}
	if isGemini3Flash(model) || isGemma4(model) {
		level := GoogleThinkingMinimal
		return &GoogleThinkingConfig{ThinkingLevel: &level}
	}
	zero := int64(0)
	return &GoogleThinkingConfig{ThinkingBudget: &zero}
}

func googleThinkingLevel(effort ai.ThinkingLevel, model *ai.Model) GoogleThinkingLevel {
	if isGemini3Pro(model) {
		if effort == ai.ThinkingMinimal || effort == ai.ThinkingLow {
			return GoogleThinkingLow
		}
		return GoogleThinkingHigh
	}
	if isGemma4(model) {
		if effort == ai.ThinkingMinimal || effort == ai.ThinkingLow {
			return GoogleThinkingMinimal
		}
		return GoogleThinkingHigh
	}
	switch effort {
	case ai.ThinkingMinimal:
		return GoogleThinkingMinimal
	case ai.ThinkingLow:
		return GoogleThinkingLow
	case ai.ThinkingMedium:
		return GoogleThinkingMedium
	default:
		return GoogleThinkingHigh
	}
}

func googleThinkingBudget(model *ai.Model, effort ai.ThinkingLevel, custom *ai.ThinkingBudgets) int64 {
	if custom != nil {
		var value *int
		switch effort {
		case ai.ThinkingMinimal:
			value = custom.Minimal
		case ai.ThinkingLow:
			value = custom.Low
		case ai.ThinkingMedium:
			value = custom.Medium
		case ai.ThinkingHigh:
			value = custom.High
		}
		if value != nil {
			return int64(*value)
		}
	}
	id := model.ID
	var budgets map[ai.ThinkingLevel]int64
	switch {
	case strings.Contains(id, "2.5-pro"):
		budgets = map[ai.ThinkingLevel]int64{ai.ThinkingMinimal: 128, ai.ThinkingLow: 2048, ai.ThinkingMedium: 8192, ai.ThinkingHigh: 32768}
	case strings.Contains(id, "2.5-flash-lite"):
		budgets = map[ai.ThinkingLevel]int64{ai.ThinkingMinimal: 512, ai.ThinkingLow: 2048, ai.ThinkingMedium: 8192, ai.ThinkingHigh: 24576}
	case strings.Contains(id, "2.5-flash"):
		budgets = map[ai.ThinkingLevel]int64{ai.ThinkingMinimal: 128, ai.ThinkingLow: 2048, ai.ThinkingMedium: 8192, ai.ThinkingHigh: 24576}
	default:
		return -1
	}
	return budgets[effort]
}
