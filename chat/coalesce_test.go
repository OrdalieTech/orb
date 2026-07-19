package chat

import (
	"context"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

func partialUpdate(text string) agent.MessageUpdateEvent {
	return agent.MessageUpdateEvent{
		Message: &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: text}}},
	}
}

func TestCoalescerKeepsLatestSnapshotOnly(t *testing.T) {
	c := newCoalescer()
	c.observe(partialUpdate("he"))
	c.observe(partialUpdate("hello"))
	c.observe(partialUpdate("hello wor"))
	text, dirty := c.snapshot()
	if !dirty || text != "hello wor" {
		t.Fatalf("snapshot = %q dirty=%v", text, dirty)
	}
	c.rendered("hello wor")
	if _, dirty := c.snapshot(); dirty {
		t.Fatal("rendered snapshot still dirty")
	}
	// A stale rendered() call must not clear a newer snapshot.
	c.observe(partialUpdate("hello world"))
	c.rendered("hello wor")
	if text, dirty := c.snapshot(); !dirty || text != "hello world" {
		t.Fatalf("stale rendered cleared newer snapshot: %q dirty=%v", text, dirty)
	}
}

func TestCoalescerObserveNeverPanicsOrBlocks(t *testing.T) {
	c := newCoalescer()
	events := []any{
		nil,
		"garbage",
		agent.MessageUpdateEvent{}, // nil message
		agent.MessageUpdateEvent{Message: "not assistant"},             // wrong type
		agent.MessageUpdateEvent{Message: (*ai.AssistantMessage)(nil)}, // typed nil
		partialUpdate(""),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Repeated observes must never block even though nobody drains notify.
		for range 100 {
			for _, event := range events {
				c.observe(event)
			}
			c.observe(partialUpdate("text"))
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("observe blocked")
	}
	if text, dirty := c.snapshot(); !dirty || text != "text" {
		t.Fatalf("snapshot = %q dirty=%v", text, dirty)
	}
}

func TestPreviewRendererSurvivesAdapterPanics(t *testing.T) {
	c := newCoalescer()
	delivery := &fauxDelivery{previewPanics: 1}
	c.observe(partialUpdate("first"))
	var previewIDs []string
	stop := startPreviewRenderer(context.Background(), c, delivery, time.Millisecond, func(id string) {
		previewIDs = append(previewIDs, id)
	})
	// First tick panics inside Preview; the renderer must keep going and
	// deliver the still-dirty snapshot on a later tick.
	waitUntil(t, 2*time.Second, "preview after panic", func() bool {
		return len(delivery.snapshotPreviews()) > 0
	})
	stop()
	stop() // stop is idempotent
	if got := delivery.snapshotPreviews(); got[0] != "first" {
		t.Fatalf("previews = %#v", got)
	}
	if len(previewIDs) == 0 || previewIDs[0] != "pv-1" {
		t.Fatalf("preview ids = %#v", previewIDs)
	}
}

func TestPreviewRendererStopsCleanly(t *testing.T) {
	c := newCoalescer()
	delivery := &fauxDelivery{}
	stop := startPreviewRenderer(context.Background(), c, delivery, time.Millisecond, func(string) {})
	c.observe(partialUpdate("tick"))
	waitUntil(t, 2*time.Second, "first preview", func() bool {
		return len(delivery.snapshotPreviews()) > 0
	})
	stop()
	count := len(delivery.snapshotPreviews())
	c.observe(partialUpdate("after stop"))
	time.Sleep(10 * time.Millisecond)
	if len(delivery.snapshotPreviews()) != count {
		t.Fatal("renderer kept previewing after stop")
	}
}
