package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
	"github.com/OrdalieTech/pigo/internal/partialjson"
	aws "github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockdocument "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go/auth/bearer"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const (
	bedrockEmptyTextPlaceholder = "<empty>"
	bedrockDataRetentionDocsURL = "https://docs.aws.amazon.com/bedrock/latest/userguide/data-retention.html"
)

var (
	bedrockStandardEndpoint = regexp.MustCompile(`^bedrock-runtime(?:-fips)?\.([a-z0-9-]+)\.amazonaws\.com(?:\.cn)?$`)
	// bedrockARNRegionPattern matches upstream bedrock-converse-stream.ts:166
	// exactly; laxer prefix checks accepted malformed ARNs. (OT-m6)
	bedrockARNRegionPattern   = regexp.MustCompile(`^arn:aws(?:-[a-z0-9-]+)?:bedrock:([a-z0-9-]+):`)
	newBedrockTransport       = newAWSBedrockTransport
	bedrockHTTPClientOverride aws.HTTPClient
)

type BedrockThinkingDisplay string

const (
	BedrockThinkingSummarized BedrockThinkingDisplay = "summarized"
	BedrockThinkingOmitted    BedrockThinkingDisplay = "omitted"
)

type BedrockToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

func (choice *BedrockToolChoice) UnmarshalJSON(data []byte) error {
	var value string
	if json.Unmarshal(data, &value) == nil {
		choice.Type = value
		choice.Name = ""
		return nil
	}
	type plain BedrockToolChoice
	return json.Unmarshal(data, (*plain)(choice))
}

type BedrockConverseStreamOptions struct {
	ai.StreamOptions
	Region              string                  `json:"region,omitempty"`
	Profile             string                  `json:"profile,omitempty"`
	ToolChoice          *BedrockToolChoice      `json:"toolChoice,omitempty"`
	Reasoning           *ai.ThinkingLevel       `json:"reasoning,omitempty"`
	ThinkingBudgets     *ai.ThinkingBudgets     `json:"thinkingBudgets,omitempty"`
	InterleavedThinking *bool                   `json:"interleavedThinking,omitempty"`
	ThinkingDisplay     *BedrockThinkingDisplay `json:"thinkingDisplay,omitempty"`
	RequestMetadata     map[string]string       `json:"requestMetadata,omitempty"`
	BearerToken         string                  `json:"bearerToken,omitempty"`
}

// BedrockConverseStreamPayload mirrors the public ConverseStream command input.
// The AWS SDK remains confined to this adapter while payload hooks retain a
// provider-shaped value that callers can inspect and replace.
type BedrockConverseStreamPayload struct {
	ModelID                      string                      `json:"modelId"`
	Messages                     []BedrockMessage            `json:"messages"`
	System                       []BedrockSystemContentBlock `json:"system,omitempty"`
	InferenceConfig              BedrockInferenceConfig      `json:"inferenceConfig"`
	ToolConfig                   *BedrockToolConfiguration   `json:"toolConfig,omitempty"`
	AdditionalModelRequestFields map[string]any              `json:"additionalModelRequestFields,omitempty"`
	RequestMetadata              map[string]string           `json:"requestMetadata,omitempty"`

	// Extra retains hook-injected top-level members without a typed field
	// (guardrailConfig, performanceConfig, ...) so they reach the SDK input,
	// mirroring upstream's verbatim ConverseStreamCommand pass-through. (OT-M7)
	Extra map[string]json.RawMessage `json:"-"`
}

type BedrockInferenceConfig struct {
	MaxTokens     *float64 `json:"maxTokens,omitempty"`
	Temperature   *float64 `json:"temperature,omitempty"`
	StopSequences []string `json:"stopSequences,omitempty"`
	TopP          *float64 `json:"topP,omitempty"`
}

type BedrockMessage struct {
	Role    string                `json:"role"`
	Content []BedrockContentBlock `json:"content"`
}

type BedrockContentBlock struct {
	Text             *string                  `json:"text,omitempty"`
	Image            *BedrockImageBlock       `json:"image,omitempty"`
	ToolUse          *BedrockToolUseBlock     `json:"toolUse,omitempty"`
	ToolResult       *BedrockToolResultBlock  `json:"toolResult,omitempty"`
	ReasoningContent *BedrockReasoningContent `json:"reasoningContent,omitempty"`
	CachePoint       *BedrockCachePoint       `json:"cachePoint,omitempty"`
}

type BedrockSystemContentBlock struct {
	Text       *string            `json:"text,omitempty"`
	CachePoint *BedrockCachePoint `json:"cachePoint,omitempty"`
}

type BedrockCachePoint struct {
	Type string  `json:"type"`
	TTL  *string `json:"ttl,omitempty"`
}

type BedrockImageBlock struct {
	Format string             `json:"format"`
	Source BedrockImageSource `json:"source"`
}

type BedrockImageSource struct {
	Bytes string `json:"bytes"`
}

type BedrockToolUseBlock struct {
	ToolUseID string `json:"toolUseId"`
	Name      string `json:"name"`
	Input     any    `json:"input"`
}

type BedrockToolResultBlock struct {
	ToolUseID string                          `json:"toolUseId"`
	Content   []BedrockToolResultContentBlock `json:"content"`
	Status    string                          `json:"status"`
}

type BedrockToolResultContentBlock struct {
	Text  *string            `json:"text,omitempty"`
	Image *BedrockImageBlock `json:"image,omitempty"`
}

type BedrockReasoningContent struct {
	ReasoningText BedrockReasoningText `json:"reasoningText"`
}

type BedrockReasoningText struct {
	Text      string  `json:"text"`
	Signature *string `json:"signature,omitempty"`
}

type BedrockToolConfiguration struct {
	Tools      []BedrockTool `json:"tools"`
	ToolChoice any           `json:"toolChoice,omitempty"`
}

type BedrockTool struct {
	ToolSpec BedrockToolSpecification `json:"toolSpec"`
}

type BedrockToolSpecification struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema BedrockToolInputSchema `json:"inputSchema"`
}

type BedrockToolInputSchema struct {
	JSON jsonschema.Schema `json:"json"`
}

func StreamBedrockConverse(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: Bedrock ConverseStream model is nil")
	}
	options := &BedrockConverseStreamOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamBedrockConverseWithOptions(ctx, request.Model, request.Context, options)
}

func StreamSimpleBedrockConverse(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Bedrock ConverseStream model is nil")
	}
	base := buildBaseStreamOptions(model, requestContext, options)
	bedrockOptions := &BedrockConverseStreamOptions{StreamOptions: base}
	if options == nil || options.Reasoning == nil {
		return StreamBedrockConverseWithOptions(ctx, model, requestContext, bedrockOptions)
	}
	bedrockOptions.Reasoning = options.Reasoning
	bedrockOptions.ThinkingBudgets = options.ThinkingBudgets
	if isBedrockClaudeModel(model) && !supportsBedrockAdaptiveThinking(model) {
		maxTokens, budget := adjustMaxTokensForThinking(*base.MaxTokens, model.MaxTokens, *options.Reasoning, options.ThinkingBudgets)
		maxTokens = clampMaxTokensToContext(model, requestContext, maxTokens)
		budget = min(budget, max(float64(0), maxTokens-1024))
		base.MaxTokens = &maxTokens
		bedrockOptions.StreamOptions = base
		level := bedrockBudgetLevel(*options.Reasoning)
		budgets := ai.ThinkingBudgets{}
		setBedrockThinkingBudget(&budgets, level, int(budget))
		bedrockOptions.ThinkingBudgets = &budgets
	}
	return StreamBedrockConverseWithOptions(ctx, model, requestContext, bedrockOptions)
}

func StreamBedrockConverseWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *BedrockConverseStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: Bedrock ConverseStream model is nil")
	}
	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		output := newAssistantMessage(model)
		streamOptions := bedrockStreamOptions(options)
		streamCtx := ctx
		cancel := func() {}
		if streamOptions.TimeoutMS != nil && *streamOptions.TimeoutMS > 0 {
			streamCtx, cancel = context.WithTimeout(ctx, time.Duration(*streamOptions.TimeoutMS)*time.Millisecond)
		}
		defer cancel()

		fail := func(err error) {
			clearBedrockStreamingFields(output)
			reason := ai.StopReasonError
			if streamCtx.Err() != nil {
				reason = ai.StopReasonAborted
				err = errors.New("Request was aborted") //nolint:staticcheck // Exact upstream text is observable.
			}
			output.StopReason = reason
			message := formatBedrockError(err)
			output.ErrorMessage = &message
			yield(ai.ErrorEvent{Reason: reason, Error: output}, nil)
		}

		payload, err := buildBedrockPayload(model, requestContext, options)
		if err != nil {
			fail(err)
			return
		}
		hooked, err := applyPayloadHook(streamCtx, model, streamOptions, payload)
		if err != nil {
			fail(err)
			return
		}
		payload, err = coerceBedrockPayload(hooked)
		if err != nil {
			fail(err)
			return
		}

		requestOptions, err := applyBedrockHeadersHook(streamCtx, model, options)
		if err != nil {
			fail(err)
			return
		}
		streamOptions = bedrockStreamOptions(requestOptions)
		transport, err := newBedrockTransport(streamCtx, model, requestOptions)
		if err != nil {
			fail(err)
			return
		}
		response, err := transport.Send(streamCtx, payload)
		if err != nil {
			fail(err)
			return
		}
		defer func() { _ = response.Close() }()
		if streamOptions.OnResponse != nil && response.Status() != 0 {
			headers := map[string]string{}
			if requestID := response.RequestID(); requestID != "" {
				headers["x-amzn-requestid"] = requestID
			}
			if err := streamOptions.OnResponse(streamCtx, ai.ProviderResponse{Status: response.Status(), Headers: headers}, model); err != nil {
				fail(err)
				return
			}
		}

		processor := bedrockStreamProcessor{model: model, output: output, sink: func(event ai.AssistantMessageEvent) bool {
			return yield(event, nil)
		}}
		for {
			item, ok := response.Next(streamCtx)
			if !ok {
				break
			}
			if err := processor.handle(item); err != nil {
				fail(err)
				return
			}
			if processor.stopped {
				return
			}
		}
		if err := response.Err(); err != nil {
			fail(err)
			return
		}
		if streamCtx.Err() != nil {
			fail(streamCtx.Err())
			return
		}
		if output.StopReason == ai.StopReasonError || output.StopReason == ai.StopReasonAborted {
			message := "An unknown error occurred"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			fail(errors.New(message))
			return
		}
		yield(ai.DoneEvent{Reason: output.StopReason, Message: output}, nil)
	}, nil
}

func bedrockStreamOptions(options *BedrockConverseStreamOptions) *ai.StreamOptions {
	if options == nil {
		return &ai.StreamOptions{}
	}
	return &options.StreamOptions
}

func applyBedrockHeadersHook(
	ctx context.Context,
	model *ai.Model,
	options *BedrockConverseStreamOptions,
) (*BedrockConverseStreamOptions, error) {
	resolved := BedrockConverseStreamOptions{}
	if options != nil {
		resolved = *options
	}
	headers := copyModelHeaders(model)
	mergeProviderHeaders(headers, bedrockStreamOptions(options).Headers)
	transformed, err := applyHeadersHook(ctx, model, bedrockStreamOptions(options), headers)
	if err != nil {
		return nil, err
	}
	values := make(ai.ProviderHeaders, len(transformed))
	for name, entries := range transformed {
		if len(entries) == 0 {
			values[name] = nil
			continue
		}
		value := strings.Join(entries, ", ")
		values[name] = &value
	}
	resolved.StreamOptions = *bedrockStreamOptions(options)
	resolved.Headers = values
	return &resolved, nil
}

func coerceBedrockPayload(value any) (*BedrockConverseStreamPayload, error) {
	switch payload := value.(type) {
	case *BedrockConverseStreamPayload:
		if payload == nil {
			return nil, errors.New("Bedrock payload hook returned nil") //nolint:staticcheck // User-facing hook error.
		}
		return payload, nil
	case BedrockConverseStreamPayload:
		copy := payload
		return &copy, nil
	}
	encoded, err := ai.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode Bedrock payload hook result: %w", err)
	}
	var payload BedrockConverseStreamPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, fmt.Errorf("decode Bedrock payload hook result: %w", err)
	}
	payload.Extra = bedrockPayloadExtras(encoded)
	return &payload, nil
}

var bedrockKnownPayloadKeys = map[string]bool{
	"modelId": true, "messages": true, "system": true, "inferenceConfig": true,
	"toolConfig": true, "additionalModelRequestFields": true, "requestMetadata": true,
}

