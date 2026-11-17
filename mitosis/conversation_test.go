package mitosis

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// runtime is an attached Orb holding one conversation; a session.new it takes ends when its
// receipt is next read, as a dispatched one does.
type runtime struct {
	mu               sync.Mutex
	session, pending string
	calls            []Call
}

func (r *runtime) Invoke(_ context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch method {
	case "instances.describe":
		return JSON(map[string]any{"instance_id": "internal", "name": "title " + r.session, "cwd": "/w", "target": map[string]string{"session_id": r.session, "session_revision": "7"}, "methods": []string{"inspect", "prompt", "session.new"}}), nil
	case "call":
		var q Request
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		r.calls = append(r.calls, q.Call)
		if q.Call.Method == "session.new" {
			r.pending = "new-" + r.session
		}
		return JSON(Receipt{InstanceID: q.Call.InstanceID, SessionID: q.Call.SessionID, OperationID: q.Call.OperationID, Status: "accepted"}), nil
	case "operations.get":
		if r.pending != "" {
			r.session, r.pending = r.pending, ""
		}
		return JSON(Receipt{Status: "succeeded"}), nil
	}
	return JSON(struct{}{}), nil
}
func (r *runtime) Close() error { return nil }
func (r *runtime) last() Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

func attachRuntime(t *testing.T, b *Mitosis, session string) (*runtime, string) {
	t.Helper()
	reg, token, err := b.Enroll("")
	if err != nil {
		t.Fatal(err)
	}
	r := &runtime{session: session}
	generation, err := b.Attach(reg.ID, token, protocol.NewID(), r)
	if err != nil {
		t.Fatal(err)
	}
	return r, generation
}

// A peer addresses the agent and one of its conversations: the call reaches the runtime holding
// it, filled with that runtime's generation and revision, and answers the session to follow.
func TestAnAgentCallReachesTheConversationsRuntime(t *testing.T) {
	b, peer := newMitosis(t), newMitosis(t)
	b.SetName("Studio")
	if err := b.AddGrant(Grant{Principal: peer.Principal(), Permissions: []string{"agent.list", "agent.inspect", "agent.prompt"}}); err != nil {
		t.Fatal(err)
	}
	attachRuntime(t, b, "a")
	second, generation := attachRuntime(t, b, "b")
	handle := func(method string, params any, result any) error {
		raw, err := b.Handle(t.Context(), peer.PeerID(), method, JSON(params))
		if err == nil && result != nil {
			err = json.Unmarshal(raw, result)
		}
		return err
	}
	var agent struct {
		Name  string
		Items []Conversation
	}
	if err := handle("agent.describe", struct{}{}, &agent); err != nil || agent.Name != "Studio" || len(agent.Items) != 2 || agent.Items[0].Title == "" || agent.Items[0].State != "idle" {
		t.Fatalf("agent.describe: %+v %v", agent, err)
	}
	var answer map[string]json.RawMessage
	if err := handle("agent.call", map[string]any{"session_id": "b", "method": "prompt", "args": map[string]string{"text": "hi"}}, &answer); err != nil {
		t.Fatal(err)
	}
	if c := second.last(); c.SessionID != "b" || c.Expected.Generation != generation || c.Expected.Revision != "7" || !protocol.ValidID(c.OperationID) {
		t.Fatalf("the call reached its runtime as %+v", c)
	}
	if string(answer["session_id"]) != `"b"` || answer["instance_id"] != nil {
		t.Fatalf("answer: %s", JSON(answer))
	}
	var described struct {
		Methods  []string
		Instance *string `json:"instance_id"`
	}
	if err := handle("conversation.describe", map[string]string{"session_id": "a"}, &described); err != nil || len(described.Methods) != 2 || described.Instance != nil {
		t.Fatalf("conversation.describe: %+v %v", described, err)
	}
	// Without the session grant, session.new is not listed nor taken.
	if err := handle("agent.call", map[string]any{"session_id": "b", "method": "session.new"}, nil); Code(err) != "not_found" {
		t.Fatalf("session.new without its grant: %v", err)
	}
	if err := b.AddGrant(Grant{Principal: peer.Principal(), Permissions: []string{"agent.session.manage"}}); err != nil {
		t.Fatal(err)
	}
	if err := handle("agent.call", map[string]any{"session_id": "b", "method": "session.new"}, &answer); err != nil || string(answer["session_id"]) != `"new-b"` {
		t.Fatalf("session.new answered %s, %v", JSON(answer), err)
	}
	if err := handle("conversation.describe", map[string]string{"session_id": "b"}, nil); Code(err) != "not_found" {
		t.Fatalf("a conversation its runtime left: %v", err)
	}
	// No launcher: a conversation no runtime holds is not found, and the default one is the
	// agent's runtime only when it has one.
	if err := handle("agent.call", map[string]any{"session_id": "stored", "method": "inspect"}, nil); Code(err) != "not_found" {
		t.Fatalf("a conversation nothing holds: %v", err)
	}
	if err := handle("agent.call", map[string]any{"method": "inspect"}, nil); Code(err) != "not_found" {
		t.Fatalf("a default among two runtimes: %v", err)
	}
}

// The default conversation and one no runtime holds are launched on a host that launches.
func TestAnAgentCallLaunchesWhatNoRuntimeHolds(t *testing.T) {
	b, peer := newMitosis(t), newMitosis(t)
	if err := b.AddGrant(Grant{Principal: peer.Principal(), Permissions: []string{"agent.prompt"}}); err != nil {
		t.Fatal(err)
	}
	var launches []string
	runtimes := map[string]*runtime{}
	b.SetHost(func(_ context.Context, _ Principal, method string, params json.RawMessage) (json.RawMessage, error) {
		var q struct {
			SessionID string `json:"session_id"`
		}
		if err := protocol.Decode(params, &q); err != nil || method != "host.launch" {
			return nil, Fail("invalid_params")
		}
		launches = append(launches, string(params))
		session := q.SessionID
		if session == "" {
			session = "latest"
		}
		runtimes[session], _ = attachRuntime(t, b, session)
		return JSON(map[string]string{"session_id": session}), nil
	})
	prompt := func(params map[string]any) json.RawMessage {
		t.Helper()
		params["method"], params["args"] = "prompt", map[string]string{"text": "hi"}
		raw, err := b.Handle(t.Context(), peer.PeerID(), "agent.call", JSON(params))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var answer struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(prompt(map[string]any{}), &answer)
	if answer.SessionID != "latest" || len(launches) != 1 || launches[0] != `{}` || runtimes["latest"].last().Method != "prompt" {
		t.Fatalf("default: %+v %v", answer, launches)
	}
	_ = json.Unmarshal(prompt(map[string]any{"session_id": "old"}), &answer)
	if answer.SessionID != "old" || len(launches) != 2 || launches[1] != `{"session_id":"old"}` {
		t.Fatalf("stored: %+v %v", answer, launches)
	}
	prompt(map[string]any{"session_id": "old"})
	if len(launches) != 2 || len(runtimes["old"].calls) != 2 {
		t.Fatalf("a held conversation was launched again: %v", launches)
	}
}
