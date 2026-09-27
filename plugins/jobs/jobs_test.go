//go:build !windows

package jobs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sessionstore "github.com/OrdalieTech/orb/agent/session"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

type harness struct {
	bash, stop engine.AgentTool
	shutdown   func()
	mu         sync.Mutex
	messages   []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{}
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<inline:jobs>", Extension(nil)); err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	options := extensions.RunnerOptions{SessionManager: manager}
	options.Actions.SendMessage = func(_ context.Context, message extensions.CustomMessage, _ *extensions.SendMessageOptions) error {
		h.mu.Lock()
		h.messages = append(h.messages, message.Content.(string))
		h.mu.Unlock()
		return nil
	}
	runner := extensions.NewRunner(registry, options)
	for _, registered := range runner.AllRegisteredTools() {
		switch registered.Definition.Name {
		case "bash":
			h.bash = extensions.WrapRegisteredTool(registered, runner)
		case "stop_job":
			h.stop = extensions.WrapRegisteredTool(registered, runner)
		}
	}
	h.shutdown = func() {
		extensions.EmitSessionShutdown(context.Background(), runner, extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownQuit})
	}
	t.Cleanup(h.shutdown)
	return h
}

func (h *harness) run(t *testing.T, tool engine.AgentTool, args map[string]any) string {
	t.Helper()
	text, err := call(tool, args)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func call(tool engine.AgentTool, args map[string]any) (string, error) {
	result, err := tool.Execute(context.Background(), "call", args, nil)
	if err != nil {
		return "", err
	}
	return ai.ContentText(result.Content), nil
}

func (h *harness) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.messages...)
}

// wait returns every message once one contains want.
func (h *harness) wait(t *testing.T, want string) string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		h.mu.Lock()
		all := strings.Join(h.messages, "\n---\n")
		h.mu.Unlock()
		if strings.Contains(all, want) {
			return all
		}
	}
	t.Fatalf("no message with %q in %q", want, h.snapshot())
	return ""
}

// A background job returns at once and reports its end with its last lines;
// a plain command still runs in the foreground.
func TestBackgroundJobReportsItsEnd(t *testing.T) {
	h := newHarness(t)
	if got := strings.TrimSpace(h.run(t, h.bash, map[string]any{"command": "echo foreground"})); got != "foreground" {
		t.Fatalf("foreground = %q", got)
	}
	start := time.Now()
	started := h.run(t, h.bash, map[string]any{"command": "echo begin; sleep 1; echo finished; exit 3", "run_in_background": true})
	if time.Since(start) > 500*time.Millisecond || !strings.Contains(started, "Started background job 1") {
		t.Fatalf("start took %v: %q", time.Since(start), started)
	}
	if got := h.wait(t, "exited with code 3"); !strings.Contains(got, "finished") {
		t.Fatalf("end message = %q", got)
	}
}

// A monitored job reports the lines it prints before its end.
func TestMonitorReportsLines(t *testing.T) {
	h := newHarness(t)
	h.run(t, h.bash, map[string]any{"command": "for i in 1 2 3; do echo line$i; sleep 0.4; done", "monitor": true})
	got := h.wait(t, "exited with code 0")
	if !strings.Contains(got, "printed:\nline1") || !strings.Contains(got, "line3") || strings.Index(got, "line3") > strings.Index(got, "exited") {
		t.Fatalf("messages = %q", got)
	}
}

// stop_job ends the job's whole process group, and no end is reported after.
func TestStopJobEndsTheProcessGroup(t *testing.T) {
	h := newHarness(t)
	started := h.run(t, h.bash, map[string]any{"command": "sleep 30 & sleep 30", "run_in_background": true})
	if got := h.run(t, h.stop, map[string]any{"job": "1"}); !strings.Contains(got, "Stopped background job 1") {
		t.Fatalf("stop = %q", got)
	}
	var group int
	if _, err := fmt.Sscanf(started[strings.Index(started, "pid "):], "pid %d", &group); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(-group, 0) == nil; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the job's process group survived stop_job")
		}
	}
	time.Sleep(1500 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.messages) != 0 {
		t.Fatalf("a stopped job reported: %q", h.messages)
	}
}
