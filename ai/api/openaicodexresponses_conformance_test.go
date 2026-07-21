package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/conformance/runner"
)

type f2CodexCase struct {
	Name            string            `json:"name"`
	Simple          bool              `json:"simple,omitempty"`
	Model           ai.Model          `json:"model"`
	Context         ai.Context        `json:"context"`
	Options         json.RawMessage   `json:"options"`
	Expected        *f2Request        `json:"expected,omitempty"`
	SSE             string            `json:"sse,omitempty"`
	HTTPStatus      int               `json:"httpStatus,omitempty"`
	HTTPBody        string            `json:"httpBody,omitempty"`
	HTTPContentType string            `json:"httpContentType,omitempty"`
	ExpectedEvents  []json.RawMessage `json:"expectedEvents,omitempty"`
}

func TestF2OpenAICodexRequestShaping(t *testing.T) {
	var fixture struct {
		Cases []f2CodexCase `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "codex-requests.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			captured, events := runF2CodexCase(t, fixtureCase, f2HTTPResponse{
				Status:      http.StatusOK,
				Body:        "data: {\"type\":\"response.done\",\"response\":{\"id\":\"resp_codex_request\",\"status\":\"completed\",\"output\":[]}}\n\n",
				ContentType: "text/event-stream",
			})
			assertF2TerminalSuccess(t, events)
			if fixtureCase.Expected == nil {
				t.Fatal("request fixture has no expected request")
			}
			if captured.Method != fixtureCase.Expected.Method || captured.URL != fixtureCase.Expected.URL {
				t.Fatalf("request = %s %s, want %s %s", captured.Method, captured.URL, fixtureCase.Expected.Method, fixtureCase.Expected.URL)
			}
			if diff := diffStringMap(fixtureCase.Expected.Headers, selectedF2CodexHeaders(captured.Headers)); diff != "" {
				t.Fatal(diff)
			}
			if diff := runner.ByteDiff([]byte(fixtureCase.Expected.Body), captured.Body); diff != "" {
				t.Fatalf("request body mismatch:\n%s\nwant: %s\n got: %s", diff, fixtureCase.Expected.Body, captured.Body)
			}
		})
	}
}

func TestF2OpenAICodexStreamTraces(t *testing.T) {
	var fixture struct {
		Cases []f2CodexCase `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "codex-streams.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			response := f2HTTPResponse{
				Status:      http.StatusOK,
				Body:        fixtureCase.SSE,
				ContentType: "text/event-stream",
			}
			if fixtureCase.HTTPStatus != 0 {
				response.Status = fixtureCase.HTTPStatus
				response.Body = fixtureCase.HTTPBody
				response.ContentType = fixtureCase.HTTPContentType
				if response.ContentType == "" {
					response.ContentType = "text/plain"
				}
			}
			_, events := runF2CodexCase(t, fixtureCase, response)
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

func runF2CodexCase(t *testing.T, fixtureCase f2CodexCase, fixtureResponse f2HTTPResponse) (capturedProviderRequest, []json.RawMessage) {
	t.Helper()
	var captured capturedProviderRequest
	previousClient := openAIHTTPClient
	previousNow := openAINowUnixMilli
	openAINowUnixMilli = func() int64 { return f2FixedTimestamp }
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		captured = capturedProviderRequest{
			Method: request.Method, URL: request.URL.String(), Headers: request.Header.Clone(), Body: body,
		}
		return &http.Response{
			StatusCode: fixtureResponse.Status,
			Status:     fmt.Sprintf("%d %s", fixtureResponse.Status, http.StatusText(fixtureResponse.Status)),
			Header:     http.Header{"Content-Type": []string{fixtureResponse.ContentType}},
			Body:       io.NopCloser(strings.NewReader(fixtureResponse.Body)),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() {
		openAIHTTPClient = previousClient
		openAINowUnixMilli = previousNow
	})

	var (
		stream ai.AssistantMessageEventStream
		err    error
	)
	if fixtureCase.Simple {
		var options ai.SimpleStreamOptions
		if err = json.Unmarshal(fixtureCase.Options, &options); err == nil {
			stream, err = StreamSimpleOpenAICodexResponses(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
		}
	} else {
		var options OpenAICodexResponsesOptions
		if err = json.Unmarshal(fixtureCase.Options, &options); err == nil {
			stream, err = StreamOpenAICodexResponsesWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &options)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	events := make([]json.RawMessage, 0, len(fixtureCase.ExpectedEvents))
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
	return captured, events
}

func selectedF2CodexHeaders(headers http.Header) map[string]string {
	selected := make(map[string]string)
	for _, name := range []string{
		"accept", "authorization", "chatgpt-account-id", "content-type", "openai-beta", "originator",
		"session-id", "x-client-request-id", "x-fixture", "x-model-header",
	} {
		if value := f2HeaderValue(headers, name); value != "" {
			selected[name] = value
		}
	}
	return selected
}
