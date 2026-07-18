package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type f2Case struct {
	Name               string                     `json:"name"`
	API                ai.API                     `json:"api"`
	Simple             bool                       `json:"simple,omitempty"`
	PayloadHook        string                     `json:"payloadHook,omitempty"`
	PayloadConfigPatch map[string]json.RawMessage `json:"payloadConfigPatch,omitempty"`
	PayloadContents    json.RawMessage            `json:"payloadContents,omitempty"`
	Model              ai.Model                   `json:"model"`
	Context            ai.Context                 `json:"context"`
	Options            json.RawMessage            `json:"options"`
	Expected           *f2Request                 `json:"expected,omitempty"`
	SSE                string                     `json:"sse,omitempty"`
	HTTPStatus         int                        `json:"httpStatus,omitempty"`
	HTTPStatusText     string                     `json:"httpStatusText,omitempty"`
	HTTPBody           string                     `json:"httpBody,omitempty"`
	HTTPContentType    string                     `json:"httpContentType,omitempty"`
	ExpectedEvents     []json.RawMessage          `json:"expectedEvents,omitempty"`
}

type f2Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type capturedProviderRequest struct {
	Method  string
	URL     string
	Headers http.Header
	Body    []byte
}

type f2HTTPResponse struct {
	Status      int
	StatusText  string
	Body        string
	ContentType string
}

const f2FixedTimestamp int64 = 1_700_000_000_123

func TestF2OpenAIRequestShaping(t *testing.T) {
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "requests.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			captured, events := runF2Case(t, fixtureCase, f2HTTPResponse{
				Status:      http.StatusOK,
				Body:        minimalF2SSE(fixtureCase.API, fixtureCase.Model.ID),
				ContentType: "text/event-stream",
			})
			assertF2TerminalSuccess(t, events)
			if fixtureCase.Expected == nil {
				t.Fatal("request fixture has no expected request")
			}
			if captured.Method != fixtureCase.Expected.Method {
				t.Fatalf("method = %q, want %q", captured.Method, fixtureCase.Expected.Method)
			}
			if captured.URL != fixtureCase.Expected.URL {
				t.Fatalf("URL = %q, want %q", captured.URL, fixtureCase.Expected.URL)
			}
			gotHeaders := selectedF2Headers(fixtureCase.API, captured.Headers)
			if diff := diffStringMap(fixtureCase.Expected.Headers, gotHeaders); diff != "" {
				t.Fatal(diff)
			}
			wantBody := []byte(fixtureCase.Expected.Body)
			if diff := runner.ByteDiff(wantBody, captured.Body); diff != "" {
				t.Fatalf("request body mismatch:\n%s\nwant: %s\n got: %s", diff, wantBody, captured.Body)
			}
		})
	}
}

func TestF2OpenAIStreamTraces(t *testing.T) {
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "streams.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			_, events := runF2Case(t, fixtureCase, f2StreamHTTPResponse(fixtureCase))
			if len(events) != len(fixtureCase.ExpectedEvents) {
				t.Fatalf("got %d events, want %d", len(events), len(fixtureCase.ExpectedEvents))
			}
			for index := range events {
				want := compactF2Event(t, fixtureCase.ExpectedEvents[index])
				got := events[index]
				if diff := runner.ByteDiff(want, got); diff != "" {
					t.Fatalf("event %d mismatch:\n%s\nwant: %s\n got: %s", index, diff, want, got)
				}
			}
		})
	}
}

func TestF2AnthropicRequestShaping(t *testing.T) {
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "anthropic-requests.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			captured, events := runF2Case(t, fixtureCase, f2HTTPResponse{
				Status:      http.StatusOK,
				Body:        minimalF2SSE(fixtureCase.API, fixtureCase.Model.ID),
				ContentType: "text/event-stream",
			})
			assertF2TerminalSuccess(t, events)
			if fixtureCase.Expected == nil {
				t.Fatal("request fixture has no expected request")
			}
			if captured.Method != fixtureCase.Expected.Method {
				t.Fatalf("method = %q, want %q", captured.Method, fixtureCase.Expected.Method)
			}
			if captured.URL != fixtureCase.Expected.URL {
				t.Fatalf("URL = %q, want %q", captured.URL, fixtureCase.Expected.URL)
			}
			if diff := diffStringMap(fixtureCase.Expected.Headers, selectedF2Headers(fixtureCase.API, captured.Headers)); diff != "" {
				t.Fatal(diff)
			}
			wantBody := []byte(fixtureCase.Expected.Body)
			if diff := runner.ByteDiff(wantBody, captured.Body); diff != "" {
				t.Fatalf("request body mismatch:\n%s\nwant: %s\n got: %s", diff, wantBody, captured.Body)
			}
		})
	}
}

