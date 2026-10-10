package extensions

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
)

type ExtensionError struct {
	ExtensionPath string `json:"extensionPath"`
	Event         string `json:"event"`
	Error         string `json:"error"`
	Stack         string `json:"stack,omitempty"`
}

type Diagnostic struct {
	Type    string
	Message string
	Path    string
}

type ContextActions struct {
	RequestInput           InputHandler
	GetModel               func() *ai.Model
	GetScopedModels        func() []ScopedModel
	IsIdle                 func() bool
	IsProjectTrusted       func() bool
	GetSignal              func() context.Context
	Abort                  func()
	HasPendingMessages     func() bool
	Shutdown               func()
	GetContextUsage        func() *ContextUsage
	Compact                func(*CompactOptions)
	GetSystemPrompt        func() string
	GetSystemPromptOptions func() SystemPromptOptions
}

type CommandActions struct {
	WaitForIdle   func(context.Context) error
	NewSession    func(context.Context, *NewSessionOptions) (SessionReplacementResult, error)
	Fork          func(context.Context, string, *ForkOptions) (SessionReplacementResult, error)
	NavigateTree  func(context.Context, string, *NavigateTreeOptions) (SessionReplacementResult, error)
	SwitchSession func(context.Context, string, *SwitchSessionOptions) (SessionReplacementResult, error)
	Reload        func(context.Context) error
}

type RunnerOptions struct {
	CWD            string
	SessionManager ReadonlySessionManager
	ModelRegistry  ModelRegistry
	Mode           Mode
	UI             UI
	Actions        Actions
	ContextActions ContextActions
	CommandActions *CommandActions
	ErrorHandler   func(ExtensionError)
}

type Runner struct {
	extensions     []*Extension
	events         EventBus
	runtime        *runtimeState
	cwd            string
	sessionManager ReadonlySessionManager
	modelRegistry  ModelRegistry

	mu                sync.RWMutex
	ui                UI
	uiPromptDepth     int
	activeUIPrompt    UIPromptStartEvent
	uiPromptTail      chan struct{}
	mode              Mode
	contextActions    ContextActions
	commandActions    CommandActions
	staleMessage      string
	deferredProviders *Actions
	toolCalls         *ToolCallHost

	errorMu        sync.RWMutex
	nextErrorID    uint64
	errorListeners map[uint64]func(ExtensionError)

	diagnosticsMu       sync.RWMutex
	shortcutDiagnostics []Diagnostic
}

func NewRunner(registry *Registry, options RunnerOptions) *Runner {
	if registry == nil {
		registry = NewRegistry(options.CWD)
	}
	cwd := options.CWD
	if cwd == "" {
		cwd = registry.cwd
	}
	runner := &Runner{
		events:         registry.Events(),
		extensions:     registry.Extensions(),
		runtime:        registry.runtime,
		cwd:            cwd,
		sessionManager: options.SessionManager,
		modelRegistry:  options.ModelRegistry,
		ui:             NewNoopUI(),
		mode:           ModePrint,
		errorListeners: make(map[uint64]func(ExtensionError)),
	}
	if options.ErrorHandler != nil {
		runner.OnError(options.ErrorHandler)
	}
	actions := options.Actions
	if options.ModelRegistry != nil {
		if actions.RegisterProvider == nil {
			actions.RegisterProvider = options.ModelRegistry.RegisterProvider
		}
		if actions.RegisterProviderConfig == nil {
			actions.RegisterProviderConfig = options.ModelRegistry.RegisterProviderConfig
		}
		if actions.UnregisterProvider == nil {
			actions.UnregisterProvider = options.ModelRegistry.UnregisterProvider
		}
	}
	runner.bindCore(actions, options.ContextActions, runner.HasHandlers(EventProjectTrust))
	runner.BindCommandContext(options.CommandActions)
	runner.SetUI(options.UI, options.Mode)
	return runner
}

func (runner *Runner) bindCore(actions Actions, contextActions ContextActions, deferProviders bool) {
	runner.mu.Lock()
	runner.contextActions = normalizeContextActions(contextActions, runner.cwd)
	runner.deferredProviders = nil
	runner.mu.Unlock()
	actions = runner.runtime.bindActions(actions)
	if deferProviders {
		runner.mu.Lock()
		runner.deferredProviders = &actions
		runner.mu.Unlock()
		return
	}
	runner.runtime.bindProviderActions(actions, runner.emitError)
}

func (runner *Runner) bindDeferredProviders() {
	runner.mu.Lock()
	actions := runner.deferredProviders
	runner.deferredProviders = nil
	runner.mu.Unlock()
	if actions != nil {
		runner.runtime.bindProviderActions(*actions, runner.emitError)
	}
}

func normalizeContextActions(actions ContextActions, cwd string) ContextActions {
	fillNilFuncs(&actions, ContextActions{
		GetModel:               func() *ai.Model { return nil },
		GetScopedModels:        func() []ScopedModel { return []ScopedModel{} },
		IsIdle:                 func() bool { return true },
		IsProjectTrusted:       func() bool { return true },
		GetSignal:              func() context.Context { return nil },
		Abort:                  func() {},
		HasPendingMessages:     func() bool { return false },
		Shutdown:               func() {},
		GetContextUsage:        func() *ContextUsage { return nil },
		Compact:                func(*CompactOptions) {},
		GetSystemPrompt:        func() string { return "" },
		GetSystemPromptOptions: func() SystemPromptOptions { return SystemPromptOptions{CWD: cwd} },
	})
	return actions
}

