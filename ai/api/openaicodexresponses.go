package api

import (
	"bufio"
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
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth/oauth"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
)

const (
	defaultOpenAICodexBaseURL = "https://chatgpt.com/backend-api"
	defaultCodexMaxRetryDelay = 60 * time.Second
)

var (
	errCodexTerminal = errors.New("ai/api: Codex terminal response")
	openAICodexSleep = sleepWithContext
	// Mirrors upstream /rate.?limit|overloaded|service.?unavailable|upstream.?connect|connection.?refused/i.
	codexRetryableTextPattern = regexp.MustCompile(`(?i)rate.?limit|overloaded|service.?unavailable|upstream.?connect|connection.?refused`)
)

type codexAPIError struct {
	message string
	code    string
}

func (failure *codexAPIError) Error() string { return failure.message }

type codexProtocolError struct{ message string }

func (failure *codexProtocolError) Error() string { return failure.message }

func isCodexNonTransportError(err error) bool {
	var apiFailure *codexAPIError
	var protocolFailure *codexProtocolError
	return errors.As(err, &apiFailure) || errors.As(err, &protocolFailure)
}

func isCodexConnectionLimitError(err error) bool {
	var failure *codexAPIError
	return errors.As(err, &failure) && failure.code == "websocket_connection_limit_reached"
}

type OpenAICodexResponsesOptions struct {
	ai.StreamOptions
	ReasoningEffort  *string `json:"reasoningEffort,omitempty"`
	ReasoningSummary *string `json:"reasoningSummary,omitempty"`
	ServiceTier      *string `json:"serviceTier,omitempty"`
	TextVerbosity    *string `json:"textVerbosity,omitempty"`
	ToolChoice       *string `json:"toolChoice,omitempty"`
}

type OpenAICodexResponsesPayload struct {
	Model             string                     `json:"model"`
	Store             bool                       `json:"store"`
	Stream            bool                       `json:"stream"`
	Instructions      string                     `json:"instructions"`
	Input             []any                      `json:"input"`
	Text              openAICodexText            `json:"text"`
	Include           []string                   `json:"include"`
	PromptCacheKey    *string                    `json:"prompt_cache_key,omitempty"`
	ToolChoice        string                     `json:"tool_choice"`
	ParallelToolCalls bool                       `json:"parallel_tool_calls"`
	Temperature       *float64                   `json:"temperature,omitempty"`
	ServiceTier       *string                    `json:"service_tier,omitempty"`
	Tools             []openAICodexResponsesTool `json:"tools,omitempty"`
	Reasoning         *OpenAIReasoningParams     `json:"reasoning,omitempty"`
}

type openAICodexText struct {
	Verbosity string `json:"verbosity"`
}

type openAICodexResponsesTool struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Parameters  jsonschema.Schema `json:"parameters"`
	Strict      *bool             `json:"strict"`
}

func StreamOpenAICodexResponses(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: OpenAI Codex Responses model is nil")
	}
	options := &OpenAICodexResponsesOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamOpenAICodexResponsesWithOptions(ctx, request.Model, request.Context, options)
}

func StreamSimpleOpenAICodexResponses(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: OpenAI Codex Responses model is nil")
	}
	base := buildBaseStreamOptions(model, requestContext, options)
	var requested *ai.ThinkingLevel
	if options != nil {
		requested = options.Reasoning
	}
	clamped := clampSimpleReasoning(model, requested)
	var effort *string
	if clamped != nil {
		value := string(*clamped)
		effort = &value
	}
	return StreamOpenAICodexResponsesWithOptions(ctx, model, requestContext, &OpenAICodexResponsesOptions{
		StreamOptions:   base,
		ReasoningEffort: effort,
	})
}

func StreamOpenAICodexResponsesWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *OpenAICodexResponsesOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: OpenAI Codex Responses model is nil")
	}
	output := newAssistantMessage(model)
	streamOptions := codexStreamOptions(options)
	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		sink := func(event ai.AssistantMessageEvent) bool { return yield(event, nil) }
		fail := func(err error) {
			clearResponsesStreamingFields(output)
			reason := ai.StopReasonError
			if ctx.Err() != nil {
				reason = ai.StopReasonAborted
			}
			output.StopReason = reason
			message := err.Error()
			output.ErrorMessage = &message
			sink(ai.ErrorEvent{Reason: reason, Error: output})
		}

		apiKey, err := resolveOpenAIAPIKey(model, streamOptions)
		if err != nil {
			fail(err)
			return
		}
		accountID := oauth.OpenAICodexAccountID(apiKey)
		if accountID == "" {
			fail(errors.New("Failed to extract accountId from token")) //nolint:staticcheck // Upstream capitalization is observable.
			return
		}
		payload, err := buildOpenAICodexResponsesPayload(model, requestContext, options)
		if err != nil {
			fail(err)
			return
		}
		hookedPayload, err := applyPayloadHook(ctx, model, streamOptions, payload)
		if err != nil {
			fail(err)
			return
		}
		body, err := ai.Marshal(hookedPayload)
		if err != nil {
			fail(fmt.Errorf("encode OpenAI Codex request: %w", err))
			return
		}
		if err := validateOpenAICodexTimeouts(streamOptions); err != nil {
			fail(err)
			return
		}
		transport := codexTransport(streamOptions)
		sessionID := rawCodexSessionID(streamOptions)
		webSocketDisabled := transport != ai.TransportSSE && openAICodexWebSocketFallbackActive(sessionID)
		if webSocketDisabled {
			recordOpenAICodexSSEFallback(sessionID)
		}
		if transport != ai.TransportSSE && !webSocketDisabled {
			requestID, err := codexWebSocketRequestID(streamOptions)
			if err != nil {
				fail(err)
				return
			}
			webSocketHeaders := buildOpenAICodexWebSocketHeaders(model, streamOptions, apiKey, accountID, requestID)
			retriedConnectionLimit := false
			for {
				started, webSocketErr := processOpenAICodexWebSocket(ctx, model, body, webSocketHeaders, options, output, sink)
				if errors.Is(webSocketErr, errStopSSE) {
					return
				}
				if webSocketErr == nil {
					if ctx.Err() != nil {
						fail(errors.New("Request was aborted")) //nolint:staticcheck // Upstream capitalization is observable.
						return
					}
					clearResponsesStreamingFields(output)
					sink(ai.DoneEvent{Reason: output.StopReason, Message: output})
					return
				}
				aborted := ctx.Err() != nil || webSocketErr.Error() == "Request was aborted"
				connectionLimit := !started && isCodexConnectionLimitError(webSocketErr)
				if !aborted && connectionLimit && !retriedConnectionLimit {
					retriedConnectionLimit = true
					continue
				}
				if aborted || (isCodexNonTransportError(webSocketErr) && !connectionLimit) {
					fail(webSocketErr)
					return
				}
				if err := appendOpenAICodexTransportFailure(output, streamOptions, webSocketErr, started, len(body)); err != nil {
					fail(err)
					return
				}
				recordOpenAICodexWebSocketFailure(sessionID, webSocketErr)
				if started {
					fail(webSocketErr)
					return
				}
				recordOpenAICodexSSEFallback(sessionID)
				break
			}
		}
		headers := buildOpenAICodexHeaders(model, streamOptions, apiKey, accountID)
		// The Codex backend decodes Content-Encoding: zstd on the SSE path; the
		// WebSocket transport above sends the uncompressed JSON frame, matching
		// the official Codex client.
		sseBody := body
		if compressed, ok := compressOpenAICodexRequestBody(body); ok {
			headers.Set("Content-Encoding", "zstd")
			sseBody = compressed
		}
		response, err := postOpenAICodexStream(ctx, model, streamOptions, sseBody, headers)
		if err != nil {
			fail(err)
			return
		}
		defer func() { _ = response.Body.Close() }()
		if !sink(ai.StartEvent{Partial: output}) {
			return
		}

		processorOptions := &OpenAIResponsesOptions{}
		if options != nil {
			processorOptions.StreamOptions = options.StreamOptions
			processorOptions.ServiceTier = options.ServiceTier
		}
		processor := newOpenAIResponsesProcessor(model, output, processorOptions, sink)
		err = readOpenAICodexSSE(response.Body, func(raw json.RawMessage) error {
			return handleOpenAICodexEvent(processor, raw)
		})
		if errors.Is(err, errStopSSE) {
			return
		}
		if errors.Is(err, errCodexTerminal) {
			err = nil
		}
		if err == nil && !processor.sawTerminalResponseEvent {
			err = errors.New("OpenAI Codex Responses stream ended before a terminal response event")
		}
		if err == nil && ctx.Err() != nil {
			err = errors.New("Request was aborted") //nolint:staticcheck // Upstream capitalization is observable.
		}
		if err != nil {
			fail(err)
			return
		}
		clearResponsesStreamingFields(output)
		sink(ai.DoneEvent{Reason: output.StopReason, Message: output})
	}, nil
}

