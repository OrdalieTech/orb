package worker

import (
	"context"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
)

// ToolsFunc supplies extra tools for each session the object starts, decided
// from the object's settings (a capability's plugin switch, for example).
type ToolsFunc func(*config.SettingsManager) []extensions.ToolDefinition

// EnableControl admits non-owning controllers (a Bridge attachment) to the
// object's sessions; every replacement is then bracketed as a transition.
func (instance *Instance) EnableControl() (*agent.SessionControl, error) {
	instance.mu.Lock()
	control := instance.control
	instance.mu.Unlock()
	if control != nil {
		return control, nil
	}
	control, err := agent.NewSessionControl(instance.Session)
	if err != nil {
		return nil, err
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.control == nil {
		instance.control = control
	}
	return instance.control, nil
}

// ObserveSessions calls observe with the current session and after every
// replacement, until the returned function is called.
func (instance *Instance) ObserveSessions(observe func(*agent.AgentSession)) func() {
	instance.mu.Lock()
	instance.nextObserver++
	id := instance.nextObserver
	if instance.observers == nil {
		instance.observers = map[uint64]func(*agent.AgentSession){}
	}
	instance.observers[id] = observe
	current := instance.session
	instance.mu.Unlock()
	if current != nil {
		observe(current)
	}
	return func() {
		instance.mu.Lock()
		delete(instance.observers, id)
		instance.mu.Unlock()
	}
}

func (instance *Instance) beginTransition(ctx context.Context) (func(), error) {
	instance.mu.Lock()
	control := instance.control
	instance.mu.Unlock()
	if control == nil {
		return func() {}, nil
	}
	return control.BeginTransition(ctx)
}

func (instance *Instance) notify(session *agent.AgentSession) {
	instance.mu.Lock()
	observers := make([]func(*agent.AgentSession), 0, len(instance.observers))
	for _, observe := range instance.observers {
		observers = append(observers, observe)
	}
	instance.mu.Unlock()
	for _, observe := range observers {
		observe(session)
	}
}

// settings reads the object's settings.json through the Store port, as the
// session itself does.
func (instance *Instance) settings() (*config.SettingsManager, error) {
	return config.NewSettingsManager(Workspace, config.WithAgentDir(AgentDir),
		config.WithGlobalDocument(instance.Host.Document("settings.json")), config.WithProjectTrusted(false))
}
