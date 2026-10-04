package agent

import (
	"context"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestV081SessionRuntimeInjectsAStreamWithoutReplacingAnAgentStream(t *testing.T) {
	t.Run("preserves constructor stream", func(t *testing.T) {
		cwd := t.TempDir()
		manager, settings := extensionRuntimeDependencies(t, cwd)
		called := false
		stream := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
			called = true
			return nil, nil
		}
		created := engine.NewAgent(stream)
		runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings})
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Dispose()
		if _, err := created.StreamFn()(context.Background(), nil, ai.Context{}, nil); err != nil {
			t.Fatal(err)
		}
		if !called {
			t.Fatal("runtime replaced the constructor stream")
		}
	})

	t.Run("fills an explicitly nil stream", func(t *testing.T) {
		cwd := t.TempDir()
		manager, settings := extensionRuntimeDependencies(t, cwd)
		created := engine.NewAgent(nil)
		runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings})
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Dispose()
		if created.StreamFn() == nil {
			t.Fatal("runtime left the agent without a stream")
		}
	})
}
