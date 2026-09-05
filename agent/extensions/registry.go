package extensions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

var (
	ErrRuntimeNotInitialized = errors.New("Extension runtime not initialized")
	ErrUIUnavailable         = errors.New("UI not available")
)

// MessageRenderOptions mirrors upstream MessageRenderOptions (types.ts):
// OutputPad is the horizontal padding configured by the outputPad setting.
type MessageRenderOptions struct {
	Expanded  bool
	OutputPad int
}

type EntryRenderOptions struct{ Expanded bool }

// MarkdownTransformContext mirrors upstream MarkdownTransformContext (types.ts).
type MarkdownTransformContext struct {
	MessageType    string // "user" | "assistant" | "assistant-thinking"
	IsStreaming    bool
	AvailableWidth int
}

// MarkdownTransformer rewrites interactive message markdown before rendering
// (upstream types.ts MarkdownTransformer). Extension registration is deferred;
// interactive mode composes the built-in transformers as a list.
type MarkdownTransformer func(markdown string, context MarkdownTransformContext) string

type MessageRenderer func(CustomMessage, MessageRenderOptions, Theme) Component

type EntryRenderer func(any, EntryRenderOptions, Theme) Component

type Extension struct {
	Path         string
	ResolvedPath string
	Hidden       bool
	SourceInfo   SourceInfo

	mu               sync.RWMutex
	handlers         map[EventType][]Handler
	tools            map[string]RegisteredTool
	toolOrder        []string
	messageRenderers map[string]MessageRenderer
	entryRenderers   map[string]EntryRenderer
	commands         map[string]Command
	commandOrder     []string
	flags            map[string]Flag
	flagOrder        []string
	shortcuts        map[string]Shortcut
	shortcutOrder    []string
}

type Registry struct {
	mu            sync.RWMutex
	extensions    []*Extension
	registrations []registration
	runtime       *runtimeState
	eventBus      EventBus
	cwd           string
}

type registration struct {
	path          string
	factory       Factory
	configuration registerOptions
}

type RegisterOption func(*registerOptions)

type registerOptions struct {
	hidden     bool
	sourceInfo *SourceInfo
}

func WithHidden(hidden bool) RegisterOption {
	return func(options *registerOptions) { options.hidden = hidden }
}

func WithSourceInfo(sourceInfo SourceInfo) RegisterOption {
	return func(options *registerOptions) { options.sourceInfo = &sourceInfo }
}

func NewRegistry(cwd string) *Registry {
	if cwd == "" {
		cwd = "."
	}
	if resolved, err := filepath.Abs(cwd); err == nil {
		cwd = filepath.Clean(resolved)
	}
	return &Registry{runtime: newRuntimeState(), eventBus: NewEventBus(), cwd: cwd}
}

func (registry *Registry) Register(path string, factory Factory, options ...RegisterOption) error {
	if factory == nil {
		return errors.New("extensions: nil factory")
	}
	if path == "" {
		path = "<inline>"
	}
	configuration := registerOptions{}
	for _, option := range options {
		if option != nil {
			option(&configuration)
		}
	}
	return registry.register(path, factory, configuration)
}

func (registry *Registry) register(path string, factory Factory, configuration registerOptions) error {
	resolved := path
	if !strings.HasPrefix(path, "<") {
		if filepath.IsAbs(path) {
			resolved = filepath.Clean(path)
		} else {
			resolved = filepath.Clean(filepath.Join(registry.cwd, path))
		}
	}
	sourceInfo := syntheticSourceInfo(path, resolved)
	if configuration.sourceInfo != nil {
		sourceInfo = *configuration.sourceInfo
	}
	extension := &Extension{
		Path:             path,
		ResolvedPath:     resolved,
		Hidden:           configuration.hidden,
		SourceInfo:       sourceInfo,
		handlers:         make(map[EventType][]Handler),
		tools:            make(map[string]RegisteredTool),
		messageRenderers: make(map[string]MessageRenderer),
		entryRenderers:   make(map[string]EntryRenderer),
		commands:         make(map[string]Command),
		flags:            make(map[string]Flag),
		shortcuts:        make(map[string]Shortcut),
	}
	api := &extensionAPI{extension: extension, runtime: registry.runtime, cwd: registry.cwd, events: registry.eventBus, loading: true, pendingFlags: make(map[string]any)}
	if err := callFactory(func(value API) error {
		if err := factory(value); err != nil {
			return err
		}
		api.commit()
		return nil
	}, api); err != nil {
		api.discard()
		return fmt.Errorf("extensions: load %s: %w", path, err)
	}
	registry.mu.Lock()
	registry.extensions = append(registry.extensions, extension)
	registry.registrations = append(registry.registrations, registration{
		path: path, factory: factory, configuration: cloneRegisterOptions(configuration),
	})
	registry.mu.Unlock()
	return nil
}

