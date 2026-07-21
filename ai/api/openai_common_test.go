package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/OrdalieTech/pigo/ai"
)

func TestReadSSE(t *testing.T) {
	input := strings.NewReader(": comment\r\ndata: {\"one\":\r\ndata: 1}\r\n\r\ndata: [DONE]\r\n\r\ndata: {\"ignored\":true}\r\n\r\n")
	var values []map[string]int
	err := readSSE(input, func(raw json.RawMessage) error {
		var value map[string]int
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		values = append(values, value)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(values, []map[string]int{{"one": 1}}) {
		t.Fatalf("unexpected SSE values: %#v", values)
	}
}

func TestReadSSEPropagatesConsumerStop(t *testing.T) {
	err := readSSE(strings.NewReader("data: {}\n\n"), func(json.RawMessage) error { return errStopSSE })
	if !errors.Is(err, errStopSSE) {
		t.Fatalf("got %v, want errStopSSE", err)
	}
}

func TestClampOpenAIPromptCacheKeyUsesCodePoints(t *testing.T) {
	value := strings.Repeat("🙂", 65)
	got, ok := clampOpenAIPromptCacheKey(&value).(string)
	if !ok {
		t.Fatal("cache key did not remain a string")
	}
	if len([]rune(got)) != 64 {
		t.Fatalf("got %d code points", len([]rune(got)))
	}
}

func TestMergeProviderHeadersIsCaseInsensitive(t *testing.T) {
	headers := http.Header{"X-Test": []string{"model"}}
	override := "option"
	mergeProviderHeaders(headers, ai.ProviderHeaders{"x-test": &override})
	if got := headers.Get("X-Test"); got != "option" {
		t.Fatalf("got %q", got)
	}
	mergeProviderHeaders(headers, ai.ProviderHeaders{"X-TEST": nil})
	if got := headers.Get("x-test"); got != "" {
		t.Fatalf("header was not deleted: %q", got)
	}
}

// Gap OA-m2: upstream getClientApiKey resolves options.apiKey and auth headers
// only; OPENAI_API_KEY resolution lives in the higher registry/auth layer, so
// the api layer must not fall back to the environment.
func TestResolveOpenAIAPIKeyPrecedenceWithoutEnvFallbackOAm2(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "process-key")
	model := &ai.Model{Provider: "openai"}
	envKey := "option-env-key"
	explicit := "explicit-key"
	key, err := resolveOpenAIAPIKey(model, &ai.StreamOptions{APIKey: &explicit, Env: ai.ProviderEnv{"OPENAI_API_KEY": envKey}})
	if err != nil || key != explicit {
		t.Fatalf("explicit key = %q, %v", key, err)
	}
	authorization := "Bearer custom"
	key, err = resolveOpenAIAPIKey(model, &ai.StreamOptions{Headers: ai.ProviderHeaders{"Authorization": &authorization}})
	if err != nil || key != "unused" {
		t.Fatalf("header-backed key = %q, %v", key, err)
	}
	if _, err := resolveOpenAIAPIKey(model, &ai.StreamOptions{Env: ai.ProviderEnv{"OPENAI_API_KEY": envKey}}); err == nil {
		t.Fatal("api layer consumed OPENAI_API_KEY from options env")
	}
	if _, err := resolveOpenAIAPIKey(model, nil); err == nil {
		t.Fatal("api layer consumed OPENAI_API_KEY from the process environment")
	}
	if _, err := resolveOpenAIAPIKey(&ai.Model{Provider: "custom"}, &ai.StreamOptions{Env: ai.ProviderEnv{"OPENAI_API_KEY": envKey}}); err == nil {
		t.Fatal("custom provider incorrectly consumed OPENAI_API_KEY")
	}
}

func TestCalculateCostUsesHighestMatchingTier(t *testing.T) {
	tiers := []ai.ModelCostTier{
		{ModelCostRates: ai.ModelCostRates{Input: 2, Output: 4, CacheRead: 1, CacheWrite: 3}, InputTokensAbove: 100},
		{ModelCostRates: ai.ModelCostRates{Input: 3, Output: 6, CacheRead: 2, CacheWrite: 5}, InputTokensAbove: 200},
	}
	model := &ai.Model{Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1, Output: 2}, Tiers: &tiers}}
	reasoning := int64(7)
	usage := ai.Usage{Input: 190, Output: 10, CacheRead: 20, CacheWrite: 5, Reasoning: &reasoning}
	calculateCost(model, &usage)
	if usage.Cost.Input != 3*190/1_000_000.0 || usage.Cost.Output != 6*10/1_000_000.0 {
		t.Fatalf("unexpected tiered cost: %#v", usage.Cost)
	}
}

func TestTruncateOpenAIErrorTextUsesUTF16CodeUnits(t *testing.T) {
	text := strings.Repeat("🙂", 2_001)
	got := truncateOpenAIErrorText(text)
	want := strings.Repeat("🙂", 2_000) + "... [truncated 2 chars]"
	if got != want {
		t.Fatalf("truncated length = %d bytes, want %d", len(got), len(want))
	}
	if got := truncateOpenAIErrorText(strings.Repeat("é", maxProviderErrorBodyChars+1)); got != strings.Repeat("é", maxProviderErrorBodyChars)+"... [truncated 1 chars]" {
		t.Fatal("BMP multibyte characters were counted as UTF-8 bytes")
	}
	boundary := strings.Repeat("a", maxProviderErrorBodyChars-1) + "🙂"
	truncated := truncateOpenAIErrorText(boundary)
	if utf8.ValidString(truncated) {
		t.Fatal("UTF-16 slice did not retain its boundary surrogate")
	}
	message := &ai.AssistantMessage{Content: ai.AssistantContent{}, ErrorMessage: &truncated}
	encoded, err := ai.MarshalAssistantMessageEvent(ai.ErrorEvent{Reason: ai.StopReasonError, Error: message})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `\ud83d... [truncated 1 chars]`) {
		t.Fatalf("error event lost boundary surrogate: %s", encoded)
	}
}

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