func TestF2AnthropicStreamTraces(t *testing.T) {
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "anthropic-streams.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			_, events := runF2Case(t, fixtureCase, f2StreamHTTPResponse(fixtureCase))
			if len(events) != len(fixtureCase.ExpectedEvents) {
				t.Fatalf("got %d events, want %d", len(events), len(fixtureCase.ExpectedEvents))
			}
			for index := range events {
				want := compactF2Event(t, fixtureCase.ExpectedEvents[index])
				if diff := runner.ByteDiff(want, events[index]); diff != "" {
					t.Fatalf("event %d mismatch:\n%s\nwant: %s\n got: %s", index, diff, want, events[index])
				}
			}
		})
	}
}

func TestF2GoogleRequestShaping(t *testing.T) {
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "google-requests.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			captured, events := runF2Case(t, fixtureCase, f2HTTPResponse{
				Status: http.StatusOK, Body: minimalF2SSE(fixtureCase.API, fixtureCase.Model.ID), ContentType: "text/event-stream",
			})
			assertF2TerminalSuccess(t, events)
			if fixtureCase.Expected == nil {
				t.Fatal("request fixture has no expected request")
			}
			if captured.Method != fixtureCase.Expected.Method || captured.URL != fixtureCase.Expected.URL {
				t.Fatalf("request = %s %s, want %s %s", captured.Method, captured.URL, fixtureCase.Expected.Method, fixtureCase.Expected.URL)
			}
			if diff := diffStringMap(fixtureCase.Expected.Headers, selectedF2Headers(fixtureCase.API, captured.Headers)); diff != "" {
				t.Fatal(diff)
			}
			if diff := runner.ByteDiff([]byte(fixtureCase.Expected.Body), captured.Body); diff != "" {
				t.Fatalf("request body mismatch:\n%s\nwant: %s\n got: %s", diff, fixtureCase.Expected.Body, captured.Body)
			}
		})
	}
}

func TestF2GoogleStreamTraces(t *testing.T) {
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "google-streams.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			_, events := runF2Case(t, fixtureCase, f2StreamHTTPResponse(fixtureCase))
			if len(events) != len(fixtureCase.ExpectedEvents) {
				t.Fatalf("got %d events, want %d", len(events), len(fixtureCase.ExpectedEvents))
			}
			for index := range events {
				want := compactF2Event(t, fixtureCase.ExpectedEvents[index])
				if diff := runner.ByteDiff(want, events[index]); diff != "" {
					t.Fatalf("event %d mismatch:\n%s\nwant: %s\n got: %s", index, diff, want, events[index])
				}
			}
		})
	}
}

func TestF2GoogleVertexRequestShaping(t *testing.T) {
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "google-vertex-requests.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			captured, events := runF2Case(t, fixtureCase, f2HTTPResponse{
				Status: http.StatusOK, Body: minimalF2SSE(fixtureCase.API, fixtureCase.Model.ID), ContentType: "text/event-stream",
			})
			assertF2TerminalSuccess(t, events)
			if fixtureCase.Expected == nil {
				t.Fatal("request fixture has no expected request")
			}
			if captured.Method != fixtureCase.Expected.Method || captured.URL != fixtureCase.Expected.URL {
				t.Fatalf("request = %s %s, want %s %s", captured.Method, captured.URL, fixtureCase.Expected.Method, fixtureCase.Expected.URL)
			}
			if diff := diffStringMap(fixtureCase.Expected.Headers, selectedF2Headers(fixtureCase.API, captured.Headers)); diff != "" {
				t.Fatal(diff)
			}
			if diff := runner.ByteDiff([]byte(fixtureCase.Expected.Body), captured.Body); diff != "" {
				t.Fatalf("request body mismatch:\n%s\nwant: %s\n got: %s", diff, fixtureCase.Expected.Body, captured.Body)
			}
		})
	}
}