func (runner *Runner) BindCommandContext(actions *CommandActions) {
	var resolved CommandActions
	if actions != nil {
		resolved = *actions
	}
	fillNilFuncs(&resolved, CommandActions{
		WaitForIdle: func(context.Context) error { return nil },
		NewSession: func(context.Context, *NewSessionOptions) (SessionReplacementResult, error) {
			return SessionReplacementResult{}, nil
		},
		Fork: func(context.Context, string, *ForkOptions) (SessionReplacementResult, error) {
			return SessionReplacementResult{}, nil
		},
		NavigateTree: func(context.Context, string, *NavigateTreeOptions) (SessionReplacementResult, error) {
			return SessionReplacementResult{}, nil
		},
		SwitchSession: func(context.Context, string, *SwitchSessionOptions) (SessionReplacementResult, error) {
			return SessionReplacementResult{}, nil
		},
		Reload: func(context.Context) error { return nil },
	})
	runner.mu.Lock()
	runner.commandActions = resolved
	runner.mu.Unlock()
}

func (runner *Runner) SetUI(ui UI, mode Mode) {
	if mode == "" {
		mode = ModePrint
	}
	if ui == nil || mode == ModePrint || mode == ModeJSON {
		ui = NewNoopUI()
	} else {
		ui = &promptUI{UI: ui, runner: runner}
	}
	runner.mu.Lock()
	runner.ui = ui
	runner.mode = mode
	runner.mu.Unlock()
}

func (runner *Runner) UI() UI {
	runner.assertActive()
	runner.mu.RLock()
	ui := runner.ui
	runner.mu.RUnlock()
	return ui
}

func (runner *Runner) HasUI() bool {
	runner.assertActive()
	runner.mu.RLock()
	ui := runner.ui
	runner.mu.RUnlock()
	_, noUI := ui.(NoopUI)
	return !noUI
}

func (runner *Runner) ExtensionPaths() []string {
	paths := make([]string, len(runner.extensions))
	for index, extension := range runner.extensions {
		paths[index] = extension.Path
	}
	return paths
}

func (runner *Runner) ModelRegistry() ModelRegistry { return runner.modelRegistry }

// Events is this attachment's bus, including when a runtime replaces it on reload.
func (runner *Runner) Events() EventBus { return runner.events }

func (runner *Runner) Shutdown() { runner.CreateContext().Shutdown() }

func (runner *Runner) ActiveTools() ([]string, error) {
	runner.assertActive()
	return runner.runtime.actionsSnapshot().GetActiveTools()
}

func (runner *Runner) HasHandlers(event EventType) bool {
	for _, extension := range runner.extensions {
		extension.mu.RLock()
		has := len(extension.handlers[event]) > 0
		extension.mu.RUnlock()
		if has {
			return true
		}
	}
	return false
}

func (runner *Runner) AllRegisteredTools() []RegisteredTool {
	seen := make(map[string]struct{})
	var tools []RegisteredTool
	for _, extension := range runner.extensions {
		extension.mu.RLock()
		for _, name := range extension.toolOrder {
			if _, exists := seen[name]; exists {
				continue
			}
			tool, exists := extension.tools[name]
			if !exists {
				continue
			}
			seen[name] = struct{}{}
			tools = append(tools, tool)
		}
		extension.mu.RUnlock()
	}
	return tools
}

func (runner *Runner) ToolDefinition(name string) *ToolDefinition {
	for _, extension := range runner.extensions {
		extension.mu.RLock()
		tool, exists := extension.tools[name]
		extension.mu.RUnlock()
		if exists {
			definition := tool.Definition
			return &definition
		}
	}
	return nil
}

func (runner *Runner) Flags() map[string]Flag {
	result := make(map[string]Flag)
	for _, flag := range runner.RegisteredFlags() {
		result[flag.Name] = flag
	}
	return result
}

func (runner *Runner) RegisteredFlags() []Flag { return registeredFlags(runner.extensions) }

func (runner *Runner) SetFlagValue(name string, value any) { runner.runtime.setFlag(name, value) }

func (runner *Runner) FlagValues() map[string]any { return runner.runtime.flagValues() }

func (runner *Runner) MessageRenderer(customType string) MessageRenderer {
	for _, extension := range runner.extensions {
		extension.mu.RLock()
		renderer := extension.messageRenderers[customType]
		extension.mu.RUnlock()
		if renderer != nil {
			return renderer
		}
	}
	return nil
}

func (runner *Runner) EntryRenderer(customType string) EntryRenderer {
	for _, extension := range runner.extensions {
		extension.mu.RLock()
		renderer := extension.entryRenderers[customType]
		extension.mu.RUnlock()
		if renderer != nil {
			return renderer
		}
	}
	return nil
}

func (runner *Runner) resolveCommands() []ResolvedCommand {
	var commands []Command
	counts := make(map[string]int)
	for _, extension := range runner.extensions {
		extension.mu.RLock()
		for _, name := range extension.commandOrder {
			command, exists := extension.commands[name]
			if !exists {
				continue
			}
			commands = append(commands, command)
			counts[command.Name]++
		}
		extension.mu.RUnlock()
	}
	seen := make(map[string]int)
	taken := make(map[string]struct{})
	resolved := make([]ResolvedCommand, 0, len(commands))
	for _, command := range commands {
		seen[command.Name]++
		occurrence := seen[command.Name]
		invocation := command.Name
		if counts[command.Name] > 1 {
			invocation = fmt.Sprintf("%s:%d", command.Name, occurrence)
		}
		if _, exists := taken[invocation]; exists {
			suffix := occurrence
			for {
				suffix++
				invocation = fmt.Sprintf("%s:%d", command.Name, suffix)
				if _, exists := taken[invocation]; !exists {
					break
				}
			}
		}
		taken[invocation] = struct{}{}
		resolved = append(resolved, ResolvedCommand{Command: command, InvocationName: invocation})
	}
	return resolved
}

func (runner *Runner) RegisteredCommands() []ResolvedCommand {
	return runner.resolveCommands()
}

