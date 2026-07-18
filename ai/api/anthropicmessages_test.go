package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func TestReadAnthropicSSEAcceptsAllLineEndings(t *testing.T) {
	for _, separator := range []string{"\n", "\r\n", "\r"} {
		t.Run(strings.ReplaceAll(separator, "\r", "CR"), func(t *testing.T) {
			input := strings.Join([]string{
				": heartbeat",
				"event: content_block_delta",
				`data: {"type":"content_block_delta",`,
				`data: "index":0}`,
				"",
			}, separator)
			var eventName, data string
			err := readAnthropicSSE(strings.NewReader(input), func(name string, raw []byte, _ []string) error {
				eventName, data = name, string(raw)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if eventName != "content_block_delta" || data != "{\"type\":\"content_block_delta\",\n\"index\":0}" {
				t.Fatalf("decoded event = %q %q", eventName, data)
			}
		})
	}
}

func TestAnthropicMalformedEventPreservesRawSSELines(t *testing.T) {
	model := anthropicTestModel()
	output := newAssistantMessage(model)
	processor := newAnthropicStreamProcessor(model, ai.Context{}, output, false, func(ai.AssistantMessageEvent) bool { return true })
	err := readAnthropicSSE(strings.NewReader(": heartbeat\nevent: message_delta\ndata: \n\n"), processor.handleSSE)
	want := `Could not parse Anthropic SSE event message_delta: Unexpected end of JSON input; data=; raw=: heartbeat\nevent: message_delta\ndata: `
	if err == nil || err.Error() != want {
		t.Fatalf("malformed event error = %q, want %q", err, want)
	}
}

func TestForceAnthropicStreamingCopiesHookPayload(t *testing.T) {
	payload := &AnthropicMessagesPayload{Model: "retained", Stream: false}
	forced, err := forceAnthropicStreaming(payload)
	if err != nil {
		t.Fatal(err)
	}
	gotPayload, ok := forced.(*AnthropicMessagesPayload)
	if !ok || gotPayload == payload || !gotPayload.Stream || payload.Stream {
		t.Fatalf("forced pointer = %#v; retained pointer = %#v", gotPayload, payload)
	}

	retained := map[string]any{"stream": false, "model": "retained"}
	forced, err = forceAnthropicStreaming(retained)
	if err != nil {
		t.Fatal(err)
	}
	gotMap, ok := forced.(map[string]any)
	if !ok || gotMap["stream"] != true || retained["stream"] != false {
		t.Fatalf("forced map = %#v; retained map = %#v", gotMap, retained)
	}
}

func TestAnthropicMalformedEventUsesPartialJSONRepair(t *testing.T) {
	model := anthropicTestModel()
	output := newAssistantMessage(model)
	processor := newAnthropicStreamProcessor(model, ai.Context{}, output, false, func(ai.AssistantMessageEvent) bool { return true })
	if err := processor.handle("message_delta", []byte("{\"type\":\"message_delta\",\"note\":\"raw\nnewline\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}")); err != nil {
		t.Fatal(err)
	}
	if output.StopReason != ai.StopReasonToolUse || output.Usage.Output != 2 {
		t.Fatalf("repaired event produced stop=%q usage=%#v", output.StopReason, output.Usage)
	}
}

func TestAnthropicCacheRetentionEnvironmentAndExplicitOverride(t *testing.T) {
	model := anthropicTestModel()
	system := "system"
	requestContext := ai.Context{
		SystemPrompt: &system,
		Messages:     ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1}},
	}
	options := &AnthropicMessagesOptions{StreamOptions: ai.StreamOptions{
		Env: ai.ProviderEnv{"PI_CACHE_RETENTION": "long"},
	}}
	payload, _, err := buildAnthropicMessagesPayload(model, requestContext, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.System) != 1 || payload.System[0].CacheControl == nil || payload.System[0].CacheControl.TTL == nil || *payload.System[0].CacheControl.TTL != "1h" {
		t.Fatalf("environment long-retention cache control = %#v", payload.System)
	}
	none := ai.CacheRetentionNone
	options.CacheRetention = &none
	payload, _, err = buildAnthropicMessagesPayload(model, requestContext, options)
	if err != nil {
		t.Fatal(err)
	}
	if payload.System[0].CacheControl != nil {
		t.Fatalf("explicit none retained cache control: %#v", payload.System)
	}
}

func TestAnthropicMissingAuthenticationIsAStreamError(t *testing.T) {
	model := anthropicTestModel()
	stream, err := StreamAnthropicMessagesWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || *message.ErrorMessage != "No API key for provider: anthropic" {
		t.Fatalf("missing-auth result = %#v", message)
	}
}

func TestAnthropicCustomClientSkipsAdapterAuthentication(t *testing.T) {
	requested := false
	var requestBody []byte
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requested = true
		var err error
		requestBody, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(minimalF2SSE(ai.APIAnthropicMessages, "claude-test"))),
			Request:    request,
		}, nil
	})}
	client := anthropic.NewClient(
		option.WithAPIKey("client-owned-key"),
		option.WithBaseURL("https://custom-anthropic.invalid"),
		option.WithHTTPClient(httpClient),
	)
	oauthLookingKey := "sk-ant-oat-client-owned"
	stream, err := StreamAnthropicMessagesWithOptions(
		context.Background(),
		anthropicTestModel(),
		ai.Context{Messages: ai.MessageList{}},
		&AnthropicMessagesOptions{StreamOptions: ai.StreamOptions{APIKey: &oauthLookingKey}, Client: &client},
	)
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !requested || message.StopReason != ai.StopReasonStop {
		t.Fatalf("custom client requested=%v message=%#v", requested, message)
	}
	if bytes.Contains(requestBody, []byte("Claude Code")) {
		t.Fatalf("custom client inherited adapter OAuth identity: %s", requestBody)
	}
}

func TestAnthropicRejectsUnserializableToolReplayAndSchema(t *testing.T) {
	model := anthropicTestModel()
	_, _, err := buildAnthropicMessagesPayload(model, ai.Context{Messages: ai.MessageList{
		&ai.AssistantMessage{Content: ai.AssistantContent{
			&ai.ToolCall{ID: "bad", Name: "echo", Arguments: map[string]any{"value": make(chan int)}},
		}},
	}}, &AnthropicMessagesOptions{})
	if err == nil || !strings.Contains(err.Error(), "tool arguments") {
		t.Fatalf("unserializable arguments error = %v", err)
	}

	tools := []ai.Tool{{Name: "bad-schema", Parameters: jsonschema.Schema(`{`)}}
	_, _, err = buildAnthropicMessagesPayload(model, ai.Context{Tools: &tools}, &AnthropicMessagesOptions{})
	if err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("invalid schema error = %v", err)
	}
}

func anthropicTestModel() *ai.Model {
	return &ai.Model{
		ID: "claude-test", Name: "Claude Test", API: ai.APIAnthropicMessages, Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 1.25}},
		ContextWindow: 200_000, MaxTokens: 4_096,
	}
}
