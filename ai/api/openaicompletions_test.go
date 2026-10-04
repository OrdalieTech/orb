package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestOpenAICompletionsRejectsStreamWithoutFinishReason(t *testing.T) {
	stream := openAICompletionsFixtureStream(t,
		`{"id":"chatcmpl-truncated","choices":[{"delta":{"content":"partial"},"finish_reason":null}]}`,
	)
	message, events := collectOpenAICompletionsFixture(t, stream)
	if message.StopReason != ai.StopReasonError {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if message.ErrorMessage == nil || *message.ErrorMessage != "Stream ended without finish_reason" {
		t.Fatalf("error message = %v", message.ErrorMessage)
	}
	if _, ok := events[len(events)-1].(ai.ErrorEvent); !ok {
		t.Fatalf("terminal event = %T, want ai.ErrorEvent", events[len(events)-1])
	}
}

func TestOpenAICompletionsInfersFinishReasonWhenUnsupported(t *testing.T) {
	for _, test := range []struct {
		name  string
		chunk string
		want  ai.StopReason
	}{
		{
			name:  "text infers stop",
			chunk: `{"id":"chatcmpl-nofinish","choices":[{"delta":{"content":"done"},"finish_reason":null}]}`,
			want:  ai.StopReasonStop,
		},
		{
			name:  "tool call infers toolUse",
			chunk: `{"id":"chatcmpl-nofinish","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},"finish_reason":null}]}`,
			want:  ai.StopReasonToolUse,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &ai.Model{
				ID: "fixture-model", API: ai.APIOpenAICompletions, Provider: "openai",
				BaseURL: "https://fixture.invalid/v1/", Input: ai.InputModalities{ai.InputText},
				Compat: json.RawMessage(`{"supportsFinishReason":false}`),
			}
			message, events := collectOpenAICompletionsFixture(t, openAICompletionsFixtureStreamForModel(t, model, test.chunk))
			if message.StopReason != test.want || message.ErrorMessage != nil {
				t.Fatalf("stop reason = %q, error = %v", message.StopReason, message.ErrorMessage)
			}
			if _, ok := events[len(events)-1].(ai.DoneEvent); !ok {
				t.Fatalf("terminal event = %T, want ai.DoneEvent", events[len(events)-1])
			}
		})
	}
}

