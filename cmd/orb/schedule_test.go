package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/internal/toolenv"
)

// orb chat holds the schedule: a due task fires in a new session that has the schedule tool, a
// run answering NO_REPLY is quiet, and what a scheduled run schedules is the agent's, never
// sooner than 5 min out.
func TestOrbChatFiresTheSchedule(t *testing.T) {
	root := t.TempDir()
	project, agentDir := filepath.Join(root, "workspace"), filepath.Join(root, "agent")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"plugins":{"schedule":{"zone":"Europe/Paris"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	task := func(id, title, prompt string) map[string]any {
		return map[string]any{"id": id, "title": title, "prompt": prompt, "when": "every 1h", "zone": "Europe/Paris", "target": "new",
			"cwd": project, "by": "person", "enabled": true, "created": due, "next": due}
	}
	seed, _ := json.Marshal(map[string]any{"version": 1, "tasks": []any{task("swatch", "watch", "Check the queue."), task("splan", "plan", "Plan a follow-up.")}})
	path := filepath.Join(agentDir, "schedule.json")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("HOME", root)
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv(toolenv.Allow, "")

	// Each run answers for its own task: watch has nothing to say; plan schedules too soon, then later.
	toolsSeen := make(chan []string, 8)
	provider := faux.New(faux.Options{API: "faux", Provider: "faux"})
	var steps []faux.ResponseStep
	for range 8 {
		steps = append(steps, faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			var names []string
			for _, tool := range *request.Tools {
				names = append(names, tool.Name)
			}
			toolsSeen <- names
			b, _ := json.Marshal(request.Messages)
			history, results := string(b), strings.Count(string(b), `"isError"`)
			switch {
			case strings.Contains(history, "[scheduled · watch · swatch]"):
				return faux.AssistantMessage("NO_REPLY"), nil
			case results == 0:
				return faux.AssistantMessage(faux.ToolCall("schedule", map[string]any{"action": "create", "title": "soon", "prompt": "Again.", "when": "in 1m"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}), nil
			case results == 1:
				return faux.AssistantMessage(faux.ToolCall("schedule", map[string]any{"action": "create", "title": "later", "prompt": "Again.", "when": "in 10m"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}), nil
			}
			return faux.AssistantMessage("Planned."), nil
		}))
	}
	provider.SetResponses(steps)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	agents := teamAgent(ctx, scriptedRuntime(provider), cliStreams{Stderr: io.Discard})
	if err := startSchedule(ctx, &agents, cliStreams{Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	type record struct {
		Tasks []struct{ ID, Title, By string }
		Runs  []struct{ Task, Status, Detail string }
	}
	var got record
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(path)
		got = record{}
		_ = json.Unmarshal(b, &got)
		ended := 0
		for _, r := range got.Runs {
			if r.Status != "running" {
				ended++
			}
		}
		if ended == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runs never ended: %s", b)
		}
	}
	status := map[string]string{}
	for _, r := range got.Runs {
		status[r.Task] = r.Status + " " + r.Detail
	}
	if status["swatch"] != "quiet " || status["splan"] != "done " {
		t.Fatalf("runs = %v", status)
	}
	by := map[string]string{}
	for _, k := range got.Tasks {
		by[k.Title] = k.By
	}
	if _, soon := by["soon"]; soon || by["later"] != "agent" {
		t.Fatalf("what the run scheduled = %v; want later by agent, nothing 1 min out", by)
	}
	if names := <-toolsSeen; !strings.Contains(strings.Join(names, " "), "schedule") {
		t.Fatalf("a scheduled run's session lacks the schedule tool: %v", names)
	}
}
