//go:build js && wasm

// orb-wasm translates worker messages into calls on the Wasm assembly.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"syscall/js"

	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/platforms/wasm"
)

func post(value any) {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	js.Global().Call("postMessage", string(data))
}

func snapshot(s *wasm.Session, kind string, err error) {
	state := s.Agent.State()
	message := state.ErrorMessage
	if err != nil {
		text := err.Error()
		message = &text
	}
	post(map[string]any{"type": kind, "messages": state.Messages, "files": s.Workspace.Snapshot(), "error": message})
}

func main() {
	var session *wasm.Session
	var cancel context.CancelFunc
	running := false
	remote := &browserBridge{slots: make(chan struct{}, 16)}
	dispatch := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 1 || args[0].Type() != js.TypeString {
			return "expected one JSON request"
		}
		raw := args[0].String()
		if len(raw) > 65536 {
			return "request exceeds 64 KiB"
		}
		var request struct {
			Type       string      `json:"type"`
			Config     wasm.Config `json:"config"`
			Text       string      `json:"text"`
			AgentCalls bool        `json:"agentCalls"`
		}
		if err := json.Unmarshal([]byte(raw), &request); err != nil {
			return err.Error()
		}
		if strings.HasPrefix(request.Type, "bridge.") {
			return remote.dispatch(raw)
		}
		switch request.Type {
		case "start":
			if running {
				return "cancel the current turn before replacing the session"
			}
			next, err := wasm.New(request.Config)
			if err != nil {
				return err.Error()
			}
			next.Agent.Subscribe(func(_ context.Context, event engine.AgentEvent) error {
				post(map[string]any{"type": "event", "event": event})
				return nil
			})
			session = next
			snapshot(session, "ready", nil)
		case "prompt":
			if session == nil {
				return "start a session first"
			}
			if running {
				return "a turn is already running"
			}
			if strings.TrimSpace(request.Text) == "" || len(request.Text) > 16384 {
				return "prompt must contain 1–16384 bytes"
			}
			tools := session.Agent.State().Tools
			base := make([]engine.AgentTool, 0, len(tools)+1)
			for _, tool := range tools {
				if tool.Spec().Name != "bridge_call" {
					base = append(base, tool)
				}
			}
			if request.AgentCalls {
				tool, err := remote.tool()
				if err != nil {
					return err.Error()
				}
				base = append(base, tool)
			}
			session.Agent.SetTools(base)
			running = true
			ctx, stop := context.WithCancel(context.Background())
			cancel = stop
			current := session
			go func() {
				defer stop()
				err := current.Agent.Prompt(ctx, request.Text)
				running, cancel = false, nil
				snapshot(current, "settled", err)
			}()
		case "cancel":
			if cancel != nil {
				cancel()
			}
		default:
			return fmt.Sprintf("unknown request %q", request.Type)
		}
		return ""
	})
	js.Global().Set("orbDispatch", dispatch)
	post(map[string]string{"type": "boot"})
	select {}
}
