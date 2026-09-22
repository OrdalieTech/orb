// Package bedrock is the AWS SDK backend of the Bedrock ConverseStream API.
// The wire shape lives in ai/api; this package only turns the resolved
// payload and client configuration into SDK calls.
package bedrock

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/api"
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

// maxCapturedErrorBytes bounds the tee'd error body; ai/api reads at most
// 4001 bytes (its provider error limit plus one) of it.
const maxCapturedErrorBytes = 4001

// Provider registers Bedrock ConverseStream on the AWS SDK backend.
func Provider() api.Provider { return api.BedrockConverse(Backend()) }

// StreamSimple streams one Bedrock ConverseStream request on the AWS SDK backend.
func StreamSimple(ctx context.Context, model *ai.Model, requestContext ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
	return Provider().StreamSimple(ctx, model, requestContext, options)
}

// Backend is the AWS SDK implementation of api.BedrockBackend.
func Backend() api.BedrockBackend { return backend(nil) }

// backend lets tests route the SDK through their own HTTP client.
func backend(httpClient aws.HTTPClient) api.BedrockBackend {
	return api.BedrockBackend{
		NewTransport: func(ctx context.Context, config api.BedrockTransportConfig) (api.BedrockTransport, error) {
			return newAWSTransport(ctx, config, httpClient)
		},
		HTTPErrorBodies:   httpErrorBodies,
		HTTPErrorMetadata: httpErrorMetadata,
	}
}

type awsBedrockTransport struct {
	client       *bedrockruntime.Client
	options      []func(*bedrockruntime.Options)
	errorCapture *bedrockErrorCapture
}

