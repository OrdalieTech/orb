package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

func bedrockTestModel(id, name string) *ai.Model {
	return &ai.Model{
		ID: id, Name: name, API: ai.APIBedrockConverse, Provider: "amazon-bedrock",
		BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", Reasoning: true,
		Input:         ai.InputModalities{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
		ContextWindow: 200_000, MaxTokens: 64_000,
	}
}

func TestBuildBedrockPayloadPreservesUpstreamReplayQuirks(t *testing.T) {
	model := bedrockTestModel("anthropic.claude-sonnet-4-5-20250929-v1:0", "Claude Sonnet 4.5")
	blank := " \n"
	unsigned := " "
	source := &ai.AssistantMessage{
		API: ai.APIOpenAIResponses, Provider: "openai", Model: "gpt-source", StopReason: ai.StopReasonToolUse,
		Content: ai.AssistantContent{
			&ai.ThinkingContent{Thinking: "cross-provider thought", ThinkingSignature: &unsigned},
			&ai.ToolCall{ID: "call:bad/" + strings.Repeat("x", 70), Name: "echo", Arguments: map[string]any{"text": "hi"}},
		},
	}
	toolID := "call:bad/" + strings.Repeat("x", 70)
	requestContext := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserContent(&ai.UnknownContentBlock{}, &ai.TextContent{Text: blank})},
		source,
		&ai.ToolResultMessage{ToolCallID: toolID, ToolName: "echo", Content: ai.ToolResultContent{}, IsError: true},
		&ai.ToolResultMessage{ToolCallID: "second", ToolName: "echo", Content: ai.ToolResultContent{&ai.TextContent{Text: blank}}},
	}}
	retention := ai.CacheRetentionNone
	payload, err := buildBedrockPayload(model, requestContext, &BedrockConverseStreamOptions{
		StreamOptions: ai.StreamOptions{CacheRetention: &retention},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Messages) != 3 {
		t.Fatalf("messages = %#v, want user, assistant, grouped tool results", payload.Messages)
	}
	if got := payload.Messages[0].Content[0].Text; got == nil || *got != bedrockEmptyTextPlaceholder {
		t.Fatalf("empty user fallback = %v", got)
	}
	assistant := payload.Messages[1].Content
	if len(assistant) != 2 || assistant[0].Text == nil || *assistant[0].Text != "cross-provider thought" {
		t.Fatalf("cross-provider thinking was not downgraded to text: %#v", assistant)
	}
	wantID := normalizeBedrockToolCallID(toolID)
	if assistant[1].ToolUse == nil || assistant[1].ToolUse.ToolUseID != wantID || len(wantID) != 64 {
		t.Fatalf("normalized tool use = %#v, want %q", assistant[1].ToolUse, wantID)
	}
	grouped := payload.Messages[2].Content
	if len(grouped) != 2 || grouped[0].ToolResult.ToolUseID != wantID || grouped[0].ToolResult.Status != "error" {
		t.Fatalf("grouped tool results = %#v", grouped)
	}
	if text := grouped[0].ToolResult.Content[0].Text; text == nil || *text != bedrockEmptyTextPlaceholder {
		t.Fatalf("empty tool result fallback = %v", text)
	}
}

func TestBedrockThinkingReplayAndCacheSupport(t *testing.T) {
	model := bedrockTestModel("anthropic.claude-sonnet-4-5-20250929-v1:0", "Claude Sonnet 4.5")
	emptySignature, signature := " ", "signed"
	blocks, err := convertBedrockAssistantContent(ai.AssistantContent{
		&ai.ThinkingContent{Thinking: "unsigned", ThinkingSignature: &emptySignature},
		&ai.ThinkingContent{Thinking: "signed thought", ThinkingSignature: &signature},
	}, model)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 || blocks[0].Text == nil || blocks[1].ReasoningContent == nil || blocks[1].ReasoningContent.ReasoningText.Signature == nil {
		t.Fatalf("Claude replay blocks = %#v", blocks)
	}

	nonClaude := bedrockTestModel("qwen.qwen3", "Qwen")
	blocks, err = convertBedrockAssistantContent(ai.AssistantContent{
		&ai.ThinkingContent{Thinking: "reason", ThinkingSignature: &signature},
	}, nonClaude)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].ReasoningContent == nil || blocks[0].ReasoningContent.ReasoningText.Signature != nil {
		t.Fatalf("non-Claude reasoning replay = %#v", blocks)
	}

	options := &ai.StreamOptions{Env: ai.ProviderEnv{"AWS_BEDROCK_FORCE_CACHE": "1"}}
	if !supportsBedrockPromptCaching(bedrockTestModel("arn:aws:bedrock:us-east-1:1:application-inference-profile/custom", "Custom"), options) {
		t.Fatal("force-cache did not enable application inference profile cache points")
	}
}

