package questions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai/providers/faux"
)

func TestNativeQuestionToolUsesRuntimeReplies(t *testing.T) {
	dir := t.TempDir()
	registry := extensions.NewRegistry(dir)
	if err := registry.Register("questions", Extension(Draw{})); err != nil {
		t.Fatal(err)
	}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall(ToolName, map[string]any{"questions": []any{map[string]any{"id": "choice", "question": "Choose", "options": []any{map[string]any{"label": "One"}, map[string]any{"label": "Two"}}}}}, faux.ToolCallOptions{ID: "q-1"})),
		faux.AssistantMessage("Continued after your answer"),
	})
	host, err := agent.NewAgentSessionRuntime(t.Context(), agent.AgentSessionOptions{CWD: dir, AgentDir: dir, Model: provider.GetModel(), StreamFn: provider.StreamSimple, ExtensionRegistry: registry, Resources: &agent.Resources{}, Tools: []string{ToolName}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose(context.Background())
	if _, err = host.EnableControl(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Session().Prompt(t.Context(), "Ask me") }()
	deadline := time.Now().Add(3 * time.Second)
	var pending *agent.InputRequest
	for time.Now().Before(deadline) {
		pending = host.Session().PendingInput()
		if pending != nil {
			break
		}
		time.Sleep(time.Millisecond * 5)
	}
	if pending == nil || pending.Presentation == nil || pending.Presentation.Kind != Kind {
		t.Fatal("native tool did not publish a shared question")
	}
	pending.Presentation.Data[0] = '!'
	if !json.Valid(host.Session().PendingInput().Presentation.Data) {
		t.Fatal("snapshot aliases pending input")
	}
	if err = host.Session().ReplyInput(pending.ID, `{"answers":[{"id":"choice","selected":["Invented"]}]}`); err == nil {
		t.Fatal("invalid reply accepted")
	}
	if err = host.Session().ReplyInput(pending.ID, `{"answers":[{"id":"choice","selected":["Two"]}]}`); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("answer did not resume the native loop")
	}
	if err = host.Session().ReplyInput(pending.ID, `{}`); err == nil {
		t.Fatal("stale reply accepted")
	}
	raw, _ := json.Marshal(host.Session().State().Messages)
	if !strings.Contains(string(raw), `\"selected\":[\"Two\"]`) {
		t.Fatalf("answer missing from transcript: %s", raw)
	}
}