func (runner *Runner) Command(name string) *ResolvedCommand {
	for _, command := range runner.resolveCommands() {
		if command.InvocationName == name {
			resolved := command
			return &resolved
		}
	}
	return nil
}

func (runner *Runner) ExecuteCommand(ctx context.Context, name, args string) bool {
	command := runner.Command(name)
	if command == nil {
		return false
	}
	err := callCommandHandler(ctx, command.Handler, args, runner.CreateCommandContext())
	if err != nil {
		runner.emitError(ExtensionError{
			ExtensionPath: "command:" + name,
			Event:         "command",
			Error:         err.Error(),
			Stack:         string(debug.Stack()),
		})
	}
	return true
}

func callCommandHandler(ctx context.Context, handler func(context.Context, string, CommandContext) error, args string, commandContext CommandContext) (err error) {
	if handler == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	return handler(ctx, args, commandContext)
}

var reservedShortcutBindings = map[string]struct{}{
	"app.interrupt": {}, "app.clear": {}, "app.exit": {}, "app.suspend": {},
	"app.thinking.cycle": {}, "app.model.cycleForward": {}, "app.model.cycleBackward": {},
	"app.model.select": {}, "app.tools.expand": {}, "app.thinking.toggle": {},
	"app.editor.external": {}, "app.message.copy": {}, "app.message.followUp": {},
	"tui.input.submit": {}, "tui.select.confirm": {}, "tui.select.cancel": {},
	"tui.input.copy": {}, "tui.editor.deleteToLineEnd": {},
}

type builtInShortcut struct {
	binding  string
	reserved bool
}

func (runner *Runner) Shortcuts(bindings map[string][]string) map[string]Shortcut {
	builtins := make(map[string]builtInShortcut)
	for binding, keys := range bindings {
		_, reserved := reservedShortcutBindings[binding]
		for _, key := range keys {
			normalized := strings.ToLower(key)
			existing, exists := builtins[normalized]
			if exists && existing.reserved && !reserved {
				continue
			}
			builtins[normalized] = builtInShortcut{binding: binding, reserved: reserved}
		}
	}
	var diagnostics []Diagnostic
	result := make(map[string]Shortcut)
	for _, extension := range runner.extensions {
		extension.mu.RLock()
		for _, key := range extension.shortcutOrder {
			shortcut, exists := extension.shortcuts[key]
			if !exists {
				continue
			}
			if builtIn, exists := builtins[key]; exists && builtIn.reserved {
				diagnostics = append(diagnostics, Diagnostic{
					Type: "warning", Path: shortcut.ExtensionPath,
					Message: fmt.Sprintf("Extension shortcut '%s' from %s conflicts with built-in shortcut. Skipping.", key, shortcut.ExtensionPath),
				})
				continue
			} else if exists {
				diagnostics = append(diagnostics, Diagnostic{
					Type: "warning", Path: shortcut.ExtensionPath,
					Message: fmt.Sprintf("Extension shortcut conflict: '%s' is built-in shortcut for %s and %s. Using %s.", key, builtIn.binding, shortcut.ExtensionPath, shortcut.ExtensionPath),
				})
			}
			if existing, exists := result[key]; exists {
				diagnostics = append(diagnostics, Diagnostic{
					Type: "warning", Path: shortcut.ExtensionPath,
					Message: fmt.Sprintf("Extension shortcut conflict: '%s' registered by both %s and %s. Using %s.", key, existing.ExtensionPath, shortcut.ExtensionPath, shortcut.ExtensionPath),
				})
			}
			result[key] = shortcut
		}
		extension.mu.RUnlock()
	}
	runner.diagnosticsMu.Lock()
	runner.shortcutDiagnostics = diagnostics
	runner.diagnosticsMu.Unlock()
	return result
}

// ShortcutOrder returns registered shortcut keys in extension registration
// order. Upstream getShortcuts returns an insertion-ordered Map; the TUI
// dispatcher walks this order so first-registered shortcuts win ties.
func (runner *Runner) ShortcutOrder() []string {
	var order []string
	seen := make(map[string]struct{})
	for _, extension := range runner.extensions {
		extension.mu.RLock()
		for _, key := range extension.shortcutOrder {
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				order = append(order, key)
			}
		}
		extension.mu.RUnlock()
	}
	return order
}

func (runner *Runner) ShortcutDiagnostics() []Diagnostic {
	runner.diagnosticsMu.RLock()
	diagnostics := append([]Diagnostic(nil), runner.shortcutDiagnostics...)
	runner.diagnosticsMu.RUnlock()
	return diagnostics
}

func (runner *Runner) OnError(listener func(ExtensionError)) func() {
	runner.errorMu.Lock()
	runner.nextErrorID++
	id := runner.nextErrorID
	runner.errorListeners[id] = listener
	runner.errorMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			runner.errorMu.Lock()
			delete(runner.errorListeners, id)
			runner.errorMu.Unlock()
		})
	}
}

func (runner *Runner) emitError(extensionError ExtensionError) {
	runner.errorMu.RLock()
	listeners := make([]func(ExtensionError), 0, len(runner.errorListeners))
	for _, id := range slices.Sorted(maps.Keys(runner.errorListeners)) {
		listeners = append(listeners, runner.errorListeners[id])
	}
	runner.errorMu.RUnlock()
	for _, listener := range listeners {
		if listener != nil {
			listener(extensionError)
		}
	}
}

func (runner *Runner) Invalidate(message string) {
	if message == "" {
		message = defaultStaleContextMessage
	}
	runner.mu.Lock()
	if runner.staleMessage == "" {
		runner.staleMessage = message
	}
	runner.mu.Unlock()
	runner.runtime.invalidate(message)
}

func (runner *Runner) assertActive() {
	runner.mu.RLock()
	message := runner.staleMessage
	runner.mu.RUnlock()
	if message != "" {
		panic(message)
	}
}

