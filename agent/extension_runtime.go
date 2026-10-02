package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
)

type extensionRuntimeState struct {
	mu sync.Mutex

	runner               *extensions.Runner
	baseTools            []engine.AgentTool
	toolRegistry         map[string]engine.AgentTool
	toolInfo             map[string]extensions.ToolInfo
	toolOrder            []string
	lazyGuidelines       map[string][]string
	previousRegistry     map[string]struct{}
	allowed              map[string]struct{}
	hasAllowlist         bool
	excluded             map[string]struct{}
	promptOptions        *SystemPromptOptions
	baseSystemPrompt     string
	systemPromptOverride *string
	resources            extensions.DiscoveredResources
	pendingNextTurn      engine.AgentMessages
	// hiddenDeclarations are active tools whose declarations requests leave
	// out, from prepareLoadout hooks.
	hiddenDeclarations map[string]struct{}
	// pendingTools are tools of the restored or reloaded loadout that are not
	// registered yet, such as MCP tools of servers still connecting: they turn
	// on when registered, and are dropped when SetActiveTools deactivates a
	// tool or the next agent run starts.
	pendingTools    []string
	turnIndex       int
	startEvent      extensions.SessionStartEvent
	started         bool
	config          SessionRuntimeConfig
	shutdownEmitted bool
	shutdownHandler func()
}

func (runtime *SessionRuntime) bindExtensions(runtimeConfig SessionRuntimeConfig) {
	replacingRunner := runtime.extensionState != nil
	state := &extensionRuntimeState{
		baseTools:        append([]engine.AgentTool(nil), runtimeConfig.BaseTools...),
		toolRegistry:     make(map[string]engine.AgentTool),
		toolInfo:         make(map[string]extensions.ToolInfo),
		previousRegistry: make(map[string]struct{}),
		excluded:         make(map[string]struct{}),
		config:           runtimeConfig,
	}
	if len(state.baseTools) == 0 {
		state.baseTools = append([]engine.AgentTool(nil), runtime.agent.State().Tools...)
	}
	// Rebuilt base tools (session replacement, cwd change) need the session
	// environment rebound, mirroring upstream's per-refresh ctx wrapping.
	runtime.bindBashSessionEnvironment(state.baseTools)
	if runtimeConfig.AllowedToolNames != nil {
		state.hasAllowlist = true
		state.allowed = stringSet(*runtimeConfig.AllowedToolNames)
	}
	for _, name := range runtimeConfig.ExcludedToolNames {
		state.excluded[name] = struct{}{}
	}
	if runtimeConfig.SystemPromptOptions != nil {
		copy := cloneSystemPromptOptions(*runtimeConfig.SystemPromptOptions)
		state.promptOptions = &copy
		state.baseSystemPrompt = BuildSystemPrompt(copy)
	} else {
		state.baseSystemPrompt = cmp.Or(runtimeConfig.SystemPrompt, runtime.agent.State().SystemPrompt)
	}
	runtime.extensionState = state
	registerProvider := runtimeConfig.RegisterProvider
	registerProviderConfig := runtimeConfig.RegisterProviderConfig
	unregisterProvider := runtimeConfig.UnregisterProvider
	if runtimeConfig.ModelRegistry != nil {
		if registerProvider != nil {
			register := registerProvider
			registerProvider = func(provider extensions.Provider) error {
				if err := register(provider); err != nil {
					return err
				}
				runtime.refreshCurrentModelFromRegistry(runtimeConfig.ModelRegistry)
				return nil
			}
		}
		if registerProviderConfig != nil {
			register := registerProviderConfig
			registerProviderConfig = func(name string, config extensions.ProviderConfig) error {
				if err := register(name, config); err != nil {
					return err
				}
				runtime.refreshCurrentModelFromRegistry(runtimeConfig.ModelRegistry)
				return nil
			}
		}
		if unregisterProvider != nil {
			unregister := unregisterProvider
			unregisterProvider = func(name string) error {
				if err := unregister(name); err != nil {
					return err
				}
				runtime.refreshCurrentModelFromRegistry(runtimeConfig.ModelRegistry)
				return nil
			}
		}
	}

	actions := extensions.Actions{
		SendMessage:     runtime.sendExtensionMessage,
		SendUserMessage: runtime.sendExtensionUserMessage,
		AppendEntry:     runtime.appendExtensionEntry,
		SetSessionName:  runtime.setExtensionSessionName,
		GetSessionName:  func(context.Context) (*string, error) { return runtime.manager.GetSessionName(), nil },
		SetLabel: func(_ context.Context, id string, label *string) error {
			_, err := runtime.manager.AppendLabelChange(id, label)
			return err
		},
		GetActiveTools:         runtime.extensionActiveTools,
		GetAllTools:            runtime.extensionAllTools,
		SetActiveTools:         runtime.setActiveToolsByName,
		RefreshTools:           func() { runtime.refreshExtensionTools(nil, false) },
		GetCommands:            runtime.extensionCommands,
		SetModel:               runtime.setExtensionModel,
		GetThinkingLevel:       func() (engine.ThinkingLevel, error) { return runtime.agent.State().ThinkingLevel, nil },
		SetThinkingLevel:       runtime.setExtensionThinkingLevel,
		RegisterProvider:       registerProvider,
		RegisterProviderConfig: registerProviderConfig,
		UnregisterProvider:     unregisterProvider,
	}
	contextActions := extensions.ContextActions{
		RequestInput: runtime.RequestInput,
		GetModel:     func() *ai.Model { return runtime.agent.State().Model },
		GetScopedModels: func() []extensions.ScopedModel {
			scoped := runtime.ScopedModels()
			result := make([]extensions.ScopedModel, len(scoped))
			for index, model := range scoped {
				result[index] = extensions.ScopedModel{Model: model.Model, ThinkingLevel: model.ThinkingLevel}
			}
			return result
		},
		IsIdle:           runtime.IsIdle,
		IsProjectTrusted: runtime.settings.IsProjectTrusted,
		GetSignal:        runtime.agent.Signal,
		Abort:            runtime.Abort,
		HasPendingMessages: func() bool {
			state.mu.Lock()
			defer state.mu.Unlock()
			return len(state.pendingNextTurn) > 0 || runtime.agent.HasQueuedMessages()
		},
		Shutdown: func() {
			state.mu.Lock()
			handler := state.shutdownHandler
			state.mu.Unlock()
			if handler != nil {
				handler()
			}
		},
		GetContextUsage: func() *extensions.ContextUsage {
			usage := runtime.GetContextUsage()
			if usage == nil {
				return nil
			}
			return &extensions.ContextUsage{Tokens: usage.Tokens, ContextWindow: int64(usage.ContextWindow), Percent: usage.Percent}
		},
		Compact: func(options *extensions.CompactOptions) {
			go func() {
				instructions := ""
				if options != nil {
					instructions = options.CustomInstructions
				}
				result, err := runtime.Compact(context.Background(), instructions)
				if err != nil {
					if options != nil && options.OnError != nil {
						options.OnError(err)
					}
					return
				}
				if options != nil && options.OnComplete != nil {
					options.OnComplete(*result)
				}
			}()
		},
		GetSystemPrompt:        runtime.systemPrompt,
		GetSystemPromptOptions: runtime.extensionSystemPromptOptions,
	}
	commandActions := runtime.runtimeCommandActions()
	runner := extensions.NewRunner(runtimeConfig.ExtensionRegistry, extensions.RunnerOptions{
		CWD: runtime.manager.GetCWD(), SessionManager: runtime.manager, ModelRegistry: runtimeConfig.ModelRegistry,
		Mode: runtimeConfig.ExtensionMode, UI: runtimeConfig.ExtensionUI, Actions: actions,
		ContextActions: contextActions, CommandActions: &commandActions, ErrorHandler: runtimeConfig.ExtensionErrorHandler,
	})
	state.runner = runner
	runner.SetToolCallHost(&extensions.ToolCallHost{Tools: runtime.nestedLoadout, Execute: runtime.executeNestedTool})

	if replacingRunner {
		runtime.agent.SetTransformContext(nil)
		runtime.agent.SetToolCallHooks(nil, runtime.afterExtensionToolCall)
		runtime.agent.SetProviderHooks(nil, nil, nil, nil)
	}
	if runner.HasHandlers(extensions.EventContext) || runner.HasHandlers(extensions.EventContextWithSystem) {
		runtime.agent.SetTransformContext(func(ctx context.Context, messages engine.AgentMessages) (engine.AgentMessages, error) {
			return runner.EmitContext(ctx, messages), nil
		})
	}
	if runner.HasHandlers(extensions.EventToolCall) {
		runtime.agent.SetToolCallHooks(runtime.beforeExtensionToolCall, runtime.afterExtensionToolCall)
	}
	if runner.HasHandlers(extensions.EventBeforeProviderRequest) || runner.HasHandlers(extensions.EventBeforeProviderHeaders) ||
		runner.HasHandlers(extensions.EventAfterProviderResponse) || runner.HasHandlers(extensions.EventProviderStreamEvent) {
		runtime.agent.SetProviderHooks(
			func(ctx context.Context, payload any, _ *ai.Model) (any, bool, error) {
				return runner.EmitBeforeProviderRequest(ctx, payload), true, nil
			},
			func(ctx context.Context, headers ai.ProviderHeaders, _ *ai.Model) (ai.ProviderHeaders, error) {
				return runner.EmitBeforeProviderHeaders(ctx, headers), nil
			},
			func(ctx context.Context, response ai.ProviderResponse, _ *ai.Model) error {
				runner.Emit(ctx, extensions.AfterProviderResponseEvent{Status: response.Status, Headers: response.Headers})
				return nil
			},
			func(ctx context.Context, data json.RawMessage, model *ai.Model) {
				if runner.HasHandlers(extensions.EventProviderStreamEvent) {
					runner.Emit(ctx, extensions.ProviderStreamEvent{Provider: model.Provider, API: model.API, Model: model.ID, Data: data})
				}
			},
		)
	}

	initial := runtimeConfig.InitialActiveToolNames
	if initial == nil {
		for _, tool := range runtime.agent.State().Tools {
			initial = append(initial, tool.Spec().Name)
		}
	}
	state.pendingTools = slices.Clone(initial)
	runtime.refreshExtensionTools(initial, true)
	startEvent := extensions.SessionStartEvent{Reason: extensions.SessionStartStartup}
	if runtimeConfig.SessionStartEvent != nil {
		startEvent = *runtimeConfig.SessionStartEvent
	} else if runtimeConfig.SessionStart != nil {
		startEvent = *runtimeConfig.SessionStart
	}
	state.startEvent = startEvent
	if !runtimeConfig.DeferExtensionStart && !runtimeConfig.DeferSessionStart {
		_ = runtime.BindExtensions(context.Background())
	}
}