func codexStreamOptions(options *OpenAICodexResponsesOptions) *ai.StreamOptions {
	if options == nil {
		return nil
	}
	return &options.StreamOptions
}

func appendOpenAICodexTransportFailure(
	output *ai.AssistantMessage,
	options *ai.StreamOptions,
	failure error,
	started bool,
	requestBytes int,
) error {
	var fallback *ai.Transport
	if !started {
		value := ai.TransportSSE
		fallback = &value
	}
	phase := "before_message_stream_start"
	if started {
		phase = "after_message_stream_start"
	}
	details, err := ai.Marshal(struct {
		ConfiguredTransport ai.Transport  `json:"configuredTransport"`
		FallbackTransport   *ai.Transport `json:"fallbackTransport,omitempty"`
		EventsEmitted       bool          `json:"eventsEmitted"`
		Phase               string        `json:"phase"`
		RequestBytes        int           `json:"requestBytes"`
	}{
		ConfiguredTransport: codexTransport(options),
		FallbackTransport:   fallback,
		EventsEmitted:       started,
		Phase:               phase,
		RequestBytes:        requestBytes,
	})
	if err != nil {
		return err
	}
	name := "Error"
	var closeFailure *codexWebSocketCloseError
	if errors.As(failure, &closeFailure) {
		name = "WebSocketCloseError"
	}
	diagnostic := ai.AssistantMessageDiagnostic{
		Type:      "provider_transport_failure",
		Timestamp: openAINowUnixMilli(),
		Error:     &ai.DiagnosticErrorInfo{Name: &name, Message: failure.Error()},
		Details:   details,
	}
	diagnostics := []ai.AssistantMessageDiagnostic{diagnostic}
	if output.Diagnostics != nil {
		diagnostics = append(append([]ai.AssistantMessageDiagnostic(nil), (*output.Diagnostics)...), diagnostic)
	}
	output.Diagnostics = &diagnostics
	return nil
}