// bedrockPayloadExtras collects hook-injected top-level members that the typed
// payload does not model, so they are not silently dropped. (OT-M7)
func bedrockPayloadExtras(encoded []byte) map[string]json.RawMessage {
	var members map[string]json.RawMessage
	if json.Unmarshal(encoded, &members) != nil {
		return nil
	}
	extras := make(map[string]json.RawMessage)
	for name, raw := range members {
		if !bedrockKnownPayloadKeys[name] {
			extras[name] = raw
		}
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}

func buildBedrockPayload(
	model *ai.Model,
	requestContext ai.Context,
	options *BedrockConverseStreamOptions,
) (*BedrockConverseStreamPayload, error) {
	retention := resolveCacheRetention(bedrockStreamOptions(options))
	messages, err := convertBedrockMessages(requestContext, model, retention, bedrockStreamOptions(options))
	if err != nil {
		return nil, err
	}
	system := buildBedrockSystemPrompt(requestContext.SystemPrompt, model, retention, bedrockStreamOptions(options))
	toolConfig := convertBedrockToolConfig(requestContext.Tools, options)
	additional := buildBedrockAdditionalFields(model, options)

	var maxTokens *float64
	if options != nil && options.MaxTokens != nil {
		value := *options.MaxTokens
		maxTokens = &value
	} else if isBedrockClaudeModel(model) {
		value := model.MaxTokens
		maxTokens = &value
	}
	var temperature *float64
	if options != nil && options.Temperature != nil {
		value := *options.Temperature
		temperature = &value
	}
	return &BedrockConverseStreamPayload{
		ModelID:                      model.ID,
		Messages:                     messages,
		System:                       system,
		InferenceConfig:              BedrockInferenceConfig{MaxTokens: maxTokens, Temperature: temperature},
		ToolConfig:                   toolConfig,
		AdditionalModelRequestFields: additional,
		RequestMetadata:              cloneBedrockMetadata(options),
	}, nil
}

func cloneBedrockMetadata(options *BedrockConverseStreamOptions) map[string]string {
	if options == nil || options.RequestMetadata == nil {
		return nil
	}
	result := make(map[string]string, len(options.RequestMetadata))
	for key, value := range options.RequestMetadata {
		result[key] = value
	}
	return result
}

func buildBedrockSystemPrompt(
	systemPrompt *string,
	model *ai.Model,
	retention ai.CacheRetention,
	options *ai.StreamOptions,
) []BedrockSystemContentBlock {
	if systemPrompt == nil || *systemPrompt == "" {
		return nil
	}
	text := sanitizeText(*systemPrompt)
	result := []BedrockSystemContentBlock{{Text: &text}}
	if retention != ai.CacheRetentionNone && supportsBedrockPromptCaching(model, options) {
		result = append(result, BedrockSystemContentBlock{CachePoint: newBedrockCachePoint(retention)})
	}
	return result
}

func convertBedrockMessages(
	requestContext ai.Context,
	model *ai.Model,
	retention ai.CacheRetention,
	options *ai.StreamOptions,
) ([]BedrockMessage, error) {
	transformed := transformMessages(requestContext.Messages, model, func(id string, _ *ai.Model, _ *ai.AssistantMessage) string {
		return normalizeBedrockToolCallID(id)
	})
	result := make([]BedrockMessage, 0, len(transformed))
	for index := 0; index < len(transformed); index++ {
		switch message := transformed[index].(type) {
		case *ai.UserMessage:
			blocks, err := convertBedrockUserContent(message.Content)
			if err != nil {
				return nil, err
			}
			result = append(result, BedrockMessage{Role: "user", Content: blocks})
		case *ai.AssistantMessage:
			blocks, err := convertBedrockAssistantContent(message.Content, model)
			if err != nil {
				return nil, err
			}
			if len(blocks) > 0 {
				result = append(result, BedrockMessage{Role: "assistant", Content: blocks})
			}
		case *ai.ToolResultMessage:
			blocks := make([]BedrockContentBlock, 0, 1)
			for index < len(transformed) {
				toolResult, ok := transformed[index].(*ai.ToolResultMessage)
				if !ok {
					break
				}
				content, err := convertBedrockToolResultContent(toolResult.Content)
				if err != nil {
					return nil, err
				}
				status := "success"
				if toolResult.IsError {
					status = "error"
				}
				blocks = append(blocks, BedrockContentBlock{ToolResult: &BedrockToolResultBlock{
					ToolUseID: toolResult.ToolCallID, Content: content, Status: status,
				}})
				index++
			}
			index--
			result = append(result, BedrockMessage{Role: "user", Content: blocks})
		}
	}
	if retention != ai.CacheRetentionNone && supportsBedrockPromptCaching(model, options) && len(result) > 0 {
		last := &result[len(result)-1]
		if last.Role == "user" {
			last.Content = append(last.Content, BedrockContentBlock{CachePoint: newBedrockCachePoint(retention)})
		}
	}
	return result, nil
}

func convertBedrockUserContent(content ai.UserContent) ([]BedrockContentBlock, error) {
	if content.Text != nil {
		text := bedrockRequiredText(*content.Text)
		return []BedrockContentBlock{{Text: &text}}, nil
	}
	result := make([]BedrockContentBlock, 0, len(content.Blocks))
	for _, raw := range content.Blocks {
		switch block := raw.(type) {
		case *ai.TextContent:
			if text, ok := bedrockNonBlankText(block.Text); ok {
				result = append(result, BedrockContentBlock{Text: &text})
			}
		case *ai.ImageContent:
			image, err := newBedrockImageBlock(block.MimeType, block.Data)
			if err != nil {
				return nil, err
			}
			result = append(result, BedrockContentBlock{Image: image})
		}
	}
	if len(result) == 0 {
		text := bedrockEmptyTextPlaceholder
		result = append(result, BedrockContentBlock{Text: &text})
	}
	return result, nil
}

func convertBedrockAssistantContent(content ai.AssistantContent, model *ai.Model) ([]BedrockContentBlock, error) {
	result := make([]BedrockContentBlock, 0, len(content))
	for _, raw := range content {
		switch block := raw.(type) {
		case *ai.TextContent:
			if text, ok := bedrockNonBlankText(block.Text); ok {
				result = append(result, BedrockContentBlock{Text: &text})
			}
		case *ai.ToolCall:
			result = append(result, BedrockContentBlock{ToolUse: &BedrockToolUseBlock{
				ToolUseID: block.ID, Name: block.Name, Input: block.Arguments,
			}})
		case *ai.ThinkingContent:
			thinking, ok := bedrockNonBlankText(block.Thinking)
			if !ok {
				continue
			}
			if isBedrockClaudeModel(model) && (block.ThinkingSignature == nil || strings.TrimSpace(*block.ThinkingSignature) == "") {
				result = append(result, BedrockContentBlock{Text: &thinking})
				continue
			}
			var signature *string
			if isBedrockClaudeModel(model) {
				signature = block.ThinkingSignature
			}
			result = append(result, BedrockContentBlock{ReasoningContent: &BedrockReasoningContent{
				ReasoningText: BedrockReasoningText{Text: thinking, Signature: signature},
			}})
		}
	}
	return result, nil
}

func convertBedrockToolResultContent(content ai.ToolResultContent) ([]BedrockToolResultContentBlock, error) {
	result := make([]BedrockToolResultContentBlock, 0, len(content))
	for _, raw := range content {
		switch block := raw.(type) {
		case *ai.TextContent:
			if text, ok := bedrockNonBlankText(block.Text); ok {
				result = append(result, BedrockToolResultContentBlock{Text: &text})
			}
		case *ai.ImageContent:
			image, err := newBedrockImageBlock(block.MimeType, block.Data)
			if err != nil {
				return nil, err
			}
			result = append(result, BedrockToolResultContentBlock{Image: image})
		}
	}
	if len(result) == 0 {
		text := bedrockEmptyTextPlaceholder
		result = append(result, BedrockToolResultContentBlock{Text: &text})
	}
	return result, nil
}

func bedrockNonBlankText(text string) (string, bool) {
	text = sanitizeText(text)
	return text, strings.TrimSpace(text) != ""
}

func bedrockRequiredText(text string) string {
	if value, ok := bedrockNonBlankText(text); ok {
		return value
	}
	return bedrockEmptyTextPlaceholder
}

func newBedrockImageBlock(mimeType, data string) (*BedrockImageBlock, error) {
	var format string
	switch mimeType {
	case "image/jpeg", "image/jpg":
		format = "jpeg"
	case "image/png":
		format = "png"
	case "image/gif":
		format = "gif"
	case "image/webp":
		format = "webp"
	default:
		return nil, fmt.Errorf("Unknown image type: %s", mimeType) //nolint:staticcheck // Exact upstream error text is observable.
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return nil, fmt.Errorf("decode %s image: %w", mimeType, err)
	}
	return &BedrockImageBlock{Format: format, Source: BedrockImageSource{Bytes: data}}, nil
}

func convertBedrockToolConfig(tools *[]ai.Tool, options *BedrockConverseStreamOptions) *BedrockToolConfiguration {
	if tools == nil || len(*tools) == 0 || options != nil && options.ToolChoice != nil && options.ToolChoice.Type == "none" {
		return nil
	}
	result := &BedrockToolConfiguration{Tools: make([]BedrockTool, 0, len(*tools))}
	for _, tool := range *tools {
		result.Tools = append(result.Tools, BedrockTool{ToolSpec: BedrockToolSpecification{
			Name: tool.Name, Description: tool.Description, InputSchema: BedrockToolInputSchema{JSON: tool.Parameters},
		}})
	}
	if options != nil && options.ToolChoice != nil {
		switch options.ToolChoice.Type {
		case "auto":
			result.ToolChoice = map[string]any{"auto": map[string]any{}}
		case "any":
			result.ToolChoice = map[string]any{"any": map[string]any{}}
		case "tool":
			result.ToolChoice = map[string]any{"tool": map[string]any{"name": options.ToolChoice.Name}}
		}
	}
	return result
}

func newBedrockCachePoint(retention ai.CacheRetention) *BedrockCachePoint {
	point := &BedrockCachePoint{Type: "default"}
	if retention == ai.CacheRetentionLong {
		ttl := "1h"
		point.TTL = &ttl
	}
	return point
}

func normalizeBedrockToolCallID(id string) string {
	var normalized strings.Builder
	for _, char := range id {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			normalized.WriteRune(char)
		} else {
			normalized.WriteByte('_')
		}
	}
	value := normalized.String()
	if len(value) > 64 {
		value = value[:64]
	}
	return value
}

func bedrockModelCandidates(model *ai.Model) []string {
	values := []string{model.ID}
	if model.Name != "" {
		values = append(values, model.Name)
	}
	result := make([]string, 0, len(values)*2)
	replacer := strings.NewReplacer(" ", "-", "_", "-", ".", "-", ":", "-")
	for _, value := range values {
		lower := strings.ToLower(value)
		result = append(result, lower, replacer.Replace(lower))
	}
	return result
}

func isBedrockClaudeModel(model *ai.Model) bool {
	for _, candidate := range bedrockModelCandidates(model) {
		if strings.Contains(candidate, "anthropic.claude") || strings.Contains(candidate, "anthropic/claude") || strings.Contains(candidate, "claude") {
			return true
		}
	}
	return false
}

func supportsBedrockAdaptiveThinking(model *ai.Model) bool {
	for _, candidate := range bedrockModelCandidates(model) {
		for _, fragment := range []string{"opus-4-6", "opus-4-7", "opus-4-8", "sonnet-4-6", "sonnet-5", "fable-5"} {
			if strings.Contains(candidate, fragment) {
				return true
			}
		}
	}
	return false
}

func supportsBedrockNativeXHigh(model *ai.Model) bool {
	for _, candidate := range bedrockModelCandidates(model) {
		for _, fragment := range []string{"opus-4-7", "opus-4-8", "sonnet-5", "fable-5"} {
			if strings.Contains(candidate, fragment) {
				return true
			}
		}
	}
	return false
}

