package extensions

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
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
	GetThinkingLevel       func() (engine.ThinkingLevel, error)
	SetThinkingLevel       func(engine.ThinkingLevel) error
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
		GetThinkingLevel:       func() (engine.ThinkingLevel, error) { return "", notInitialized() },
		SetThinkingLevel:       func(engine.ThinkingLevel) error { return notInitialized() },
		RegisterProvider:       func(Provider) error { return notInitialized() },
		RegisterProviderConfig: func(string, ProviderConfig) error { return notInitialized() },
		UnregisterProvider:     func(string) error { return notInitialized() },
	}
}

// fillNilFuncs sets every nil func field of *target to the field of defaults.
func fillNilFuncs[T any](target *T, defaults T) {
	value, fallback := reflect.ValueOf(target).Elem(), reflect.ValueOf(defaults)
	for index := range value.NumField() {
		if field := value.Field(index); field.Kind() == reflect.Func && field.IsNil() {
			field.Set(fallback.Field(index))
		}
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
	fillNilFuncs(&actions, defaults)
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
	runtime.bindProviderFuncs(actions.RegisterProvider, actions.RegisterProviderConfig, actions.UnregisterProvider, report)
}

func (runtime *runtimeState) bindProviders(registry ModelRegistry, report func(ExtensionError)) {
	if registry != nil {
		runtime.bindProviderFuncs(registry.RegisterProvider, registry.RegisterProviderConfig, registry.UnregisterProvider, report)
	}
}

// bindProviderFuncs binds provider registration and flushes the registrations
// queued while unbound: configs first, then native providers.
func (runtime *runtimeState) bindProviderFuncs(
	registerNative func(Provider) error,
	registerConfig func(string, ProviderConfig) error,
	unregister func(string) error,
	report func(ExtensionError),
) {
	runtime.mu.Lock()
	runtime.registerNative, runtime.registerConfig, runtime.unregister = registerNative, registerConfig, unregister
	runtime.providerBound = true
	pending := runtime.pendingProviders
	runtime.pendingProviders = nil
	runtime.mu.Unlock()
	flush := func(registration pendingProviderRegistration, err error) {
		if err != nil && report != nil {
			report(ExtensionError{ExtensionPath: registration.extensionPath, Event: "register_provider", Error: err.Error()})
		}
	}
	for _, registration := range pending {
		if registration.config != nil {
			flush(registration, registerConfig(registration.name, *registration.config))
		}
	}
	for _, registration := range pending {
		if registration.native != nil {
			flush(registration, registerNative(*registration.native))
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
	defer runtime.mu.RUnlock()
	return maps.Clone(runtime.flags)
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
		runtime.pendingProviders = slices.DeleteFunc(runtime.pendingProviders, func(registration pendingProviderRegistration) bool {
			return registration.name == name
		})
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
