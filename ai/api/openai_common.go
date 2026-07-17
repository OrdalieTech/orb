package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/OrdalieTech/pi-go/ai"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

const (
	openAIPromptCacheKeyMaxLength = 64
	maxProviderErrorBodyChars     = 4000
)

var (
	errStopSSE                           = errors.New("ai/api: stop SSE stream")
	openAIHTTPClient   option.HTTPClient = http.DefaultClient
	openAINowUnixMilli                   = func() int64 { return time.Now().UnixMilli() }
)

type eventSink func(ai.AssistantMessageEvent) bool

func newAssistantMessage(model *ai.Model) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content:    ai.AssistantContent{},
		API:        model.API,
		Provider:   model.Provider,
		Model:      model.ID,
		Usage:      zeroUsage(),
		StopReason: ai.StopReasonStop,
		Timestamp:  openAINowUnixMilli(),
	}
}

func zeroUsage() ai.Usage {
	return ai.Usage{Cost: ai.Cost{}}
}

func resolveCacheRetention(options *ai.StreamOptions) ai.CacheRetention {
	if options != nil && options.CacheRetention != nil {
		return *options.CacheRetention
	}
	if providerEnvValue("PI_CACHE_RETENTION", options) == "long" {
		return ai.CacheRetentionLong
	}
	return ai.CacheRetentionShort
}

func providerEnvValue(name string, options *ai.StreamOptions) string {
	if options != nil {
		if value := options.Env[name]; value != "" {
			return value
		}
	}
	return os.Getenv(name)
}

func clampOpenAIPromptCacheKey(value *string) any {
	if value == nil {
		return nil
	}
	runes := []rune(*value)
	if len(runes) > openAIPromptCacheKeyMaxLength {
		runes = runes[:openAIPromptCacheKeyMaxLength]
	}
	return string(runes)
}

func modelSupportsImage(model *ai.Model) bool {
	for _, modality := range model.Input {
		if modality == ai.InputImage {
			return true
		}
	}
	return false
}

func sanitizeText(value string) string {
	if utf8.ValidString(value) {
		return value
	}
	return strings.ToValidUTF8(value, "")
}

func decodeCompat[T any](model *ai.Model) (T, error) {
	var compat T
	if len(model.Compat) == 0 || string(model.Compat) == "null" {
		return compat, nil
	}
	if err := json.Unmarshal(model.Compat, &compat); err != nil {
		return compat, fmt.Errorf("decode %s compat: %w", model.Provider, err)
	}
	return compat, nil
}

func copyModelHeaders(model *ai.Model) http.Header {
	headers := make(http.Header)
	if model.Headers == nil {
		return headers
	}
	for name, value := range *model.Headers {
		headers.Set(name, value)
	}
	return headers
}

func mergeProviderHeaders(headers http.Header, values ai.ProviderHeaders) {
	for name, value := range values {
		if value == nil {
			headers.Del(name)
			continue
		}
		headers.Set(name, *value)
	}
}

func addCopilotHeaders(headers http.Header, model *ai.Model, requestContext ai.Context) {
	if model.Provider != "github-copilot" {
		return
	}
	initiator := "user"
	if length := len(requestContext.Messages); length > 0 {
		if _, ok := requestContext.Messages[length-1].(*ai.UserMessage); !ok {
			initiator = "agent"
		}
	}
	headers.Set("X-Initiator", initiator)
	headers.Set("Openai-Intent", "conversation-edits")
	if contextHasImages(requestContext.Messages) {
		headers.Set("Copilot-Vision-Request", "true")
	}
}

func contextHasImages(messages ai.MessageList) bool {
	for _, message := range messages {
		switch value := message.(type) {
		case *ai.UserMessage:
			for _, block := range value.Content.Blocks {
				if _, ok := block.(*ai.ImageContent); ok {
					return true
				}
			}
		case *ai.ToolResultMessage:
			for _, block := range value.Content {
				if _, ok := block.(*ai.ImageContent); ok {
					return true
				}
			}
		}
	}
	return false
}

func hasUsableHeader(headers ai.ProviderHeaders, name string) bool {
	for key, value := range headers {
		if strings.EqualFold(key, name) && value != nil && strings.TrimSpace(*value) != "" {
			return true
		}
	}
	return false
}

func resolveOpenAIAPIKey(model *ai.Model, options *ai.StreamOptions) (string, error) {
	if options != nil && options.APIKey != nil && *options.APIKey != "" {
		return *options.APIKey, nil
	}
	if options != nil && (hasUsableHeader(options.Headers, "authorization") || hasUsableHeader(options.Headers, "cf-aig-authorization")) {
		return "unused", nil
	}
	if model.Provider == "openai" {
		if key := providerEnvValue("OPENAI_API_KEY", options); key != "" {
			return key, nil
		}
	}
	return "", fmt.Errorf("No API key for provider: %s", model.Provider) //nolint:staticcheck // Exact upstream error text is observable.
}

