package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

func TestGoogleSimpleRequiresAPIKey(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash")
	_, err := StreamSimpleGoogleGenerativeAI(context.Background(), model, ai.Context{}, nil)
	if err == nil || err.Error() != "No API key for provider: google" {
		t.Fatalf("missing-key error = %v", err)
	}
}

func TestGooglePayloadHookCanReplaceParameters(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash")
	modelHeader := "model"
	model.Headers = &map[string]string{"X-Model": modelHeader}
	apiKey := "key"
	var captured []byte
	var capturedHeader string
	previous := googleHTTPClient
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var err error
		captured, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		capturedHeader = request.Header.Get("X-Extension")
		return googleTestResponse("data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n"), nil
	})}
	t.Cleanup(func() { googleHTTPClient = previous })

	options := &GoogleOptions{StreamOptions: ai.StreamOptions{APIKey: &apiKey}}
	options.OnPayload = func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
		parameters := payload.(GoogleGenerateContentParameters)
		parameters.Contents = []GoogleContent{{Parts: []GooglePart{{Text: stringPointer("hooked")}}, Role: "user"}}
		return parameters, true, nil
	}
	options.TransformHeaders = func(_ context.Context, headers ai.ProviderHeaders, _ *ai.Model) (ai.ProviderHeaders, error) {
		if headers["X-Model"] == nil || *headers["X-Model"] != "model" {
			t.Fatalf("headers before hook = %#v", headers)
		}
		value := "yes"
		headers["X-Extension"] = &value
		return headers, nil
	}
	stream, err := StreamGoogleGenerativeAIWithOptions(context.Background(), model, ai.Context{}, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ai.Collect(stream); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Contents []GoogleContent `json:"contents"`
	}
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Contents) != 1 || body.Contents[0].Parts[0].Text == nil || *body.Contents[0].Parts[0].Text != "hooked" {
		t.Fatalf("hooked body = %s", captured)
	}
	if capturedHeader != "yes" {
		t.Fatalf("hooked header = %q", capturedHeader)
	}
}

func TestGoogleStreamGeneratesUniqueMissingAndDuplicateToolIDs(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash")
	apiKey := "key"
	previousClient := googleHTTPClient
	previousNow := openAINowUnixMilli
	openAINowUnixMilli = func() int64 { return 123 }
	googleToolCallCounter.Store(0)
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return googleTestResponse("data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"echo\",\"args\":{}}},{\"functionCall\":{\"id\":\"same\",\"name\":\"echo\",\"args\":{}}},{\"functionCall\":{\"id\":\"same\",\"name\":\"echo\",\"args\":{}}}]},\"finishReason\":\"STOP\"}]}\n\n"), nil
	})}
	t.Cleanup(func() {
		googleHTTPClient = previousClient
		openAINowUnixMilli = previousNow
		googleToolCallCounter.Store(0)
	})
	requestContext := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("call echo")},
	}}
	stream, err := StreamGoogleGenerativeAIWithOptions(context.Background(), model, requestContext, &GoogleOptions{StreamOptions: ai.StreamOptions{APIKey: &apiKey}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 3)
	for _, block := range message.Content {
		if call, ok := block.(*ai.ToolCall); ok {
			ids = append(ids, call.ID)
		}
	}
	if len(ids) != 3 || ids[0] != "echo_123_1" || ids[1] != "same" || ids[2] != "echo_123_2" {
		errorMessage := ""
		if message.ErrorMessage != nil {
			errorMessage = *message.ErrorMessage
		}
		t.Fatalf("tool call IDs = %v (stop=%s error=%q)", ids, message.StopReason, errorMessage)
	}
}

