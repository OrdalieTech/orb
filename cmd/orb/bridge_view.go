package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/OrdalieTech/orb/agent/extensions"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
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
	Target     agent.ControlTarget `json:"target"`
	Generation string              `json:"registration_generation"`
	Methods    []string            `json:"methods"`
}

type remoteConversation struct {
	body, status *remoteTranscript
	input        *tui.Input
	cancel       context.CancelFunc
	height       func() int
	offset       int
	invalidate   func()
}

func (v *remoteConversation) Render(width int) []string {
	lines := v.body.Render(width)
	available := max(1, v.height()-5)
	if len(lines) > available {
		v.offset = min(v.offset, len(lines)-available)
		end := len(lines) - v.offset
		lines = lines[max(0, end-available):end]
	}
	lines = append(lines, v.status.Render(width)...)
	return append(lines, v.input.Render(width)...)
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
	v.input.HandleInput(key)
}
func (v *remoteConversation) SetFocused(f bool) { v.input.SetFocused(f) }
func (v *remoteConversation) Dispose()          { v.cancel() }

func newRemoteConversation(parent context.Context, profile, peer, instance string, invalidate func(), height func() int, done func()) *remoteConversation {
	ctx, cancel := context.WithCancel(parent)
	v := &remoteConversation{body: &remoteTranscript{}, status: &remoteTranscript{}, input: tui.NewInput(), cancel: cancel, height: height, invalidate: invalidate}
	requests := make(chan string, 1)
	v.status.set("Connecting · Esc closes view")
	v.input.OnSubmit = func(text string) {
		select {
		case requests <- text:
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
		runRemoteConversation(ctx, instance, remote, requests, v.body, v.status, invalidate)
	}()
	return v
}

func openBridgeView(ctx context.Context, ui extensions.UI, profile, peer, instance string) error {
	_, _, err := ui.Custom(ctx, func(host extensions.UIHost, _ extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		return newRemoteConversation(ctx, profile, peer, instance, host.Invalidate, host.Height, func() { done(nil) }), nil
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
	v := newRemoteConversation(ctx, profile, peer, instance, ui.RequestRender, func() int { return 24 }, cancel)
	defer v.Dispose()
	chrome := &tui.Container{}
	chrome.AddChild(v.status)
	chrome.AddChild(v.input)
	ui.SetViewport(v.body, chrome)
	ui.SetFocus(v.input)
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

func runRemoteConversation(ctx context.Context, instance string, remote func(string, any, any) error, requests <-chan string, body, status *remoteTranscript, invalidate func()) {
	var info remoteDescriptor
	var err error
	cursor := ""
	partial := ""
	var transcript strings.Builder
	pending := ""
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-requests:
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
				status.set(err.Error() + " · operation " + call.OperationID)
			} else {
				pending = receipt.OperationID
				status.set(receipt.Status + " · " + pending)
			}
			invalidate()
		case <-tick.C:
			previous := info.Target.SessionID
			if err = remote("instances.describe", map[string]string{"instance_id": instance}, &info); err != nil {
				status.set("Disconnected · reconnecting · Esc closes view")
				invalidate()
				continue
			}
			if previous != info.Target.SessionID {
				cursor = ""
				transcript.Reset()
			}
			if pending != "" {
				var receipt connect.Receipt
				if remote("operations.get", map[string]string{"instance_id": instance, "operation_id": pending}, &receipt) == nil {
					status.set(receipt.Status + " · " + receipt.Error + " · Esc closes view")
				}
			}
			if cursor == "" {
				snapshot, offset := "", ""
				transcript.Reset()
				for {
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
							case "agent_end":
								partial = ""
							}
						}
					}
					cursor = page.Cursor
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
				status.set(state + " · " + strings.Join(info.Methods, " · ") + " · Esc closes view")
			}
			invalidate()
		}
	}
}
