package chat

import (
	"context"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func partialUpdate(text string) engine.MessageUpdateEvent {
	return engine.MessageUpdateEvent{
		Message: &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: text}}},
	}
}

func TestCoalescerObserveNeverPanicsOrBlocks(t *testing.T) {
	c := new(coalescer)
	events := []any{
		nil,
		"garbage",
		engine.MessageUpdateEvent{},
		engine.MessageUpdateEvent{Message: "not assistant"},             // wrong type
		engine.MessageUpdateEvent{Message: (*ai.AssistantMessage)(nil)}, // typed nil
		partialUpdate(""),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Repeated observes must never block.
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
	if panics := c.panics.Load(); panics != 0 {
		t.Fatalf("well-formed events counted %d panics", panics)
	}
}

func TestPreviewRendererSurvivesAdapterPanics(t *testing.T) {
	c := new(coalescer)
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
