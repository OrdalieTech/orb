package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func boolPtr(v bool) *bool    { return &v }
func strPtr(v string) *string { return &v }

func TestBedrockSupportsReasoningReplayAndFlatEffort(t *testing.T) {
	t.Run("omits thinking when supportsReasoningReplay is false", func(t *testing.T) {
		compatBytes, _ := json.Marshal(ai.BedrockCompat{SupportsReasoningReplay: boolPtr(false)})
		model := &ai.Model{
			ID:        "moonshotai.kimi-k3.1",
			API:       ai.APIBedrockConverse,
			Provider:  "amazon-bedrock",
			Reasoning: true,
			Compat:    compatBytes,
		}
		ctx := ai.Context{
			Messages: []ai.Message{
				&ai.UserMessage{Content: ai.UserContent{Text: strPtr("hi")}},
				&ai.AssistantMessage{
					API:        ai.APIBedrockConverse,
					Provider:   "amazon-bedrock",
					Model:      model.ID,
					StopReason: ai.StopReasonStop,
					Content: ai.AssistantContent{
						&ai.ThinkingContent{Thinking: "secret chain"},
						&ai.TextContent{Text: "hello"},
					},
				},
				&ai.UserMessage{Content: ai.UserContent{Text: strPtr("next")}},
			},
		}
		messages, err := convertBedrockMessages(ctx, model, ai.CacheRetentionNone, nil)
		if err != nil {
			t.Fatalf("convertBedrockMessages: %v", err)
		}
		if len(messages) != 3 {
			t.Fatalf("got %d messages, want 3", len(messages))
		}
		for _, block := range messages[1].Content {
			if block.ReasoningContent != nil {
				t.Fatalf("expected thinking block to be omitted when supportsReasoningReplay=false")
			}
		}
	})

	t.Run("flat reasoning_effort for non-Claude non-GPT models", func(t *testing.T) {
		model := &ai.Model{
			ID:        "moonshotai.kimi-k3.1",
			API:       ai.APIBedrockConverse,
			Provider:  "amazon-bedrock",
			Reasoning: true,
			ThinkingLevelMap: &map[ai.ModelThinkingLevel]*string{
				ai.ModelThinkingOff:  strPtr("none"),
				ai.ModelThinkingHigh: strPtr("high"),
			},
		}
		high := ai.ThinkingHigh
		gotHigh := buildBedrockAdditionalFields(model, &BedrockConverseStreamOptions{Reasoning: &high})
		if gotHigh == nil || gotHigh["reasoning_effort"] != "high" {
			t.Fatalf("high additional fields = %#v, want reasoning_effort=high", gotHigh)
		}
		gotOff := buildBedrockAdditionalFields(model, &BedrockConverseStreamOptions{})
		if gotOff == nil || gotOff["reasoning_effort"] != "none" {
			t.Fatalf("off additional fields = %#v, want reasoning_effort=none", gotOff)
		}
	})
}

func TestOpenAICompletionsAntLingAlias(t *testing.T) {
	compatBytes, _ := json.Marshal(ai.OpenAICompletionsCompat{
		ThinkingFormat: func() *ai.ThinkingFormat { f := ai.ThinkingFormatAntLing; return &f }(),
	})
	model := &ai.Model{
		ID:        "Ring-2.6-1T",
		API:       ai.APIOpenAICompletions,
		Provider:  "ant-ling",
		BaseURL:   "https://api.ant-ling.com/v1",
		Reasoning: true,
		Compat:    compatBytes,
	}
	resolved, err := resolveOpenAICompletionsCompat(model)
	if err != nil {
		t.Fatalf("resolveOpenAICompletionsCompat: %v", err)
	}
	if resolved.thinkingFormat != ai.ThinkingFormatDeepSeek {
		t.Fatalf("thinkingFormat = %q, want %q", resolved.thinkingFormat, ai.ThinkingFormatDeepSeek)
	}
}

