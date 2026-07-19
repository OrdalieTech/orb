package chat

import (
	"context"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

// coalescer collapses streamed assistant partials into a latest-full-snapshot.
// Its observe method runs inline on the turn-driving goroutine (session
// Subscribe callbacks have no recover), so it never blocks and never lets a
// panic escape. Memory is bounded by construction: only the latest snapshot
// is retained.
type coalescer struct {
	mu     sync.Mutex
	latest string
	dirty  bool
	notify chan struct{}
}

func newCoalescer() *coalescer {
	return &coalescer{notify: make(chan struct{}, 1)}
}

// observe is a session event listener extracting partial assistant text.
func (c *coalescer) observe(event any) {
	defer func() { _ = recover() }()
	update, ok := event.(agent.MessageUpdateEvent)
	if !ok {
		return
	}
	partial, ok := update.Message.(*ai.AssistantMessage)
	if !ok {
		return
	}
	text := assistantText(partial)
	if text == "" {
		return
	}
	c.mu.Lock()
	if text != c.latest {
		c.latest = text
		c.dirty = true
	}
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// snapshot returns the latest text and whether it is unrendered.
func (c *coalescer) snapshot() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest, c.dirty
}

// rendered clears the dirty flag when text is still the latest snapshot, so
// a failed or stale preview stays scheduled for the next tick.
func (c *coalescer) rendered(text string) {
	c.mu.Lock()
	if c.latest == text {
		c.dirty = false
	}
	c.mu.Unlock()
}

// startPreviewRenderer runs the per-turn renderer goroutine: every interval,
// a dirty snapshot is pushed through delivery.Preview (rate limiting and 429
// handling live inside the adapter). onPreview is invoked with the platform
// preview id after each successful render. The returned stop function halts
// the renderer and waits for it to exit; it is safe to call more than once.
func startPreviewRenderer(
	ctx context.Context,
	c *coalescer,
	delivery Delivery,
	interval time.Duration,
	onPreview func(previewID string),
) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			text, dirty := c.snapshot()
			if !dirty {
				continue
			}
			if !safePreview(ctx, delivery, text) {
				continue
			}
			c.rendered(text)
			if id := delivery.PreviewID(); id != "" {
				onPreview(id)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-finished
		})
	}
}

// safePreview shields the renderer from adapter panics and reports success.
func safePreview(ctx context.Context, delivery Delivery, text string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return delivery.Preview(ctx, text) == nil
}
