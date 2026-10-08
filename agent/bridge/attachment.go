// Package bridge adapts an existing Orb runtime to Bridge without owning its lifetime.
package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	runtime "github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/plugins/usage"
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
	Status func(*runtime.AgentSession) string
	// Usage is the plan limits of the session's provider account; nil when it reports none. A new
	// reading moves the pulse, so followers describe again. It is called often and must not block.
	Usage func(*runtime.AgentSession) *usage.Snapshot
	// Complete is what `@query` completes to in a message, as the TUI offers it.
	Complete    func(context.Context, *runtime.AgentSession, string) []Completion
	InstanceID  string
	Store       document.Document
	Authorize   func(bridge.Request) bool
	LedgerQuota int
}
type frozen struct {
	first    int // the index in the conversation of messages[0]
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
	ledger                   *bridge.Ledger
	generation               string
	closed                   bool
	inflight                 chan struct{}
	stopSessions, stopEvents func()
	stream                   *bridge.Stream
	partial                  json.RawMessage
	messages                 []json.RawMessage
	messageBytes             int
	dropped                  int // the oldest messages the window let go
	exhausted                bool
	snapshots                map[string]frozen
	pictures                 map[string]picture // images the stream carried, by reference (see swapImages)
	pictureOrder             []string
	pictureBytes             int
}

type picture struct{ data, mime string }

