package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

const (
	openAIPromptCacheKeyMaxLength = 64
	maxProviderErrorBodyChars     = 4000
	openAIDefaultTimeout          = 10 * time.Minute
)

var (
	errStopSSE                            = errors.New("ai/api: stop SSE stream")
	errOpenAIHeaderTimeout                = errors.New("Request timed out.") //nolint:staticcheck // Exact upstream SDK error text is observable.
	openAIHTTPClient       openAIHTTPDoer = http.DefaultClient
	openAINowUnixMilli                    = func() int64 { return time.Now().UnixMilli() }
)

type eventSink func(ai.AssistantMessageEvent) bool

type grammarConstrainedSampling struct {
	format        string
	definition    string
	inputProperty string
}

type grammarToolInputJSONBuffer struct {
	input   string
	started bool
	closed  bool
}

func getGrammarToolInput(toolName string, arguments map[string]any, inputProperty string) (string, error) {
	input, ok := arguments[inputProperty].(string)
	if !ok {
		return "", fmt.Errorf("Grammar tool call %q requires argument %q to be a string.", toolName, inputProperty) //nolint:staticcheck // Exact upstream text.
	}
	return input, nil
}

func appendGrammarToolInputJSONDelta(
	buffer *grammarToolInputJSONBuffer,
	inputProperty string,
	nextInput string,
	closeInput bool,
) (*string, error) {
	if buffer.closed {
		if closeInput && nextInput == buffer.input {
			return nil, nil
		}
		return nil, fmt.Errorf("grammar tool input for property %q changed after it was closed", inputProperty)
	}
	if !strings.HasPrefix(nextInput, buffer.input) {
		return nil, fmt.Errorf("grammar tool input for property %q changed non-monotonically", inputProperty)
	}

	inputDelta := strings.TrimPrefix(nextInput, buffer.input)
	if !closeInput && inputDelta == "" {
		return nil, nil
	}
	var delta strings.Builder
	if !buffer.started {
		property, err := jsonwire.MarshalString(inputProperty)
		if err != nil {
			return nil, err
		}
		delta.WriteByte('{')
		delta.Write(property)
		delta.WriteString(`:"`)
		buffer.started = true
	}
	encoded, err := jsonwire.MarshalString(inputDelta)
	if err != nil {
		return nil, err
	}
	delta.Write(encoded[1 : len(encoded)-1])
	buffer.input = nextInput
	if closeInput {
		delta.WriteString(`"}`)
		buffer.closed = true
	}
	value := delta.String()
	return &value, nil
}

// resolveJSONSchemaStrictSampling decides strict sampling for a tool; with
// rejects, a "prefer" tool whose schema hits a keyword the provider's strict
// mode rejects is sent non-strict.
func resolveJSONSchemaStrictSampling(tool ai.Tool, supportsStrictMode bool, rejects ...strictKeywordCheck) (*bool, error) {
	config := tool.ConstrainedSampling
	if config == nil || config.Type != ai.ConstrainedSamplingJSONSchema {
		return nil, nil
	}
	if supportsStrictMode {
		var check strictKeywordCheck
		if len(rejects) > 0 {
			check = rejects[0]
		}
		if _, err := makeStrictJSONSchema(tool.Parameters, check); err != nil {
			var unsupported *unsupportedStrictSchemaError
			if !errors.As(err, &unsupported) {
				return nil, err
			}
			if config.Strict != ai.ConstrainedSamplingRequire {
				return nil, nil
			}
			return nil, fmt.Errorf("Tool %q requires JSON-schema constrained sampling, but %s.", tool.Name, unsupported.reason) //nolint:staticcheck // Exact upstream text.
		}
		value := true
		return &value, nil
	}
	if config.Strict == ai.ConstrainedSamplingRequire {
		return nil, fmt.Errorf("Tool %q requires JSON-schema constrained sampling, but strict tools are unsupported.", tool.Name) //nolint:staticcheck // Exact upstream text.
	}
	return nil, nil
}

