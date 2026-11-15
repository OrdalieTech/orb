package api

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
)

func fixedJitter(t *testing.T, value float64) {
	t.Helper()
	previous := providerRetryJitter
	providerRetryJitter = func() float64 { return value }
	t.Cleanup(func() { providerRetryJitter = previous })
}

func retryStatusError(status int, headers http.Header, message string) error {
	if headers == nil {
		headers = http.Header{}
	}
	return &retryableHTTPStatusError{status: status, headers: headers, inner: errors.New(message)}
}

func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }

func TestRetryProviderRequestRetriesSDKStatuses(t *testing.T) {
	fixedJitter(t, 0)
	for _, status := range []int{408, 409, 429, 500, 503} {
		calls := 0
		value, err := retryProviderRequest(context.Background(), &ai.StreamOptions{MaxRetries: intPointer(2)}, func() (string, error) {
			calls++
			if calls < 3 {
				return "", retryStatusError(status, http.Header{"Retry-After-Ms": []string{"1"}}, "boom")
			}
			return "ok", nil
		})
		if err != nil || value != "ok" || calls != 3 {
			t.Fatalf("status %d: value=%q err=%v calls=%d", status, value, err, calls)
		}
	}
	// Non-retryable statuses pass straight through.
	calls := 0
	_, err := retryProviderRequest(context.Background(), &ai.StreamOptions{MaxRetries: intPointer(2)}, func() (string, error) {
		calls++
		return "", retryStatusError(400, nil, "bad request")
	})
	if err == nil || calls != 1 {
		t.Fatalf("400: err=%v calls=%d, want immediate failure", err, calls)
	}
}

func TestRetryProviderRequestHonoursXShouldRetry(t *testing.T) {
	fixedJitter(t, 0)
	// x-should-retry true overrides a non-retryable status.
	calls := 0
	value, err := retryProviderRequest(context.Background(), &ai.StreamOptions{MaxRetries: intPointer(1)}, func() (string, error) {
		calls++
		if calls == 1 {
			return "", retryStatusError(400, http.Header{"X-Should-Retry": []string{"true"}, "Retry-After-Ms": []string{"1"}}, "odd")
		}
		return "ok", nil
	})
	if err != nil || value != "ok" || calls != 2 {
		t.Fatalf("true override: value=%q err=%v calls=%d", value, err, calls)
	}
	// x-should-retry false overrides a retryable status.
	calls = 0
	_, err = retryProviderRequest(context.Background(), &ai.StreamOptions{MaxRetries: intPointer(3)}, func() (string, error) {
		calls++
		return "", retryStatusError(429, http.Header{"X-Should-Retry": []string{"false"}}, "no")
	})
	if err == nil || calls != 1 {
		t.Fatalf("false override: err=%v calls=%d, want immediate failure", err, calls)
	}
}

func TestRetryProviderRequestFailsAboveMaxServerDelay(t *testing.T) {
	calls := 0
	_, err := retryProviderRequest(context.Background(),
		&ai.StreamOptions{MaxRetries: intPointer(3), MaxRetryDelayMS: int64Pointer(2000)},
		func() (string, error) {
			calls++
			return "", retryStatusError(429, http.Header{"Retry-After": []string{"30"}}, "please slow down")
		})
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (fail, not clamp)", calls)
	}
	want := "Server requested 30s retry delay (max: 2s). please slow down"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	// Zero disables the limit entirely (upstream: maxDelayMs > 0 guard).
	fixedJitter(t, 0)
	calls = 0
	_, err = retryProviderRequest(context.Background(),
		&ai.StreamOptions{MaxRetries: intPointer(1), MaxRetryDelayMS: int64Pointer(0)},
		func() (string, error) {
			calls++
			if calls == 1 {
				return "", retryStatusError(429, http.Header{"Retry-After-Ms": []string{"1"}}, "go on")
			}
			return "ok", nil
		})
	if err != nil || calls != 2 {
		t.Fatalf("disabled limit: err=%v calls=%d", err, calls)
	}
}

func TestRetryProviderRequestAbortsDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	start := time.Now()
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, err := retryProviderRequest(ctx, &ai.StreamOptions{MaxRetries: intPointer(1)}, func() (string, error) {
		calls++
		// A 30s server delay: only an interruptible sleep returns promptly.
		return "", retryStatusError(500, http.Header{"Retry-After": []string{"30"}}, "hold")
	})
	if err == nil || err.Error() != "Request aborted" {
		t.Fatalf("err = %v, want the upstream createAbortError text", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("abort took %s; the backoff sleep is not interruptible", elapsed)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRetryProviderRequestTreatsConnectionErrorsAsRetryable(t *testing.T) {
	fixedJitter(t, 1) // full jitter → shortest backoff (0.375s at retryIndex 0)
	calls := 0
	value, err := retryProviderRequest(context.Background(), &ai.StreamOptions{MaxRetries: intPointer(1)}, func() (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("dial tcp: connection refused")
		}
		return "ok", nil
	})
	if err != nil || value != "ok" || calls != 2 {
		t.Fatalf("connection error: value=%q err=%v calls=%d", value, err, calls)
	}
	// Zero retries (the default) never retries anything.
	calls = 0
	_, err = retryProviderRequest(context.Background(), nil, func() (string, error) {
		calls++
		return "", retryStatusError(500, nil, "boom")
	})
	if err == nil || calls != 1 {
		t.Fatalf("default: err=%v calls=%d, want single attempt", err, calls)
	}
}

func TestProviderRetryDelayReadsServerHeaders(t *testing.T) {
	// retry-after-ms takes precedence and is milliseconds.
	delay, err := providerRetryDelay(errors.New("x"), http.Header{"Retry-After-Ms": []string{"250"}, "Retry-After": []string{"9"}}, 0, nil)
	if err != nil || delay != 250*time.Millisecond {
		t.Fatalf("retry-after-ms: %s / %v", delay, err)
	}
	// retry-after in float seconds.
	delay, err = providerRetryDelay(errors.New("x"), http.Header{"Retry-After": []string{"1.5"}}, 0, nil)
	if err != nil || delay != 1500*time.Millisecond {
		t.Fatalf("retry-after seconds: %s / %v", delay, err)
	}
	// retry-after as an HTTP date resolves relative to now.
	when := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	delay, err = providerRetryDelay(errors.New("x"), http.Header{"Retry-After": []string{when}}, 0, nil)
	if err != nil || delay > 2*time.Second || delay < 500*time.Millisecond {
		t.Fatalf("retry-after date: %s / %v", delay, err)
	}
	if !strings.Contains(when, "GMT") {
		t.Fatalf("date format sanity: %q", when)
	}
	// An unparseable value backs off exponentially instead of retrying at once.
	for _, value := range []string{"soon", "NaN"} {
		delay, err = providerRetryDelay(errors.New("x"), http.Header{"Retry-After": []string{value}}, 2, nil)
		if err != nil || delay < 1500*time.Millisecond || delay > 2*time.Second {
			t.Fatalf("retry-after %q: %s / %v", value, delay, err)
		}
	}
}

// A provider stream that net/http reports cut short after the reply began ends as an assistant
// error the retry policy retries (upstream #11879), not as a bare stream error.
func TestDroppedProviderStreamIsARetryableAssistantError(t *testing.T) {
	for _, test := range []struct {
		name, begun string
		stream      func(*http.Client, string) (ai.AssistantMessageEventStream, error)
	}{
		{"Anthropic", `{"type":"message_start","message":{"id":"m","usage":{"input_tokens":1,"output_tokens":0}}}`, func(client *http.Client, url string) (ai.AssistantMessageEventStream, error) {
			model := anthropicTestModel()
			model.BaseURL = url
			return StreamAnthropicMessagesWithOptions(context.Background(), model, droppedStreamContext, &AnthropicMessagesOptions{StreamOptions: droppedStreamOptions(client)})
		}},
		{"OpenAI Completions", `{"id":"c","choices":[{"index":0,"delta":{"content":"partial"}}]}`, func(client *http.Client, url string) (ai.AssistantMessageEventStream, error) {
			model := &ai.Model{ID: "fixture", API: ai.APIOpenAICompletions, Provider: "openai", BaseURL: url, MaxTokens: 128}
			return StreamOpenAICompletionsWithOptions(context.Background(), model, droppedStreamContext, &OpenAICompletionsOptions{StreamOptions: droppedStreamOptions(client)})
		}},
		{"OpenAI Responses", `{"type":"response.created","response":{"id":"r"}}`, func(client *http.Client, url string) (ai.AssistantMessageEventStream, error) {
			model := responsesTestModel()
			model.BaseURL = url
			return StreamOpenAIResponsesWithOptions(context.Background(), model, droppedStreamContext, &OpenAIResponsesOptions{StreamOptions: droppedStreamOptions(client)})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, "data: "+test.begun+"\n\n")
				writer.(http.Flusher).Flush()
				// Close the connection inside the chunked body.
				connection, _, _ := writer.(http.Hijacker).Hijack()
				_ = connection.Close()
			}))
			defer server.Close()
			stream, err := test.stream(server.Client(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			message, err := ai.Collect(stream)
			if err != nil || !ai.IsRetryableAssistantError(message) {
				encoded, _ := ai.Marshal(message)
				t.Fatalf("message = %s (error %v), want a retryable assistant error", encoded, err)
			}
		})
	}
}

var droppedStreamContext = ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1}}}

func droppedStreamOptions(client *http.Client) ai.StreamOptions {
	key, retries := "key", 0
	return ai.StreamOptions{APIKey: &key, HTTPClient: client, MaxRetries: &retries}
}

// Connection failures keep their cause in the error message, as pi 1.5.1 now shows it (#11695).
func TestProviderConnectionErrorsShowTheirCause(t *testing.T) {
	if runtime.GOARCH == "wasm" {
		t.Skip("a Wasm host has no sockets to refuse or to present a certificate")
	}
	refused := httptest.NewServer(http.NotFoundHandler())
	refused.Close()
	untrusted := httptest.NewUnstartedServer(http.NotFoundHandler())
	untrusted.Config.ErrorLog = log.New(io.Discard, "", 0)
	untrusted.StartTLS()
	defer untrusted.Close()
	for url, cause := range map[string]string{refused.URL: "refused", untrusted.URL: "certificate"} {
		model := anthropicTestModel()
		model.BaseURL = url
		stream, err := StreamAnthropicMessagesWithOptions(context.Background(), model, droppedStreamContext, &AnthropicMessagesOptions{StreamOptions: droppedStreamOptions(nil)})
		if err != nil {
			t.Fatal(err)
		}
		if message, _ := ai.Collect(stream); message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, cause) {
			t.Fatalf("error = %+v, want the cause %q", message, cause)
		}
	}
}
