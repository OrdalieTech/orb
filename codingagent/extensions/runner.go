package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
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
	GetModel               func() *ai.Model
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
	runtime        *runtimeState
	cwd            string
	sessionManager ReadonlySessionManager
	modelRegistry  ModelRegistry

	mu             sync.RWMutex
	ui             UI
	mode           Mode
	contextActions ContextActions
	commandActions CommandActions
	staleMessage   string

	errorMu        sync.RWMutex
	nextErrorID    uint64
	errorListeners map[uint64]func(ExtensionError)

	diagnosticsMu       sync.RWMutex
	shortcutDiagnostics []Diagnostic
	commandDiagnostics  []Diagnostic
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
	runner.BindCore(options.Actions, options.ContextActions)
	if options.CommandActions != nil {
		runner.BindCommandContext(options.CommandActions)
	} else {
		runner.BindCommandContext(nil)
	}
	runner.SetUI(options.UI, options.Mode)
	return runner
}

func (runner *Runner) BindCore(actions Actions, contextActions ContextActions) {
	runner.mu.Lock()
	runner.contextActions = normalizeContextActions(contextActions, runner.cwd)
	runner.mu.Unlock()
	runner.runtime.bind(actions, runner.emitError)
}

func normalizeContextActions(actions ContextActions, cwd string) ContextActions {
	if actions.GetModel == nil {
		actions.GetModel = func() *ai.Model { return nil }
	}
	if actions.IsIdle == nil {
		actions.IsIdle = func() bool { return true }
	}
	if actions.IsProjectTrusted == nil {
		actions.IsProjectTrusted = func() bool { return true }
	}
	if actions.GetSignal == nil {
		actions.GetSignal = func() context.Context { return nil }
	}
	if actions.Abort == nil {
		actions.Abort = func() {}
	}
	if actions.HasPendingMessages == nil {
		actions.HasPendingMessages = func() bool { return false }
	}
	if actions.Shutdown == nil {
		actions.Shutdown = func() {}
	}
	if actions.GetContextUsage == nil {
		actions.GetContextUsage = func() *ContextUsage { return nil }
	}
	if actions.Compact == nil {
		actions.Compact = func(*CompactOptions) {}
	}
	if actions.GetSystemPrompt == nil {
		actions.GetSystemPrompt = func() string { return "" }
	}
	if actions.GetSystemPromptOptions == nil {
		actions.GetSystemPromptOptions = func() SystemPromptOptions { return SystemPromptOptions{CWD: cwd} }
	}
	return actions
}

func (runner *Runner) BindCommandContext(actions *CommandActions) {
	resolved := CommandActions{
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
	}
	if actions != nil {
		if actions.WaitForIdle != nil {
			resolved.WaitForIdle = actions.WaitForIdle
		}
		if actions.NewSession != nil {
			resolved.NewSession = actions.NewSession
		}
		if actions.Fork != nil {
			resolved.Fork = actions.Fork
		}
		if actions.NavigateTree != nil {
			resolved.NavigateTree = actions.NavigateTree
		}
		if actions.SwitchSession != nil {
			resolved.SwitchSession = actions.SwitchSession
		}
		if actions.Reload != nil {
			resolved.Reload = actions.Reload
		}
	}
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
	for _, extension := range runner.extensions {
		extension.mu.RLock()
		for _, name := range extension.flagOrder {
			if _, exists := result[name]; exists {
				continue
			}
			result[name] = extension.flags[name]
		}
		extension.mu.RUnlock()
	}
	return result
}

func (runner *Runner) RegisteredFlags() []Flag {
	seen := make(map[string]struct{})
	var flags []Flag
	for _, extension := range runner.extensions {
		extension.mu.RLock()
		for _, name := range extension.flagOrder {
			if _, exists := seen[name]; exists {
				continue
			}
			flag, exists := extension.flags[name]
			if !exists {
				continue
			}
			seen[name] = struct{}{}
			flags = append(flags, flag)
		}
		extension.mu.RUnlock()
	}
	return flags
}

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
	runner.diagnosticsMu.Lock()
	runner.commandDiagnostics = nil
	runner.diagnosticsMu.Unlock()
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

func (runner *Runner) CommandDiagnostics() []Diagnostic {
	runner.diagnosticsMu.RLock()
	diagnostics := append([]Diagnostic(nil), runner.commandDiagnostics...)
	runner.diagnosticsMu.RUnlock()
	return diagnostics
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
	ids := make([]uint64, 0, len(runner.errorListeners))
	for id := range runner.errorListeners {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	listeners := make([]func(ExtensionError), 0, len(ids))
	for _, id := range ids {
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
	if trustContext == nil {
		trustContext = runner.CreateContext()
	}
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
			decision, ok := projectTrustResult(result)
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
	extensionContext := runner.CreateContext()
	var current any
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, event.Type()) {
			result, err := callHandler(ctx, handler, event, extensionContext)
			if err != nil {
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

func (runner *Runner) EmitMessageEnd(ctx context.Context, event MessageEndEvent) agent.AgentMessage {
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
			replacement, ok := messageEndResult(result)
			if !ok || replacement.Message == nil {
				continue
			}
			currentRole, currentErr := messageRole(current)
			replacementRole, replacementErr := messageRole(replacement.Message)
			if currentErr != nil || replacementErr != nil || currentRole != replacementRole {
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
			patch, ok := toolResultResult(result)
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
		}
	}
	if !modified {
		return nil
	}
	content := current.Content
	details := current.Details
	isError := current.IsError
	return &ToolResultResult{Content: &content, Details: &details, IsError: &isError}
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
			if parsed, ok := toolCallResult(result); ok {
				current = parsed
				if current.Block {
					return current
				}
			}
		}
	}
	return current
}

func (runner *Runner) EmitUserBash(ctx context.Context, event UserBashEvent) *UserBashResult {
	extensionContext := runner.CreateContext()
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventUserBash) {
			result, err := callHandler(ctx, handler, event, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventUserBash, err))
				continue
			}
			if parsed, ok := userBashResult(result); ok {
				return parsed
			}
		}
	}
	return nil
}

