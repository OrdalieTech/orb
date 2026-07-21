package extensions

import (
	"context"
	"errors"
	"sync"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
)

const defaultStaleContextMessage = "This extension ctx is stale after session replacement or reload. Do not use a captured pi or command ctx after ctx.newSession(), ctx.fork(), ctx.switchSession(), or ctx.reload(). For newSession, fork, and switchSession, move post-replacement work into withSession and use the ctx passed to withSession. For reload, do not use the old ctx after await ctx.reload()."

type Actions struct {
	SendMessage            func(context.Context, CustomMessage, *SendMessageOptions) error
	SendUserMessage        func(context.Context, ai.UserContent, *SendUserMessageOptions) error
	AppendEntry            func(context.Context, string, any) error
	SetSessionName         func(context.Context, string) error
	GetSessionName         func(context.Context) (*string, error)
	SetLabel               func(context.Context, string, *string) error
	GetActiveTools         func() ([]string, error)
	GetAllTools            func() ([]ToolInfo, error)
	SetActiveTools         func([]string) error
	RefreshTools           func()
	GetCommands            func() ([]SlashCommandInfo, error)
	SetModel               func(context.Context, *ai.Model) (bool, error)
	GetThinkingLevel       func() (agent.ThinkingLevel, error)
	SetThinkingLevel       func(agent.ThinkingLevel) error
	RegisterProvider       func(Provider) error
	RegisterProviderConfig func(string, ProviderConfig) error
	UnregisterProvider     func(string) error
}

type pendingProviderRegistration struct {
	name          string
	native        *Provider
	config        *ProviderConfig
	extensionPath string
}

type runtimeState struct {
	mu               sync.RWMutex
	actions          Actions
	providerBound    bool
	registerNative   func(Provider) error
	registerConfig   func(string, ProviderConfig) error
	unregister       func(string) error
	staleMessage     string
	flags            map[string]any
	pendingProviders []pendingProviderRegistration
}

func newRuntimeState() *runtimeState {
	return &runtimeState{actions: uninitializedActions(), flags: make(map[string]any)}
}

func uninitializedActions() Actions {
	notInitialized := func() error {
		return errors.New(ErrRuntimeNotInitialized.Error() + ". Action methods cannot be called during extension loading.")
	}
	return Actions{
		SendMessage:            func(context.Context, CustomMessage, *SendMessageOptions) error { return notInitialized() },
		SendUserMessage:        func(context.Context, ai.UserContent, *SendUserMessageOptions) error { return notInitialized() },
		AppendEntry:            func(context.Context, string, any) error { return notInitialized() },
		SetSessionName:         func(context.Context, string) error { return notInitialized() },
		GetSessionName:         func(context.Context) (*string, error) { return nil, notInitialized() },
		SetLabel:               func(context.Context, string, *string) error { return notInitialized() },
		GetActiveTools:         func() ([]string, error) { return nil, notInitialized() },
		GetAllTools:            func() ([]ToolInfo, error) { return nil, notInitialized() },
		SetActiveTools:         func([]string) error { return notInitialized() },
		RefreshTools:           func() {},
		GetCommands:            func() ([]SlashCommandInfo, error) { return nil, notInitialized() },
		SetModel:               func(context.Context, *ai.Model) (bool, error) { return false, notInitialized() },
		GetThinkingLevel:       func() (agent.ThinkingLevel, error) { return "", notInitialized() },
		SetThinkingLevel:       func(agent.ThinkingLevel) error { return notInitialized() },
		RegisterProvider:       func(Provider) error { return notInitialized() },
		RegisterProviderConfig: func(string, ProviderConfig) error { return notInitialized() },
		UnregisterProvider:     func(string) error { return notInitialized() },
	}
}

func normalizeActions(actions Actions) Actions {
	defaults := uninitializedActions()
	if actions.RegisterProviderConfig == nil && actions.RegisterProvider != nil {
		register := actions.RegisterProvider
		actions.RegisterProviderConfig = func(name string, config ProviderConfig) error {
			return register(Provider{ID: name, Name: config.Name, Config: config})
		}
	}
	if actions.SendMessage == nil {
		actions.SendMessage = defaults.SendMessage
	}
	if actions.SendUserMessage == nil {
		actions.SendUserMessage = defaults.SendUserMessage
	}
	if actions.AppendEntry == nil {
		actions.AppendEntry = defaults.AppendEntry
	}
	if actions.SetSessionName == nil {
		actions.SetSessionName = defaults.SetSessionName
	}
	if actions.GetSessionName == nil {
		actions.GetSessionName = defaults.GetSessionName
	}
	if actions.SetLabel == nil {
		actions.SetLabel = defaults.SetLabel
	}
	if actions.GetActiveTools == nil {
		actions.GetActiveTools = defaults.GetActiveTools
	}
	if actions.GetAllTools == nil {
		actions.GetAllTools = defaults.GetAllTools
	}
	if actions.SetActiveTools == nil {
		actions.SetActiveTools = defaults.SetActiveTools
	}
	if actions.RefreshTools == nil {
		actions.RefreshTools = func() {}
	}
	if actions.GetCommands == nil {
		actions.GetCommands = defaults.GetCommands
	}
	if actions.SetModel == nil {
		actions.SetModel = defaults.SetModel
	}
	if actions.GetThinkingLevel == nil {
		actions.GetThinkingLevel = defaults.GetThinkingLevel
	}
	if actions.SetThinkingLevel == nil {
		actions.SetThinkingLevel = defaults.SetThinkingLevel
	}
	if actions.RegisterProvider == nil {
		actions.RegisterProvider = defaults.RegisterProvider
	}
	if actions.RegisterProviderConfig == nil {
		actions.RegisterProviderConfig = defaults.RegisterProviderConfig
	}
	if actions.UnregisterProvider == nil {
		actions.UnregisterProvider = defaults.UnregisterProvider
	}
	return actions
}