type extensionContext struct {
	runner       *Runner
	systemPrompt func() string
}

type restrictedProjectTrustContext struct {
	Context
	cwd   string
	mode  Mode
	hasUI bool
	ui    TrustUI
}

func restrictProjectTrustContext(value Context) Context {
	return &restrictedProjectTrustContext{
		cwd: value.CWD(), mode: value.Mode(), hasUI: value.HasUI(), ui: value.UI(),
	}
}

func (value *restrictedProjectTrustContext) CWD() string { return value.cwd }
func (value *restrictedProjectTrustContext) Mode() Mode  { return value.mode }
func (value *restrictedProjectTrustContext) HasUI() bool { return value.hasUI }
func (value *restrictedProjectTrustContext) UI() UI      { return restrictedProjectTrustUI{trust: value.ui} }

type restrictedProjectTrustUI struct {
	UI
	trust TrustUI
}

func (value restrictedProjectTrustUI) Select(ctx context.Context, title string, options []string, config *DialogOptions) (string, bool, error) {
	return value.trust.Select(ctx, title, options, config)
}

func (value restrictedProjectTrustUI) Confirm(ctx context.Context, title, message string, config *DialogOptions) (bool, error) {
	return value.trust.Confirm(ctx, title, message, config)
}

func (value restrictedProjectTrustUI) Input(ctx context.Context, title string, placeholder *string, config *DialogOptions) (string, bool, error) {
	return value.trust.Input(ctx, title, placeholder, config)
}

func (value restrictedProjectTrustUI) Notify(message string, notificationType NotificationType) {
	value.trust.Notify(message, notificationType)
}

func (contextValue *extensionContext) UI() UI {
	contextValue.runner.assertActive()
	return contextValue.runner.UI()
}

func (contextValue *extensionContext) Mode() Mode {
	contextValue.runner.assertActive()
	contextValue.runner.mu.RLock()
	mode := contextValue.runner.mode
	contextValue.runner.mu.RUnlock()
	return mode
}

func (contextValue *extensionContext) HasUI() bool {
	contextValue.runner.assertActive()
	return contextValue.runner.HasUI()
}

func (contextValue *extensionContext) CWD() string {
	contextValue.runner.assertActive()
	return contextValue.runner.cwd
}

func (contextValue *extensionContext) SessionManager() ReadonlySessionManager {
	contextValue.runner.assertActive()
	return contextValue.runner.sessionManager
}

func (contextValue *extensionContext) ModelRegistry() ModelRegistry {
	contextValue.runner.assertActive()
	return contextValue.runner.modelRegistry
}

func (contextValue *extensionContext) actions() ContextActions {
	contextValue.runner.assertActive()
	contextValue.runner.mu.RLock()
	actions := contextValue.runner.contextActions
	contextValue.runner.mu.RUnlock()
	return actions
}

func (contextValue *extensionContext) Model() *ai.Model { return contextValue.actions().GetModel() }

func (contextValue *extensionContext) ScopedModels() []ScopedModel {
	models := contextValue.actions().GetScopedModels()
	if models == nil {
		return []ScopedModel{}
	}
	return models
}

func (contextValue *extensionContext) ThinkingLevel() engine.ThinkingLevel {
	contextValue.runner.assertActive()
	level, err := contextValue.runner.runtime.actionsSnapshot().GetThinkingLevel()
	if err != nil {
		return ""
	}
	return level
}

func (contextValue *extensionContext) IsIdle() bool { return contextValue.actions().IsIdle() }

func (contextValue *extensionContext) IsProjectTrusted() bool {
	return contextValue.actions().IsProjectTrusted()
}

func (contextValue *extensionContext) Signal() context.Context {
	return contextValue.actions().GetSignal()
}

func (contextValue *extensionContext) Abort() { contextValue.actions().Abort() }

func (contextValue *extensionContext) HasPendingMessages() bool {
	return contextValue.actions().HasPendingMessages()
}

func (contextValue *extensionContext) Shutdown() { contextValue.actions().Shutdown() }

func (contextValue *extensionContext) GetContextUsage() *ContextUsage {
	return contextValue.actions().GetContextUsage()
}

func (contextValue *extensionContext) Compact(options *CompactOptions) {
	contextValue.actions().Compact(options)
}

func (contextValue *extensionContext) GetSystemPrompt() string {
	contextValue.runner.assertActive()
	if contextValue.systemPrompt != nil {
		return contextValue.systemPrompt()
	}
	return contextValue.actions().GetSystemPrompt()
}

type extensionCommandContext struct{ *extensionContext }

type extensionReplacedSessionContext struct{ *extensionCommandContext }

func (contextValue *extensionCommandContext) commandActions() CommandActions {
	contextValue.runner.assertActive()
	contextValue.runner.mu.RLock()
	actions := contextValue.runner.commandActions
	contextValue.runner.mu.RUnlock()
	return actions
}

func (contextValue *extensionCommandContext) GetSystemPromptOptions() SystemPromptOptions {
	return contextValue.actions().GetSystemPromptOptions()
}

func (contextValue *extensionCommandContext) WaitForIdle(ctx context.Context) error {
	return contextValue.commandActions().WaitForIdle(ctx)
}

func (contextValue *extensionCommandContext) NewSession(ctx context.Context, options *NewSessionOptions) (SessionReplacementResult, error) {
	return contextValue.commandActions().NewSession(ctx, options)
}

func (contextValue *extensionCommandContext) Fork(ctx context.Context, entryID string, options *ForkOptions) (SessionReplacementResult, error) {
	return contextValue.commandActions().Fork(ctx, entryID, options)
}

func (contextValue *extensionCommandContext) NavigateTree(ctx context.Context, targetID string, options *NavigateTreeOptions) (SessionReplacementResult, error) {
	return contextValue.commandActions().NavigateTree(ctx, targetID, options)
}

