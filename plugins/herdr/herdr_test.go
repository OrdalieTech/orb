package herdr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestExtensionReportsInteractiveLifecycle(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls")
	binary := filepath.Join(root, "herdr")
	if runtime.GOOS == "windows" {
		// No POSIX shell for a script fake: the test binary stands in (TestMain).
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		binary = executable
		t.Setenv(fakeHerdrEnv, "1")
	} else if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HERDR_TEST_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERDR_TEST_LOG", logPath)

	idle := true
	registry := extensions.NewRegistry(root)
	if err := registry.Register("<inline:herdr>", Extension(binary, "w1:p1")); err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{
		Mode: extensions.ModeTUI,
		UI:   &interactiveTestUI{},
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
	if err := headlessRegistry.Register("<inline:herdr>", Extension(binary, "w1:p1")); err != nil {
		t.Fatal(err)
	}
	headless := extensions.NewRunner(headlessRegistry, extensions.RunnerOptions{Mode: extensions.ModePrint})
	headless.Emit(ctx, extensions.SessionStartEvent{})
	time.Sleep(50 * time.Millisecond)
	if got := len(waitForLines(t, logPath, 6)); got != 6 {
		t.Fatalf("headless session changed call count to %d", got)
	}
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
