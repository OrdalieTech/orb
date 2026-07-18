package oauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPollOAuthDeviceCodeFlowPendingSlowDownComplete(t *testing.T) {
	now := time.UnixMilli(1_000)
	sleeps := make([]time.Duration, 0)
	interval := 2.0
	provided := 9.0
	results := []deviceCodePollResult[string]{
		{status: deviceCodePending},
		{status: deviceCodeSlowDown},
		{status: deviceCodeSlowDown, intervalSeconds: &provided},
		{status: deviceCodeComplete, value: "token"},
	}
	value, err := pollOAuthDeviceCodeFlow(deviceCodePollOptions[string]{
		intervalSeconds: &interval,
		waitBeforeFirst: true,
		ctx:             context.Background(),
		now:             func() time.Time { return now },
		sleep: func(_ context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			now = now.Add(duration)
			return nil
		},
		poll: func() (deviceCodePollResult[string], error) {
			result := results[0]
			results = results[1:]
			return result, nil
		},
	})
	if err != nil || value != "token" {
		t.Fatalf("poll = %q, %v", value, err)
	}
	want := []time.Duration{2 * time.Second, 2 * time.Second, 7 * time.Second, 9 * time.Second}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", sleeps, want)
	}
	for index := range want {
		if sleeps[index] != want[index] {
			t.Fatalf("sleeps = %v, want %v", sleeps, want)
		}
	}
}

func TestPollOAuthDeviceCodeFlowTimeoutAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name      string
		result    deviceCodePollResult[string]
		want      string
		cancelled bool
	}{
		{name: "timeout", result: deviceCodePollResult[string]{status: deviceCodePending}, want: deviceCodeTimeoutMessage},
		{name: "slow-down-timeout", result: deviceCodePollResult[string]{status: deviceCodeSlowDown}, want: deviceCodeSlowDownTimeout},
		{name: "cancelled", result: deviceCodePollResult[string]{status: deviceCodePending}, want: deviceCodeCancelMessage, cancelled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if test.cancelled {
				cancel()
			} else {
				defer cancel()
			}
			now := time.UnixMilli(0)
			expires := 1.0
			_, err := pollOAuthDeviceCodeFlow(deviceCodePollOptions[string]{
				expiresInSeconds: &expires,
				ctx:              ctx,
				now:              func() time.Time { return now },
				sleep: func(_ context.Context, duration time.Duration) error {
					now = now.Add(duration)
					return nil
				},
				poll: func() (deviceCodePollResult[string], error) { return test.result, nil },
			})
			if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPollOAuthDeviceCodeFlowPropagatesPollFailure(t *testing.T) {
	want := errors.New("network")
	_, err := pollOAuthDeviceCodeFlow(deviceCodePollOptions[string]{
		ctx: context.Background(),
		poll: func() (deviceCodePollResult[string], error) {
			return deviceCodePollResult[string]{}, want
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