func (contextValue *extensionCommandContext) SwitchSession(ctx context.Context, path string, options *SwitchSessionOptions) (SessionReplacementResult, error) {
	return contextValue.commandActions().SwitchSession(ctx, path, options)
}

func (contextValue *extensionCommandContext) Reload(ctx context.Context) error {
	return contextValue.commandActions().Reload(ctx)
}

func (contextValue *extensionReplacedSessionContext) SendMessage(
	ctx context.Context,
	message CustomMessage,
	options *SendMessageOptions,
) error {
	contextValue.runner.assertActive()
	return contextValue.runner.runtime.actionsSnapshot().SendMessage(ctx, message, options)
}

func (contextValue *extensionReplacedSessionContext) SendUserMessage(
	ctx context.Context,
	content ai.UserContent,
	options *SendUserMessageOptions,
) error {
	contextValue.runner.assertActive()
	return contextValue.runner.runtime.actionsSnapshot().SendUserMessage(ctx, content, options)
}

func (runner *Runner) CreateContext() Context {
	return &extensionContext{runner: runner}
}

// SetToolCallHost lets tools run other tools (ToolContext.ExecuteTool).
func (runner *Runner) SetToolCallHost(host *ToolCallHost) {
	runner.mu.Lock()
	runner.toolCalls = host
	runner.mu.Unlock()
}

// CreateToolContext is the context of the tool call toolCallID: a ToolContext
// when the session runs nested calls.
func (runner *Runner) CreateToolContext(toolCallID string) Context {
	runner.mu.RLock()
	host := runner.toolCalls
	runner.mu.RUnlock()
	if host == nil {
		return runner.CreateContext()
	}
	return &toolContext{extensionContext: &extensionContext{runner: runner}, host: host, toolCallID: toolCallID}
}

type toolContext struct {
	*extensionContext
	host       *ToolCallHost
	toolCallID string
}

func (tool *toolContext) Tools() []LoadoutTool { return tool.host.Tools() }

func (tool *toolContext) ExecuteTool(ctx context.Context, name string, args any, onUpdate engine.AgentToolUpdateCallback) engine.AgentToolCallOutcome {
	return tool.host.Execute(ctx, tool.toolCallID, name, args, onUpdate)
}

func (runner *Runner) CreateCommandContext() CommandContext {
	return &extensionCommandContext{extensionContext: &extensionContext{runner: runner}}
}

// CreateReplacedSessionContext creates the post-replacement context passed to
// new, fork, and switch callbacks.
func (runner *Runner) CreateReplacedSessionContext() ReplacedSessionContext {
	command := &extensionCommandContext{extensionContext: &extensionContext{runner: runner}}
	return &extensionReplacedSessionContext{extensionCommandContext: command}
}

func (runner *Runner) EmitProjectTrust(ctx context.Context, event ProjectTrustEvent, trustContext Context) (*ProjectTrustResult, []ExtensionError) {
	defer runner.bindDeferredProviders()
	if trustContext == nil {
		trustContext = runner.CreateContext()
	}
	trustContext = restrictProjectTrustContext(trustContext)
	var errorsSeen []ExtensionError
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventProjectTrust) {
			result, err := callHandler(ctx, handler, event, trustContext)
			if err != nil {
				extensionError := makeExtensionError(extension.Path, EventProjectTrust, err)
				errorsSeen = append(errorsSeen, extensionError)
				runner.emitError(extensionError)
				continue
			}
			decision, ok := eventResult[ProjectTrustResult](result)
			if !ok || decision.Trusted == ProjectTrustUndecided {
				continue
			}
			return decision, errorsSeen
		}
	}
	return nil, errorsSeen
}

func (runner *Runner) Emit(ctx context.Context, event Event) any {
	if event == nil {
		return nil
	}
	// A disposed or replaced runtime's late events (a turn still finishing as the
	// process exits, a session_start the user quit during) reach no further
	// extension: every call on their ctx would fail as stale.
	stale := func() bool {
		runner.mu.RLock()
		defer runner.mu.RUnlock()
		return runner.staleMessage != ""
	}
	if stale() {
		return nil
	}
	if event.Type() != EventProjectTrust {
		runner.bindDeferredProviders()
	}
	extensionContext := runner.CreateContext()
	var current any
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, event.Type()) {
			if stale() {
				return current
			}
			result, err := callHandler(ctx, handler, event, extensionContext)
			if err != nil {
				if stale() {
					return current
				}
				runner.emitError(makeExtensionError(extension.Path, event.Type(), err))
				continue
			}
			if isSessionBeforeEvent(event.Type()) && result != nil {
				current = result
				if sessionBeforeCancelled(result) {
					return current
				}
			}
		}
	}
	return current
}

func EmitSessionShutdown(ctx context.Context, runner *Runner, event SessionShutdownEvent) bool {
	if runner == nil || !runner.HasHandlers(EventSessionShutdown) {
		return false
	}
	runner.Emit(ctx, event)
	return true
}

func (runner *Runner) EmitMessageEnd(ctx context.Context, event MessageEndEvent) engine.AgentMessage {
	extensionContext := runner.CreateContext()
	current := event.Message
	modified := false
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventMessageEnd) {
			result, err := callHandler(ctx, handler, MessageEndEvent{Message: current}, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventMessageEnd, err))
				continue
			}
			replacement, ok := eventResult[MessageEndResult](result)
			if !ok || replacement.Message == nil {
				continue
			}
			if role := harness.MessageRole(current); role == "" || role != harness.MessageRole(replacement.Message) {
				runner.emitError(ExtensionError{
					ExtensionPath: extension.Path,
					Event:         string(EventMessageEnd),
					Error:         "message_end handlers must return a message with the same role",
				})
				continue
			}
			current = replacement.Message
			modified = true
		}
	}
	if !modified {
		return nil
	}
	return current
}

