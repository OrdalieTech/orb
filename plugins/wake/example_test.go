package wake_test

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/plugins/wake"
)

// An SDK host wires wake without orb chat: its own document, clock and
// delivery, and Advance called when it likes. A wake due after a restart is
// delivered once.
func Example() {
	ctx := context.Background()
	dir, _ := os.MkdirTemp("", "wake")
	defer func() { _ = os.RemoveAll(dir) }()
	store := &document.Memory{} // the host's Store document
	now := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	deliver := func(_ context.Context, w wake.Wake) error {
		fmt.Println("delivered:", w.Prompt)
		return nil
	}

	scheduler := wake.New(store, clock, deliver)
	loader, _ := agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{
		CWD: dir, AgentDir: dir, NoContextFiles: true, ExtensionFactories: []extensions.Factory{scheduler.Extension()},
	})
	_ = loader.Reload(ctx, nil)
	provider := faux.New()
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("schedule_wake", map[string]any{"delay": "10m", "prompt": "check the build"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}),
		faux.AssistantMessage("I'll check in 10 minutes."),
	})
	manager, _ := sessionstore.InMemory(dir)
	result, err := agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: dir, AgentDir: dir, StreamFn: provider.StreamSimple, Model: provider.GetModel(), ResourceLoader: loader, SessionManager: manager,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	_ = result.Session.Prompt(ctx, "Check the build in 10 minutes.")
	result.Session.Dispose()

	// The host restarts: a new scheduler on the same document, 10 minutes later.
	scheduler = wake.New(store, clock, deliver)
	now = now.Add(10 * time.Minute)
	for range 2 {
		next, _ := scheduler.Advance(ctx)
		fmt.Println("next:", next.IsZero())
	}
	// Output:
	// delivered: check the build
	// next: true
	// next: true
}
