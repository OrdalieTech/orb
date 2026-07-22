package ai

import (
	"context"
	"regexp"
	"time"
)

// RetryPolicy applies bounded exponential backoff to assistant-producing calls.
// MaxRetries counts retries after the initial call.
type RetryPolicy struct {
	Enabled     bool  `json:"enabled"`
	MaxRetries  int   `json:"maxRetries"`
	BaseDelayMS int64 `json:"baseDelayMs"`
}

// RetryCallbacks reports the lifecycle of retries after the initial call.
// Callback errors stop the retry operation, matching rejected async callbacks upstream.
type RetryCallbacks struct {
	OnRetryScheduled    func(attempt, maxAttempts int, delayMS int64, errorMessage string) error
	OnRetryAttemptStart func() error
	OnRetryFinished     func(success bool, attempt int, finalError *string) error
}

// AssistantCall produces one terminal assistant message. Returned errors are
// terminal because upstream retries message-level provider failures, not rejected calls.
type AssistantCall func() (*AssistantMessage, error)

var nonRetryableProviderLimitPatterns = compilePatterns([]string{
	`GoUsageLimitError`, `FreeUsageLimitError`, `Monthly usage limit reached`, `available balance`,
	`insufficient_quota`, `out of budget`, `quota exceeded`, `billing`,
})

var retryableProviderPatterns = compilePatterns([]string{
	`overloaded`, `rate.?limit`, `too many requests`, `429`, `500`, `502`, `503`, `504`, `524`,
	`service.?unavailable`, `server.?error`, `internal.?error`, `provider.?returned.?error`,
	`network.?error`, `connection.?error`, `connection.?refused`, `connection.?lost`, `other side closed`,
	`fetch failed`, `upstream.?connect`, `reset before headers`, `socket hang up`,
	`socket connection was closed`, `timed? out`, `timeout`, `terminated`, `websocket.?closed`,
	`websocket.?error`, `ended without`, `stream ended before message_stop`,
	`stream ended before a terminal response event`, `http2 request did not get a response`, `retry delay`,
	`you can retry your request`, `try your request again`, `please retry your request`, `ResourceExhausted`,
})

var overflowPatterns = compilePatterns([]string{
	`prompt is too long`, `request_too_large`, `input is too long for requested model`,
	`exceeds the context window`, `exceeds (the )?(model'?s )?maximum context length( of [0-9,]+ tokens?|[[:space:]]*\([0-9,]+\))`,
	`input token count.*exceeds the maximum`, `maximum prompt length is [0-9]+`,
	`reduce the length of the messages`, `range of input length should be`, `maximum context length is [0-9]+ tokens`,
	`exceeds (the )?maximum allowed input length of [0-9,]+ tokens?`,
	`input \([0-9]+ tokens\) is longer than the model'?s context length \([0-9]+ tokens\)`,
	`exceeds the limit of [0-9]+`, `exceeds the available context size`, `greater than the context length`,
	`context window exceeds limit`, `exceeded model token limit`,
	`too large for model with [0-9]+ maximum context length`,
	`prompt has [0-9,]+ tokens?, but the configured context size is [0-9,]+ tokens?`,
	`model_context_window_exceeded`, `prompt too long; exceeded (max )?context length`,
	`context[_ ]length[_ ]exceeded`, `too many tokens`, `token limit exceeded`,
	`^4(00|13)[[:space:]]*(status code)?[[:space:]]*\(no body\)`,
})

var nonOverflowPatterns = compilePatterns([]string{
	`^(Throttling error|Service unavailable):`, `rate limit`, `too many requests`,
})

// IsRetryableAssistantError reports whether a failed assistant message is a
// transient provider or transport error.
func IsRetryableAssistantError(message *AssistantMessage) bool {
	if message == nil || message.StopReason != StopReasonError || message.ErrorMessage == nil || *message.ErrorMessage == "" {
		return false
	}
	text := *message.ErrorMessage
	return !matchesAny(nonRetryableProviderLimitPatterns, text) && matchesAny(retryableProviderPatterns, text)
}