func buildOpenAICodexResponsesPayload(
	model *ai.Model,
	requestContext ai.Context,
	options *OpenAICodexResponsesOptions,
) (*OpenAICodexResponsesPayload, error) {
	compat, err := getOpenAIResponsesCompat(model)
	if err != nil {
		return nil, err
	}
	placement := splitResponsesTools(requestContext, compat.supportsToolSearch)
	withoutSystem := requestContext
	withoutSystem.SystemPrompt = nil
	input, err := convertResponsesMessages(model, withoutSystem, placement.deferred, compat.supportsDeveloperRole)
	if err != nil {
		return nil, err
	}
	instructions := "You are a helpful assistant."
	if requestContext.SystemPrompt != nil && *requestContext.SystemPrompt != "" {
		instructions = *requestContext.SystemPrompt
	}
	verbosity := "low"
	toolChoice := "auto"
	if options != nil {
		if options.TextVerbosity != nil && *options.TextVerbosity != "" {
			verbosity = *options.TextVerbosity
		}
		if options.ToolChoice != nil {
			toolChoice = *options.ToolChoice
		}
	}
	payload := &OpenAICodexResponsesPayload{
		Model: model.ID, Store: false, Stream: true, Instructions: instructions, Input: input,
		Text: openAICodexText{Verbosity: verbosity}, Include: []string{"reasoning.encrypted_content"},
		ToolChoice: toolChoice, ParallelToolCalls: true,
	}
	streamOptions := codexStreamOptions(options)
	if streamOptions != nil {
		payload.Temperature = streamOptions.Temperature
		if streamOptions.SessionID != nil {
			clamped := clampOpenAIPromptCacheKey(streamOptions.SessionID)
			if key, ok := clamped.(string); ok {
				payload.PromptCacheKey = &key
			}
		}
	}
	if options != nil {
		payload.ServiceTier = options.ServiceTier
		if options.ReasoningEffort != nil {
			// Upstream coalesces a null thinkingLevelMap entry back to the
			// requested effort with ??, so a nil mapping never omits reasoning.
			level, fallback := *options.ReasoningEffort, *options.ReasoningEffort
			if level == "none" {
				level, fallback = "off", "none"
			}
			summary := "auto"
			if options.ReasoningSummary != nil {
				summary = *options.ReasoningSummary
			}
			payload.Reasoning = &OpenAIReasoningParams{Effort: mappedThinkingLevel(model, level, fallback), Summary: &summary}
		}
	}
	if len(placement.immediate) > 0 {
		payload.Tools = make([]openAICodexResponsesTool, 0, len(placement.immediate))
		for _, tool := range placement.immediate {
			payload.Tools = append(payload.Tools, openAICodexResponsesTool{
				Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters,
			})
		}
	}
	return payload, nil
}

func buildOpenAICodexHeaders(model *ai.Model, options *ai.StreamOptions, token, accountID string) http.Header {
	headers := copyModelHeaders(model)
	if options != nil {
		mergeProviderHeaders(headers, options.Headers)
	}
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("chatgpt-account-id", accountID)
	headers.Set("originator", "pi")
	headers.Set("User-Agent", openAICodexUserAgent())
	headers.Set("OpenAI-Beta", "responses=experimental")
	headers.Set("Accept", "text/event-stream")
	headers.Set("Content-Type", "application/json")
	if options != nil && options.SessionID != nil {
		clamped := clampOpenAIPromptCacheKey(options.SessionID)
		if sessionID, ok := clamped.(string); ok && sessionID != "" {
			headers.Set("session-id", sessionID)
			headers.Set("x-client-request-id", sessionID)
		}
	}
	return headers
}

// compressOpenAICodexRequestBody returns the zstd-compressed body bytes at the
// level the official Codex client uses (zstd level 3, klauspost SpeedDefault).
// Callers fall back to sending the uncompressed JSON when compression fails.
func compressOpenAICodexRequestBody(body []byte) ([]byte, bool) {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, false
	}
	defer func() { _ = encoder.Close() }()
	return encoder.EncodeAll(body, nil), true
}

func resolveOpenAICodexURL(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		raw = defaultOpenAICodexBaseURL
	}
	normalized := strings.TrimRight(raw, "/")
	if strings.HasSuffix(normalized, "/codex/responses") {
		return normalized
	}
	if strings.HasSuffix(normalized, "/codex") {
		return normalized + "/responses"
	}
	return normalized + "/codex/responses"
}

