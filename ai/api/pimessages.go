package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/partialjson"
)

// PiMessagesOptions contains the gateway-specific options sent through the
// pi-messages wire shape.
type PiMessagesOptions struct {
	ai.StreamOptions
	Reasoning  *ai.ThinkingLevel    `json:"reasoning,omitempty"`
	ToolChoice PiMessagesToolChoice `json:"toolChoice,omitempty"`
	Debug      bool                 `json:"debug,omitempty"`
}

// PiMessagesToolChoice is either auto/none/required or the upstream function
// selector object.
type PiMessagesToolChoice any

type piMessagesRawJSON json.RawMessage

func (raw piMessagesRawJSON) MarshalJSON() ([]byte, error) {
	return ai.NormalizeJSONStringifyJSON(raw)
}

func (options *PiMessagesOptions) UnmarshalJSON(data []byte) error {
	type plain PiMessagesOptions
	if err := json.Unmarshal(data, (*plain)(options)); err != nil {
		return err
	}
	var raw struct {
		ToolChoice json.RawMessage `json:"toolChoice"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw.ToolChoice) > 0 && string(raw.ToolChoice) != "null" {
		options.ToolChoice = piMessagesRawJSON(bytes.Clone(raw.ToolChoice))
	}
	return nil
}

// PiMessagesRewriteImpact records a server-side context rewrite reported by
// the gateway.
type PiMessagesRewriteImpact struct {
	PolicyID            string `json:"policyId"`
	PolicyVersion       int64  `json:"policyVersion"`
	Changed             bool   `json:"changed"`
	TokenCountChange    int64  `json:"tokenCountChange"`
	MessageCountChange  int64  `json:"messageCountChange"`
	SystemPromptChanged bool   `json:"systemPromptChanged"`
}

// PiMessagesResponseError retains the structured diagnostics returned for a
// non-successful gateway response.
type PiMessagesResponseError struct {
	Message           string
	Code              *string
	DiagnosticDetails json.RawMessage
}

func (err *PiMessagesResponseError) Error() string { return err.Message }

// PiMessagesPayload is the request body accepted by a pi-messages backend.
type PiMessagesPayload struct {
	Model   string                   `json:"model"`
	Context ai.Context               `json:"context"`
	Options PiMessagesPayloadOptions `json:"options"`
}

// PiMessagesPayloadOptions is ordered to match upstream JSON serialization.
type PiMessagesPayloadOptions struct {
	Temperature    *float64             `json:"temperature,omitempty"`
	MaxTokens      *float64             `json:"maxTokens,omitempty"`
	Reasoning      *ai.ThinkingLevel    `json:"reasoning,omitempty"`
	CacheRetention *ai.CacheRetention   `json:"cacheRetention,omitempty"`
	SessionID      *string              `json:"sessionId,omitempty"`
	ToolChoice     PiMessagesToolChoice `json:"toolChoice,omitempty"`
}

type piMessagesWireEvent struct {
	Type             string                   `json:"type"`
	ContentIndex     int                      `json:"contentIndex,omitempty"`
	Delta            string                   `json:"delta,omitempty"`
	Content          string                   `json:"content,omitempty"`
	ContentSignature *string                  `json:"contentSignature,omitempty"`
	Redacted         *bool                    `json:"redacted,omitempty"`
	ID               string                   `json:"id,omitempty"`
	ToolName         string                   `json:"toolName,omitempty"`
	ToolCall         *ai.ToolCall             `json:"toolCall,omitempty"`
	Reason           ai.StopReason            `json:"reason,omitempty"`
	Usage            *ai.Usage                `json:"usage,omitempty"`
	ErrorMessage     *string                  `json:"errorMessage,omitempty"`
	ResponseID       *string                  `json:"responseId,omitempty"`
	Rewrite          *PiMessagesRewriteImpact `json:"rewrite,omitempty"`
	Raw              json.RawMessage          `json:"-"`
}

func (event *piMessagesWireEvent) UnmarshalJSON(data []byte) error {
	type plainEvent piMessagesWireEvent
	var decoded plainEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	normalized, err := ai.NormalizeJSONStringifyJSON(data)
	if err != nil {
		return err
	}
	*event = piMessagesWireEvent(decoded)
	event.Raw = normalized
	return nil
}

type piMessagesErrorBody struct {
	Message *string
	Code    *string
	Raw     json.RawMessage
}

var (
	piMessagesHTTPClient  = http.DefaultClient
	errStopPiMessagesSSE  = errors.New("ai/api: stop pi-messages SSE")
	errPiMessagesTerminal = errors.New("ai/api: pi-messages terminal event")
)

// StreamPiMessages adapts the provider-neutral request to the pi-messages
// gateway shape.
func StreamPiMessages(ctx context.Context, request ai.Request) (ai.AssistantMessageEventStream, error) {
	if request.Model == nil {
		return nil, errors.New("ai/api: pi-messages model is nil")
	}
	options := &PiMessagesOptions{}
	if request.Options != nil {
		options.StreamOptions = *request.Options
	}
	return StreamPiMessagesWithOptions(ctx, request.Model, request.Context, options)
}

// StreamSimplePiMessages forwards the unified reasoning option without
// inventing provider defaults that upstream does not send.
func StreamSimplePiMessages(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: pi-messages model is nil")
	}
	piOptions := &PiMessagesOptions{}
	if options != nil {
		piOptions.StreamOptions = options.StreamOptions
		piOptions.Reasoning = options.Reasoning
	}
	return StreamPiMessagesWithOptions(ctx, model, requestContext, piOptions)
}

// StreamPiMessagesWithOptions streams serialized assistant-message events
// from a conforming gateway.
func StreamPiMessagesWithOptions(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *PiMessagesOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, errors.New("ai/api: pi-messages model is nil")
	}
	if options == nil {
		options = &PiMessagesOptions{}
	}
	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		fail := func(err error) {
			yield(piMessagesErrorEvent(model, err, ctx.Err() != nil), nil)
		}

		if options.APIKey == nil || *options.APIKey == "" {
			fail(fmt.Errorf("No API key provided for provider %q", model.Provider)) //nolint:staticcheck // Exact upstream error text is observable.
			return
		}
		endpoint, err := piMessagesURL(model.BaseURL, options.Debug)
		if err != nil {
			fail(err)
			return
		}
		payload := PiMessagesPayload{
			Model:   model.ID,
			Context: requestContext,
			Options: PiMessagesPayloadOptions{
				Temperature:    options.Temperature,
				MaxTokens:      options.MaxTokens,
				Reasoning:      options.Reasoning,
				CacheRetention: piMessagesCacheRetention(options),
				SessionID:      options.SessionID,
				ToolChoice:     options.ToolChoice,
			},
		}
		hooked, err := applyPayloadHook(ctx, model, &options.StreamOptions, payload)
		if err != nil {
			fail(err)
			return
		}
		body, err := ai.Marshal(hooked)
		if err != nil {
			fail(fmt.Errorf("encode pi-messages request: %w", err))
			return
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
		if err != nil {
			fail(err)
			return
		}
		request.Header = piMessagesHeaders(*options.APIKey, options.Headers)
		request.Header, err = applyHeadersHook(ctx, model, &options.StreamOptions, request.Header)
		if err != nil {
			fail(err)
			return
		}
		response, err := piMessagesHTTPClient.Do(request)
		if err != nil {
			fail(err)
			return
		}
		if response == nil {
			fail(errors.New("pi-messages API returned no HTTP response"))
			return
		}
		if options.OnResponse != nil {
			if err := options.OnResponse(ctx, providerResponse(response), model); err != nil {
				if response.Body != nil {
					_ = response.Body.Close()
				}
				fail(err)
				return
			}
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			responseBody, readErr := piMessagesResponseBody(response)
			if readErr != nil {
				fail(readErr)
				return
			}
			fail(newPiMessagesResponseError(model, endpoint, response, responseBody))
			return
		}
		if response.Body == nil {
			fail(fmt.Errorf("%s response has no body", model.Provider))
			return
		}
		defer func() { _ = response.Body.Close() }()

		converter := newPiMessagesEventConverter(model)
		var pending ai.AssistantMessageEvent
		emitPending := func() error {
			if pending == nil {
				return nil
			}
			event := pending
			pending = nil
			if !yield(event, nil) {
				return errStopPiMessagesSSE
			}
			return nil
		}
		err = readPiMessagesEvents(response.Body, func(wire piMessagesWireEvent) error {
			event, done, convertErr := converter.convert(wire)
			if convertErr != nil {
				return convertErr
			}
			if err := emitPending(); err != nil {
				return err
			}
			pending = event
			if done {
				return errPiMessagesTerminal
			}
			return nil
		}, emitPending)
		if errors.Is(err, errPiMessagesTerminal) {
			_ = emitPending()
			return
		}
		if errors.Is(err, errStopPiMessagesSSE) {
			return
		}
		if err != nil {
			fail(err)
			return
		}
		fail(fmt.Errorf("%s stream ended without a terminal event", model.Provider))
	}, nil
}

type piMessagesEventConverter struct {
	partial  *ai.AssistantMessage
	toolJSON map[int]string
}

func newPiMessagesEventConverter(model *ai.Model) *piMessagesEventConverter {
	return &piMessagesEventConverter{
		partial:  newAssistantMessage(model),
		toolJSON: make(map[int]string),
	}
}

func (converter *piMessagesEventConverter) convert(wire piMessagesWireEvent) (ai.AssistantMessageEvent, bool, error) {
	partial := converter.partial
	switch wire.Type {
	case "done":
		partial.StopReason = wire.Reason
		if wire.Usage != nil {
			partial.Usage = *wire.Usage
		}
		partial.ResponseID = wire.ResponseID
		appendPiMessagesRewriteDiagnostic(partial, wire.Rewrite)
		return ai.DoneEvent{Reason: wire.Reason, Message: partial}, true, nil
	case "error":
		partial.StopReason = wire.Reason
		if wire.Usage != nil {
			partial.Usage = *wire.Usage
		}
		partial.ErrorMessage = wire.ErrorMessage
		partial.ResponseID = wire.ResponseID
		ai.SetAssistantMessageErrorBeforeResponseID(partial, wire.ErrorMessage != nil)
		appendPiMessagesRewriteDiagnostic(partial, wire.Rewrite)
		return ai.ErrorEvent{Reason: wire.Reason, Error: partial}, true, nil
	case "start":
		return ai.StartEvent{Partial: partial}, false, nil
	case "text_start":
		converter.ensureContentIndex(wire.ContentIndex)
		partial.Content[wire.ContentIndex] = &ai.TextContent{}
		return ai.TextStartEvent{ContentIndex: wire.ContentIndex, Partial: partial}, false, nil
	case "text_delta":
		content, ok := converter.textContent(wire.ContentIndex)
		if !ok {
			return nil, false, fmt.Errorf("pi-messages text_delta has no text block at content index %d", wire.ContentIndex)
		}
		content.Text += wire.Delta
		return ai.TextDeltaEvent{ContentIndex: wire.ContentIndex, Delta: wire.Delta, Partial: partial}, false, nil
	case "text_end":
		content, ok := converter.textContent(wire.ContentIndex)
		if !ok {
			return nil, false, fmt.Errorf("pi-messages text_end has no text block at content index %d", wire.ContentIndex)
		}
		content.Text = wire.Content
		content.TextSignature = wire.ContentSignature
		return ai.TextEndEvent{
			ContentIndex: wire.ContentIndex, Content: wire.Content,
			ContentSignature: wire.ContentSignature, Partial: partial,
		}, false, nil
	case "thinking_start":
		converter.ensureContentIndex(wire.ContentIndex)
		partial.Content[wire.ContentIndex] = &ai.ThinkingContent{}
		return ai.ThinkingStartEvent{ContentIndex: wire.ContentIndex, Partial: partial}, false, nil
	case "thinking_delta":
		content, ok := converter.thinkingContent(wire.ContentIndex)
		if !ok {
			return nil, false, fmt.Errorf("pi-messages thinking_delta has no thinking block at content index %d", wire.ContentIndex)
		}
		content.Thinking += wire.Delta
		return ai.ThinkingDeltaEvent{ContentIndex: wire.ContentIndex, Delta: wire.Delta, Partial: partial}, false, nil
	case "thinking_end":
		content, ok := converter.thinkingContent(wire.ContentIndex)
		if !ok {
			return nil, false, fmt.Errorf("pi-messages thinking_end has no thinking block at content index %d", wire.ContentIndex)
		}
		content.Thinking = wire.Content
		content.ThinkingSignature = wire.ContentSignature
		content.Redacted = wire.Redacted
		return ai.ThinkingEndEvent{
			ContentIndex: wire.ContentIndex, Content: wire.Content,
			ContentSignature: wire.ContentSignature, Redacted: wire.Redacted, Partial: partial,
		}, false, nil
	case "toolcall_start":
		converter.ensureContentIndex(wire.ContentIndex)
		partial.Content[wire.ContentIndex] = &ai.ToolCall{ID: wire.ID, Name: wire.ToolName, Arguments: map[string]any{}}
		converter.toolJSON[wire.ContentIndex] = ""
		return ai.ToolCallStartEvent{
			ContentIndex: wire.ContentIndex, ID: wire.ID, ToolName: wire.ToolName, Partial: partial,
		}, false, nil
	case "toolcall_delta":
		call, ok := converter.toolCall(wire.ContentIndex)
		if !ok {
			return nil, false, fmt.Errorf("pi-messages toolcall_delta has no tool call at content index %d", wire.ContentIndex)
		}
		arguments := converter.toolJSON[wire.ContentIndex] + wire.Delta
		converter.toolJSON[wire.ContentIndex] = arguments
		encoded, err := partialjson.StringifyStreamingJSON(arguments)
		if err != nil || ai.SetToolCallArgumentsJSON(call, encoded) != nil {
			_ = ai.SetToolCallArgumentsJSON(call, []byte(`{}`))
		}
		return ai.ToolCallDeltaEvent{ContentIndex: wire.ContentIndex, Delta: wire.Delta, Partial: partial}, false, nil
	case "toolcall_end":
		call, ok := converter.toolCall(wire.ContentIndex)
		if !ok {
			return nil, false, fmt.Errorf("pi-messages toolcall_end has no tool call at content index %d", wire.ContentIndex)
		}
		if wire.ToolCall == nil {
			return nil, false, fmt.Errorf("pi-messages toolcall_end has no toolCall at content index %d", wire.ContentIndex)
		}
		*call = *wire.ToolCall
		delete(converter.toolJSON, wire.ContentIndex)
		return ai.ToolCallEndEvent{ContentIndex: wire.ContentIndex, ToolCall: call, Partial: partial}, false, nil
	default:
		return ai.RawAssistantMessageEvent{Raw: wire.Raw, Partial: partial}, false, nil
	}
}

func (converter *piMessagesEventConverter) ensureContentIndex(index int) {
	for len(converter.partial.Content) <= index {
		converter.partial.Content = append(converter.partial.Content, nil)
	}
}

func (converter *piMessagesEventConverter) textContent(index int) (*ai.TextContent, bool) {
	if index < 0 || index >= len(converter.partial.Content) {
		return nil, false
	}
	content, ok := converter.partial.Content[index].(*ai.TextContent)
	return content, ok
}

func (converter *piMessagesEventConverter) thinkingContent(index int) (*ai.ThinkingContent, bool) {
	if index < 0 || index >= len(converter.partial.Content) {
		return nil, false
	}
	content, ok := converter.partial.Content[index].(*ai.ThinkingContent)
	return content, ok
}

func (converter *piMessagesEventConverter) toolCall(index int) (*ai.ToolCall, bool) {
	if index < 0 || index >= len(converter.partial.Content) {
		return nil, false
	}
	call, ok := converter.partial.Content[index].(*ai.ToolCall)
	return call, ok
}

func piMessagesCacheRetention(options *PiMessagesOptions) *ai.CacheRetention {
	if options.CacheRetention != nil {
		return options.CacheRetention
	}
	if providerEnvValue("PI_CACHE_RETENTION", &options.StreamOptions) != "long" {
		return nil
	}
	retention := ai.CacheRetentionLong
	return &retention
}

func piMessagesURL(baseURL string, debug bool) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + "/messages")
	if err != nil {
		return nil, err
	}
	if debug {
		query := endpoint.Query()
		query.Set("debug", "1")
		endpoint.RawQuery = query.Encode()
	}
	return endpoint, nil
}

func piMessagesHeaders(apiKey string, custom ai.ProviderHeaders) http.Header {
	headers := http.Header{
		"Authorization": []string{"Bearer " + apiKey},
		"Accept":        []string{"text/event-stream"},
		"Content-Type":  []string{"application/json"},
	}
	for name, value := range custom {
		if value != nil {
			headers.Set(name, *value)
		}
	}
	return headers
}

func piMessagesResponseBody(response *http.Response) (string, error) {
	if response.Body == nil {
		return "", nil
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	return strings.ToValidUTF8(string(body), "�"), err
}

func newPiMessagesResponseError(model *ai.Model, endpoint *url.URL, response *http.Response, body string) *PiMessagesResponseError {
	errorBody := parsePiMessagesErrorBody(body)
	suffix := body
	var code *string
	if errorBody != nil {
		if errorBody.Message != nil {
			suffix = *errorBody.Message
		}
		code = errorBody.Code
	}
	codeSuffix := ""
	if code != nil && *code != "" {
		codeSuffix = " (" + *code + ")"
	}
	statusText := piMessagesStatusText(response)
	message := fmt.Sprintf("%d %s: %s%s", response.StatusCode, statusText, suffix, codeSuffix)
	details := struct {
		Version     int             `json:"version"`
		Provider    ai.ProviderID   `json:"provider"`
		Model       string          `json:"model"`
		URL         string          `json:"url"`
		Status      int             `json:"status"`
		StatusText  string          `json:"statusText"`
		Error       json.RawMessage `json:"error,omitempty"`
		Body        *string         `json:"body,omitempty"`
		TimestampMS int64           `json:"timestampMs"`
	}{
		Version: 1, Provider: model.Provider, Model: model.ID, URL: endpoint.String(),
		Status: response.StatusCode, StatusText: statusText, TimestampMS: openAINowUnixMilli(),
	}
	if errorBody == nil {
		truncated := truncatePiMessagesDiagnosticString(body)
		details.Body = &truncated
	} else {
		details.Error = errorBody.Raw
	}
	encoded, _ := ai.Marshal(details)
	return &PiMessagesResponseError{Message: message, Code: code, DiagnosticDetails: encoded}
}

func parsePiMessagesErrorBody(body string) *piMessagesErrorBody {
	var envelope map[string]json.RawMessage
	if json.Unmarshal([]byte(body), &envelope) != nil {
		return nil
	}
	rawError, ok := envelope["error"]
	if !ok {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(rawError, &fields) != nil || fields == nil {
		return nil
	}
	result := &piMessagesErrorBody{}
	if normalized, err := ai.NormalizeJSONStringifyJSON(rawError); err == nil {
		result.Raw = normalized
	} else {
		result.Raw = bytes.Clone(rawError)
	}
	var message string
	if raw, ok := fields["message"]; ok && json.Unmarshal(raw, &message) == nil {
		result.Message = &message
	}
	if raw, ok := fields["code"]; ok {
		var code string
		if json.Unmarshal(raw, &code) == nil {
			result.Code = &code
		}
	}
	return result
}

func piMessagesStatusText(response *http.Response) string {
	prefix := fmt.Sprintf("%d ", response.StatusCode)
	if strings.HasPrefix(response.Status, prefix) {
		return strings.TrimPrefix(response.Status, prefix)
	}
	return http.StatusText(response.StatusCode)
}

func truncatePiMessagesDiagnosticString(value string) string {
	const maximum = 8192
	runes := []rune(value)
	units := 0
	for index, current := range runes {
		width := 1
		if current > 0xffff {
			width = 2
		}
		if units+width > maximum {
			return string(runes[:index]) + "…"
		}
		units += width
	}
	return value
}

func appendPiMessagesRewriteDiagnostic(message *ai.AssistantMessage, rewrite *PiMessagesRewriteImpact) {
	if rewrite == nil {
		return
	}
	details, _ := ai.Marshal(rewrite)
	appendPiMessagesDiagnostic(message, ai.AssistantMessageDiagnostic{
		Type: "pi_messages_rewrite", Timestamp: openAINowUnixMilli(), Details: details,
	})
}

func piMessagesErrorEvent(model *ai.Model, err error, aborted bool) ai.ErrorEvent {
	reason := ai.StopReasonError
	if aborted {
		reason = ai.StopReasonAborted
	}
	message := newAssistantMessage(model)
	message.StopReason = reason
	errorMessage := err.Error()
	message.ErrorMessage = &errorMessage
	ai.SetAssistantMessageErrorBeforeTimestamp(message, true)
	var responseError *PiMessagesResponseError
	if !aborted && errors.As(err, &responseError) {
		name := "PiMessagesResponseError"
		var code json.RawMessage
		if responseError.Code != nil {
			code, _ = ai.Marshal(*responseError.Code)
		}
		appendPiMessagesDiagnostic(message, ai.AssistantMessageDiagnostic{
			Type:      "pi_messages_response_failure",
			Timestamp: openAINowUnixMilli(),
			Error: &ai.DiagnosticErrorInfo{
				Name: &name, Message: responseError.Message, Code: code,
			},
			Details: responseError.DiagnosticDetails,
		})
	}
	return ai.ErrorEvent{Reason: reason, Error: message}
}

func appendPiMessagesDiagnostic(message *ai.AssistantMessage, diagnostic ai.AssistantMessageDiagnostic) {
	diagnostics := make([]ai.AssistantMessageDiagnostic, 0, 1)
	if message.Diagnostics != nil {
		diagnostics = append(diagnostics, (*message.Diagnostics)...)
	}
	diagnostics = append(diagnostics, diagnostic)
	message.Diagnostics = &diagnostics
}

func readPiMessagesEvents(
	reader io.Reader,
	handle func(piMessagesWireEvent) error,
	afterChunk func() error,
) error {
	buffer := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		read, readErr := reader.Read(chunk)
		if read > 0 {
			buffer = append(buffer, chunk[:read]...)
			buffer = bytes.ReplaceAll(buffer, []byte("\r\n"), []byte("\n"))
			for {
				index := bytes.Index(buffer, []byte("\n\n"))
				if index < 0 {
					break
				}
				if err := parsePiMessagesEvent(buffer[:index], handle); err != nil {
					return err
				}
				buffer = append(buffer[:0], buffer[index+2:]...)
			}
			if afterChunk != nil {
				if err := afterChunk(); err != nil {
					return err
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return readErr
			}
			if len(bytes.TrimSpace(buffer)) != 0 {
				if err := parsePiMessagesEvent(buffer, handle); err != nil {
					return err
				}
				if afterChunk != nil {
					return afterChunk()
				}
			}
			return nil
		}
	}
}

func parsePiMessagesEvent(raw []byte, handle func(piMessagesWireEvent) error) error {
	var data []byte
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data:")) {
			data = bytes.TrimSpace(line[len("data:"):])
			break
		}
	}
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil
	}
	var event piMessagesWireEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	return handle(event)
}
