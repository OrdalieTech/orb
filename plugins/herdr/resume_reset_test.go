package herdr

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
)

func TestNewReporterClearsInheritedResume(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls")
	binary := fakeHerdr(t, root, logPath)
	start := func(manager *testSession) *extensions.Runner {
		registry := extensions.NewRegistry(root)
		if err := registry.Register("builtin:herdr", Extension(binary, "pane")); err != nil {
			t.Fatal(err)
		}
		runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: &interactiveTestUI{}, SessionManager: manager})
		runner.Emit(t.Context(), extensions.SessionStartEvent{})
		return runner
	}
	previous := start(&testSession{id: "persisted", persisted: true})
	waitForLines(t, logPath, 1)
	previous.Emit(t.Context(), extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownReload})
	current := start(&testSession{id: "ephemeral"})
	lines := waitForLines(t, logPath, 3)
	if !strings.Contains(lines[1], "release-agent") || !strings.Contains(lines[2], "report-agent") || strings.Contains(lines[2], "-- orb") {
		t.Fatalf("previous reporter's restore was retained: %v", lines)
	}
	current.Emit(t.Context(), extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
}

func TestNonResumableReplacementClearsPreviousResume(t *testing.T) {
	for _, invalidPath := range []bool{false, true} {
		t.Run(strconv.FormatBool(invalidPath), func(t *testing.T) {
			root := t.TempDir()
			logPath := filepath.Join(root, "calls")
			registry := extensions.NewRegistry(root)
			if err := registry.Register("builtin:herdr", Extension(fakeHerdr(t, root, logPath), "pane")); err != nil {
				t.Fatal(err)
			}
			manager := &testSession{id: "persisted", persisted: true}
			runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI, UI: &interactiveTestUI{}, SessionManager: manager})
			runner.Emit(t.Context(), extensions.SessionStartEvent{})
			waitForLines(t, logPath, 1)
			if invalidPath {
				manager.file = filepath.Join(root, "can't-resume.jsonl")
			} else {
				manager.persisted = false
			}
			runner.Emit(t.Context(), extensions.SessionStartEvent{})
			lines := waitForLines(t, logPath, 3)
			if !strings.Contains(lines[1], "release-agent") || !strings.Contains(lines[2], "report-agent") || strings.Contains(lines[2], "-- orb") {
				t.Fatalf("stale resume not cleared: %v", lines)
			}
			var previous uint64
			for _, line := range lines {
				seq, err := strconv.ParseUint(option(strings.Fields(line), "--seq"), 10, 64)
				if err != nil || seq <= previous {
					t.Fatalf("out of order: %v", lines)
				}
				previous = seq
			}
			runner.Emit(t.Context(), extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
		})
	}
}