func cloneRegisterOptions(options registerOptions) registerOptions {
	cloned := registerOptions{hidden: options.hidden}
	if options.sourceInfo != nil {
		sourceInfo := *options.sourceInfo
		cloned.sourceInfo = &sourceInfo
	}
	return cloned
}

// Fresh recreates every registered extension against a new active runtime.
// Factories run again so captured API and command contexts belong exclusively
// to the replacement session.
func (registry *Registry) Fresh(cwd string) (*Registry, error) {
	fresh := NewRegistry(cwd)
	if registry == nil {
		return fresh, nil
	}
	registry.mu.RLock()
	registrations := append([]registration(nil), registry.registrations...)
	registry.mu.RUnlock()
	for _, entry := range registrations {
		if err := fresh.register(entry.path, entry.factory, cloneRegisterOptions(entry.configuration)); err != nil {
			return nil, err
		}
	}
	for name, value := range registry.runtime.flagValues() {
		fresh.runtime.setFlag(name, value)
	}
	return fresh, nil
}

// HasPath reports whether an extension path is already registered.
func (registry *Registry) HasPath(path string) bool {
	if registry == nil {
		return false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for _, entry := range registry.registrations {
		if entry.path == path {
			return true
		}
	}
	return false
}

func callFactory(factory Factory, api API) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	return factory(api)
}

func (registry *Registry) Extensions() []*Extension {
	registry.mu.RLock()
	extensions := append([]*Extension(nil), registry.extensions...)
	registry.mu.RUnlock()
	return extensions
}

func (registry *Registry) Events() EventBus { return registry.eventBus }

func (registry *Registry) BindModelRegistry(models ModelRegistry, report func(ExtensionError)) {
	registry.runtime.bindProviders(models, report)
}

func syntheticSourceInfo(path, resolved string) SourceInfo {
	source := "local"
	var baseDir *string
	if strings.HasPrefix(path, "<") && strings.HasSuffix(path, ">") {
		name := strings.TrimSuffix(strings.TrimPrefix(path, "<"), ">")
		if prefix, _, ok := strings.Cut(name, ":"); ok {
			name = prefix
		}
		if name != "" {
			source = name
		} else {
			source = "temporary"
		}
	} else {
		directory := filepath.Dir(resolved)
		baseDir = &directory
	}
	return SourceInfo{
		Path: path, Source: source, Scope: SourceScopeTemporary, Origin: SourceOriginTopLevel, BaseDir: baseDir,
	}
}

type extensionAPI struct {
	mu             sync.Mutex
	loading        bool
	failed         bool
	pendingFlags   map[string]any
	pendingChanges []func()
	unsubscribers  []func()
	extension      *Extension
	runtime        *runtimeState
	cwd            string
	events         EventBus
}

func (api *extensionAPI) On(event EventType, handler Handler) {
	api.assertActive()
	api.extension.mu.Lock()
	api.extension.handlers[event] = append(api.extension.handlers[event], handler)
	api.extension.mu.Unlock()
}

func (api *extensionAPI) RegisterTool(tool ToolDefinition) {
	api.assertActive()
	api.extension.mu.Lock()
	if _, exists := api.extension.tools[tool.Name]; !exists {
		api.extension.toolOrder = append(api.extension.toolOrder, tool.Name)
	}
	api.extension.tools[tool.Name] = RegisteredTool{Definition: tool, SourceInfo: api.extension.SourceInfo}
	api.extension.mu.Unlock()
	api.runtime.refreshTools()
}

func (api *extensionAPI) RegisterCommand(name string, command Command) {
	api.assertActive()
	command.Name = name
	command.SourceInfo = api.extension.SourceInfo
	api.extension.mu.Lock()
	if _, exists := api.extension.commands[name]; !exists {
		api.extension.commandOrder = append(api.extension.commandOrder, name)
	}
	api.extension.commands[name] = command
	api.extension.mu.Unlock()
}

