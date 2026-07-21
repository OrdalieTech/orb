package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

func TestMistralToolCallIDNormalizer(t *testing.T) {
	normalize := createMistralToolCallIDNormalizer()
	if got := normalize("Abc123XYZ"); got != "Abc123XYZ" {
		t.Fatalf("valid tool call ID = %q, want unchanged", got)
	}
	if got := normalize("call.foreign/tool"); got != "wbf5ziha0" {
		t.Fatalf("foreign tool call ID = %q, want upstream hash", got)
	}
	if first, second := normalize("call.foreign/tool"), normalize("call.foreign/tool"); first != second {
		t.Fatalf("normalization is unstable: %q then %q", first, second)
	}
}

func TestMistralUsageCacheFieldVariants(t *testing.T) {
	model := &ai.Model{Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1_000_000, Output: 2_000_000, CacheRead: 3_000_000}}}
	usage := parseMistralUsage([]byte(`{"promptTokens":20,"completionTokens":7,"totalTokens":27,"promptTokensDetails":{"cachedTokens":2}}`), model)
	if usage.Input != 18 || usage.Output != 7 || usage.CacheRead != 2 || usage.TotalTokens != 27 {
		t.Fatalf("camel-case usage = %#v", usage)
	}
	usage = parseMistralUsage([]byte(`{"prompt_tokens":5,"completion_tokens":2,"num_cached_tokens":9}`), model)
	if usage.Input != 0 || usage.Output != 2 || usage.CacheRead != 5 || usage.TotalTokens != 7 {
		t.Fatalf("snake-case usage = %#v", usage)
	}
}