func resolveGrammarConstrainedSampling(tool ai.Tool, supportsOpenAIGrammarTools bool) (*grammarConstrainedSampling, error) {
	config := tool.ConstrainedSampling
	if config == nil || config.Type != ai.ConstrainedSamplingGrammar || !supportsOpenAIGrammarTools {
		return nil, nil
	}
	var lark, regex string
	if config.Variants != nil {
		if config.Variants.OpenAILark != nil {
			lark = *config.Variants.OpenAILark
		}
		if config.Variants.OpenAIRegex != nil {
			regex = *config.Variants.OpenAIRegex
		}
	}
	hasLark := strings.TrimSpace(lark) != ""
	hasRegex := strings.TrimSpace(regex) != ""
	if !hasLark && !hasRegex {
		return nil, fmt.Errorf("Tool %q cannot use grammar constrained sampling: no supported grammar variant was provided.", tool.Name) //nolint:staticcheck // Exact upstream text.
	}
	inputProperty, err := inferGrammarInputProperty(tool)
	if err != nil {
		return nil, fmt.Errorf("Tool %q cannot use grammar constrained sampling: %s.", tool.Name, err) //nolint:staticcheck // Exact upstream text.
	}
	if hasLark {
		return &grammarConstrainedSampling{format: "lark", definition: lark, inputProperty: inputProperty}, nil
	}
	return &grammarConstrainedSampling{format: "regex", definition: regex, inputProperty: inputProperty}, nil
}