func TestOpenAICompletionsReasoningOpaqueStreamAndReplay(t *testing.T) {
	t.Run("with reasoning_text", func(t *testing.T) {
		model := &ai.Model{ID: "gpt-5", API: ai.APIOpenAICompletions, Provider: "github-copilot"}
		output := &ai.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID}
		state := newCompletionsStreamState(output)
		chunk1 := json.RawMessage(`{"choices":[{"delta":{"reasoning_text":"step 1","reasoning_opaque":"opq_1"}}]}`)
		chunk2 := json.RawMessage(`{"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}`)
		if err := state.consumeChunk(model, chunk1, func(ai.AssistantMessageEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := state.consumeChunk(model, chunk2, func(ai.AssistantMessageEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
		state.applyReasoningDetails()
		if state.thinking == nil || state.thinking.ThinkingSignature == nil {
			t.Fatalf("expected thinking signature to be set")
		}
		converted, ok, err := convertOpenAICompletionsAssistantMessageWithGrammar(completionsMessageSettings{}, output, nil)
		if err != nil || !ok {
			t.Fatalf("convert assistant message: ok=%v err=%v", ok, err)
		}
		if converted["reasoning_text"] != "step 1" {
			t.Fatalf("reasoning_text = %#v, want %q", converted["reasoning_text"], "step 1")
		}
		if converted["reasoning_opaque"] != "opq_1" {
			t.Fatalf("reasoning_opaque = %#v, want %q", converted["reasoning_opaque"], "opq_1")
		}
	})

	t.Run("without reasoning_text", func(t *testing.T) {
		model := &ai.Model{ID: "gpt-5", API: ai.APIOpenAICompletions, Provider: "github-copilot"}
		output := &ai.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID}
		state := newCompletionsStreamState(output)
		chunk := json.RawMessage(`{"choices":[{"delta":{"reasoning_opaque":"opq_only","content":"ok"},"finish_reason":"stop"}]}`)
		if err := state.consumeChunk(model, chunk, func(ai.AssistantMessageEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
		state.applyReasoningDetails()
		converted, ok, err := convertOpenAICompletionsAssistantMessageWithGrammar(completionsMessageSettings{}, output, nil)
		if err != nil || !ok {
			t.Fatalf("convert assistant message: ok=%v err=%v", ok, err)
		}
		if converted["reasoning_opaque"] != "opq_only" {
			t.Fatalf("reasoning_opaque = %#v, want %q", converted["reasoning_opaque"], "opq_only")
		}
		if _, exists := converted["reasoning_text"]; exists {
			t.Fatalf("unexpected reasoning_text when opaque arrived without text")
		}
	})
}

func TestOpenAICompletionsOpenRouterNativeFinishReason(t *testing.T) {
	t.Run("empty stop with non-clean native_finish_reason becomes error", func(t *testing.T) {
		model := &ai.Model{ID: "google/gemini-3-pro", API: ai.APIOpenAICompletions, Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}
		output := &ai.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID}
		state := newCompletionsStreamState(output)
		state.isOpenRouter = true
		chunk := json.RawMessage(`{"choices":[{"delta":{"reasoning":"hmm"},"finish_reason":"stop","native_finish_reason":"MALFORMED_RESPONSE"}]}`)
		if err := state.consumeChunk(model, chunk, func(ai.AssistantMessageEvent) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if output.RawStopReason == nil || *output.RawStopReason != "stop.MALFORMED_RESPONSE" {
			t.Fatalf("rawStopReason = %v, want stop.MALFORMED_RESPONSE", output.RawStopReason)
		}
		flagOpenRouterEmptyStop(output, state.nativeFinishReason)
		if output.StopReason != ai.StopReasonError {
			t.Fatalf("stopReason = %q, want error", output.StopReason)
		}
		if output.ErrorMessage == nil || !strings.Contains(*output.ErrorMessage, "MALFORMED_RESPONSE") {
			t.Fatalf("errorMessage = %v, want MALFORMED_RESPONSE", output.ErrorMessage)
		}
	})

	t.Run("empty stop with clean end_turn stays stop", func(t *testing.T) {
		output := &ai.AssistantMessage{StopReason: ai.StopReasonStop}
		flagOpenRouterEmptyStop(output, "end_turn")
		if output.StopReason != ai.StopReasonStop {
			t.Fatalf("stopReason = %q, want stop", output.StopReason)
		}
	})
}

func TestGoogleCompatStrictModeXHighAndPromptBlock(t *testing.T) {
	t.Run("compat supportsStrictMode overrides model ID detection", func(t *testing.T) {
		trueCompat, _ := json.Marshal(ai.GoogleCompat{SupportsStrictMode: boolPtr(true)})
		falseCompat, _ := json.Marshal(ai.GoogleCompat{SupportsStrictMode: boolPtr(false)})
		if !supportsGoogleStrictToolSampling(&ai.Model{ID: "custom-alias", Compat: trueCompat}) {
			t.Fatal("expected compat supportsStrictMode=true to override non-Gemini-3 ID")
		}
		if supportsGoogleStrictToolSampling(&ai.Model{ID: "gemini-3-pro-preview", Compat: falseCompat}) {
			t.Fatal("expected compat supportsStrictMode=false to override Gemini-3 ID")
		}
		if !supportsGoogleStrictToolSampling(&ai.Model{ID: "gemini-3-pro-preview"}) {
			t.Fatal("expected default Gemini-3 ID detection to remain true")
		}
	})

	t.Run("xhigh thinking level", func(t *testing.T) {
		model := &ai.Model{ID: "gemini-3.1-pro-preview", API: ai.APIGoogleGenerativeAI, Provider: "google", Reasoning: true}
		resolved, err := resolveGoogleThinkingLevel(model, ai.ThinkingXHigh)
		if err != nil {
			t.Fatalf("resolveGoogleThinkingLevel(xhigh): %v", err)
		}
		if resolved != ai.ThinkingXHigh {
			t.Fatalf("resolved = %q, want xhigh", resolved)
		}
		level := googleThinkingLevel(resolved)
		if level == nil || *level != GoogleThinkingXHigh {
			t.Fatalf("googleThinkingLevel = %v, want XHIGH", level)
		}
	})

	t.Run("prompt block error", func(t *testing.T) {
		sseBody := "data: {\"promptFeedback\":{\"blockReason\":\"SAFETY\",\"blockReasonMessage\":\"Blocked by policy\",\"safetyRatings\":[{\"category\":\"HARM_CATEGORY_DANGEROUS_CONTENT\",\"probability\":\"HIGH\"}]}}\n\n"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(sseBody))
		}))
		defer server.Close()
		model := &ai.Model{
			ID:       "gemini-3-pro-preview",
			API:      ai.APIGoogleGenerativeAI,
			Provider: "google",
			BaseURL:  server.URL,
		}
		stream, err := StreamGoogleGenerativeAIWithOptions(context.Background(), model, ai.Context{
			Messages: []ai.Message{&ai.UserMessage{Content: ai.UserContent{Text: strPtr("test")}}},
		}, &GoogleOptions{
			StreamOptions: ai.StreamOptions{APIKey: strPtr("test-key")},
		})
		if err != nil {
			t.Fatalf("StreamGoogleGenerativeAIWithOptions: %v", err)
		}
		var errEvent *ai.ErrorEvent
		for event, iterErr := range stream {
			if iterErr != nil {
				t.Fatalf("stream iter error: %v", iterErr)
			}
			if e, ok := event.(ai.ErrorEvent); ok {
				errEvent = &e
			}
		}
		if errEvent == nil || errEvent.Error == nil || errEvent.Error.ErrorMessage == nil {
			t.Fatalf("expected error event for prompt block")
		}
		got := *errEvent.Error.ErrorMessage
		want := "Google prompt blocked: SAFETY. Blocked by policy. Safety rating: HARM_CATEGORY_DANGEROUS_CONTENT (HIGH)"
		if got != want {
			t.Fatalf("error = %q, want %q", got, want)
		}
	})
}

