// Package bedrock is Orb's Bedrock ConverseStream transport: the request,
// signed with SigV4 or a bearer token, goes over net/http, and the response
// is read from the AWS event stream framing. The wire shape lives in ai/api.
// Where the request options leave the region or the credentials open, native
// hosts resolve them through the AWS shared config chain (chain.go); a
// js/wasm host has no such chain.
package bedrock

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/ai/api"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// maxCapturedErrorBytes bounds a kept error body; ai/api reads at most
// 4001 bytes (its provider error limit plus one) of it.
const maxCapturedErrorBytes = 4001

// Provider registers Bedrock ConverseStream on Orb's transport.
func Provider() api.Provider { return api.BedrockConverse(Backend()) }

// Backend is Orb's implementation of api.BedrockBackend.
func Backend() api.BedrockBackend { return backend(nil) }

// backend lets tests route requests through their own HTTP client.
func backend(client *http.Client) api.BedrockBackend {
	return api.BedrockBackend{
		NewTransport: func(ctx context.Context, config api.BedrockTransportConfig) (api.BedrockTransport, error) {
			return newTransport(ctx, config, client)
		},
		HTTPErrorBodies:   httpErrorBodies,
		HTTPErrorMetadata: httpErrorMetadata,
	}
}

type transport struct {
	client      *http.Client
	endpoint    string
	region      string
	config      api.BedrockTransportConfig
	credentials func(context.Context) (api.BedrockCredentials, error)
}

// defaultClient sends through the default transport, reusing its connections.
var defaultClient = &http.Client{}