func TestMistralReplayWireShape(t *testing.T) {
	model := &ai.Model{ID: "m", API: ai.APIMistralConversations, Provider: "mistral", Input: ai.InputModalities{ai.InputText}}
	messages := ai.MessageList{
		&ai.AssistantMessage{
			API: ai.APIMistralConversations, Provider: "mistral", Model: "m", StopReason: ai.StopReasonToolUse,
			Content: ai.AssistantContent{&ai.ToolCall{ID: "Abc123XYZ", Name: "echo", Arguments: map[string]any{"text": "first"}}},
		},
		&ai.ToolResultMessage{ToolCallID: "Abc123XYZ", ToolName: "echo", Content: ai.ToolResultContent{&ai.TextContent{Text: "done"}}},
	}
	payload, err := buildMistralPayload(model, ai.Context{Messages: messages}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := ai.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"m","stream":true,"messages":[{"role":"assistant","tool_calls":[{"id":"Abc123XYZ","type":"function","function":{"name":"echo","arguments":"{\"text\":\"first\"}"},"index":0}],"prefix":false},{"role":"tool","content":[{"type":"text","text":"done"}],"tool_call_id":"Abc123XYZ","name":"echo"}]}`
	if string(body) != want {
		t.Fatalf("wire body = %s\nwant      = %s", body, want)
	}
}

func TestMistralSimpleReasoningSelection(t *testing.T) {
	previousClient := mistralHTTPClient
	mistralHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-Extension") != "yes" {
			t.Errorf("hooked header = %q", request.Header.Get("X-Extension"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"id\":\"mistral-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
			)),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { mistralHTTPClient = previousClient })

	tests := []struct {
		name           string
		modelID        string
		reasoning      *ai.ThinkingLevel
		wantEffort     string
		wantPromptMode string
	}{
		{name: "small uses reasoning effort", modelID: "mistral-small-2603", reasoning: thinkingLevel(ai.ThinkingMedium), wantEffort: "high"},
		{name: "medium 3.5 uses reasoning effort", modelID: "mistral-medium-3.5", reasoning: thinkingLevel(ai.ThinkingMedium), wantEffort: "high"},
		{name: "magistral uses prompt mode", modelID: "magistral-medium-latest", reasoning: thinkingLevel(ai.ThinkingMedium), wantPromptMode: "reasoning"},
		{name: "omits controls without reasoning", modelID: "mistral-small-2603"},
		{name: "omits controls when reasoning is off", modelID: "mistral-small-2603", reasoning: thinkingLevel(ai.ThinkingLevel("off"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			apiKey := "test-key"
			model := &ai.Model{
				ID: test.modelID, Name: test.modelID, API: ai.APIMistralConversations, Provider: "mistral",
				BaseURL: "https://mistral.invalid", Reasoning: true, Input: ai.InputModalities{ai.InputText},
				ContextWindow: 128_000, MaxTokens: 8_192,
			}
			var captured *MistralChatPayload
			stream, err := StreamSimpleMistralConversations(context.Background(), model, ai.Context{}, &ai.SimpleStreamOptions{
				StreamOptions: ai.StreamOptions{
					APIKey: &apiKey,
					OnPayload: func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
						captured = payload.(*MistralChatPayload)
						return nil, false, nil
					},
					TransformHeaders: func(_ context.Context, headers ai.ProviderHeaders, _ *ai.Model) (ai.ProviderHeaders, error) {
						value := "yes"
						headers["X-Extension"] = &value
						return headers, nil
					},
				},
				Reasoning: test.reasoning,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ai.Collect(stream); err != nil {
				t.Fatal(err)
			}
			if captured == nil {
				t.Fatal("payload hook was not called")
			}
			if got := optionalString(captured.ReasoningEffort); got != test.wantEffort {
				t.Fatalf("reasoning_effort = %q, want %q", got, test.wantEffort)
			}
			if got := optionalString(captured.PromptMode); got != test.wantPromptMode {
				t.Fatalf("prompt_mode = %q, want %q", got, test.wantPromptMode)
			}
		})
	}
}

// TestMistralNoDefaultRequestDeadline_OTM5 pins upstream buildRequestOptions
// (mistral-conversations.ts:213-238): no request timeout is installed and
// timeoutMs is deliberately ignored, so a caller context without a deadline
// reaches the HTTP client without one. (OT-M5)
func TestMistralNoDefaultRequestDeadline_OTM5(t *testing.T) {
	previousClient := mistralHTTPClient
	sawRequest := false
	mistralHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		sawRequest = true
		if deadline, hasDeadline := request.Context().Deadline(); hasDeadline {
			t.Errorf("request context carries an invented deadline: %v", deadline)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"id\":\"mistral-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
			)),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { mistralHTTPClient = previousClient })

	apiKey := "test-key"
	timeoutMS := int64(50)
	stream, err := StreamMistralConversationsWithOptions(context.Background(), mistralTestModel(), ai.Context{}, &MistralConversationsOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey, TimeoutMS: &timeoutMS},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !sawRequest || message.StopReason != ai.StopReasonStop {
		t.Fatalf("stream result: sawRequest=%t message=%#v", sawRequest, message)
	}
}

// TestMistralCachedTokenFallbackChain_OTm7 pins the upstream ?? fallback chain
// (mistral-conversations.ts:283-292): an empty details object falls through to
// num_cached_tokens, camel-case candidates are consulted before snake-case,
// and a consumed non-number terminates the chain with zero. (OT-m7)
func TestMistralCachedTokenFallbackChain_OTm7(t *testing.T) {
	model := &ai.Model{}
	tests := []struct {
		name       string
		usage      string
		wantCached int64
	}{
		{
			name:       "empty details object falls through to num_cached_tokens",
			usage:      `{"prompt_tokens":10,"prompt_tokens_details":{},"num_cached_tokens":5}`,
			wantCached: 5,
		},
		{
			name:       "null cached tokens falls through",
			usage:      `{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":null},"num_cached_tokens":5}`,
			wantCached: 5,
		},
		{
			name:       "camel-case details win over snake-case",
			usage:      `{"prompt_tokens":10,"promptTokensDetails":{"cachedTokens":3},"prompt_tokens_details":{"cached_tokens":7}}`,
			wantCached: 3,
		},
		{
			name:       "consumed non-number terminates the chain with zero",
			usage:      `{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":"three"},"num_cached_tokens":5}`,
			wantCached: 0,
		},
		{
			name:       "camel num cached wins over snake",
			usage:      `{"prompt_tokens":10,"numCachedTokens":4,"num_cached_tokens":6}`,
			wantCached: 4,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			usage := parseMistralUsage([]byte(test.usage), model)
			if usage.CacheRead != test.wantCached {
				t.Fatalf("cached tokens = %d, want %d", usage.CacheRead, test.wantCached)
			}
		})
	}
}