func supportsBedrockPromptCaching(model *ai.Model, options *ai.StreamOptions) bool {
	candidates := bedrockModelCandidates(model)
	hasClaude := false
	for _, candidate := range candidates {
		hasClaude = hasClaude || strings.Contains(candidate, "claude")
	}
	if !hasClaude {
		return providerEnvValue("AWS_BEDROCK_FORCE_CACHE", options) == "1"
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "fable-5") || strings.Contains(candidate, "sonnet-5") || strings.Contains(candidate, "-4-") || strings.Contains(candidate, "claude-3-7-sonnet") || strings.Contains(candidate, "claude-3-5-haiku") {
			return true
		}
	}
	return false
}

func buildBedrockAdditionalFields(model *ai.Model, options *BedrockConverseStreamOptions) map[string]any {
	if options == nil || options.Reasoning == nil || !model.Reasoning || !isBedrockClaudeModel(model) {
		return nil
	}
	display := any(BedrockThinkingSummarized)
	if options.ThinkingDisplay != nil {
		display = *options.ThinkingDisplay
	}
	if isBedrockGovCloudTarget(model, options) {
		display = nil
	}
	if supportsBedrockAdaptiveThinking(model) {
		thinking := map[string]any{"type": "adaptive"}
		if display != nil {
			thinking["display"] = display
		}
		return map[string]any{
			"thinking":      thinking,
			"output_config": map[string]any{"effort": mapBedrockThinkingEffort(model, *options.Reasoning)},
		}
	}
	budget := bedrockThinkingBudget(*options.Reasoning, options.ThinkingBudgets)
	thinking := map[string]any{"type": "enabled", "budget_tokens": budget}
	if display != nil {
		thinking["display"] = display
	}
	result := map[string]any{"thinking": thinking}
	if options.InterleavedThinking == nil || *options.InterleavedThinking {
		result["anthropic_beta"] = []string{"interleaved-thinking-2025-05-14"}
	}
	return result
}

func mapBedrockThinkingEffort(model *ai.Model, level ai.ThinkingLevel) string {
	if level == ai.ThinkingXHigh && supportsBedrockNativeXHigh(model) {
		return "xhigh"
	}
	if model.ThinkingLevelMap != nil {
		if mapped, ok := (*model.ThinkingLevelMap)[ai.ModelThinkingLevel(level)]; ok && mapped != nil {
			return *mapped
		}
	}
	switch level {
	case ai.ThinkingMinimal, ai.ThinkingLow:
		return "low"
	case ai.ThinkingMedium:
		return "medium"
	default:
		return "high"
	}
}

func bedrockThinkingBudget(level ai.ThinkingLevel, budgets *ai.ThinkingBudgets) int {
	defaults := map[ai.ThinkingLevel]int{
		ai.ThinkingMinimal: 1024, ai.ThinkingLow: 2048, ai.ThinkingMedium: 8192,
		ai.ThinkingHigh: 16384, ai.ThinkingXHigh: 16384, ai.ThinkingMax: 16384,
	}
	level = bedrockBudgetLevel(level)
	if budgets != nil {
		var configured *int
		switch level {
		case ai.ThinkingMinimal:
			configured = budgets.Minimal
		case ai.ThinkingLow:
			configured = budgets.Low
		case ai.ThinkingMedium:
			configured = budgets.Medium
		case ai.ThinkingHigh:
			configured = budgets.High
		}
		if configured != nil {
			return *configured
		}
	}
	return defaults[level]
}

func bedrockBudgetLevel(level ai.ThinkingLevel) ai.ThinkingLevel {
	if level == ai.ThinkingXHigh || level == ai.ThinkingMax {
		return ai.ThinkingHigh
	}
	return level
}

func setBedrockThinkingBudget(budgets *ai.ThinkingBudgets, level ai.ThinkingLevel, value int) {
	switch level {
	case ai.ThinkingMinimal:
		budgets.Minimal = &value
	case ai.ThinkingLow:
		budgets.Low = &value
	case ai.ThinkingMedium:
		budgets.Medium = &value
	default:
		budgets.High = &value
	}
}

func isBedrockGovCloudTarget(model *ai.Model, options *BedrockConverseStreamOptions) bool {
	if region := configuredBedrockRegion(options); strings.HasPrefix(strings.ToLower(region), "us-gov-") {
		return true
	}
	id := strings.ToLower(model.ID)
	return strings.HasPrefix(id, "us-gov.") || strings.HasPrefix(id, "arn:aws-us-gov:")
}

type bedrockStreamItemKind uint8

const (
	bedrockItemMessageStart bedrockStreamItemKind = iota + 1
	bedrockItemContentStart
	bedrockItemContentDelta
	bedrockItemContentStop
	bedrockItemMessageStop
	bedrockItemMetadata
)

type bedrockStreamItem struct {
	Kind               bedrockStreamItemKind
	Role               string
	ContentBlockIndex  int
	ToolUseID          string
	ToolName           string
	Text               *string
	ToolInput          *string
	ReasoningText      *string
	ReasoningSignature *string
	StopReason         string
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	TotalTokens        int64
}

type bedrockResponse interface {
	Status() int
	RequestID() string
	Next(context.Context) (bedrockStreamItem, bool)
	Close() error
	Err() error
}

type bedrockTransport interface {
	Send(context.Context, *BedrockConverseStreamPayload) (bedrockResponse, error)
}

type bedrockBlock struct {
	content ai.AssistantContentBlock
	index   int
}

type bedrockStreamProcessor struct {
	model   *ai.Model
	output  *ai.AssistantMessage
	blocks  []bedrockBlock
	sink    eventSink
	stopped bool
}

func (processor *bedrockStreamProcessor) handle(item bedrockStreamItem) error {
	switch item.Kind {
	case bedrockItemMessageStart:
		if item.Role != "assistant" {
			return errors.New("Unexpected assistant message start but got user message start instead") //nolint:staticcheck // Exact upstream text is observable.
		}
		processor.stopped = !processor.sink(ai.StartEvent{Partial: processor.output})
	case bedrockItemContentStart:
		if item.ToolUseID != "" || item.ToolName != "" {
			partial := ""
			index := item.ContentBlockIndex
			block := &ai.ToolCall{ID: item.ToolUseID, Name: item.ToolName, Arguments: map[string]any{}, PartialJSON: &partial, Index: &index}
			processor.output.Content = append(processor.output.Content, block)
			processor.blocks = append(processor.blocks, bedrockBlock{content: block, index: item.ContentBlockIndex})
			processor.stopped = !processor.sink(ai.ToolCallStartEvent{ContentIndex: len(processor.output.Content) - 1, Partial: processor.output})
		}
	case bedrockItemContentDelta:
		processor.handleDelta(item)
	case bedrockItemContentStop:
		processor.handleStop(item.ContentBlockIndex)
	case bedrockItemMessageStop:
		processor.output.StopReason, processor.output.ErrorMessage = mapBedrockStopReason(item.StopReason)
	case bedrockItemMetadata:
		processor.output.Usage.Input = item.InputTokens
		processor.output.Usage.Output = item.OutputTokens
		processor.output.Usage.CacheRead = item.CacheReadTokens
		processor.output.Usage.CacheWrite = item.CacheWriteTokens
		processor.output.Usage.TotalTokens = item.TotalTokens
		if processor.output.Usage.TotalTokens == 0 {
			processor.output.Usage.TotalTokens = item.InputTokens + item.OutputTokens
		}
		calculateCost(processor.model, &processor.output.Usage)
	}
	return nil
}

func (processor *bedrockStreamProcessor) blockAt(index int) (int, ai.AssistantContentBlock) {
	for position, block := range processor.blocks {
		if block.index == index {
			return position, block.content
		}
	}
	return -1, nil
}