func postOpenAICodexStream(
	ctx context.Context,
	model *ai.Model,
	options *ai.StreamOptions,
	body []byte,
	headers http.Header,
) (*http.Response, error) {
	maxRetries := 0
	if options != nil && options.MaxRetries != nil {
		maxRetries = *options.MaxRetries
	}
	var lastError error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, errors.New("Request was aborted") //nolint:staticcheck // Upstream capitalization is observable.
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveOpenAICodexURL(model.BaseURL), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header = headers.Clone()
		response, err, timedOut := doOpenAICodexRequest(ctx, request, codexHeaderTimeout(options))
		if err != nil {
			if timedOut {
				err = fmt.Errorf("Codex SSE response headers timed out after %dms", *options.TimeoutMS) //nolint:staticcheck // Upstream capitalization is observable.
			}
			lastError = err
			retry, retryErr := retryOpenAICodexFailure(ctx, lastError, attempt, maxRetries)
			if retryErr != nil {
				return nil, retryErr
			}
			if retry {
				continue
			}
			return nil, lastError
		}
		if options != nil && options.OnResponse != nil {
			if err := options.OnResponse(ctx, providerResponse(response), model); err != nil {
				_ = response.Body.Close()
				lastError = err
				retry, retryErr := retryOpenAICodexFailure(ctx, lastError, attempt, maxRetries)
				if retryErr != nil {
					return nil, retryErr
				}
				if retry {
					continue
				}
				return nil, lastError
			}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		contents, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			lastError = readErr
			retry, retryErr := retryOpenAICodexFailure(ctx, lastError, attempt, maxRetries)
			if retryErr != nil {
				return nil, retryErr
			}
			if retry {
				continue
			}
			return nil, lastError
		}
		if attempt < maxRetries && retryableCodexError(response.StatusCode, string(contents)) {
			delay := codexRetryDelay(response, attempt, options)
			if err := openAICodexSleep(ctx, delay); err != nil {
				return nil, errors.New("Request was aborted") //nolint:staticcheck // Upstream capitalization is observable.
			}
			continue
		}
		lastError = parseOpenAICodexHTTPError(response.StatusCode, response.Status, contents)
		retry, retryErr := retryOpenAICodexFailure(ctx, lastError, attempt, maxRetries)
		if retryErr != nil {
			return nil, retryErr
		}
		if retry {
			continue
		}
		return nil, lastError
	}
	if lastError != nil {
		return nil, lastError
	}
	return nil, errors.New("Failed after retries") //nolint:staticcheck // Upstream capitalization is observable.
}

func retryOpenAICodexFailure(ctx context.Context, failure error, attempt, maxRetries int) (bool, error) {
	if ctx.Err() != nil || failure.Error() == "Request was aborted" {
		return false, errors.New("Request was aborted") //nolint:staticcheck // Upstream capitalization is observable.
	}
	if attempt >= maxRetries || strings.Contains(failure.Error(), "usage limit") {
		return false, nil
	}
	if err := openAICodexSleep(ctx, time.Second*time.Duration(1<<attempt)); err != nil {
		return false, errors.New("Request was aborted") //nolint:staticcheck // Upstream capitalization is observable.
	}
	return true, nil
}

func codexHeaderTimeout(options *ai.StreamOptions) time.Duration {
	if options == nil || options.TimeoutMS == nil || *options.TimeoutMS <= 0 {
		return 0
	}
	return time.Duration(*options.TimeoutMS) * time.Millisecond
}

func validateOpenAICodexTimeouts(options *ai.StreamOptions) error {
	if options == nil {
		return nil
	}
	if options.TimeoutMS != nil && *options.TimeoutMS < 0 {
		return fmt.Errorf("Invalid timeoutMs: %d", *options.TimeoutMS) //nolint:staticcheck // Upstream capitalization is observable.
	}
	if options.WebSocketConnectTimeoutMS != nil && *options.WebSocketConnectTimeoutMS < 0 {
		return fmt.Errorf("Invalid timeoutMs: %d", *options.WebSocketConnectTimeoutMS) //nolint:staticcheck // Upstream uses the same message for both timeout options.
	}
	return nil
}

type codexHTTPResult struct {
	response *http.Response
	err      error
}

func doOpenAICodexRequest(ctx context.Context, request *http.Request, headerTimeout time.Duration) (*http.Response, error, bool) {
	if headerTimeout <= 0 {
		response, err := openAIHTTPClient.Do(request)
		return response, err, false
	}
	requestContext, cancel := context.WithCancel(ctx)
	request = request.WithContext(requestContext)
	result := make(chan codexHTTPResult)
	abandoned := make(chan struct{})
	go func() {
		response, err := openAIHTTPClient.Do(request)
		select {
		case result <- codexHTTPResult{response: response, err: err}:
		case <-abandoned:
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
		}
	}()
	timer := time.NewTimer(headerTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		cancel()
		close(abandoned)
		return nil, ctx.Err(), false
	case <-timer.C:
		cancel()
		close(abandoned)
		return nil, context.DeadlineExceeded, true
	case completed := <-result:
		if completed.err != nil || completed.response == nil || completed.response.Body == nil {
			cancel()
			return completed.response, completed.err, false
		}
		completed.response.Body = &codexCancelReadCloser{ReadCloser: completed.response.Body, cancel: cancel}
		return completed.response, nil, false
	}
}

type codexCancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *codexCancelReadCloser) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

func handleOpenAICodexEvent(processor *openAIResponsesProcessor, raw json.RawMessage) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return &codexProtocolError{message: err.Error()}
	}
	typeName, _ := codexString(envelope["type"])
	switch typeName {
	case "error":
		code, _ := codexString(envelope["code"])
		message, _ := codexString(envelope["message"])
		var nested map[string]json.RawMessage
		if json.Unmarshal(envelope["error"], &nested) == nil {
			if code == "" {
				code, _ = codexString(nested["code"])
			}
			if message == "" {
				message, _ = codexString(nested["message"])
			}
		}
		if message == "" {
			message = code
		}
		if message == "" {
			message = compactCodexEvent(raw)
		}
		return &codexAPIError{message: "Codex error: " + message, code: code}
	case "response.failed":
		var response, nested map[string]json.RawMessage
		_ = json.Unmarshal(envelope["response"], &response)
		_ = json.Unmarshal(response["error"], &nested)
		message, code := "Codex response failed", ""
		if rawCode, ok := nested["code"]; ok {
			var flexible codexFlexibleCode
			_ = flexible.UnmarshalJSON(rawCode)
			code = flexible.text
		}
		if nestedMessage, ok := codexString(nested["message"]); ok && nestedMessage != "" {
			message = nestedMessage
		}
		return &codexAPIError{message: message, code: code}
	case "response.done", "response.completed", "response.incomplete":
		var requestedServiceTier *string
		if processor.options != nil {
			requestedServiceTier = processor.options.ServiceTier
		}
		normalized, err := normalizeOpenAICodexTerminalEvent(envelope["response"], requestedServiceTier)
		if err != nil {
			return err
		}
		if err := processor.handle(normalized); err != nil {
			return err
		}
		return errCodexTerminal
	}
	return processor.handle(raw)
}

func codexString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func readOpenAICodexSSE(body io.Reader, handle func(json.RawMessage) error) error {
	reader := bufio.NewReader(body)
	dataLines := make([]string, 0, 1)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if line == "" {
				data := strings.TrimSpace(strings.Join(dataLines, "\n"))
				dataLines = dataLines[:0]
				if data != "" && data != "[DONE]" {
					var raw json.RawMessage
					if decodeErr := json.Unmarshal([]byte(data), &raw); decodeErr != nil {
						return fmt.Errorf("Invalid Codex SSE JSON: %w", decodeErr) //nolint:staticcheck // Upstream capitalization is observable.
					}
					if handleErr := handle(raw); handleErr != nil {
						return handleErr
					}
				}
			} else if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// codexFlexibleCode decodes an error code that providers send as either a JSON
// string or a number without failing the strict event unmarshal.
type codexFlexibleCode struct {
	text     string
	isString bool
}

func (code *codexFlexibleCode) UnmarshalJSON(data []byte) error {
	var text string
	if json.Unmarshal(data, &text) == nil {
		code.text, code.isString = text, true
		return nil
	}
	var number json.Number
	if json.Unmarshal(data, &number) == nil {
		code.text, code.isString = number.String(), false
		return nil
	}
	code.text, code.isString = "", false
	return nil
}

func compactCodexEvent(raw json.RawMessage) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err == nil {
		return compact.String()
	}
	return string(raw)
}

func normalizeOpenAICodexTerminalEvent(raw json.RawMessage, requestedServiceTier *string) (json.RawMessage, error) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if encodedStatus, ok := response["status"]; ok {
		var status string
		if json.Unmarshal(encodedStatus, &status) != nil || !validOpenAICodexStatus(status) {
			delete(response, "status")
		}
	}
	if requestedServiceTier != nil && (*requestedServiceTier == "flex" || *requestedServiceTier == "priority") {
		var responseServiceTier string
		if json.Unmarshal(response["service_tier"], &responseServiceTier) == nil && responseServiceTier == "default" {
			encodedServiceTier, err := json.Marshal(*requestedServiceTier)
			if err != nil {
				return nil, err
			}
			response["service_tier"] = encodedServiceTier
		}
	}
	normalizedResponse, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(`{"type":"response.completed","response":` + string(normalizedResponse) + `}`), nil
}