func TestBedrockThinkingFieldsMatchModelFamilies(t *testing.T) {
	reasoning := ai.ThinkingXHigh
	display := BedrockThinkingOmitted
	adaptive := bedrockTestModel("arn:aws:bedrock:us-east-1:1:application-inference-profile/custom", "Claude Opus 4.7")
	fields := buildBedrockAdditionalFields(adaptive, &BedrockConverseStreamOptions{
		Reasoning: &reasoning, ThinkingDisplay: &display,
	})
	thinking := fields["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" || thinking["display"] != display || fields["output_config"].(map[string]any)["effort"] != "xhigh" {
		t.Fatalf("adaptive fields = %#v", fields)
	}

	region := "us-gov-west-1"
	fields = buildBedrockAdditionalFields(adaptive, &BedrockConverseStreamOptions{
		Region: region, Reasoning: &reasoning, ThinkingDisplay: &display,
	})
	if _, ok := fields["thinking"].(map[string]any)["display"]; ok {
		t.Fatalf("GovCloud fields contain thinking.display: %#v", fields)
	}

	fixed := bedrockTestModel("anthropic.claude-sonnet-4-5-20250929-v1:0", "Claude Sonnet 4.5")
	budget := 2345
	fields = buildBedrockAdditionalFields(fixed, &BedrockConverseStreamOptions{
		Reasoning: &reasoning, ThinkingBudgets: &ai.ThinkingBudgets{High: &budget},
	})
	if fields["thinking"].(map[string]any)["budget_tokens"] != budget || fields["anthropic_beta"] == nil {
		t.Fatalf("fixed-budget fields = %#v", fields)
	}
}

func TestBedrockRegionEndpointAndCredentialPrecedence(t *testing.T) {
	for _, name := range []string{"AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK"} {
		t.Setenv(name, "")
	}
	model := bedrockTestModel("arn:aws-us-gov:bedrock:us-gov-west-1:1:application-inference-profile/x", "Claude")
	options := &BedrockConverseStreamOptions{Region: "eu-west-1"}
	if got := resolveBedrockRegion(model, options); got != "us-gov-west-1" {
		t.Fatalf("ARN region = %q", got)
	}
	model.ID = "anthropic.claude-sonnet-4-5"
	if got := resolveBedrockRegion(model, options); got != "eu-west-1" {
		t.Fatalf("explicit region = %q", got)
	}
	options.Region = ""
	options.Env = ai.ProviderEnv{"AWS_REGION": "ap-southeast-2"}
	if got := resolveBedrockRegion(model, options); got != "ap-southeast-2" {
		t.Fatalf("scoped region = %q", got)
	}
	options.Env = nil
	model.BaseURL = "https://bedrock-runtime.eu-central-1.amazonaws.com"
	if got := resolveBedrockRegion(model, options); got != "eu-central-1" {
		t.Fatalf("endpoint region = %q", got)
	}
	t.Setenv("AWS_PROFILE", "ambient")
	if got := resolveBedrockRegion(model, options); got != "" {
		t.Fatalf("ambient profile region = %q, want SDK chain", got)
	}
	if shouldUseExplicitBedrockEndpoint(model.BaseURL, "", true) {
		t.Fatal("standard endpoint was pinned over an ambient profile")
	}

	apiKey, bearer := "api-key", "explicit-bearer"
	options.APIKey, options.BearerToken = &apiKey, bearer
	options.Env = ai.ProviderEnv{"AWS_BEARER_TOKEN_BEDROCK": "scoped-bearer"}
	if got := configuredBedrockBearerToken(options); got != bearer {
		t.Fatalf("bearer precedence = %q", got)
	}
	options.BearerToken = ""
	if got := configuredBedrockBearerToken(options); got != apiKey {
		t.Fatalf("API key bearer precedence = %q", got)
	}
	options.Env["AWS_ACCESS_KEY_ID"], options.Env["AWS_SECRET_ACCESS_KEY"], options.Env["AWS_SESSION_TOKEN"] = "access", "secret", "session"
	if access, secret, session, ok := configuredBedrockCredentials(options); !ok || access != "access" || secret != "secret" || session != "session" {
		t.Fatalf("static credentials = %q %q %q %t", access, secret, session, ok)
	}
}

func TestBedrockProxyResolution(t *testing.T) {
	for _, name := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY", "no_proxy", "NO_PROXY"} {
		t.Setenv(name, "")
	}
	options := &ai.StreamOptions{Env: ai.ProviderEnv{
		"HTTPS_PROXY": "scoped-proxy.example:8080",
		"NO_PROXY":    "other.example, .internal.example:443",
	}}
	proxy, err := resolveBedrockHTTPProxy("https://bedrock-runtime.us-east-1.amazonaws.com", options)
	if err != nil || proxy.String() != "https://scoped-proxy.example:8080" {
		t.Fatalf("proxy = %v, err = %v", proxy, err)
	}
	options.Env["NO_PROXY"] = "*.amazonaws.com"
	if proxy, err = resolveBedrockHTTPProxy("https://bedrock-runtime.us-east-1.amazonaws.com", options); err != nil || proxy != nil {
		t.Fatalf("NO_PROXY result = %v, err = %v", proxy, err)
	}
	options.Env["NO_PROXY"], options.Env["HTTPS_PROXY"] = "", "socks5://proxy.example:1080"
	if _, err = resolveBedrockHTTPProxy("https://bedrock-runtime.us-east-1.amazonaws.com", options); err == nil || !strings.Contains(err.Error(), "Unsupported proxy protocol") {
		t.Fatalf("unsupported proxy error = %v", err)
	}
}