// TestMistralNonObjectStreamedToolArgsPreserved_OTm8 pins upstream finalize
// behavior (mistral-conversations.ts:472): parseStreamingJson returns valid
// non-object JSON values unchanged at runtime, despite the Record type cast.
// The stream must therefore retain the provider's array. (OT-m8)
func TestMistralNonObjectStreamedToolArgsPreserved_OTm8(t *testing.T) {
	previousClient := mistralHTTPClient
	mistralHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(strings.Join([]string{
				`data: {"id":"mistral-test","choices":[{"index":0,"delta":{"tool_calls":[{"id":"Abc123XYZ","index":0,"function":{"name":"echo","arguments":"[1, 2]"}}]},"finish_reason":null}]}`,
				``,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				``,
				`data: [DONE]`,
				``,
				``,
			}, "\n")),
			),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { mistralHTTPClient = previousClient })

	apiKey := "test-key"
	stream, err := StreamMistralConversationsWithOptions(context.Background(), mistralTestModel(), ai.Context{}, &MistralConversationsOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonToolUse {
		errorMessage := ""
		if message.ErrorMessage != nil {
			errorMessage = *message.ErrorMessage
		}
		t.Fatalf("stop reason = %q (error %q), want toolUse", message.StopReason, errorMessage)
	}
	if len(message.Content) != 1 {
		t.Fatalf("content = %#v, want a single tool call", message.Content)
	}
	call, ok := message.Content[0].(*ai.ToolCall)
	if !ok || call.Name != "echo" {
		t.Fatalf("tool call = %#v", message.Content[0])
	}
	encoded, err := ai.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire.Arguments) != `[1,2]` {
		t.Fatalf("wire arguments = %s, want [1,2]", wire.Arguments)
	}
	value, ok := ai.ToolCallArgumentsValue(call).([]any)
	if !ok || len(value) != 2 || value[0] != float64(1) || value[1] != float64(2) {
		t.Fatalf("runtime arguments = %#v, want [1,2]", ai.ToolCallArgumentsValue(call))
	}
}

func TestOTm8MistralArgumentsTextMatchesJSTruthiness(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "null", raw: `null`, want: `{}`},
		{name: "false", raw: `false`, want: `{}`},
		{name: "zero", raw: `0`, want: `{}`},
		{name: "negative zero", raw: `-0`, want: `{}`},
		{name: "empty string remains string delta", raw: `""`, want: ``},
		{name: "truthy number", raw: `1`, want: `1`},
		{name: "array", raw: `[1,2]`, want: `[1,2]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := mistralArgumentsText(json.RawMessage(test.raw)); got != test.want {
				t.Fatalf("mistralArgumentsText(%s) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func mistralTestModel() *ai.Model {
	return &ai.Model{
		ID: "mistral-small-2603", Name: "Mistral Small", API: ai.APIMistralConversations, Provider: "mistral",
		BaseURL: "https://mistral.invalid", Input: ai.InputModalities{ai.InputText},
		ContextWindow: 128_000, MaxTokens: 8_192,
	}
}

func thinkingLevel(value ai.ThinkingLevel) *ai.ThinkingLevel {
	return &value
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
