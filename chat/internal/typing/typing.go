// Package typing keeps a platform typing indicator alive while a turn runs.
package typing

import (
	"context"
	"sync"
	"time"
)

// Refresher re-fires a typing indicator on a ticker. The zero value is ready.
type Refresher struct {
	mu   sync.Mutex
	stop chan struct{}
}

// Start launches the refresh loop unless one was started and not stopped:
// refresh runs every interval until Stop, ctx ends, or refresh returns false.
func (r *Refresher) Start(ctx context.Context, interval time.Duration, refresh func() bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stop != nil {
		return
	}
	stop := make(chan struct{})
	r.stop = stop
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !refresh() {
					return
				}
			}
		}
	}()
}

// Stop ends the loop, if one is running.
func (r *Refresher) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stop != nil {
		close(r.stop)
		r.stop = nil
	}
}
