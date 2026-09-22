package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/storage/sqlite"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
	"github.com/OrdalieTech/orb/plugins/questions"
	"github.com/OrdalieTech/orb/tui"
)

type remoteTranscript struct {
	mu   sync.Mutex
	text string
}

func (v *remoteTranscript) Render(width int) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return tui.NewText(v.text, 0, 0, nil).Render(width)
}
func (v *remoteTranscript) set(s string) {
	v.mu.Lock()
	v.text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, tui.StripANSI(s))
	v.mu.Unlock()
}
func remoteMessage(raw json.RawMessage) string {
	var m struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Role == "system" {
		return ""
	}
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &content) != nil {
		var text string
		_ = json.Unmarshal(m.Content, &text)
		return m.Role + ": " + text + "\n"
	}
	var out strings.Builder
	for _, block := range content {
		if block.Type == "text" {
			out.WriteString(block.Text)
		}
	}
	if out.Len() == 0 {
		return ""
	}
	return m.Role + ": " + out.String() + "\n\n"
}

type remoteDescriptor struct {
	Model      string              `json:"model,omitempty"`
	Models     []connect.Model     `json:"models,omitempty"`
	Input      *agent.InputRequest `json:"input,omitempty"`
	Name       string              `json:"name"`
	CWD        string              `json:"cwd"`
	Target     agent.ControlTarget `json:"target"`
	Generation string              `json:"registration_generation"`
	Methods    []string            `json:"methods"`
}

type remoteRequest struct{ text, inputID string }

type remoteConversation struct {
	promptMu     sync.Mutex
	prompt       *questions.Panel
	promptID     string
	body, status *remoteTranscript
	input        *tui.Input
	cancel       context.CancelFunc
	height       func() int
	offset       int
	controlsTop  int
	invalidate   func()
}

func (v *remoteConversation) Render(width int) []string {
	lines := v.body.Render(width)
	controls := remoteControls{v}.Render(width)
	available := max(1, v.height()-len(controls))
	if len(lines) > available {
		v.offset = min(v.offset, len(lines)-available)
		end := len(lines) - v.offset
		lines = lines[max(0, end-available):end]
	}
	v.controlsTop = len(lines)
	return append(lines, controls...)
}

type remoteControls struct{ v *remoteConversation }

func (c remoteControls) Render(width int) []string {
	c.v.promptMu.Lock()
	prompt := c.v.prompt
	c.v.promptMu.Unlock()
	if prompt != nil {
		return prompt.Render(width)
	}
	return append(c.v.status.Render(width), c.v.input.Render(width)...)
}
func (v *remoteConversation) HandleInput(key tui.KeyEvent) {
	if tui.MatchesKey(key.Raw, "pageup") {
		v.offset += max(1, v.height()-5)
		v.invalidate()
		return
	}
	if tui.MatchesKey(key.Raw, "pagedown") {
		v.offset = max(0, v.offset-max(1, v.height()-5))
		v.invalidate()
		return
	}
	v.promptMu.Lock()
	prompt := v.prompt
	v.promptMu.Unlock()
	if prompt != nil {
		prompt.HandleInput(key)
		return
	}
	v.input.HandleInput(key)
}
func (c remoteControls) WantsMouseMotion() bool { return true }
func (c remoteControls) HandleMouse(event tui.MouseEvent) bool {
	c.v.promptMu.Lock()
	prompt := c.v.prompt
	c.v.promptMu.Unlock()
	if prompt != nil {
		return prompt.HandleMouse(event)
	}
	return false
}
func (v *remoteConversation) WantsMouseMotion() bool { return true }
func (v *remoteConversation) HandleMouse(event tui.MouseEvent) bool {
	if event.Row >= v.controlsTop {
		event.Row -= v.controlsTop
		return (remoteControls{v}).HandleMouse(event)
	}
	switch event.Type {
	case tui.MouseWheelUp:
		v.offset += 3
	case tui.MouseWheelDown:
		v.offset = max(0, v.offset-3)
	default:
		return false
	}
	v.invalidate()
	return true
}
func (v *remoteConversation) SetFocused(f bool) {
	v.promptMu.Lock()
	prompt := v.prompt
	v.promptMu.Unlock()
	if prompt != nil {
		prompt.SetFocused(f)
	} else {
		v.input.SetFocused(f)
	}
}
func (v *remoteConversation) Dispose() { v.cancel() }