func validOpenAICodexStatus(status string) bool {
	switch status {
	case "completed", "incomplete", "failed", "cancelled", "queued", "in_progress":
		return true
	default:
		return false
	}
}

func retryableCodexError(status int, text string) bool {
	terminal := regexpMatchFold(text, "GoUsageLimitError", "FreeUsageLimitError", "Monthly usage limit reached", "available balance", "insufficient_quota", "out of budget", "quota exceeded", "billing")
	if status == http.StatusTooManyRequests && terminal {
		return false
	}
	if status == 429 || status == 500 || status == 502 || status == 503 || status == 504 {
		return true
	}
	return codexRetryableTextPattern.MatchString(text)
}

func regexpMatchFold(text string, markers ...string) bool {
	normalized := strings.ToLower(text)
	for _, marker := range markers {
		if strings.Contains(normalized, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func codexRetryDelay(response *http.Response, attempt int, options *ai.StreamOptions) time.Duration {
	if value := response.Header.Get("retry-after-ms"); value != "" {
		if milliseconds, err := strconv.ParseFloat(value, 64); err == nil {
			return capCodexRetryDelay(time.Duration(max(0, milliseconds))*time.Millisecond, response.StatusCode, options)
		}
	}
	if value := response.Header.Get("retry-after"); value != "" {
		if seconds, err := strconv.ParseFloat(value, 64); err == nil {
			return capCodexRetryDelay(time.Duration(max(0, seconds)*float64(time.Second)), response.StatusCode, options)
		}
		if date, err := http.ParseTime(value); err == nil {
			delay := max(time.Duration(0), date.Sub(time.UnixMilli(openAINowUnixMilli())))
			return capCodexRetryDelay(delay, response.StatusCode, options)
		}
	}
	return time.Second * time.Duration(1<<attempt)
}

func capCodexRetryDelay(delay time.Duration, status int, options *ai.StreamOptions) time.Duration {
	if status != http.StatusTooManyRequests {
		return delay
	}
	maximum := defaultCodexMaxRetryDelay
	if options != nil && options.MaxRetryDelayMS != nil {
		maximum = time.Duration(*options.MaxRetryDelayMS) * time.Millisecond
	}
	if maximum > 0 {
		return min(delay, maximum)
	}
	return delay
}

func parseOpenAICodexHTTPError(status int, statusText string, contents []byte) error {
	message := string(contents)
	if message == "" {
		message = strings.TrimSpace(strings.TrimPrefix(statusText, strconv.Itoa(status)))
	}
	var envelope struct {
		Error *struct {
			Code     string `json:"code"`
			Type     string `json:"type"`
			Message  string `json:"message"`
			PlanType string `json:"plan_type"`
			ResetsAt int64  `json:"resets_at"`
		} `json:"error"`
	}
	if json.Unmarshal(contents, &envelope) == nil && envelope.Error != nil {
		code := envelope.Error.Code
		if code == "" {
			code = envelope.Error.Type
		}
		if status == http.StatusTooManyRequests || regexpMatchFold(code, "usage_limit_reached", "usage_not_included", "rate_limit_exceeded") {
			plan := ""
			if envelope.Error.PlanType != "" {
				plan = " (" + strings.ToLower(envelope.Error.PlanType) + " plan)"
			}
			when := ""
			if envelope.Error.ResetsAt != 0 {
				minutes := max(int64(0), int64(float64(envelope.Error.ResetsAt*1000-openAINowUnixMilli())/60000+0.5))
				when = fmt.Sprintf(" Try again in ~%d min.", minutes)
			}
			return errors.New(strings.TrimSpace("You have hit your ChatGPT usage limit" + plan + "." + when))
		}
		if envelope.Error.Message != "" {
			message = envelope.Error.Message
		}
	}
	return errors.New(message)
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