func TestBedrockPayloadAndResponseHooks(t *testing.T) {
	previousTransport := newBedrockTransport
	defer func() { newBedrockTransport = previousTransport }()
	response := &fixtureBedrockResponse{
		status: 202, requestID: "request-hook", items: []bedrockStreamItem{
			{Kind: bedrockItemMessageStart, Role: "assistant"},
			{Kind: bedrockItemMessageStop, StopReason: "end_turn"},
		},
	}
	var sent *BedrockConverseStreamPayload
	var sentOptions *BedrockConverseStreamOptions
	newBedrockTransport = func(_ context.Context, _ *ai.Model, options *BedrockConverseStreamOptions) (bedrockTransport, error) {
		sentOptions = options
		return bedrockTransportFunc(func(_ context.Context, payload *BedrockConverseStreamPayload) (bedrockResponse, error) {
			sent = payload
			return response, nil
		}), nil
	}
	model := bedrockTestModel("anthropic.claude-sonnet-4-5", "Claude")
	modelHeader := "model"
	model.Headers = &map[string]string{"X-Model": modelHeader}
	calledResponse := false
	options := &BedrockConverseStreamOptions{StreamOptions: ai.StreamOptions{
		OnPayload: func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
			copy := *(payload.(*BedrockConverseStreamPayload))
			copy.ModelID = "replacement-model"
			return &copy, true, nil
		},
		OnResponse: func(_ context.Context, response ai.ProviderResponse, _ *ai.Model) error {
			calledResponse = response.Status == 202 && response.Headers["x-amzn-requestid"] == "request-hook"
			return nil
		},
		TransformHeaders: func(_ context.Context, headers ai.ProviderHeaders, _ *ai.Model) (ai.ProviderHeaders, error) {
			if headers["X-Model"] == nil || *headers["X-Model"] != "model" {
				t.Fatalf("headers before hook = %#v", headers)
			}
			value := "yes"
			headers["X-Extension"] = &value
			return headers, nil
		},
	}}
	stream, err := StreamBedrockConverseWithOptions(context.Background(), model, ai.Context{
		Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello")}},
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonStop || sent == nil || sent.ModelID != "replacement-model" || !calledResponse || sentOptions == nil || sentOptions.Headers["X-Extension"] == nil || *sentOptions.Headers["X-Extension"] != "yes" {
		t.Fatalf("hook result: message=%#v sent=%#v options=%#v response=%t", message, sent, sentOptions, calledResponse)
	}
}

func TestStreamSimpleDispatchesAndClampsFixedThinking(t *testing.T) {
	previousTransport := newBedrockTransport
	defer func() { newBedrockTransport = previousTransport }()
	var sent *BedrockConverseStreamPayload
	newBedrockTransport = func(context.Context, *ai.Model, *BedrockConverseStreamOptions) (bedrockTransport, error) {
		return bedrockTransportFunc(func(_ context.Context, payload *BedrockConverseStreamPayload) (bedrockResponse, error) {
			sent = payload
			return &fixtureBedrockResponse{items: []bedrockStreamItem{
				{Kind: bedrockItemMessageStart, Role: "assistant"},
				{Kind: bedrockItemMessageStop, StopReason: "end_turn"},
			}}, nil
		}), nil
	}
	model := bedrockTestModel("anthropic.claude-sonnet-4-5-20250929-v1:0", "Claude Sonnet 4.5")
	model.ContextWindow, model.MaxTokens = 20_000, 10_000
	reasoning := ai.ThinkingHigh
	requested := float64(5_000)
	stream, err := StreamSimple(context.Background(), model, ai.Context{
		Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello")}},
	}, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{MaxTokens: &requested}, Reasoning: &reasoning})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ai.Collect(stream); err != nil {
		t.Fatal(err)
	}
	if sent == nil || sent.InferenceConfig.MaxTokens == nil || *sent.InferenceConfig.MaxTokens != model.MaxTokens {
		t.Fatalf("simple maxTokens payload = %#v", sent)
	}
	thinking := sent.AdditionalModelRequestFields["thinking"].(map[string]any)
	if budget := thinking["budget_tokens"].(int); budget > int(*sent.InferenceConfig.MaxTokens-1024) {
		t.Fatalf("thinking budget = %d, exceeds maxTokens reserve", budget)
	}
}