func TestF2GoogleVertexStreamTraces(t *testing.T) {
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "google-vertex-streams.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			_, events := runF2Case(t, fixtureCase, f2StreamHTTPResponse(fixtureCase))
			if len(events) != len(fixtureCase.ExpectedEvents) {
				t.Fatalf("got %d events, want %d", len(events), len(fixtureCase.ExpectedEvents))
			}
			for index := range events {
				want := compactF2Event(t, fixtureCase.ExpectedEvents[index])
				if diff := runner.ByteDiff(want, events[index]); diff != "" {
					t.Fatalf("event %d mismatch:\n%s\nwant: %s\n got: %s", index, diff, want, events[index])
				}
			}
		})
	}
}

func f2StreamHTTPResponse(fixtureCase f2Case) f2HTTPResponse {
	if fixtureCase.HTTPStatus != 0 {
		contentType := fixtureCase.HTTPContentType
		if contentType == "" {
			contentType = "text/plain"
		}
		return f2HTTPResponse{
			Status: fixtureCase.HTTPStatus, StatusText: fixtureCase.HTTPStatusText,
			Body: fixtureCase.HTTPBody, ContentType: contentType,
		}
	}
	return f2HTTPResponse{Status: http.StatusOK, Body: fixtureCase.SSE, ContentType: "text/event-stream"}
}

func runF2Case(t *testing.T, fixtureCase f2Case, fixtureResponse f2HTTPResponse) (capturedProviderRequest, []json.RawMessage) {
	t.Helper()
	var captured capturedProviderRequest
	previousClient := openAIHTTPClient
	previousAnthropicClient := anthropicHTTPClient
	previousGoogleClient := googleHTTPClient
	previousNow := openAINowUnixMilli
	openAINowUnixMilli = func() int64 { return f2FixedTimestamp }
	testClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		captured = capturedProviderRequest{
			Method:  request.Method,
			URL:     request.URL.String(),
			Headers: request.Header.Clone(),
			Body:    body,
		}
		statusText := fixtureResponse.StatusText
		if statusText == "" {
			statusText = http.StatusText(fixtureResponse.Status)
		}
		return &http.Response{
			StatusCode: fixtureResponse.Status,
			Status:     fmt.Sprintf("%d %s", fixtureResponse.Status, statusText),
			Header:     http.Header{"Content-Type": []string{fixtureResponse.ContentType}},
			Body:       io.NopCloser(strings.NewReader(fixtureResponse.Body)),
			Request:    request,
		}, nil
	})}
	openAIHTTPClient = testClient
	anthropicHTTPClient = testClient
	googleHTTPClient = testClient
	t.Cleanup(func() {
		openAIHTTPClient = previousClient
		anthropicHTTPClient = previousAnthropicClient
		googleHTTPClient = previousGoogleClient
		openAINowUnixMilli = previousNow
	})

	events, err := streamF2Case(fixtureCase)
	if err != nil {
		t.Fatal(err)
	}
	serialized := make([]json.RawMessage, 0)
	for event, streamErr := range events {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		encoded, err := ai.MarshalAssistantMessageEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		serialized = append(serialized, encoded)
	}
	return captured, serialized
}

func streamF2Case(fixtureCase f2Case) (ai.AssistantMessageEventStream, error) {
	switch fixtureCase.API {
	case ai.APIOpenAIResponses:
		var options OpenAIResponsesOptions
		if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
			return nil, err
		}
		return StreamOpenAIResponsesWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
	case ai.APIOpenAICompletions:
		var options OpenAICompletionsOptions
		if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
			return nil, err
		}
		return StreamOpenAICompletionsWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
	case ai.APIAnthropicMessages:
		if fixtureCase.Simple {
			var options ai.SimpleStreamOptions
			if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
				return nil, err
			}
			setF2AnthropicPayloadHook(fixtureCase.PayloadHook, &options.StreamOptions)
			return StreamSimpleAnthropicMessages(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
		}
		var options AnthropicMessagesOptions
		if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
			return nil, err
		}
		setF2AnthropicPayloadHook(fixtureCase.PayloadHook, &options.StreamOptions)
		return StreamAnthropicMessagesWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
	case ai.APIGoogleGenerativeAI:
		var options GoogleOptions
		if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
			return nil, err
		}
		setF2GooglePayloadHook(fixtureCase.PayloadConfigPatch, fixtureCase.PayloadContents, &options.StreamOptions)
		return StreamGoogleGenerativeAIWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
	case ai.APIGoogleVertex:
		if fixtureCase.Simple {
			var options ai.SimpleStreamOptions
			if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
				return nil, err
			}
			setF2GooglePayloadHook(fixtureCase.PayloadConfigPatch, fixtureCase.PayloadContents, &options.StreamOptions)
			return StreamSimpleGoogleVertex(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
		}
		var options GoogleVertexOptions
		if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
			return nil, err
		}
		setF2GooglePayloadHook(fixtureCase.PayloadConfigPatch, fixtureCase.PayloadContents, &options.StreamOptions)
		return StreamGoogleVertexWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
	default:
		return nil, fmt.Errorf("unsupported F2 API %q", fixtureCase.API)
	}
}