func (runner *Runner) EmitContext(ctx context.Context, messages agent.AgentMessages) agent.AgentMessages {
	extensionContext := runner.CreateContext()
	current := cloneMessages(messages)
	for _, extension := range runner.extensions {
		for _, handler := range handlersFor(extension, EventContext) {
			result, err := callHandler(ctx, handler, ContextEvent{Messages: current}, extensionContext)
			if err != nil {
				runner.emitError(makeExtensionError(extension.Path, EventContext, err))
				continue
			}
			if parsed, ok := contextResult(result); ok && parsed.Messages != nil {
				current = parsed.Messages
			}
		}
	}
	return current
}

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
			parsed, ok := beforeAgentStartResult(result)
			if !ok {
				continue
			}
			if parsed.Message != nil {
				messages = append(messages, *parsed.Message)
			}
			if parsed.SystemPrompt != nil {
				currentPrompt = *parsed.SystemPrompt
				modified = true
			}
		}
	}
	if len(messages) == 0 && !modified {
		return nil
	}
	result := &BeforeAgentStartCombinedResult{Messages: messages}
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
			resources, ok := resourcesDiscoverResult(value)
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
			result, ok := inputResult(value)
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
		return nil, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	return handler(ctx, event, extensionContext)
}

func makeExtensionError(path string, event EventType, err error) ExtensionError {
	stack := ""
	if err != nil {
		stack = string(debug.Stack())
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
	switch typed := result.(type) {
	case SessionBeforeSwitchResult:
		return typed.Cancel
	case *SessionBeforeSwitchResult:
		return typed != nil && typed.Cancel
	case SessionBeforeForkResult:
		return typed.Cancel
	case *SessionBeforeForkResult:
		return typed != nil && typed.Cancel
	case SessionBeforeCompactResult:
		return typed.Cancel
	case *SessionBeforeCompactResult:
		return typed != nil && typed.Cancel
	case SessionBeforeTreeResult:
		return typed.Cancel
	case *SessionBeforeTreeResult:
		return typed != nil && typed.Cancel
	default:
		return false
	}
}

func projectTrustResult(value any) (*ProjectTrustResult, bool) {
	switch typed := value.(type) {
	case ProjectTrustResult:
		return &typed, true
	case *ProjectTrustResult:
		return typed, typed != nil
	default:
		return nil, false
	}
}

func messageEndResult(value any) (*MessageEndResult, bool) {
	switch typed := value.(type) {
	case MessageEndResult:
		return &typed, true
	case *MessageEndResult:
		return typed, typed != nil
	default:
		return nil, false
	}
}

func toolResultResult(value any) (*ToolResultResult, bool) {
	switch typed := value.(type) {
	case ToolResultResult:
		return &typed, true
	case *ToolResultResult:
		return typed, typed != nil
	default:
		return nil, false
	}
}

func toolCallResult(value any) (*ToolCallResult, bool) {
	switch typed := value.(type) {
	case ToolCallResult:
		return &typed, true
	case *ToolCallResult:
		return typed, typed != nil
	default:
		return nil, false
	}
}

func userBashResult(value any) (*UserBashResult, bool) {
	switch typed := value.(type) {
	case UserBashResult:
		return &typed, true
	case *UserBashResult:
		return typed, typed != nil
	default:
		return nil, false
	}
}

func contextResult(value any) (*ContextResult, bool) {
	switch typed := value.(type) {
	case ContextResult:
		return &typed, true
	case *ContextResult:
		return typed, typed != nil
	default:
		return nil, false
	}
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

func beforeAgentStartResult(value any) (*BeforeAgentStartResult, bool) {
	switch typed := value.(type) {
	case BeforeAgentStartResult:
		return &typed, true
	case *BeforeAgentStartResult:
		return typed, typed != nil
	default:
		return nil, false
	}
}

func resourcesDiscoverResult(value any) (*ResourcesDiscoverResult, bool) {
	switch typed := value.(type) {
	case ResourcesDiscoverResult:
		return &typed, true
	case *ResourcesDiscoverResult:
		return typed, typed != nil
	default:
		return nil, false
	}
}

func inputResult(value any) (*InputResult, bool) {
	switch typed := value.(type) {
	case InputResult:
		return &typed, true
	case *InputResult:
		return typed, typed != nil
	default:
		return nil, false
	}
}

func messageRole(message agent.AgentMessage) (string, error) {
	switch message.(type) {
	case *ai.UserMessage:
		return "user", nil
	case *ai.AssistantMessage:
		return "assistant", nil
	case *ai.ToolResultMessage:
		return "toolResult", nil
	}
	encoded, err := ai.Marshal(message)
	if err != nil {
		return "", err
	}
	var envelope struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return "", err
	}
	if envelope.Role == "" {
		return "", fmt.Errorf("message has no role")
	}
	return envelope.Role, nil
}

func cloneMessages(messages agent.AgentMessages) agent.AgentMessages {
	if messages == nil {
		return nil
	}
	result := make(agent.AgentMessages, len(messages))
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