func inferGrammarInputProperty(tool ai.Tool) (string, error) {
	var schema struct {
		Type       any                        `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []any                      `json:"required"`
	}
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		return "", err
	}
	if schema.Type != "object" {
		return "", errors.New("grammar constrained sampling requires an object parameter schema")
	}
	if len(schema.Required) != 1 {
		return "", errors.New("grammar constrained sampling requires exactly one required string property")
	}
	inputProperty, ok := schema.Required[0].(string)
	if !ok {
		return "", errors.New("grammar constrained sampling requires exactly one required string property")
	}
	property, ok := schema.Properties[inputProperty]
	if !ok || bytes.Equal(bytes.TrimSpace(property), []byte("null")) {
		return "", fmt.Errorf("grammar constrained sampling requires a properties entry for %s", inputProperty)
	}
	var definition struct {
		Type any `json:"type"`
	}
	if json.Unmarshal(property, &definition) != nil || definition.Type != "string" {
		return "", fmt.Errorf("grammar constrained sampling property %s must have type string", inputProperty)
	}
	return inputProperty, nil
}

func createGrammarToolInputProperties(tools *[]ai.Tool, supportsOpenAIGrammarTools bool) (map[string]string, error) {
	properties := make(map[string]string)
	if tools == nil {
		return properties, nil
	}
	for _, tool := range *tools {
		grammar, err := resolveGrammarConstrainedSampling(tool, supportsOpenAIGrammarTools)
		if err != nil {
			return nil, err
		}
		if grammar != nil {
			properties[tool.Name] = grammar.inputProperty
		}
	}
	return properties, nil
}

// streamParseFloor is the accumulated tool-argument size below which every
// delta re-parses, matching upstream exactly.
const streamParseFloor = 8 << 10

// streamBuffer accumulates streamed deltas. Upstream concatenates with `+=`
// (anthropic-messages.ts:647) because V8 builds a rope; Go copies the whole
// buffer per delta, so the same idiom is quadratic here. strings.Builder
// appends in amortised constant time and String() aliases the buffer without
// copying, so every accumulated value stays byte-identical to upstream's.
type streamBuffer struct {
	accumulated strings.Builder
	parsed      int
}

// append returns accumulated+delta. Re-seeding whenever the caller's value no
// longer matches the buffer keeps the result identical to `accumulated+delta`
// even when something outside the stream replaced the field.
func (buffer *streamBuffer) append(accumulated, delta string) string {
	if buffer.accumulated.Len() != len(accumulated) {
		buffer.accumulated.Reset()
		buffer.accumulated.WriteString(accumulated)
		buffer.parsed = 0
	}
	buffer.accumulated.WriteString(delta)
	return buffer.accumulated.String()
}

// shouldParse gates the streaming re-parse of tool-call arguments. Rebuilding
// the argument map costs O(len(buffer)), so parsing every delta is quadratic
// no matter how the buffer is stored.
//
// ponytail: parse gated at 8 KB of accumulated arguments, then on each
// doubling — below the floor (every fixture, every human-sized tool call) the
// streamed arguments are byte-identical to upstream, above it only the live
// preview lags while partialJson/partialArgs and the end event stay exact.
// Upgrade path: a resumable partial-JSON parser that consumes only the
// appended suffix.
func (buffer *streamBuffer) shouldParse() bool {
	size := buffer.accumulated.Len()
	if size >= streamParseFloor && size < 2*buffer.parsed {
		return false
	}
	buffer.parsed = size
	return true
}

// streamBuffers hands out one accumulator per streamed block index.
type streamBuffers map[int]*streamBuffer

func (buffers streamBuffers) at(index int) *streamBuffer {
	buffer := buffers[index]
	if buffer == nil {
		buffer = &streamBuffer{}
		buffers[index] = buffer
	}
	return buffer
}

func newAssistantMessage(model *ai.Model) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content:    ai.AssistantContent{},
		API:        model.API,
		Provider:   model.Provider,
		Model:      model.ID,
		Usage:      zeroUsage(),
		StopReason: ai.StopReasonPending,
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
	if providerEnvValue("ORB_CACHE_RETENTION", options) == "long" {
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
	var body []byte
	if wire, ok := payload.(openAICompletionsWirePayload); ok {
		// Already compact JSON.stringify output; a second encoder pass only revalidated it.
		body, err = wire.MarshalJSON()
	} else {
		body, err = ai.Marshal(payload)
	}
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI request: %w", err)
	}
	defaults := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}, "Authorization": {"Bearer " + apiKey}}
	return postProviderJSON(ctx, model, options, openAIHTTPClient, strings.TrimRight(model.BaseURL, "/")+"/"+path, defaults, headers, body, openAIResponseError)
}

// postProviderJSON posts body with the default headers, then headers over
// them (an empty value removes one), under the provider retry policy. A status
// of 400 or more is read into errorFor's error.
func postProviderJSON(
	ctx context.Context,
	model *ai.Model,
	options *ai.StreamOptions,
	client openAIHTTPDoer,
	endpoint string,
	defaults, headers http.Header,
	body []byte,
	errorFor func(status int, body []byte) error,
) (*http.Response, error) {
	for name, values := range headers {
		if len(values) == 0 {
			defaults.Del(name)
		} else {
			defaults.Set(name, values[len(values)-1])
		}
	}
	if options != nil && options.HTTPClient != nil {
		client = options.HTTPClient
	}
	doer, err := openAIHeaderTimeoutClient(client, streamTimeoutMS(options), headers)
	if err != nil {
		return nil, err
	}
	var last *http.Response
	response, err := retryProviderRequest(ctx, options, func() (*http.Response, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		request.Header = defaults.Clone()
		attempt, err := doer.Do(request)
		if last != nil && last != attempt && last.Body != nil {
			_ = last.Body.Close()
		}
		last = attempt
		if err != nil || attempt == nil || attempt.StatusCode < http.StatusBadRequest {
			return attempt, err
		}
		contents, err := io.ReadAll(attempt.Body)
		// The header-timeout doer's body wrapper releases its context only on Close.
		_ = attempt.Body.Close()
		if err != nil {
			return attempt, err
		}
		return attempt, &retryableHTTPStatusError{status: attempt.StatusCode, headers: attempt.Header, inner: errorFor(attempt.StatusCode, contents)}
	})
	if statusError, ok := errors.AsType[*retryableHTTPStatusError](err); ok {
		return response, statusError.inner
	}
	if err != nil {
		return response, err
	}
	if response == nil {
		return nil, errors.New("ai/api: provider returned no HTTP response")
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
	base             openAIHTTPDoer
	timeout          time.Duration
	stainlessTimeout *string
}

func streamTimeoutMS(options *ai.StreamOptions) *int64 {
	if options == nil {
		return nil
	}
	return options.TimeoutMS
}

// The pinned JavaScript SDK times fetch only until response headers arrive.
// Its timeout header is added here because openai-go treats the value "0" as
// an internal sentinel, but JavaScript emits "0" for positive subsecond values.
func openAIHeaderTimeoutClient(base openAIHTTPDoer, timeoutMS *int64, headers http.Header) (openAIHTTPDoer, error) {
	timeout := openAIDefaultTimeout
	if timeoutMS != nil && *timeoutMS < 0 {
		return nil, errors.New("timeout must be a positive integer")
	}
	if timeoutMS != nil {
		timeout = time.Duration(*timeoutMS) * time.Millisecond
	}
	stainlessTimeout, headerOverridden := httpHeaderLastValue(headers, "X-Stainless-Timeout")
	if timeoutMS != nil && *timeoutMS > 0 && !headerOverridden {
		value := strconv.FormatInt(*timeoutMS/1000, 10)
		stainlessTimeout = &value
	}
	return &openAIHeaderTimeoutDoer{base: base, timeout: timeout, stainlessTimeout: stainlessTimeout}, nil
}

func (client *openAIHeaderTimeoutDoer) Do(request *http.Request) (*http.Response, error) {
	if client.stainlessTimeout != nil {
		request.Header.Set("X-Stainless-Timeout", *client.stainlessTimeout)
	}
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

func httpHeaderLastValue(headers http.Header, name string) (*string, bool) {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			if len(values) == 0 {
				return nil, true
			}
			value := values[len(values)-1]
			return &value, true
		}
	}
	return nil, false
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

// sseEvent is one server-sent event as the WHATWG event-stream grammar reads
// it; its buffers are reused once handle returns.
type sseEvent struct {
	name []byte // the last "event" field
	data []byte // every "data" field, each followed by "\n"
	raw  []byte // every line, comments included, each followed by "\n"
	eof  bool   // the body ended before a blank line closed the event
}

type sseScanner struct {
	buffer []byte
	event  sseEvent
}

// sseScanners keeps line and event buffers across streams.
var sseScanners = sync.Pool{New: func() any { return &sseScanner{buffer: make([]byte, 0, 4096)} }}

// scanSSE splits body into lines at CR, LF or CRLF and hands handle each event
// a blank line closes, then any lines still pending at EOF as an event marked
// eof. Each provider applies its own data and error rules.
func scanSSE(body io.Reader, handle func(*sseEvent) error) error {
	scanner := sseScanners.Get().(*sseScanner)
	defer sseScanners.Put(scanner)
	event := &scanner.event
	*event = sseEvent{name: event.name[:0], data: event.data[:0], raw: event.raw[:0]}
	buffer, afterCR := scanner.buffer[:0], false
	defer func() { scanner.buffer = buffer }()
	for {
		if len(buffer) == cap(buffer) {
			buffer = slices.Grow(buffer, len(buffer))
		}
		read, readErr := body.Read(buffer[len(buffer):cap(buffer)])
		buffer = buffer[:len(buffer)+read]
		start := 0
		for start < len(buffer) {
			if afterCR {
				afterCR = false
				if buffer[start] == '\n' {
					start++
					continue
				}
			}
			line := buffer[start:]
			end := bytes.IndexByte(line, '\n')
			if end >= 0 {
				line = line[:end]
			}
			if cr := bytes.IndexByte(line, '\r'); cr >= 0 {
				end, afterCR = cr, true
			}
			if end < 0 {
				break
			}
			if err := event.addLine(line[:end], handle); err != nil {
				return err
			}
			start += end + 1
		}
		buffer = buffer[:copy(buffer, buffer[start:])]
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if len(buffer) > 0 {
			_ = event.addLine(buffer, handle) // a non-blank line never dispatches
		}
		if len(event.raw) == 0 {
			return nil
		}
		event.eof = true
		return handle(event)
	}
}

func (event *sseEvent) addLine(line []byte, handle func(*sseEvent) error) error {
	if len(line) == 0 {
		if len(event.raw) == 0 {
			return nil
		}
		err := handle(event)
		event.name, event.data, event.raw = event.name[:0], event.data[:0], event.raw[:0]
		return err
	}
	event.raw = append(append(event.raw, line...), '\n')
	name, value, _ := bytes.Cut(line, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	switch string(name) {
	case "event":
		event.name = append(event.name[:0], value...)
	case "data":
		event.data = append(append(event.data, value...), '\n')
	}
	return nil
}

// errSSEDone is "[DONE]" ending an OpenAI-style stream.
var errSSEDone = errors.New("ai/api: SSE done")

// readSSE hands each event's data to handle, as the OpenAI SDK's stream did:
// "[DONE]" ends the stream, a top-level "error" member fails it, and an event
// the body ends before closing is dropped.
func readSSE(body io.Reader, handle func(json.RawMessage) error) error {
	err := scanSSE(body, func(event *sseEvent) error {
		data := event.data
		if len(data) == 0 || event.eof {
			return nil
		}
		if bytes.HasPrefix(data, []byte("[DONE]")) {
			return errSSEDone
		}
		var raw json.RawMessage
		if jsonwire.Valid(data) {
			raw = bytes.Clone(bytes.TrimSpace(data))
		} else if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		// An error member, matched as encoding/json matches a struct field,
		// fails the stream.
		var streamError []byte
		jsonwire.EachMember(raw, func(name, value []byte) bool {
			if bytes.EqualFold(name, []byte("error")) {
				streamError = value
			}
			return true
		})
		if streamError != nil {
			message := string(streamError)
			if text, err := jsonwire.UnmarshalString(streamError); err == nil {
				message = text
			} else if message == "null" {
				message = ""
			}
			return fmt.Errorf("received error while streaming: %s", message)
		}
		return handle(raw)
	})
	if errors.Is(err, errSSEDone) {
		return nil
	}
	return err
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
	bodyError, ok := errors.AsType[*openAIBodyError](err)
	if !ok {
		return err.Error()
	}
	body := truncateOpenAIErrorText(extractOpenAIErrorBody(bodyError.raw))
	if prefix != "" {
		return fmt.Sprintf("%s (%d): %s", prefix, bodyError.status, body)
	}
	return fmt.Sprintf("%d: %s", bodyError.status, body)
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
	if bodyError, ok := errors.AsType[*openAIBodyError](err); ok {
		return openRouterMetadataFromErrorBody([]byte(bodyError.raw))
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
	if json.Unmarshal(parsed.Metadata.Raw, &value) != nil || !jsValueTruthy(value) {
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

// openAIBodyError is a response whose body held an "error" object.
type openAIBodyError struct {
	status int
	raw    string
}

func (err *openAIBodyError) Error() string { return fmt.Sprintf("%d %s", err.status, err.raw) }

// openAIResponseError keeps an "error" object apart, as the OpenAI SDK did,
// and gives every other body the status error.
func openAIResponseError(status int, contents []byte) error {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(contents, &envelope) == nil && bytes.HasPrefix(envelope.Error, []byte("{")) && extractOpenAIErrorBody(string(envelope.Error)) != "" {
		return &openAIBodyError{status: status, raw: string(envelope.Error)}
	}
	return newOpenAIStatusError(status, contents)
}

func newOpenAIStatusError(status int, contents []byte) error {
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
	if json.Unmarshal(normalized, &value) != nil || !jsValueTruthy(value) {
		return &openAIStatusError{status: status, message: statusOnly}
	}
	serialized := string(normalized)
	messageValue := ""
	if object, ok := value.(map[string]any); ok {
		if candidate, ok := object["message"]; ok && jsValueTruthy(candidate) {
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

// jsValueTruthy is JavaScript truthiness of a decoded JSON value.
func jsValueTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case float64:
		return typed != 0
	case json.Number:
		number, err := strconv.ParseFloat(string(typed), 64)
		return err != nil || number != 0
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
	message := formatOpenAIError(err, prefix)
	if ctx.Err() != nil {
		reason = ai.StopReasonAborted
		// Upstream aborts surface the plain abort Error's message, never the
		// transport error text; the exact string is observable in the TUI.
		message = "Request was aborted"
	}
	output.StopReason = reason
	output.ErrorMessage = &message
	return ai.ErrorEvent{Reason: reason, Error: output}
}

// mergedSamplingParams is Object.assign({}, model.samplingParams,
// options.samplingParams): request keys override model defaults.
func mergedSamplingParams(model *ai.Model, options *ai.StreamOptions) map[string]any {
	merged := map[string]any{}
	maps.Copy(merged, model.SamplingParams)
	if options != nil {
		maps.Copy(merged, options.SamplingParams)
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

// assignJSONObject applies Object.assign to an encoded object: existing keys
// keep their position, new keys follow in sorted order.
func assignJSONObject(encoded []byte, values map[string]any) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	object := jsonwire.OrderedObject{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object = append(object, jsonwire.OrderedMember{Name: key.(string), Value: value})
	}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		object.Set(key, values[key])
	}
	return jsonwire.Marshal(object)
}

// withStreamEvents hands each raw stream event to OnProviderStreamEvent before
// handle normalizes it.
func withStreamEvents(ctx context.Context, options *ai.StreamOptions, model *ai.Model, handle func(json.RawMessage) error) func(json.RawMessage) error {
	if options == nil || options.OnProviderStreamEvent == nil {
		return handle
	}
	return func(raw json.RawMessage) error {
		options.OnProviderStreamEvent(ctx, raw, model)
		return handle(raw)
	}
}

// emitProviderStreamEvent hands a decoded stream event to OnProviderStreamEvent.
func emitProviderStreamEvent(ctx context.Context, options *ai.StreamOptions, model *ai.Model, event any) {
	if options == nil || options.OnProviderStreamEvent == nil {
		return
	}
	if encoded, err := ai.Marshal(event); err == nil {
		options.OnProviderStreamEvent(ctx, encoded, model)
	}
}
