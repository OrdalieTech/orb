package ctxsleep

import (
	"context"
	"testing"
	"time"
)

func TestSleepZeroDelayIgnoresCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, 0); err != nil {
		t.Fatalf("Sleep(cancelled, 0) = %v, want nil", err)
	}
	if err := Sleep(ctx, -time.Second); err != nil {
		t.Fatalf("Sleep(cancelled, -1s) = %v, want nil", err)
	}
}

func TestSleepCancelledContextReturnsErr(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Minute); err != context.Canceled {
		t.Fatalf("Sleep(cancelled, 1m) = %v, want context.Canceled", err)
	}
}

func TestSleepElapses(t *testing.T) {
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("Sleep = %v, want nil", err)
	}
}
