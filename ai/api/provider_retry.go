package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	anthropic "github.com/anthropics/anthropic-sdk-go"
	openai "github.com/openai/openai-go/v3"
)

// Mirrors upstream utils/provider-retry.ts (7af8533c). The pinned SDKs' own
// retry timers ignore the request's abort signal, so every SDK call runs with
// maxRetries 0 and this wrapper owns retrying: the backoff sleep honours the
// context, the classification mirrors the SDKs (x-should-retry, 408/409/429,
// 5xx, connection errors), and a server-requested delay above maxRetryDelayMs
// fails instead of being clamped (60 seconds by default, zero disables).

const defaultMaxProviderRetryDelayMS = 60_000

// providerRetryJitter mirrors Math.random in the upstream backoff formula.
var providerRetryJitter = rand.Float64

// retryableHTTPStatusError adapts a hand-rolled HTTP surface (Azure) to the
// classification below; the wrapped error is what callers observe.
type retryableHTTPStatusError struct {
	status  int
	headers http.Header
	inner   error
}

func (statusError *retryableHTTPStatusError) Error() string { return statusError.inner.Error() }
func (statusError *retryableHTTPStatusError) Unwrap() error { return statusError.inner }

// providerErrorParts mirrors isProviderError: SDK errors expose their status
// and headers, and anything else thrown by a request is the connection-error
// class whose status is undefined (retryable). Context cancellation is the
// abort path and never classifies.
func providerErrorParts(err error) (status *int, headers http.Header, isProviderError bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, nil, false
	}
	var openaiError *openai.Error
	if errors.As(err, &openaiError) {
		if openaiError.Response != nil {
			headers = openaiError.Response.Header
		}
		return &openaiError.StatusCode, headers, true
	}
	var anthropicError *anthropic.Error
	if errors.As(err, &anthropicError) {
		if anthropicError.Response != nil {
			headers = anthropicError.Response.Header
		}
		return &anthropicError.StatusCode, headers, true
	}
	var statusError *retryableHTTPStatusError
	if errors.As(err, &statusError) {
		return &statusError.status, statusError.headers, true
	}
	return nil, nil, true
}

// isRetryableProviderError mirrors the pinned OpenAI/Anthropic SDK retry
// policy; review when either SDK is upgraded.
func isRetryableProviderError(status *int, headers http.Header) bool {
	switch headers.Get("x-should-retry") {
	case "true":
		return true
	case "false":
		return false
	}
	if status == nil {
		return true
	}
	return *status == http.StatusRequestTimeout || *status == http.StatusConflict ||
		*status == http.StatusTooManyRequests || *status >= http.StatusInternalServerError
}

func validateServerRetryDelay(delayMS float64, maxRetryDelayMS *int64, providerErrorMessage string) (time.Duration, error) {
	maxDelayMS := int64(defaultMaxProviderRetryDelayMS)
	if maxRetryDelayMS != nil {
		maxDelayMS = *maxRetryDelayMS
	}
	if maxDelayMS > 0 && delayMS > float64(maxDelayMS) {
		return 0, fmt.Errorf("Server requested %ds retry delay (max: %ds). %s", //nolint:staticcheck // Exact upstream error text is observable.
			int64(math.Ceil(delayMS/1000)), int64(math.Ceil(float64(maxDelayMS)/1000)), providerErrorMessage)
	}
	return time.Duration(delayMS * float64(time.Millisecond)), nil
}

func providerRetryDelay(err error, headers http.Header, retryIndex int, maxRetryDelayMS *int64) (time.Duration, error) {
	if value := headers.Get("retry-after-ms"); value != "" {
		if parsed, parseErr := strconv.ParseFloat(value, 64); parseErr == nil {
			return validateServerRetryDelay(parsed, maxRetryDelayMS, err.Error())
		}
	}
	if value := headers.Get("retry-after"); value != "" {
		if seconds, parseErr := strconv.ParseFloat(value, 64); parseErr == nil {
			return validateServerRetryDelay(seconds*1000, maxRetryDelayMS, err.Error())
		}
		delayMS := float64(0)
		if when, parseErr := http.ParseTime(value); parseErr == nil {
			delayMS = float64(time.Until(when) / time.Millisecond)
		}
		return validateServerRetryDelay(delayMS, maxRetryDelayMS, err.Error())
	}
	exponential := math.Min(0.5*math.Pow(2, float64(retryIndex)), 8) * 1000
	return time.Duration(exponential * (1 - providerRetryJitter()*0.25) * float64(time.Millisecond)), nil
}

// imagesStreamOptions views the images options' retry fields through the
// StreamOptions shape the wrapper reads.
func imagesStreamOptions(options *ai.ImagesOptions) *ai.StreamOptions {
	if options == nil {
		return nil
	}
	return &ai.StreamOptions{MaxRetries: options.MaxRetries, MaxRetryDelayMS: options.MaxRetryDelayMS}
}

// createProviderAbortError mirrors createAbortError: aborts surface a plain
// error whose message replaces whatever the request failed with.
func createProviderAbortError() error {
	return errors.New("Request aborted") //nolint:staticcheck // Exact upstream error text is observable.
}

// retryProviderRequest reproduces retryProviderRequest: each retry is a fresh
// SDK request, and the sleep between attempts aborts with the context.
func retryProviderRequest[T any](ctx context.Context, options *ai.StreamOptions, request func() (T, error)) (T, error) {
	maxRetries := 0
	var maxRetryDelayMS *int64
	if options != nil {
		if options.MaxRetries != nil {
			maxRetries = *options.MaxRetries
		}
		maxRetryDelayMS = options.MaxRetryDelayMS
	}
	retriesRemaining := maxRetries
	for {
		value, err := request()
		if err == nil {
			return value, nil
		}
		if ctx.Err() != nil {
			return value, createProviderAbortError()
		}
		status, headers, isProviderError := providerErrorParts(err)
		if retriesRemaining <= 0 || !isProviderError || !isRetryableProviderError(status, headers) {
			return value, err
		}
		retryIndex := maxRetries - retriesRemaining
		retriesRemaining--
		delay, delayErr := providerRetryDelay(err, headers, retryIndex, maxRetryDelayMS)
		if delayErr != nil {
			return value, delayErr
		}
		timer := time.NewTimer(max(0, delay))
		select {
		case <-ctx.Done():
			timer.Stop()
			return value, createProviderAbortError()
		case <-timer.C:
		}
	}
}
