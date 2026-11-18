// Package mitosisagents lets an agent see and talk to its owner's other Orb
// conversations, on this machine and on connected devices, through Mitosis. Its
// tool exists for the model only while one of them is reachable.
package mitosisagents

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	"github.com/OrdalieTech/orb/plugins/internal/toolutil"
)

// Mitosis is how the plugin reaches Mitosis, with its owner's reach.
type Mitosis interface {
	// Machines are the peer IDs of this machine's agent, first, and of the connected devices'.
	Machines(ctx context.Context) ([]string, error)
	// Call runs a peer method as the owner does.
	Call(ctx context.Context, peer, method string, params, result any) error
	// Self is the conversation this Orb holds on Mitosis, empty when it is not attached.
	Self() string
}

const name = "agents"

var schema = ai.JSONSchema(`{"type":"object","required":["action"],"properties":{"action":{"type":"string","enum":["list","read","send"]},"agent":{"type":"string","description":"An agent's id from list, or an unambiguous start of it."},"conversation":{"type":"string","description":"A conversation's id from list. Without one, the agent's default conversation."},"text":{"type":"string","description":"The message to send."}}}`)

// Extension registers the agents tool, active only while another conversation
// is reachable: no Mitosis, or nobody else on it, costs the model nothing.
func Extension(b Mitosis) extensions.Factory {
	return func(api extensions.API) error {
		t := &tool{mitosis: b}
		api.RegisterTool(extensions.ToolDefinition{
			Name: name, Label: "Agents", Parameters: schema,
			Description: "Your Orb agents, this machine's and those of connected devices, and their conversations. list gives each agent's id and name, and the conversations open in it with their id, folder, title and state; read shows a conversation's latest messages; send gives it a message, queued while it works. Without a conversation, read and send go to the agent's default one, its latest.",
			Execute: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
				var input struct{ Action, Agent, Conversation, Text string }
				if err := toolutil.Decode(raw, &input); err != nil {
					return engine.AgentToolResult{}, err
				}
				text, err := t.run(ctx, input.Action, input.Agent, input.Conversation, input.Text)
				if err != nil {
					return engine.AgentToolResult{}, err
				}
				return toolutil.TextResult(text), nil
			},
		})
		// The tool shows only while someone is reachable; one the host left out
		// (--tools) stays out.
		hidden := false
		refresh := func(ctx context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			active, err := api.GetActiveTools()
			if err != nil {
				return nil, nil //nolint:nilerr // ponytail: no tool list, nothing to show or hide.
			}
			shown := slices.Contains(active, name)
			if !shown && !hidden {
				return nil, nil
			}
			if reachable := t.reachable(ctx); reachable != shown {
				if reachable {
					active = append(active, name)
				} else {
					active = slices.DeleteFunc(active, func(tool string) bool { return tool == name })
				}
				hidden = !reachable
				_ = api.SetActiveTools(active)
			}
			return nil, nil
		}
		api.On(extensions.EventSessionStart, refresh)
		api.On(extensions.EventBeforeAgentStart, refresh)
		return nil
	}
}

type tool struct {
	mitosis Mitosis
	mu      sync.Mutex
	home    string // this machine's agent and its name, from the last list
	machine string
	seen    time.Time // when reachable last looked
	others  bool
}

// orbAgent is one Orb agent as list shows it: a peer, and the conversations open in it.
type orbAgent struct {
	peer, name    string
	conversations []conversation
}

// conversation is one agent.describe lists.
type conversation struct {
	ID    string `json:"session_id"`
	Title string `json:"title"`
	CWD   string `json:"cwd"`
	State string `json:"state"`
}

func (c conversation) state() string {
	return map[string]string{"asking": "waiting for an answer", "running": "working"}[c.State]
}

func (t *tool) run(ctx context.Context, action, agentID, session, text string) (string, error) {
	switch action {
	case "list":
		found, err := t.agents(ctx)
		if err != nil {
			return "", err
		}
		if len(found) == 0 {
			return "No other agent is reachable.", nil
		}
		var lines []string
		for _, a := range found {
			lines = append(lines, "Agent "+a.peer+" · "+a.name)
			for _, c := range a.conversations {
				fields := []string{c.ID, c.CWD, c.Title, cmp.Or(c.state(), "idle")}
				lines = append(lines, "  "+strings.Join(slices.DeleteFunc(fields, func(field string) bool { return field == "" }), " · "))
			}
			if len(a.conversations) == 0 {
				lines = append(lines, "  no conversation open: send reaches its default one")
			}
		}
		return strings.Join(lines, "\n"), nil
	case "read", "send":
		peer, err := t.locate(ctx, agentID, session)
		if err != nil {
			return "", err
		}
		if action == "read" {
			return t.read(ctx, peer, session)
		}
		if strings.TrimSpace(text) == "" {
			return "", errors.New("agents: text is required")
		}
		return t.send(ctx, peer, session, text)
	}
	return "", errors.New("agents: action must be list, read or send")
}

