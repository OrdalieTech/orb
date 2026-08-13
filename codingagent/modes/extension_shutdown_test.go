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

// The built-in cwd band is --verbose only: the default panel is the empty mark,
// and verbose still overrides quiet startup.
func TestBuiltInHeaderIsVerboseOnly(t *testing.T) {
	for _, test := range []struct {
		name           string
		quiet, verbose bool
		wantHeader     bool
	}{
		{name: "default", quiet: false, verbose: false, wantHeader: false},
		{name: "quiet", quiet: true, verbose: false, wantHeader: false},
		{name: "verbose", quiet: false, verbose: true, wantHeader: true},
		{name: "quiet overridden by verbose", quiet: true, verbose: true, wantHeader: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mode, _, _, _ := newF12ShutdownMode(t)
			mode.session.SetQuietStartup(test.quiet)
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