func TestOpenAICompletionsCustomToolCallStreamingRoundTrip(t *testing.T) {
	model := &ai.Model{ID: "gpt-test", API: ai.APIOpenAICompletions, Provider: "openai"}
	output := newAssistantMessage(model)
	state := newCompletionsStreamState(output)
	state.grammarToolInputProperties = map[string]string{"emit": "payload"}
	var deltas strings.Builder
	emit := func(event ai.AssistantMessageEvent) error {
		if delta, ok := event.(ai.ToolCallDeltaEvent); ok {
			deltas.WriteString(delta.Delta)
		}
		return nil
	}
	for _, raw := range []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"custom","custom":{"name":"emit","input":"a\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"custom":{"input":"b"}}]},"finish_reason":"tool_calls"}]}`,
	} {
		if err := state.consumeChunk(model, json.RawMessage(raw), emit); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.finishBlocks(emit); err != nil {
		t.Fatal(err)
	}
	if deltas.String() != `{"payload":"a\"b"}` {
		t.Fatalf("streamed JSON deltas = %q", deltas.String())
	}
	call, ok := output.Content[0].(*ai.ToolCall)
	if !ok || call.Name != "emit" || call.Arguments["payload"] != `a"b` {
		t.Fatalf("custom tool call = %#v", output.Content)
	}
	replay, include, err := convertOpenAICompletionsAssistantMessageWithGrammar(
		model, output, resolvedOpenAICompletionsCompat{}, map[string]string{"emit": "payload"},
	)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := ai.Marshal(replay)
	if err != nil {
		t.Fatal(err)
	}
	if !include || !strings.Contains(string(wire), `"type":"custom"`) ||
		!strings.Contains(string(wire), `"input":"a\"b"`) {
		t.Fatalf("custom call replay = %s", wire)
	}
}

func TestOpenAICompletionsKeepsToolCallsWithoutIndexesSeparate(t *testing.T) {
	stream := openAICompletionsFixtureStream(t,
		`{"id":"chatcmpl-tools","choices":[{"delta":{"tool_calls":[{"id":"call_a","function":{"name":"read","arguments":"{\"path\":\"A"}},{"id":"call_b","function":{"name":"read","arguments":"{\"path\":\"B"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tools","choices":[{"delta":{"tool_calls":[{"id":"call_b","function":{"arguments":".txt\"}"}},{"id":"call_a","function":{"arguments":".txt\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	message, _ := collectOpenAICompletionsFixture(t, stream)
	if message.StopReason != ai.StopReasonToolUse {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if len(message.Content) != 2 {
		t.Fatalf("content length = %d", len(message.Content))
	}
	want := []struct {
		id   string
		path string
	}{{"call_a", "A.txt"}, {"call_b", "B.txt"}}
	for index, expected := range want {
		call, ok := message.Content[index].(*ai.ToolCall)
		if !ok {
			t.Fatalf("content[%d] = %T", index, message.Content[index])
		}
		if call.ID != expected.id || call.Arguments["path"] != expected.path {
			t.Fatalf("call[%d] = %#v", index, call)
		}
		if call.StreamIndex != nil || call.PartialArgs != nil {
			t.Fatalf("call[%d] retained streaming scratch: %#v", index, call)
		}
	}
}

func TestOpenAICompletionsPreservesStreamedArgumentOrderOnReplay(t *testing.T) {
	stream := openAICompletionsFixtureStream(t,
		`{"id":"chatcmpl-order","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_order","function":{"name":"echo","arguments":"{\"text\":\"first\",\"mode\":\"plain\",\"metadata\":{\"count\":1}}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	message, _ := collectOpenAICompletionsFixture(t, stream)
	converted, include, err := convertOpenAICompletionsAssistantMessageWithGrammar(
		&ai.Model{},
		message,
		resolvedOpenAICompletionsCompat{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !include {
		t.Fatal("streamed tool call replay was omitted")
	}
	calls := converted["tool_calls"].([]any)
	function := calls[0].(map[string]any)["function"].(map[string]any)
	want := `{"text":"first","mode":"plain","metadata":{"count":1}}`
	if got := function["arguments"]; got != want {
		t.Fatalf("arguments = %q, want %s", got, want)
	}
}

func TestOpenAICompletionsUsesChoiceUsageFallback(t *testing.T) {
	stream := openAICompletionsFixtureStream(t,
		`{"id":"chatcmpl-usage","choices":[{"delta":{},"finish_reason":"stop","usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":50,"cache_write_tokens":30},"completion_tokens_details":{"reasoning_tokens":2}}}]}`,
	)
	message, _ := collectOpenAICompletionsFixture(t, stream)
	if message.Usage.Input != 20 || message.Usage.Output != 5 || message.Usage.CacheRead != 50 || message.Usage.CacheWrite != 30 {
		t.Fatalf("usage = %#v", message.Usage)
	}
	if message.Usage.Reasoning == nil || *message.Usage.Reasoning != 2 {
		t.Fatalf("reasoning usage = %v", message.Usage.Reasoning)
	}
	if message.Usage.TotalTokens != 105 {
		t.Fatalf("total tokens = %d", message.Usage.TotalTokens)
	}
}

func openAICompletionsFixtureStream(t *testing.T, chunks ...string) ai.AssistantMessageEventStream {
	t.Helper()
	return openAICompletionsFixtureStreamForModel(t, &ai.Model{
		ID:       "fixture-model",
		API:      ai.APIOpenAICompletions,
		Provider: "openai",
		BaseURL:  "https://fixture.invalid/v1/",
		Input:    ai.InputModalities{ai.InputText},
		Cost:     ai.ModelCost{},
	}, chunks...)
}

func openAICompletionsFixtureStreamForModel(t *testing.T, model *ai.Model, chunks ...string) ai.AssistantMessageEventStream {
	t.Helper()
	previousClient := openAIHTTPClient
	var body strings.Builder
	for _, chunk := range chunks {
		fmt.Fprintf(&body, "data: %s\n\n", chunk)
	}
	body.WriteString("data: [DONE]\n\n")
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body.String())),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { openAIHTTPClient = previousClient })

	key := "fixture-key"
	stream, err := StreamOpenAICompletionsWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("test")},
	}}, &OpenAICompletionsOptions{StreamOptions: ai.StreamOptions{APIKey: &key}})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func collectOpenAICompletionsFixture(
	t *testing.T,
	stream ai.AssistantMessageEventStream,
) (*ai.AssistantMessage, []ai.AssistantMessageEvent) {
	t.Helper()
	var terminal *ai.AssistantMessage
	events := make([]ai.AssistantMessageEvent, 0)
	for event, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		switch value := event.(type) {
		case ai.DoneEvent:
			terminal = value.Message
		case ai.ErrorEvent:
			terminal = value.Error
		}
	}
	if terminal == nil {
		t.Fatal("stream did not emit a terminal event")
	}
	return terminal, events
}