func (runner *Runner) EmitToolResult(ctx context.Context, event ToolResultEvent) *ToolResultResult {
	extensionContext := runner.CreateContext()
	current := event
	modified := false
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventToolResult) {
			result, err := callHandler(ctx, handler, current, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventToolResult, err))
				continue
			}
			patch, ok := eventResult[ToolResultResult](result)
			if !ok {
				continue
			}
			if patch.Content != nil {
				current.Content = *patch.Content
				modified = true
			}
			if patch.Details != nil {
				current.Details = *patch.Details
				modified = true
			}
			if patch.IsError != nil {
				current.IsError = *patch.IsError
				modified = true
			}
			if patch.Usage != nil {
				current.Usage = patch.Usage
				modified = true
			}
		}
	}
	if !modified {
		return nil
	}
	content := current.Content
	details := current.Details
	isError := current.IsError
	return &ToolResultResult{Content: &content, Details: &details, IsError: &isError, Usage: current.Usage}
}

func (runner *Runner) inputContext(ctx context.Context) context.Context {
	runner.mu.RLock()
	handler := runner.contextActions.RequestInput
	runner.mu.RUnlock()
	if handler != nil {
		return WithInputHandler(ctx, handler)
	}
	return ctx
}

func (runner *Runner) EmitToolCall(ctx context.Context, event ToolCallEvent) *ToolCallResult {
	extensionContext := runner.CreateContext()
	var current *ToolCallResult
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventToolCall) {
			result, err := callHandler(ctx, handler, event, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventToolCall, err))
				return &ToolCallResult{Block: true, Reason: err.Error()}
			}
			if parsed, ok := eventResult[ToolCallResult](result); ok {
				next := *parsed
				if current != nil {
					next.Approved = next.Approved || current.Approved
				}
				current = &next
				if current.Block {
					return current
				}
			}
		}
	}
	return current
}

func (runner *Runner) EmitUserBashChecked(ctx context.Context, event UserBashEvent) (*UserBashResult, error) {
	extensionContext := runner.CreateContext()
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventUserBash) {
			result, err := callHandler(ctx, handler, event, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventUserBash, err))
				return nil, err
			}
			if parsed, ok := eventResult[UserBashResult](result); ok {
				return parsed, nil
			}
			if result != nil {
				err := errors.New("Invalid user_bash handler result: return nil for local execution or exactly one valid operations or result value") //nolint:staticcheck // Upstream error capitalization is observable.
				runner.emitError(makeExtensionError(extension.Path, EventUserBash, err))
				return nil, err
			}
		}
	}
	return nil, nil
}

// EmitContext runs the request-time transforms in two phases. context
// handlers see the conversation only; an unchanged result keeps every system
// message in place, a changed one gets the current prompt and tool state as
// one leading system message, so pruning cannot drop it. context_with_system
// handlers then see the full transcript and their output is used as returned.
func (runner *Runner) EmitContext(ctx context.Context, messages engine.AgentMessages) engine.AgentMessages {
	extensionContext := runner.CreateContext()
	current := cloneMessages(messages)
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventContext) {
			visible := slices.DeleteFunc(slices.Clone(current), isSystemMessage)
			snapshot := slices.Clone(visible)
			result, err := callHandler(ctx, handler, ContextEvent{Messages: visible}, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventContext, err))
				continue
			}
			returned := visible
			if parsed, ok := eventResult[ContextResult](result); ok && parsed.Messages != nil {
				returned = parsed.Messages
			}
			if reflect.DeepEqual(returned, snapshot) {
				continue
			}
			if head := currentSystemMessage(current); head != nil {
				returned = append(engine.AgentMessages{head}, returned...)
			}
			current = returned
		}
	}
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventContextWithSystem) {
			leading := len(current) > 0 && isSystemMessage(current[0])
			result, err := callHandler(ctx, handler, ContextWithSystemEvent{Messages: current}, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventContextWithSystem, err))
				continue
			}
			if parsed, ok := eventResult[ContextResult](result); ok && parsed.Messages != nil {
				current = parsed.Messages
			}
			// Providers read the prompt and initial tools from the leading system message.
			if leading && (len(current) == 0 || !isSystemMessage(current[0])) {
				runner.emitError(ExtensionError{ExtensionPath: extension.Path, Event: string(EventContextWithSystem),
					Error: "Handler removed the leading system message; the request has no prompt or initial tool declarations. Keep it at index 0 or replace a dropped prefix with getCurrentSystemMessage()."})
			}
		}
	}
	return current
}

func isSystemMessage(message engine.AgentMessage) bool {
	switch message.(type) {
	case *ai.SystemMessage, ai.SystemMessage:
		return true
	}
	return false
}

func currentSystemMessage(messages engine.AgentMessages) engine.AgentMessage {
	list := make(ai.MessageList, 0, len(messages))
	for _, message := range messages {
		switch typed := message.(type) {
		case *ai.SystemMessage:
			list = append(list, typed)
		case ai.SystemMessage:
			list = append(list, &typed)
		}
	}
	if head := ai.CurrentSystemMessage(list); head != nil {
		return head
	}
	return nil
}

// BoundaryDispatch is the outcome of an actionable boundary. Invalid entries
// (the last preview failed to build) discard every proposal.
type BoundaryDispatch struct {
	Entries  []SessionBoundaryDraft
	Continue bool
	Context  BoundaryContextPreview
	Valid    bool
}

