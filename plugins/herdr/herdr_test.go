package herdr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
)

type interactiveTestUI struct{ extensions.NoopUI }

// fakeHerdrEnv makes the test binary append its arguments to $HERDR_TEST_LOG,
// like the shell fake, and exit.
const fakeHerdrEnv = "ORB_HERDR_TEST_FAKE"

func TestMain(m *testing.M) {
	if os.Getenv(fakeHerdrEnv) != "" {
		log, err := os.OpenFile(os.Getenv("HERDR_TEST_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			_, err = log.WriteString(strings.Join(os.Args[1:], " ") + "\n")
			err = errors.Join(err, log.Close())
		}
		if err != nil {
			os.Exit(1)
		}
		if os.Getenv("HERDR_TEST_LEGACY") != "" && slices.Contains(os.Args, "--") {
			fmt.Fprintln(os.Stderr, "unexpected argument '--' found")
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestExtensionReportsInteractiveLifecycle(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls")
	binary := fakeHerdr(t, root, logPath)

	idle := true
	registry := extensions.NewRegistry(root)
	if err := registry.Register("builtin:herdr", Extension(binary, "w1:p1")); err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{
		Mode:           extensions.ModeTUI,
		UI:             &interactiveTestUI{},
		SessionManager: &testSession{id: "lifecycle", persisted: true},
		ContextActions: extensions.ContextActions{
			IsIdle: func() bool { return idle },
		},
	})
	ctx := context.Background()

	runner.Emit(ctx, extensions.SessionStartEvent{})
	waitForLines(t, logPath, 1)

	idle = false
	runner.Emit(ctx, extensions.AgentStartEvent{})
	waitForLines(t, logPath, 2)
	title := "Allow bash?"
	runner.Emit(ctx, extensions.UIPromptStartEvent{Title: &title})
	waitForLines(t, logPath, 3)
	runner.Emit(ctx, extensions.UIPromptEndEvent{})
	waitForLines(t, logPath, 4)
	idle = true
	runner.Emit(ctx, extensions.AgentSettledEvent{})
	waitForLines(t, logPath, 5)
	runner.Emit(ctx, extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
	lines := waitForLines(t, logPath, 6)

	states := []string{"idle", "working", "blocked", "working", "idle"}
	var previous uint64
	for index, state := range states {
		fields := strings.Fields(lines[index])
		if strings.Join(fields[:3], " ") != "pane report-agent w1:p1" || option(fields, "--source") != "custom:orb" || option(fields, "--agent") != "orb" || option(fields, "--state") != state {
			t.Fatalf("report %d = %q", index, lines[index])
		}
		sequence, err := strconv.ParseUint(option(fields, "--seq"), 10, 64)
		if err != nil || sequence <= previous {
			t.Fatalf("report %d sequence = %d, %v; previous = %d", index, sequence, err, previous)
		}
		previous = sequence
	}
	if !strings.Contains(lines[2], "--message Allow bash?") {
		t.Fatalf("blocked report = %q", lines[2])
	}
	if !strings.HasPrefix(lines[5], "pane release-agent w1:p1 --source custom:orb --agent orb") {
		t.Fatalf("release = %q", lines[5])
	}

	headlessRegistry := extensions.NewRegistry(root)
	if err := headlessRegistry.Register("builtin:herdr", Extension(binary, "w1:p1")); err != nil {
		t.Fatal(err)
	}
	headless := extensions.NewRunner(headlessRegistry, extensions.RunnerOptions{Mode: extensions.ModePrint})
	headless.Emit(ctx, extensions.SessionStartEvent{})
	time.Sleep(50 * time.Millisecond)
	if got := len(waitForLines(t, logPath, 6)); got != 6 {
		t.Fatalf("headless session changed call count to %d", got)
	}
}

func fakeHerdr(t *testing.T, root, logPath string) string {
	t.Helper()
	binary := filepath.Join(root, "herdr")
	if runtime.GOOS == "windows" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		binary = executable
		t.Setenv(fakeHerdrEnv, "1")
	} else if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HERDR_TEST_LOG\"\nif [ -n \"$HERDR_TEST_LEGACY\" ]; then for arg in \"$@\"; do if [ \"$arg\" = -- ]; then echo \"unexpected argument --\" >&2; exit 2; fi; done; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_TEST_LOG", logPath)
	return binary
}

func waitForLines(t *testing.T, path string, count int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		contents, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
			if len(lines) >= count {
				return lines
			}
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d Herdr calls", count)
	return nil
}

func option(fields []string, name string) string {
	for index := range len(fields) - 1 {
		if fields[index] == name {
			return fields[index+1]
		}
	}
	return ""
}

type testSession struct {
	extensions.ReadonlySessionManager
	id, file  string
	persisted bool
}

func (s *testSession) GetSessionID() string   { return s.id }
func (s *testSession) GetSessionFile() string { return s.file }
func (s *testSession) IsPersisted() bool      { return s.persisted }

func TestNativeResumeAndSessionReplacement(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls")
	registry := extensions.NewRegistry(root)
	if err := registry.Register("builtin:herdr", Extension(fakeHerdr(t, root, logPath), "pane", "--auto")); err != nil {
		t.Fatal(err)
	}
	manager := &testSession{id: "native-id", persisted: true}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: &interactiveTestUI{}, SessionManager: manager})
	runner.Emit(t.Context(), extensions.SessionStartEvent{})
	line := waitForLines(t, logPath, 1)[0]
	if !strings.Contains(line, "-- orb --auto --session native-id") {
		t.Fatalf("resume = %s", line)
	}
	manager.id = "second-id"
	runner.Emit(t.Context(), extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownNew})
	runner.Emit(t.Context(), extensions.SessionStartEvent{Reason: extensions.SessionStartNew})
	line = waitForLines(t, logPath, 2)[1]
	if !strings.Contains(line, "-- orb --auto --session second-id") || strings.Contains(line, "release-agent") {
		t.Fatalf("replacement = %s", line)
	}
	manager.file = filepath.Join(root, "session.jsonl")
	runner.Emit(t.Context(), extensions.SessionStartEvent{Reason: extensions.SessionStartResume})
	line = waitForLines(t, logPath, 3)[2]
	if !strings.Contains(line, "-- orb --pi-files --auto --session "+manager.file) {
		t.Fatalf("file resume = %s", line)
	}
	runner.Emit(t.Context(), extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
}