// agents are the reachable agents, this machine's first, each with its conversations but this one.
func (t *tool) agents(ctx context.Context) ([]orbAgent, error) {
	machines, err := t.mitosis.Machines(ctx)
	if err != nil {
		return nil, err
	}
	self := t.mitosis.Self()
	// Each device is described at once, so one out of reach (a peer just gone still shows
	// connected until its channel times out) delays nothing and only lists nothing.
	listed := make([]*orbAgent, len(machines))
	var wg sync.WaitGroup
	for i, peer := range machines {
		wg.Go(func() {
			a := orbAgent{peer: peer}
			cursor := ""
			for range 32 {
				var page struct {
					Name   string
					Items  []conversation
					Cursor string
				}
				if err := t.mitosis.Call(ctx, peer, "agent.describe", map[string]string{"cursor": cursor}, &page); err != nil {
					return
				}
				a.name = cmp.Or(page.Name, a.name)
				a.conversations = append(a.conversations, slices.DeleteFunc(page.Items, func(c conversation) bool { return c.ID == self })...)
				if cursor = page.Cursor; cursor == "" {
					break
				}
			}
			listed[i] = &a
		})
	}
	wg.Wait()
	var found []orbAgent
	for i, a := range listed {
		if a == nil {
			continue
		}
		if i == 0 {
			a.name = cmp.Or(a.name, "this machine") + " (this machine)"
			t.mu.Lock()
			t.home, t.machine = a.peer, strings.TrimSuffix(a.name, " (this machine)")
			t.mu.Unlock()
		} else {
			a.name = cmp.Or(a.name, "a connected device")
		}
		found = append(found, *a)
	}
	return found, nil
}

// locate is the agent an id or start of one names, or the one where a conversation from list is.
func (t *tool) locate(ctx context.Context, id, session string) (string, error) {
	if id == "" {
		if session == "" {
			return "", errors.New("agents: agent or conversation is required")
		}
		found, err := t.agents(ctx)
		if err != nil {
			return "", err
		}
		for _, a := range found {
			if slices.ContainsFunc(a.conversations, func(c conversation) bool { return c.ID == session }) {
				return a.peer, nil
			}
		}
		return "", fmt.Errorf("agents: no conversation %q is reachable; list them again", session)
	}
	machines, err := t.mitosis.Machines(ctx)
	if err != nil {
		return "", err
	}
	matches := slices.DeleteFunc(machines, func(peer string) bool {
		return !strings.HasPrefix(peer, id) && !strings.HasPrefix(peer[strings.LastIndex(peer, ":")+1:], id)
	})
	if len(matches) != 1 {
		return "", fmt.Errorf("agents: %d reachable agents match %q; list them again", len(matches), id)
	}
	return matches[0], nil
}

// descriptor is what the tool reads of a conversation (agent.call inspect) and the session it
// answers for: the agent's default one when none was named.
type descriptor struct {
	SessionID  string `json:"session_id"`
	Name, CWD  string
	Generation string `json:"registration_generation"`
	Target     struct {
		Session   string `json:"session_id"`
		Revision  string `json:"session_revision"`
		Execution string `json:"execution_id"`
	}
	Input any `json:"input"`
}

func (d descriptor) state() string {
	switch {
	case d.Input != nil:
		return "waiting for an answer"
	case d.Target.Execution != "":
		return "working"
	}
	return "idle"
}

func (t *tool) inspect(ctx context.Context, peer, session string) (descriptor, error) {
	var d descriptor
	err := t.mitosis.Call(ctx, peer, "agent.call", map[string]any{"session_id": session, "method": "inspect", "args": map[string]any{}}, &d)
	if err == nil && d.SessionID == "" {
		err = errors.New("agents: that conversation is not ready for messages")
	}
	return d, err
}