func newRemoteConversation(parent context.Context, profile, peer, instance string, invalidate func(), height func() int, done func(), theme extensions.Theme, initial ...sqlite.ForeignSession) *remoteConversation {
	ctx, cancel := context.WithCancel(parent)
	v := &remoteConversation{body: &remoteTranscript{}, status: &remoteTranscript{}, input: tui.NewInput(), cancel: cancel, height: height, invalidate: invalidate}
	requests := make(chan remoteRequest, 1)
	v.status.set("Connecting · Esc closes view")
	v.input.OnSubmit = func(text string) {
		select {
		case requests <- remoteRequest{text: text}:
			v.input.SetValue("")
		default:
			v.status.set("A command is still pending.")
		}
		invalidate()
	}
	v.input.OnEscape = func() { cancel(); done() }
	go func() {
		var admin *protocol.Conn
		defer func() {
			if admin != nil {
				_ = admin.Close()
			}
		}()
		remote := func(method string, p any, result any) error {
			callCtx, stop := context.WithTimeout(ctx, 20*time.Second)
			defer stop()
			if admin != nil {
				select {
				case <-admin.Done():
					admin = nil
				default:
				}
			}
			var err error
			if admin == nil {
				admin, err = bridgeAdmin(callCtx, profile)
				if err != nil {
					return err
				}
			}
			return admin.Call(callCtx, "remote", map[string]any{"peer_id": peer, "method": method, "params": p}, result)
		}
		db, cacheErr := openBridgeCache(ctx, profile)
		var cache *sqlite.Foreign
		if cacheErr == nil {
			defer func() { _ = db.Close() }()
			cache = db.Foreign(profile)
		}
		var saved *sqlite.ForeignSession
		if len(initial) > 0 {
			saved = &initial[0]
		} else if cache != nil {
			rows, err := cache.List(ctx, peer)
			if err == nil {
				for _, row := range rows {
					if row.Instance == instance {
						saved = &row
						break
					}
				}
			}
		}
		if saved != nil {
			v.body.set(cachedTranscript(*saved))
			v.status.set("Cached preview · stale · read-only · reconnecting")
			invalidate()
		}
		expected := ""
		if len(initial) > 0 {
			expected = initial[0].ID
		}
		runRemoteConversation(ctx, instance, remote, requests, v.body, v.status, invalidate, cache, peer, expected, func(input *agent.InputRequest) {
			v.promptMu.Lock()
			defer v.promptMu.Unlock()
			if input == nil || input.Presentation == nil || input.Presentation.Kind != questions.Kind {
				v.prompt, v.promptID = nil, ""
				return
			}
			if input.ID == v.promptID {
				return
			}
			var request questions.Request
			if json.Unmarshal(input.Presentation.Data, &request) != nil || request.Validate() != nil {
				return
			}
			v.promptID = input.ID
			id := input.ID
			v.prompt = questions.NewPanel(request, theme, height, invalidate, func(result questions.Result) {
				encoded, _ := json.Marshal(result)
				go func() {
					select {
					case requests <- remoteRequest{text: "/reply " + string(encoded), inputID: id}:
					case <-ctx.Done():
					}
				}()
			})
		})
	}()
	return v
}

func openBridgeView(ctx context.Context, ui extensions.UI, profile, peer, instance string, initial ...sqlite.ForeignSession) error {
	_, _, err := ui.Custom(ctx, func(host extensions.UIHost, theme extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		return newRemoteConversation(ctx, profile, peer, instance, host.Invalidate, host.Height, func() { done(nil) }, theme, initial...), nil
	}, nil)
	return err
}