func setF2AnthropicPayloadHook(name string, options *ai.StreamOptions) {
	if name != "disable-stream" {
		return
	}
	options.OnPayload = func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
		if typed, ok := payload.(*AnthropicMessagesPayload); ok {
			typed.Stream = false
		}
		return nil, false, nil
	}
}

func setF2GooglePayloadHook(patch map[string]json.RawMessage, contents json.RawMessage, options *ai.StreamOptions) {
	if len(patch) == 0 && len(contents) == 0 {
		return
	}
	options.OnPayload = func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
		encoded, err := ai.Marshal(payload)
		if err != nil {
			return nil, false, err
		}
		var parameters map[string]any
		if err := json.Unmarshal(encoded, &parameters); err != nil {
			return nil, false, err
		}
		config, _ := parameters["config"].(map[string]any)
		if config == nil {
			config = make(map[string]any)
			parameters["config"] = config
		}
		for name, value := range patch {
			config[name] = value
		}
		if len(contents) > 0 {
			parameters["contents"] = contents
		}
		return parameters, true, nil
	}
}

func minimalF2SSE(apiShape ai.API, modelID string) string {
	if apiShape == ai.APIAnthropicMessages {
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_request_fixture\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	if apiShape == ai.APIGoogleGenerativeAI || apiShape == ai.APIGoogleVertex {
		return "data: {\"responseId\":\"google_request_fixture\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":0,\"candidatesTokenCount\":0,\"totalTokenCount\":0}}\n\n"
	}
	if apiShape == ai.APIOpenAIResponses {
		return "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_request_fixture\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n"
	}
	return fmt.Sprintf("data: {\"id\":\"chat_request_fixture\",\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", modelID)
}

func assertF2TerminalSuccess(t *testing.T, events []json.RawMessage) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("stream emitted no events")
	}
	var terminal struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(events[len(events)-1], &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != "done" {
		t.Fatalf("terminal event = %q, want done: %s", terminal.Type, events[len(events)-1])
	}
}

func selectedF2Headers(apiShape ai.API, headers http.Header) map[string]string {
	names := []string{
		"authorization",
		"content-type",
		"session_id",
		"x-client-request-id",
		"x-fixture",
		"x-model-header",
		"x-session-affinity",
		"x-session-id",
	}
	if apiShape == ai.APIAnthropicMessages {
		names = append(names, "accept", "anthropic-beta", "anthropic-dangerous-direct-browser-access", "anthropic-version", "x-api-key", "x-app")
		if strings.HasPrefix(headers.Get("user-agent"), "claude-cli/") {
			names = append(names, "user-agent")
		}
	}
	if apiShape == ai.APIGoogleGenerativeAI || apiShape == ai.APIGoogleVertex {
		names = append(names, "x-goog-api-key")
	}
	selected := make(map[string]string)
	for _, name := range names {
		if value := f2HeaderValue(headers, name); value != "" {
			selected[name] = value
		}
	}
	return selected
}

func f2HeaderValue(headers http.Header, name string) string {
	var values []string
	for key, current := range headers {
		if strings.EqualFold(key, name) {
			values = append(values, current...)
		}
	}
	return strings.Join(values, ", ")
}

func diffStringMap(want, got map[string]string) string {
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if bytes.Equal(wantJSON, gotJSON) {
		return ""
	}
	return fmt.Sprintf("headers mismatch\nwant: %s\n got: %s", wantJSON, gotJSON)
}

func compactF2Event(t *testing.T, data []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := json.Compact(&output, data); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
