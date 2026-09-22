package rpc

import (
	"context"
	"errors"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
)

// RuntimeHost is the SessionHost over an [agent.AgentSessionRuntime]: session
// replacements run through the runtime and the server rebinds after each one.
type RuntimeHost struct {
	ctx     context.Context
	mu      sync.RWMutex
	runtime *agent.AgentSessionRuntime
}

// NewRuntimeHost builds the host. Unless deferInitialBind is set it binds the
// session's extensions at construction (emitting the initial session_start).
// Set deferInitialBind when Serve will bind extensions itself after installing
// the RPC extension UI, so session_start fires once, with a live ctx.ui rather
// than the headless noop (the session must also be created with
// DeferSessionStart so construction does not fire session_start first).
func NewRuntimeHost(ctx context.Context, runtime *agent.AgentSessionRuntime, deferInitialBind bool) (*RuntimeHost, error) {
	if runtime == nil || runtime.Session() == nil {
		return nil, errors.New("RPC session host requires an agent session runtime")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	host := &RuntimeHost{ctx: ctx, runtime: runtime}
	runtime.SetRebindSession(func(replacement *agent.AgentSession) error {
		return replacement.BindExtensions(ctx)
	})
	if deferInitialBind {
		return host, nil
	}
	if err := runtime.Session().BindExtensions(ctx); err != nil {
		return nil, err
	}
	return host, nil
}

func (host *RuntimeHost) Session() *agent.SessionRuntime {
	runtime := host.current()
	if runtime == nil {
		return nil
	}
	return runtime.Session()
}

func (host *RuntimeHost) SetRebindSession(rebind func(*agent.SessionRuntime) error) {
	runtime := host.current()
	if runtime == nil {
		return
	}
	runtime.SetRebindSession(func(replacement *agent.AgentSession) error {
		if rebind == nil {
			return replacement.BindExtensions(host.ctx)
		}
		return rebind(replacement)
	})
}

func (host *RuntimeHost) NewSession(parentSession string) (bool, error) {
	runtime := host.current()
	if runtime == nil {
		return false, errors.New("RPC session host is disposed")
	}
	result, err := runtime.NewSession(host.ctx, &extensions.NewSessionOptions{ParentSession: parentSession})
	return result.Cancelled, err
}

func (host *RuntimeHost) SwitchSession(sessionPath string) (bool, error) {
	runtime := host.current()
	if runtime == nil {
		return false, errors.New("RPC session host is disposed")
	}
	result, err := runtime.SwitchSession(host.ctx, sessionPath, nil)
	return result.Cancelled, err
}

func (host *RuntimeHost) Fork(entryID string, atEntry bool) (string, bool, error) {
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

func (host *RuntimeHost) Dispose() {
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

func (host *RuntimeHost) current() *agent.AgentSessionRuntime {
	if host == nil {
		return nil
	}
	host.mu.RLock()
	defer host.mu.RUnlock()
	return host.runtime
}

// SlashCommands reports runtime's commands in the get_commands wire shape.
func SlashCommands(runtime *agent.SessionRuntime) []SlashCommand {
	if runtime == nil {
		return []SlashCommand{}
	}
	commands := runtime.Commands()
	result := make([]SlashCommand, 0, len(commands))
	for _, command := range commands {
		var description, baseDir *string
		if command.Description != "" {
			description = &command.Description
		}
		if command.SourceInfo.BaseDir != "" {
			baseDir = &command.SourceInfo.BaseDir
		}
		result = append(result, SlashCommand{
			Name: command.Name, Description: description, Source: string(command.Source),
			SourceInfo: SourceInfo{
				Path: command.SourceInfo.Path, Source: command.SourceInfo.Source,
				Scope: command.SourceInfo.Scope, Origin: command.SourceInfo.Origin, BaseDir: baseDir,
			},
		})
	}
	return result
}