func (processor *bedrockStreamProcessor) handleDelta(item bedrockStreamItem) {
	position, content := processor.blockAt(item.ContentBlockIndex)
	if item.Text != nil {
		// Upstream only creates a block when NO block exists at the stream
		// index; a type-mismatched block drops the delta. (OT-m5)
		if content == nil {
			index := item.ContentBlockIndex
			block := &ai.TextContent{Text: *item.Text, Index: &index}
			processor.output.Content = append(processor.output.Content, block)
			position = len(processor.output.Content) - 1
			processor.blocks = append(processor.blocks, bedrockBlock{content: block, index: item.ContentBlockIndex})
			processor.stopped = !processor.sink(ai.TextStartEvent{ContentIndex: position, Partial: processor.output})
			if !processor.stopped {
				processor.stopped = !processor.sink(ai.TextDeltaEvent{ContentIndex: position, Delta: *item.Text, Partial: processor.output})
			}
		} else if block, ok := content.(*ai.TextContent); ok && !processor.stopped {
			block.Text += *item.Text
			processor.stopped = !processor.sink(ai.TextDeltaEvent{ContentIndex: position, Delta: *item.Text, Partial: processor.output})
		}
		return
	}
	if item.ToolInput != nil {
		if block, ok := content.(*ai.ToolCall); ok {
			partial := *item.ToolInput
			if block.PartialJSON != nil {
				partial = *block.PartialJSON + partial
			}
			block.PartialJSON = &partial
			block.Arguments = bedrockToolArguments(partial)
			processor.stopped = !processor.sink(ai.ToolCallDeltaEvent{ContentIndex: position, Delta: *item.ToolInput, Partial: processor.output})
		}
		return
	}
	if item.ReasoningText != nil || item.ReasoningSignature != nil {
		if content == nil {
			index := item.ContentBlockIndex
			empty := ""
			block := &ai.ThinkingContent{ThinkingSignature: &empty, Index: &index}
			if item.ReasoningText != nil {
				block.Thinking = *item.ReasoningText
			}
			if item.ReasoningSignature != nil && *item.ReasoningSignature != "" {
				value := *item.ReasoningSignature
				block.ThinkingSignature = &value
			}
			processor.output.Content = append(processor.output.Content, block)
			position = len(processor.output.Content) - 1
			processor.blocks = append(processor.blocks, bedrockBlock{content: block, index: item.ContentBlockIndex})
			processor.stopped = !processor.sink(ai.ThinkingStartEvent{ContentIndex: position, Partial: processor.output})
			if item.ReasoningText != nil && *item.ReasoningText != "" && !processor.stopped {
				processor.stopped = !processor.sink(ai.ThinkingDeltaEvent{ContentIndex: position, Delta: *item.ReasoningText, Partial: processor.output})
			}
		} else if block, ok := content.(*ai.ThinkingContent); ok {
			if item.ReasoningText != nil && *item.ReasoningText != "" && !processor.stopped {
				block.Thinking += *item.ReasoningText
				processor.stopped = !processor.sink(ai.ThinkingDeltaEvent{ContentIndex: position, Delta: *item.ReasoningText, Partial: processor.output})
			}
			if item.ReasoningSignature != nil && *item.ReasoningSignature != "" {
				value := *item.ReasoningSignature
				if block.ThinkingSignature != nil {
					value = *block.ThinkingSignature + value
				}
				block.ThinkingSignature = &value
			}
		}
	}
}

func (processor *bedrockStreamProcessor) handleStop(streamIndex int) {
	position, content := processor.blockAt(streamIndex)
	if content == nil {
		return
	}
	switch block := content.(type) {
	case *ai.TextContent:
		block.Index = nil
		processor.stopped = !processor.sink(ai.TextEndEvent{ContentIndex: position, Content: block.Text, Partial: processor.output})
	case *ai.ThinkingContent:
		block.Index = nil
		processor.stopped = !processor.sink(ai.ThinkingEndEvent{ContentIndex: position, Content: block.Thinking, Partial: processor.output})
	case *ai.ToolCall:
		partial := ""
		if block.PartialJSON != nil {
			partial = *block.PartialJSON
		}
		if err := ai.SetToolCallArgumentsJSON(block, []byte(partial)); err != nil {
			block.Arguments = bedrockToolArguments(partial)
		}
		block.Index = nil
		block.PartialJSON = nil
		processor.stopped = !processor.sink(ai.ToolCallEndEvent{ContentIndex: position, ToolCall: block, Partial: processor.output})
	}
}

func bedrockToolArguments(value string) map[string]any {
	parsed := partialjson.ParseStreamingJSON(value)
	if object, ok := parsed.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

func mapBedrockStopReason(reason string) (ai.StopReason, *string) {
	switch reason {
	case "end_turn", "stop_sequence":
		return ai.StopReasonStop, nil
	case "max_tokens", "model_context_window_exceeded":
		return ai.StopReasonLength, nil
	case "tool_use":
		return ai.StopReasonToolUse, nil
	default:
		if reason == "" {
			return ai.StopReasonError, nil
		}
		return ai.StopReasonError, &reason
	}
}

func clearBedrockStreamingFields(output *ai.AssistantMessage) {
	for _, raw := range output.Content {
		switch block := raw.(type) {
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

var bedrockErrorPrefixes = map[string]string{
	"InternalServerException":     "Internal server error",
	"ModelStreamErrorException":   "Model stream error",
	"ValidationException":         "Validation error",
	"ThrottlingException":         "Throttling error",
	"ServiceUnavailableException": "Service unavailable",
}

func formatBedrockError(err error) string {
	if err == nil {
		return "An unknown error occurred"
	}
	core := err.Error()
	code := ""
	var apiError interface {
		error
		ErrorCode() string
		ErrorMessage() string
	}
	if errors.As(err, &apiError) {
		code = apiError.ErrorCode()
		if apiError.ErrorMessage() != "" {
			core = apiError.ErrorMessage()
		}
	}
	var capturedError *bedrockHTTPResponseError
	if errors.As(err, &capturedError) && !bedrockErrorCarriesBody(core, capturedError.body) {
		core = fmt.Sprintf("%d: %s", capturedError.status, capturedError.body)
	} else {
		var responseError *smithyhttp.ResponseError
		if errors.As(err, &responseError) && responseError.Response != nil && responseError.Response.Response != nil {
			if body := readBedrockErrorBody(responseError.Response.Body); body != "" && !bedrockErrorCarriesBody(core, body) {
				core = fmt.Sprintf("%d: %s", responseError.Response.StatusCode, body)
			}
		}
	}
	if strings.Contains(strings.ToLower(core), "data retention mode") {
		core += " See " + bedrockDataRetentionDocsURL + " for supported data retention modes."
	}
	if code != "" {
		prefix := bedrockErrorPrefixes[code]
		if prefix == "" {
			prefix = code
		}
		return prefix + ": " + core
	}
	return core
}

func bedrockErrorCarriesBody(message, body string) bool {
	if body == "" || strings.Contains(message, body) {
		return true
	}
	var value map[string]any
	if json.Unmarshal([]byte(body), &value) == nil {
		for _, name := range []string{"message", "Message", "error"} {
			if text, ok := value[name].(string); ok && text != "" && strings.Contains(message, text) {
				return true
			}
		}
	}
	return false
}

func readBedrockErrorBody(body io.ReadCloser) string {
	if body == nil {
		return ""
	}
	contents, err := io.ReadAll(io.LimitReader(body, maxProviderErrorBodyChars+1))
	if err != nil {
		return ""
	}
	return truncateOpenAIErrorText(strings.TrimSpace(string(contents)))
}

type awsBedrockTransport struct {
	client       *bedrockruntime.Client
	options      []func(*bedrockruntime.Options)
	errorCapture *bedrockErrorCapture
}

func newAWSBedrockTransport(
	ctx context.Context,
	model *ai.Model,
	options *BedrockConverseStreamOptions,
) (bedrockTransport, error) {
	region := resolveBedrockRegion(model, options)
	loadOptions := make([]func(*awsconfig.LoadOptions) error, 0, 4)
	if region != "" {
		loadOptions = append(loadOptions, awsconfig.WithRegion(region))
	}
	profile := bedrockOptionProfile(options)
	if profile != "" {
		loadOptions = append(loadOptions, awsconfig.WithSharedConfigProfile(profile))
	}
	skipAuth := providerEnvValue("AWS_BEDROCK_SKIP_AUTH", bedrockStreamOptions(options)) == "1"
	if skipAuth {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("dummy-access-key", "dummy-secret-key", "")))
	} else if accessKey, secretKey, sessionToken, ok := configuredBedrockCredentials(options); ok {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, sessionToken)))
	}
	bearerToken := configuredBedrockBearerToken(options)
	if bearerToken != "" && !skipAuth {
		loadOptions = append(loadOptions, awsconfig.WithBearerAuthTokenProvider(bearer.StaticTokenProvider{Token: bearer.Token{Value: bearerToken}}))
	}
	proxyURL, err := resolveBedrockHTTPProxy(model.BaseURL, bedrockStreamOptions(options))
	if err != nil {
		return nil, err
	}
	forceHTTP1 := providerEnvValue("AWS_BEDROCK_FORCE_HTTP1", bedrockStreamOptions(options)) == "1"
	if proxyURL != nil || forceHTTP1 {
		client := awshttp.NewBuildableClient().WithTransportOptions(func(transport *http.Transport) {
			if proxyURL != nil {
				transport.Proxy = http.ProxyURL(proxyURL)
			}
			if forceHTTP1 {
				transport.ForceAttemptHTTP2 = false
			}
		})
		loadOptions = append(loadOptions, awsconfig.WithHTTPClient(client))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, err
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = awshttp.NewBuildableClient()
	}
	if bedrockHTTPClientOverride != nil {
		cfg.HTTPClient = bedrockHTTPClientOverride
	}
	errorCapture := &bedrockErrorCapture{}
	cfg.HTTPClient = &bedrockCapturingHTTPClient{next: cfg.HTTPClient, capture: errorCapture}
	explicitEndpoint := shouldUseExplicitBedrockEndpoint(model.BaseURL, configuredBedrockRegion(options), os.Getenv("AWS_PROFILE") != "")
	client := bedrockruntime.NewFromConfig(cfg, func(clientOptions *bedrockruntime.Options) {
		if explicitEndpoint {
			clientOptions.BaseEndpoint = aws.String(model.BaseURL)
		}
		if bearerToken != "" && !skipAuth {
			clientOptions.BearerAuthTokenProvider = bearer.StaticTokenProvider{Token: bearer.Token{Value: bearerToken}}
			clientOptions.AuthSchemePreference = []string{"httpBearerAuth"}
		}
		if skipAuth {
			clientOptions.BearerAuthTokenProvider = nil
			clientOptions.AuthSchemePreference = nil
		}
	})
	operationOptions := []func(*bedrockruntime.Options){func(operation *bedrockruntime.Options) {
		operation.APIOptions = append(operation.APIOptions, awsmiddleware.AddRawResponseToMetadata)
		for name, value := range bedrockStreamOptions(options).Headers {
			if value == nil || isReservedBedrockHeader(name) {
				continue
			}
			operation.APIOptions = append(operation.APIOptions, smithyhttp.SetHeaderValue(name, *value))
		}
	}}
	return &awsBedrockTransport{client: client, options: operationOptions, errorCapture: errorCapture}, nil
}