func (api *extensionAPI) RegisterShortcut(shortcut string, definition Shortcut) {
	api.assertActive()
	shortcut = strings.ToLower(shortcut)
	definition.Shortcut = shortcut
	definition.ExtensionPath = api.extension.Path
	api.extension.mu.Lock()
	if _, exists := api.extension.shortcuts[shortcut]; !exists {
		api.extension.shortcutOrder = append(api.extension.shortcutOrder, shortcut)
	}
	api.extension.shortcuts[shortcut] = definition
	api.extension.mu.Unlock()
}

func (api *extensionAPI) RegisterFlag(name string, flag Flag) {
	api.assertActive()
	if flag.Default != nil {
		actual := "object"
		switch flag.Default.(type) {
		case bool:
			actual = "boolean"
		case string:
			actual = "string"
		case int, int64, float64:
			actual = "number"
		}
		if actual != string(flag.Type) {
			panic(fmt.Sprintf("Invalid default for flag %q: expected %s, got %s", name, flag.Type, actual))
		}
	}
	flag.Name = name
	flag.ExtensionPath = api.extension.Path
	api.extension.mu.Lock()
	if _, exists := api.extension.flags[name]; !exists {
		api.extension.flagOrder = append(api.extension.flagOrder, name)
	}
	api.extension.flags[name] = flag
	api.extension.mu.Unlock()
	if flag.Default != nil {
		api.mu.Lock()
		if api.loading {
			if _, exists := api.pendingFlags[name]; !exists {
				api.pendingFlags[name] = flag.Default
			}
		} else {
			api.runtime.setFlagDefault(name, flag.Default)
		}
		api.mu.Unlock()
	}
}

