package mitosis

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// AgentCall is a call to one of an agent's conversations, as agent.call carries it. SessionID
// addresses the conversation; empty, it is the agent's default one. Expected and OperationID,
// when left out, are filled from the runtime holding the conversation.
type AgentCall struct {
	SessionID   string          `json:"session_id,omitempty"`
	Method      string          `json:"method"`
	Args        json.RawMessage `json:"args,omitempty"`
	Expected    Expected        `json:"expected,omitzero"`
	OperationID string          `json:"operation_id,omitempty"`
}

// Conversation is one agent.describe lists: a conversation open in one of the agent's runtimes.
type Conversation struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	State     string `json:"state"` // "idle", "running" during a turn, or "asking" while a question waits for an answer
}

// held is an available runtime of this agent and what it last described of itself.
type held struct {
	id, generation string
	endpoint       Endpoint
	target         struct {
		Session   string `json:"session_id"`
		Revision  string `json:"session_revision"`
		Execution string `json:"execution_id"`
	}
	descriptor map[string]json.RawMessage
}

// observation is what an endpoint takes to read a runtime: its descriptor, an operation, events.
type observation struct {
	OperationID string `json:"operation_id,omitempty"`
	Cursor      string `json:"cursor,omitempty"`
	SnapshotID  string `json:"snapshot_id,omitempty"`
	Offset      string `json:"offset,omitempty"`
	Wait        bool   `json:"wait,omitempty"`
	State       string `json:"state,omitempty"`
	Tail        int    `json:"tail,omitempty"`
	Catalog     string `json:"catalog,omitempty"`
}

// observe reads runtime id through its endpoint, as principal p.
func (b *Mitosis) observe(ctx context.Context, p Principal, e Endpoint, method, id string, o observation) (json.RawMessage, error) {
	return e.Invoke(ctx, method, JSON(struct {
		Principal Principal `json:"principal"`
		Limit     int       `json:"page_limit"`
		Params    any       `json:"params"`
	}{p, protocol.PageLimit(ctx), struct {
		InstanceID string `json:"instance_id"`
		observation
	}{id, o}}))
}

// runtimes describes the agent's available runtimes. A describe leaves out the models and commands
// whose digest is catalog when set, else the digest each runtime last sent: resolving a session
// needs neither.
func (b *Mitosis) runtimes(ctx context.Context, catalog *string) []held {
	b.mu.Lock()
	ids := make([]string, 0, len(b.active))
	for id := range b.active {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	slices.Sort(ids)
	out := []held{}
	for _, id := range ids {
		if h, ok := b.describe(ctx, id, catalog); ok {
			out = append(out, h)
		}
	}
	return out
}

func (b *Mitosis) describe(ctx context.Context, id string, catalog *string) (held, bool) {
	b.mu.Lock()
	r, ok := b.active[id]
	b.mu.Unlock()
	if !ok {
		return held{}, false
	}
	known := r.catalog
	if catalog != nil {
		known = *catalog
	}
	raw, err := b.observe(ctx, b.Principal(), r.endpoint, "instances.describe", id, observation{Catalog: known})
	h := held{id: id, generation: r.generation, endpoint: r.endpoint}
	if err != nil || json.Unmarshal(raw, &h.descriptor) != nil || json.Unmarshal(h.descriptor["target"], &h.target) != nil {
		return held{}, false
	}
	var digest string
	if json.Unmarshal(h.descriptor["catalog"], &digest) == nil && digest != "" {
		b.mu.Lock()
		if r, ok := b.active[id]; ok && r.generation == h.generation {
			r.catalog = digest
			b.active[id] = r
		}
		b.mu.Unlock()
	}
	delete(h.descriptor, "instance_id")
	return h, true
}

// holder is the available runtime holding session.
func (b *Mitosis) holder(ctx context.Context, session string, catalog *string) (held, error) {
	if session != "" {
		for _, h := range b.runtimes(ctx, catalog) {
			if h.target.Session == session {
				return h, nil
			}
		}
	}
	return held{}, Fail("not_found")
}

// Held is the session each available runtime holds, by registration.
func (b *Mitosis) Held(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, h := range b.runtimes(ctx, nil) {
		out[h.id] = h.target.Session
	}
	return out
}

// permits reports whether p may do what permission names with this agent's conversations.
func (b *Mitosis) permits(p Principal, permission string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed || b.closed || b.state.Blocked[p.PeerID] {
		return false
	}
	return b.owner(p) || slices.ContainsFunc(b.state.Grants, func(g Grant) bool {
		return g.Principal == p && g.Destination == "" && slices.Contains(g.Permissions, permission)
	})
}

// conversations are those open in the agent's runtimes, for agent.describe.
func (b *Mitosis) conversations(ctx context.Context) []Conversation {
	out := []Conversation{}
	for _, h := range b.runtimes(ctx, nil) {
		if h.target.Session == "" {
			continue
		}
		c := Conversation{SessionID: h.target.Session, State: "idle"}
		_ = json.Unmarshal(h.descriptor["name"], &c.Title)
		_ = json.Unmarshal(h.descriptor["cwd"], &c.CWD)
		switch {
		case len(h.descriptor["input"]) > 0 && string(h.descriptor["input"]) != "null":
			c.State = "asking"
		case h.target.Execution != "":
			c.State = "running"
		}
		out = append(out, c)
	}
	return out
}

// resolve finds the runtime holding session, or the agent's default conversation when session is
// empty: its most recent stored one on a host that launches runtimes (host.launch with neither
// folder nor session), else its one running runtime. A stored conversation no runtime holds is
// launched.
func (b *Mitosis) resolve(ctx context.Context, session string) (held, error) {
	b.mu.Lock()
	host := b.host
	b.mu.Unlock()
	if session != "" {
		h, err := b.holder(ctx, session, nil)
		if err == nil || host == nil {
			return h, err
		}
	} else if host == nil {
		running := slices.DeleteFunc(b.runtimes(ctx, nil), func(h held) bool { return h.target.Session == "" })
		if len(running) != 1 {
			return held{}, Fail("not_found")
		}
		return running[0], nil
	}
	params := map[string]string{}
	if session != "" {
		params["session_id"] = session
	}
	raw, err := host(ctx, b.Principal(), "host.launch", JSON(params))
	if err != nil {
		return held{}, err
	}
	var launched struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &launched) != nil {
		return held{}, Fail("unavailable")
	}
	return b.holder(ctx, launched.SessionID, nil)
}