func (runtime *runtimeState) bindActions(actions Actions) Actions {
	actions = normalizeActions(actions)
	runtime.mu.Lock()
	runtime.actions = actions
	runtime.mu.Unlock()
	return actions
}

func (runtime *runtimeState) bindProviderActions(actions Actions, report func(ExtensionError)) {
	runtime.mu.Lock()
	runtime.registerNative = actions.RegisterProvider
	runtime.registerConfig = actions.RegisterProviderConfig
	runtime.unregister = actions.UnregisterProvider
	runtime.providerBound = true
	pending := append([]pendingProviderRegistration(nil), runtime.pendingProviders...)
	runtime.pendingProviders = nil
	runtime.mu.Unlock()
	flushProviderRegistrations(pending, actions.RegisterProvider, actions.RegisterProviderConfig, report)
}

func (runtime *runtimeState) bindProviders(registry ModelRegistry, report func(ExtensionError)) {
	if registry == nil {
		return
	}
	runtime.mu.Lock()
	runtime.registerNative = registry.RegisterProvider
	runtime.registerConfig = registry.RegisterProviderConfig
	runtime.unregister = registry.UnregisterProvider
	runtime.providerBound = true
	pending := append([]pendingProviderRegistration(nil), runtime.pendingProviders...)
	runtime.pendingProviders = nil
	runtime.mu.Unlock()
	flushProviderRegistrations(pending, registry.RegisterProvider, registry.RegisterProviderConfig, report)
}

func flushProviderRegistrations(
	pending []pendingProviderRegistration,
	registerNative func(Provider) error,
	registerConfig func(string, ProviderConfig) error,
	report func(ExtensionError),
) {
	for _, registration := range pending {
		if registration.config == nil {
			continue
		}
		err := registerConfig(registration.name, *registration.config)
		if err != nil && report != nil {
			report(ExtensionError{ExtensionPath: registration.extensionPath, Event: "register_provider", Error: err.Error()})
		}
	}
	for _, registration := range pending {
		if registration.native == nil {
			continue
		}
		err := registerNative(*registration.native)
		if err != nil && report != nil {
			report(ExtensionError{ExtensionPath: registration.extensionPath, Event: "register_provider", Error: err.Error()})
		}
	}
}

func (runtime *runtimeState) actionsSnapshot() Actions {
	runtime.mustBeActive()
	runtime.mu.RLock()
	actions := runtime.actions
	runtime.mu.RUnlock()
	return actions
}

func (runtime *runtimeState) refreshTools() {
	runtime.mustBeActive()
	runtime.mu.RLock()
	refresh := runtime.actions.RefreshTools
	runtime.mu.RUnlock()
	refresh()
}

func (runtime *runtimeState) setFlagDefault(name string, value any) {
	runtime.mu.Lock()
	if _, exists := runtime.flags[name]; !exists {
		runtime.flags[name] = value
	}
	runtime.mu.Unlock()
}

func (runtime *runtimeState) setFlag(name string, value any) {
	runtime.mustBeActive()
	runtime.mu.Lock()
	runtime.flags[name] = value
	runtime.mu.Unlock()
}

func (runtime *runtimeState) flag(name string) (any, bool) {
	runtime.mu.RLock()
	value, exists := runtime.flags[name]
	runtime.mu.RUnlock()
	return value, exists
}

func (runtime *runtimeState) flagValues() map[string]any {
	runtime.mu.RLock()
	values := make(map[string]any, len(runtime.flags))
	for name, value := range runtime.flags {
		values[name] = value
	}
	runtime.mu.RUnlock()
	return values
}

func (runtime *runtimeState) registerProvider(provider Provider, extensionPath string) {
	runtime.mu.Lock()
	if !runtime.providerBound {
		runtime.pendingProviders = append(runtime.pendingProviders, pendingProviderRegistration{name: provider.ID, native: &provider, extensionPath: extensionPath})
		runtime.mu.Unlock()
		return
	}
	register := runtime.registerNative
	runtime.mu.Unlock()
	if err := register(provider); err != nil {
		panic(err)
	}
}

func (runtime *runtimeState) registerProviderConfig(name string, config ProviderConfig, extensionPath string) {
	runtime.mu.Lock()
	if !runtime.providerBound {
		runtime.pendingProviders = append(runtime.pendingProviders, pendingProviderRegistration{name: name, config: &config, extensionPath: extensionPath})
		runtime.mu.Unlock()
		return
	}
	register := runtime.registerConfig
	runtime.mu.Unlock()
	if err := register(name, config); err != nil {
		panic(err)
	}
}

func (runtime *runtimeState) unregisterProvider(name, _ string) {
	runtime.mu.Lock()
	if !runtime.providerBound {
		filtered := runtime.pendingProviders[:0]
		for _, registration := range runtime.pendingProviders {
			if registration.name != name {
				filtered = append(filtered, registration)
			}
		}
		runtime.pendingProviders = filtered
		runtime.mu.Unlock()
		return
	}
	unregister := runtime.unregister
	runtime.mu.Unlock()
	if err := unregister(name); err != nil {
		panic(err)
	}
}

func (runtime *runtimeState) invalidate(message string) {
	if message == "" {
		message = defaultStaleContextMessage
	}
	runtime.mu.Lock()
	if runtime.staleMessage == "" {
		runtime.staleMessage = message
	}
	runtime.mu.Unlock()
}

func (runtime *runtimeState) mustBeActive() {
	runtime.mu.RLock()
	message := runtime.staleMessage
	runtime.mu.RUnlock()
	if message != "" {
		panic(message)
	}
}
