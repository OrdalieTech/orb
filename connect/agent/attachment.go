// Package agent adapts an existing Orb runtime without owning its lifetime.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	runtime "github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// Host is the local non-owning attachment boundary shared by the SDK runtime
// and Orb's existing interactive host. It intentionally has no Dispose method.
type Host interface {
	Session() *runtime.AgentSession
	EnableControl() (*runtime.SessionControl, error)
	ObserveSessions(func(*runtime.AgentSession)) func()
	NewSession(context.Context, *extensions.NewSessionOptions) (extensions.SessionReplacementResult, error)
	SwitchSession(context.Context, string, *runtime.AgentSessionRuntimeSwitchOptions) (extensions.SessionReplacementResult, error)
	Fork(context.Context, string, *extensions.ForkOptions) (runtime.AgentSessionRuntimeForkResult, error)
}

type Options struct {
	InstanceID  string
	Store       connect.Store
	Authorize   func(connect.Request) bool
	LedgerQuota int
}
type frozen struct {
	messages []json.RawMessage
	partial  json.RawMessage
	cursor   string
	expires  time.Time
}
type Attachment struct {
	mu                       sync.Mutex
	host                     Host
	control                  *runtime.SessionControl
	options                  Options
	lifetime                 context.Context
	ledger                   *connect.Ledger
	generation               string
	closed                   bool
	inflight                 chan struct{}
	stopSessions, stopEvents func()
	stream                   *connect.Stream
	partial                  json.RawMessage
	messages                 []json.RawMessage
	messageBytes             int
	exhausted                bool
	snapshots                map[string]frozen
}