// read looks at a conversation's last readTail messages and shows the last
// readShown of them with text: tool calls and output are left out.
const readTail, readShown = 30, 6

func (t *tool) read(ctx context.Context, peer, session string) (string, error) {
	d, err := t.inspect(ctx, peer, session)
	if err != nil {
		return "", err
	}
	var snapshot struct {
		ID       string            `json:"snapshot_id"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := t.mitosis.Call(ctx, peer, "events.subscribe", map[string]any{"session_id": d.SessionID, "tail": readTail}, &snapshot); err != nil {
		return "", err
	}
	_ = t.mitosis.Call(ctx, peer, "events.unsubscribe", map[string]string{"session_id": d.SessionID, "snapshot_id": snapshot.ID}, nil)
	var lines []string
	for _, raw := range snapshot.Messages {
		if line := messageLine(raw); line != "" {
			lines = append(lines, line)
		}
	}
	lines = lines[max(0, len(lines)-readShown):]
	return strings.Join(append([]string{"Conversation " + d.SessionID + " · state: " + d.state()}, lines...), "\n\n"), nil
}

// messageLine is a message as read shows it: who said it and its text.
func messageLine(raw json.RawMessage) string {
	message, err := ai.UnmarshalMessage(raw)
	if err != nil {
		return ""
	}
	var role, text string
	switch m := message.(type) {
	case *ai.UserMessage:
		role = "user"
		if m.Content.Text != nil {
			text = *m.Content.Text
		} else {
			text = ai.ContentText(m.Content.Blocks)
		}
	case *ai.AssistantMessage:
		role = "assistant"
		var parts []string
		for _, block := range m.Content {
			if b, ok := block.(*ai.TextContent); ok {
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	default:
		return ""
	}
	if text = strings.TrimSpace(text); text == "" {
		return ""
	}
	if len(text) > 1200 {
		text = strings.ToValidUTF8(text[:1200], "") + " …"
	}
	return role + ": " + text
}

func (t *tool) send(ctx context.Context, peer, session, text string) (string, error) {
	d, err := t.inspect(ctx, peer, session)
	if err != nil {
		return "", err
	}
	if self := t.mitosis.Self(); self != "" && d.SessionID == self {
		return "", errors.New("agents: that is this conversation")
	}
	method, args, done := "prompt", map[string]any{"text": t.sender(ctx) + "\n\n" + text}, "Sent to conversation "+d.SessionID+"."
	if d.Target.Execution != "" {
		method, args["execution_id"], done = "follow_up", d.Target.Execution, "Queued: conversation "+d.SessionID+" reads it after its current turn."
	}
	call := map[string]any{
		"session_id": d.SessionID, "method": method, "operation_id": protocol.NewID(), "args": args,
		"expected": map[string]string{"registration_generation": d.Generation, "session_revision": d.Target.Revision},
	}
	if err := t.mitosis.Call(ctx, peer, "agent.call", call, nil); err != nil {
		return "", err
	}
	return done, nil
}

// sender is the line a message opens with, so its recipient knows who wrote
// and can answer with send.
func (t *tool) sender(ctx context.Context) string {
	t.mu.Lock()
	home, machine := t.home, t.machine
	t.mu.Unlock()
	from, self := "Message from an Orb conversation", t.mitosis.Self()
	var d descriptor
	if self != "" {
		from = "Message from Orb agent " + home + ", conversation " + self
		_ = t.mitosis.Call(ctx, home, "conversation.describe", map[string]string{"session_id": self}, &d)
	}
	if about := slices.DeleteFunc([]string{d.Name, machine}, func(field string) bool { return field == "" }); len(about) > 0 {
		from += " (" + strings.Join(about, ", ") + ")"
	}
	return from + ":"
}

// reachable reports whether another conversation is reachable, looking at most
// every half minute: it decides whether the tool shows before each prompt.
func (t *tool) reachable(ctx context.Context) bool {
	t.mu.Lock()
	if time.Since(t.seen) < 30*time.Second {
		others := t.others
		t.mu.Unlock()
		return others
	}
	t.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	others := false
	if found, err := t.agents(ctx); err == nil {
		others = len(found) > 1 || len(found) == 1 && len(found[0].conversations) > 0
	}
	t.mu.Lock()
	t.seen, t.others = time.Now(), others
	t.mu.Unlock()
	return others
}