func Attach(lifetime context.Context, host Host, options Options) (*Attachment, error) {
	if lifetime == nil || host == nil || !protocol.ValidID(options.InstanceID) || options.Authorize == nil {
		return nil, errors.New("attachment requires explicit runtime, lifetime, identity, storage, and authorization")
	}
	if options.LedgerQuota == 0 {
		options.LedgerQuota = protocol.MaxFrame
	}
	ledger, err := bridge.OpenLedger(options.Store, options.LedgerQuota)
	if err != nil {
		return nil, err
	}
	control, err := host.EnableControl()
	if err != nil {
		return nil, err
	}
	a := &Attachment{host: host, control: control, options: options, lifetime: lifetime, ledger: ledger, inflight: make(chan struct{}, 64), snapshots: map[string]frozen{}, pictures: map[string]picture{}}
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
	stopState := s.ObserveState(func(state engine.AgentState) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.stream = bridge.NewStream(2048, 4<<20)
		a.messages = nil
		a.messageBytes = 0
		a.dropped = 0
		a.exhausted = false
		a.snapshots = map[string]frozen{}
		a.partial = nil
		if state.StreamingMessage != nil {
			a.partial, _ = jsonwire.Marshal(state.StreamingMessage)
			a.partial = a.swapImages(a.partial)
		}
		for _, m := range state.Messages {
			b, err := jsonwire.Marshal(m)
			if err != nil {
				a.exhausted = true
				continue
			}
			a.appendMessage(a.swapImages(b))
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
		b = a.swapImages(b)
		switch event.(type) {
		case engine.MessageStartEvent, engine.MessageUpdateEvent, engine.MessageEndEvent:
			var fields struct {
				Message json.RawMessage `json:"message"`
			}
			if err := json.Unmarshal(b, &fields); err != nil {
				return
			}
			if _, end := event.(engine.MessageEndEvent); end {
				a.partial = nil
				a.appendMessage(fields.Message)
			} else {
				a.partial = fields.Message
			}
			// An update repeats the whole message in its assistantMessageEvent; followers read the message.
			if _, update := event.(engine.MessageUpdateEvent); update {
				b = append(append([]byte(`{"type":"message_update","message":`), fields.Message...), '}')
			}
		case engine.AgentEndEvent:
			a.partial = nil
		}
		if len(a.partial) > protocol.MaxFrame/4 {
			a.partial = nil
		}
		a.stream.Publish(b)
	})
	// Session events a follower draws (compaction, retries, a new name) ride the same stream;
	// the rest repeat engine events or change nothing on screen.
	stopSession := s.Subscribe(func(event any) {
		switch event.(type) {
		case runtime.CompactionStartEvent, runtime.CompactionEndEvent, runtime.AutoRetryStartEvent, runtime.AutoRetryEndEvent, runtime.SessionInfoChangedEvent:
		default:
			return
		}
		if b, err := runtime.MarshalSessionEvent(event); err == nil {
			a.mu.Lock()
			if !a.closed && a.stream != nil {
				a.stream.Publish(b)
			}
			a.mu.Unlock()
		}
	})
	stop = func() { stopState(); stopSession() }
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

// appendMessage keeps the conversation's newest messages within 8 MiB: a long one still opens at
// its end, and pages back only as far as the window reaches.
func (a *Attachment) appendMessage(b []byte) {
	a.messageBytes += len(b)
	a.messages = append(a.messages, append(json.RawMessage(nil), b...))
	for (a.messageBytes > 8<<20 || len(a.messages) > 16384) && len(a.messages) > 1 {
		a.messageBytes -= len(a.messages[0])
		a.messages = a.messages[1:]
		a.dropped++
	}
}

// swapImages shapes what the stream and snapshots carry: each image becomes a reference a client
// fetches at the size it shows (the image call), and long tool output keeps its ends, so neither
// grows with what a follower never shows. The last 32 MiB of images are kept; a.mu is held.
func (a *Attachment) swapImages(b []byte) []byte {
	large := len(b) > protocol.MaxFrame/4
	if len(b) <= 8<<10 && !bytes.Contains(b, []byte(`"type":"image"`)) {
		return b
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	var v any
	if decoder.Decode(&v) != nil {
		return b
	}
	var tool bool // a tool's result or output, whose text is output too
	var swap func(any)
	swap = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			// Long tool output and arguments keep their ends, what a follower shows of them; what anyone
			// said stays whole unless its message would outgrow a page.
			for key, x := range v {
				text, ok := x.(string)
				switch {
				case !ok || key == "data":
				case len(text) > 8<<10 && (tool || key != "text" && key != "thinking"):
					v[key] = text[:1<<10] + "\n…\n" + text[len(text)-7<<10:]
				case large && len(text) > 32<<10:
					v[key] = text[:16<<10] + "\n…\n" + text[len(text)-16<<10:]
				}
			}
			if data, ok := v["data"].(string); ok && v["type"] == "image" && data != "" {
				sum := sha256.Sum256([]byte(data))
				ref := base64.RawURLEncoding.EncodeToString(sum[:16])
				if _, kept := a.pictures[ref]; !kept {
					mime, _ := v["mimeType"].(string)
					a.pictures[ref], a.pictureOrder, a.pictureBytes = picture{data, mime}, append(a.pictureOrder, ref), a.pictureBytes+len(data)
					for a.pictureBytes > 32<<20 && len(a.pictureOrder) > 1 {
						a.pictureBytes -= len(a.pictures[a.pictureOrder[0]].data)
						delete(a.pictures, a.pictureOrder[0])
						a.pictureOrder = a.pictureOrder[1:]
					}
				}
				delete(v, "data")
				v["ref"] = ref
				return
			}
			for _, x := range v {
				swap(x)
			}
		case []any:
			for _, x := range v {
				swap(x)
			}
		}
	}
	top, _ := v.(map[string]any)
	message, _ := top["message"].(map[string]any)
	kind, _ := top["type"].(string)
	tool = top["role"] == "toolResult" || message["role"] == "toolResult" || strings.HasPrefix(kind, "tool_execution_")
	swap(v)
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(v) != nil {
		return b
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// image is one the stream carried, fitted within size pixels and half a frame, as base64.
func (a *Attachment) image(args json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Ref  string `json:"ref"`
		Size int    `json:"size"`
	}
	if err := protocol.Decode(args, &p); err != nil {
		return nil, err
	}
	a.mu.Lock()
	pic, ok := a.pictures[p.Ref]
	a.mu.Unlock()
	raw, err := base64.StdEncoding.DecodeString(pic.data)
	if !ok || err != nil {
		return nil, bridge.Fail("not_found")
	}
	size := min(max(p.Size, 64), 4096)
	fitted := tools.ResizeImage(raw, pic.mime, &tools.ImageResizeOptions{MaxWidth: size, MaxHeight: size, MaxBytes: protocol.MaxFrame / 2})
	if fitted == nil {
		return nil, bridge.Fail("unavailable")
	}
	return bridge.JSON(map[string]string{"data": fitted.Data, "mime_type": fitted.MimeType}), nil
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
		return nil, bridge.Fail("unavailable")
	}
	if method == "call" {
		var r bridge.Request
		if err := protocol.Decode(params, &r); err != nil {
			return nil, err
		}
		return a.call(ctx, r)
	}
	var p struct {
		Principal bridge.Principal `json:"principal"`
		Limit     int              `json:"page_limit,omitempty"`
		Params    struct {
			InstanceID  string `json:"instance_id"`
			OperationID string `json:"operation_id,omitempty"`
			Cursor      string `json:"cursor,omitempty"`
			SnapshotID  string `json:"snapshot_id,omitempty"`
			Offset      string `json:"offset,omitempty"`
			Wait        bool   `json:"wait,omitempty"`
			State       string `json:"state,omitempty"`
			Tail        int    `json:"tail,omitempty"`
			Catalog     string `json:"catalog,omitempty"`
		} `json:"params"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	if p.Params.InstanceID != a.options.InstanceID {
		return nil, bridge.Fail("not_found")
	}
	r := bridge.Request{Principal: p.Principal, Call: bridge.Call{InstanceID: p.Params.InstanceID, Method: "inspect"}}
	if !a.options.Authorize(r) {
		return nil, bridge.Fail("not_found")
	}
	switch method {
	case "instances.describe":
		return a.inspect(p.Params.Catalog), nil
	case "operations.get":
		receipt, err := a.ledger.Get(p.Principal, p.Params.InstanceID, p.Params.OperationID)
		return bridge.JSON(receipt), err
	case "events.subscribe":
		if p.Params.Wait && p.Params.Cursor != "" {
			return a.await(ctx, p.Params.Cursor, p.Params.State, positivePage(p.Limit))
		}
		return a.observe(p.Params.Cursor, p.Params.SnapshotID, p.Params.Offset, p.Params.Tail, p.Limit)
	case "events.unsubscribe":
		a.mu.Lock()
		delete(a.snapshots, p.Params.SnapshotID)
		a.mu.Unlock()
		return bridge.JSON(struct{}{}), nil
	default:
		return nil, bridge.Fail("not_found")
	}
}

// Descriptor is what instances.describe reports about the attached runtime.
type Descriptor struct {
	Status     string                `json:"status,omitempty"`
	Model      string                `json:"model,omitempty"`
	Provider   string                `json:"provider,omitempty"` // the model's: two providers may offer one name
	Thinking   string                `json:"thinking,omitempty"`
	Stats      *runtime.SessionStats `json:"stats,omitempty"`
	Usage      *usage.Snapshot       `json:"usage,omitempty"`
	Commands   []Command             `json:"commands,omitempty"`
	Catalog    string                `json:"catalog,omitempty"` // digest of models and commands, which a describe sending it omits
	Waits      bool                  `json:"waits"`             // events.subscribe takes wait: a follower long-polls
	Models     []bridge.Model        `json:"models,omitempty"`
	Name       string                `json:"name,omitempty"`
	CWD        string                `json:"cwd,omitempty"`
	InstanceID string                `json:"instance_id"`
	Service    string                `json:"service"`
	Generation string                `json:"registration_generation"`
	Target     runtime.ControlTarget `json:"target"`
	Methods    []string              `json:"methods"`
	Input      *runtime.InputRequest `json:"input,omitempty"`
}

// Command is a slash command the instance runs: an extension's, a prompt template or a skill.
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Completion is one `@` completion: the text that replaces the token, and how to show it.
type Completion struct {
	Text   string `json:"text"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// methods are the calls an instance takes, before each principal's permissions filter them.
var methods = []string{"inspect", "prompt", "steer", "follow_up", "cancel", "session.list", "session.new", "session.switch", "session.fork", "input.reply", "session.model", "session.name", "session.compact", "shell", "complete", "image"}

// inspect describes the instance. Its models and commands, most of a descriptor and rarely
// changed, are left out when [known] is their digest: a client sends the one it holds.
func (a *Attachment) inspect(known string) json.RawMessage {
	a.mu.Lock()
	generation := a.generation
	a.mu.Unlock()
	d := Descriptor{InstanceID: a.options.InstanceID, Service: protocol.Service, Generation: generation, Target: a.control.Target(), Methods: methods, Waits: true}
	if session := a.host.Session(); session != nil {
		d.CWD = session.Manager().GetCWD()
		state := session.State()
		if state.Model != nil {
			d.Model, d.Provider = state.Model.Name, string(state.Model.Provider)
		}
		d.Thinking = string(state.ThinkingLevel)
		stats := session.GetSessionStats()
		d.Stats = &stats
		if a.options.Usage != nil {
			d.Usage = a.options.Usage(session)
		}
		for _, c := range session.Commands() {
			d.Commands = append(d.Commands, Command{c.Name, c.Description})
		}
		for _, model := range session.AvailableModels() {
			row := bridge.Model{ID: model.ID, Provider: string(model.Provider), Name: model.Name}
			for _, level := range ai.SupportedThinkingLevels(&model) {
				row.Thinking = append(row.Thinking, string(level))
			}
			d.Models = append(d.Models, row)
		}
		if a.options.Status != nil {
			if d.Status = a.options.Status(session); len(d.Status) > 1024 {
				d.Status = ""
			}
		}
		d.Input = session.PendingInput()
		if title := session.Manager().GetSessionName(); title != nil {
			d.Name = *title
		}
		sum := sha256.Sum256(bridge.JSON([]any{d.Models, d.Commands}))
		if d.Catalog = base64.RawURLEncoding.EncodeToString(sum[:12]); d.Catalog == known {
			d.Models, d.Commands = nil, nil
		}
	}
	return bridge.JSON(d)
}
func (a *Attachment) call(ctx context.Context, r bridge.Request) (json.RawMessage, error) {
	if err := bridge.ValidateCall(r.Call); err != nil {
		return nil, err
	}
	if r.Call.InstanceID != a.options.InstanceID || !a.options.Authorize(r) {
		return nil, bridge.Fail("not_found")
	}
	if r.Call.Method == "inspect" {
		var args struct{}
		if err := protocol.Decode(r.Call.Args, &args); err != nil {
			return nil, err
		}
		return a.inspect(""), nil
	}
	if r.Call.Method == "session.list" {
		return a.list(protocol.WithPageLimit(ctx, positivePage(r.PageLimit)), r.Call.Args)
	}
	if r.Call.Method == "image" {
		return a.image(r.Call.Args)
	}
	if r.Call.Method == "complete" {
		var args struct {
			Query string `json:"query"`
		}
		if err := protocol.Decode(r.Call.Args, &args); err != nil {
			return nil, err
		}
		s := a.host.Session()
		if s == nil || a.options.Complete == nil {
			return nil, bridge.Fail("unavailable")
		}
		return bridge.JSON(map[string]any{"items": append([]Completion{}, a.options.Complete(ctx, s, args.Query)...)}), nil
	}
	// Retained receipts are checked before routing/session fences. Their original
	// preconditions remain part of the semantic digest across reconnects.
	if old, err := a.ledger.Get(r.Principal, r.Call.InstanceID, r.Call.OperationID); err == nil {
		_ = old
		receipt, _, err := a.ledger.Accept(r)
		return bridge.JSON(receipt), err
	}
	a.mu.Lock()
	current := a.generation
	a.mu.Unlock()
	if r.Generation != current || r.Call.Expected.Generation != current {
		return nil, bridge.Fail("stale_target")
	}
	if err := a.validateArgs(r.Call); err != nil {
		return nil, err
	}
	select {
	case a.inflight <- struct{}{}:
	default:
		return nil, bridge.Fail("resource_exhausted")
	}
	receipt, duplicate, err := a.ledger.Accept(r)
	if err != nil || duplicate {
		<-a.inflight
		return bridge.JSON(receipt), err
	}
	go func() { defer func() { <-a.inflight }(); a.dispatch(r) }()
	return bridge.JSON(receipt), nil
}

type textArgs struct {
	Text        string `json:"text"`
	ExecutionID string `json:"execution_id,omitempty"`
}
type inputArgs struct {
	ExecutionID string `json:"execution_id"`
	ID          string `json:"id"`
	Value       string `json:"value"`
}

type modelArgs struct {
	Model    string                 `json:"model"`
	Provider string                 `json:"provider"`
	Thinking *ai.ModelThinkingLevel `json:"thinking,omitempty"`
}
type switchArgs struct {
	SessionID string `json:"session_id"`
}
type forkArgs struct {
	EntryID string `json:"entry_id"`
}
type nameArgs struct {
	Name string `json:"name"`
}
type compactArgs struct {
	Instructions string `json:"instructions,omitempty"`
}
type shellArgs struct {
	Command string `json:"command"`
}

func (a *Attachment) validateArgs(c bridge.Call) error {
	switch c.Method {
	case "session.model":
		var p modelArgs
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if p.Model == "" || len(p.Model) > 256 || p.Provider == "" || len(p.Provider) > 128 || p.Thinking != nil && len(*p.Thinking) > 16 {
			return bridge.Fail("invalid_params")
		}
	case "input.reply":
		var p inputArgs
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if !protocol.ValidID(p.ExecutionID) || !protocol.ValidID(p.ID) || len(p.Value) > 64<<10 {
			return bridge.Fail("invalid_params")
		}
	case "prompt":
		var p struct {
			Text *string `json:"text"`
		}
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if p.Text == nil {
			return bridge.Fail("invalid_params")
		}
		return nil
	case "steer", "follow_up":
		var p textArgs
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if !protocol.ValidID(p.ExecutionID) {
			return bridge.Fail("stale_target")
		}
	case "cancel":
		var p struct {
			ExecutionID string `json:"execution_id"`
		}
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if !protocol.ValidID(p.ExecutionID) {
			return bridge.Fail("stale_target")
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
			return bridge.Fail("invalid_params")
		}
	case "session.fork":
		var p forkArgs
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if p.EntryID == "" || len(p.EntryID) > 128 {
			return bridge.Fail("invalid_params")
		}
	case "session.name":
		var p nameArgs
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if strings.TrimSpace(p.Name) == "" || len(p.Name) > 512 {
			return bridge.Fail("invalid_params")
		}
	case "session.compact":
		var p compactArgs
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if len(p.Instructions) > 64<<10 {
			return bridge.Fail("invalid_params")
		}
	case "shell":
		var p shellArgs
		if err := protocol.Decode(c.Args, &p); err != nil {
			return err
		}
		if strings.TrimSpace(p.Command) == "" || len(p.Command) > 64<<10 {
			return bridge.Fail("invalid_params")
		}
	}
	return nil
}
func (a *Attachment) dispatch(r bridge.Request) {
	fail := func(err error) { _, _ = a.ledger.Set(r, "failed", nil, controlCode(err)) }
	if !a.options.Authorize(r) {
		fail(bridge.Fail("unauthorized"))
		return
	}
	a.mu.Lock()
	gen := a.generation
	a.mu.Unlock()
	if r.Generation != gen {
		fail(bridge.Fail("stale_target"))
		return
	}
	target := runtime.ControlTarget{SessionID: r.Call.SessionID, Revision: r.Call.Expected.Revision}
	current := a.control.Target()
	if target.SessionID != current.SessionID || target.Revision != current.Revision {
		fail(bridge.Fail("stale_target"))
		return
	}
	if _, err := a.ledger.Set(r, "running", nil, ""); err != nil {
		return
	}
	ctx := runtime.WithControlTarget(a.lifetime, target)
	var result any
	var err error
	switch r.Call.Method {
	case "session.model":
		var p modelArgs
		_ = json.Unmarshal(r.Call.Args, &p)
		s := a.host.Session()
		err = bridge.Fail("not_found")
		if s != nil {
			for _, model := range s.AvailableModels() {
				if model.ID != p.Model || string(model.Provider) != p.Provider {
					continue
				}
				if p.Thinking != nil && !slices.Contains(ai.SupportedThinkingLevels(&model), *p.Thinking) {
					err = bridge.Fail("invalid_params")
					break
				}
				err = s.SetModelAndThinking(ctx, model, p.Thinking)
				break
			}
		}
	case "input.reply":
		var p inputArgs
		_ = json.Unmarshal(r.Call.Args, &p)
		target.ExecutionID = p.ExecutionID
		err = a.control.Execution(target, "input.reply", string(bridge.JSON(struct {
			ID    string `json:"id"`
			Value string `json:"value"`
		}{p.ID, p.Value})))
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
			entries, listErr := storedSessions(ctx, s.Manager())
			if listErr != nil {
				fail(listErr)
				return
			}
			for _, entry := range entries {
				if entry.ID == p.SessionID {
					path = entry.Reference()
					break
				}
			}
		}
		if path == "" {
			err = bridge.Fail("not_found")
		} else {
			result, err = a.host.SwitchSession(ctx, path, nil)
		}
	case "session.name":
		var p nameArgs
		_ = json.Unmarshal(r.Call.Args, &p)
		if s := a.host.Session(); s == nil {
			err = runtime.ErrSessionDisposed
		} else {
			err = s.SetSessionName(strings.TrimSpace(p.Name))
		}
	case "session.fork":
		var p forkArgs
		_ = json.Unmarshal(r.Call.Args, &p)
		result, err = a.host.Fork(ctx, p.EntryID, &extensions.ForkOptions{Position: extensions.ForkBefore})
	case "session.compact", "shell":
		s := a.host.Session()
		if s == nil {
			err = runtime.ErrSessionDisposed
			break
		}
		// The same calls RPC's compact and bash make: a command's output joins the conversation.
		var p struct {
			compactArgs
			shellArgs
		}
		_ = json.Unmarshal(r.Call.Args, &p)
		if r.Call.Method == "shell" {
			result, err = s.ExecuteUserBashWithID(ctx, p.Command, nil, nil)
		} else {
			result, err = s.Compact(ctx, p.Instructions)
		}
	default:
		err = bridge.Fail("not_found")
	}
	if err != nil {
		fail(err)
		return
	}
	_, _ = a.ledger.Set(r, "succeeded", bridge.JSON(result), "")
}
func controlCode(err error) string {
	if errors.Is(err, runtime.ErrControlStale) {
		return "stale_target"
	}
	if errors.Is(err, runtime.ErrControlBusy) {
		return "busy"
	}
	return bridge.Code(err)
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
		return nil, bridge.Fail("unavailable")
	}
	entries, err := storedSessions(ctx, s.Manager())
	if err != nil {
		return nil, err
	}
	if offset > uint64(len(entries)) {
		return nil, bridge.Fail("cursor_expired")
	}
	type item struct {
		ID       string  `json:"session_id"`
		Name     *string `json:"name"`
		Modified int64   `json:"modified,omitempty"`
		Messages int     `json:"messages,omitempty"`
		First    string  `json:"first,omitempty"`
	}
	out := struct {
		Items  []item `json:"items"`
		Offset string `json:"offset,omitempty"`
	}{Items: []item{}}
	end := min(int(offset)+protocol.PageLimit(ctx), len(entries))
	for _, e := range entries[offset:end] {
		first, modified := []rune(e.FirstMessage), int64(0)
		if !e.Modified.IsZero() {
			modified = e.Modified.UnixMilli()
		}
		out.Items = append(out.Items, item{e.ID, e.Name, modified, e.MessageCount, string(first[:min(len(first), 160)])})
	}
	if end < len(entries) {
		out.Offset = strconv.Itoa(end)
	}
	return bridge.JSON(out), nil
}

