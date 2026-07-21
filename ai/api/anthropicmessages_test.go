package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/models"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
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

func TestAnthropicCopilotDynamicHeaders(t *testing.T) {
	model := anthropicTestModel()
	model.Provider = "github-copilot"
	requestContext := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserContent(&ai.ImageContent{Data: "AA==", MimeType: "image/png"})},
		&ai.AssistantMessage{},
	}}
	headers := anthropicHeaders(model, requestContext, nil, nil)
	if headers.Get("X-Initiator") != "agent" || headers.Get("Openai-Intent") != "conversation-edits" || headers.Get("Copilot-Vision-Request") != "true" {
		t.Fatalf("Copilot headers = %v", headers)
	}

	user := "user-override"
	headers = anthropicHeaders(model, requestContext, &ai.StreamOptions{Headers: ai.ProviderHeaders{"X-Initiator": &user}}, nil)
	if headers.Get("X-Initiator") != user {
		t.Fatalf("options header did not override dynamic header: %v", headers)
	}
}

func TestAnthropicCustomClientSkipsAdapterAuthentication(t *testing.T) {
	requested := false
	headerHookCalled := false
	var requestBody []byte
	var requestHeaders http.Header
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requested = true
		requestHeaders = request.Header.Clone()
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
		&AnthropicMessagesOptions{StreamOptions: ai.StreamOptions{
			APIKey:  &oauthLookingKey,
			Headers: ai.ProviderHeaders{"X-Adapter-Header": stringPointer("adapter-owned")},
			TransformHeaders: func(_ context.Context, headers ai.ProviderHeaders, _ *ai.Model) (ai.ProviderHeaders, error) {
				headerHookCalled = true
				headers["X-Hook-Header"] = stringPointer("hook-owned")
				return headers, nil
			},
		}, Client: &client},
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
	if headerHookCalled || requestHeaders.Get("X-Adapter-Header") != "" || requestHeaders.Get("X-Hook-Header") != "" {
		t.Fatalf("custom client inherited adapter headers: hook=%v headers=%v", headerHookCalled, requestHeaders)
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

// Ports packages/ai/test/anthropic-tool-name-normalization.test.ts: Claude
// Code OAuth tool naming is a case-insensitive round-trip against CC's
// canonical casing, never a mapping between different tool names.
func TestAnthropicClaudeCodeToolNameNormalizationRoundTrip(t *testing.T) {
	toolsNamed := func(names ...string) *[]ai.Tool {
		list := make([]ai.Tool, len(names))
		for index, name := range names {
			list[index] = ai.Tool{Name: name}
		}
		return &list
	}
	// A user-defined tool matching a CC name round-trips: todowrite -> TodoWrite -> todowrite.
	if got := toClaudeCodeToolName("todowrite"); got != "TodoWrite" {
		t.Fatalf("toClaudeCodeToolName(todowrite) = %q, want TodoWrite", got)
	}
	if got := fromClaudeCodeToolName("TodoWrite", toolsNamed("todowrite")); got != "todowrite" {
		t.Fatalf("fromClaudeCodeToolName(TodoWrite) = %q, want todowrite", got)
	}
	// pi's built-in tools convert to CC casing outbound and back to the
	// original lowercase names inbound.
	for lower, canonical := range map[string]string{"read": "Read", "write": "Write", "edit": "Edit", "bash": "Bash"} {
		if got := toClaudeCodeToolName(lower); got != canonical {
			t.Fatalf("toClaudeCodeToolName(%s) = %q, want %s", lower, got, canonical)
		}
		if got := fromClaudeCodeToolName(canonical, toolsNamed(lower)); got != lower {
			t.Fatalf("fromClaudeCodeToolName(%s) = %q, want %s", canonical, got, lower)
		}
	}
	// find is not a CC tool name: it must pass through, never map to Glob.
	if got := toClaudeCodeToolName("find"); got != "find" {
		t.Fatalf("toClaudeCodeToolName(find) = %q, want find", got)
	}
	// The old find->Glob mapping broke here: Glob has no matching context tool.
	if got := fromClaudeCodeToolName("Glob", toolsNamed("find")); got != "Glob" {
		t.Fatalf("fromClaudeCodeToolName(Glob) with only a find tool = %q, want Glob", got)
	}
	// Custom tool names pass through unchanged in both directions.
	if got := toClaudeCodeToolName("my_custom_tool"); got != "my_custom_tool" {
		t.Fatalf("toClaudeCodeToolName(my_custom_tool) = %q", got)
	}
	if got := fromClaudeCodeToolName("my_custom_tool", toolsNamed("my_custom_tool")); got != "my_custom_tool" {
		t.Fatalf("fromClaudeCodeToolName(my_custom_tool) = %q", got)
	}
}

// The two tests below port packages/ai/test/github-copilot-anthropic.test.ts
// adaptive-thinking cases (the dynamic-header case lives in
// TestAnthropicCopilotDynamicHeaders).

func TestAnthropicCopilotAdaptiveThinkingEffortOverrides(t *testing.T) {
	catalog, err := models.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	requireLevel := func(t *testing.T, model *ai.Model, level ai.ModelThinkingLevel, want string) {
		t.Helper()
		if model.ThinkingLevelMap == nil {
			t.Fatalf("%s/%s has no thinkingLevelMap", model.Provider, model.ID)
		}
		mapped := (*model.ThinkingLevelMap)[level]
		if mapped == nil || *mapped != want {
			t.Fatalf("%s/%s thinkingLevelMap[%s] = %v, want %q", model.Provider, model.ID, level, mapped, want)
		}
	}

	opus, ok := catalog.Find("github-copilot", "claude-opus-4.7")
	if !ok {
		t.Fatal("github-copilot/claude-opus-4.7 missing from builtin catalog")
	}
	requireLevel(t, &opus, ai.ModelThinkingMinimal, "low")
	requireLevel(t, &opus, ai.ModelThinkingXHigh, "xhigh")
	requireLevel(t, &opus, ai.ModelThinkingMax, "max")
	supported := ai.SupportedThinkingLevels(&opus)
	if !slices.Contains(supported, ai.ModelThinkingXHigh) || !slices.Contains(supported, ai.ModelThinkingMax) {
		t.Fatalf("opus-4.7 supported levels = %v, want xhigh and max", supported)
	}
	// Adapter side: the map drives the adaptive-thinking effort selection.
	if got := mapAnthropicEffort(&opus, ai.ThinkingMinimal); got != AnthropicEffortLow {
		t.Fatalf("mapAnthropicEffort(opus-4.7, minimal) = %q, want low", got)
	}
	if got := mapAnthropicEffort(&opus, ai.ThinkingXHigh); got != AnthropicEffort("xhigh") {
		t.Fatalf("mapAnthropicEffort(opus-4.7, xhigh) = %q, want xhigh", got)
	}
	if got := mapAnthropicEffort(&opus, ai.ThinkingMax); got != AnthropicEffort("max") {
		t.Fatalf("mapAnthropicEffort(opus-4.7, max) = %q, want max", got)
	}

	sonnet, ok := catalog.Find("github-copilot", "claude-sonnet-4.6")
	if !ok {
		t.Fatal("github-copilot/claude-sonnet-4.6 missing from builtin catalog")
	}
	requireLevel(t, &sonnet, ai.ModelThinkingMinimal, "low")
	requireLevel(t, &sonnet, ai.ModelThinkingMax, "max")
	supported = ai.SupportedThinkingLevels(&sonnet)
	if !slices.Contains(supported, ai.ModelThinkingMax) {
		t.Fatalf("sonnet-4.6 supported levels = %v, want max", supported)
	}
	if slices.Contains(supported, ai.ModelThinkingXHigh) {
		t.Fatalf("sonnet-4.6 supported levels = %v, xhigh must be absent", supported)
	}
	if got := mapAnthropicEffort(&sonnet, ai.ThinkingMinimal); got != AnthropicEffortLow {
		t.Fatalf("mapAnthropicEffort(sonnet-4.6, minimal) = %q, want low", got)
	}
}

func TestAnthropicCopilotAdaptiveThinkingOmitsInterleavedBeta(t *testing.T) {
	catalog, err := models.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	model, ok := catalog.Find("github-copilot", "claude-sonnet-4.6")
	if !ok {
		t.Fatal("github-copilot/claude-sonnet-4.6 missing from builtin catalog")
	}
	interleaved := true
	headers := anthropicHeaders(&model, ai.Context{}, nil, &AnthropicMessagesOptions{InterleavedThinking: &interleaved})
	if strings.Contains(headers.Get("anthropic-beta"), "interleaved-thinking") {
		t.Fatalf("adaptive-thinking Copilot model sent interleaved-thinking beta: %q", headers.Get("anthropic-beta"))
	}
	// Non-adaptive Claude keeps the interleaved-thinking beta.
	control := anthropicHeaders(anthropicTestModel(), ai.Context{}, nil, &AnthropicMessagesOptions{InterleavedThinking: &interleaved})
	if !strings.Contains(control.Get("anthropic-beta"), anthropicInterleavedThinkingBeta) {
		t.Fatalf("non-adaptive model lost interleaved-thinking beta: %q", control.Get("anthropic-beta"))
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

// Gap OA-m6: upstream's github-copilot client branch never adds session
// affinity headers (anthropic-messages.ts:852-898), so copilot must be
// excluded even when compat opts in.
func TestAnthropicCopilotExcludedFromSessionAffinityOAm6(t *testing.T) {
	model := anthropicTestModel()
	model.Compat = json.RawMessage(`{"sendSessionAffinityHeaders":true}`)
	sessionID := "session-1"
	options := &ai.StreamOptions{SessionID: &sessionID}
	headers := anthropicHeaders(model, ai.Context{}, options, nil)
	if headers.Get("x-session-affinity") != sessionID {
		t.Fatalf("api-key provider affinity = %q, want %q", headers.Get("x-session-affinity"), sessionID)
	}
	model.Provider = "github-copilot"
	headers = anthropicHeaders(model, ai.Context{}, options, nil)
	if got := headers.Get("x-session-affinity"); got != "" {
		t.Fatalf("copilot affinity header = %q, want none", got)
	}
}
