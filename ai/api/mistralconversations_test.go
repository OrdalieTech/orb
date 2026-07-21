package api

import (
	"context"
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

func thinkingLevel(value ai.ThinkingLevel) *ai.ThinkingLevel {
	return &value
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
