package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
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

func TestProviderRetryDelayBackoffFormula(t *testing.T) {
	fixedJitter(t, 0) // no jitter → exact upstream curve: min(0.5*2^i, 8) seconds
	for retryIndex, want := range []time.Duration{500, 1000, 2000, 4000, 8000, 8000} {
		delay, err := providerRetryDelay(errors.New("x"), http.Header{}, retryIndex, nil)
		if err != nil || delay != want*time.Millisecond {
			t.Fatalf("retryIndex %d: delay=%s err=%v, want %s", retryIndex, delay, err, want*time.Millisecond)
		}
	}
	fixedJitter(t, 1) // full jitter shaves 25%
	delay, err := providerRetryDelay(errors.New("x"), http.Header{}, 0, nil)
	if err != nil || delay != 375*time.Millisecond {
		t.Fatalf("jittered: delay=%s err=%v, want 375ms", delay, err)
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
}