func applyPayloadHook(ctx context.Context, model *ai.Model, options *ai.StreamOptions, payload any) (any, error) {
	if options == nil || options.OnPayload == nil {
		return payload, nil
	}
	replacement, replace, err := options.OnPayload(ctx, payload, model)
	if err != nil {
		return nil, err
	}
	if replace {
		return replacement, nil
	}
	return payload, nil
}

func postOpenAIStream(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.StreamOptions,
	path string,
	payload any,
	headers http.Header,
) (*http.Response, error) {
	apiKey, err := resolveOpenAIAPIKey(model, options)
	if err != nil {
		return nil, err
	}
	body, err := ai.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI request: %w", err)
	}

	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(model.BaseURL),
		option.WithHTTPClient(openAIHTTPClient),
	)
	requestOptions := []option.RequestOption{option.WithMaxRetries(0)}
	if options != nil {
		if options.MaxRetries != nil {
			requestOptions[0] = option.WithMaxRetries(*options.MaxRetries)
		}
		if options.TimeoutMS != nil {
			requestOptions = append(requestOptions, option.WithRequestTimeout(time.Duration(*options.TimeoutMS)*time.Millisecond))
		}
	}
	for name, values := range headers {
		if len(values) == 0 {
			requestOptions = append(requestOptions, option.WithHeaderDel(name))
			continue
		}
		requestOptions = append(requestOptions, option.WithHeader(name, values[len(values)-1]))
	}

	var response *http.Response
	if err := client.Post(ctx, path, json.RawMessage(body), &response, requestOptions...); err != nil {
		return response, normalizeOpenAIRequestError(response, err)
	}
	if response == nil {
		return nil, errors.New("OpenAI API returned no HTTP response")
	}
	if options != nil && options.OnResponse != nil {
		if err := options.OnResponse(ctx, providerResponse(response), model); err != nil {
			_ = response.Body.Close()
			return nil, err
		}
	}
	return response, nil
}

func providerResponse(response *http.Response) ai.ProviderResponse {
	headers := make(map[string]string, len(response.Header))
	for name, values := range response.Header {
		headers[name] = strings.Join(values, ", ")
	}
	return ai.ProviderResponse{Status: response.StatusCode, Headers: headers}
}

func readSSE(body io.Reader, handle func(json.RawMessage) error) error {
	closer, ok := body.(io.ReadCloser)
	if !ok {
		closer = io.NopCloser(body)
	}
	response := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   closer,
	}
	stream := ssestream.NewStream[json.RawMessage](ssestream.NewDecoder(response), nil)
	for stream.Next() {
		if err := handle(stream.Current()); err != nil {
			return err
		}
	}
	return stream.Err()
}

func calculateCost(model *ai.Model, usage *ai.Usage) {
	inputTokens := usage.Input + usage.CacheRead + usage.CacheWrite
	rates := model.Cost.ModelCostRates
	matchedThreshold := float64(-1)
	if model.Cost.Tiers != nil {
		for _, tier := range *model.Cost.Tiers {
			if float64(inputTokens) > tier.InputTokensAbove && tier.InputTokensAbove > matchedThreshold {
				rates = tier.ModelCostRates
				matchedThreshold = tier.InputTokensAbove
			}
		}
	}
	longWrite := int64(0)
	if usage.CacheWrite1h != nil {
		longWrite = *usage.CacheWrite1h
	}
	shortWrite := usage.CacheWrite - longWrite
	usage.Cost.Input = rates.Input / 1_000_000 * float64(usage.Input)
	usage.Cost.Output = rates.Output / 1_000_000 * float64(usage.Output)
	usage.Cost.CacheRead = rates.CacheRead / 1_000_000 * float64(usage.CacheRead)
	usage.Cost.CacheWrite = (rates.CacheWrite*float64(shortWrite) + rates.Input*2*float64(longWrite)) / 1_000_000
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
}

