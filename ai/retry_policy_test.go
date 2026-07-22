package ai

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRetryAssistantCallPolicyParity(t *testing.T) {
	t.Run("success is returned without retry callbacks", func(t *testing.T) {
		success := retryPolicyMessage(StopReasonStop, "", "ok")
		calls := 0
		finished := 0
		got, err := RetryAssistantCall(context.Background(), func() (*AssistantMessage, error) {
			calls++
			return success, nil
		}, &RetryPolicy{Enabled: true, MaxRetries: 3}, &RetryCallbacks{
			OnRetryFinished: func(bool, int, *string) error {
				finished++
				return nil
			},
		})
		if err != nil || got != success || calls != 1 || finished != 0 {
			t.Fatalf("got=%#v err=%v calls=%d finished=%d", got, err, calls, finished)
		}
	})

	t.Run("transient errors retry in lifecycle order and stop on success", func(t *testing.T) {
		calls := 0
		events := []string{}
		got, err := RetryAssistantCall(context.Background(), func() (*AssistantMessage, error) {
			events = append(events, "produce")
			calls++
			if calls < 3 {
				return retryPolicyMessage(StopReasonError, "terminated", ""), nil
			}
			return retryPolicyMessage(StopReasonStop, "", "recovered"), nil
		}, &RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMS: 1}, &RetryCallbacks{
			OnRetryScheduled: func(attempt, maxAttempts int, delayMS int64, errorMessage string) error {
				events = append(events, "scheduled")
				wantDelayMS := int64(1 << (attempt - 1))
				if attempt < 1 || maxAttempts != 3 || delayMS != wantDelayMS || errorMessage != "terminated" {
					t.Fatalf("scheduled args = %d %d %d %q", attempt, maxAttempts, delayMS, errorMessage)
				}
				return nil
			},
			OnRetryAttemptStart: func() error {
				events = append(events, "attempt-start")
				return nil
			},
			OnRetryFinished: func(success bool, attempt int, finalError *string) error {
				events = append(events, "finished")
				if !success || attempt != 2 || finalError != nil {
					t.Fatalf("finished args = %t %d %#v", success, attempt, finalError)
				}
				return nil
			},
		})
		wantEvents := []string{"produce", "scheduled", "attempt-start", "produce", "scheduled", "attempt-start", "produce", "finished"}
		if err != nil || got.StopReason != StopReasonStop || calls != 3 || !reflect.DeepEqual(events, wantEvents) {
			t.Fatalf("got=%#v err=%v calls=%d events=%#v", got, err, calls, events)
		}
	})

	t.Run("retry exhaustion reports the final provider error", func(t *testing.T) {
		calls := 0
		var finishedError *string
		got, err := RetryAssistantCall(context.Background(), func() (*AssistantMessage, error) {
			calls++
			return retryPolicyMessage(StopReasonError, "terminated", ""), nil
		}, &RetryPolicy{Enabled: true, MaxRetries: 3}, &RetryCallbacks{
			OnRetryFinished: func(success bool, attempt int, finalError *string) error {
				if success || attempt != 3 {
					t.Fatalf("finished args = %t %d", success, attempt)
				}
				finishedError = finalError
				return nil
			},
		})
		if err != nil || got.StopReason != StopReasonError || calls != 4 || finishedError == nil || *finishedError != "terminated" {
			t.Fatalf("got=%#v err=%v calls=%d final=%#v", got, err, calls, finishedError)
		}
	})

	t.Run("non-retryable and disabled failures do not enter lifecycle", func(t *testing.T) {
		for _, testCase := range []struct {
			name   string
			policy *RetryPolicy
			error  string
		}{
			{name: "quota", policy: &RetryPolicy{Enabled: true, MaxRetries: 3}, error: "insufficient_quota"},
			{name: "disabled", policy: &RetryPolicy{Enabled: false, MaxRetries: 3}, error: "terminated"},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				calls := 0
				callbacks := 0
				got, err := RetryAssistantCall(context.Background(), func() (*AssistantMessage, error) {
					calls++
					return retryPolicyMessage(StopReasonError, testCase.error, ""), nil
				}, testCase.policy, &RetryCallbacks{
					OnRetryScheduled: func(int, int, int64, string) error { callbacks++; return nil },
					OnRetryFinished:  func(bool, int, *string) error { callbacks++; return nil },
				})
				if err != nil || got.StopReason != StopReasonError || calls != 1 || callbacks != 0 {
					t.Fatalf("got=%#v err=%v calls=%d callbacks=%d", got, err, calls, callbacks)
				}
			})
		}
	})

	t.Run("an aborted retried call finishes unsuccessfully without a final error", func(t *testing.T) {
		calls := 0
		finished := false
		got, err := RetryAssistantCall(context.Background(), func() (*AssistantMessage, error) {
			calls++
			if calls == 1 {
				return retryPolicyMessage(StopReasonError, "terminated", ""), nil
			}
			return retryPolicyMessage(StopReasonAborted, "provider abort", ""), nil
		}, &RetryPolicy{Enabled: true, MaxRetries: 3}, &RetryCallbacks{
			OnRetryFinished: func(success bool, attempt int, finalError *string) error {
				finished = true
				if success || attempt != 1 || finalError != nil {
					t.Fatalf("finished args = %t %d %#v", success, attempt, finalError)
				}
				return nil
			},
		})
		if err != nil || got.StopReason != StopReasonAborted || calls != 2 || !finished {
			t.Fatalf("got=%#v err=%v calls=%d finished=%t", got, err, calls, finished)
		}
	})

	t.Run("cancelling backoff normalizes the last response to aborted", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		original := retryPolicyMessage(StopReasonError, "terminated", "partial")
		var finalError *string
		got, err := RetryAssistantCall(ctx, func() (*AssistantMessage, error) {
			return original, nil
		}, &RetryPolicy{Enabled: true, MaxRetries: 5, BaseDelayMS: 10_000}, &RetryCallbacks{
			OnRetryScheduled: func(int, int, int64, string) error {
				cancel()
				return nil
			},
			OnRetryFinished: func(success bool, attempt int, providerError *string) error {
				if success || attempt != 1 {
					t.Fatalf("finished args = %t %d", success, attempt)
				}
				finalError = providerError
				return nil
			},
		})
		if err != nil || got == original || got.StopReason != StopReasonAborted || got.ErrorMessage != nil || finalError == nil || *finalError != "terminated" {
			t.Fatalf("got=%#v err=%v final=%#v", got, err, finalError)
		}
	})

	t.Run("producer and callback errors remain terminal", func(t *testing.T) {
		produceErr := errors.New("produce failed")
		if _, err := RetryAssistantCall(context.Background(), func() (*AssistantMessage, error) {
			return nil, produceErr
		}, &RetryPolicy{Enabled: true, MaxRetries: 3}, nil); !errors.Is(err, produceErr) {
			t.Fatalf("producer error = %v", err)
		}
	})
}

func retryPolicyMessage(reason StopReason, errorMessage, text string) *AssistantMessage {
	message := &AssistantMessage{
		Content:    AssistantContent{&TextContent{Text: text}},
		StopReason: reason,
		Usage:      Usage{Cost: Cost{}},
	}
	if errorMessage != "" {
		message.ErrorMessage = &errorMessage
	}
	return message
}