func (api *extensionAPI) GetFlag(name string) (any, bool) {
	api.assertActive()
	api.extension.mu.RLock()
	_, registered := api.extension.flags[name]
	api.extension.mu.RUnlock()
	if !registered {
		return nil, false
	}
	if value, ok := api.runtime.flag(name); ok {
		return value, true
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	value, ok := api.pendingFlags[name]
	return value, ok
}

func (api *extensionAPI) RegisterMessageRenderer(customType string, renderer MessageRenderer) {
	api.assertActive()
	api.extension.mu.Lock()
	api.extension.messageRenderers[customType] = renderer
	api.extension.mu.Unlock()
}

func (api *extensionAPI) RegisterEntryRenderer(customType string, renderer EntryRenderer) {
	api.assertActive()
	api.extension.mu.Lock()
	api.extension.entryRenderers[customType] = renderer
	api.extension.mu.Unlock()
}

func (api *extensionAPI) SendMessage(ctx context.Context, message CustomMessage, options *SendMessageOptions) error {
	api.assertActive()
	return api.runtime.actionsSnapshot().SendMessage(ctx, message, options)
}

func (api *extensionAPI) SendUserMessage(ctx context.Context, content ai.UserContent, options *SendUserMessageOptions) error {
	api.assertActive()
	return api.runtime.actionsSnapshot().SendUserMessage(ctx, content, options)
}

func (api *extensionAPI) AppendEntry(ctx context.Context, customType string, data any) error {
	api.assertActive()
	return api.runtime.actionsSnapshot().AppendEntry(ctx, customType, data)
}

func (api *extensionAPI) SetSessionName(ctx context.Context, name string) error {
	api.assertActive()
	return api.runtime.actionsSnapshot().SetSessionName(ctx, name)
}

func (api *extensionAPI) GetSessionName(ctx context.Context) (*string, error) {
	api.assertActive()
	return api.runtime.actionsSnapshot().GetSessionName(ctx)
}

func (api *extensionAPI) SetLabel(ctx context.Context, entryID string, label *string) error {
	api.assertActive()
	return api.runtime.actionsSnapshot().SetLabel(ctx, entryID, label)
}

func (api *extensionAPI) Exec(ctx context.Context, command string, args []string, options *ExecOptions) (ExecResult, error) {
	api.assertActive()
	if options == nil {
		options = &ExecOptions{}
	}
	if options.CWD == "" {
		copy := *options
		copy.CWD = api.cwd
		options = &copy
	}
	return Exec(ctx, command, args, options)
}

func (api *extensionAPI) GetActiveTools() ([]string, error) {
	api.assertActive()
	return api.runtime.actionsSnapshot().GetActiveTools()
}

func (api *extensionAPI) GetAllTools() ([]ToolInfo, error) {
	api.assertActive()
	return api.runtime.actionsSnapshot().GetAllTools()
}

func (api *extensionAPI) SetActiveTools(names []string) error {
	api.assertActive()
	return api.runtime.actionsSnapshot().SetActiveTools(names)
}

func (api *extensionAPI) GetCommands() ([]SlashCommandInfo, error) {
	api.assertActive()
	return api.runtime.actionsSnapshot().GetCommands()
}

func (api *extensionAPI) SetModel(ctx context.Context, model *ai.Model) (bool, error) {
	api.assertActive()
	return api.runtime.actionsSnapshot().SetModel(ctx, model)
}

func (api *extensionAPI) GetThinkingLevel() (engine.ThinkingLevel, error) {
	api.assertActive()
	return api.runtime.actionsSnapshot().GetThinkingLevel()
}

func (api *extensionAPI) SetThinkingLevel(level engine.ThinkingLevel) error {
	api.assertActive()
	return api.runtime.actionsSnapshot().SetThinkingLevel(level)
}

func (api *extensionAPI) RegisterProvider(provider Provider) {
	api.assertActive()
	api.applyRuntimeChange(func() { api.runtime.registerProvider(provider, api.extension.Path) })
}

func (api *extensionAPI) RegisterProviderConfig(name string, config ProviderConfig) {
	api.assertActive()
	api.applyRuntimeChange(func() { api.runtime.registerProviderConfig(name, config, api.extension.Path) })
}

func (api *extensionAPI) UnregisterProvider(name string) {
	api.assertActive()
	api.applyRuntimeChange(func() { api.runtime.unregisterProvider(name, api.extension.Path) })
}

func (api *extensionAPI) Events() EventBus {
	api.assertActive()
	return extensionEventBus{api}
}

func (api *extensionAPI) assertActive() {
	api.mu.Lock()
	failed := api.failed
	api.mu.Unlock()
	if failed {
		panic(fmt.Sprintf("Extension %q failed to load and its API is no longer active.", api.extension.Path))
	}
	api.runtime.mustBeActive()
}
func (api *extensionAPI) applyRuntimeChange(change func()) {
	api.mu.Lock()
	if api.loading {
		api.pendingChanges = append(api.pendingChanges, change)
		api.mu.Unlock()
		return
	}
	api.mu.Unlock()
	change()
}
func (api *extensionAPI) commit() {
	api.mu.Lock()
	flags := make(map[string]any, len(api.pendingFlags))
	for name, value := range api.pendingFlags {
		flags[name] = value
	}
	changes := append([]func(){}, api.pendingChanges...)
	api.mu.Unlock()
	for name, value := range flags {
		api.runtime.setFlagDefault(name, value)
	}
	for _, change := range changes {
		change()
	}
	api.mu.Lock()
	api.pendingFlags = nil
	api.pendingChanges = nil
	api.unsubscribers = nil
	api.loading = false
	api.mu.Unlock()
}
func (api *extensionAPI) discard() {
	api.mu.Lock()
	api.failed = true
	api.loading = false
	unsubscribers := api.unsubscribers
	api.unsubscribers = nil
	api.pendingFlags = nil
	api.pendingChanges = nil
	api.mu.Unlock()
	for _, unsubscribe := range unsubscribers {
		unsubscribe()
	}
}

type extensionEventBus struct{ api *extensionAPI }

func (bus extensionEventBus) Emit(ctx context.Context, channel string, data any) []error {
	bus.api.assertActive()
	return bus.api.events.Emit(ctx, channel, data)
}
func (bus extensionEventBus) On(channel string, handler EventListener) func() {
	bus.api.assertActive()
	unsubscribe := bus.api.events.On(channel, handler)
	bus.api.mu.Lock()
	if bus.api.loading {
		bus.api.unsubscribers = append(bus.api.unsubscribers, unsubscribe)
	}
	bus.api.mu.Unlock()
	return unsubscribe
}
func (bus extensionEventBus) Clear() { bus.api.assertActive(); bus.api.events.Clear() }