// EmitBoundary dispatches turn_end or agent_before_settle: each handler sees
// the entries and continuation chained so far plus their preview, rebuilt by
// buildContext after every handler.
func (runner *Runner) EmitBoundary(
	ctx context.Context,
	base Event,
	buildContext func([]SessionBoundaryDraft) (BoundaryContextPreview, error),
) BoundaryDispatch {
	extensionContext := runner.CreateContext()
	state := BoundaryState{Entries: []SessionBoundaryDraft{}}
	if turnEnd, ok := base.(TurnEndEvent); ok {
		state.Outcome = turnEnd.Outcome
	} else if settle, ok := base.(AgentBeforeSettleEvent); ok {
		state.Outcome = settle.Outcome
	}
	preview, err := buildContext(state.Entries)
	valid := err == nil
	state.Context = preview
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, base.Type()) {
			var event Event
			switch typed := base.(type) {
			case TurnEndEvent:
				typed.BoundaryState = state
				event = typed
			default:
				event = AgentBeforeSettleEvent{BoundaryState: state}
			}
			result, err := callHandler(ctx, handler, event, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, base.Type(), err))
			} else if parsed, ok := eventResult[BoundaryResult](result); ok {
				if parsed.Entries != nil {
					state.Entries = slices.Clone(*parsed.Entries)
				}
				if parsed.Continue != nil {
					state.Continue = *parsed.Continue
				}
			}
			if state.Context, err = buildContext(state.Entries); err != nil {
				valid = false
				runner.emitError(ExtensionError{ExtensionPath: extension.Path, Event: string(base.Type()), Error: "Invalid boundary entries: " + err.Error()})
			} else {
				valid = true
			}
		}
	}
	if !valid {
		return BoundaryDispatch{Entries: []SessionBoundaryDraft{}, Context: state.Context}
	}
	return BoundaryDispatch{Entries: state.Entries, Continue: state.Continue, Context: state.Context, Valid: true}
}

// EmitBoundaryError reports a boundary failure that no single handler owns.
func (runner *Runner) EmitBoundaryError(event EventType, message string) {
	runner.ReportError(ExtensionError{ExtensionPath: "<boundary>", Event: string(event), Error: message})
}

// ReportError reports an extension failure outside event dispatch, such as a
// tool's prepareLoadout hook.
func (runner *Runner) ReportError(extensionError ExtensionError) { runner.emitError(extensionError) }

func (runner *Runner) EmitBeforeProviderRequest(ctx context.Context, payload any) any {
	extensionContext := runner.CreateContext()
	current := payload
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventBeforeProviderRequest) {
			result, err := callHandler(ctx, handler, BeforeProviderRequestEvent{Payload: current}, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventBeforeProviderRequest, err))
				continue
			}
			if parsed, ok := providerRequestResult(result); ok && parsed.Replace {
				current = parsed.Payload
			}
		}
	}
	return current
}

func (runner *Runner) EmitBeforeProviderHeaders(ctx context.Context, headers ai.ProviderHeaders) ai.ProviderHeaders {
	extensionContext := runner.CreateContext()
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventBeforeProviderHeaders) {
			_, err := callHandler(ctx, handler, BeforeProviderHeadersEvent{Headers: headers}, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventBeforeProviderHeaders, err))
			}
		}
	}
	return headers
}

func (runner *Runner) EmitBeforeAgentStart(
	ctx context.Context,
	prompt string,
	images []*ai.ImageContent,
	systemPrompt string,
	options SystemPromptOptions,
) *BeforeAgentStartCombinedResult {
	if options.CustomPrompt == nil && systemPrompt != "" {
		value := systemPrompt
		options.CustomPrompt = &value
	}
	if options.SelectedTools == nil {
		options.SelectedTools = []string{"read", "bash", "edit", "write"}
	}
	if options.ToolSnippets == nil {
		options.ToolSnippets = map[string]string{}
	}
	if options.ToolGuidelines == nil {
		options.ToolGuidelines = map[string][]string{}
	}
	if options.PromptGuidelines == nil {
		options.PromptGuidelines = []string{}
	}
	if options.AppendSystemPrompt == nil {
		empty := ""
		options.AppendSystemPrompt = &empty
	}
	if options.Sections == nil {
		options.Sections = map[string]string{}
	}
	if options.ContextFiles == nil {
		options.ContextFiles = []ContextFile{}
	}
	if options.Skills == nil {
		options.Skills = []Skill{}
	}
	currentPrompt := systemPrompt
	extensionContext := &extensionContext{runner: runner, systemPrompt: func() string { return currentPrompt }}
	var messages []CustomMessage
	modified := false
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventBeforeAgentStart) {
			event := BeforeAgentStartEvent{
				Prompt: prompt, Images: images, SystemPrompt: currentPrompt, SystemPromptOptions: options,
			}
			result, err := callHandler(ctx, handler, event, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventBeforeAgentStart, err))
				continue
			}
			parsed, ok := eventResult[BeforeAgentStartResult](result)
			if !ok {
				continue
			}
			if parsed.Message != nil {
				messages = append(messages, *parsed.Message)
			}
			if parsed.SystemPrompt != nil {
				currentPrompt = *parsed.SystemPrompt
				value := currentPrompt
				options.ForceSystemPrompt = &value
				modified = true
			}
		}
	}
	result := &BeforeAgentStartCombinedResult{Messages: messages, SystemPromptOptions: options}
	if modified {
		result.SystemPrompt = &currentPrompt
	}
	return result
}