func TestEphemeralSessionHasNoResume(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls")
	registry := extensions.NewRegistry(root)
	if err := registry.Register("builtin:herdr", Extension(fakeHerdr(t, root, logPath), "pane")); err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: &interactiveTestUI{}, SessionManager: &testSession{id: "ephemeral"}})
	runner.Emit(t.Context(), extensions.SessionStartEvent{})
	lines := waitForLines(t, logPath, 2)
	if !strings.Contains(lines[0], "release-agent") || !strings.Contains(lines[1], "report-agent") || strings.Contains(lines[1], "--session") {
		t.Fatalf("ephemeral resume = %v", lines)
	}
	runner.Emit(t.Context(), extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
}

func TestOldHerdrFallsBackToOrbStateOnly(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls")
	t.Setenv("HERDR_TEST_LEGACY", "1")
	registry := extensions.NewRegistry(root)
	if err := registry.Register("builtin:herdr", Extension(fakeHerdr(t, root, logPath), "pane")); err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: &interactiveTestUI{}, SessionManager: &testSession{id: "native", persisted: true}})
	runner.Emit(t.Context(), extensions.SessionStartEvent{})
	lines := waitForLines(t, logPath, 2)
	if !strings.Contains(lines[0], "-- orb --session native") || strings.Contains(lines[1], "-- orb") || !strings.Contains(lines[1], "--agent orb") {
		t.Fatalf("fallback = %v", lines)
	}
	runner.Emit(t.Context(), extensions.AgentStartEvent{})
	lines = waitForLines(t, logPath, 3)
	if strings.Contains(lines[2], "-- orb") {
		t.Fatalf("old CLI was reprobed: %v", lines)
	}
	runner.Emit(t.Context(), extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
}

func TestResumeArgumentsRespectHerdrValidation(t *testing.T) {
	for _, value := range []string{"apostrophe'", "newline\n", "tab\t", "control\x7f", strings.Repeat("x", 8193)} {
		if validResume([]string{"orb", "--session", value}) {
			t.Fatalf("accepted %q", value)
		}
	}
	if !validResume([]string{"orb", "--pi-files", "--session", "/directory with spaces/file.jsonl"}) {
		t.Fatal("space in argument rejected")
	}
}

func TestHeadlessNeverReportsOrReleases(t *testing.T) {
	for _, mode := range []extensions.Mode{extensions.ModePrint, extensions.ModeJSON, extensions.ModeRPC, extensions.ModeTUI} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			logPath := filepath.Join(root, "calls")
			registry := extensions.NewRegistry(root)
			if err := registry.Register("builtin:herdr", Extension(fakeHerdr(t, root, logPath), "pane")); err != nil {
				t.Fatal(err)
			}
			runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: mode, SessionManager: &testSession{id: "native", persisted: true}})
			runner.Emit(t.Context(), extensions.SessionStartEvent{})
			runner.Emit(t.Context(), extensions.AgentStartEvent{})
			runner.Emit(t.Context(), extensions.AgentSettledEvent{})
			runner.Emit(t.Context(), extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
			time.Sleep(20 * time.Millisecond)
			if _, err := os.Stat(logPath); !os.IsNotExist(err) {
				t.Fatalf("headless reported: %v", err)
			}
		})
	}
}

func TestReportsKeepOnlyLatestPendingState(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls")
	reporter := &reporter{binaryPath: fakeHerdr(t, root, logPath), paneID: "pane"}
	reporter.active.Store(true)
	reporter.claimed.Store(true)
	runner := extensions.NewRunner(extensions.NewRegistry(root), extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: &interactiveTestUI{}})
	reporter.command.Lock()
	for range 100 {
		reporter.report(runner.CreateContext(), "working")
	}
	reporter.report(runner.CreateContext(), "idle")
	reporter.command.Unlock()
	lines := waitForLines(t, logPath, 1)
	if len(lines) != 1 || !strings.Contains(lines[0], "--state idle") {
		t.Fatalf("pending reports were not coalesced: %v", lines)
	}
	reporter.release()
	lines = waitForLines(t, logPath, 2)
	if len(lines) != 2 || !strings.Contains(lines[1], "release-agent") {
		t.Fatalf("reports escaped after release: %v", lines)
	}
}