// observe pages a snapshot of the conversation, from offset or, for a new one, its last tail
// messages (all of them without one: a follower on a phone asks for the end and pages back),
// or replays the stream after cursor.
func (a *Attachment) observe(cursor, id, offset string, tail int, limits ...int) (json.RawMessage, error) {
	limit := protocol.MaxPage
	if len(limits) > 0 {
		limit = positivePage(limits[0])
	}
	if cursor != "" {
		out, err := a.replay(cursor, limit)
		if err != nil {
			return nil, err
		}
		return bridge.JSON(out), nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stream == nil {
		return nil, bridge.Fail("unavailable")
	}
	for key, s := range a.snapshots {
		if time.Now().After(s.expires) {
			delete(a.snapshots, key)
		}
	}
	if id == "" {
		if a.exhausted || len(a.snapshots) >= 4 {
			return nil, bridge.Fail("resource_exhausted")
		}
		id = protocol.NewID()
		a.snapshots[id] = frozen{first: a.dropped, partial: append(json.RawMessage(nil), a.partial...), messages: append([]json.RawMessage(nil), a.messages...), cursor: a.stream.Cursor(), expires: time.Now().Add(time.Minute)}
	}
	snap, ok := a.snapshots[id]
	if !ok {
		return nil, bridge.Fail("cursor_expired")
	}
	n := uint64(0)
	var err error
	if offset != "" {
		n, err = protocol.Counter(offset)
	} else if tail > 0 && tail < len(snap.messages) {
		n = uint64(snap.first + len(snap.messages) - tail)
	}
	end := uint64(snap.first + len(snap.messages))
	if err != nil || n > end {
		return nil, bridge.Fail("cursor_expired")
	}
	n = max(n, uint64(snap.first)) // offsets count the whole conversation; the window starts later
	out := struct {
		SnapshotID string            `json:"snapshot_id"`
		Partial    json.RawMessage   `json:"partial,omitempty"`
		Messages   []json.RawMessage `json:"messages"`
		Cursor     string            `json:"cursor"`
		Offset     string            `json:"offset,omitempty"`
		From       uint64            `json:"from,omitempty"` // the index of the first message here
	}{SnapshotID: id, Partial: snap.partial, Messages: []json.RawMessage{}, Cursor: snap.cursor, From: n}
	size := 0
	for n < end && len(out.Messages) < limit {
		m := snap.messages[int(n)-snap.first]
		if size+len(m) > protocol.MaxFrame/2 {
			break
		}
		out.Messages = append(out.Messages, m)
		size += len(m)
		n++
	}
	if n < end {
		if len(out.Messages) == 0 {
			return nil, bridge.Fail("resource_exhausted")
		}
		out.Offset = strconv.FormatUint(n, 10)
	}
	return bridge.JSON(out), nil
}

// page is a stretch of the event stream after a cursor, with the instance's pulse: a digest of
// what its descriptor says changes (session, turn, pending input, name, model, reasoning).
type page struct {
	Events []bridge.Event `json:"events"`
	Cursor string         `json:"cursor"`
	State  string         `json:"state"`
}

func (a *Attachment) replay(cursor string, limit int) (page, error) {
	out := page{Events: []bridge.Event{}, Cursor: cursor, State: a.pulse()}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stream == nil {
		return out, bridge.Fail("unavailable")
	}
	events, err := a.stream.Replay(cursor)
	if err != nil {
		return out, err
	}
	// Each update carries its whole message or tool output, so one a later event in this stretch
	// replaces is not sent: a follower that polls less often receives less, never older.
	type kind struct {
		Type string `json:"type"`
		Tool string `json:"toolCallId"`
	}
	kinds := make([]kind, len(events))
	for i, e := range events {
		_ = json.Unmarshal(e.Data, &kinds[i])
	}
	replaced := func(i int) bool {
		for _, later := range kinds[i+1:] {
			if kinds[i].Type == "message_update" && (later.Type == "message_update" || later.Type == "message_end") ||
				kinds[i].Type == "tool_execution_update" && later.Tool == kinds[i].Tool && strings.HasPrefix(later.Type, "tool_execution_") {
				return true
			}
		}
		return false
	}
	size := 0
	for i, e := range events {
		if len(out.Events) >= limit || size+len(e.Data) > protocol.MaxFrame/2 {
			break
		}
		out.Cursor = e.Cursor
		if replaced(i) {
			continue
		}
		out.Events = append(out.Events, e)
		size += len(e.Data)
	}
	if len(events) > 0 && len(out.Events) == 0 {
		return out, bridge.Fail("cursor_expired")
	}
	return out, nil
}

// waitLimit keeps a long poll under the 20 s a peer gives any call.
const waitLimit = 15 * time.Second

// await answers once the stream has events after cursor, the pulse moved from state (a question
// asked, a turn begun, a rename), or waitLimit passed: a follower needs no timer of its own.
func (a *Attachment) await(ctx context.Context, cursor, state string, limit int) (json.RawMessage, error) {
	deadline := time.Now().Add(waitLimit)
	for {
		out, err := a.replay(cursor, limit)
		if err != nil || len(out.Events) > 0 || out.State != state || time.Now().After(deadline) {
			return bridge.JSON(out), err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (a *Attachment) pulse() string {
	s := a.host.Session()
	if s == nil {
		return ""
	}
	t, state, input, name := a.control.Target(), s.State(), "", ""
	if in := s.PendingInput(); in != nil {
		input = in.ID
	}
	if n := s.Manager().GetSessionName(); n != nil {
		name = *n
	}
	model, limits := "", ""
	if state.Model != nil {
		model = string(state.Model.Provider) + "/" + state.Model.ID
	}
	if a.options.Usage != nil {
		if u := a.options.Usage(s); u != nil {
			limits = u.CheckedAt.String()
		}
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{t.SessionID, t.Revision, t.ExecutionID, input, name, model, string(state.ThinkingLevel), limits}, "\x00")))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

func positivePage(n int) int {
	if n <= 0 {
		return protocol.MaxPage
	}
	return min(n, protocol.MaxPage)
}

func storedSessions(ctx context.Context, manager *session.SessionManager) ([]session.SessionInfo, error) {
	repo := manager.HarnessRepo()
	if repo == nil {
		return session.ListContext(ctx, manager.GetCWD(), manager.GetSessionDir(), nil)
	}
	if lister, ok := repo.(interface {
		ListInfo(context.Context, string, session.SessionListUpdateFunc) ([]session.SessionInfo, error)
	}); ok {
		return lister.ListInfo(ctx, manager.GetCWD(), nil)
	}
	rows, err := repo.List(ctx, harness.SessionListOptions{CWD: manager.GetCWD()})
	if err != nil {
		return nil, err
	}
	result := make([]session.SessionInfo, 0, len(rows))
	for _, row := range rows {
		result = append(result, session.SessionInfo{ID: row.ID, Path: row.Path, CWD: row.CWD})
	}
	return result, nil
}