func runBridgeView(parent context.Context, profile, peer, instance string, streams cliStreams) int {
	if !streams.StdinTTY || !streams.StdoutTTY {
		return reportCLIError(streams.Stderr, fmt.Errorf("remote view requires a terminal; use orb bridge remote for scripted calls"))
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	ui := tui.NewTUI(tui.NewProcessTerminal())
	v := newRemoteConversation(ctx, profile, peer, instance, ui.RequestRender, func() int { return ui.Terminal().Rows() }, cancel, extensions.NewNoopUI().Theme())
	defer v.Dispose()
	chrome := &tui.Container{}
	chrome.AddChild(remoteControls{v})
	ui.SetViewport(v.body, chrome)
	ui.SetFocus(v)
	ui.AddInputListener(func(data string) tui.InputListenerResult {
		if data == "\x03" {
			cancel()
			return tui.InputListenerResult{Consume: true}
		}
		return tui.InputListenerResult{}
	})
	if err := ui.Start(); err != nil {
		return reportCLIError(streams.Stderr, err)
	}
	defer func() { _ = ui.Stop() }()
	<-ctx.Done()
	return 0
}

func runRemoteConversation(ctx context.Context, instance string, remote func(string, any, any) error, requests <-chan remoteRequest, body, status *remoteTranscript, invalidate func(), cache *sqlite.Foreign, peer, expected string, showInput ...func(*agent.InputRequest)) {
	updateInput := func(input *agent.InputRequest) {
		if len(showInput) > 0 {
			showInput[0](input)
		}
	}
	var info remoteDescriptor
	var err error
	cursor := ""
	partial := ""
	var transcript strings.Builder
	pending := ""
	notice := ""
	connected := false
	revoked := false
	var preview sqlite.ForeignSession
	var ticket int64
	cacheWarning := ""
	if cache == nil {
		cacheWarning = " · offline cache unavailable"
	}
	deny := func(err error) bool {
		if connect.Code(err) != "unauthorized" {
			return false
		}
		connected = false
		updateInput(nil)
		body.set("")
		transcript.Reset()
		partial = ""
		cursor = ""
		message := "Access revoked · cached preview removed"
		if cache != nil && !revoked {
			if e := cache.Forget(ctx, peer); e != nil {
				message = "Access revoked · cache removal failed: " + e.Error()
			} else {
				revoked = true
			}
		}
		status.set(message)
		invalidate()
		return true
	}
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-requests:
			text := request.text
			notice = ""
			if !connected {
				status.set("Read-only · waiting for the remote session")
				invalidate()
				continue
			}
			if text == "/models" {
				var lines []string
				for _, model := range info.Models {
					lines = append(lines, model.Provider+"/"+model.ID+" · "+model.Name+" · effort: "+strings.Join(model.Thinking, ", "))
				}
				notice = strings.Join(lines, "\n") + "\n/model <provider/id> [effort]"
				status.set(notice)
				invalidate()
				continue
			}
			if text == "/sessions" {
				var result json.RawMessage
				err = remote("instances.call", connect.Call{InstanceID: instance, Service: protocol.Service, Method: "session.list", Args: connect.JSON(struct{}{})}, &result)
				if err != nil {
					status.set(err.Error())
				} else {
					status.set(string(result))
				}
				invalidate()
				continue
			}
			method := "prompt"
			arguments := map[string]string{"text": text}
			switch {
			case strings.HasPrefix(text, "/model "):
				parts := strings.Fields(strings.TrimPrefix(text, "/model "))
				if len(parts) < 1 || len(parts) > 2 {
					status.set("Use /model <provider/id> [effort]")
					invalidate()
					continue
				}
				provider, id, ok := strings.Cut(parts[0], "/")
				if !ok {
					status.set("Use /models to choose a model.")
					invalidate()
					continue
				}
				method = "session.model"
				arguments = map[string]string{"provider": provider, "model": id}
				if len(parts) == 2 {
					arguments["thinking"] = parts[1]
				}
			case strings.HasPrefix(text, "/reply "):
				if info.Input == nil || (request.inputID != "" && request.inputID != info.Input.ID) {
					status.set("No question is waiting for a reply.")
					invalidate()
					continue
				}
				method = "input.reply"
				arguments = map[string]string{"execution_id": info.Target.ExecutionID, "id": info.Input.ID, "value": strings.TrimPrefix(text, "/reply ")}
			case text == "/new":
				method = "session.new"
				arguments = map[string]string{}
			case text == "/cancel":
				method = "cancel"
				arguments = map[string]string{"execution_id": info.Target.ExecutionID}
			case strings.HasPrefix(text, "/steer "):
				method = "steer"
				arguments = map[string]string{"execution_id": info.Target.ExecutionID, "text": strings.TrimPrefix(text, "/steer ")}
			case strings.HasPrefix(text, "/follow "):
				method = "follow_up"
				arguments = map[string]string{"execution_id": info.Target.ExecutionID, "text": strings.TrimPrefix(text, "/follow ")}
			case strings.HasPrefix(text, "/switch "):
				method = "session.switch"
				arguments = map[string]string{"session_id": strings.TrimPrefix(text, "/switch ")}
			case strings.HasPrefix(text, "/fork "):
				method = "session.fork"
				arguments = map[string]string{"entry_id": strings.TrimPrefix(text, "/fork ")}
			}
			if !slices.Contains(info.Methods, method) {
				status.set("This control is not permitted.")
				invalidate()
				continue
			}
			call := connect.Call{InstanceID: instance, Service: protocol.Service, Method: method, SessionID: info.Target.SessionID, Expected: connect.Expected{Generation: info.Generation, Revision: info.Target.Revision}, OperationID: protocol.NewID(), Args: connect.JSON(arguments)}
			var receipt connect.Receipt
			if err = remote("instances.call", call, &receipt); err != nil {
				if request.inputID != "" {
					updateInput(nil)
				}
				if !deny(err) {
					status.set(err.Error() + " · operation " + call.OperationID)
				}
			} else {
				pending = receipt.OperationID
				status.set(receipt.Status + " · " + pending)
			}
			invalidate()
		case <-tick.C:
			previous := info.Target.SessionID
			var next remoteDescriptor
			if err = remote("instances.describe", map[string]string{"instance_id": instance}, &next); err != nil {
				connected = false
				updateInput(nil)
				if !deny(err) {
					status.set("Offline · cached content is stale · read-only · reconnecting" + cacheWarning)
				}
				invalidate()
				continue
			}
			info = next
			if expected != "" && expected != info.Target.SessionID {
				connected = false
				updateInput(nil)
				status.set("Cached preview · read-only · this session is no longer active remotely")
				invalidate()
				continue
			}
			connected = false
			if previous != info.Target.SessionID {
				cursor = ""
				transcript.Reset()
				partial = ""
			}
			if pending != "" {
				var receipt connect.Receipt
				if remote("operations.get", map[string]string{"instance_id": instance, "operation_id": pending}, &receipt) == nil {
					status.set(receipt.Status + " · " + receipt.Error + " · Esc closes view")
				}
			}
			cacheDirty := cursor == ""
			if cursor == "" {
				if cache != nil {
					ticket, err = cache.Begin(ctx, peer)
					if err != nil {
						cacheWarning = " · cache write failed"
					}
				}
				snapshot, offset := "", ""
				preview = sqlite.ForeignSession{Peer: peer, Namespace: protocol.Service + "/" + instance, ID: info.Target.SessionID, Instance: instance, Name: info.Name, CWD: info.CWD}
				transcript.Reset()
				for pages := 0; ; pages++ {
					if pages >= 128 {
						err = connect.Fail("resource_exhausted")
						break
					}
					var page struct {
						Partial    json.RawMessage   `json:"partial"`
						SnapshotID string            `json:"snapshot_id"`
						Messages   []json.RawMessage `json:"messages"`
						Cursor     string            `json:"cursor"`
						Offset     string            `json:"offset"`
					}
					err = remote("events.subscribe", map[string]string{"instance_id": instance, "snapshot_id": snapshot, "offset": offset}, &page)
					if err != nil {
						break
					}
					for _, m := range page.Messages {
						transcript.WriteString(remoteMessage(m))
						preview.AddMessage(m)
					}
					if transcript.Len() > 8<<20 || page.Offset != "" && page.Offset == offset {
						err = connect.Fail("resource_exhausted")
						break
					}
					partial = remoteMessage(page.Partial)
					snapshot, offset = page.SnapshotID, page.Offset
					if offset == "" {
						cursor = page.Cursor
						_ = remote("events.unsubscribe", map[string]string{"instance_id": instance, "snapshot_id": snapshot}, nil)
						break
					}
				}
			} else {
				var page struct {
					Events []connect.Event `json:"events"`
					Cursor string          `json:"cursor"`
				}
				err = remote("events.subscribe", map[string]string{"instance_id": instance, "cursor": cursor}, &page)
				if err != nil {
					cursor = ""
				} else {
					for _, e := range page.Events {
						var event struct {
							Type    string          `json:"type"`
							Message json.RawMessage `json:"message"`
						}
						if json.Unmarshal(e.Data, &event) == nil {
							switch event.Type {
							case "message_start", "message_update":
								partial = remoteMessage(event.Message)
							case "message_end":
								partial = ""
								transcript.WriteString(remoteMessage(event.Message))
								preview.AddMessage(event.Message)
								cacheDirty = true
							case "agent_end":
								partial = ""
							}
						}
					}
					cursor = page.Cursor
				}
			}
			if err != nil {
				updateInput(nil)
				cursor = ""
				if !deny(err) {
					status.set("Remote data unavailable · read-only · retrying")
					invalidate()
				}
				continue
			}
			// A session transition during snapshot paging must never label the
			// new conversation with the previous session's identity.
			var current remoteDescriptor
			if err = remote("instances.describe", map[string]string{"instance_id": instance}, &current); err != nil || current.Target.SessionID != info.Target.SessionID || current.Target.Revision != info.Target.Revision || current.Generation != info.Generation {
				cursor = ""
				updateInput(nil)
				if !deny(err) {
					status.set("Session changed or disconnected · refreshing")
				}
				invalidate()
				continue
			}
			connected = true
			revoked = false
			cacheDirty = cacheDirty || preview.Name != current.Name || preview.CWD != current.CWD
			preview.Name = current.Name
			preview.CWD = current.CWD
			if cache != nil && ticket > 0 && cacheDirty {
				if err := cache.Put(ctx, ticket, preview); err != nil {
					if errors.Is(err, sqlite.ErrForeignSuperseded) {
						cursor = ""
					} else {
						cacheWarning = " · cache write failed"
					}
				} else {
					cacheWarning = ""
				}
			}
			if transcript.Len() > 8<<20 {
				cursor = ""
				transcript.Reset()
			}
			body.set(transcript.String() + partial)
			if pending == "" {
				state := "idle"
				if info.Target.ExecutionID != "" {
					state = "running"
				}
				if info.Model != "" {
					state = info.Model + " · " + state
				}
				status.set(state + cacheWarning + " · " + strings.Join(info.Methods, " · ") + " · Esc closes view")
			}
			if notice != "" {
				status.set(notice)
			}
			if slices.Contains(info.Methods, "input.reply") {
				updateInput(info.Input)
			} else {
				updateInput(nil)
			}
			if info.Input != nil {
				status.set(info.Input.Title + "\nReply with /reply " + strings.Join(info.Input.Choices, " | ") + " · /cancel stops execution")
			}
			invalidate()
		}
	}
}

func openBridgeCache(ctx context.Context, profile string) (*sqlite.DB, error) {
	if state := stateFromContext(ctx); state != nil {
		path, err := nativeStatePath(state.agentDir)
		if err != nil {
			return nil, err
		}
		return sqlite.Open(ctx, path)
	}
	dir, err := bridgeDir(profile)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(filepath.Dir(dir))
	if override := os.Getenv("ORB_BRIDGE_HOME"); override != "" {
		root = override
	}
	return sqlite.Open(ctx, filepath.Join(root, "state", "orb.db"))
}
func cachedTranscript(s sqlite.ForeignSession) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Cached preview · %s · %s\nLast refreshed %s\n\n", s.Peer, s.ID, s.RefreshedAt.Format(time.RFC3339))
	for _, m := range s.Messages {
		fmt.Fprintf(&b, "%s: %s\n\n", m.Role, m.Text)
	}
	return b.String()
}