func newAWSTransport(ctx context.Context, config api.BedrockTransportConfig, httpClient aws.HTTPClient) (api.BedrockTransport, error) {
	loadOptions := make([]func(*awsconfig.LoadOptions) error, 0, 4)
	if config.Region != "" {
		loadOptions = append(loadOptions, awsconfig.WithRegion(config.Region))
	}
	if config.Profile != "" {
		loadOptions = append(loadOptions, awsconfig.WithSharedConfigProfile(config.Profile))
	}
	if static := config.Credentials; static != nil {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(static.AccessKeyID, static.SecretAccessKey, static.SessionToken)))
	}
	if config.BearerToken != "" {
		loadOptions = append(loadOptions, awsconfig.WithBearerAuthTokenProvider(bearer.StaticTokenProvider{Token: bearer.Token{Value: config.BearerToken}}))
	}
	if config.Proxy != nil || config.ForceHTTP1 {
		client := awshttp.NewBuildableClient().WithTransportOptions(func(transport *http.Transport) {
			if config.Proxy != nil {
				transport.Proxy = http.ProxyURL(config.Proxy)
			}
			if config.ForceHTTP1 {
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
	if httpClient != nil {
		cfg.HTTPClient = httpClient
	}
	errorCapture := &bedrockErrorCapture{}
	cfg.HTTPClient = &bedrockCapturingHTTPClient{next: cfg.HTTPClient, capture: errorCapture}
	client := bedrockruntime.NewFromConfig(cfg, func(clientOptions *bedrockruntime.Options) {
		if config.Endpoint != nil {
			clientOptions.BaseEndpoint = aws.String(*config.Endpoint)
		}
		if config.BearerToken != "" {
			clientOptions.BearerAuthTokenProvider = bearer.StaticTokenProvider{Token: bearer.Token{Value: config.BearerToken}}
			clientOptions.AuthSchemePreference = []string{"httpBearerAuth"}
		}
		if config.SkipAuth {
			clientOptions.BearerAuthTokenProvider = nil
			clientOptions.AuthSchemePreference = nil
		}
	})
	operationOptions := []func(*bedrockruntime.Options){func(operation *bedrockruntime.Options) {
		operation.APIOptions = append(operation.APIOptions, awsmiddleware.AddRawResponseToMetadata)
		for name, value := range config.Headers {
			operation.APIOptions = append(operation.APIOptions, smithyhttp.SetHeaderValue(name, value))
		}
	}}
	return &awsBedrockTransport{client: client, options: operationOptions, errorCapture: errorCapture}, nil
}

func httpErrorBodies(err error) []api.BedrockHTTPErrorBody {
	var bodies []api.BedrockHTTPErrorBody
	var captured *bedrockHTTPResponseError
	if errors.As(err, &captured) {
		bodies = append(bodies, api.BedrockHTTPErrorBody{Status: captured.status, Body: strings.NewReader(captured.body)})
	}
	var responseError *smithyhttp.ResponseError
	if errors.As(err, &responseError) && responseError.Response != nil && responseError.Response.Response != nil && responseError.Response.Body != nil {
		bodies = append(bodies, api.BedrockHTTPErrorBody{Status: responseError.Response.StatusCode, Body: responseError.Response.Body})
	}
	return bodies
}

func httpErrorMetadata(err error) (int, string, bool) {
	var responseError *awshttp.ResponseError
	if !errors.As(err, &responseError) {
		return 0, "", false
	}
	return responseError.HTTPStatusCode(), responseError.ServiceRequestID(), true
}

func (transport *awsBedrockTransport) Send(ctx context.Context, payload *api.BedrockConverseStreamPayload) (api.BedrockResponse, error) {
	input, err := bedrockSDKInput(payload)
	if err != nil {
		return nil, err
	}
	output, err := transport.client.ConverseStream(ctx, input, transport.options...)
	if err != nil {
		if status, body := transport.errorCapture.snapshot(); strings.TrimSpace(body) != "" {
			err = &bedrockHTTPResponseError{err: err, status: status, body: body}
		}
		return nil, err
	}
	stream := output.GetStream()
	if stream == nil {
		return nil, errors.New("Bedrock ConverseStream returned no stream") //nolint:staticcheck // Provider error text.
	}
	status := http.StatusOK
	headers := map[string]string{}
	if raw, ok := awsmiddleware.GetRawResponse(output.ResultMetadata).(*smithyhttp.Response); ok && raw != nil && raw.Response != nil {
		status = raw.StatusCode
		for name, values := range raw.Header {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	return &awsBedrockResponse{stream: stream, status: status, requestID: requestID, headers: headers}, nil
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
	remaining := maxCapturedErrorBytes - len(capture.body)
	if remaining > 0 {
		capture.body = append(capture.body, data[:min(len(data), remaining)]...)
	}
	return len(data), nil
}

func (capture *bedrockErrorCapture) snapshot() (int, string) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.status, string(capture.body)
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

func bedrockSDKInput(payload *api.BedrockConverseStreamPayload) (*bedrockruntime.ConverseStreamInput, error) {
	if payload == nil {
		return nil, errors.New("Bedrock payload is nil") //nolint:staticcheck // Hook-facing error.
	}
	input := &bedrockruntime.ConverseStreamInput{
		ModelId:         aws.String(payload.ModelID),
		RequestMetadata: payload.RequestMetadata,
	}
	if !payload.InferenceConfigOmitted() {
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
				Strict:      tool.ToolSpec.Strict,
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

func bedrockSDKContentBlock(block api.BedrockContentBlock) (bedrocktypes.ContentBlock, error) {
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
		if len(block.ReasoningContent.RedactedContent) > 0 {
			return &bedrocktypes.ContentBlockMemberReasoningContent{Value: &bedrocktypes.ReasoningContentBlockMemberRedactedContent{Value: block.ReasoningContent.RedactedContent}}, nil
		}
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

func bedrockSDKImageBlock(image *api.BedrockImageBlock) (bedrocktypes.ImageBlock, error) {
	bytes, err := base64.StdEncoding.DecodeString(image.Source.Bytes)
	if err != nil {
		return bedrocktypes.ImageBlock{}, err
	}
	return bedrocktypes.ImageBlock{
		Format: bedrocktypes.ImageFormat(image.Format), Source: &bedrocktypes.ImageSourceMemberBytes{Value: bytes},
	}, nil
}

func bedrockSDKCachePoint(point *api.BedrockCachePoint) bedrocktypes.CachePointBlock {
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
	headers map[string]string

	stream    *bedrockruntime.ConverseStreamEventStream
	status    int
	requestID string
}

func (response *awsBedrockResponse) Status() int       { return response.status }
func (response *awsBedrockResponse) RequestID() string { return response.requestID }
func (response *awsBedrockResponse) Close() error      { return response.stream.Close() }
func (response *awsBedrockResponse) Err() error        { return response.stream.Err() }

func (response *awsBedrockResponse) Next(ctx context.Context) (api.BedrockStreamItem, bool) {
	select {
	case <-ctx.Done():
		return api.BedrockStreamItem{}, false
	case event, ok := <-response.stream.Events():
		if !ok {
			return api.BedrockStreamItem{}, false
		}
		return convertBedrockSDKEvent(event), true
	}
}

func convertBedrockSDKEvent(event bedrocktypes.ConverseStreamOutput) api.BedrockStreamItem {
	switch value := event.(type) {
	case *bedrocktypes.ConverseStreamOutputMemberMessageStart:
		return api.BedrockStreamItem{Kind: api.BedrockItemMessageStart, Role: string(value.Value.Role)}
	case *bedrocktypes.ConverseStreamOutputMemberContentBlockStart:
		item := api.BedrockStreamItem{Kind: api.BedrockItemContentStart, ContentBlockIndex: int(aws.ToInt32(value.Value.ContentBlockIndex))}
		if start, ok := value.Value.Start.(*bedrocktypes.ContentBlockStartMemberToolUse); ok {
			item.ToolUseID = aws.ToString(start.Value.ToolUseId)
			item.ToolName = aws.ToString(start.Value.Name)
		}
		return item
	case *bedrocktypes.ConverseStreamOutputMemberContentBlockDelta:
		item := api.BedrockStreamItem{Kind: api.BedrockItemContentDelta, ContentBlockIndex: int(aws.ToInt32(value.Value.ContentBlockIndex))}
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
			case *bedrocktypes.ReasoningContentBlockDeltaMemberRedactedContent:
				item.RedactedContent = reasoning.Value
			}
		}
		return item
	case *bedrocktypes.ConverseStreamOutputMemberContentBlockStop:
		return api.BedrockStreamItem{Kind: api.BedrockItemContentStop, ContentBlockIndex: int(aws.ToInt32(value.Value.ContentBlockIndex))}
	case *bedrocktypes.ConverseStreamOutputMemberMessageStop:
		return api.BedrockStreamItem{Kind: api.BedrockItemMessageStop, StopReason: string(value.Value.StopReason)}
	case *bedrocktypes.ConverseStreamOutputMemberMetadata:
		item := api.BedrockStreamItem{Kind: api.BedrockItemMetadata}
		if value.Value.Usage != nil {
			item.InputTokens = int64(aws.ToInt32(value.Value.Usage.InputTokens))
			item.OutputTokens = int64(aws.ToInt32(value.Value.Usage.OutputTokens))
			item.CacheReadTokens = int64(aws.ToInt32(value.Value.Usage.CacheReadInputTokens))
			item.CacheWriteTokens = int64(aws.ToInt32(value.Value.Usage.CacheWriteInputTokens))
			item.CacheDetailsPresent = value.Value.Usage.CacheDetails != nil
			for _, detail := range value.Value.Usage.CacheDetails {
				if detail.Ttl == bedrocktypes.CacheTTLOneHour {
					item.CacheWrite1hTokens += int64(aws.ToInt32(detail.InputTokens))
				}
			}
			item.TotalTokens = int64(aws.ToInt32(value.Value.Usage.TotalTokens))
		}
		return item
	default:
		return api.BedrockStreamItem{}
	}
}

func (response *awsBedrockResponse) Headers() map[string]string { return response.headers }
