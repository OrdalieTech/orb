package modes

import (
	"testing"

	"github.com/OrdalieTech/orb/tui"
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

func TestVerboseOverridesQuietStartupHeader(t *testing.T) {
	for _, test := range []struct {
		name       string
		verbose    bool
		wantHeader bool
	}{{"quiet", false, false}, {"verbose", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			mode, _, _, _ := newF12ShutdownMode(t)
			mode.session.SetQuietStartup(true)
			mode.header = &tui.Container{}
			mode.options.Verbose = test.verbose
			mode.options.SessionHeader = mode.session.Manager().GetHeader()
			mode.addDefaultHeader()
			if got := len(mode.header.Children()) > 0; got != test.wantHeader {
				t.Fatalf("header rendered = %t, want %t (interactive-mode.ts:732)", got, test.wantHeader)
			}
		})
	}
}
