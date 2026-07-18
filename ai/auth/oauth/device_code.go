package oauth

import (
	"context"
	"errors"
	"math"
	"time"
)

const (
	deviceCodeCancelMessage   = "Login cancelled"
	deviceCodeTimeoutMessage  = "Device flow timed out"
	deviceCodeSlowDownTimeout = "Device flow timed out after one or more slow_down responses. This is often caused by clock drift in WSL or VM environments. Please sync or restart the VM clock and try again."
	minimumPollInterval       = time.Second
	defaultPollInterval       = 5 * time.Second
	slowDownIncrement         = 5 * time.Second
)

type deviceCodePollStatus string

const (
	deviceCodePending  deviceCodePollStatus = "pending"
	deviceCodeSlowDown deviceCodePollStatus = "slow_down"
	deviceCodeFailed   deviceCodePollStatus = "failed"
	deviceCodeComplete deviceCodePollStatus = "complete"
)

type deviceCodePollResult[T any] struct {
	status          deviceCodePollStatus
	value           T
	intervalSeconds *float64
	message         string
}

type deviceCodePollOptions[T any] struct {
	intervalSeconds  *float64
	expiresInSeconds *float64
	waitBeforeFirst  bool
	poll             func() (deviceCodePollResult[T], error)
	ctx              context.Context
	now              func() time.Time
	sleep            func(context.Context, time.Duration) error
}

func pollOAuthDeviceCodeFlow[T any](options deviceCodePollOptions[T]) (T, error) {
	var zero T
	ctx := options.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	now := options.now
	if now == nil {
		now = time.Now
	}
	sleep := options.sleep
	if sleep == nil {
		sleep = sleepDeviceCode
	}
	deadline := math.Inf(1)
	if options.expiresInSeconds != nil {
		deadline = float64(now().UnixMilli()) + *options.expiresInSeconds*1000
	}
	interval := defaultPollInterval
	if options.intervalSeconds != nil {
		interval = time.Duration(math.Floor(*options.intervalSeconds*1000)) * time.Millisecond
	}
	interval = max(interval, minimumPollInterval)

	slowDownResponses := 0
	if options.waitBeforeFirst {
		remaining := deviceCodeRemaining(deadline, now)
		if remaining > 0 {
			if err := sleep(ctx, min(interval, remaining)); err != nil {
				return zero, errors.New(deviceCodeCancelMessage)
			}
		}
	}

	for float64(now().UnixMilli()) < deadline {
		if ctx.Err() != nil {
			return zero, errors.New(deviceCodeCancelMessage)
		}
		result, err := options.poll()
		if err != nil {
			return zero, err
		}
		switch result.status {
		case deviceCodeComplete:
			return result.value, nil
		case deviceCodeFailed:
			return zero, errors.New(result.message)
		case deviceCodeSlowDown:
			slowDownResponses++
			if result.intervalSeconds != nil && !math.IsNaN(*result.intervalSeconds) && !math.IsInf(*result.intervalSeconds, 0) && *result.intervalSeconds > 0 {
				interval = max(minimumPollInterval, time.Duration(math.Floor(*result.intervalSeconds*1000))*time.Millisecond)
			} else {
				interval = max(minimumPollInterval, interval+slowDownIncrement)
			}
		}
		remaining := deviceCodeRemaining(deadline, now)
		if remaining <= 0 {
			break
		}
		if err := sleep(ctx, min(interval, remaining)); err != nil {
			return zero, errors.New(deviceCodeCancelMessage)
		}
	}
	if slowDownResponses > 0 {
		return zero, errors.New(deviceCodeSlowDownTimeout) //nolint:staticcheck // Exact upstream error text is observable.
	}
	return zero, errors.New(deviceCodeTimeoutMessage) //nolint:staticcheck // Exact upstream error text is observable.
}

func deviceCodeRemaining(deadline float64, now func() time.Time) time.Duration {
	if math.IsInf(deadline, 1) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(deadline-float64(now().UnixMilli())) * time.Millisecond
}

func sleepDeviceCode(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
