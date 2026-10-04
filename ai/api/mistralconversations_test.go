package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

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

func mistralTestModel() *ai.Model {
	return &ai.Model{
		ID: "mistral-small-2603", Name: "Mistral Small", API: ai.APIMistralConversations, Provider: "mistral",
		BaseURL: "https://mistral.invalid", Input: ai.InputModalities{ai.InputText},
		ContextWindow: 128_000, MaxTokens: 8_192,
	}
}