func configuredBedrockRegion(options *BedrockConverseStreamOptions) string {
	if options != nil && options.Region != "" {
		return options.Region
	}
	if value := providerEnvValue("AWS_REGION", bedrockStreamOptions(options)); value != "" {
		return value
	}
	return providerEnvValue("AWS_DEFAULT_REGION", bedrockStreamOptions(options))
}

func resolveBedrockRegion(model *ai.Model, options *BedrockConverseStreamOptions) string {
	if region := bedrockARNRegion(model.ID); region != "" {
		return region
	}
	if region := configuredBedrockRegion(options); region != "" {
		return region
	}
	if os.Getenv("AWS_PROFILE") == "" {
		if region := standardBedrockEndpointRegion(model.BaseURL); region != "" {
			return region
		}
		return "us-east-1"
	}
	return ""
}

func bedrockOptionProfile(options *BedrockConverseStreamOptions) string {
	if options != nil && options.Profile != "" {
		return options.Profile
	}
	return providerEnvValue("AWS_PROFILE", bedrockStreamOptions(options))
}

func configuredBedrockCredentials(options *BedrockConverseStreamOptions) (string, string, string, bool) {
	streamOptions := bedrockStreamOptions(options)
	accessKey := providerEnvValue("AWS_ACCESS_KEY_ID", streamOptions)
	secretKey := providerEnvValue("AWS_SECRET_ACCESS_KEY", streamOptions)
	if accessKey == "" || secretKey == "" {
		return "", "", "", false
	}
	return accessKey, secretKey, providerEnvValue("AWS_SESSION_TOKEN", streamOptions), true
}

func configuredBedrockBearerToken(options *BedrockConverseStreamOptions) string {
	if options != nil {
		if options.BearerToken != "" {
			return options.BearerToken
		}
		if options.APIKey != nil && *options.APIKey != "" {
			return *options.APIKey
		}
	}
	return providerEnvValue("AWS_BEARER_TOKEN_BEDROCK", bedrockStreamOptions(options))
}

func standardBedrockEndpointRegion(baseURL string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	match := bedrockStandardEndpoint.FindStringSubmatch(strings.ToLower(parsed.Hostname()))
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func shouldUseExplicitBedrockEndpoint(baseURL, configuredRegion string, hasAmbientProfile bool) bool {
	if standardBedrockEndpointRegion(baseURL) == "" {
		return true
	}
	return configuredRegion == "" && !hasAmbientProfile
}

func bedrockARNRegion(modelID string) string {
	match := bedrockARNRegionPattern.FindStringSubmatch(modelID)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func isReservedBedrockHeader(name string) bool {
	lower := strings.ToLower(name)
	return lower == "authorization" || lower == "host" || strings.HasPrefix(lower, "x-amz-")
}

func resolveBedrockHTTPProxy(target string, options *ai.StreamOptions) (*url.URL, error) {
	targetURL, err := url.Parse(target)
	if err != nil || targetURL.Scheme == "" || targetURL.Host == "" {
		return nil, nil
	}
	port := targetURL.Port()
	if port == "" {
		switch targetURL.Scheme {
		case "http", "ws":
			port = "80"
		case "https", "wss":
			port = "443"
		}
	}
	if !bedrockShouldProxy(targetURL.Hostname(), port, bedrockProxyEnv("no_proxy", options)) {
		return nil, nil
	}
	value := bedrockProxyEnv(targetURL.Scheme+"_proxy", options)
	if value == "" {
		value = bedrockProxyEnv("all_proxy", options)
	}
	if value == "" {
		return nil, nil
	}
	if !strings.Contains(value, "://") {
		value = targetURL.Scheme + "://" + value
	}
	proxyURL, err := url.Parse(value)
	if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
		if err == nil {
			err = errors.New("missing protocol or host")
		}
		return nil, fmt.Errorf("Invalid proxy URL %q: %v", value, err) //nolint:staticcheck // Exact upstream prefix is observable.
	}
	if proxyURL.Scheme != "http" && proxyURL.Scheme != "https" {
		return nil, fmt.Errorf("Unsupported proxy protocol. SOCKS and PAC proxy URLs are not supported; use an HTTP or HTTPS proxy URL. Got %s:", proxyURL.Scheme) //nolint:staticcheck // Exact upstream text is observable.
	}
	return proxyURL, nil
}

func bedrockProxyEnv(name string, options *ai.StreamOptions) string {
	lower, upper := strings.ToLower(name), strings.ToUpper(name)
	if options != nil {
		if value := options.Env[lower]; value != "" {
			return value
		}
		if value := options.Env[upper]; value != "" {
			return value
		}
	}
	if value := os.Getenv(lower); value != "" {
		return value
	}
	return os.Getenv(upper)
}

func bedrockShouldProxy(hostname, targetPort, noProxy string) bool {
	noProxy = strings.ToLower(noProxy)
	if noProxy == "" {
		return true
	}
	if noProxy == "*" {
		return false
	}
	hostname = strings.ToLower(hostname)
	entries := strings.FieldsFunc(noProxy, func(char rune) bool { return char == ',' || unicode.IsSpace(char) })
	for _, entry := range entries {
		proxyHost, proxyPort := entry, ""
		if separator := strings.LastIndex(entry, ":"); separator > 0 {
			if _, err := strconv.Atoi(entry[separator+1:]); err == nil {
				proxyHost, proxyPort = entry[:separator], entry[separator+1:]
			}
		}
		if proxyPort != "" && proxyPort != targetPort {
			continue
		}
		wildcard := strings.HasPrefix(proxyHost, "*")
		if wildcard {
			proxyHost = strings.TrimPrefix(proxyHost, "*")
		}
		if wildcard || strings.HasPrefix(proxyHost, ".") {
			if strings.HasSuffix(hostname, proxyHost) {
				return false
			}
		} else if hostname == proxyHost {
			return false
		}
	}
	return true
}

func (transport *awsBedrockTransport) Send(ctx context.Context, payload *BedrockConverseStreamPayload) (bedrockResponse, error) {
	input, err := bedrockSDKInput(payload)
	if err != nil {
		return nil, err
	}
	output, err := transport.client.ConverseStream(ctx, input, transport.options...)
	if err != nil {
		if status, body := transport.errorCapture.snapshot(); body != "" {
			err = &bedrockHTTPResponseError{err: err, status: status, body: body}
		}
		return nil, err
	}
	stream := output.GetStream()
	if stream == nil {
		return nil, errors.New("Bedrock ConverseStream returned no stream") //nolint:staticcheck // Provider error text.
	}
	status := http.StatusOK
	if raw, ok := awsmiddleware.GetRawResponse(output.ResultMetadata).(*smithyhttp.Response); ok && raw != nil && raw.Response != nil {
		status = raw.StatusCode
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	return &awsBedrockResponse{stream: stream, status: status, requestID: requestID}, nil
}

type bedrockErrorCapture struct {
	mu     sync.Mutex
	status int
	body   []byte
}

func (capture *bedrockErrorCapture) reset(status int) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.status = status
	capture.body = capture.body[:0]
}

func (capture *bedrockErrorCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	remaining := maxProviderErrorBodyChars + 1 - len(capture.body)
	if remaining > 0 {
		capture.body = append(capture.body, data[:min(len(data), remaining)]...)
	}
	return len(data), nil
}

func (capture *bedrockErrorCapture) snapshot() (int, string) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.status, truncateOpenAIErrorText(strings.TrimSpace(string(capture.body)))
}