func formatOpenAIError(err error, prefix string) string {
	var statusError *openAIStatusError
	if errors.As(err, &statusError) {
		if statusError.body != "" && !strings.Contains(statusError.message, statusError.body) {
			if prefix != "" {
				return fmt.Sprintf("%s (%d): %s", prefix, statusError.status, statusError.body)
			}
			return fmt.Sprintf("%d: %s", statusError.status, statusError.body)
		}
		if prefix != "" {
			return fmt.Sprintf("%s (%d): %s", prefix, statusError.status, statusError.message)
		}
		return statusError.message
	}
	var apiError *openai.Error
	if !errors.As(err, &apiError) {
		return err.Error()
	}
	body := extractOpenAIErrorBody(apiError.RawJSON())
	if body == "" {
		if prefix != "" {
			return fmt.Sprintf("%s (%d): %s", prefix, apiError.StatusCode, err)
		}
		return err.Error()
	}
	body = truncateOpenAIErrorText(body, maxProviderErrorBodyChars)
	if prefix != "" {
		return fmt.Sprintf("%s (%d): %s", prefix, apiError.StatusCode, body)
	}
	return fmt.Sprintf("%d: %s", apiError.StatusCode, body)
}

type openAIStatusError struct {
	status  int
	message string
	body    string
}

func (err *openAIStatusError) Error() string { return err.message }

func normalizeOpenAIRequestError(response *http.Response, err error) error {
	var apiError *openai.Error
	if response == nil || response.Body == nil || response.StatusCode < http.StatusBadRequest {
		return err
	}
	if errors.As(err, &apiError) && extractOpenAIErrorBody(apiError.RawJSON()) != "" {
		return err
	}
	contents, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return err
	}
	response.Body = io.NopCloser(bytes.NewReader(contents))
	return newOpenAIStatusError(response.StatusCode, contents)
}

func newOpenAIStatusError(status int, contents []byte) *openAIStatusError {
	statusOnly := fmt.Sprintf("%d status code (no body)", status)
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(contents, &envelope); err != nil {
		if len(contents) == 0 {
			return &openAIStatusError{status: status, message: statusOnly}
		}
		return &openAIStatusError{status: status, message: fmt.Sprintf("%d %s", status, contents)}
	}
	rawError, exists := envelope["error"]
	if !exists {
		return &openAIStatusError{status: status, message: statusOnly}
	}
	normalized, normalizeErr := ai.NormalizeJSONStringifyJSON(rawError)
	if normalizeErr != nil {
		return &openAIStatusError{status: status, message: statusOnly}
	}
	var value any
	if json.Unmarshal(normalized, &value) != nil || !openAIJSONTruthy(value) {
		return &openAIStatusError{status: status, message: statusOnly}
	}
	serialized := string(normalized)
	messageValue := ""
	if object, ok := value.(map[string]any); ok {
		if candidate, ok := object["message"]; ok && openAIJSONTruthy(candidate) {
			if text, ok := candidate.(string); ok {
				messageValue = text
			} else if encoded, encodeErr := ai.Marshal(candidate); encodeErr == nil {
				messageValue = string(encoded)
			}
		}
	}
	if messageValue == "" {
		messageValue = serialized
	}
	body := ""
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) > 0 {
			body = serialized
		}
	case []any:
		if len(typed) > 0 {
			body = serialized
		}
	}
	return &openAIStatusError{
		status:  status,
		message: fmt.Sprintf("%d %s", status, messageValue),
		body:    body,
	}
}

func openAIJSONTruthy(value any) bool {
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

func truncateOpenAIErrorText(text string, maxChars int) string {
	units := utf16.Encode([]rune(text))
	if len(units) <= maxChars {
		return text
	}
	prefixUnits := units[:maxChars]
	prefix := string(utf16.Decode(prefixUnits))
	if len(prefixUnits) > 0 && prefixUnits[len(prefixUnits)-1] >= 0xd800 && prefixUnits[len(prefixUnits)-1] <= 0xdbff {
		unit := prefixUnits[len(prefixUnits)-1]
		prefix = string(utf16.Decode(prefixUnits[:len(prefixUnits)-1])) + string([]byte{
			byte(0xe0 | unit>>12),
			byte(0x80 | unit>>6&0x3f),
			byte(0x80 | unit&0x3f),
		})
	}
	return fmt.Sprintf("%s... [truncated %d chars]", prefix, len(units)-maxChars)
}

func extractOpenAIErrorBody(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return ""
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(trimmed), &envelope) != nil {
		return trimmed
	}
	body, ok := envelope["error"]
	if !ok {
		body = json.RawMessage(trimmed)
	} else if string(body) == "{}" || string(body) == "null" {
		return ""
	}
	encoded, err := ai.NormalizeJSONStringifyJSON(body)
	if err != nil {
		return strings.TrimSpace(string(body))
	}
	return string(encoded)
}

func streamFailure(ctx context.Context, output *ai.AssistantMessage, err error, prefix string) ai.ErrorEvent {
	reason := ai.StopReasonError
	if ctx.Err() != nil {
		reason = ai.StopReasonAborted
	}
	output.StopReason = reason
	message := formatOpenAIError(err, prefix)
	output.ErrorMessage = &message
	return ai.ErrorEvent{Reason: reason, Error: output}
}