// SetExtensionShutdownHandler installs the mode-specific behavior for an
// extension's ctx.shutdown(). Upstream leaves it unset outside interactive and
// RPC, where shutdown is a no-op.
func (runtime *SessionRuntime) SetExtensionShutdownHandler(handler func()) {
	if runtime == nil || runtime.extensionState == nil {
		return
	}
	state := runtime.extensionState
	state.mu.Lock()
	state.shutdownHandler = handler
	state.mu.Unlock()
}

// BindExtensions activates the session's extension instance and emits its
// configured session_start event once.
func (runtime *SessionRuntime) BindExtensions(ctx context.Context) error {
	if runtime == nil || runtime.extensionState == nil {
		return nil
	}
	state := runtime.extensionState
	state.mu.Lock()
	if state.started || state.shutdownEmitted {
		state.mu.Unlock()
		return nil
	}
	state.started = true
	startEvent := state.startEvent
	runner := state.runner
	state.mu.Unlock()
	if runner == nil {
		return nil
	}
	runner.Emit(ctx, startEvent)
	if runner.HasHandlers(extensions.EventResourcesDiscover) {
		discoverReason := extensions.ResourcesDiscoverStartup
		if startEvent.Reason == extensions.SessionStartReload {
			discoverReason = extensions.ResourcesDiscoverReload
		}
		resources := runner.EmitResourcesDiscover(context.Background(), runtime.manager.GetCWD(), discoverReason)
		state.mu.Lock()
		state.resources = resources
		state.mu.Unlock()
		runtime.extendResourcesFromExtensions(resources)
	}
	runtime.syncExtensionCommands()
	return nil
}

// BindExtensionUI installs the extension UI seam on the active runner and in
// the stored runner configuration, so /reload rebuilds keep it. Upstream
// rpc-mode rebindSession passes its uiContext into bindExtensions on every
// rebind (rpc-mode.ts:311-320); this is the equivalent seam for Go hosts.
func (runtime *SessionRuntime) BindExtensionUI(ui extensions.UI, mode extensions.Mode) {
	if runtime == nil || runtime.extensionState == nil {
		return
	}
	state := runtime.extensionState
	state.mu.Lock()
	state.config.ExtensionUI = ui
	if mode != "" {
		state.config.ExtensionMode = mode
	}
	runner := state.runner
	mode = state.config.ExtensionMode
	state.mu.Unlock()
	if runner != nil {
		runner.SetUI(ui, mode)
	}
}

func (runtime *SessionRuntime) runtimeCommandActions() extensions.CommandActions {
	return extensions.CommandActions{
		WaitForIdle: runtime.WaitForIdle,
		NavigateTree: func(ctx context.Context, targetID string, options *extensions.NavigateTreeOptions) (extensions.SessionReplacementResult, error) {
			resolved := NavigateTreeOptions{}
			if options != nil {
				resolved = NavigateTreeOptions{
					Summarize: options.Summarize, CustomInstructions: options.CustomInstructions,
					ReplaceInstructions: options.ReplaceInstructions, Label: options.Label,
				}
			}
			result, err := runtime.NavigateTree(ctx, targetID, resolved)
			return extensions.SessionReplacementResult{Cancelled: result.Cancelled || result.Aborted}, err
		},
	}
}

func (runtime *SessionRuntime) BindHostCommandActions(actions extensions.CommandActions) {
	if runtime == nil || runtime.extensionState == nil || runtime.extensionState.runner == nil {
		return
	}
	merged := runtime.runtimeCommandActions()
	merged.NewSession = actions.NewSession
	merged.Fork = actions.Fork
	merged.SwitchSession = actions.SwitchSession
	merged.Reload = actions.Reload
	if actions.WaitForIdle != nil {
		merged.WaitForIdle = actions.WaitForIdle
	}
	if actions.NavigateTree != nil {
		merged.NavigateTree = actions.NavigateTree
	}
	runtime.extensionState.runner.BindCommandContext(&merged)
}

// StartExtensions activates a deferred session after the TUI has attached its
// UI implementation and event subscription.
func (runtime *SessionRuntime) StartExtensions() {
	_ = runtime.BindExtensions(context.Background())
}

// Reload rebuilds the session's native extension instance from its registered
// factories, then emits the reload lifecycle on the fresh context.
func (runtime *SessionRuntime) Reload(ctx context.Context) error {
	if runtime.beginReload != nil {
		if err := runtime.beginReload(); err != nil {
			return err
		}
		if runtime.endReload != nil {
			defer runtime.endReload()
		}
	}
	if err := runtime.reloadExtensions(ctx); err != nil {
		return err
	}
	if runtime.reloadPrepared != nil {
		if err := runtime.reloadPrepared(); err != nil {
			return err
		}
	}
	return runtime.BindExtensions(ctx)
}

func (runtime *SessionRuntime) reloadExtensions(ctx context.Context) error {
	if runtime == nil || runtime.extensionState == nil {
		return nil
	}
	if err := runtime.WaitForIdle(ctx); err != nil {
		return err
	}
	state := runtime.extensionState
	state.mu.Lock()
	runner := state.runner
	configuration := state.config
	state.mu.Unlock()
	flagValues := map[string]any{}
	if runner != nil {
		flagValues = runner.FlagValues()
	}
	activeTools := runtime.agent.State().Tools
	configuration.InitialActiveToolNames = make([]string, 0, len(activeTools))
	for _, tool := range activeTools {
		configuration.InitialActiveToolNames = append(configuration.InitialActiveToolNames, tool.Spec().Name)
	}
	extensions.EmitSessionShutdown(ctx, runner, extensions.SessionShutdownEvent{Reason: extensions.SessionShutdownReload})
	if runner != nil {
		runner.Invalidate("")
	}
	previousDefaults := runtime.defaultToolNames(configuration.UsesDefaultTools)
	runtime.settings.Reload()
	// Tools newly added to defaultTools turn on; removed ones stay active, and
	// tools turned off during the session stay off unless newly added.
	for _, name := range runtime.defaultToolNames(configuration.UsesDefaultTools) {
		if !slices.Contains(previousDefaults, name) && !slices.Contains(configuration.InitialActiveToolNames, name) {
			configuration.InitialActiveToolNames = append(configuration.InitialActiveToolNames, name)
		}
	}
	var registry *extensions.Registry
	if loader := runtime.ResourceLoader(); loader != nil {
		loaderOwnedRegistry := configuration.ExtensionRegistry == loader.GetExtensions()
		if err := loader.Reload(ctx, nil); err != nil {
			return err
		}
		if loaderOwnedRegistry {
			registry = loader.GetExtensions()
		} else {
			var err error
			registry, err = configuration.ExtensionRegistry.Fresh(runtime.manager.GetCWD())
			if err != nil {
				return err
			}
		}
		base := cloneSlashResolver(runtime.baseSlashResolver)
		if base == nil {
			base = &SlashResolver{}
		}
		base.Skills = loader.GetSkills().Skills
		base.PromptTemplates = loader.GetPrompts().Prompts
		runtime.baseSlashResolver = base
	} else {
		var err error
		registry, err = configuration.ExtensionRegistry.Fresh(runtime.manager.GetCWD())
		if err != nil {
			return err
		}
	}
	if registry == nil {
		registry = extensions.NewRegistry(runtime.manager.GetCWD())
	}
	for name, value := range flagValues {
		registry.SetFlagValue(name, value)
	}
	if configuration.RebuildBaseTools != nil {
		baseTools, rebuildErr := configuration.RebuildBaseTools()
		if rebuildErr != nil {
			return rebuildErr
		}
		configuration.BaseTools = baseTools
	}
	runtime.agent.SetSteeringMode(engine.QueueMode(runtime.settings.GetSteeringMode()))
	runtime.agent.SetFollowUpMode(engine.QueueMode(runtime.settings.GetFollowUpMode()))
	runtime.mu.Lock()
	runtime.autoCompaction = runtime.settings.GetCompactionSettings().Enabled
	runtime.autoRetry = runtime.settings.GetRetrySettings().Enabled
	runtime.mu.Unlock()
	configuration.ExtensionRegistry = registry
	runtime.slashResolver = cloneSlashResolver(runtime.baseSlashResolver)
	configuration.SlashResolver = runtime.slashResolver
	configuration.SessionStartEvent = &extensions.SessionStartEvent{Reason: extensions.SessionStartReload}
	configuration.DeferExtensionStart = true
	runtime.bindExtensions(configuration)
	return nil
}

func (runtime *SessionRuntime) ShutdownExtensions(reason extensions.SessionShutdownReason, target *string) {
	if runtime == nil || runtime.extensionState == nil {
		return
	}
	state := runtime.extensionState
	state.mu.Lock()
	if state.shutdownEmitted {
		state.mu.Unlock()
		return
	}
	state.shutdownEmitted = true
	started := state.started
	runner := state.runner
	state.mu.Unlock()
	if !started || runner == nil {
		return
	}
	extensions.EmitSessionShutdown(context.Background(), runner, extensions.SessionShutdownEvent{Reason: reason, TargetSessionFile: target})
}

func (runtime *SessionRuntime) refreshCurrentModelFromRegistry(registry extensions.ModelRegistry) {
	current := runtime.agent.State().Model
	if current == nil {
		return
	}
	refreshed, ok := registry.Find(string(current.Provider), current.ID)
	if ok {
		runtime.agent.SetModel(&refreshed)
	}
}