func TestGoogleCanceledContextEmitsAborted(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash")
	apiKey := "key"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream, err := StreamGoogleGenerativeAIWithOptions(ctx, model, ai.Context{}, &GoogleOptions{StreamOptions: ai.StreamOptions{APIKey: &apiKey}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonAborted {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if message.ErrorMessage == nil || *message.ErrorMessage != "Request aborted" {
		t.Fatalf("error message = %v", message.ErrorMessage)
	}
}

func TestGoogleThinkingConfiguration(t *testing.T) {
	pro := googleTestModel("gemini-3.1-pro-preview")
	disabled := disabledGoogleThinkingConfig(pro)
	if disabled.ThinkingLevel == nil || *disabled.ThinkingLevel != GoogleThinkingLow || disabled.IncludeThoughts != nil {
		t.Fatalf("disabled Pro config = %#v", disabled)
	}
	flashLite := googleTestModel("gemini-2.5-flash-lite")
	if got := googleThinkingBudget(flashLite, ai.ThinkingMinimal, nil); got == nil || *got != 512 {
		t.Fatalf("flash-lite minimal budget = %v", got)
	}
	custom := 99
	if got := googleThinkingBudget(flashLite, ai.ThinkingHigh, &ai.ThinkingBudgets{High: &custom}); got == nil || *got != 99 {
		t.Fatalf("custom high budget = %v", got)
	}
	uppercaseFlash := googleTestModel("GEMINI-FLASH-LATEST")
	if disabled := disabledGoogleThinkingConfig(uppercaseFlash); disabled.ThinkingLevel == nil || *disabled.ThinkingLevel != GoogleThinkingMinimal {
		t.Fatalf("uppercase Flash disabled config = %#v", disabled)
	}
	uppercase25 := googleTestModel("GEMINI-2.5-FLASH")
	if got := googleThinkingBudget(uppercase25, ai.ThinkingHigh, nil); got == nil || *got != -1 {
		t.Fatalf("uppercase 2.5 budget = %v, want upstream case-sensitive fallback", got)
	}
}

// TestGoogleThinkingBudgetOmittedForUnmappedEffort_OTm9 pins upstream
// getGoogleBudget/getThinkingLevel: xhigh/max efforts without an explicit
// mapping resolve to undefined, so thinkingConfig carries only
// includeThoughts instead of a zero budget or empty level. (OT-m9)
func TestGoogleThinkingBudgetOmittedForUnmappedEffort_OTm9(t *testing.T) {
	flashLite := googleTestModel("gemini-2.5-flash-lite")
	if got := googleThinkingBudget(flashLite, ai.ThinkingXHigh, nil); got != nil {
		t.Fatalf("flash-lite xhigh budget = %v, want omitted", *got)
	}
	if got := googleThinkingBudget(flashLite, ai.ThinkingMax, nil); got != nil {
		t.Fatalf("flash-lite max budget = %v, want omitted", *got)
	}
	if got := googleThinkingLevel(ai.ThinkingXHigh, googleTestModel("gemini-3.1-pro-preview")); got != nil {
		t.Fatalf("gemini-3 pro xhigh level = %v, want omitted", *got)
	}
	if got := googleVertexThinkingBudget(flashLite, ai.ThinkingXHigh, nil); got != nil {
		t.Fatalf("vertex flash-lite xhigh budget = %v, want omitted", *got)
	}

	parameters, err := buildGoogleParameters(flashLite, ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello")},
	}}, &GoogleOptions{Thinking: &GoogleThinkingOptions{Enabled: true, BudgetTokens: googleThinkingBudget(flashLite, ai.ThinkingXHigh, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	config := parameters.Config
	if config.ThinkingConfig == nil || config.ThinkingConfig.IncludeThoughts == nil || !*config.ThinkingConfig.IncludeThoughts {
		t.Fatalf("thinking config = %#v", config.ThinkingConfig)
	}
	if config.ThinkingConfig.ThinkingBudget != nil || config.ThinkingConfig.ThinkingLevel != nil {
		t.Fatalf("thinking config carries an invented budget or level: %#v", config.ThinkingConfig)
	}
}

// TestGoogleStreamUnknownFinishReasonFailsStream_OTm1 pins the streaming
// consequence of google-shared.ts mapStopReason throwing: an unknown
// finishReason turns the stream into an error event carrying the exact
// upstream message. (OT-m1)
func TestGoogleStreamUnknownFinishReasonFailsStream_OTm1(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash")
	apiKey := "key"
	previousClient := googleHTTPClient
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return googleTestResponse("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"BRAND_NEW_REASON\"}]}\n\n"), nil
	})}
	t.Cleanup(func() { googleHTTPClient = previousClient })
	stream, err := StreamGoogleGenerativeAIWithOptions(context.Background(), model, ai.Context{
		Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello")}},
	}, &GoogleOptions{StreamOptions: ai.StreamOptions{APIKey: &apiKey}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	errorMessage := ""
	if message.ErrorMessage != nil {
		errorMessage = *message.ErrorMessage
	}
	if message.StopReason != ai.StopReasonError || errorMessage != "Unhandled stop reason: BRAND_NEW_REASON" {
		t.Fatalf("stop=%q error=%q", message.StopReason, errorMessage)
	}
}

func TestReadGoogleSSEPreservesPrettyMultilineEvent(t *testing.T) {
	input := "data: {\n  \"candidates\": [{\n    \"finishReason\": \"STOP\"\n  }]\n}\n\n"
	var got googleGenerateContentResponse
	if err := readGoogleSSE(strings.NewReader(input), func(raw json.RawMessage) error {
		return json.Unmarshal(raw, &got)
	}); err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].FinishReason != "STOP" {
		t.Fatalf("decoded multiline event = %#v", got)
	}
}

func TestReadGoogleSSEReturnsRawChunkError(t *testing.T) {
	input := `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied"}}`
	err := readGoogleSSE(strings.NewReader(input), func(json.RawMessage) error {
		t.Fatal("raw error chunk reached the SSE event handler")
		return nil
	})
	want := `got status: PERMISSION_DENIED. {"error":{"code":403,"status":"PERMISSION_DENIED","message":"denied"}}`
	if err == nil || err.Error() != want {
		t.Fatalf("raw error = %v, want %q", err, want)
	}
}

func TestGoogleRawChunkErrorUsesJSPresenceAndCoercion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "missing status and numeric string",
			input: `{"10":"ten","2":"two","error":{"code":"4.03e2","message":"\u0061"}}`,
			want:  `got status: undefined. {"2":"two","10":"ten","error":{"code":"4.03e2","message":"a"}}`,
		},
		{
			name:  "empty status",
			input: `{"error":{"code":403,"status":""}}`,
			want:  `got status: . {"error":{"code":403,"status":""}}`,
		},
		{
			name:  "null status",
			input: `{"error":{"code":403,"status":null}}`,
			want:  `got status: null. {"error":{"code":403,"status":null}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := googleSSEChunkError([]byte(test.input))
			if err == nil || err.Error() != test.want {
				t.Fatalf("raw error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGoogleProviderHeadersMatchSDKCaseSensitivePatchThenFetchNormalization(t *testing.T) {
	model := googleTestModel("gemini-2.5-flash")
	modelContentType := "application/model"
	modelRemoved := "model"
	model.Headers = &map[string]string{
		"Content-Type": modelContentType,
		"X-Removed":    modelRemoved,
	}
	lowerContentType := "application/option"
	options := &ai.StreamOptions{Headers: ai.ProviderHeaders{
		"content-type": &lowerContentType,
		"X-Removed":    nil,
	}}
	headers := googleProviderHeaders(model, options)
	if got := headers["Content-Type"]; len(got) != 1 || got[0] != modelContentType {
		t.Fatalf("exact-case Content-Type = %#v", got)
	}
	//nolint:staticcheck // The pre-request map deliberately preserves differently cased upstream keys.
	if got := headers["content-type"]; len(got) != 1 || got[0] != lowerContentType {
		t.Fatalf("differently-cased content-type = %#v", got)
	}
	if _, present := headers["X-Removed"]; present {
		t.Fatalf("same-case null option did not filter the model header: %#v", headers)
	}

	exactOverride := "application/exact-option"
	headers = googleProviderHeaders(model, &ai.StreamOptions{Headers: ai.ProviderHeaders{"Content-Type": &exactOverride}})
	if len(headers) != 4 || headers["Content-Type"][0] != exactOverride {
		t.Fatalf("same-case Content-Type override = %#v", headers)
	}

	headers = googleProviderHeaders(model, &ai.StreamOptions{Headers: ai.ProviderHeaders{"Content-Type": nil}})
	if got := headers["Content-Type"]; len(got) != 1 || got[0] != "application/json" {
		t.Fatalf("null custom Content-Type should expose SDK default: %#v", headers)
	}
}

// TestGoogleProviderHeadersCarrySDKTelemetry_OTm2 pins the x-goog-api-client
// and user-agent telemetry headers the genai 1.52.0 SDK sends on every
// request, shared by the mldev and Vertex posters. (OT-m2)
func TestGoogleProviderHeadersCarrySDKTelemetry_OTm2(t *testing.T) {
	headers := googleProviderHeaders(googleTestModel("gemini-2.5-flash"), nil)
	want := "google-genai-sdk/1.52.0 gl-go/" + runtime.Version()
	if got := headers["User-Agent"]; len(got) != 1 || got[0] != want {
		t.Fatalf("User-Agent = %#v, want %q", got, want)
	}
	var got []string
	for name, values := range headers {
		if strings.EqualFold(name, "x-goog-api-client") {
			got = values
			break
		}
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("x-goog-api-client = %#v, want %q", got, want)
	}
}

func googleTestModel(id string) *ai.Model {
	return &ai.Model{
		ID: id, Name: id, API: ai.APIGoogleGenerativeAI, Provider: "google",
		BaseURL: "https://generativelanguage.googleapis.com/v1beta", Reasoning: true,
		Input: ai.InputModalities{ai.InputText, ai.InputImage}, ContextWindow: 1_000_000, MaxTokens: 65_536,
	}
}

func googleTestResponse(sse string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK",
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(strings.NewReader(sse)),
	}
}

func stringPointer(value string) *string { return &value }