func newTransport(ctx context.Context, config api.BedrockTransportConfig, client *http.Client) (api.BedrockTransport, error) {
	if client == nil {
		client = defaultClient
		if config.Proxy != nil || config.ForceHTTP1 {
			transport := &http.Transport{MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
			if config.Proxy != nil {
				transport.Proxy = http.ProxyURL(config.Proxy)
			}
			if config.ForceHTTP1 {
				transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
			}
			client = &http.Client{Transport: transport}
		}
	}
	region, credentials := config.Region, func(context.Context) (api.BedrockCredentials, error) { return *config.Credentials, nil }
	if needCredentials := config.Credentials == nil && config.BearerToken == ""; region == "" || needCredentials {
		chainRegion, chainCredentials, err := awsChain(ctx, config, client)
		if err != nil {
			return nil, err
		}
		if region == "" {
			region = chainRegion
		}
		if needCredentials {
			credentials = chainCredentials
		}
	}
	endpoint := ""
	switch {
	case config.Endpoint != nil:
		endpoint = strings.TrimSuffix(*config.Endpoint, "/")
	case region == "":
		return nil, operationError(errors.New("resolve endpoint: endpoint rule error, Invalid Configuration: Missing Region"))
	default:
		endpoint = bedrockEndpoint(region, config.UseFIPS, config.UseDualStack)
	}
	return &transport{client: client, endpoint: endpoint, region: region, config: config, credentials: credentials}, nil
}

// bedrockEndpoint follows the SDK's endpoint rules: the region's partition
// names the DNS suffix, and FIPS or dual-stack endpoints can be asked for.
func bedrockEndpoint(region string, fips, dualStack bool) string {
	suffix, dualStackSuffix := "amazonaws.com", "api.aws"
	for _, partition := range [...][3]string{
		{"cn-", "amazonaws.com.cn", "api.amazonwebservices.com.cn"},
		{"eusc-de-", "amazonaws.eu", "api.amazonwebservices.eu"},
		{"us-iso-", "c2s.ic.gov", "api.aws.ic.gov"},
		{"us-isob-", "sc2s.sgov.gov", "api.aws.scloud"},
		{"eu-isoe-", "cloud.adc-e.uk", "api.cloud-aws.adc-e.uk"},
		{"us-isof-", "csp.hci.ic.gov", "api.aws.hci.ic.gov"},
	} {
		if strings.HasPrefix(region, partition[0]) {
			suffix, dualStackSuffix = partition[1], partition[2]
		}
	}
	host := "bedrock-runtime"
	if fips {
		host += "-fips"
	}
	if dualStack {
		suffix = dualStackSuffix
	}
	return "https://" + host + "." + region + "." + suffix
}

// maxAttempts and the retry rules below are the SDK's standard retryer,
// which both pi's and Orb's Bedrock clients ran with.
const maxAttempts = 3

func (transport *transport) Send(ctx context.Context, payload *api.BedrockConverseStreamPayload) (api.BedrockResponse, error) {
	body, err := requestBody(payload)
	if err != nil {
		return nil, err
	}
	target := transport.endpoint + "/model/" + escapePath(payload.ModelID) + "/converse-stream"
	for attempt := 1; ; attempt++ {
		response, err := transport.send(ctx, target, body)
		if err == nil {
			return response, nil
		}
		if attempt == maxAttempts || ctx.Err() != nil || !retryable(err) {
			if attempt > 1 && ctx.Err() == nil && retryable(err) {
				err = &retryError{err: err}
			}
			return nil, operationError(err)
		}
		// Full jitter over 2^attempt seconds, as the SDK's legacy backoff.
		timer := time.NewTimer(time.Duration(rand.Float64() * float64(time.Second<<attempt)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, operationError(ctx.Err())
		case <-timer.C:
		}
	}
}

func (transport *transport) send(ctx context.Context, target string, body []byte) (*response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range transport.config.Headers {
		request.Header.Set(name, value)
	}
	if transport.config.BearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+transport.config.BearerToken)
	} else {
		credentials, err := transport.credentials(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to sign request: failed to retrieve credentials: %w", err)
		}
		signV4(request, body, credentials, transport.region, time.Now())
	}
	httpResponse, err := transport.client.Do(request)
	if err != nil {
		return nil, &sendError{err: err}
	}
	requestID := httpResponse.Header.Get("X-Amzn-Requestid")
	if httpResponse.StatusCode >= http.StatusMultipleChoices {
		defer func() { _ = httpResponse.Body.Close() }()
		data, _ := io.ReadAll(io.LimitReader(httpResponse.Body, 1<<20))
		return nil, newResponseError(httpResponse.StatusCode, httpResponse.Header, requestID, data)
	}
	headers := make(map[string]string, len(httpResponse.Header))
	for name, values := range httpResponse.Header {
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	return &response{body: httpResponse.Body, reader: bufio.NewReader(httpResponse.Body), status: httpResponse.StatusCode, requestID: requestID, headers: headers}, nil
}

// escapePath escapes a path segment as the SDK's REST binding does: every
// byte but the unreserved ones, '/' and ':' included.
func escapePath(value string) string {
	var escaped strings.Builder
	for index := 0; index < len(value); index++ {
		if char := value[index]; 'a' <= char && char <= 'z' || 'A' <= char && char <= 'Z' || '0' <= char && char <= '9' || strings.IndexByte("-_.~", char) >= 0 {
			escaped.WriteByte(char)
		} else {
			fmt.Fprintf(&escaped, "%%%02X", char)
		}
	}
	return escaped.String()
}

// signV4 signs request with AWS Signature Version 4 for the bedrock service.
func signV4(request *http.Request, body []byte, credentials api.BedrockCredentials, region string, now time.Time) {
	stamp := now.UTC().Format("20060102T150405Z")
	request.Header.Set("X-Amz-Date", stamp)
	if credentials.SessionToken != "" {
		request.Header.Set("X-Amz-Security-Token", credentials.SessionToken)
	}
	headers := map[string]string{"host": request.URL.Host}
	if request.ContentLength > 0 {
		headers["content-length"] = strconv.FormatInt(request.ContentLength, 10)
	}
	for name, values := range request.Header {
		headers[strings.ToLower(name)] = strings.Join(strings.Fields(strings.Join(values, ",")), " ")
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for name := range headers {
			if name != "authorization" && name != "user-agent" && name != "expect" && name != "x-amzn-trace-id" && name != "transfer-encoding" && !yield(name) {
				return
			}
		}
	})
	var canonical strings.Builder
	// Services other than S3 sign the escaped path escaped once more.
	canonical.WriteString("POST\n" + strings.ReplaceAll(escapePath(request.URL.EscapedPath()), "%2F", "/") + "\n\n")
	for _, name := range names {
		canonical.WriteString(name + ":" + headers[name] + "\n")
	}
	payloadHash := sha256.Sum256(body)
	signedHeaders := strings.Join(names, ";")
	canonical.WriteString("\n" + signedHeaders + "\n" + hex.EncodeToString(payloadHash[:]))
	canonicalHash := sha256.Sum256([]byte(canonical.String()))
	scope := stamp[:8] + "/" + region + "/bedrock/aws4_request"
	key := []byte("AWS4" + credentials.SecretAccessKey)
	for _, part := range []string{stamp[:8], region, "bedrock", "aws4_request"} {
		key = hmacSHA256(key, part)
	}
	signature := hex.EncodeToString(hmacSHA256(key, "AWS4-HMAC-SHA256\n"+stamp+"\n"+scope+"\n"+hex.EncodeToString(canonicalHash[:])))
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+credentials.AccessKeyID+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// requestBody is the ConverseStream request document. Like the SDK, it sends
// only the modeled members: a content block keeps its first set member, and
// hook-injected extras are decoded into their modeled shapes.
func requestBody(payload *api.BedrockConverseStreamPayload) ([]byte, error) {
	if payload == nil {
		return nil, errors.New("Bedrock payload is nil") //nolint:staticcheck // Hook-facing error.
	}
	// Empty lists and enums are omitted, as the SDK omits nil members.
	body := map[string]any{}
	setList := func(object map[string]any, name string, values []any) {
		if len(values) > 0 {
			object[name] = values
		}
	}
	var messages []any
	for _, message := range payload.Messages {
		var content []any
		for _, block := range message.Content {
			converted, err := contentBlock(block)
			if err != nil {
				return nil, err
			}
			if converted != nil {
				content = append(content, converted)
			}
		}
		converted := map[string]any{"role": message.Role}
		setList(converted, "content", content)
		messages = append(messages, converted)
	}
	setList(body, "messages", messages)
	var system []any
	for _, block := range payload.System {
		switch {
		case block.Text != nil:
			system = append(system, map[string]any{"text": *block.Text})
		case block.CachePoint != nil:
			system = append(system, map[string]any{"cachePoint": cachePoint(block.CachePoint)})
		}
	}
	setList(body, "system", system)
	if !payload.InferenceConfigOmitted() {
		config, inference := map[string]any{}, payload.InferenceConfig
		if inference.MaxTokens != nil {
			value := *inference.MaxTokens
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < math.MinInt32 || value > math.MaxInt32 {
				return nil, fmt.Errorf("bedrock maxTokens %g is not an SDK int32 value", value)
			}
			config["maxTokens"] = int32(value)
		}
		if inference.Temperature != nil {
			config["temperature"] = float32(*inference.Temperature)
		}
		if inference.TopP != nil {
			config["topP"] = float32(*inference.TopP)
		}
		if inference.StopSequences != nil {
			config["stopSequences"] = inference.StopSequences
		}
		body["inferenceConfig"] = config
	}
	if payload.AdditionalModelRequestFields != nil {
		body["additionalModelRequestFields"] = payload.AdditionalModelRequestFields
	}
	if payload.ToolConfig != nil {
		var tools []any
		for _, tool := range payload.ToolConfig.Tools {
			var schema any
			if err := json.Unmarshal(tool.ToolSpec.InputSchema.JSON, &schema); err != nil {
				return nil, fmt.Errorf("decode Bedrock tool schema %q: %w", tool.ToolSpec.Name, err)
			}
			spec := map[string]any{"name": tool.ToolSpec.Name, "description": tool.ToolSpec.Description, "inputSchema": map[string]any{"json": schema}}
			if tool.ToolSpec.Strict != nil {
				spec["strict"] = *tool.ToolSpec.Strict
			}
			tools = append(tools, map[string]any{"toolSpec": spec})
		}
		config := map[string]any{}
		setList(config, "tools", tools)
		if choice := toolChoice(payload.ToolConfig.ToolChoice); choice != nil {
			config["toolChoice"] = choice
		}
		body["toolConfig"] = config
	}
	if payload.RequestMetadata != nil {
		body["requestMetadata"] = payload.RequestMetadata
	}
	for name, raw := range payload.Extra {
		value, err := extraMember(name, raw)
		if err != nil {
			return nil, err
		}
		if value != nil {
			body[name] = value
		}
	}
	return jsonwire.Marshal(body)
}

func contentBlock(block api.BedrockContentBlock) (any, error) {
	switch {
	case block.Text != nil:
		return map[string]any{"text": *block.Text}, nil
	case block.Image != nil:
		image, err := imageBlock(block.Image)
		return map[string]any{"image": image}, err
	case block.ToolUse != nil:
		return map[string]any{"toolUse": map[string]any{"toolUseId": block.ToolUse.ToolUseID, "name": block.ToolUse.Name, "input": block.ToolUse.Input}}, nil
	case block.ToolResult != nil:
		var content []any
		for _, item := range block.ToolResult.Content {
			switch {
			case item.Text != nil:
				content = append(content, map[string]any{"text": *item.Text})
			case item.Image != nil:
				image, err := imageBlock(item.Image)
				if err != nil {
					return nil, err
				}
				content = append(content, map[string]any{"image": image})
			}
		}
		result := map[string]any{"toolUseId": block.ToolResult.ToolUseID}
		if len(content) > 0 {
			result["content"] = content
		}
		if block.ToolResult.Status != "" {
			result["status"] = block.ToolResult.Status
		}
		return map[string]any{"toolResult": result}, nil
	case block.ReasoningContent != nil:
		if redacted := block.ReasoningContent.RedactedContent; len(redacted) > 0 {
			return map[string]any{"reasoningContent": map[string]any{"redactedContent": redacted}}, nil
		}
		text := map[string]any{"text": ""}
		if reasoning := block.ReasoningContent.ReasoningText; reasoning != nil {
			text["text"] = reasoning.Text
			if reasoning.Signature != nil {
				text["signature"] = *reasoning.Signature
			}
		}
		return map[string]any{"reasoningContent": map[string]any{"reasoningText": text}}, nil
	case block.CachePoint != nil:
		return map[string]any{"cachePoint": cachePoint(block.CachePoint)}, nil
	}
	return nil, nil
}

// imageBlock re-encodes the image bytes, as decoding and serializing them did.
func imageBlock(image *api.BedrockImageBlock) (map[string]any, error) {
	data, err := base64.StdEncoding.DecodeString(image.Source.Bytes)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"source": map[string]any{"bytes": data}}
	if image.Format != "" {
		result["format"] = image.Format
	}
	return result, nil
}