// RefreshCurrentModelFromRegistry applies provider-dependent model projection
// changes after an in-place auth refresh without recording a model switch.
func (runtime *SessionRuntime) RefreshCurrentModelFromRegistry(registry extensions.ModelRegistry) {
	if runtime == nil || registry == nil {
		return
	}
	runtime.refreshCurrentModelFromRegistry(registry)
}

func (runtime *SessionRuntime) disposeExtensions(emitShutdown bool) {
	state := runtime.extensionState
	if state == nil || state.runner == nil {
		return
	}
	if emitShutdown {
		runtime.ShutdownExtensions(extensions.SessionShutdownQuit, nil)
	} else {
		state.mu.Lock()
		state.shutdownEmitted = true
		state.mu.Unlock()
	}
	state.runner.Invalidate("")
}

func (runtime *SessionRuntime) ExtensionRunner() *extensions.Runner {
	if runtime == nil || runtime.extensionState == nil {
		return nil
	}
	return runtime.extensionState.runner
}

// GetToolDefinition mirrors upstream AgentSession.getToolDefinition for
// extension tools: the registered ToolDefinition (including renderCall and
// renderResult) for name, or nil for built-in, unknown, or disallowed tools.
func (runtime *SessionRuntime) GetToolDefinition(name string) *extensions.ToolDefinition {
	if runtime == nil || runtime.extensionState == nil {
		return nil
	}
	// allowed/excluded are written only at bind time, so no lock is needed.
	state := runtime.extensionState
	if state.runner == nil || !state.toolAllowed(name) {
		return nil
	}
	return state.runner.ToolDefinition(name)
}

// RegisteredTool returns the configured agent.AgentTool for name — built-in or
// extension-wrapped, active or not. Renderers type-assert built-ins for their
// render seams (tools.PlainTextRenderer) instead of duplicating definitions.
func (runtime *SessionRuntime) RegisteredTool(name string) engine.AgentTool {
	if runtime == nil {
		return nil
	}
	if state := runtime.extensionState; state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.toolRegistry[name]
	}
	for _, tool := range runtime.agent.State().Tools {
		if tool.Spec().Name == name {
			return tool
		}
	}
	return nil
}

func (runtime *SessionRuntime) ExtensionResources() extensions.DiscoveredResources {
	if runtime == nil || runtime.extensionState == nil {
		return extensions.DiscoveredResources{}
	}
	runtime.extensionState.mu.Lock()
	defer runtime.extensionState.mu.Unlock()
	return cloneDiscoveredResources(runtime.extensionState.resources)
}

func (runtime *SessionRuntime) extendResourcesFromExtensions(resources extensions.DiscoveredResources) {
	if runtime.slashResolver == nil {
		runtime.slashResolver = &SlashResolver{}
	}
	cwd := runtime.manager.GetCWD()
	if loader := runtime.ResourceLoader(); loader != nil {
		loader.ExtendResources(ResourceExtensionPaths{
			SkillPaths:  resourcePathsFromExtensions(cwd, resources.SkillPaths),
			PromptPaths: resourcePathsFromExtensions(cwd, resources.PromptPaths),
			ThemePaths:  resourcePathsFromExtensions(cwd, resources.ThemePaths),
		})
		runtime.applyDiscoveredSlashResources(loader.GetSkills().Skills, loader.GetPrompts().Prompts)
		return
	}
	agentDir := DefaultAgentDir()
	skillInputs := []LoadSkillsResult{{Skills: append([]Skill(nil), runtime.slashResolver.Skills...)}}
	for _, entry := range resources.SkillPaths {
		loaded := LoadSkills(LoadSkillsOptions{CWD: cwd, AgentDir: agentDir, SkillPaths: []string{entry.Path}})
		source, baseDir := extensionResourceMetadata(cwd, entry.ExtensionPath)
		for index := range loaded.Skills {
			loaded.Skills[index].SourceInfo = SourceInfo{
				Path: loaded.Skills[index].FilePath, Source: source, Scope: "temporary",
				Origin: "top-level", BaseDir: baseDir,
			}
		}
		skillInputs = append(skillInputs, loaded)
	}
	mergedSkills := combineSkills(skillInputs).Skills

	promptInputs := [][]PromptTemplate{append([]PromptTemplate(nil), runtime.slashResolver.PromptTemplates...)}
	for _, entry := range resources.PromptPaths {
		loaded, _ := LoadPromptTemplates(LoadPromptTemplatesOptions{CWD: cwd, AgentDir: agentDir, PromptPaths: []string{entry.Path}})
		source, baseDir := extensionResourceMetadata(cwd, entry.ExtensionPath)
		for index := range loaded {
			loaded[index].SourceInfo = SourceInfo{
				Path: loaded[index].FilePath, Source: source, Scope: "temporary",
				Origin: "top-level", BaseDir: baseDir,
			}
		}
		promptInputs = append(promptInputs, loaded)
	}
	mergedPrompts, _ := combinePrompts(promptInputs)
	runtime.applyDiscoveredSlashResources(mergedSkills, mergedPrompts)
}

func (runtime *SessionRuntime) applyDiscoveredSlashResources(skills []Skill, prompts []PromptTemplate) {
	runtime.slashResolver.Skills = append([]Skill(nil), skills...)
	runtime.slashResolver.PromptTemplates = append([]PromptTemplate(nil), prompts...)

	state := runtime.extensionState
	if state == nil {
		return
	}
	state.mu.Lock()
	if state.promptOptions != nil {
		options := cloneSystemPromptOptions(*state.promptOptions)
		options.Skills = append([]Skill(nil), skills...)
		state.promptOptions = &options
		state.baseSystemPrompt = BuildSystemPrompt(options)
	}
	state.mu.Unlock()
}

func resourcePathsFromExtensions(cwd string, paths []extensions.DiscoveredPath) []ResourcePath {
	result := make([]ResourcePath, 0, len(paths))
	for _, entry := range paths {
		source, baseDir := extensionResourceMetadata(cwd, entry.ExtensionPath)
		result = append(result, ResourcePath{Path: entry.Path, Metadata: PathMetadata{
			Source: source, Scope: "temporary", Origin: "top-level", BaseDir: baseDir,
		}})
	}
	return result
}

func extensionResourceMetadata(cwd, extensionPath string) (source, baseDir string) {
	if strings.HasPrefix(extensionPath, "<") {
		name := strings.NewReplacer("<", "", ">", "").Replace(extensionPath)
		return "extension:" + name, ""
	}
	name := filepath.Base(extensionPath)
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".ts"), ".js")
	return "extension:" + name, filepath.Dir(resolveResourcePathFrom(extensionPath, cwd))
}

func sourceInfoFromExtension(info extensions.SourceInfo) SourceInfo {
	baseDir := ""
	if info.BaseDir != nil {
		baseDir = *info.BaseDir
	}
	return SourceInfo{
		Path: info.Path, Source: info.Source, Scope: string(info.Scope), Origin: string(info.Origin), BaseDir: baseDir,
	}
}

func sourceInfoToExtension(info SourceInfo) extensions.SourceInfo {
	var baseDir *string
	if info.BaseDir != "" {
		value := info.BaseDir
		baseDir = &value
	}
	return extensions.SourceInfo{
		Path: info.Path, Source: info.Source, Scope: extensions.SourceScope(info.Scope),
		Origin: extensions.SourceOrigin(info.Origin), BaseDir: baseDir,
	}
}

func (runtime *SessionRuntime) refreshExtensionTools(active []string, includeAll bool) {
	state := runtime.extensionState
	if state == nil || state.runner == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	previousActive := runtime.agent.State().Tools
	previousNames := make([]string, 0, len(previousActive))
	for _, tool := range previousActive {
		previousNames = append(previousNames, tool.Spec().Name)
	}
	if active == nil {
		active = previousNames
	} else {
		active = append([]string(nil), active...)
	}
	previousRegistry := state.previousRegistry

	registry := make(map[string]engine.AgentTool)
	info := make(map[string]extensions.ToolInfo)
	order := make([]string, 0, len(state.baseTools))
	for _, tool := range state.baseTools {
		spec := tool.Spec()
		if !state.toolAllowed(spec.Name) {
			continue
		}
		registry[spec.Name] = tool
		order = append(order, spec.Name)
		info[spec.Name] = extensions.ToolInfo{
			Name: spec.Name, Description: spec.Description, Parameters: spec.Parameters, Exposure: extensions.ToolDirect,
			SourceInfo: extensions.SourceInfo{Path: extensions.BuiltinPathPrefix + spec.Name, Source: "builtin", Scope: extensions.SourceScopeTemporary, Origin: extensions.SourceOriginTopLevel},
		}
	}
	extensionNames := make([]string, 0)
	for _, registered := range state.runner.AllRegisteredTools() {
		name := registered.Definition.Name
		if !state.toolAllowed(name) {
			continue
		}
		if _, exists := registry[name]; !exists {
			order = append(order, name)
		}
		registry[name] = extensions.WrapRegisteredTool(registered, state.runner)
		info[name] = extensions.ToolInfo{
			Name: name, Description: registered.Definition.Description, Parameters: registered.Definition.Parameters,
			PromptGuidelines: append([]string(nil), registered.Definition.PromptGuidelines...), Exposure: registered.Definition.EffectiveExposure(),
			Namespace: registered.Definition.Namespace, Annotations: registered.Definition.Annotations, SourceInfo: registered.SourceInfo,
		}
		extensionNames = append(extensionNames, name)
	}
	state.toolRegistry = registry
	state.toolInfo = info
	state.toolOrder = order
	state.previousRegistry = make(map[string]struct{}, len(registry))
	for name := range registry {
		state.previousRegistry[name] = struct{}{}
	}

	if state.hasAllowlist {
		for _, name := range order {
			if _, allowed := state.allowed[name]; allowed {
				active = append(active, name)
			}
		}
	} else if includeAll {
		for _, name := range extensionNames {
			if state.activatedOnRegistration(name) {
				active = append(active, name)
			}
		}
	} else {
		for _, name := range order {
			if _, existed := previousRegistry[name]; !existed && state.activatedOnRegistration(name) {
				active = append(active, name)
			}
		}
	}
	// Pending tools that are registered now turn on.
	active = append(active, state.pendingTools...)
	runtime.setActiveToolsLocked(uniqueStrings(active), state)
}

