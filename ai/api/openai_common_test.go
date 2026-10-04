package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
)

func TestPostOpenAIStreamPreservesWireJSONAndHooksResponse(t *testing.T) {
	var body string
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		body = string(data)
		if request.URL.Path != "/v1/responses" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer fixture-key" {
			t.Errorf("authorization = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header: http.Header{
				"X-Response":   []string{"seen"},
				"Content-Type": []string{"text/event-stream"},
			},
			Body:    io.NopCloser(strings.NewReader("data: [DONE]\n\n")),
			Request: request,
		}, nil
	})}
	defer func() { openAIHTTPClient = previousClient }()

	key := "fixture-key"
	hookCalled := false
	options := &ai.StreamOptions{
		APIKey: &key,
		OnResponse: func(_ context.Context, response ai.ProviderResponse, _ *ai.Model) error {
			hookCalled = response.Status == http.StatusAccepted && response.Headers["x-response"] == "seen"
			return nil
		},
	}
	model := &ai.Model{Provider: "openai", BaseURL: "https://fixture.invalid/v1"}
	response, err := postOpenAIStream(
		context.Background(),
		model,
		options,
		"responses",
		map[string]any{"text": "<&"},
		make(http.Header),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if body != `{"text":"<&"}` {
		t.Fatalf("body = %q", body)
	}
	if !hookCalled {
		t.Fatal("response hook did not receive status and headers")
	}
}

func TestPostOpenAIStreamRecoversSDKHTTPErrorBodies(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		responsesError   string
		completionsError string
	}{
		{
			name:             "raw text",
			body:             "denied",
			responsesError:   "OpenAI API error (403): 403 denied",
			completionsError: "403 denied",
		},
		{
			name:             "empty",
			responsesError:   "OpenAI API error (403): 403 status code (no body)",
			completionsError: "403 status code (no body)",
		},
		{
			name:             "JSON error",
			body:             `{"error":{"message":"\u0062ad","code":1e2}}`,
			responsesError:   `OpenAI API error (403): {"message":"bad","code":100}`,
			completionsError: `403: {"message":"bad","code":100}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previousClient := openAIHTTPClient
			openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(test.body)),
					Request:    request,
				}, nil
			})}
			defer func() { openAIHTTPClient = previousClient }()

			key := "fixture-key"
			response, err := postOpenAIStream(
				context.Background(),
				&ai.Model{Provider: "openai", BaseURL: "https://fixture.invalid/v1"},
				&ai.StreamOptions{APIKey: &key},
				"responses",
				map[string]any{},
				make(http.Header),
			)
			if response == nil || response.StatusCode != http.StatusForbidden {
				t.Fatalf("response = %#v", response)
			}
			if got := formatOpenAIError(err, "OpenAI API error"); got != test.responsesError {
				t.Fatalf("Responses error = %q, want %q", got, test.responsesError)
			}
			if got := formatOpenAIError(err, ""); got != test.completionsError {
				t.Fatalf("Completions error = %q, want %q", got, test.completionsError)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

// contextGatedBody mimics a real streamed response body: the first read stalls
// for delay, and every read fails once the request context is cancelled. It
// lets tests prove a request timeout no longer races the streamed body (OA-M1).
type contextGatedBody struct {
	ctx    context.Context
	delay  time.Duration
	waited bool
	reader io.Reader
}

func (body *contextGatedBody) Read(buffer []byte) (int, error) {
	if !body.waited {
		body.waited = true
		select {
		case <-body.ctx.Done():
			return 0, body.ctx.Err()
		case <-time.After(body.delay):
		}
	}
	if err := body.ctx.Err(); err != nil {
		return 0, err
	}
	return body.reader.Read(buffer)
}

func (body *contextGatedBody) Close() error { return nil }