func cachePoint(point *api.BedrockCachePoint) map[string]any {
	result := map[string]any{}
	if point.Type != "" {
		result["type"] = point.Type
	}
	if point.TTL != nil && *point.TTL != "" {
		result["ttl"] = *point.TTL
	}
	return result
}

func toolChoice(value any) any {
	choice, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if _, ok := choice["auto"]; ok {
		return map[string]any{"auto": map[string]any{}}
	}
	if _, ok := choice["any"]; ok {
		return map[string]any{"any": map[string]any{}}
	}
	if raw, ok := choice["tool"].(map[string]any); ok {
		if name, ok := raw["name"].(string); ok {
			return map[string]any{"tool": map[string]any{"name": name}}
		}
	}
	return nil
}

// extraMember decodes a hook-injected top-level member into its modeled
// shape; upstream passes the hook's return to ConverseStreamCommand, which
// serializes every modeled member and drops the rest. (OT-M7)
func extraMember(name string, raw json.RawMessage) (any, error) {
	var value any
	switch name {
	case "guardrailConfig":
		value = &struct {
			GuardrailIdentifier  *string `json:"guardrailIdentifier,omitempty"`
			GuardrailVersion     *string `json:"guardrailVersion,omitempty"`
			Trace                string  `json:"trace,omitempty"`
			StreamProcessingMode string  `json:"streamProcessingMode,omitempty"`
		}{}
	case "performanceConfig":
		value = &struct {
			Latency string `json:"latency,omitempty"`
		}{}
	case "serviceTier":
		value = &struct {
			Type string `json:"type,omitempty"`
		}{}
	case "additionalModelResponseFieldPaths":
		value = &[]string{}
	case "promptVariables":
		var variables map[string]struct {
			Text *string `json:"text"`
		}
		if err := json.Unmarshal(raw, &variables); err != nil {
			return nil, fmt.Errorf("decode Bedrock promptVariables: %w", err)
		}
		result := map[string]any{}
		for key, variable := range variables {
			if variable.Text != nil {
				result[key] = map[string]any{"text": *variable.Text}
			}
		}
		return result, nil
	case "outputConfig":
		var wire struct {
			TextFormat *struct {
				Type      string `json:"type"`
				Structure struct {
					JSONSchema *struct {
						Schema      *string `json:"schema,omitempty"`
						Name        *string `json:"name,omitempty"`
						Description *string `json:"description,omitempty"`
					} `json:"jsonSchema"`
				} `json:"structure"`
			} `json:"textFormat"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, fmt.Errorf("decode Bedrock outputConfig: %w", err)
		}
		config := map[string]any{}
		if format := wire.TextFormat; format != nil {
			converted := map[string]any{}
			if format.Type != "" {
				converted["type"] = format.Type
			}
			if schema := format.Structure.JSONSchema; schema != nil {
				converted["structure"] = map[string]any{"jsonSchema": schema}
			}
			config["textFormat"] = converted
		}
		return config, nil
	default:
		return nil, nil
	}
	if err := json.Unmarshal(raw, value); err != nil {
		return nil, fmt.Errorf("decode Bedrock %s: %w", name, err)
	}
	return value, nil
}

// response reads ConverseStream's application/vnd.amazon.eventstream body.
type response struct {
	body      io.ReadCloser
	reader    *bufio.Reader
	headers   map[string]string
	requestID string
	status    int
	err       error
}

func (response *response) Status() int                { return response.status }
func (response *response) RequestID() string          { return response.requestID }
func (response *response) Headers() map[string]string { return response.headers }
func (response *response) Close() error               { return response.body.Close() }
func (response *response) Err() error                 { return response.err }

func (response *response) Next(ctx context.Context) (api.BedrockStreamItem, bool) {
	if response.err != nil || ctx.Err() != nil {
		return api.BedrockStreamItem{}, false
	}
	headers, payload, err := readMessage(response.reader)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			response.err = err
		}
		return api.BedrockStreamItem{}, false
	}
	switch headers[":message-type"] {
	case "event":
		return streamItem(headers[":event-type"], payload), true
	case "exception":
		// The exception's member name, capitalized, is its shape name.
		kind := headers[":exception-type"]
		response.err = &apiError{code: strings.ToUpper(kind[:min(1, len(kind))]) + kind[min(1, len(kind)):], message: modeledMessage(payload), modeled: true}
	case "error":
		response.err = &apiError{code: headers[":error-code"], message: headers[":error-message"]}
	default:
		response.err = fmt.Errorf("unrecognized event stream message type %q", headers[":message-type"])
	}
	return api.BedrockStreamItem{}, false
}

// readMessage reads one event stream message: a prelude of total and header
// lengths with its CRC, the headers, the payload and the message CRC.
func readMessage(reader *bufio.Reader) (map[string]string, []byte, error) {
	var prelude [12]byte
	if _, err := io.ReadFull(reader, prelude[:]); err != nil {
		return nil, nil, err // io.EOF between messages ends the stream
	}
	total, headerLength := binary.BigEndian.Uint32(prelude[0:4]), binary.BigEndian.Uint32(prelude[4:8])
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:12]) {
		return nil, nil, errors.New("event stream prelude checksum mismatch")
	}
	if total < 16 || headerLength > total-16 || total > 24<<20 {
		return nil, nil, fmt.Errorf("invalid event stream message length %d", total)
	}
	// The SDK copies headers and payload through limit readers, so a stream
	// cut short there ends quietly at the missing checksum; only a checksum
	// cut short fails.
	message := make([]byte, total-12)
	if read, err := io.ReadFull(reader, message); err != nil {
		if read > len(message)-4 {
			return nil, nil, io.ErrUnexpectedEOF
		}
		return nil, nil, io.EOF
	}
	body := message[:len(message)-4]
	if crc32.Update(crc32.ChecksumIEEE(prelude[:]), crc32.IEEETable, body) != binary.BigEndian.Uint32(message[len(message)-4:]) {
		return nil, nil, errors.New("event stream message checksum mismatch")
	}
	headers := map[string]string{}
	for data := body[:headerLength]; len(data) > 0; {
		nameLength := int(data[0])
		if len(data) < 2+nameLength {
			return nil, nil, errors.New("invalid event stream header")
		}
		name, kind := string(data[1:1+nameLength]), data[1+nameLength]
		data = data[2+nameLength:]
		if kind > 9 {
			return nil, nil, errors.New("invalid event stream header type")
		}
		// Value sizes by type: true, false, byte, short, int, long, bytes, string, timestamp, uuid.
		size := [...]int{0, 0, 1, 2, 4, 8, 0, 0, 8, 16}[kind]
		if kind == 6 || kind == 7 {
			if len(data) < 2 {
				return nil, nil, errors.New("invalid event stream header")
			}
			size = 2 + int(binary.BigEndian.Uint16(data))
		}
		if len(data) < size {
			return nil, nil, errors.New("invalid event stream header")
		}
		if kind == 7 {
			headers[name] = string(data[2:size])
		}
		data = data[size:]
	}
	return headers, body[headerLength:], nil
}

// streamItem converts one ConverseStream event; unknown events convert to an
// empty item, as the SDK's unknown union member did.
func streamItem(kind string, payload []byte) api.BedrockStreamItem {
	var event struct {
		Role              string `json:"role"`
		ContentBlockIndex int    `json:"contentBlockIndex"`
		StopReason        string `json:"stopReason"`
		Start             struct {
			ToolUse *struct {
				ToolUseID string `json:"toolUseId"`
				Name      string `json:"name"`
			} `json:"toolUse"`
		} `json:"start"`
		Delta struct {
			Text    *string `json:"text"`
			ToolUse *struct {
				Input *string `json:"input"`
			} `json:"toolUse"`
			ReasoningContent *struct {
				Text            *string `json:"text"`
				Signature       *string `json:"signature"`
				RedactedContent []byte  `json:"redactedContent"`
			} `json:"reasoningContent"`
		} `json:"delta"`
		Usage *struct {
			InputTokens           int64 `json:"inputTokens"`
			OutputTokens          int64 `json:"outputTokens"`
			TotalTokens           int64 `json:"totalTokens"`
			CacheReadInputTokens  int64 `json:"cacheReadInputTokens"`
			CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
			CacheDetails          []struct {
				TTL         string `json:"ttl"`
				InputTokens int64  `json:"inputTokens"`
			} `json:"cacheDetails"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(payload, &event)
	item := api.BedrockStreamItem{ContentBlockIndex: event.ContentBlockIndex}
	switch kind {
	case "messageStart":
		item.Kind, item.Role = api.BedrockItemMessageStart, event.Role
	case "contentBlockStart":
		item.Kind = api.BedrockItemContentStart
		if tool := event.Start.ToolUse; tool != nil {
			item.ToolUseID, item.ToolName = tool.ToolUseID, tool.Name
		}
	case "contentBlockDelta":
		item.Kind, item.Text = api.BedrockItemContentDelta, event.Delta.Text
		if tool := event.Delta.ToolUse; tool != nil {
			item.ToolInput = tool.Input
		}
		if reasoning := event.Delta.ReasoningContent; reasoning != nil {
			item.ReasoningText, item.ReasoningSignature, item.RedactedContent = reasoning.Text, reasoning.Signature, reasoning.RedactedContent
		}
	case "contentBlockStop":
		item.Kind = api.BedrockItemContentStop
	case "messageStop":
		item.Kind, item.StopReason = api.BedrockItemMessageStop, event.StopReason
	case "metadata":
		item.Kind = api.BedrockItemMetadata
		if usage := event.Usage; usage != nil {
			item.InputTokens, item.OutputTokens, item.TotalTokens = usage.InputTokens, usage.OutputTokens, usage.TotalTokens
			item.CacheReadTokens, item.CacheWriteTokens = usage.CacheReadInputTokens, usage.CacheWriteInputTokens
			item.CacheDetailsPresent = usage.CacheDetails != nil
			for _, detail := range usage.CacheDetails {
				if detail.TTL == "1h" {
					item.CacheWrite1hTokens += detail.InputTokens
				}
			}
		}
	default:
		return api.BedrockStreamItem{}
	}
	return item
}

// apiError is an AWS error, as ai/api reads one through ErrorCode and
// ErrorMessage. Modeled errors print as the SDK's typed errors, the others as
// its generic API error.
type apiError struct {
	code, message string
	modeled       bool
}

func (err *apiError) ErrorCode() string    { return err.code }
func (err *apiError) ErrorMessage() string { return err.message }
func (err *apiError) Error() string {
	if err.modeled {
		return err.code + ": " + err.message
	}
	return "api error " + err.code + ": " + err.message
}

// modeledErrors are the ConverseStream errors the SDK decodes into types;
// their message is the body's "message" alone.
var modeledErrors = []string{
	"AccessDeniedException", "InternalServerException", "ModelErrorException", "ModelNotReadyException", "ModelStreamErrorException",
	"ModelTimeoutException", "ResourceNotFoundException", "ServiceQuotaExceededException", "ServiceUnavailableException", "ThrottlingException", "ValidationException",
}

// modeledMessage is a modeled error's message member, matched by its exact
// name as the SDK's schema decoder matches it.
func modeledMessage(body []byte) string {
	var members map[string]json.RawMessage
	var message string
	if json.Unmarshal(body, &members) == nil {
		_ = json.Unmarshal(members["message"], &message)
	}
	return message
}

// responseError is an HTTP error response.
type responseError struct {
	err       error
	status    int
	requestID string
	body      string
}

// newResponseError decodes an error response as the SDK's REST-JSON
// protocol: the X-Amzn-ErrorType header, else the body's __type or code,
// names the error; a body that is not JSON fails to deserialize.
func newResponseError(status int, header http.Header, requestID string, body []byte) *responseError {
	result := &responseError{status: status, requestID: requestID, body: string(body[:min(len(body), maxCapturedErrorBytes)])}
	var info struct {
		Type    string `json:"__type"`
		Message string
		Code    any
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&info); err != nil && !errors.Is(err, io.EOF) {
		result.err = fmt.Errorf("deserialization failed, failed to decode response body, %w", err)
		return result
	}
	code := "UnknownError"
	if headerCode := header.Get("X-Amzn-ErrorType"); headerCode != "" {
		code = headerCode
	} else if info.Type != "" {
		code = info.Type
	} else if value, ok := info.Code.(string); ok && value != "" {
		code = value
	}
	if index := strings.Index(code, ":"); index >= 0 {
		code = code[:index]
	}
	if index := strings.Index(code, "#"); index >= 0 {
		code = code[index+1:]
	}
	if slices.Contains(modeledErrors, code) {
		result.err = &apiError{code: code, message: modeledMessage(body), modeled: true}
	} else {
		message := code
		if info.Message != "" {
			message = info.Message
		}
		result.err = &apiError{code: code, message: message}
	}
	return result
}

func (err *responseError) Error() string {
	return "https response error StatusCode: " + strconv.Itoa(err.status) + ", RequestID: " + err.requestID + ", " + err.err.Error()
}
func (err *responseError) Unwrap() error { return err.err }

// sendError is a request that got no response.
type sendError struct{ err error }

func (err *sendError) Error() string {
	return "https response error StatusCode: 0, RequestID: , request send failed, " + err.err.Error()
}
func (err *sendError) Unwrap() error { return err.err }

type retryError struct{ err error }

func (err *retryError) Error() string {
	return "exceeded maximum number of attempts, " + strconv.Itoa(maxAttempts) + ", " + err.err.Error()
}
func (err *retryError) Unwrap() error { return err.err }

type wrappedOperationError struct{ err error }

func operationError(err error) error { return &wrappedOperationError{err: err} }

func (err *wrappedOperationError) Error() string {
	return "operation error Bedrock Runtime: ConverseStream, " + err.err.Error()
}
func (err *wrappedOperationError) Unwrap() error { return err.err }

// retryable follows the SDK's standard retryer: connection errors, 5xx
// gateway statuses, and timeout or throttling error codes.
func retryable(err error) bool {
	if response, ok := errors.AsType[*responseError](err); ok {
		if response.status == 500 || response.status == 502 || response.status == 503 || response.status == 504 {
			return true
		}
		if apiErr, ok := response.err.(*apiError); ok {
			return slices.Contains([]string{
				"RequestTimeout", "RequestTimeoutException", "Throttling", "ThrottlingException", "ThrottledException",
				"RequestThrottledException", "TooManyRequestsException", "ProvisionedThroughputExceededException",
				"TransactionInProgressException", "RequestLimitExceeded", "BandwidthLimitExceeded", "LimitExceededException",
				"RequestThrottled", "SlowDown", "PriorRequestNotComplete", "EC2ThrottledException",
			}, apiErr.code)
		}
		return false
	}
	if _, ok := errors.AsType[*sendError](err); !ok {
		return false
	}
	if dnsError, ok := errors.AsType[*net.DNSError](err); ok && dnsError.IsNotFound {
		return false
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func httpErrorBodies(err error) []api.BedrockHTTPErrorBody {
	if response, ok := errors.AsType[*responseError](err); ok {
		return []api.BedrockHTTPErrorBody{{Status: response.status, Body: strings.NewReader(response.body)}}
	}
	return nil
}

func httpErrorMetadata(err error) (int, string, bool) {
	if response, ok := errors.AsType[*responseError](err); ok {
		return response.status, response.requestID, true
	}
	return 0, "", false
}