func TestMistralStrictModeDefaultFalseAndErrorStopReason(t *testing.T) {
	tools := []ai.Tool{{
		Name:                "ping",
		Description:         "ping",
		ConstrainedSampling: &ai.ConstrainedSamplingConfig{Type: ai.ConstrainedSamplingJSONSchema, Strict: ai.ConstrainedSamplingStrict("preferred")},
		Parameters:          ai.JSONSchema(`{"type":"object","properties":{"x":{"type":"string"}},"required":["x"],"additionalProperties":false}`),
	}}
	ctx := ai.Context{
		Messages: []ai.Message{&ai.UserMessage{Content: ai.UserContent{Text: strPtr("hi")}}},
		Tools:    &tools,
	}
	defaultModel := &ai.Model{ID: "mistral-large-latest", API: ai.APIMistralConversations, Provider: "mistral"}
	payload, err := buildMistralPayload(defaultModel, ctx, nil)
	if err != nil {
		t.Fatalf("buildMistralPayload(default): %v", err)
	}
	if len(payload.Tools) != 1 || payload.Tools[0].Function.Strict {
		t.Fatalf("expected default Mistral tool strict to be false, got %v", payload.Tools[0].Function.Strict)
	}

	strictCompat, _ := json.Marshal(ai.MistralConversationsCompat{SupportsStrictMode: boolPtr(true)})
	strictModel := &ai.Model{ID: "mistral-large-latest", API: ai.APIMistralConversations, Provider: "mistral", Compat: strictCompat}
	strictPayload, err := buildMistralPayload(strictModel, ctx, nil)
	if err != nil {
		t.Fatalf("buildMistralPayload(strict): %v", err)
	}
	if len(strictPayload.Tools) != 1 || !strictPayload.Tools[0].Function.Strict {
		t.Fatalf("expected opt-in Mistral tool strict=true, got %v", strictPayload.Tools[0].Function.Strict)
	}

	stopReason, errMsg := mapMistralStopReason(strPtr("error"))
	if stopReason != ai.StopReasonError || errMsg == nil || *errMsg != "Provider stopped with: error" {
		t.Fatalf("mapMistralStopReason(error) = (%q, %v), want (error, %q)", stopReason, errMsg, "Provider stopped with: error")
	}
}
