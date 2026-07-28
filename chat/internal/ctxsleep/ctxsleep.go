// Package ctxsleep provides a context-aware sleep shared by the chat
// adapters and processor.
package ctxsleep

import (
	"context"
	"time"
)

// Sleep pauses for d, honoring ctx cancellation. A non-positive d returns
// nil immediately without consulting ctx.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