// exposure is how the model reaches a registered tool; built-ins are direct.
func (state *extensionRuntimeState) exposure(name string) extensions.ToolExposure {
	if info, ok := state.toolInfo[name]; ok && info.Exposure != "" {
		return info.Exposure
	}
	return extensions.ToolDirect
}

// activatedOnRegistration: direct and model-only tools turn on when
// registered unless they opt out with DefaultActive false.
func (state *extensionRuntimeState) activatedOnRegistration(name string) bool {
	exposure := state.exposure(name)
	if exposure != extensions.ToolDirect && exposure != extensions.ToolModelOnly {
		return false
	}
	definition := state.runner.ToolDefinition(name)
	return definition == nil || definition.DefaultActive == nil || *definition.DefaultActive
}

// callableTools are the tools reachable through ctx.ExecuteTool: the active
// direct tools and every registered deferred tool.
func (state *extensionRuntimeState) callableTools(active map[string]struct{}) []engine.AgentTool {
	var callable []engine.AgentTool
	for _, name := range state.toolOrder {
		_, isActive := active[name]
		if exposure := state.exposure(name); exposure == extensions.ToolDeferred || exposure == extensions.ToolDirect && isActive {
			callable = append(callable, state.toolRegistry[name])
		}
	}
	return callable
}

// applyLoadoutHooks runs the prepareLoadout hooks of the active tools: they
// may rewrite declared descriptions and hide declarations from requests.
func (runtime *SessionRuntime) applyLoadoutHooks(state *extensionRuntimeState, active []engine.AgentTool) []engine.AgentTool {
	state.hiddenDeclarations = nil
	activeNames := make(map[string]struct{}, len(active))
	var hooks []*extensions.ToolDefinition
	for _, tool := range active {
		name := tool.Spec().Name
		activeNames[name] = struct{}{}
		if definition := state.runner.ToolDefinition(name); definition != nil && definition.PrepareLoadout != nil {
			hooks = append(hooks, definition)
		}
	}
	if len(hooks) == 0 {
		return active
	}
	listed := func(tool engine.AgentTool) extensions.LoadoutTool {
		spec := tool.Spec()
		return extensions.LoadoutTool{Name: spec.Name, Label: spec.Label, Description: spec.Description, Parameters: spec.Parameters}
	}
	specs := func(tools []engine.AgentTool) []extensions.LoadoutTool {
		result := make([]extensions.LoadoutTool, 0, len(tools))
		for _, tool := range tools {
			result = append(result, listed(tool))
		}
		return result
	}
	loadout := extensions.ToolLoadout{Declared: specs(active), Callable: specs(state.callableTools(activeNames)), Exposures: map[string]extensions.ToolExposure{}, Namespaces: map[string]extensions.ToolNamespace{}}
	for _, name := range state.toolOrder {
		loadout.Registered = append(loadout.Registered, listed(state.toolRegistry[name]))
		loadout.Exposures[name] = state.exposure(name)
		if namespace := state.toolInfo[name].Namespace; namespace != nil {
			loadout.Namespaces[name] = *namespace
		}
	}
	descriptions := map[string]string{}
	hidden := map[string]struct{}{}
	for _, definition := range hooks {
		changes, err := callLoadoutHook(definition.PrepareLoadout, loadout)
		if err != nil {
			state.runner.ReportError(extensions.ExtensionError{ExtensionPath: state.toolInfo[definition.Name].SourceInfo.Path, Event: "prepare_loadout", Error: err.Error()})
			continue
		}
		if changes != nil {
			maps.Copy(descriptions, changes.Descriptions)
			for _, name := range changes.HiddenDeclarations {
				hidden[name] = struct{}{}
			}
		}
	}
	state.hiddenDeclarations = hidden
	declared := make([]engine.AgentTool, len(active))
	for index, tool := range active {
		declared[index] = tool
		if description, ok := descriptions[tool.Spec().Name]; ok {
			declared[index] = describedTool{AgentTool: tool, description: description}
		}
	}
	return declared
}

func callLoadoutHook(hook func(extensions.ToolLoadout) *extensions.ToolLoadoutChanges, loadout extensions.ToolLoadout) (changes *extensions.ToolLoadoutChanges, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	return hook(loadout), nil
}

// describedTool declares a tool with the description a prepareLoadout hook gave it.
type describedTool struct {
	engine.AgentTool
	description string
}

func (tool describedTool) Spec() engine.AgentToolSpec {
	spec := tool.AgentTool.Spec()
	spec.Description = tool.description
	return spec
}

func (tool describedTool) PrepareParallelExecution(ctx context.Context, args any) (context.Context, func(), error) {
	if preparer, ok := tool.AgentTool.(engine.ParallelExecutionPreparer); ok {
		return preparer.PrepareParallelExecution(ctx, args)
	}
	return ctx, func() {}, nil
}

// hideDeclarations leaves the declarations prepareLoadout hooks hide out of a
// request; the whole transcript is filtered with the current set, so
// declarations change only when the loadout does.
func (runtime *SessionRuntime) hideDeclarations(messages engine.AgentMessages) engine.AgentMessages {
	state := runtime.extensionState
	if state == nil {
		return messages
	}
	state.mu.Lock()
	hidden := state.hiddenDeclarations
	state.mu.Unlock()
	if len(hidden) == 0 {
		return messages
	}
	result := make(engine.AgentMessages, len(messages))
	for index, message := range messages {
		result[index] = message
		system, ok := message.(*ai.SystemMessage)
		if !ok || len(system.ToolsAdded) == 0 && len(system.ToolsRemoved) == 0 {
			continue
		}
		filtered := *system
		filtered.ToolsAdded = slices.DeleteFunc(slices.Clone(system.ToolsAdded), func(tool ai.Tool) bool { _, hide := hidden[tool.Name]; return hide })
		filtered.ToolsRemoved = slices.DeleteFunc(slices.Clone(system.ToolsRemoved), func(tool ai.ToolReference) bool { _, hide := hidden[tool.Name]; return hide })
		result[index] = &filtered
	}
	return result
}

func (state *extensionRuntimeState) toolAllowed(name string) bool {
	if _, excluded := state.excluded[name]; excluded {
		return false
	}
	if !state.hasAllowlist {
		return true
	}
	_, allowed := state.allowed[name]
	return allowed
}

