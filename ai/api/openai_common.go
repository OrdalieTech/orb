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
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/OrdalieTech/pigo/ai"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

const (
	openAIPromptCacheKeyMaxLength = 64
	maxProviderErrorBodyChars     = 4000
)

var (
	errStopSSE                               = errors.New("ai/api: stop SSE stream")
	errOpenAIHeaderTimeout                   = errors.New("Request timed out.") //nolint:staticcheck // Exact upstream SDK error text is observable.
	openAIHTTPClient       option.HTTPClient = http.DefaultClient
	openAINowUnixMilli                       = func() int64 { return time.Now().UnixMilli() }
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
	return slices.Contains(model.Input, ai.InputImage)
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

func applyHeadersHook(
	ctx context.Context,
	model *ai.Model,
	options *ai.StreamOptions,
	headers http.Header,
) (http.Header, error) {
	if options == nil || options.TransformHeaders == nil {
		return headers, nil
	}
	values := make(ai.ProviderHeaders, len(headers))
	for name, entries := range headers {
		if len(entries) == 0 {
			values[name] = nil
			continue
		}
		value := strings.Join(entries, ", ")
		values[name] = &value
	}
	transformed, err := options.TransformHeaders(ctx, values, model)
	if err != nil {
		return nil, err
	}
	if transformed == nil {
		transformed = ai.ProviderHeaders{}
	}
	result := make(http.Header, len(transformed))
	for name, value := range transformed {
		if value != nil {
			result.Set(name, *value)
		}
	}
	return result, nil
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
	// Upstream getClientApiKey never consults the environment; env-based key
	// resolution lives in the higher provider registry/auth layer (OA-m2).
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
	httpClient, err := openAIHeaderTimeoutClient(openAIHTTPClient, streamTimeoutMS(options))
	if err != nil {
		return nil, err
	}

	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(model.BaseURL),
		option.WithHTTPClient(httpClient),
	)
	requestOptions := []option.RequestOption{option.WithMaxRetries(0)}
	if options != nil && options.MaxRetries != nil {
		requestOptions[0] = option.WithMaxRetries(*options.MaxRetries)
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

type openAIHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type openAIHeaderTimeoutDoer struct {
	base    openAIHTTPDoer
	timeout time.Duration
}

func streamTimeoutMS(options *ai.StreamOptions) *int64 {
	if options == nil {
		return nil
	}
	return options.TimeoutMS
}

func openAIHeaderTimeoutClient(base openAIHTTPDoer, timeoutMS *int64) (openAIHTTPDoer, error) {
	if timeoutMS == nil {
		return base, nil
	}
	if *timeoutMS < 0 {
		return nil, errors.New("timeout must be a positive integer")
	}
	return &openAIHeaderTimeoutDoer{base: base, timeout: time.Duration(*timeoutMS) * time.Millisecond}, nil
}

func (client *openAIHeaderTimeoutDoer) Do(request *http.Request) (*http.Response, error) {
	requestContext, cancel := context.WithCancel(request.Context())
	timedOut := make(chan struct{})
	timer := time.AfterFunc(client.timeout, func() {
		cancel()
		close(timedOut)
	})
	response, err := client.base.Do(request.Clone(requestContext))
	if !timer.Stop() {
		<-timedOut
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		cancel()
		return nil, errOpenAIHeaderTimeout
	}
	if err != nil || response == nil || response.Body == nil {
		cancel()
		return response, err
	}
	response.Body = &cancelOnCloseBody{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

// cancelOnCloseBody releases the request-scoped cancel context once the caller
// finishes reading the streamed body.
type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *cancelOnCloseBody) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

func providerResponse(response *http.Response) ai.ProviderResponse {
	headers := make(map[string]string, len(response.Header))
	for name, values := range response.Header {
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
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

func calculateCost(model *ai.Model, usage *ai.Usage) { ai.CalculateCost(model, usage) }

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
	body = truncateOpenAIErrorText(body)
	if prefix != "" {
		return fmt.Sprintf("%s (%d): %s", prefix, apiError.StatusCode, body)
	}
	return fmt.Sprintf("%d: %s", apiError.StatusCode, body)
}

// openRouterErrorMetadataRaw extracts error.metadata.raw from the parsed
// provider error body. Some providers behind OpenRouter relay the raw upstream
// response there, and upstream appends it to the completions error message
// when it is not already present (OA-m1).
func openRouterErrorMetadataRaw(err error) string {
	var statusError *openAIStatusError
	if errors.As(err, &statusError) {
		return openRouterMetadataFromErrorBody([]byte(statusError.body))
	}
	var apiError *openai.Error
	if errors.As(err, &apiError) {
		// The SDK already unwraps the body's "error" member into RawJSON.
		return openRouterMetadataFromErrorBody([]byte(apiError.RawJSON()))
	}
	return ""
}

func openRouterMetadataFromErrorBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed struct {
		Metadata struct {
			Raw json.RawMessage `json:"raw"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &parsed) != nil || len(parsed.Metadata.Raw) == 0 {
		return ""
	}
	var value any
	if json.Unmarshal(parsed.Metadata.Raw, &value) != nil || !openAIJSONTruthy(value) {
		return ""
	}
	return openAIJSString(value)
}

// openAIJSString mirrors JavaScript String() for JSON-decoded values.
func openAIJSString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		encoded, err := ai.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	case []any:
		parts := make([]string, len(typed))
		for index, item := range typed {
			if item == nil {
				continue
			}
			parts[index] = openAIJSString(item)
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
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

func truncateOpenAIErrorText(text string) string {
	units := utf16.Encode([]rune(text))
	if len(units) <= maxProviderErrorBodyChars {
		return text
	}
	prefixUnits := units[:maxProviderErrorBodyChars]
	prefix := string(utf16.Decode(prefixUnits))
	if len(prefixUnits) > 0 && prefixUnits[len(prefixUnits)-1] >= 0xd800 && prefixUnits[len(prefixUnits)-1] <= 0xdbff {
		unit := prefixUnits[len(prefixUnits)-1]
		prefix = string(utf16.Decode(prefixUnits[:len(prefixUnits)-1])) + string([]byte{
			byte(0xe0 | unit>>12),
			byte(0x80 | unit>>6&0x3f),
			byte(0x80 | unit&0x3f),
		})
	}
	return fmt.Sprintf("%s... [truncated %d chars]", prefix, len(units)-maxProviderErrorBodyChars)
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
