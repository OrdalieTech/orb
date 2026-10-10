// Package bridgeagents lets an agent see and talk to its owner's other Orb
// conversations, on this machine and on connected devices, through Bridge. Its
// tool exists for the model only while one of them is reachable.
package bridgeagents

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
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/internal/toolutil"
)

// Bridge is how the plugin reaches Bridge, with its owner's reach.
type Bridge interface {
	// Machines are the peer IDs of this machine, first, and of the connected devices.
	Machines(ctx context.Context) ([]string, error)
	// Call runs a peer method as the owner does.
	Call(ctx context.Context, peer, method string, params, result any) error
	// Self is the instance this Orb runs as on Bridge, empty when it is not attached.
	Self() string
}

const name = "agents"

var schema = ai.JSONSchema(`{"type":"object","required":["action"],"properties":{"action":{"type":"string","enum":["list","read","send"]},"id":{"type":"string","description":"An id from list."},"text":{"type":"string","description":"The message to send."}}}`)

// Extension registers the agents tool, active only while another conversation
// is reachable: no Bridge, or nobody else on it, costs the model nothing.
func Extension(b Bridge) extensions.Factory {
	return func(api extensions.API) error {
		t := &tool{bridge: b}
		api.RegisterTool(extensions.ToolDefinition{
			Name: name, Label: "Agents", Parameters: schema,
			Description: "Your other Orb conversations, here and on connected devices. list gives each one's id, machine, folder, title and state; read shows its latest messages; send gives it a message, queued while it works.",
			Execute: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
				var input struct{ Action, ID, Text string }
				if err := toolutil.Decode(raw, &input); err != nil {
					return engine.AgentToolResult{}, err
				}
				text, err := t.run(ctx, input.Action, input.ID, input.Text)
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
	bridge  Bridge
	mu      sync.Mutex
	peers   map[string]string // instance ID → peer, from the last list
	home    string            // this machine's peer and name, from the last list
	machine string
	seen    time.Time // when reachable last looked
	others  bool
}

// conversation is one other Orb conversation as list shows it.
type conversation struct {
	id, peer, machine, folder, title, state string
}

func (t *tool) run(ctx context.Context, action, id, text string) (string, error) {
	switch action {
	case "list":
		found, err := t.list(ctx)
		if err != nil {
			return "", err
		}
		if len(found) == 0 {
			return "No other conversation is reachable.", nil
		}
		lines := make([]string, len(found))
		for i, c := range found {
			fields := []string{c.id, c.machine, c.folder, c.title, c.state}
			lines[i] = strings.Join(slices.DeleteFunc(fields, func(field string) bool { return field == "" }), " · ")
		}
		return strings.Join(lines, "\n"), nil
	case "read", "send":
		if id == "" {
			return "", errors.New("agents: id is required")
		}
		peer, err := t.locate(ctx, id)
		if err != nil {
			return "", err
		}
		if action == "read" {
			return t.read(ctx, peer, id)
		}
		if strings.TrimSpace(text) == "" {
			return "", errors.New("agents: text is required")
		}
		return t.send(ctx, peer, id, text)
	}
	return "", errors.New("agents: action must be list, read or send")
}

// descriptor is what the tool reads of instances.describe.
type descriptor struct {
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

// instances are a machine's conversations other than this one.
func (t *tool) instances(ctx context.Context, peer string) ([]string, error) {
	var ids []string
	cursor := ""
	for range 32 {
		var page struct {
			Items []struct {
				ID        string `json:"instance_id"`
				Available bool   `json:"available"`
			}
			Cursor string
		}
		if err := t.bridge.Call(ctx, peer, "instances.list", map[string]string{"cursor": cursor}, &page); err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.Available && item.ID != t.bridge.Self() {
				ids = append(ids, item.ID)
			}
		}
		if cursor = page.Cursor; cursor == "" {
			break
		}
	}
	return ids, nil
}

func (t *tool) list(ctx context.Context) ([]conversation, error) {
	machines, err := t.bridge.Machines(ctx)
	if err != nil {
		return nil, err
	}
	var found []conversation
	peers := map[string]string{}
	for i, peer := range machines {
		ids, err := t.instances(ctx, peer)
		if err != nil {
			continue // a device out of reach lists nothing
		}
		var named struct{ Name string }
		_ = t.bridge.Call(ctx, peer, "bridge.ping", struct{}{}, &named)
		machine := cmp.Or(named.Name, "a connected device")
		if i == 0 {
			machine = cmp.Or(named.Name, "this machine") // an older Bridge does not tell its owner
			t.mu.Lock()
			t.home, t.machine = peer, machine
			t.mu.Unlock()
		}
		for _, id := range ids {
			var d descriptor
			if t.bridge.Call(ctx, peer, "instances.describe", map[string]string{"instance_id": id}, &d) != nil {
				continue
			}
			peers[id] = peer
			found = append(found, conversation{id: id, peer: peer, machine: machine, folder: d.CWD, title: d.Name, state: d.state()})
		}
	}
	t.mu.Lock()
	t.peers = peers
	t.mu.Unlock()
	return found, nil
}

// locate is the peer an id from list is on, listing again for one it has not seen.
func (t *tool) locate(ctx context.Context, id string) (string, error) {
	t.mu.Lock()
	peer, ok := t.peers[id]
	t.mu.Unlock()
	if ok {
		return peer, nil
	}
	if _, err := t.list(ctx); err != nil {
		return "", err
	}
	t.mu.Lock()
	peer, ok = t.peers[id]
	t.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("agents: no conversation %q is reachable; list them again", id)
	}
	return peer, nil
}

// read looks at a conversation's last readTail messages and shows the last
// readShown of them with text: tool calls and output are left out.
const readTail, readShown = 30, 6

func (t *tool) read(ctx context.Context, peer, id string) (string, error) {
	var d descriptor
	if err := t.bridge.Call(ctx, peer, "instances.describe", map[string]string{"instance_id": id}, &d); err != nil {
		return "", err
	}
	var snapshot struct {
		ID       string            `json:"snapshot_id"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := t.bridge.Call(ctx, peer, "events.subscribe", map[string]any{"instance_id": id, "tail": readTail}, &snapshot); err != nil {
		return "", err
	}
	_ = t.bridge.Call(ctx, peer, "events.unsubscribe", map[string]string{"instance_id": id, "snapshot_id": snapshot.ID}, nil)
	var lines []string
	for _, raw := range snapshot.Messages {
		if line := messageLine(raw); line != "" {
			lines = append(lines, line)
		}
	}
	lines = lines[max(0, len(lines)-readShown):]
	return strings.Join(append([]string{"State: " + d.state()}, lines...), "\n\n"), nil
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

func (t *tool) send(ctx context.Context, peer, id, text string) (string, error) {
	var d descriptor
	if err := t.bridge.Call(ctx, peer, "instances.describe", map[string]string{"instance_id": id}, &d); err != nil {
		return "", err
	}
	if d.Target.Session == "" {
		return "", errors.New("agents: that conversation is not ready for messages")
	}
	method, args, done := "prompt", map[string]any{"text": t.sender(ctx) + "\n\n" + text}, "Sent."
	if d.Target.Execution != "" {
		method, args["execution_id"], done = "follow_up", d.Target.Execution, "Queued: it reads it after its current turn."
	}
	call := map[string]any{
		"instance_id": id, "service": protocol.Service, "method": method, "session_id": d.Target.Session,
		"operation_id": protocol.NewID(), "args": args,
		"expected": map[string]string{"registration_generation": d.Generation, "session_revision": d.Target.Revision},
	}
	if err := t.bridge.Call(ctx, peer, "instances.call", call, nil); err != nil {
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
	from, self := "Message from an Orb conversation", t.bridge.Self()
	var d descriptor
	if self != "" {
		from += " " + self
		_ = t.bridge.Call(ctx, home, "instances.describe", map[string]string{"instance_id": self}, &d)
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
	if machines, err := t.bridge.Machines(ctx); err == nil {
		for _, peer := range machines {
			if ids, err := t.instances(ctx, peer); err == nil && len(ids) > 0 {
				others = true
				break
			}
		}
	}
	t.mu.Lock()
	t.seen, t.others = time.Now(), others
	t.mu.Unlock()
	return others
}
