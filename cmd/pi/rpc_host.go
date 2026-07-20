package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/modes"
	"github.com/OrdalieTech/pi-go/internal/jsonwire"
)

type rpcSessionHost struct {
	ctx     context.Context
	mu      sync.RWMutex
	runtime *codingagent.AgentSessionRuntime
}

func rpcSlashCommands(runtime *codingagent.SessionRuntime) []modes.RPCSlashCommand {
	if runtime == nil {
		return []modes.RPCSlashCommand{}
	}
	commands := runtime.Commands()
	result := make([]modes.RPCSlashCommand, 0, len(commands))
	for _, command := range commands {
		var description, baseDir *string
		if command.Description != "" {
			description = stringValue(command.Description)
		}
		if command.SourceInfo.BaseDir != "" {
			baseDir = stringValue(command.SourceInfo.BaseDir)
		}
		result = append(result, modes.RPCSlashCommand{
			Name: command.Name, Description: description, Source: string(command.Source),
			SourceInfo: modes.RPCSourceInfo{
				Path: command.SourceInfo.Path, Source: command.SourceInfo.Source,
				Scope: command.SourceInfo.Scope, Origin: command.SourceInfo.Origin, BaseDir: baseDir,
			},
		})
	}
	return result
}

// newRPCSessionHost builds the RPC host. By default it binds the session's
// extensions at construction (emitting the initial session_start). Pass
// deferInitialBind when RunRPCMode will bind extensions itself after installing
// the RPC extension UI, so session_start fires once, with a live ctx.ui rather
// than the headless noop (the session must also be created with
// DeferSessionStart so construction does not fire session_start first).
func newRPCSessionHost(ctx context.Context, runtime *codingagent.AgentSessionRuntime, deferInitialBind ...bool) (*rpcSessionHost, error) {
	if runtime == nil || runtime.Session() == nil {
		return nil, errors.New("RPC session host requires an agent session runtime")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	host := &rpcSessionHost{ctx: ctx, runtime: runtime}
	runtime.SetRebindSession(func(replacement *codingagent.AgentSession) error {
		return replacement.BindExtensions(ctx)
	})
	if len(deferInitialBind) > 0 && deferInitialBind[0] {
		return host, nil
	}
	if err := runtime.Session().BindExtensions(ctx); err != nil {
		return nil, err
	}
	return host, nil
}

func (host *rpcSessionHost) Session() *codingagent.SessionRuntime {
	runtime := host.current()
	if runtime == nil {
		return nil
	}
	return runtime.Session()
}

func (host *rpcSessionHost) SetRebindSession(rebind func(*codingagent.SessionRuntime) error) {
	runtime := host.current()
	if runtime == nil {
		return
	}
	runtime.SetRebindSession(func(replacement *codingagent.AgentSession) error {
		if rebind == nil {
			return replacement.BindExtensions(host.ctx)
		}
		return rebind(replacement)
	})
}

func (host *rpcSessionHost) NewSession(parentSession string) (bool, error) {
	runtime := host.current()
	if runtime == nil {
		return false, errors.New("RPC session host is disposed")
	}
	result, err := runtime.NewSession(host.ctx, &extensions.NewSessionOptions{ParentSession: parentSession})
	return result.Cancelled, err
}

func (host *rpcSessionHost) SwitchSession(sessionPath string) (bool, error) {
	runtime := host.current()
	if runtime == nil {
		return false, errors.New("RPC session host is disposed")
	}
	result, err := runtime.SwitchSession(host.ctx, sessionPath, nil)
	return result.Cancelled, err
}

func (host *rpcSessionHost) Fork(entryID string, atEntry bool) (string, bool, error) {
	runtime := host.current()
	if runtime == nil {
		return "", false, errors.New("RPC session host is disposed")
	}
	position := extensions.ForkBefore
	if atEntry {
		position = extensions.ForkAt
	}
	result, err := runtime.Fork(host.ctx, entryID, &extensions.ForkOptions{Position: position})
	text := ""
	if result.SelectedText != nil {
		text = *result.SelectedText
	}
	return text, result.Cancelled, err
}

func (host *rpcSessionHost) Dispose() {
	if host == nil {
		return
	}
	host.mu.Lock()
	runtime := host.runtime
	host.runtime = nil
	host.mu.Unlock()
	if runtime != nil {
		runtime.Dispose(host.ctx)
	}
}

func (host *rpcSessionHost) current() *codingagent.AgentSessionRuntime {
	if host == nil {
		return nil
	}
	host.mu.RLock()
	defer host.mu.RUnlock()
	return host.runtime
}

func rpcMessageRoleAndText(raw json.RawMessage) (string, string) {
	return jsonwire.MessageRoleAndText(raw)
}
