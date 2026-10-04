package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
)

func TestPiLifecycleOnlyPublishesOrbPresentation(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls")
	binary := fakeHerdr(t, root, logPath)
	registry := extensions.NewRegistry(root)
	if err := registry.Register("builtin:herdr", Extension(binary, "w1:p1")); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("herdr-agent-state.ts", WithPiLifecycle(func(extensions.API) error { return nil })); err != nil {
		t.Fatal(err)
	}
	// Fresh must re-establish the owner before session_start too.
	registry, err := registry.Fresh(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: &interactiveTestUI{}})
	ctx := context.Background()
	runner.Emit(ctx, extensions.SessionStartEvent{})
	waitForLines(t, logPath, 1)
	runner.Emit(ctx, extensions.AgentStartEvent{})
	waitForLines(t, logPath, 2)
	runner.Emit(ctx, extensions.UIPromptStartEvent{})
	waitForLines(t, logPath, 3)
	runner.Emit(ctx, extensions.UIPromptEndEvent{})
	waitForLines(t, logPath, 4)
	runner.Emit(ctx, extensions.AgentSettledEvent{})
	waitForLines(t, logPath, 5)
	runner.Emit(ctx, extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
	lines := waitForLines(t, logPath, 6)
	if len(lines) != 6 {
		t.Fatalf("pi-owned lifecycle produced extra reports: %q", lines)
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if option(fields, "--source") != "custom:orb" || option(fields, "--agent") != "pi" || option(fields, "--applies-to-source") != "herdr:pi" {
			t.Fatalf("unguarded metadata: %q", line)
		}
		if len(fields) < 2 || fields[1] != "report-metadata" {
			t.Fatalf("second lifecycle reporter: %q", line)
		}
	}
	if option(strings.Fields(lines[0]), "--display-agent") != "Orb" || !slices.Contains(strings.Fields(lines[5]), "--clear-display-agent") {
		t.Fatalf("metadata lifecycle: %q", lines)
	}
	for _, mode := range []extensions.Mode{extensions.ModePrint, extensions.ModeJSON, extensions.ModeRPC, extensions.ModeTUI} {
		fresh, err := registry.Fresh(root)
		if err != nil {
			t.Fatal(err)
		}
		headless := extensions.NewRunner(fresh, extensions.RunnerOptions{Mode: mode})
		headless.Emit(ctx, extensions.SessionStartEvent{})
		headless.Emit(ctx, extensions.AgentStartEvent{})
		headless.Emit(ctx, extensions.AgentSettledEvent{})
		headless.Emit(ctx, extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
	}
	if got := len(waitForLines(t, logPath, 6)); got != 6 {
		t.Fatalf("headless session published %d extra calls", got-6)
	}
}

func TestPiReadinessRequiresCurrentSession(t *testing.T) {
	root := t.TempDir()
	reporter := &reporter{binaryPath: fakeHerdr(t, root, filepath.Join(root, "calls")), paneID: "w1:p1"}
	for _, test := range []struct {
		response string
		want     bool
	}{
		{`{"result":{"agent":{"agent":"pi","screen_detection_skipped":true,"agent_session":{"source":"herdr:pi","value":"current"}}}}`, true},
		{`{"result":{"agent":{"agent":"pi","screen_detection_skipped":true,"agent_session":{"source":"herdr:pi","value":"previous"}}}}`, false},
		{`{"result":{"agent":{"agent":"pi","screen_detection_skipped":false,"agent_session":{"source":"herdr:pi","value":"current"}}}}`, false},
		{`{"result":{"agent":{"agent":"orb","screen_detection_skipped":true,"agent_session":{"source":"herdr:pi","value":"current"}}}}`, false},
		{`{"result":{"agent":{"agent":"pi","screen_detection_skipped":true,"agent_session":{"source":"custom:other","value":"current"}}}}`, false},
		{`{"error":{"code":"agent_not_found"}}`, false},
	} {
		t.Setenv("HERDR_TEST_AGENT_RESPONSE", test.response)
		if got := reporter.piReady(context.Background(), "current"); got != test.want {
			t.Fatalf("readiness for %s = %v", test.response, got)
		}
	}
}

func TestFailedPiIntegrationDoesNotClaimLifecycle(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls")
	registry := extensions.NewRegistry(root)
	if err := registry.Register("builtin:herdr", Extension(fakeHerdr(t, root, logPath), "w1:p1")); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("broken", WithPiLifecycle(func(extensions.API) error { return errors.New("failed") })); err == nil {
		t.Fatal("expected failed registration")
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: &interactiveTestUI{}})
	runner.Emit(context.Background(), extensions.SessionStartEvent{})
	if line := waitForLines(t, logPath, 1)[0]; option(strings.Fields(line), "--agent") != "orb" {
		t.Fatalf("failed integration claimed lifecycle: %q", line)
	}
	runner.Emit(context.Background(), extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
}

func TestPiForegroundHintOnlyInsideAPaneWithTheManagedIntegration(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "herdr-agent-state.ts")
	plain := filepath.Join(root, "other.ts")
	if err := os.WriteFile(managed, []byte("// installed by herdr\n// HERDR_INTEGRATION_ID=pi\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, []byte("export default function () {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := PiForegroundHint([]string{plain, managed}); !slices.Equal(got, []string{"HERDR_AGENT=pi"}) {
		t.Fatalf("managed hint = %v", got)
	}
	if got := PiForegroundHint([]string{plain}); got != nil {
		t.Fatalf("plain hint = %v", got)
	}
	for _, test := range []struct {
		env, pane, socket string
		want              bool
	}{
		{"1", "w1:p1", "socket", true},
		{"", "w1:p1", "socket", false},
		{"1", "", "socket", false},
		{"1", "w1:p1", "", false},
	} {
		t.Setenv("HERDR_ENV", test.env)
		t.Setenv("HERDR_PANE_ID", test.pane)
		t.Setenv("HERDR_SOCKET_PATH", test.socket)
		if got := InPane(); got != test.want {
			t.Fatalf("InPane with %+v = %v", test, got)
		}
	}
}
