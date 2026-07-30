package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

// openAICompletionsRawStream streams a verbatim SSE body so third-party
// framing (comment keepalives, trailers, truncation) is exercised, not only the
// chunk JSON that openAICompletionsFixtureStream wraps.
func openAICompletionsRawStream(t *testing.T, body string) ai.AssistantMessageEventStream {
	t.Helper()
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { openAIHTTPClient = previousClient })

	key := "fixture-key"
	stream, err := StreamOpenAICompletions(context.Background(), ai.Request{
		Model: &ai.Model{
			ID: "vendor/model", API: ai.APIOpenAICompletions, Provider: "tensorx",
			BaseURL: "https://fixture.invalid/v1/", Input: ai.InputModalities{ai.InputText},
		},
		Context: ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("test")}}},
		Options: &ai.StreamOptions{APIKey: &key},
	})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func compatEdgeText(message *ai.AssistantMessage) string {
	var text strings.Builder
	for _, block := range message.Content {
		if value, ok := block.(*ai.TextContent); ok {
			text.WriteString(value.Text)
		}
	}
	return text.String()
}

// Proxies in front of OpenAI-compatible endpoints inject comment keepalives
// between chunks; they carry no data and must not end or corrupt the stream.
func TestOpenAICompletionsSkipsCommentKeepaliveFrames(t *testing.T) {
	body := ": keepalive\n\n" +
		`data: {"id":"c1","choices":[{"delta":{"content":"O"},"finish_reason":null}]}` + "\n\n" +
		": ping\n\n" +
		`data: {"id":"c1","choices":[{"delta":{"content":"K"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}` + "\n\n" +
		"data: [DONE]\n\n"
	message, _ := collectOpenAICompletionsFixture(t, openAICompletionsRawStream(t, body))
	if message.StopReason != ai.StopReasonStop {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if got := compatEdgeText(message); got != "OK" {
		t.Fatalf("text = %q, want %q", got, "OK")
	}
	if message.Usage.Input != 3 || message.Usage.Output != 2 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

// stream_options.include_usage makes the endpoint emit usage on a trailing
// chunk whose choices array is empty, after finish_reason already arrived.
func TestOpenAICompletionsReadsUsageOnlyTrailerChunk(t *testing.T) {
	message, _ := collectOpenAICompletionsFixture(t, openAICompletionsFixtureStream(t,
		`{"id":"c1","choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}]}`,
		`{"id":"c1","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
	))
	if message.StopReason != ai.StopReasonStop {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if message.Usage.Input != 11 || message.Usage.Output != 7 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

// A stream carrying no usage at all leaves usage and cost zeroed rather than
// failing; several OpenAI-compatible endpoints omit the field entirely.
func TestOpenAICompletionsToleratesMissingUsage(t *testing.T) {
	message, _ := collectOpenAICompletionsFixture(t, openAICompletionsFixtureStream(t,
		`{"id":"c1","choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}]}`,
	))
	if message.StopReason != ai.StopReasonStop || compatEdgeText(message) != "OK" {
		t.Fatalf("message = %q / %q", message.StopReason, compatEdgeText(message))
	}
	if message.Usage.TotalTokens != 0 || message.Usage.Cost.Total != 0 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

// A supplementary code point delivered whole inside one delta round-trips.
// Splitting the surrogate pair across two deltas does not: encoding/json
// replaces each half with U+FFFD, while upstream's UTF-16 strings rejoin on
// concatenation. No endpoint in the live matrix splits emoji this way, so the
// divergence is pinned here instead of carrying a speculative decoder.
func TestOpenAICompletionsSupplementaryCodePointDeltas(t *testing.T) {
	whole, _ := collectOpenAICompletionsFixture(t, openAICompletionsFixtureStream(t,
		`{"id":"c1","choices":[{"delta":{"content":"h🌍i"},"finish_reason":"stop"}]}`,
	))
	if got := compatEdgeText(whole); got != "h\U0001F30Di" {
		t.Fatalf("whole-pair text = %q, want %q", got, "h\U0001F30Di")
	}
	split, _ := collectOpenAICompletionsFixture(t, openAICompletionsFixtureStream(t,
		`{"id":"c1","choices":[{"delta":{"content":"h\ud83c"},"finish_reason":null}]}`,
		`{"id":"c1","choices":[{"delta":{"content":"\udf0di"},"finish_reason":"stop"}]}`,
	))
	if got := compatEdgeText(split); got != "h��i" {
		t.Fatalf("split-pair text = %q; halves now rejoin, drop this pin", got)
	}
}

// Tool arguments arrive as concatenated JSON fragments; a large payload must
// survive accumulation and still parse into the final object.
func TestOpenAICompletionsAccumulatesLargeToolArguments(t *testing.T) {
	blob := strings.Repeat("x", 200_000)
	message, _ := collectOpenAICompletionsFixture(t, openAICompletionsFixtureStream(t,
		`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"text\":\""}}]},"finish_reason":null}]}`,
		`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"`+blob+`"}}]},"finish_reason":null}]}`,
		`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]},"finish_reason":"tool_calls"}]}`,
	))
	if message.StopReason != ai.StopReasonToolUse {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	call, ok := message.Content[0].(*ai.ToolCall)
	if !ok {
		t.Fatalf("content[0] = %T", message.Content[0])
	}
	if text, _ := call.Arguments["text"].(string); len(text) != len(blob) {
		t.Fatalf("argument length = %d, want %d", len(text), len(blob))
	}
}

// Non-OpenAI finish_reason values (SGLang and vLLM emit these) surface as a
// stream error while keeping the text accumulated so far.
func TestOpenAICompletionsSurfacesUnknownFinishReason(t *testing.T) {
	message, _ := collectOpenAICompletionsFixture(t, openAICompletionsFixtureStream(t,
		`{"id":"c1","choices":[{"delta":{"content":"OK"},"finish_reason":"eos_token_reached"}]}`,
	))
	if message.StopReason != ai.StopReasonError {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if got := compatEdgeText(message); got != "OK" {
		t.Fatalf("text = %q", got)
	}
}

// A connection dropped mid tool call ends the stream in error and leaves the
// truncated arguments unparsed rather than replaying them as a complete call.
func TestOpenAICompletionsFailsOnStreamCutMidToolCall(t *testing.T) {
	body := `data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"cit"}}]},"finish_reason":null}]}` + "\n\n"
	message, _ := collectOpenAICompletionsFixture(t, openAICompletionsRawStream(t, body))
	if message.StopReason != ai.StopReasonError {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	call, ok := message.Content[0].(*ai.ToolCall)
	if !ok {
		t.Fatalf("content[0] = %T", message.Content[0])
	}
	if call.Name != "get_weather" || len(call.Arguments) != 0 {
		t.Fatalf("call = %#v", call)
	}
}

// A single unparseable data frame aborts the stream, matching the eager
// JSON.parse in the openai-node Stream upstream builds on; later valid chunks
// are not recovered.
func TestOpenAICompletionsFailsOnMalformedDataFrame(t *testing.T) {
	body := "data: {not json\n\n" +
		`data: {"id":"c1","choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	message, _ := collectOpenAICompletionsFixture(t, openAICompletionsRawStream(t, body))
	if message.StopReason != ai.StopReasonError {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if got := compatEdgeText(message); got != "" {
		t.Fatalf("text = %q, want the aborted stream to yield nothing", got)
	}
}