func (runner *Runner) EmitResourcesDiscover(ctx context.Context, cwd string, reason ResourcesDiscoverReason) DiscoveredResources {
	extensionContext := runner.CreateContext()
	result := DiscoveredResources{
		SkillPaths: []DiscoveredPath{}, PromptPaths: []DiscoveredPath{}, ThemePaths: []DiscoveredPath{},
	}
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventResourcesDiscover) {
			value, err := callHandler(ctx, handler, ResourcesDiscoverEvent{CWD: cwd, Reason: reason}, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventResourcesDiscover, err))
				continue
			}
			resources, ok := eventResult[ResourcesDiscoverResult](value)
			if !ok {
				continue
			}
			for _, path := range resources.SkillPaths {
				result.SkillPaths = append(result.SkillPaths, DiscoveredPath{Path: path, ExtensionPath: extension.Path})
			}
			for _, path := range resources.PromptPaths {
				result.PromptPaths = append(result.PromptPaths, DiscoveredPath{Path: path, ExtensionPath: extension.Path})
			}
			for _, path := range resources.ThemePaths {
				result.ThemePaths = append(result.ThemePaths, DiscoveredPath{Path: path, ExtensionPath: extension.Path})
			}
		}
	}
	return result
}

func (runner *Runner) EmitInput(
	ctx context.Context,
	text string,
	images []*ai.ImageContent,
	source InputSource,
	streamingBehavior *DeliveryMode,
) InputResult {
	extensionContext := runner.CreateContext()
	currentText := text
	currentImages := images
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventInput) {
			event := InputEvent{
				Text: currentText, Images: currentImages, Source: source, StreamingBehavior: streamingBehavior,
			}
			value, err := callHandler(ctx, handler, event, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventInput, err))
				continue
			}
			result, ok := eventResult[InputResult](value)
			if !ok || result.Action == InputContinue || result.Action == "" {
				continue
			}
			if result.Action == InputHandled {
				return InputResult{Action: InputHandled}
			}
			if result.Action == InputTransform {
				currentText = result.Text
				if result.Images != nil {
					currentImages = result.Images
				}
			}
		}
	}
	if currentText != text || !sameImageSlice(currentImages, images) {
		return InputResult{Action: InputTransform, Text: currentText, Images: currentImages}
	}
	return InputResult{Action: InputContinue}
}

func sameImageSlice(left, right []*ai.ImageContent) bool {
	if len(left) != len(right) {
		return false
	}
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return reflect.ValueOf(left).Pointer() == reflect.ValueOf(right).Pointer()
}

func handlersFor(extension *Extension, event EventType) []Handler {
	extension.mu.RLock()
	handlers := append([]Handler(nil), extension.handlers[event]...)
	extension.mu.RUnlock()
	return handlers
}

func callHandler(ctx context.Context, handler Handler, event Event, extensionContext Context) (result any, err error) {
	if handler == nil {
		return nil, errors.New("extension handler is not callable")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &handlerPanicError{message: fmt.Sprint(recovered), stack: string(debug.Stack())}
		}
	}()
	return handler(ctx, event, extensionContext)
}

type handlerPanicError struct {
	message string
	stack   string
}

func (value *handlerPanicError) Error() string { return value.message }

func makeExtensionError(path string, event EventType, err error) ExtensionError {
	stack := ""
	if err != nil {
		var panicError *handlerPanicError
		if errors.As(err, &panicError) {
			stack = panicError.stack
		} else {
			stack = string(debug.Stack())
		}
	}
	return ExtensionError{ExtensionPath: path, Event: string(event), Error: err.Error(), Stack: stack}
}

func isSessionBeforeEvent(event EventType) bool {
	switch event {
	case EventSessionBeforeSwitch, EventSessionBeforeFork, EventSessionBeforeCompact, EventSessionBeforeTree:
		return true
	default:
		return false
	}
}

func sessionBeforeCancelled(result any) bool {
	if typed, ok := eventResult[SessionBeforeSwitchResult](result); ok {
		return typed.Cancel
	}
	if typed, ok := eventResult[SessionBeforeForkResult](result); ok {
		return typed.Cancel
	}
	if typed, ok := eventResult[SessionBeforeCompactResult](result); ok {
		return typed.Cancel
	}
	if typed, ok := eventResult[SessionBeforeTreeResult](result); ok {
		return typed.Cancel
	}
	return false
}

// eventResult accepts a handler result returned as T or as a non-nil *T.
func eventResult[T any](value any) (*T, bool) {
	switch typed := value.(type) {
	case T:
		return &typed, true
	case *T:
		return typed, typed != nil
	}
	return nil, false
}

func providerRequestResult(value any) (*ProviderRequestResult, bool) {
	switch typed := value.(type) {
	case ProviderRequestResult:
		return &typed, true
	case *ProviderRequestResult:
		return typed, typed != nil
	default:
		if value == nil {
			return nil, false
		}
		return &ProviderRequestResult{Payload: value, Replace: true}, true
	}
}

func cloneMessages(messages engine.AgentMessages) engine.AgentMessages {
	if messages == nil {
		return nil
	}
	result := make(engine.AgentMessages, len(messages))
	for index, message := range messages {
		cloned := cloneReflectValue(reflect.ValueOf(message))
		if cloned.IsValid() {
			result[index] = cloned.Interface()
		}
	}
	return result
}

func cloneReflectValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.New(value.Type()).Elem()
		copy.Set(cloneReflectValue(value.Elem()))
		return copy
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.New(value.Type().Elem())
		copy.Elem().Set(cloneReflectValue(value.Elem()))
		return copy
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			copy.SetMapIndex(iterator.Key(), cloneReflectValue(iterator.Value()))
		}
		return copy
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			copy.Index(index).Set(cloneReflectValue(value.Index(index)))
		}
		return copy
	case reflect.Array:
		copy := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			copy.Index(index).Set(cloneReflectValue(value.Index(index)))
		}
		return copy
	case reflect.Struct:
		copy := reflect.New(value.Type()).Elem()
		copy.Set(value)
		for index := 0; index < value.NumField(); index++ {
			if copy.Field(index).CanSet() && value.Field(index).CanInterface() {
				copy.Field(index).Set(cloneReflectValue(value.Field(index)))
			}
		}
		return copy
	default:
		return value
	}
}