func TestBedrockSDKInputRequiresIntegerMaxTokens(t *testing.T) {
	for _, value := range []float64{3.5, 2_147_483_648} {
		t.Run(fmt.Sprintf("%g", value), func(t *testing.T) {
			_, err := bedrockSDKInput(&BedrockConverseStreamPayload{
				ModelID: "fixture", InferenceConfig: BedrockInferenceConfig{MaxTokens: &value},
			})
			if err == nil || !strings.Contains(err.Error(), "is not an SDK int32 value") {
				t.Fatalf("maxTokens %g error = %v", value, err)
			}
		})
	}
	valid := float64(777)
	input, err := bedrockSDKInput(&BedrockConverseStreamPayload{
		ModelID: "fixture", InferenceConfig: BedrockInferenceConfig{MaxTokens: &valid},
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.InferenceConfig == nil || input.InferenceConfig.MaxTokens == nil || *input.InferenceConfig.MaxTokens != 777 {
		t.Fatalf("SDK maxTokens = %#v", input.InferenceConfig)
	}
}

func TestAWSBedrockTransportAuthenticationHeadersAndErrorBody(t *testing.T) {
	for _, name := range []string{"AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
		t.Setenv(name, "")
	}
	cases := []struct {
		name         string
		options      *BedrockConverseStreamOptions
		authContains string
	}{
		{
			name: "skip-auth-dummy-sigv4",
			options: &BedrockConverseStreamOptions{StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{
				"AWS_BEDROCK_SKIP_AUTH": "1", "AWS_REGION": "us-east-1", "NO_PROXY": "*",
			}}},
			authContains: "Credential=dummy-access-key/",
		},
		{
			name: "static-sigv4",
			options: &BedrockConverseStreamOptions{StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{
				"AWS_ACCESS_KEY_ID": "fixture-access", "AWS_SECRET_ACCESS_KEY": "fixture-secret", "AWS_REGION": "us-east-1", "NO_PROXY": "*",
			}}},
			authContains: "Credential=fixture-access/",
		},
		{
			name:         "bearer",
			options:      &BedrockConverseStreamOptions{Region: "us-east-1", BearerToken: "fixture-bearer", StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{"NO_PROXY": "*"}}},
			authContains: "Bearer fixture-bearer",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			custom, reserved := "custom", "forbidden"
			testCase.options.Headers = ai.ProviderHeaders{
				"x-fixture": &custom, "authorization": &reserved, "x-amz-fixture": &reserved,
			}
			headers, formatted := runBedrockHTTPFailure(t, testCase.options)
			if !strings.Contains(headers.Get("authorization"), testCase.authContains) {
				t.Fatalf("authorization = %q, want substring %q (error: %s)", headers.Get("authorization"), testCase.authContains, formatted)
			}
			if headers.Get("x-fixture") != custom || headers.Get("x-amz-fixture") != "" {
				t.Fatalf("custom/reserved headers = %#v", headers)
			}
			if !strings.Contains(formatted, "403: denied by fixture gateway") {
				t.Fatalf("formatted error = %q", formatted)
			}
		})
	}
}

