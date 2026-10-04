package modes

import (
	"testing"
)

func TestExtensionShutdownQuitsInteractiveWhenIdle(t *testing.T) {
	mode, _, _, _ := newF12ShutdownMode(t)
	mode.requestExtensionShutdown()
	mode.mu.Lock()
	requested := mode.shutdownRequested
	mode.mu.Unlock()
	if !requested {
		t.Fatal("extension shutdown on an idle session must quit immediately (interactive-mode.ts:1689-1694)")
	}
}

func TestExtensionShutdownDeferredUntilAgentSettled(t *testing.T) {
	mode, _, _, _ := newF12ShutdownMode(t)

	mode.checkExtensionShutdownRequested()
	mode.mu.Lock()
	requested := mode.shutdownRequested
	mode.mu.Unlock()
	if requested {
		t.Fatal("checkExtensionShutdownRequested shut down without a pending request")
	}

	mode.mu.Lock()
	mode.extensionShutdownRequested = true
	mode.mu.Unlock()
	mode.checkExtensionShutdownRequested()
	mode.mu.Lock()
	requested = mode.shutdownRequested
	mode.mu.Unlock()
	if !requested {
		t.Fatal("pending extension shutdown must complete on agent_settled (interactive-mode.ts:3626-3631)")
	}
}