// RetryAssistantCall runs produce once and retries transient assistant errors
// according to policy. Cancellation during backoff is returned as a shallow
// copy of the last response with an aborted stop reason and no error message.
func RetryAssistantCall(
	ctx context.Context,
	produce AssistantCall,
	policy *RetryPolicy,
	callbacks *RetryCallbacks,
) (*AssistantMessage, error) {
	maxAttempts := 0
	if policy != nil && policy.Enabled {
		maxAttempts = policy.MaxRetries
	}

	attempt := 0
	lastRetryError := ""
	for {
		response, err := produce()
		if err != nil {
			return nil, err
		}

		if response.StopReason == StopReasonAborted {
			if attempt > 0 {
				if err := retryFinished(callbacks, false, attempt, nil); err != nil {
					return nil, err
				}
			}
			return response, nil
		}
		if response.StopReason != StopReasonError {
			if attempt > 0 {
				if err := retryFinished(callbacks, true, attempt, nil); err != nil {
					return nil, err
				}
			}
			return response, nil
		}
		if attempt >= maxAttempts || !IsRetryableAssistantError(response) {
			if attempt > 0 {
				if err := retryFinished(callbacks, false, attempt, response.ErrorMessage); err != nil {
					return nil, err
				}
			}
			return response, nil
		}

		attempt++
		lastRetryError = "Unknown error"
		if response.ErrorMessage != nil && *response.ErrorMessage != "" {
			lastRetryError = *response.ErrorMessage
		}
		delayMS := retryDelayMS(policy.BaseDelayMS, attempt)
		if callbacks != nil && callbacks.OnRetryScheduled != nil {
			if err := callbacks.OnRetryScheduled(attempt, maxAttempts, delayMS, lastRetryError); err != nil {
				return nil, err
			}
		}

		if !waitRetryDelay(ctx, delayMS) {
			finalError := lastRetryError
			if err := retryFinished(callbacks, false, attempt, &finalError); err != nil {
				return nil, err
			}
			aborted := *response
			aborted.StopReason = StopReasonAborted
			aborted.ErrorMessage = nil
			return &aborted, nil
		}
		if callbacks != nil && callbacks.OnRetryAttemptStart != nil {
			if err := callbacks.OnRetryAttemptStart(); err != nil {
				return nil, err
			}
		}
	}
}

func retryFinished(callbacks *RetryCallbacks, success bool, attempt int, finalError *string) error {
	if callbacks == nil || callbacks.OnRetryFinished == nil {
		return nil
	}
	return callbacks.OnRetryFinished(success, attempt, finalError)
}

func retryDelayMS(baseDelayMS int64, attempt int) int64 {
	delayMS := baseDelayMS
	for current := 1; current < attempt; current++ {
		delayMS *= 2
	}
	return delayMS
}

func waitRetryDelay(ctx context.Context, delayMS int64) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	timer := time.NewTimer(time.Duration(delayMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// IsContextOverflow recognizes upstream's explicit and silent overflow forms.
func IsContextOverflow(message *AssistantMessage, contextWindow float64) bool {
	if message == nil {
		return false
	}
	if message.StopReason == StopReasonError && message.ErrorMessage != nil {
		text := *message.ErrorMessage
		if !matchesAny(nonOverflowPatterns, text) && matchesAny(overflowPatterns, text) {
			return true
		}
	}
	inputTokens := message.Usage.Input + message.Usage.CacheRead
	if contextWindow > 0 && message.StopReason == StopReasonStop && float64(inputTokens) > contextWindow {
		return true
	}
	return contextWindow > 0 && message.StopReason == StopReasonLength && message.Usage.Output == 0 && float64(inputTokens) >= contextWindow*0.99
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, len(patterns))
	for index, pattern := range patterns {
		compiled[index] = regexp.MustCompile(`(?i)` + pattern)
	}
	return compiled
}

func matchesAny(patterns []*regexp.Regexp, value string) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}