func TestFormatBedrockErrorPrefixesAndRetentionHint(t *testing.T) {
	err := testBedrockAPIError{code: "ThrottlingException", message: "data retention mode 'default' is unavailable"}
	formatted := formatBedrockError(err)
	if !strings.HasPrefix(formatted, "Throttling error: ") || !strings.Contains(formatted, bedrockDataRetentionDocsURL) {
		t.Fatalf("formatted error = %q", formatted)
	}
}

type bedrockTransportFunc func(context.Context, *BedrockConverseStreamPayload) (bedrockResponse, error)

func (function bedrockTransportFunc) Send(ctx context.Context, payload *BedrockConverseStreamPayload) (bedrockResponse, error) {
	return function(ctx, payload)
}

type testBedrockAPIError struct{ code, message string }

func (err testBedrockAPIError) Error() string        { return err.message }
func (err testBedrockAPIError) ErrorCode() string    { return err.code }
func (err testBedrockAPIError) ErrorMessage() string { return err.message }

func runBedrockHTTPFailure(t *testing.T, options *BedrockConverseStreamOptions) (http.Header, string) {
	t.Helper()
	requests := make(chan http.Header, 1)
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		select {
		case requests <- request.Header.Clone():
		default:
		}
		response.Header().Set("content-type", "text/plain")
		response.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(response, "denied by fixture gateway")
	})
	previousClient := bedrockHTTPClientOverride
	var server *httptest.Server
	if options.BearerToken != "" {
		server = httptest.NewTLSServer(handler)
		bedrockHTTPClientOverride = server.Client()
	} else {
		server = httptest.NewServer(handler)
	}
	defer func() {
		server.Close()
		bedrockHTTPClientOverride = previousClient
	}()
	model := bedrockTestModel("amazon.nova-micro-v1:0", "Nova Micro")
	model.BaseURL = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transport, err := newAWSBedrockTransport(ctx, model, options)
	if err != nil {
		t.Fatal(err)
	}
	_, sendErr := transport.Send(ctx, &BedrockConverseStreamPayload{
		ModelID:         model.ID,
		Messages:        []BedrockMessage{{Role: "user", Content: []BedrockContentBlock{{Text: bedrockStringPointer("hello")}}}},
		InferenceConfig: BedrockInferenceConfig{},
	})
	if sendErr == nil {
		t.Fatal("Bedrock request unexpectedly succeeded")
	}
	select {
	case headers := <-requests:
		return headers, formatBedrockError(sendErr)
	default:
		return http.Header{}, formatBedrockError(sendErr)
	}
}

func bedrockStringPointer(value string) *string { return &value }