func (runtime *SessionRuntime) setActiveToolsByName(names []string) error {
	state := runtime.extensionState
	if state == nil {
		return errors.New("agent: extension runtime is not bound")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	previous := runtime.agent.State().Tools
	runtime.setActiveToolsLocked(names, state)
	// A loadout that deactivates a tool replaces the restored one and drops its
	// pending tools; one that only adds tools, like tool_search, keeps them.
	active := runtime.agent.State().Tools
	for _, tool := range previous {
		if !slices.ContainsFunc(active, func(other engine.AgentTool) bool { return other.Spec().Name == tool.Spec().Name }) {
			state.pendingTools = nil
			break
		}
	}
	return nil
}

func (runtime *SessionRuntime) setActiveToolsLocked(names []string, state *extensionRuntimeState) {
	active := make([]engine.AgentTool, 0, len(names))
	valid := make([]string, 0, len(names))
	for _, name := range names {
		// Hidden tools are registered but never active.
		if tool := state.toolRegistry[name]; tool != nil && state.exposure(name) != extensions.ToolHidden {
			active = append(active, tool)
			valid = append(valid, name)
		}
	}
	state.pendingTools = slices.DeleteFunc(state.pendingTools, func(name string) bool { return slices.Contains(valid, name) })
	runtime.agent.SetTools(runtime.applyLoadoutHooks(state, active))
	if state.promptOptions == nil {
		return
	}
	options := cloneSystemPromptOptions(*state.promptOptions)
	options.SelectedTools = append([]string(nil), valid...)
	snippets := make(map[string]string)
	var guidelines []string
	for _, name := range valid {
		if definition := state.runner.ToolDefinition(name); definition != nil {
			if snippet := normalizePromptText(definition.PromptSnippet); snippet != "" {
				snippets[name] = snippet
			}
			// A lazily re-read guideline set (PromptGuidelinesFunc, refreshed
			// per turn by refreshLazyToolGuidelines) supersedes the
			// registration-time snapshot.
			if lazy, tracked := state.lazyGuidelines[name]; tracked {
				guidelines = append(guidelines, normalizeGuidelines(lazy)...)
			} else {
				guidelines = append(guidelines, normalizeGuidelines(definition.PromptGuidelines)...)
			}
			continue
		}
		builtInSnippets, builtInGuidelines := builtInToolPromptDataWithOverrides([]string{name}, runtime.builtinToolPrompts)
		if snippet := normalizePromptText(builtInSnippets[name]); snippet != "" {
			snippets[name] = snippet
		}
		guidelines = append(guidelines, normalizeGuidelines(builtInGuidelines)...)
	}
	options.ToolSnippets = snippets
	options.PromptGuidelines = guidelines
	state.promptOptions = &options
	state.baseSystemPrompt = BuildSystemPrompt(options)
}

// systemPrompt is the current effective prompt, including changes not yet
// sent to the model.
func (runtime *SessionRuntime) systemPrompt() string {
	state := runtime.extensionState
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.systemPromptOverride != nil {
		return *state.systemPromptOverride
	}
	return state.baseSystemPrompt
}

// refreshLazyToolGuidelines re-reads the current prompt guidelines of every
// active tool that declares a PromptGuidelinesFunc (the upstream lazy
// `get promptGuidelines()` contract, e.g. a workflow tool whose guidelines
// list currently-available models) and rebuilds the system prompt when any
// changed. Called before each turn so the model sees current guidelines
// instead of the registration-time wire snapshot. Reads happen outside the
// state lock: the funcs may round-trip to the extension host process.
func (runtime *SessionRuntime) refreshLazyToolGuidelines(ctx context.Context) {
	state := runtime.extensionState
	if state == nil || state.runner == nil {
		return
	}
	state.mu.Lock()
	hasPromptOptions := state.promptOptions != nil
	state.mu.Unlock()
	if !hasPromptOptions {
		return
	}
	activeNames, err := runtime.extensionActiveTools()
	if err != nil {
		return
	}
	type refreshedGuidelines struct {
		name       string
		guidelines []string
	}
	var refreshed []refreshedGuidelines
	for _, name := range activeNames {
		definition := state.runner.ToolDefinition(name)
		if definition == nil || definition.PromptGuidelinesFunc == nil {
			continue
		}
		guidelines, err := definition.PromptGuidelinesFunc(ctx)
		if err != nil {
			continue // best-effort: the snapshot (or last read) stays in place
		}
		refreshed = append(refreshed, refreshedGuidelines{name: name, guidelines: guidelines})
	}
	if len(refreshed) == 0 {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.lazyGuidelines == nil {
		state.lazyGuidelines = make(map[string][]string)
	}
	changed := false
	for _, entry := range refreshed {
		current, tracked := state.lazyGuidelines[entry.name]
		if !tracked {
			if definition := state.runner.ToolDefinition(entry.name); definition != nil {
				current = definition.PromptGuidelines
			}
		}
		if !stringSlicesEqual(current, entry.guidelines) {
			changed = true
		}
		state.lazyGuidelines[entry.name] = entry.guidelines
	}
	if changed {
		runtime.setActiveToolsLocked(activeNames, state)
	}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (runtime *SessionRuntime) extensionActiveTools() ([]string, error) {
	state := runtime.agent.State()
	names := make([]string, 0, len(state.Tools))
	for _, tool := range state.Tools {
		names = append(names, tool.Spec().Name)
	}
	return names, nil
}

func (runtime *SessionRuntime) extensionAllTools() ([]extensions.ToolInfo, error) {
	state := runtime.extensionState
	if state == nil {
		return nil, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	result := make([]extensions.ToolInfo, 0, len(state.toolOrder))
	for _, name := range state.toolOrder {
		if item, ok := state.toolInfo[name]; ok {
			result = append(result, item)
		}
	}
	return result, nil
}

func (runtime *SessionRuntime) registeredExtensionCommands() []SlashCommandInfo {
	state := runtime.extensionState
	if state == nil || state.runner == nil {
		return nil
	}
	commands := state.runner.RegisteredCommands()
	result := make([]SlashCommandInfo, 0, len(commands))
	for _, command := range commands {
		result = append(result, SlashCommandInfo{
			Name: command.InvocationName, Description: command.Description, Source: SlashCommandExtension,
			SourceInfo: sourceInfoFromExtension(command.SourceInfo),
		})
	}
	return result
}

func (runtime *SessionRuntime) syncExtensionCommands() {
	if runtime == nil || runtime.slashResolver == nil {
		return
	}
	runtime.slashResolver.ExtensionCommands = runtime.registeredExtensionCommands()
}

func (runtime *SessionRuntime) extensionCommands() ([]extensions.SlashCommandInfo, error) {
	commands := runtime.Commands()
	result := make([]extensions.SlashCommandInfo, 0, len(commands))
	for _, command := range commands {
		result = append(result, extensions.SlashCommandInfo{
			Name: command.Name, Description: command.Description, Source: extensions.SlashCommandSource(command.Source),
			SourceInfo: sourceInfoToExtension(command.SourceInfo),
		})
	}
	return result, nil
}

func (runtime *SessionRuntime) beforeExtensionToolCall(ctx context.Context, call engine.BeforeToolCallContext) (*engine.BeforeToolCallResult, error) {
	state := runtime.extensionState
	if state == nil || state.runner == nil || !state.runner.HasHandlers(extensions.EventToolCall) {
		return nil, nil
	}
	if runtime.control.Load() != nil {
		ctx = extensions.WithInputHandler(ctx, runtime.RequestInput)
	}
	result := state.runner.EmitToolCall(ctx, extensions.ToolCallEvent{ToolCallID: call.ToolCall.ID, ToolName: call.ToolCall.Name, ParentToolCallID: parentToolCall(ctx), Input: toolCallInput(call.Args, call.ToolCall.Arguments)})
	if result == nil {
		return nil, nil
	}
	return &engine.BeforeToolCallResult{Block: result.Block, Reason: result.Reason, Approved: result.Approved}, nil
}

// toolCallInput exposes the prepared, validated arguments the loop is about to
// execute. Handing over ToolCall.Arguments instead would invert the upstream
// contract: mutations to event.input would rewrite the recorded call and leave
// the execution untouched. Non-object schemas keep the recorded arguments.
func toolCallInput(args any, recorded map[string]any) map[string]any {
	if input, ok := args.(map[string]any); ok {
		return input
	}
	return recorded
}

// afterExtensionToolCall runs tool_result handlers, then normalizes the
// result's images to the model's limits, including images handlers injected.
func (runtime *SessionRuntime) afterExtensionToolCall(ctx context.Context, call engine.AfterToolCallContext) (*engine.AfterToolCallResult, error) {
	var patch *engine.AfterToolCallResult
	if state := runtime.extensionState; state != nil && state.runner != nil && state.runner.HasHandlers(extensions.EventToolResult) {
		if result := state.runner.EmitToolResult(ctx, extensions.ToolResultEvent{
			ToolCallID: call.ToolCall.ID, ToolName: call.ToolCall.Name, ParentToolCallID: parentToolCall(ctx), Input: toolCallInput(call.Args, call.ToolCall.Arguments),
			Content: call.Result.Content, Details: call.Result.Details, StructuredContent: call.Result.StructuredContent, IsError: call.IsError, Usage: call.Result.Usage,
		}); result != nil {
			patch = &engine.AfterToolCallResult{IsError: result.IsError, Usage: result.Usage}
			if result.Content != nil {
				patch.Content = *result.Content
			}
			if result.Details != nil {
				patch.Details = *result.Details
				patch.DetailsSet = true
			}
		}
	}
	content := call.Result.Content
	if patch != nil && patch.Content != nil {
		content = patch.Content
	}
	normalized, changed := tools.NormalizeToolResultImages(content, runtime.settings.GetImageAutoResize(), tools.ModelResizeOptions(runtime.agent.State().Model))
	if !changed {
		return patch, nil
	}
	if patch == nil {
		// Resized images still match the tool's structured content.
		patch = &engine.AfterToolCallResult{StructuredContent: call.Result.StructuredContent}
	}
	patch.Content = normalized
	return patch, nil
}

func (runtime *SessionRuntime) sendExtensionMessage(ctx context.Context, message extensions.CustomMessage, options *extensions.SendMessageOptions) error {
	content := message.Content
	if content == nil {
		content = []any{}
	}
	appMessage := &harness.CustomMessage{Role: "custom", CustomType: message.CustomType, Content: content, Display: message.Display, Details: message.Details, Timestamp: runtime.clock()}
	state := runtime.extensionState
	var deliverAs extensions.DeliveryMode
	var triggerTurn *bool
	if options != nil {
		deliverAs, triggerTurn = options.DeliverAs, options.TriggerTurn
	}
	if deliverAs == extensions.DeliverNextTurn {
		state.mu.Lock()
		state.pendingNextTurn = append(state.pendingNextTurn, appMessage)
		state.mu.Unlock()
		return nil
	}
	runtime.mu.Lock()
	streaming := runtime.activeRuns != 0
	if streaming && triggerTurn != nil && !*triggerTurn {
		runtime.pendingCustom = append(runtime.pendingCustom, appMessage)
		runtime.mu.Unlock()
		return nil
	}
	runtime.mu.Unlock()
	// An unset triggerTurn still steers a live turn; only an explicit false opts out.
	if streaming && (triggerTurn == nil || *triggerTurn) {
		if deliverAs == extensions.DeliverFollowUp {
			runtime.agent.FollowUp(appMessage)
		} else {
			runtime.agent.Steer(appMessage)
		}
		return nil
	}
	if triggerTurn != nil && *triggerTurn {
		return runtime.runPolicies(ctx, func() error { return runtime.agent.Prompt(ctx, appMessage) })
	}
	return runtime.appendCustomMessage(appMessage)
}

func (runtime *SessionRuntime) appendCustomMessage(appMessage *harness.CustomMessage) error {
	runtime.agent.AppendMessage(appMessage)
	var err error
	if appMessage.Details != nil {
		_, err = runtime.manager.AppendCustomMessageEntry(appMessage.CustomType, appMessage.Content, appMessage.Display, appMessage.Details)
	} else {
		_, err = runtime.manager.AppendCustomMessageEntry(appMessage.CustomType, appMessage.Content, appMessage.Display)
	}
	if err != nil {
		return err
	}
	runtime.emit(engine.MessageStartEvent{Message: appMessage})
	runtime.emit(engine.MessageEndEvent{Message: appMessage})
	return nil
}

func (runtime *SessionRuntime) flushPendingCustom() error {
	runtime.mu.Lock()
	pending := runtime.pendingCustom
	runtime.pendingCustom = nil
	if len(pending) > 0 {
		runtime.customContextDirty = true
	}
	runtime.mu.Unlock()
	for _, message := range pending {
		if err := runtime.appendCustomMessage(message); err != nil {
			return err
		}
	}
	return nil
}

func (runtime *SessionRuntime) sendExtensionUserMessage(ctx context.Context, content ai.UserContent, options *extensions.SendUserMessageOptions) error {
	text := ""
	var images []*ai.ImageContent
	if content.Text != nil {
		text = *content.Text
	} else {
		var textParts []string
		for _, block := range content.Blocks {
			switch value := block.(type) {
			case *ai.TextContent:
				textParts = append(textParts, value.Text)
			case *ai.ImageContent:
				images = append(images, value)
			}
		}
		text = strings.Join(textParts, "\n")
	}
	var streamingBehavior *extensions.DeliveryMode
	expand := false
	if options != nil {
		if options.DeliverAs != "" {
			behavior := options.DeliverAs
			streamingBehavior = &behavior
		}
		expand = options.ExpandPromptTemplates
	}
	return runtime.promptExtensionInput(ctx, text, images, extensions.InputExtension, expand, streamingBehavior, true, nil)
}

func (runtime *SessionRuntime) appendExtensionEntry(_ context.Context, customType string, data any) error {
	id, err := runtime.manager.AppendCustomEntry(customType, data)
	if err != nil {
		return err
	}
	if entry := runtime.manager.GetEntry(id); entry != nil {
		runtime.emit(EntryAppendedEvent{Entry: *entry})
	}
	return nil
}

func (runtime *SessionRuntime) setExtensionSessionName(_ context.Context, name string) error {
	if _, err := runtime.manager.AppendSessionInfo(name); err != nil {
		return err
	}
	current := runtime.manager.GetSessionName()
	runtime.emit(SessionInfoChangedEvent{Name: current})
	state := runtime.extensionState
	if state != nil && state.runner.HasHandlers(extensions.EventSessionInfoChanged) {
		state.runner.Emit(context.Background(), extensions.SessionInfoChangedEvent{Name: current})
	}
	return nil
}

func (runtime *SessionRuntime) setExtensionModel(ctx context.Context, model *ai.Model) (bool, error) {
	if model == nil {
		return false, nil
	}
	state := runtime.extensionState
	if state == nil || state.runner.ModelRegistry() == nil || !state.runner.ModelRegistry().HasConfiguredAuth(string(model.Provider), nil) {
		return false, nil
	}
	return true, runtime.setModel(ctx, *model, nil, false, extensions.ModelSelectSet)
}

func (runtime *SessionRuntime) setExtensionThinkingLevel(level engine.ThinkingLevel) error {
	return runtime.SetThinkingLevel(level)
}

func sameModel(left, right *ai.Model) bool {
	return left != nil && right != nil && left.Provider == right.Provider && left.ID == right.ID
}

func (runtime *SessionRuntime) extensionSystemPromptOptions() extensions.SystemPromptOptions {
	state := runtime.extensionState
	if state == nil {
		return extensions.SystemPromptOptions{CWD: runtime.manager.GetCWD()}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.promptOptions == nil {
		return extensions.SystemPromptOptions{CWD: runtime.manager.GetCWD()}
	}
	options := state.promptOptions
	return extensions.SystemPromptOptions{
		CustomPrompt: options.CustomPrompt, SelectedTools: append([]string(nil), options.SelectedTools...),
		ToolSnippets: cloneStringMap(options.ToolSnippets), PromptGuidelines: append([]string(nil), options.PromptGuidelines...),
		AppendSystemPrompt: options.AppendSystemPrompt, CWD: options.CWD, ContextFiles: extensionContextFiles(options.ContextFiles),
	}
}

func (runtime *SessionRuntime) ExecuteUserBash(
	ctx context.Context,
	command string,
	excludeFromContext bool,
	onChunk func(string),
) (extensions.BashResult, error) {
	return runtime.executeUserBash(ctx, command, &excludeFromContext, onChunk, nil)
}

func (runtime *SessionRuntime) ExecuteUserBashWithID(
	ctx context.Context,
	command string,
	excludeFromContext *bool,
	id *string,
) (tools.BashResult, error) {
	result, err := runtime.executeUserBash(ctx, command, excludeFromContext, nil, id)
	if err != nil {
		return tools.BashResult{}, err
	}
	converted := tools.BashResult{
		Output: result.Output, ExitCode: result.ExitCode, Cancelled: result.Cancelled, Truncated: result.Truncated,
	}
	if result.FullOutput != nil {
		converted.FullOutputPath = *result.FullOutput
	}
	return converted, nil
}

func (runtime *SessionRuntime) executeUserBash(
	ctx context.Context,
	command string,
	excludeFromContext *bool,
	onChunk func(string),
	id *string,
) (extensions.BashResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	exclude := excludeFromContext != nil && *excludeFromContext
	var operations tools.BashOperations
	state := runtime.extensionState
	if state != nil && state.runner != nil && state.runner.HasHandlers(extensions.EventUserBash) {
		result, hookErr := state.runner.EmitUserBashChecked(ctx, extensions.UserBashEvent{Command: command, ExcludeFromContext: exclude, CWD: runtime.manager.GetCWD()})
		if hookErr != nil {
			return extensions.BashResult{}, hookErr
		}
		if result != nil {
			if result.Result != nil {
				return *result.Result, runtime.recordBashResult(command, *result.Result, excludeFromContext)
			}
			operations = result.Operations
		}
	}
	if operations == nil {
		shellPath, err := runtime.settings.GetShellPath()
		if err != nil {
			return extensions.BashResult{}, err
		}
		operations = tools.NewLocalBashOperations(tools.LocalBashOperationsOptions{ShellPath: shellPath})
	}
	resolvedCommand := command
	if prefix := runtime.settings.GetShellCommandPrefix(); prefix != "" {
		resolvedCommand = prefix + "\n" + command
	}
	environment, err := tools.GetShellEnv()
	if err != nil {
		return extensions.BashResult{}, err
	}
	bashContext, finish := runtime.startBash(ctx)
	defer finish()
	output := tools.NewOutputAccumulator()
	var outputErr error
	var outputErrMu sync.Mutex
	result, executeErr := operations.Exec(bashContext, resolvedCommand, runtime.manager.GetCWD(), tools.BashExecOptions{
		OnData: func(data []byte) {
			if err := output.Append(data); err != nil {
				outputErrMu.Lock()
				if outputErr == nil {
					outputErr = err
				}
				outputErrMu.Unlock()
			}
			if onChunk != nil {
				onChunk(string(data))
			}
			runtime.emit(BashExecutionUpdateEvent{ID: id, Delta: string(data)})
		},
		Env: environment,
	})
	outputErrMu.Lock()
	appendErr := outputErr
	outputErrMu.Unlock()
	if appendErr != nil && executeErr == nil {
		executeErr = appendErr
	}
	if finishErr := output.Finish(); finishErr != nil && executeErr == nil {
		executeErr = finishErr
	}
	snapshot, snapshotErr := output.Snapshot(tools.OutputSnapshotOptions{PersistIfTruncated: true})
	if snapshotErr != nil && executeErr == nil {
		executeErr = snapshotErr
	}
	if closeErr := output.CloseTempFile(); closeErr != nil && executeErr == nil {
		executeErr = closeErr
	}
	cancelled := errors.Is(bashContext.Err(), context.Canceled)
	if executeErr != nil && !cancelled {
		return extensions.BashResult{}, executeErr
	}
	var fullOutput *string
	if snapshot.FullOutputPath != "" {
		fullOutput = &snapshot.FullOutputPath
	}
	bashResult := extensions.BashResult{
		Output: snapshot.Content, ExitCode: result.ExitCode, Cancelled: cancelled,
		Truncated: snapshot.Truncation.Truncated, FullOutput: fullOutput,
	}
	return bashResult, runtime.recordBashResult(command, bashResult, excludeFromContext)
}

func (runtime *SessionRuntime) recordBashResult(command string, result extensions.BashResult, excludeFromContext *bool) error {
	var exclude *bool
	if excludeFromContext != nil {
		value := *excludeFromContext
		exclude = &value
	}
	message := harness.BashExecutionMessage{
		Role: "bashExecution", Command: command, Output: result.Output, ExitCode: result.ExitCode,
		Cancelled: result.Cancelled, Truncated: result.Truncated, FullOutputPath: result.FullOutput,
		ExcludeFromContext: exclude, Timestamp: runtime.clock(),
	}
	return runtime.recordBashMessage(message)
}

func (runtime *SessionRuntime) clearExtensionTurnState() {
	state := runtime.extensionState
	if state != nil {
		state.mu.Lock()
		state.systemPromptOverride = nil
		state.mu.Unlock()
	}
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := values[:0]
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneSystemPromptOptions(options SystemPromptOptions) SystemPromptOptions {
	options.SelectedTools = append([]string(nil), options.SelectedTools...)
	options.ToolSnippets = cloneStringMap(options.ToolSnippets)
	options.PromptGuidelines = append([]string(nil), options.PromptGuidelines...)
	options.ContextFiles = append([]ContextFile(nil), options.ContextFiles...)
	return options
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func normalizePromptText(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " ")), " ")
}

func normalizeGuidelines(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func extensionContextFiles(files []ContextFile) []extensions.ContextFile {
	result := make([]extensions.ContextFile, len(files))
	for index, file := range files {
		result[index] = extensions.ContextFile{Path: file.Path, Content: file.Content}
	}
	return result
}

func cloneDiscoveredResources(resources extensions.DiscoveredResources) extensions.DiscoveredResources {
	resources.SkillPaths = append([]extensions.DiscoveredPath(nil), resources.SkillPaths...)
	resources.PromptPaths = append([]extensions.DiscoveredPath(nil), resources.PromptPaths...)
	resources.ThemePaths = append([]extensions.DiscoveredPath(nil), resources.ThemePaths...)
	return resources
}

func (runtime *SessionRuntime) extensionLifecycleEvent(ctx context.Context, event engine.AgentEvent) engine.AgentEvent {
	state := runtime.extensionState
	if state == nil || state.runner == nil {
		return event
	}
	runner := state.runner
	switch typed := event.(type) {
	case engine.AgentStartEvent:
		state.mu.Lock()
		state.turnIndex = 0
		state.mu.Unlock()
		runner.Emit(ctx, extensions.AgentStartEvent{})
	case engine.AgentEndEvent:
		runner.Emit(ctx, extensions.AgentEndEvent{Messages: typed.Messages})
	case engine.TurnStartEvent:
		state.mu.Lock()
		index := state.turnIndex
		state.mu.Unlock()
		runner.Emit(ctx, extensions.TurnStartEvent{TurnIndex: index, Timestamp: time.Now().UnixMilli()})
	case engine.TurnEndEvent:
		// Extensions saw turn_end as an actionable boundary from finishTurn.
		state.mu.Lock()
		state.turnIndex++
		state.mu.Unlock()
	case engine.MessageStartEvent:
		runner.Emit(ctx, extensions.MessageStartEvent{Message: typed.Message})
	case engine.MessageUpdateEvent:
		runner.Emit(ctx, extensions.MessageUpdateEvent{Message: typed.Message, AssistantMessageEvent: typed.AssistantMessageEvent})
	case engine.MessageEndEvent:
		if engine.IsEphemeralAgentEvent(ctx) {
			runner.Emit(ctx, extensions.MessageEndEvent{Message: typed.Message})
			break
		}
		if replacement := runner.EmitMessageEnd(ctx, extensions.MessageEndEvent{Message: typed.Message}); replacement != nil {
			replacement = normalizeExtensionMessage(replacement)
			if replaceAgentMessageInPlace(typed.Message, replacement) {
				replacement = typed.Message
			} else {
				typed.Message = replacement
			}
			runtime.agent.ReplaceLastMessage(replacement)
			return typed
		}
	case engine.ToolExecutionStartEvent:
		runner.Emit(ctx, extensions.ToolExecutionStartEvent{ToolCallID: typed.ToolCallID, ToolName: typed.ToolName, Args: typed.Args, ParentToolCallID: typed.ParentToolCallID})
	case engine.ToolExecutionUpdateEvent:
		runner.Emit(ctx, extensions.ToolExecutionUpdateEvent{ToolCallID: typed.ToolCallID, ToolName: typed.ToolName, Args: typed.Args, PartialResult: typed.PartialResult, ParentToolCallID: typed.ParentToolCallID})
	case engine.ToolExecutionEndEvent:
		runner.Emit(ctx, extensions.ToolExecutionEndEvent{ToolCallID: typed.ToolCallID, ToolName: typed.ToolName, Result: typed.Result, IsError: typed.IsError, ParentToolCallID: typed.ParentToolCallID})
	}
	return event
}

func normalizeExtensionMessage(message engine.AgentMessage) engine.AgentMessage {
	switch value := message.(type) {
	case *ai.UserMessage:
		if value.Content.Text == nil && value.Content.Blocks == nil {
			copy := *value
			copy.Content.Blocks = ai.UserContentBlocks{}
			return &copy
		}
	case ai.UserMessage:
		if value.Content.Text == nil && value.Content.Blocks == nil {
			value.Content.Blocks = ai.UserContentBlocks{}
			return value
		}
	case *ai.AssistantMessage:
		if value.Content == nil {
			copy := *value
			copy.Content = ai.AssistantContent{}
			return &copy
		}
	case ai.AssistantMessage:
		if value.Content == nil {
			value.Content = ai.AssistantContent{}
			return value
		}
	case *ai.ToolResultMessage:
		if value.Content == nil {
			copy := *value
			copy.Content = ai.ToolResultContent{}
			return &copy
		}
	case ai.ToolResultMessage:
		if value.Content == nil {
			value.Content = ai.ToolResultContent{}
			return value
		}
	case *harness.CustomMessage:
		if value.Content == nil {
			copy := *value
			copy.Content = []any{}
			return &copy
		}
	case harness.CustomMessage:
		if value.Content == nil {
			value.Content = []any{}
			return value
		}
	}
	return message
}

func replaceAgentMessageInPlace(target, replacement engine.AgentMessage) bool {
	switch current := target.(type) {
	case *ai.UserMessage:
		switch next := replacement.(type) {
		case *ai.UserMessage:
			*current = *next
			return true
		case ai.UserMessage:
			*current = next
			return true
		}
	case *ai.AssistantMessage:
		switch next := replacement.(type) {
		case *ai.AssistantMessage:
			*current = *next
			return true
		case ai.AssistantMessage:
			*current = next
			return true
		}
	case *ai.ToolResultMessage:
		switch next := replacement.(type) {
		case *ai.ToolResultMessage:
			*current = *next
			return true
		case ai.ToolResultMessage:
			*current = next
			return true
		}
	case *harness.CustomMessage:
		switch next := replacement.(type) {
		case *harness.CustomMessage:
			*current = *next
			return true
		case harness.CustomMessage:
			*current = next
			return true
		}
	}
	return false
}

func (runtime *SessionRuntime) emitExtensionSettled(ctx context.Context) {
	state := runtime.extensionState
	if state != nil && state.runner != nil && state.runner.HasHandlers(extensions.EventAgentSettled) {
		state.runner.Emit(ctx, extensions.AgentSettledEvent{})
	}
}

func (runtime *SessionRuntime) promptExtensionInput(
	ctx context.Context,
	text string,
	images []*ai.ImageContent,
	source extensions.InputSource,
	commands bool,
	streamingBehavior *extensions.DeliveryMode,
	runPreflight bool,
	preflightResult func(InputDisposition),
) error {
	// Slash commands keep their existing replacement behavior. Model work reserves
	// its session before preflight or input hooks can mutate it.
	var finish func()
	if runtime.control.Load() != nil && (!commands || !strings.HasPrefix(text, "/")) && runtime.agent.IsIdle() {
		var err error
		ctx, finish, err = runtime.reserveControl(ctx)
		if err != nil {
			return err
		}
		defer finish()
	}
	state := runtime.extensionState
	if state == nil || state.runner == nil {
		if runPreflight {
			if err := runtime.PromptPreflight(ctx); err != nil {
				return err
			}
			if preflightResult != nil {
				preflightResult(DispositionStarted)
			}
		}
		return runtime.runPolicies(ctx, func() error { return runtime.agent.Prompt(ctx, text, images...) })
	}
	if commands && strings.HasPrefix(text, "/") {
		commandText := strings.TrimPrefix(text, "/")
		name, args, _ := strings.Cut(commandText, " ")
		if state.runner.ExecuteCommand(ctx, name, args) {
			if preflightResult != nil {
				preflightResult(DispositionHandled)
			}
			return nil
		}
	}
	if state.runner.HasHandlers(extensions.EventInput) {
		eventStreamingBehavior := streamingBehavior
		if runtime.agent.IsIdle() {
			eventStreamingBehavior = nil
		}
		result := state.runner.EmitInput(ctx, text, images, source, eventStreamingBehavior)
		if result.Action == extensions.InputHandled {
			if preflightResult != nil {
				preflightResult(DispositionHandled)
			}
			return nil
		}
		if result.Action == extensions.InputTransform {
			text = result.Text
			if result.Images != nil {
				images = result.Images
			}
		}
	}
	if commands && runtime.slashResolver != nil {
		text = runtime.slashResolver.Expand(text)
	}
	if !runtime.agent.IsIdle() {
		if streamingBehavior == nil {
			return errors.New("Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.") //nolint:staticcheck // User-visible error matches upstream.
		}
		message := runtime.userMessage(text, images)
		runtime.mu.Lock()
		if *streamingBehavior == extensions.DeliverFollowUp {
			runtime.followUps = append(runtime.followUps, text)
			runtime.mu.Unlock()
			runtime.agent.FollowUp(message)
		} else {
			runtime.steering = append(runtime.steering, text)
			runtime.mu.Unlock()
			runtime.agent.Steer(message)
		}
		runtime.emitQueueUpdate()
		if preflightResult != nil {
			preflightResult(DispositionQueued)
		}
		return nil
	}
	if runPreflight {
		if err := runtime.PromptPreflight(ctx); err != nil {
			return err
		}
		if preflightResult != nil {
			preflightResult(DispositionStarted)
		}
	}

	// Lazy prompt guidelines re-read per turn (upstream `get promptGuidelines()`
	// parity): must run before the base system prompt is captured below.
	runtime.refreshLazyToolGuidelines(ctx)

	state.mu.Lock()
	basePrompt := state.baseSystemPrompt
	if state.promptOptions == nil {
		basePrompt = runtime.agent.State().SystemPrompt
	}
	state.systemPromptOverride = nil
	options := runtime.extensionSystemPromptOptionsLocked(state)
	var promptBuildOptions *SystemPromptOptions
	if state.promptOptions != nil {
		copy := cloneSystemPromptOptions(*state.promptOptions)
		promptBuildOptions = &copy
	}
	pending := append(engine.AgentMessages(nil), state.pendingNextTurn...)
	state.pendingNextTurn = nil
	state.mu.Unlock()

	var injected engine.AgentMessages
	var forcedPrompt *string
	if state.runner.HasHandlers(extensions.EventBeforeAgentStart) {
		result := state.runner.EmitBeforeAgentStart(ctx, text, images, basePrompt, options)
		if result != nil && promptBuildOptions != nil {
			// Sections handlers set apply to this run; the transcript records changes.
			for _, name := range slices.Sorted(maps.Keys(result.SystemPromptOptions.Sections)) {
				text := result.SystemPromptOptions.Sections[name]
				promptBuildOptions.Sections = append(promptBuildOptions.Sections, ai.SystemPromptSection{Name: name, Text: &text})
			}
		}
		if result != nil {
			for _, message := range result.Messages {
				content := message.Content
				if content == nil {
					content = []any{}
				}
				injected = append(injected, &harness.CustomMessage{Role: "custom", CustomType: message.CustomType, Content: content, Display: message.Display, Details: message.Details, Timestamp: time.Now().UnixMilli()})
			}
			if result.SystemPrompt != nil {
				forcedPrompt = result.SystemPrompt
				state.mu.Lock()
				prompt := *result.SystemPrompt
				state.systemPromptOverride = &prompt
				state.mu.Unlock()
			}
		}
	}
	// The run records the loadout; restored tools that did not register by now
	// are dropped, so a tool that never registers does not stay pending.
	state.mu.Lock()
	state.pendingTools = nil
	state.mu.Unlock()
	messages := make(engine.AgentMessages, 0, 2+len(pending)+len(injected))
	transcript, _ := ConvertToLLM(ctx, runtime.agent.State().Messages)
	current := ai.CurrentSystemMessage(transcript)
	if promptBuildOptions != nil {
		desired := BuildSystemPromptSections(*promptBuildOptions)
		previous := ai.SystemPromptSections(nil)
		if current != nil {
			previous = current.Sections
		}
		if patch := DiffSystemPromptSections(previous, desired); patch != nil {
			messages = append(messages, &ai.SystemMessage{Content: "", Sections: patch, Timestamp: runtime.clock()})
		}
	} else if current == nil && basePrompt != "" {
		messages = append(messages, &ai.SystemMessage{Content: basePrompt, Timestamp: runtime.clock()})
	}
	messages = append(messages, runtime.userMessage(text, images))
	messages = append(messages, pending...)
	messages = append(messages, injected...)
	runtime.agent.SetRequestSystemPromptOverride(forcedPrompt)
	defer runtime.agent.SetRequestSystemPromptOverride(nil)
	return runtime.runPolicies(ctx, func() error { return runtime.agent.Prompt(ctx, messages) })
}

func (runtime *SessionRuntime) extensionSystemPromptOptionsLocked(state *extensionRuntimeState) extensions.SystemPromptOptions {
	if state.promptOptions == nil {
		return extensions.SystemPromptOptions{CWD: runtime.manager.GetCWD()}
	}
	options := state.promptOptions
	return extensions.SystemPromptOptions{
		CustomPrompt: options.CustomPrompt, SelectedTools: append([]string(nil), options.SelectedTools...),
		ToolSnippets: cloneStringMap(options.ToolSnippets), PromptGuidelines: append([]string(nil), options.PromptGuidelines...),
		AppendSystemPrompt: options.AppendSystemPrompt, CWD: options.CWD, ContextFiles: extensionContextFiles(options.ContextFiles),
	}
}

// installTurnEndBoundary dispatches turn_end to extensions from finishTurn,
// before the engine's turn_end, so handlers can persist entries and ensure one
// more request.
func (runtime *SessionRuntime) installTurnEndBoundary() {
	var previous engine.FinishTurnFunc
	previous = runtime.agent.SwapFinishTurn(func(ctx context.Context, turn engine.TurnContext) (engine.TurnAction, error) {
		extensionContinue := runtime.dispatchTurnEndBoundary(ctx, turn)
		var action engine.TurnAction
		if previous != nil {
			var err error
			if action, err = previous(ctx, turn); err != nil {
				return "", err
			}
		}
		if action == engine.TurnEnd {
			return action, nil
		}
		if extensionContinue || action == engine.TurnContinue {
			return engine.TurnContinue, nil
		}
		return "", nil
	})
}

func (runtime *SessionRuntime) dispatchTurnEndBoundary(ctx context.Context, turn engine.TurnContext) bool {
	if turn.Message == nil || engine.IsEphemeralAgentEvent(ctx) {
		return false
	}
	outcome := extensions.ActivityOutcome("completed")
	switch turn.Message.StopReason {
	case ai.StopReasonAborted:
		outcome = "aborted"
	case ai.StopReasonError:
		outcome = "error"
	}
	runtime.mu.Lock()
	runtime.activityOutcome = outcome
	runtime.mu.Unlock()
	state := runtime.extensionState
	if state == nil || state.runner == nil || !state.runner.HasHandlers(extensions.EventTurnEnd) {
		return false
	}
	messageEntryID, toolResultEntryIDs := runtime.trailingTurnEntryIDs()
	if messageEntryID == "" || len(toolResultEntryIDs) != len(turn.ToolResults) {
		state.runner.EmitBoundaryError(extensions.EventTurnEnd, "turn_end could not resolve the persisted assistant entry ID")
		return false
	}
	state.mu.Lock()
	index := state.turnIndex
	state.mu.Unlock()
	event := extensions.TurnEndEvent{
		TurnIndex: index, Message: turn.Message, ToolResults: turn.ToolResults,
		MessageEntryID: messageEntryID, ToolResultEntryIDs: toolResultEntryIDs,
		BoundaryState: extensions.BoundaryState{Outcome: outcome},
	}
	boundary := state.runner.EmitBoundary(ctx, event, func(drafts []extensions.SessionBoundaryDraft) (extensions.BoundaryContextPreview, error) {
		return runtime.boundaryContext(drafts, extensions.EventTurnEnd)
	})
	if err := runtime.commitBoundaryDrafts(boundary.Entries); err != nil {
		state.runner.EmitBoundaryError(extensions.EventTurnEnd, err.Error())
		return false
	}
	if boundary.Continue {
		if final, err := runtime.boundaryContext(nil, extensions.EventTurnEnd); err != nil || !final.CanContinue {
			state.runner.EmitBoundaryError(extensions.EventTurnEnd, "turn_end requested continuation without runnable model context")
			return false
		}
	}
	return boundary.Continue
}

// runBeforeSettleBoundary dispatches agent_before_settle and reports whether
// the run continues.
func (runtime *SessionRuntime) runBeforeSettleBoundary(ctx context.Context) bool {
	state := runtime.extensionState
	if state == nil || state.runner == nil || !state.runner.HasHandlers(extensions.EventAgentBeforeSettle) {
		return runtime.agent.HasQueuedMessages()
	}
	runtime.mu.Lock()
	outcome := runtime.activityOutcome
	runtime.mu.Unlock()
	result := state.runner.EmitBoundary(ctx, extensions.AgentBeforeSettleEvent{BoundaryState: extensions.BoundaryState{Outcome: outcome}},
		func(drafts []extensions.SessionBoundaryDraft) (extensions.BoundaryContextPreview, error) {
			return runtime.boundaryContext(drafts, extensions.EventAgentBeforeSettle)
		})
	if err := runtime.commitBoundaryDrafts(result.Entries); err != nil {
		state.runner.EmitBoundaryError(extensions.EventAgentBeforeSettle, err.Error())
		return false
	}
	if err := runtime.flushPendingCustom(); err != nil {
		state.runner.EmitBoundaryError(extensions.EventAgentBeforeSettle, err.Error())
	}
	if runtime.aborted() {
		return false
	}
	shouldContinue := result.Continue || runtime.agent.HasQueuedMessages()
	if final, err := runtime.boundaryContext(nil, extensions.EventAgentBeforeSettle); shouldContinue && (err != nil || !final.CanContinue) {
		if result.Continue {
			state.runner.EmitBoundaryError(extensions.EventAgentBeforeSettle, "agent_before_settle requested continuation without runnable model context")
		}
		return false
	}
	return shouldContinue
}

// boundaryContext previews the next request's context with drafts applied to
// an in-memory copy of the branch.
func (runtime *SessionRuntime) boundaryContext(drafts []extensions.SessionBoundaryDraft, boundary extensions.EventType) (extensions.BoundaryContextPreview, error) {
	preview, err := runtime.manager.BranchPreview()
	if err != nil {
		return extensions.BoundaryContextPreview{}, err
	}
	if _, err := applyBoundaryDrafts(preview, drafts); err != nil {
		return extensions.BoundaryContextPreview{}, err
	}
	projection := preview.BuildSessionContext()
	messages := make(engine.AgentMessages, 0, len(projection.Messages))
	for _, raw := range projection.Messages {
		messages = append(messages, decodeSessionMessage(raw))
	}
	llmMessages, err := ConvertToLLM(context.Background(), messages)
	if err != nil {
		return extensions.BoundaryContextPreview{}, err
	}
	runtime.mu.Lock()
	pendingCustom := len(runtime.pendingCustom) > 0
	pending := runtime.agent.PeekQueuedMessages()
	for _, message := range runtime.pendingCustom {
		pending = append(pending, message)
	}
	runtime.mu.Unlock()
	endsWithAssistant, nonSystem := false, false
	for _, message := range llmMessages {
		_, system := message.(*ai.SystemMessage)
		_, endsWithAssistant = message.(*ai.AssistantMessage)
		nonSystem = nonSystem || !system
	}
	queued := runtime.agent.HasQueuedMessages()
	if boundary == extensions.EventAgentBeforeSettle {
		queued = queued && endsWithAssistant
	}
	return extensions.BoundaryContextPreview{
		ContextEntries: preview.BuildContextEntries(), ContextMessages: messages, LLMMessages: llmMessages, PendingMessages: pending,
		CanContinue: (nonSystem && !endsWithAssistant) || pendingCustom || queued,
	}, nil
}

func (runtime *SessionRuntime) commitBoundaryDrafts(drafts []extensions.SessionBoundaryDraft) error {
	if len(drafts) == 0 {
		return nil
	}
	appended, err := applyBoundaryDrafts(runtime.manager, drafts)
	runtime.RefreshContext()
	for _, entry := range appended {
		runtime.emit(EntryAppendedEvent{Entry: entry})
	}
	return err
}

func applyBoundaryDrafts(manager *sessionstore.SessionManager, drafts []extensions.SessionBoundaryDraft) ([]sessionstore.SessionEntry, error) {
	appended := make([]sessionstore.SessionEntry, 0, len(drafts))
	for _, draft := range drafts {
		var entryID string
		var err error
		switch draft.Type {
		case "custom":
			entryID, err = manager.AppendCustomEntry(draft.CustomType, draft.Data)
		case "custom_message":
			entryID, err = manager.AppendCustomMessageEntry(draft.CustomType, draft.Content, draft.Display, draft.Details)
		case "context_edit":
			replacement := draft.Replacement
			if string(replacement) == "null" {
				replacement = nil
			}
			entryID, err = manager.AppendContextEdit(draft.TargetID, replacement)
		case "compaction":
			messages := make(engine.AgentMessages, 0)
			for _, raw := range manager.BuildSessionContext().Messages {
				messages = append(messages, decodeSessionMessage(raw))
			}
			fromHook := true
			firstKept := ""
			if draft.FirstKeptEntryID != nil {
				firstKept = *draft.FirstKeptEntryID
			}
			entryID, err = manager.AppendCompaction(draft.Summary, firstKept, int64(harness.EstimateContextTokens(messages).Tokens),
				sessionstore.OptionalEntryFields{HasDetails: draft.Details != nil, Details: draft.Details, FromHook: &fromHook, Usage: draft.Usage})
		default:
			err = fmt.Errorf("unknown boundary entry type %q", draft.Type)
		}
		if err != nil {
			return appended, err
		}
		if entry := manager.GetEntry(entryID); entry != nil {
			appended = append(appended, *entry)
		}
	}
	return appended, nil
}

func (runtime *SessionRuntime) defaultToolNames(usesDefaults bool) []string {
	if !usesDefaults {
		return nil
	}
	if tools := runtime.settings.GetDefaultTools(); tools != nil {
		return tools
	}
	return DefaultActiveToolNames
}
