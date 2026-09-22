package api

import (
	"context"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
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

func TestBedrockConstrainedSamplingWire(t *testing.T) {
	tools := []ai.Tool{constrainedSamplingTestTool("strict", strictSamplingTestConfig(ai.ConstrainedSamplingPrefer))}
	converted, err := convertBedrockToolConfig(&tools, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if converted.Tools[0].ToolSpec.Strict == nil || !*converted.Tools[0].ToolSpec.Strict {
		t.Fatalf("strict Bedrock tool = %#v", converted.Tools[0])
	}
	converted, err = convertBedrockToolConfig(&tools, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if converted.Tools[0].ToolSpec.Strict != nil {
		t.Fatalf("unsupported Bedrock strict field survived: %#v", converted.Tools[0])
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

func TestBedrockOneHourCacheWriteUsageAndCost(t *testing.T) {
	model := bedrockTestModel("anthropic.claude-opus-4-8", "Claude Opus 4.8")
	output := newAssistantMessage(model)
	processor := bedrockStreamProcessor{model: model, output: output}
	if err := processor.handle(BedrockStreamItem{
		Kind: BedrockItemMetadata, InputTokens: 100, OutputTokens: 5,
		CacheWriteTokens: 1_000_000, CacheWrite1hTokens: 400_000, TotalTokens: 1_000_105,
	}); err != nil {
		t.Fatal(err)
	}
	if output.Usage.CacheWrite1h == nil || *output.Usage.CacheWrite1h != 400_000 {
		t.Fatalf("cacheWrite1h = %#v", output.Usage.CacheWrite1h)
	}
	want := (600_000*model.Cost.CacheWrite + 400_000*model.Cost.Input*2) / 1_000_000
	if output.Usage.Cost.CacheWrite != want {
		t.Fatalf("cache write cost = %v, want %v", output.Usage.Cost.CacheWrite, want)
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
	response := &fixtureBedrockResponse{
		status: 202, requestID: "request-hook", items: []BedrockStreamItem{
			{Kind: BedrockItemMessageStart, Role: "assistant"},
			{Kind: BedrockItemMessageStop, StopReason: "end_turn"},
		},
	}
	var sent *BedrockConverseStreamPayload
	var sentConfig *BedrockTransportConfig
	backend := &BedrockBackend{NewTransport: func(_ context.Context, config BedrockTransportConfig) (BedrockTransport, error) {
		sentConfig = &config
		return bedrockTransportFunc(func(_ context.Context, payload *BedrockConverseStreamPayload) (BedrockResponse, error) {
			sent = payload
			return response, nil
		}), nil
	}}
	model := bedrockTestModel("anthropic.claude-sonnet-4-5", "Claude")
	modelHeader := "model"
	model.Headers = &map[string]string{"X-Model": modelHeader}
	calledResponse := false
	options := &BedrockConverseStreamOptions{Backend: backend, StreamOptions: ai.StreamOptions{
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
	if message.StopReason != ai.StopReasonStop || sent == nil || sent.ModelID != "replacement-model" || !calledResponse || sentConfig == nil || sentConfig.Headers["X-Extension"] != "yes" {
		t.Fatalf("hook result: message=%#v sent=%#v config=%#v response=%t", message, sent, sentConfig, calledResponse)
	}
}

func TestStreamSimpleDispatchesAndClampsFixedThinking(t *testing.T) {
	var sent *BedrockConverseStreamPayload
	registry := NewRegistry(BedrockConverse(BedrockBackend{NewTransport: func(context.Context, BedrockTransportConfig) (BedrockTransport, error) {
		return bedrockTransportFunc(func(_ context.Context, payload *BedrockConverseStreamPayload) (BedrockResponse, error) {
			sent = payload
			return &fixtureBedrockResponse{items: []BedrockStreamItem{
				{Kind: BedrockItemMessageStart, Role: "assistant"},
				{Kind: BedrockItemMessageStop, StopReason: "end_turn"},
			}}, nil
		}), nil
	}}))
	model := bedrockTestModel("anthropic.claude-sonnet-4-5-20250929-v1:0", "Claude Sonnet 4.5")
	model.ContextWindow, model.MaxTokens = 20_000, 10_000
	reasoning := ai.ThinkingHigh
	requested := float64(5_000)
	stream, err := registry.StreamSimple(context.Background(), model, ai.Context{
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

// TestBedrockTypeMismatchedDeltaDropped_OTm5 pins upstream
// handleContentBlockDelta (bedrock-converse-stream.ts:471-518): a delta whose
// block index resolves to a block of a different type is dropped; a new block
// is only created when NO block exists at that stream index. (OT-m5)
func TestBedrockTypeMismatchedDeltaDropped_OTm5(t *testing.T) {
	mismatchedText := "dropped"
	mismatchedReasoning := "also dropped"
	freshText := "kept"
	backend := &BedrockBackend{NewTransport: func(context.Context, BedrockTransportConfig) (BedrockTransport, error) {
		return bedrockTransportFunc(func(context.Context, *BedrockConverseStreamPayload) (BedrockResponse, error) {
			return &fixtureBedrockResponse{items: []BedrockStreamItem{
				{Kind: BedrockItemMessageStart, Role: "assistant"},
				{Kind: BedrockItemContentStart, ContentBlockIndex: 1, ToolUseID: "tool-1", ToolName: "echo"},
				{Kind: BedrockItemContentDelta, ContentBlockIndex: 1, Text: &mismatchedText},
				{Kind: BedrockItemContentDelta, ContentBlockIndex: 1, ReasoningText: &mismatchedReasoning},
				{Kind: BedrockItemContentDelta, ContentBlockIndex: 2, Text: &freshText},
				{Kind: BedrockItemContentStop, ContentBlockIndex: 1},
				{Kind: BedrockItemContentStop, ContentBlockIndex: 2},
				{Kind: BedrockItemMessageStop, StopReason: "tool_use"},
			}}, nil
		}), nil
	}}
	stream, err := StreamBedrockConverseWithOptions(context.Background(), bedrockTestModel("anthropic.claude-sonnet-4-5", "Claude"), ai.Context{
		Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello")}},
	}, &BedrockConverseStreamOptions{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Content) != 2 {
		t.Fatalf("content = %#v, want tool call plus fresh text only", message.Content)
	}
	call, ok := message.Content[0].(*ai.ToolCall)
	if !ok || call.Name != "echo" {
		t.Fatalf("first block = %#v, want the tool call", message.Content[0])
	}
	text, ok := message.Content[1].(*ai.TextContent)
	if !ok || text.Text != freshText {
		t.Fatalf("second block = %#v, want fresh text %q", message.Content[1], freshText)
	}
}

func TestOTm5BedrockEmptyReasoningDeltaOnlyStartsBlock(t *testing.T) {
	empty := ""
	output := &ai.AssistantMessage{Content: ai.AssistantContent{}}
	var events []ai.AssistantMessageEvent
	processor := &bedrockStreamProcessor{
		output: output,
		sink: func(event ai.AssistantMessageEvent) bool {
			events = append(events, event)
			return true
		},
	}
	processor.handleDelta(BedrockStreamItem{ContentBlockIndex: 3, ReasoningText: &empty})
	if len(events) != 1 {
		t.Fatalf("empty initial reasoning delta emitted %d events, want one start: %#v", len(events), events)
	}
	if _, ok := events[0].(ai.ThinkingStartEvent); !ok {
		t.Fatalf("empty initial reasoning event = %T, want ThinkingStartEvent", events[0])
	}
	events = nil
	processor.handleDelta(BedrockStreamItem{ContentBlockIndex: 3, ReasoningText: &empty, ReasoningSignature: &empty})
	if len(events) != 0 {
		t.Fatalf("empty reasoning update emitted events: %#v", events)
	}
}

func TestBedrockRawStopReason(t *testing.T) {
	output := newAssistantMessage(&ai.Model{})
	processor := &bedrockStreamProcessor{output: output, sink: func(ai.AssistantMessageEvent) bool { return true }}
	if err := processor.handle(BedrockStreamItem{Kind: BedrockItemMessageStop, StopReason: "guardrail_intervened"}); err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.StopReasonError || output.RawStopReason == nil || *output.RawStopReason != "guardrail_intervened" {
		t.Fatalf("output = %#v", output)
	}
	if output.ErrorMessage == nil || *output.ErrorMessage != "Provider stopped with: guardrail_intervened" {
		t.Fatalf("error message = %v", output.ErrorMessage)
	}
}

func TestBedrockConfiguredProfileSuppressesStaticCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AWS_PROFILE", "")
	for _, test := range []struct {
		name    string
		options *BedrockConverseStreamOptions
		want    bool
	}{
		{name: "no profile", options: &BedrockConverseStreamOptions{}, want: false},
		{name: "explicit profile", options: &BedrockConverseStreamOptions{Profile: "stored-profile"}, want: true},
		{name: "scoped profile", options: &BedrockConverseStreamOptions{StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{"AWS_PROFILE": "scoped-profile"}}}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := configuredBedrockProfile(test.options); got != test.want {
				t.Fatalf("configured profile = %t, want %t", got, test.want)
			}
		})
	}
	t.Setenv("AWS_PROFILE", "ambient-profile")
	if configuredBedrockProfile(&BedrockConverseStreamOptions{}) {
		t.Fatal("ambient profile counted as an explicit profile")
	}
}

// TestBedrockARNRegionMatchesUpstreamPattern_OTm6 pins the upstream ARN
// region regex (bedrock-converse-stream.ts:166). (OT-m6)
func TestBedrockARNRegionMatchesUpstreamPattern_OTm6(t *testing.T) {
	tests := []struct {
		modelID string
		want    string
	}{
		{"arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.anthropic.claude", "us-east-1"},
		{"arn:aws-us-gov:bedrock:us-gov-west-1:123456789012:inference-profile/profile", "us-gov-west-1"},
		{"arn:aws-cn:bedrock:cn-north-1:123456789012:application-inference-profile/x", "cn-north-1"},
		{"arnX:aws:bedrock:us-east-1:123:profile", ""},
		{"arn:aws:bedrock:US-EAST-1:123:profile", ""},
		{"arn:aws:sagemaker:us-east-1:123:endpoint/x", ""},
		{"arn:awsgov:bedrock:us-gov-west-1:123:profile", ""},
		{"anthropic.claude-sonnet-4-5", ""},
	}
	for _, test := range tests {
		if got := bedrockARNRegion(test.modelID); got != test.want {
			t.Fatalf("bedrockARNRegion(%q) = %q, want %q", test.modelID, got, test.want)
		}
	}
}

func TestFormatBedrockErrorPrefixesAndRetentionHint(t *testing.T) {
	err := testBedrockAPIError{code: "ThrottlingException", message: "data retention mode 'default' is unavailable"}
	formatted := formatBedrockError(err, BedrockBackend{})
	if !strings.HasPrefix(formatted, "Throttling error: ") || !strings.Contains(formatted, bedrockDataRetentionDocsURL) {
		t.Fatalf("formatted error = %q", formatted)
	}
}

type bedrockTransportFunc func(context.Context, *BedrockConverseStreamPayload) (BedrockResponse, error)

func (function bedrockTransportFunc) Send(ctx context.Context, payload *BedrockConverseStreamPayload) (BedrockResponse, error) {
	return function(ctx, payload)
}

type testBedrockAPIError struct{ code, message string }

func (err testBedrockAPIError) Error() string        { return err.message }
func (err testBedrockAPIError) ErrorCode() string    { return err.code }
func (err testBedrockAPIError) ErrorMessage() string { return err.message }
