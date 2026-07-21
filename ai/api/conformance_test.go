package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/conformance/runner"
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
	BedrockItems       []bedrockFixtureItem       `json:"items,omitempty"`
	Status             int                        `json:"status,omitempty"`
	RequestID          string                     `json:"requestId,omitempty"`
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

type bedrockFixtureItem struct {
	MessageStart *struct {
		Role string `json:"role"`
	} `json:"messageStart,omitempty"`
	ContentBlockStart *struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
		Start             struct {
			ToolUse *struct {
				ToolUseID string `json:"toolUseId"`
				Name      string `json:"name"`
			} `json:"toolUse,omitempty"`
		} `json:"start"`
	} `json:"contentBlockStart,omitempty"`
	ContentBlockDelta *struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
		Delta             struct {
			Text    *string `json:"text,omitempty"`
			ToolUse *struct {
				Input *string `json:"input,omitempty"`
			} `json:"toolUse,omitempty"`
			ReasoningContent *struct {
				Text      *string `json:"text,omitempty"`
				Signature *string `json:"signature,omitempty"`
			} `json:"reasoningContent,omitempty"`
		} `json:"delta"`
	} `json:"contentBlockDelta,omitempty"`
	ContentBlockStop *struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
	} `json:"contentBlockStop,omitempty"`
	MessageStop *struct {
		StopReason string `json:"stopReason"`
	} `json:"messageStop,omitempty"`
	Metadata *struct {
		Usage *struct {
			InputTokens      int64 `json:"inputTokens"`
			OutputTokens     int64 `json:"outputTokens"`
			CacheReadTokens  int64 `json:"cacheReadInputTokens"`
			CacheWriteTokens int64 `json:"cacheWriteInputTokens"`
			TotalTokens      int64 `json:"totalTokens"`
		} `json:"usage,omitempty"`
	} `json:"metadata,omitempty"`
	ThrottlingException *struct {
		Message string `json:"message"`
	} `json:"throttlingException,omitempty"`
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

func TestF2BedrockRequestShaping(t *testing.T) {
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "bedrock-requests.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			if fixtureCase.Expected == nil {
				t.Fatal("request fixture has no expected request")
			}
			capturedRequests := make(chan capturedProviderRequest, 1)
			server := http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Error(err)
					return
				}
				captured := capturedProviderRequest{
					Method: request.Method, URL: request.URL.RequestURI(), Headers: request.Header.Clone(), Body: body,
				}
				select {
				case capturedRequests <- captured:
				default:
				}
				response.Header().Set("content-type", "application/json")
				response.Header().Set("x-amzn-errortype", "ValidationException")
				response.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(response, `{"message":"fixture capture complete"}`)
			})}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			serveDone := make(chan error, 1)
			go func() { serveDone <- server.Serve(listener) }()
			t.Cleanup(func() {
				_ = server.Close()
				if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
					t.Error(err)
				}
			})

			fixtureCase.Model.BaseURL = "http://" + listener.Addr().String()
			var options BedrockConverseStreamOptions
			if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
				t.Fatal(err)
			}
			stream, err := StreamBedrockConverseWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
			if err != nil {
				t.Fatal(err)
			}
			for _, streamErr := range stream {
				if streamErr != nil {
					t.Fatal(streamErr)
				}
			}
			captured := <-capturedRequests
			if captured.Method != fixtureCase.Expected.Method || captured.URL != fixtureCase.Expected.URL {
				t.Fatalf("request = %s %s, want %s %s", captured.Method, captured.URL, fixtureCase.Expected.Method, fixtureCase.Expected.URL)
			}
			if diff := diffStringMap(fixtureCase.Expected.Headers, selectedBedrockHeaders(captured.Headers)); diff != "" {
				t.Fatal(diff)
			}
			if diff := semanticJSONDiff([]byte(fixtureCase.Expected.Body), captured.Body); diff != "" {
				t.Fatalf("request body mismatch:\n%s\nwant: %s\n got: %s", diff, fixtureCase.Expected.Body, captured.Body)
			}
		})
	}
}

func TestF2MistralRequestShaping(t *testing.T) {
	testF2RequestShaping(t, "mistral-requests.json")
}

func TestF2MistralStreamTraces(t *testing.T) {
	testF2StreamTraces(t, "mistral-streams.json")
}

func TestF2AzureOpenAIRequestShaping(t *testing.T) {
	testF2RequestShaping(t, "azure-requests.json")
}

func TestF2AzureOpenAIStreamTraces(t *testing.T) {
	testF2StreamTraces(t, "azure-streams.json")
}