func Attach(lifetime context.Context, host Host, options Options) (*Attachment, error) {
	if lifetime == nil || host == nil || !protocol.ValidID(options.InstanceID) || options.Authorize == nil {
		return nil, errors.New("attachment requires explicit runtime, lifetime, identity, storage, and authorization")
	}
	if options.LedgerQuota == 0 {
		options.LedgerQuota = protocol.MaxFrame
	}
	ledger, err := connect.OpenLedger(options.Store, options.LedgerQuota)
	if err != nil {
		return nil, err
	}
	control, err := host.EnableControl()
	if err != nil {
		return nil, err
	}
	a := &Attachment{host: host, control: control, options: options, lifetime: lifetime, ledger: ledger, inflight: make(chan struct{}, 64), snapshots: map[string]frozen{}}
	a.stopSessions = host.ObserveSessions(a.bind)
	return a, nil
}
func (a *Attachment) bind(s *runtime.AgentSession) {
	a.mu.Lock()
	stop := a.stopEvents
	a.stopEvents = nil
	closed := a.closed
	a.mu.Unlock()
	if stop != nil {
		stop()
	}
	if closed {
		return
	}
	stop = s.ObserveState(func(state engine.AgentState) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.stream = connect.NewStream(2048, 4<<20)
		a.messages = nil
		a.messageBytes = 0
		a.exhausted = false
		a.snapshots = map[string]frozen{}
		a.partial = nil
		if state.StreamingMessage != nil {
			a.partial, _ = jsonwire.Marshal(state.StreamingMessage)
		}
		for _, m := range state.Messages {
			b, err := jsonwire.Marshal(m)
			if err != nil {
				a.exhausted = true
				continue
			}
			a.appendMessage(b)
		}
	}, func(event engine.AgentEvent) {
		b, err := engine.MarshalAgentEvent(event)
		if err != nil {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closed {
			return
		}
		switch e := event.(type) {
		case engine.MessageStartEvent:
			a.partial, _ = jsonwire.Marshal(e.Message)
		case engine.MessageUpdateEvent:
			a.partial, _ = jsonwire.Marshal(e.Message)
		case engine.MessageEndEvent, engine.AgentEndEvent:
			a.partial = nil
		}
		if len(a.partial) > protocol.MaxFrame/4 {
			a.partial = nil
		}
		if end, ok := event.(engine.MessageEndEvent); ok {
			m, e := jsonwire.Marshal(end.Message)
			if e == nil {
				a.appendMessage(m)
			}
		}
		a.stream.Publish(b)
	})
	a.mu.Lock()
	closed = a.closed
	if !closed {
		a.stopEvents = stop
	}
	a.mu.Unlock()
	if closed {
		stop()
	}
}
func (a *Attachment) appendMessage(b []byte) {
	if a.messageBytes+len(b) > 8<<20 || len(a.messages) >= 16384 {
		a.exhausted = true
		return
	}
	a.messageBytes += len(b)
	a.messages = append(a.messages, append(json.RawMessage(nil), b...))
}
func (a *Attachment) SetGeneration(generation string) error {
	if _, err := protocol.Counter(generation); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.generation = generation
	return nil
}
func (a *Attachment) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	stopSessions, stopEvents := a.stopSessions, a.stopEvents
	a.mu.Unlock()
	if stopSessions != nil {
		stopSessions()
	}
	if stopEvents != nil {
		stopEvents()
	}
	return nil
}
func (a *Attachment) Invoke(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return nil, connect.Fail("unavailable")
	}
	if method == "call" {
		var r connect.Request
		if err := protocol.Decode(params, &r); err != nil {
			return nil, err
		}
		return a.call(ctx, r)
	}
	var p struct {
		Principal connect.Principal `json:"principal"`
		Limit     int               `json:"page_limit,omitempty"`
		Params    struct {
			InstanceID  string `json:"instance_id"`
			OperationID string `json:"operation_id,omitempty"`
			Cursor      string `json:"cursor,omitempty"`
			SnapshotID  string `json:"snapshot_id,omitempty"`
			Offset      string `json:"offset,omitempty"`
		} `json:"params"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	if p.Params.InstanceID != a.options.InstanceID {
		return nil, connect.Fail("not_found")
	}
	r := connect.Request{Principal: p.Principal, Call: connect.Call{InstanceID: p.Params.InstanceID, Method: "inspect"}}
	if !a.options.Authorize(r) {
		return nil, connect.Fail("not_found")
	}
	switch method {
	case "instances.describe":
		return a.inspect(), nil
	case "operations.get":
		receipt, err := a.ledger.Get(p.Principal, p.Params.InstanceID, p.Params.OperationID)
		return connect.JSON(receipt), err
	case "events.subscribe":
		return a.observe(p.Params.Cursor, p.Params.SnapshotID, p.Params.Offset, p.Limit)
	case "events.unsubscribe":
		a.mu.Lock()
		delete(a.snapshots, p.Params.SnapshotID)
		a.mu.Unlock()
		return connect.JSON(struct{}{}), nil
	default:
		return nil, connect.Fail("not_found")
	}
}
func (a *Attachment) inspect() json.RawMessage {
	a.mu.Lock()
	generation := a.generation
	a.mu.Unlock()
	name, cwd := "", ""
	if session := a.host.Session(); session != nil {
		cwd = session.Manager().GetCWD()
		if title := session.Manager().GetSessionName(); title != nil {
			name = *title
		}
	}
	return connect.JSON(struct {
		Name       string                `json:"name,omitempty"`
		CWD        string                `json:"cwd,omitempty"`
		InstanceID string                `json:"instance_id"`
		Service    string                `json:"service"`
		Generation string                `json:"registration_generation"`
		Target     runtime.ControlTarget `json:"target"`
		Methods    []string              `json:"methods"`
	}{name, cwd, a.options.InstanceID, protocol.Service, generation, a.control.Target(), []string{"inspect", "prompt", "steer", "follow_up", "cancel", "session.list", "session.new", "session.switch", "session.fork"}})
}
func (a *Attachment) call(ctx context.Context, r connect.Request) (json.RawMessage, error) {
	if err := connect.ValidateCall(r.Call); err != nil {
		return nil, err
	}
	if r.Call.InstanceID != a.options.InstanceID || !a.options.Authorize(r) {
		return nil, connect.Fail("not_found")
	}
	if r.Call.Method == "inspect" {
		var args struct{}
		if err := protocol.Decode(r.Call.Args, &args); err != nil {
			return nil, err
		}
		return a.inspect(), nil
	}
	if r.Call.Method == "session.list" {
		return a.list(protocol.WithPageLimit(ctx, positivePage(r.PageLimit)), r.Call.Args)
	}
	// Retained receipts are checked before routing/session fences. Their original
	// preconditions remain part of the semantic digest across reconnects.
	if old, err := a.ledger.Get(r.Principal, r.Call.InstanceID, r.Call.OperationID); err == nil {
		_ = old
		receipt, _, err := a.ledger.Accept(r)
		return connect.JSON(receipt), err
	}
	a.mu.Lock()
	current := a.generation
	a.mu.Unlock()
	if r.Generation != current || r.Call.Expected.Generation != current {
		return nil, connect.Fail("stale_target")
	}
	if err := a.validateArgs(r.Call); err != nil {
		return nil, err
	}
	select {
	case a.inflight <- struct{}{}:
	default:
		return nil, connect.Fail("resource_exhausted")
	}
	receipt, duplicate, err := a.ledger.Accept(r)
	if err != nil || duplicate {
		<-a.inflight
		return connect.JSON(receipt), err
	}
	go func() { defer func() { <-a.inflight }(); a.dispatch(r) }()
	return connect.JSON(receipt), nil
}

type textArgs struct {
	Text        string `json:"text"`
	ExecutionID string `json:"execution_id,omitempty"`
}
type switchArgs struct {
	SessionID string `json:"session_id"`
}
type forkArgs struct {
	EntryID string `json:"entry_id"`
}

func (a *Attachment) validateArgs(c connect.Call) error {
	switch c.Method {
	case "prompt":
		var p struct {
			Text *string `json:"text"`
		}
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if p.Text == nil {
			return connect.Fail("invalid_params")
		}
		return nil
	case "steer", "follow_up":
		var p textArgs
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if !protocol.ValidID(p.ExecutionID) {
			return connect.Fail("stale_target")
		}
	case "cancel":
		var p struct {
			ExecutionID string `json:"execution_id"`
		}
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if !protocol.ValidID(p.ExecutionID) {
			return connect.Fail("stale_target")
		}
	case "session.new":
		var p struct{}
		return protocol.Decode(c.Args, &p)
	case "session.switch":
		var p switchArgs
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if p.SessionID == "" || len(p.SessionID) > 128 {
			return connect.Fail("invalid_params")
		}
	case "session.fork":
		var p forkArgs
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if p.EntryID == "" || len(p.EntryID) > 128 {
			return connect.Fail("invalid_params")
		}
	}
	return nil
}
func (a *Attachment) dispatch(r connect.Request) {
	fail := func(err error) { _, _ = a.ledger.Set(r, "failed", nil, controlCode(err)) }
	if !a.options.Authorize(r) {
		fail(connect.Fail("unauthorized"))
		return
	}
	a.mu.Lock()
	gen := a.generation
	a.mu.Unlock()
	if r.Generation != gen {
		fail(connect.Fail("stale_target"))
		return
	}
	target := runtime.ControlTarget{SessionID: r.Call.SessionID, Revision: r.Call.Expected.Revision}
	current := a.control.Target()
	if target.SessionID != current.SessionID || target.Revision != current.Revision {
		fail(connect.Fail("stale_target"))
		return
	}
	if _, err := a.ledger.Set(r, "running", nil, ""); err != nil {
		return
	}
	ctx := runtime.WithControlTarget(a.lifetime, target)
	var result any
	var err error
	switch r.Call.Method {
	case "prompt":
		var p textArgs
		_ = json.Unmarshal(r.Call.Args, &p)
		s := a.host.Session()
		if s == nil {
			err = runtime.ErrSessionDisposed
		} else {
			err = s.PromptControlled(ctx, p.Text)
		}
	case "steer", "follow_up", "cancel":
		var p textArgs
		_ = json.Unmarshal(r.Call.Args, &p)
		target.ExecutionID = p.ExecutionID
		err = a.control.Execution(target, r.Call.Method, p.Text)
	case "session.new":
		result, err = a.host.NewSession(ctx, nil)
	case "session.switch":
		var p switchArgs
		_ = json.Unmarshal(r.Call.Args, &p)
		path := ""
		s := a.host.Session()
		if s != nil {
			for _, entry := range session.List(s.Manager().GetCWD(), s.Manager().GetSessionDir(), nil) {
				if entry.ID == p.SessionID {
					path = entry.Path
					break
				}
			}
		}
		if path == "" {
			err = connect.Fail("not_found")
		} else {
			result, err = a.host.SwitchSession(ctx, path, nil)
		}
	case "session.fork":
		var p forkArgs
		_ = json.Unmarshal(r.Call.Args, &p)
		result, err = a.host.Fork(ctx, p.EntryID, &extensions.ForkOptions{Position: extensions.ForkBefore})
	default:
		err = connect.Fail("not_found")
	}
	if err != nil {
		fail(err)
		return
	}
	_, _ = a.ledger.Set(r, "succeeded", connect.JSON(result), "")
}
func controlCode(err error) string {
	if errors.Is(err, runtime.ErrControlStale) {
		return "stale_target"
	}
	if errors.Is(err, runtime.ErrControlBusy) {
		return "busy"
	}
	return connect.Code(err)
}
func (a *Attachment) list(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Offset string `json:"offset,omitempty"`
	}
	if err := protocol.Decode(args, &p); err != nil {
		return nil, err
	}
	offset := uint64(0)
	var err error
	if p.Offset != "" {
		offset, err = protocol.Counter(p.Offset)
		if err != nil {
			return nil, err
		}
	}
	s := a.host.Session()
	if s == nil {
		return nil, connect.Fail("unavailable")
	}
	entries, err := session.ListContext(ctx, s.Manager().GetCWD(), s.Manager().GetSessionDir(), nil)
	if err != nil {
		return nil, err
	}
	if offset > uint64(len(entries)) {
		return nil, connect.Fail("cursor_expired")
	}
	type item struct {
		ID   string  `json:"session_id"`
		Name *string `json:"name"`
	}
	out := struct {
		Items  []item `json:"items"`
		Offset string `json:"offset,omitempty"`
	}{Items: []item{}}
	end := min(int(offset)+protocol.PageLimit(ctx), len(entries))
	for _, e := range entries[offset:end] {
		out.Items = append(out.Items, item{e.ID, e.Name})
	}
	if end < len(entries) {
		out.Offset = strconv.Itoa(end)
	}
	return connect.JSON(out), nil
}
func (a *Attachment) observe(cursor, id, offset string, limits ...int) (json.RawMessage, error) {
	limit := protocol.MaxPage
	if len(limits) > 0 {
		limit = positivePage(limits[0])
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stream == nil {
		return nil, connect.Fail("unavailable")
	}
	if cursor != "" {
		events, err := a.stream.Replay(cursor)
		if err != nil {
			return nil, err
		}
		out := struct {
			Events []connect.Event `json:"events"`
			Cursor string          `json:"cursor"`
		}{Events: []connect.Event{}, Cursor: cursor}
		size := 0
		for _, e := range events {
			if len(out.Events) >= limit || size+len(e.Data) > protocol.MaxFrame/2 {
				break
			}
			out.Events = append(out.Events, e)
			out.Cursor = e.Cursor
			size += len(e.Data)
		}
		if len(events) > 0 && len(out.Events) == 0 {
			return nil, connect.Fail("cursor_expired")
		}
		return connect.JSON(out), nil
	}
	for key, s := range a.snapshots {
		if time.Now().After(s.expires) {
			delete(a.snapshots, key)
		}
	}
	if id == "" {
		if a.exhausted || len(a.snapshots) >= 4 {
			return nil, connect.Fail("resource_exhausted")
		}
		id = protocol.NewID()
		a.snapshots[id] = frozen{partial: append(json.RawMessage(nil), a.partial...), messages: append([]json.RawMessage(nil), a.messages...), cursor: a.stream.Snapshot().Cursor, expires: time.Now().Add(time.Minute)}
	}
	snap, ok := a.snapshots[id]
	if !ok {
		return nil, connect.Fail("cursor_expired")
	}
	n := uint64(0)
	var err error
	if offset != "" {
		n, err = protocol.Counter(offset)
	}
	if err != nil || n > uint64(len(snap.messages)) {
		return nil, connect.Fail("cursor_expired")
	}
	out := struct {
		SnapshotID string            `json:"snapshot_id"`
		Partial    json.RawMessage   `json:"partial,omitempty"`
		Messages   []json.RawMessage `json:"messages"`
		Cursor     string            `json:"cursor"`
		Offset     string            `json:"offset,omitempty"`
	}{SnapshotID: id, Partial: snap.partial, Messages: []json.RawMessage{}, Cursor: snap.cursor}
	size := 0
	for int(n) < len(snap.messages) && len(out.Messages) < limit {
		m := snap.messages[n]
		if size+len(m) > protocol.MaxFrame/2 {
			break
		}
		out.Messages = append(out.Messages, m)
		size += len(m)
		n++
	}
	if int(n) < len(snap.messages) {
		if len(out.Messages) == 0 {
			return nil, connect.Fail("resource_exhausted")
		}
		out.Offset = strconv.FormatUint(n, 10)
	}
	return connect.JSON(out), nil
}

func positivePage(n int) int {
	if n <= 0 {
		return protocol.MaxPage
	}
	return min(n, protocol.MaxPage)
}