type bedrockCapturingHTTPClient struct {
	next    aws.HTTPClient
	capture *bedrockErrorCapture
}

func (client *bedrockCapturingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	response, err := client.next.Do(request)
	if response != nil && response.StatusCode >= http.StatusBadRequest && response.Body != nil {
		client.capture.reset(response.StatusCode)
		response.Body = struct {
			io.Reader
			io.Closer
		}{Reader: io.TeeReader(response.Body, client.capture), Closer: response.Body}
	}
	return response, err
}

type bedrockHTTPResponseError struct {
	err    error
	status int
	body   string
}

func (err *bedrockHTTPResponseError) Error() string { return err.err.Error() }
func (err *bedrockHTTPResponseError) Unwrap() error { return err.err }

func bedrockSDKInput(payload *BedrockConverseStreamPayload) (*bedrockruntime.ConverseStreamInput, error) {
	if payload == nil {
		return nil, errors.New("Bedrock payload is nil") //nolint:staticcheck // Hook-facing error.
	}
	input := &bedrockruntime.ConverseStreamInput{
		ModelId:         aws.String(payload.ModelID),
		RequestMetadata: payload.RequestMetadata,
	}
	input.InferenceConfig = &bedrocktypes.InferenceConfiguration{}
	if payload.InferenceConfig.MaxTokens != nil {
		value := *payload.InferenceConfig.MaxTokens
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < math.MinInt32 || value > math.MaxInt32 {
			return nil, fmt.Errorf("bedrock maxTokens %g is not an SDK int32 value", value)
		}
		input.InferenceConfig.MaxTokens = aws.Int32(int32(value))
	}
	if payload.InferenceConfig.Temperature != nil {
		input.InferenceConfig.Temperature = aws.Float32(float32(*payload.InferenceConfig.Temperature))
	}
	if payload.InferenceConfig.TopP != nil {
		input.InferenceConfig.TopP = aws.Float32(float32(*payload.InferenceConfig.TopP))
	}
	if payload.InferenceConfig.StopSequences != nil {
		input.InferenceConfig.StopSequences = payload.InferenceConfig.StopSequences
	}
	if payload.AdditionalModelRequestFields != nil {
		input.AdditionalModelRequestFields = bedrockdocument.NewLazyDocument(payload.AdditionalModelRequestFields)
	}
	for _, system := range payload.System {
		switch {
		case system.Text != nil:
			input.System = append(input.System, &bedrocktypes.SystemContentBlockMemberText{Value: *system.Text})
		case system.CachePoint != nil:
			input.System = append(input.System, &bedrocktypes.SystemContentBlockMemberCachePoint{Value: bedrockSDKCachePoint(system.CachePoint)})
		}
	}
	for _, message := range payload.Messages {
		converted := bedrocktypes.Message{Role: bedrocktypes.ConversationRole(message.Role)}
		for _, block := range message.Content {
			value, err := bedrockSDKContentBlock(block)
			if err != nil {
				return nil, err
			}
			if value != nil {
				converted.Content = append(converted.Content, value)
			}
		}
		input.Messages = append(input.Messages, converted)
	}
	if payload.ToolConfig != nil {
		configuration := &bedrocktypes.ToolConfiguration{}
		for _, tool := range payload.ToolConfig.Tools {
			var schema any
			if err := json.Unmarshal(tool.ToolSpec.InputSchema.JSON, &schema); err != nil {
				return nil, fmt.Errorf("decode Bedrock tool schema %q: %w", tool.ToolSpec.Name, err)
			}
			configuration.Tools = append(configuration.Tools, &bedrocktypes.ToolMemberToolSpec{Value: bedrocktypes.ToolSpecification{
				Name: aws.String(tool.ToolSpec.Name), Description: aws.String(tool.ToolSpec.Description),
				InputSchema: &bedrocktypes.ToolInputSchemaMemberJson{Value: bedrockdocument.NewLazyDocument(schema)},
			}})
		}
		configuration.ToolChoice = bedrockSDKToolChoice(payload.ToolConfig.ToolChoice)
		input.ToolConfig = configuration
	}
	if err := applyBedrockPayloadExtras(input, payload.Extra); err != nil {
		return nil, err
	}
	return input, nil
}

// applyBedrockPayloadExtras merges hook-injected top-level members back into
// the SDK input; upstream passes the hook return verbatim to
// ConverseStreamCommand, which serializes every modeled member. (OT-M7)
func applyBedrockPayloadExtras(input *bedrockruntime.ConverseStreamInput, extras map[string]json.RawMessage) error {
	for name, raw := range extras {
		switch name {
		case "guardrailConfig":
			config := &bedrocktypes.GuardrailStreamConfiguration{}
			if err := json.Unmarshal(raw, config); err != nil {
				return fmt.Errorf("decode Bedrock guardrailConfig: %w", err)
			}
			input.GuardrailConfig = config
		case "performanceConfig":
			config := &bedrocktypes.PerformanceConfiguration{}
			if err := json.Unmarshal(raw, config); err != nil {
				return fmt.Errorf("decode Bedrock performanceConfig: %w", err)
			}
			input.PerformanceConfig = config
		case "additionalModelResponseFieldPaths":
			var paths []string
			if err := json.Unmarshal(raw, &paths); err != nil {
				return fmt.Errorf("decode Bedrock additionalModelResponseFieldPaths: %w", err)
			}
			input.AdditionalModelResponseFieldPaths = paths
		case "promptVariables":
			var values map[string]struct {
				Text *string `json:"text"`
			}
			if err := json.Unmarshal(raw, &values); err != nil {
				return fmt.Errorf("decode Bedrock promptVariables: %w", err)
			}
			variables := make(map[string]bedrocktypes.PromptVariableValues, len(values))
			for name, value := range values {
				if value.Text != nil {
					variables[name] = &bedrocktypes.PromptVariableValuesMemberText{Value: *value.Text}
				}
			}
			input.PromptVariables = variables
		case "serviceTier":
			config := &bedrocktypes.ServiceTier{}
			if err := json.Unmarshal(raw, config); err != nil {
				return fmt.Errorf("decode Bedrock serviceTier: %w", err)
			}
			input.ServiceTier = config
		case "outputConfig":
			config, err := decodeBedrockOutputConfig(raw)
			if err != nil {
				return err
			}
			input.OutputConfig = config
		default:
			// Members the SDK does not model are dropped at serialization
			// upstream as well.
		}
	}
	return nil
}