func TestF2PiMessagesRequestShaping(t *testing.T) {
	testF2RequestShaping(t, "pi-messages-requests.json")
}

func TestF2PiMessagesStreamTraces(t *testing.T) {
	testF2StreamTraces(t, "pi-messages-streams.json")
}

func testF2RequestShaping(t *testing.T, fixtureName string) {
	t.Helper()
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", fixtureName, &fixture)
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

func testF2StreamTraces(t *testing.T, fixtureName string) {
	t.Helper()
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", fixtureName, &fixture)
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

func TestF2BedrockStreamTraces(t *testing.T) {
	var fixture struct {
		Cases []f2Case `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "bedrock-streams.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			previousTransport := newBedrockTransport
			previousNow := openAINowUnixMilli
			openAINowUnixMilli = func() int64 { return f2FixedTimestamp }
			items, streamErr := fixtureBedrockItems(fixtureCase.BedrockItems)
			newBedrockTransport = func(context.Context, *ai.Model, *BedrockConverseStreamOptions) (bedrockTransport, error) {
				return &fixtureBedrockTransport{response: &fixtureBedrockResponse{
					items: items, status: fixtureCase.Status, requestID: fixtureCase.RequestID, err: streamErr,
				}}, nil
			}
			t.Cleanup(func() {
				newBedrockTransport = previousTransport
				openAINowUnixMilli = previousNow
			})

			var options BedrockConverseStreamOptions
			if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
				t.Fatal(err)
			}
			stream, err := StreamBedrockConverseWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
			if err != nil {
				t.Fatal(err)
			}
			var events []json.RawMessage
			for event, streamErr := range stream {
				if streamErr != nil {
					t.Fatal(streamErr)
				}
				encoded, err := ai.MarshalAssistantMessageEvent(event)
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, encoded)
			}
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
	previousMistralClient := mistralHTTPClient
	previousAzureClient := azureOpenAIHTTPClient
	previousPiMessagesClient := piMessagesHTTPClient
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
	mistralHTTPClient = testClient
	azureOpenAIHTTPClient = testClient
	piMessagesHTTPClient = testClient
	t.Cleanup(func() {
		openAIHTTPClient = previousClient
		anthropicHTTPClient = previousAnthropicClient
		googleHTTPClient = previousGoogleClient
		mistralHTTPClient = previousMistralClient
		azureOpenAIHTTPClient = previousAzureClient
		piMessagesHTTPClient = previousPiMessagesClient
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
			// Route through the production dispatcher so cloudflare endpoint
			// placeholders resolve exactly as upstream cloudflareStreams does (G1).
			return StreamSimple(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
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
	case ai.APIMistralConversations:
		var options MistralConversationsOptions
		if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
			return nil, err
		}
		return StreamMistralConversationsWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
	case ai.APIAzureOpenAIResponses:
		var options AzureOpenAIResponsesOptions
		if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
			return nil, err
		}
		return StreamAzureOpenAIResponsesWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
	case ai.APIPiMessages:
		var options PiMessagesOptions
		if err := json.Unmarshal(fixtureCase.Options, &options); err != nil {
			return nil, err
		}
		return StreamPiMessagesWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
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
	if apiShape == ai.APIPiMessages {
		return "data: {\"type\":\"done\",\"reason\":\"stop\",\"usage\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":0,\"cost\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"total\":0}}}\n\n"
	}
	if apiShape == ai.APIAnthropicMessages {
		return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_request_fixture\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	}
	if apiShape == ai.APIGoogleGenerativeAI || apiShape == ai.APIGoogleVertex {
		return "data: {\"responseId\":\"google_request_fixture\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":0,\"candidatesTokenCount\":0,\"totalTokenCount\":0}}\n\n"
	}
	if apiShape == ai.APIOpenAIResponses || apiShape == ai.APIAzureOpenAIResponses {
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
	if apiShape == ai.APIOpenAIResponses || apiShape == ai.APIOpenAICompletions {
		names = append(names, "copilot-vision-request", "openai-intent", "x-initiator")
	}
	if apiShape == ai.APIAnthropicMessages {
		names = append(names, "accept", "anthropic-beta", "anthropic-dangerous-direct-browser-access", "anthropic-version", "cf-aig-authorization", "x-api-key", "x-app")
		if strings.HasPrefix(headers.Get("user-agent"), "claude-cli/") {
			names = append(names, "user-agent")
		}
	}
	if apiShape == ai.APIGoogleGenerativeAI || apiShape == ai.APIGoogleVertex {
		names = append(names, "x-goog-api-key")
	}
	if apiShape == ai.APIPiMessages {
		names = append(names, "accept")
	}
	if apiShape == ai.APIMistralConversations {
		names = append(names, "accept", "x-affinity")
	}
	if apiShape == ai.APIAzureOpenAIResponses {
		names = append(names, "accept", "api-key")
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

func selectedBedrockHeaders(headers http.Header) map[string]string {
	selected := make(map[string]string)
	for _, name := range []string{"content-type", "x-fixture"} {
		if value := headers.Get(name); value != "" {
			selected[name] = value
		}
	}
	return selected
}

type fixtureBedrockTransport struct{ response bedrockResponse }

func (transport *fixtureBedrockTransport) Send(context.Context, *BedrockConverseStreamPayload) (bedrockResponse, error) {
	return transport.response, nil
}

type fixtureBedrockResponse struct {
	items     []bedrockStreamItem
	index     int
	status    int
	requestID string
	// err mirrors the AWS Go SDK surface for mid-stream exception events: the
	// event channel closes and the exception is reported via stream.Err() (G8).
	err error
}

func (response *fixtureBedrockResponse) Status() int       { return response.status }
func (response *fixtureBedrockResponse) RequestID() string { return response.requestID }
func (response *fixtureBedrockResponse) Close() error      { return nil }
func (response *fixtureBedrockResponse) Err() error        { return response.err }

func (response *fixtureBedrockResponse) Next(context.Context) (bedrockStreamItem, bool) {
	if response.index >= len(response.items) {
		return bedrockStreamItem{}, false
	}
	item := response.items[response.index]
	response.index++
	return item, true
}

func fixtureBedrockItems(values []bedrockFixtureItem) ([]bedrockStreamItem, error) {
	result := make([]bedrockStreamItem, 0, len(values))
	for _, value := range values {
		if value.ThrottlingException != nil {
			// The SDK ends the event stream at an exception member and reports
			// it through Err(); later fixture items are unreachable (G8).
			return result, &bedrocktypes.ThrottlingException{Message: aws.String(value.ThrottlingException.Message)}
		}
		item := bedrockStreamItem{}
		switch {
		case value.MessageStart != nil:
			item.Kind, item.Role = bedrockItemMessageStart, value.MessageStart.Role
		case value.ContentBlockStart != nil:
			item.Kind = bedrockItemContentStart
			item.ContentBlockIndex = value.ContentBlockStart.ContentBlockIndex
			if tool := value.ContentBlockStart.Start.ToolUse; tool != nil {
				item.ToolUseID, item.ToolName = tool.ToolUseID, tool.Name
			}
		case value.ContentBlockDelta != nil:
			item.Kind = bedrockItemContentDelta
			item.ContentBlockIndex = value.ContentBlockDelta.ContentBlockIndex
			item.Text = value.ContentBlockDelta.Delta.Text
			if tool := value.ContentBlockDelta.Delta.ToolUse; tool != nil {
				item.ToolInput = tool.Input
			}
			if reasoning := value.ContentBlockDelta.Delta.ReasoningContent; reasoning != nil {
				item.ReasoningText, item.ReasoningSignature = reasoning.Text, reasoning.Signature
			}
		case value.ContentBlockStop != nil:
			item.Kind, item.ContentBlockIndex = bedrockItemContentStop, value.ContentBlockStop.ContentBlockIndex
		case value.MessageStop != nil:
			item.Kind, item.StopReason = bedrockItemMessageStop, value.MessageStop.StopReason
		case value.Metadata != nil:
			item.Kind = bedrockItemMetadata
			if usage := value.Metadata.Usage; usage != nil {
				item.InputTokens, item.OutputTokens = usage.InputTokens, usage.OutputTokens
				item.CacheReadTokens, item.CacheWriteTokens = usage.CacheReadTokens, usage.CacheWriteTokens
				item.TotalTokens = usage.TotalTokens
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func diffStringMap(want, got map[string]string) string {
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if bytes.Equal(wantJSON, gotJSON) {
		return ""
	}
	return fmt.Sprintf("headers mismatch\nwant: %s\n got: %s", wantJSON, gotJSON)
}

func semanticJSONDiff(want, got []byte) string {
	var wantValue, gotValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		return "invalid expected JSON: " + err.Error()
	}
	if err := json.Unmarshal(got, &gotValue); err != nil {
		return "invalid actual JSON: " + err.Error()
	}
	wantCanonical, _ := json.Marshal(wantValue)
	gotCanonical, _ := json.Marshal(gotValue)
	return runner.ByteDiff(wantCanonical, gotCanonical)
}

func compactF2Event(t *testing.T, data []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := json.Compact(&output, data); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