// agentCall runs call on the runtime holding its conversation. The answer says, as session_id,
// the conversation the runtime holds after it: a new, switched or forked one is the address
// that follows.
func (b *Mitosis) agentCall(ctx context.Context, p Principal, call AgentCall) (json.RawMessage, error) {
	if !b.permits(p, permission(call.Method)) {
		return nil, Fail("not_found")
	}
	// A retried operation goes again to the runtime and target it first resolved to, so its
	// receipt answers it, even after a session change moved the runtime on.
	key := ledgerKey(p, "", call.OperationID)
	b.mu.Lock()
	first, retried := b.resolved[key]
	b.mu.Unlock()
	h, err := held{}, error(nil)
	if retried && call.OperationID != "" {
		var ok bool
		if h, ok = b.describe(ctx, first.InstanceID, nil); !ok {
			err = Fail("unavailable")
		}
	} else {
		h, err = b.resolve(ctx, call.SessionID)
	}
	if err != nil {
		return nil, err
	}
	c := Call{InstanceID: h.id, Service: protocol.Service, Method: call.Method, SessionID: h.target.Session, Expected: call.Expected, OperationID: call.OperationID, Args: call.Args}
	if retried && call.OperationID != "" {
		c.SessionID, c.Expected = first.SessionID, first.Expected
	}
	if len(c.Args) == 0 {
		c.Args = JSON(struct{}{})
	}
	if c.Expected.Generation == "" {
		c.Expected.Generation = h.generation
	}
	if c.Expected.Revision == "" {
		c.Expected.Revision = h.target.Revision
	}
	if c.OperationID == "" {
		c.OperationID = protocol.NewID()
	} else if !retried {
		b.mu.Lock()
		if len(b.resolved) >= 1024 {
			clear(b.resolved)
		}
		b.resolved[key] = c
		b.mu.Unlock()
	}
	raw, err := b.Call(ctx, p, c)
	if err != nil {
		return nil, err
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil {
		return raw, nil
	}
	session := h.target.Session
	switch call.Method {
	case "session.new", "session.switch", "session.fork":
		b.settle(ctx, p, h, c.OperationID)
		if after, ok := b.describe(ctx, h.id, nil); ok {
			session = after.target.Session
		}
	}
	delete(result, "instance_id")
	result["session_id"] = JSON(session)
	return JSON(result), nil
}

// settle waits, a while at most, for an operation to end: a session change answers with the
// session it leaves the runtime in.
func (b *Mitosis) settle(ctx context.Context, p Principal, h held, operation string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		raw, err := b.observe(ctx, p, h.endpoint, "operations.get", h.id, observation{OperationID: operation})
		var r Receipt
		if err != nil || json.Unmarshal(raw, &r) != nil || r.Status != "accepted" && r.Status != "running" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
}