func decodeBedrockOutputConfig(raw json.RawMessage) (*bedrocktypes.OutputConfig, error) {
	var wire struct {
		TextFormat *struct {
			Type      string `json:"type"`
			Structure struct {
				JSONSchema *struct {
					Schema      *string `json:"schema"`
					Name        *string `json:"name"`
					Description *string `json:"description"`
				} `json:"jsonSchema"`
			} `json:"structure"`
		} `json:"textFormat"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("decode Bedrock outputConfig: %w", err)
	}
	config := &bedrocktypes.OutputConfig{}
	if wire.TextFormat == nil {
		return config, nil
	}
	format := &bedrocktypes.OutputFormat{Type: bedrocktypes.OutputFormatType(wire.TextFormat.Type)}
	if schema := wire.TextFormat.Structure.JSONSchema; schema != nil {
		format.Structure = &bedrocktypes.OutputFormatStructureMemberJsonSchema{Value: bedrocktypes.JsonSchemaDefinition{
			Schema: schema.Schema, Name: schema.Name, Description: schema.Description,
		}}
	}
	config.TextFormat = format
	return config, nil
}

func bedrockSDKContentBlock(block BedrockContentBlock) (bedrocktypes.ContentBlock, error) {
	switch {
	case block.Text != nil:
		return &bedrocktypes.ContentBlockMemberText{Value: *block.Text}, nil
	case block.Image != nil:
		image, err := bedrockSDKImageBlock(block.Image)
		if err != nil {
			return nil, err
		}
		return &bedrocktypes.ContentBlockMemberImage{Value: image}, nil
	case block.ToolUse != nil:
		return &bedrocktypes.ContentBlockMemberToolUse{Value: bedrocktypes.ToolUseBlock{
			ToolUseId: aws.String(block.ToolUse.ToolUseID), Name: aws.String(block.ToolUse.Name),
			Input: bedrockdocument.NewLazyDocument(block.ToolUse.Input),
		}}, nil
	case block.ToolResult != nil:
		result := bedrocktypes.ToolResultBlock{ToolUseId: aws.String(block.ToolResult.ToolUseID), Status: bedrocktypes.ToolResultStatus(block.ToolResult.Status)}
		for _, content := range block.ToolResult.Content {
			switch {
			case content.Text != nil:
				result.Content = append(result.Content, &bedrocktypes.ToolResultContentBlockMemberText{Value: *content.Text})
			case content.Image != nil:
				image, err := bedrockSDKImageBlock(content.Image)
				if err != nil {
					return nil, err
				}
				result.Content = append(result.Content, &bedrocktypes.ToolResultContentBlockMemberImage{Value: image})
			}
		}
		return &bedrocktypes.ContentBlockMemberToolResult{Value: result}, nil
	case block.ReasoningContent != nil:
		text := block.ReasoningContent.ReasoningText.Text
		return &bedrocktypes.ContentBlockMemberReasoningContent{Value: &bedrocktypes.ReasoningContentBlockMemberReasoningText{Value: bedrocktypes.ReasoningTextBlock{
			Text: &text, Signature: block.ReasoningContent.ReasoningText.Signature,
		}}}, nil
	case block.CachePoint != nil:
		return &bedrocktypes.ContentBlockMemberCachePoint{Value: bedrockSDKCachePoint(block.CachePoint)}, nil
	default:
		return nil, nil
	}
}

func bedrockSDKImageBlock(image *BedrockImageBlock) (bedrocktypes.ImageBlock, error) {
	bytes, err := base64.StdEncoding.DecodeString(image.Source.Bytes)
	if err != nil {
		return bedrocktypes.ImageBlock{}, err
	}
	return bedrocktypes.ImageBlock{
		Format: bedrocktypes.ImageFormat(image.Format), Source: &bedrocktypes.ImageSourceMemberBytes{Value: bytes},
	}, nil
}

func bedrockSDKCachePoint(point *BedrockCachePoint) bedrocktypes.CachePointBlock {
	result := bedrocktypes.CachePointBlock{Type: bedrocktypes.CachePointType(point.Type)}
	if point.TTL != nil {
		result.Ttl = bedrocktypes.CacheTTL(*point.TTL)
	}
	return result
}

func bedrockSDKToolChoice(value any) bedrocktypes.ToolChoice {
	choice, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if _, ok := choice["auto"]; ok {
		return &bedrocktypes.ToolChoiceMemberAuto{}
	}
	if _, ok := choice["any"]; ok {
		return &bedrocktypes.ToolChoiceMemberAny{}
	}
	if raw, ok := choice["tool"].(map[string]any); ok {
		if name, ok := raw["name"].(string); ok {
			return &bedrocktypes.ToolChoiceMemberTool{Value: bedrocktypes.SpecificToolChoice{Name: &name}}
		}
	}
	return nil
}

type awsBedrockResponse struct {
	stream    *bedrockruntime.ConverseStreamEventStream
	status    int
	requestID string
}

func (response *awsBedrockResponse) Status() int       { return response.status }
func (response *awsBedrockResponse) RequestID() string { return response.requestID }
func (response *awsBedrockResponse) Close() error      { return response.stream.Close() }
func (response *awsBedrockResponse) Err() error        { return response.stream.Err() }

func (response *awsBedrockResponse) Next(ctx context.Context) (bedrockStreamItem, bool) {
	select {
	case <-ctx.Done():
		return bedrockStreamItem{}, false
	case event, ok := <-response.stream.Events():
		if !ok {
			return bedrockStreamItem{}, false
		}
		return convertBedrockSDKEvent(event), true
	}
}

func convertBedrockSDKEvent(event bedrocktypes.ConverseStreamOutput) bedrockStreamItem {
	switch value := event.(type) {
	case *bedrocktypes.ConverseStreamOutputMemberMessageStart:
		return bedrockStreamItem{Kind: bedrockItemMessageStart, Role: string(value.Value.Role)}
	case *bedrocktypes.ConverseStreamOutputMemberContentBlockStart:
		item := bedrockStreamItem{Kind: bedrockItemContentStart, ContentBlockIndex: int(aws.ToInt32(value.Value.ContentBlockIndex))}
		if start, ok := value.Value.Start.(*bedrocktypes.ContentBlockStartMemberToolUse); ok {
			item.ToolUseID = aws.ToString(start.Value.ToolUseId)
			item.ToolName = aws.ToString(start.Value.Name)
		}
		return item
	case *bedrocktypes.ConverseStreamOutputMemberContentBlockDelta:
		item := bedrockStreamItem{Kind: bedrockItemContentDelta, ContentBlockIndex: int(aws.ToInt32(value.Value.ContentBlockIndex))}
		switch delta := value.Value.Delta.(type) {
		case *bedrocktypes.ContentBlockDeltaMemberText:
			item.Text = &delta.Value
		case *bedrocktypes.ContentBlockDeltaMemberToolUse:
			item.ToolInput = delta.Value.Input
		case *bedrocktypes.ContentBlockDeltaMemberReasoningContent:
			switch reasoning := delta.Value.(type) {
			case *bedrocktypes.ReasoningContentBlockDeltaMemberText:
				item.ReasoningText = &reasoning.Value
			case *bedrocktypes.ReasoningContentBlockDeltaMemberSignature:
				item.ReasoningSignature = &reasoning.Value
			}
		}
		return item
	case *bedrocktypes.ConverseStreamOutputMemberContentBlockStop:
		return bedrockStreamItem{Kind: bedrockItemContentStop, ContentBlockIndex: int(aws.ToInt32(value.Value.ContentBlockIndex))}
	case *bedrocktypes.ConverseStreamOutputMemberMessageStop:
		return bedrockStreamItem{Kind: bedrockItemMessageStop, StopReason: string(value.Value.StopReason)}
	case *bedrocktypes.ConverseStreamOutputMemberMetadata:
		item := bedrockStreamItem{Kind: bedrockItemMetadata}
		if value.Value.Usage != nil {
			item.InputTokens = int64(aws.ToInt32(value.Value.Usage.InputTokens))
			item.OutputTokens = int64(aws.ToInt32(value.Value.Usage.OutputTokens))
			item.CacheReadTokens = int64(aws.ToInt32(value.Value.Usage.CacheReadInputTokens))
			item.CacheWriteTokens = int64(aws.ToInt32(value.Value.Usage.CacheWriteInputTokens))
			item.TotalTokens = int64(aws.ToInt32(value.Value.Usage.TotalTokens))
		}
		return item
	default:
		return bedrockStreamItem{}
	}
}
