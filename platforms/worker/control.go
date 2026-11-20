package worker

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
)

// ToolsFunc supplies extra tools for each session the object starts, decided
// from the object's settings (a capability's plugin switch, for example).
type ToolsFunc func(*config.SettingsManager) []extensions.ToolDefinition

// EnableControl admits non-owning controllers (a Mitosis attachment) to the
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

// Settings reads the object's settings.json through the Store port, as the
// session itself does; what it writes there applies to sessions started after.
func (instance *Instance) Settings() (*config.SettingsManager, error) {
	return config.NewSettingsManager(Workspace, config.WithAgentDir(AgentDir),
		config.WithGlobalDocument(instance.Host.Document("settings.json")), config.WithProjectTrusted(false))
}

// DefaultModels is what a new session of this object can start on: its
// available models, the persisted default and that default's reasoning levels.
type DefaultModels struct {
	Models   []string `json:"models"`
	Default  string   `json:"default"`
	Thinking string   `json:"thinking,omitempty"`
	Levels   []string `json:"levels,omitempty"`
}

// DefaultModels lists the models a new session can use and the persisted default,
// matching `orb models --json` on native hosts.
func (instance *Instance) DefaultModels() (DefaultModels, error) {
	settings, err := instance.Settings()
	if err != nil {
		return DefaultModels{}, err
	}
	out := DefaultModels{Models: []string{}}
	if instance.registry == nil {
		return out, nil
	}
	for _, m := range instance.registry.Available(nil) {
		out.Models = append(out.Models, string(m.Provider)+"/"+m.ID)
	}
	if def := agent.DefaultModel(instance.registry, settings); def != nil {
		out.Default = string(def.Provider) + "/" + def.ID
		for _, l := range ai.SupportedThinkingLevels(def) {
			out.Levels = append(out.Levels, string(l))
		}
		level := cmp.Or(settings.GetModelThinkingLevel(string(def.Provider), def.ID), settings.GetDefaultThinkingLevel(), ai.ModelThinkingMedium)
		out.Thinking = string(ai.ClampThinkingLevel(def, level))
	}
	return out, nil
}

// SetDefaultModel persists the default model (and optional thinking level) in
// settings.json so new sessions start on it, matching `orb models use`.
func (instance *Instance) SetDefaultModel(ctx context.Context, provider, id, level string) error {
	settings, err := instance.Settings()
	if err != nil {
		return err
	}
	if instance.registry == nil {
		return errors.New("model registry unavailable")
	}
	// Refresh stored credentials (e.g. accounts synced via Mitosis) before checking auth.
	_ = instance.registry.Available(nil)
	m, ok := instance.registry.Find(provider, id)
	if !ok || !instance.registry.HasConfiguredAuth(provider, nil) {
		return errors.New("no signed-in model " + provider + "/" + id)
	}
	var thinking ai.ModelThinkingLevel
	if level != "" {
		thinking = ai.ModelThinkingLevel(level)
		if !slices.Contains(ai.SupportedThinkingLevels(&m), thinking) {
			return errors.New(provider + "/" + id + " has no reasoning level " + level)
		}
	}
	settings.SetDefaultModelAndProvider(provider, id)
	switch {
	case thinking == "":
	case settings.GetModelThinkingLevel(provider, id) != "":
		settings.SetModelThinkingLevel(provider, id, thinking)
	default:
		settings.SetDefaultThinkingLevel(thinking)
	}
	if errs := settings.DrainErrors(); len(errs) > 0 {
		return errs[0].Err
	}
	if session := instance.Session(); session != nil {
		var ptr *ai.ModelThinkingLevel
		if thinking != "" {
			ptr = &thinking
		}
		_ = session.SetModelAndThinking(ctx, m, ptr)
	}
	return nil
}
