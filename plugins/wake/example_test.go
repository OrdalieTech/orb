package wake_test

import (
	"context"
	"fmt"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/plugins/wake"
)

// A plain engine.Agent gets wake's tools for its conversation, and the host
// calls Advance when it likes. A wake due after a restart is delivered once.
func Example() {
	ctx := context.Background()
	wakes := &document.Memory{} // the host's wake.Document; a table or Redis implements wake.Store
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	deliver := func(_ context.Context, w wake.Wake) error {
		fmt.Println("delivered to", w.Session+":", w.Prompt)
		return nil
	}

	scheduler := wake.New(wake.InDocument(wakes), clock, deliver)
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("schedule_wake", map[string]any{"delay": "10m", "prompt": "check the build"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("I'll check in 10 minutes."),
	})
	agent := engine.NewAgent(provider.StreamSimple, engine.WithInitialState(engine.AgentState{
		Model: provider.GetModel(), Tools: scheduler.Tools("conversation-1", "/srv/work"),
	}))
	if err := agent.Prompt(ctx, "Check the build in 10 minutes."); err != nil {
		fmt.Println(err)
		return
	}

	// The host restarts: a new scheduler on the same store, 10 minutes later.
	scheduler = wake.New(wake.InDocument(wakes), clock, deliver)
	now = now.Add(10 * time.Minute)
	for range 2 {
		next, _ := scheduler.Advance(ctx)
		fmt.Println("next:", next.IsZero())
	}
	// Output:
	// delivered to conversation-1: check the build
	// next: true
	// next: true
}
