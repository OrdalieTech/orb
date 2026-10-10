package bridgeagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
)

// fakeBridge is this machine (p0), where this Orb runs as i0, and a connected
// device named lab (p1).
type fakeBridge struct {
	mu        sync.Mutex
	instances map[string][]string // peer → available instances
	calls     []map[string]any    // instances.call payloads
	open      int                 // snapshots not given back
}

var descriptors = map[string]string{
	"i0": `{"name":"me","cwd":"/work","target":{"session_id":"s0"}}`,
	"i1": `{"name":"Docs","cwd":"/work/docs","registration_generation":"g1","target":{"session_id":"s1","session_revision":"r1"}}`,
	"i2": `{"name":"Build","cwd":"/srv/app","registration_generation":"g2","target":{"session_id":"s2","session_revision":"r2","execution_id":"e2"}}`,
}

func (b *fakeBridge) Machines(context.Context) ([]string, error) { return []string{"p0", "p1"}, nil }
func (b *fakeBridge) Self() string                               { return "i0" }

func (b *fakeBridge) Call(_ context.Context, peer, method string, params, result any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	encoded, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(encoded, &p)
	var reply string
	switch method {
	case "instances.list":
		items := []map[string]any{}
		for _, id := range b.instances[peer] {
			items = append(items, map[string]any{"instance_id": id, "available": true})
		}
		data, _ := json.Marshal(map[string]any{"items": items})
		reply = string(data)
	case "instances.describe":
		reply = descriptors[p["instance_id"].(string)]
	case "bridge.ping":
		reply = `{"name":"lab"}`
	case "events.subscribe":
		b.open++
		reply = `{"snapshot_id":"x","messages":[` +
			`{"role":"user","content":"Fix the docs build","timestamp":1},` +
			`{"role":"assistant","content":[{"type":"text","text":"Running it."},{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"make docs"}}],"api":"faux","provider":"faux","model":"x","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"toolUse","timestamp":2},` +
			`{"role":"toolResult","toolCallId":"c1","toolName":"bash","content":[{"type":"text","text":"huge build log"}],"isError":false,"timestamp":3}]}`
	case "events.unsubscribe":
		b.open--
		return nil
	case "instances.call":
		b.calls = append(b.calls, p)
		return nil
	default:
		return errors.New("unexpected " + method)
	}
	return json.Unmarshal([]byte(reply), result)
}

// An agent sees the agents tool only while another conversation is reachable,
// lists them without itself, reads one's latest messages without tool output,
// and messages them: a prompt to an idle one, a follow-up to a working one,
// each fenced by the revision it described.
func TestAgentsListReadAndMessageOtherConversations(t *testing.T) {
	alone := &fakeBridge{instances: map[string][]string{"p0": {"i0"}}}
	offered := func(request ai.Context) bool {
		return request.Tools != nil && slices.ContainsFunc(*request.Tools, func(tool ai.Tool) bool { return tool.Name == name })
	}
	var shownAlone bool
	run(t, alone, faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		shownAlone = offered(request)
		return faux.AssistantMessage("nobody else"), nil
	}))
	if shownAlone {
		t.Fatal("agents tool offered with no other conversation reachable")
	}

	others := &fakeBridge{instances: map[string][]string{"p0": {"i0", "i1"}, "p1": {"i2"}}}
	script := []map[string]any{
		{"action": "list"},
		{"action": "read", "id": "i1"},
		{"action": "send", "id": "i1", "text": "Docs build is green?"},
		{"action": "send", "id": "i2", "text": "Stop after the build."},
	}
	var shown bool
	var results []string
	steps := make([]faux.ResponseStep, 0, len(script)+1)
	for step := range len(script) + 1 {
		steps = append(steps, faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			shown = shown || offered(request)
			if last, ok := request.Messages[len(request.Messages)-1].(*ai.ToolResultMessage); ok {
				results = append(results, ai.ContentText(last.Content))
			}
			if step == len(script) {
				return faux.AssistantMessage("done"), nil
			}
			return faux.AssistantMessage(faux.ToolCall(name, script[step], faux.ToolCallOptions{ID: fmt.Sprint("call-", step)})), nil
		}))
	}
	run(t, others, steps...)

	if !shown {
		t.Fatal("agents tool not offered with other conversations reachable")
	}
	if len(results) != len(script) {
		t.Fatalf("results = %q", results)
	}
	if want := "i1 · this machine · /work/docs · Docs · idle\ni2 · lab · /srv/app · Build · working"; results[0] != want {
		t.Fatalf("list = %q, want %q", results[0], want)
	}
	if read := results[1]; !strings.Contains(read, "State: idle") || !strings.Contains(read, "user: Fix the docs build") ||
		!strings.Contains(read, "assistant: Running it.") || strings.Contains(read, "huge build log") {
		t.Fatalf("read = %q", read)
	}
	if results[2] != "Sent." || !strings.HasPrefix(results[3], "Queued") || others.open != 0 {
		t.Fatalf("send results = %q, %q; snapshots left open %d", results[2], results[3], others.open)
	}
	if len(others.calls) != 2 {
		t.Fatalf("calls = %v", others.calls)
	}
	idle, busy := others.calls[0], others.calls[1]
	if idle["method"] != "prompt" || idle["session_id"] != "s1" || idle["expected"].(map[string]any)["session_revision"] != "r1" ||
		busy["method"] != "follow_up" || busy["args"].(map[string]any)["execution_id"] != "e2" || busy["expected"].(map[string]any)["registration_generation"] != "g2" {
		t.Fatalf("calls = %v", others.calls)
	}
}

func run(t *testing.T, b Bridge, steps ...faux.ResponseStep) {
	t.Helper()
	root := t.TempDir()
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses(steps)
	registry := extensions.NewRegistry(root)
	if err := registry.Register("builtin:bridge-agent-calls", Extension(b)); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(root+"/agent"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root)
	if err != nil {
		t.Fatal(err)
	}
	prompt := "parent"
	result, err := agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: root, AgentDir: root + "/agent", Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &agent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()
	if err := result.Session.PromptSync(context.Background(), "check on the others"); err != nil {
		t.Fatal(err)
	}
}
