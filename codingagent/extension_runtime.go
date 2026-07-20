package codingagent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
)

type extensionRuntimeState struct {
	mu sync.Mutex

	runner               *extensions.Runner
	baseTools            []agent.AgentTool
	toolRegistry         map[string]agent.AgentTool
	toolInfo             map[string]extensions.ToolInfo
	toolOrder            []string
	previousRegistry     map[string]struct{}
	allowed              map[string]struct{}
	hasAllowlist         bool
	excluded             map[string]struct{}
	promptOptions        *SystemPromptOptions
	baseSystemPrompt     string
	systemPromptOverride *string
	resources            extensions.DiscoveredResources
	pendingNextTurn      agent.AgentMessages
	turnIndex            int
	startEvent           extensions.SessionStartEvent
	started              bool
	config               SessionRuntimeConfig
	shutdownEmitted      bool
}

func (runtime *SessionRuntime) bindExtensions(runtimeConfig SessionRuntimeConfig) {
	replacingRunner := runtime.extensionState != nil
	state := &extensionRuntimeState{
		baseTools:        append([]agent.AgentTool(nil), runtimeConfig.BaseTools...),
		toolRegistry:     make(map[string]agent.AgentTool),
		toolInfo:         make(map[string]extensions.ToolInfo),
		previousRegistry: make(map[string]struct{}),
		excluded:         make(map[string]struct{}),
		config:           runtimeConfig,
	}
	if len(state.baseTools) == 0 {
		state.baseTools = append([]agent.AgentTool(nil), runtime.agent.State().Tools...)
	}
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
		runtime.agent.SetSystemPrompt(state.baseSystemPrompt)
	} else {
		state.baseSystemPrompt = runtime.agent.State().SystemPrompt
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
		GetThinkingLevel:       func() (agent.ThinkingLevel, error) { return runtime.agent.State().ThinkingLevel, nil },
		SetThinkingLevel:       runtime.setExtensionThinkingLevel,
		RegisterProvider:       registerProvider,
		RegisterProviderConfig: registerProviderConfig,
		UnregisterProvider:     unregisterProvider,
	}
	contextActions := extensions.ContextActions{
		GetModel:         func() *ai.Model { return runtime.agent.State().Model },
		IsIdle:           runtime.agent.IsIdle,
		IsProjectTrusted: runtime.settings.IsProjectTrusted,
		GetSignal:        runtime.agent.Signal,
		Abort:            runtime.Abort,
		HasPendingMessages: func() bool {
			state.mu.Lock()
			defer state.mu.Unlock()
			return len(state.pendingNextTurn) > 0 || runtime.agent.HasQueuedMessages()
		},
		Shutdown: runtime.Abort,
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
		GetSystemPrompt:        func() string { return runtime.agent.State().SystemPrompt },
		GetSystemPromptOptions: runtime.extensionSystemPromptOptions,
	}
	commandActions := runtime.runtimeCommandActions()
	runner := extensions.NewRunner(runtimeConfig.ExtensionRegistry, extensions.RunnerOptions{
		CWD: runtime.manager.GetCWD(), SessionManager: runtime.manager, ModelRegistry: runtimeConfig.ModelRegistry,
		Mode: runtimeConfig.ExtensionMode, UI: runtimeConfig.ExtensionUI, Actions: actions,
		ContextActions: contextActions, CommandActions: &commandActions, ErrorHandler: runtimeConfig.ExtensionErrorHandler,
	})
	state.runner = runner

	if replacingRunner {
		runtime.agent.SetTransformContext(nil)
		runtime.agent.SetToolCallHooks(nil, nil)
		runtime.agent.SetProviderHooks(nil, nil, nil)
	}
	if runner.HasHandlers(extensions.EventContext) {
		runtime.agent.SetTransformContext(func(ctx context.Context, messages agent.AgentMessages) (agent.AgentMessages, error) {
			return runner.EmitContext(ctx, messages), nil
		})
	}
	if runner.HasHandlers(extensions.EventToolCall) || runner.HasHandlers(extensions.EventToolResult) {
		runtime.agent.SetToolCallHooks(runtime.beforeExtensionToolCall, runtime.afterExtensionToolCall)
	}
	if runner.HasHandlers(extensions.EventBeforeProviderRequest) || runner.HasHandlers(extensions.EventBeforeProviderHeaders) || runner.HasHandlers(extensions.EventAfterProviderResponse) {
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
		)
	}

	initial := runtimeConfig.InitialActiveToolNames
	if initial == nil {
		for _, tool := range runtime.agent.State().Tools {
			initial = append(initial, tool.Spec().Name)
		}
	}
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
		WaitForIdle: runtime.agent.WaitForIdle,
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
	runtime.settings.Reload()
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
	runtime.agent.SetSteeringMode(agent.QueueMode(runtime.settings.GetSteeringMode()))
	runtime.agent.SetFollowUpMode(agent.QueueMode(runtime.settings.GetFollowUpMode()))
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
func (runtime *SessionRuntime) RegisteredTool(name string) agent.AgentTool {
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
		loaded := LoadPromptTemplates(LoadPromptTemplatesOptions{CWD: cwd, AgentDir: agentDir, PromptPaths: []string{entry.Path}})
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
	basePrompt := state.baseSystemPrompt
	overridden := state.systemPromptOverride != nil
	state.mu.Unlock()
	if !overridden {
		runtime.agent.SetSystemPrompt(basePrompt)
	}
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

	registry := make(map[string]agent.AgentTool)
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
			Name: spec.Name, Description: spec.Description, Parameters: spec.Parameters,
			SourceInfo: extensions.SourceInfo{Path: "<builtin:" + spec.Name + ">", Source: "builtin", Scope: extensions.SourceScopeTemporary, Origin: extensions.SourceOriginTopLevel},
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
			PromptGuidelines: append([]string(nil), registered.Definition.PromptGuidelines...), SourceInfo: registered.SourceInfo,
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
		active = append(active, extensionNames...)
	} else {
		for _, name := range order {
			if _, existed := previousRegistry[name]; !existed {
				active = append(active, name)
			}
		}
	}
	runtime.setActiveToolsLocked(uniqueStrings(active), state)
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
		return errors.New("codingagent: extension runtime is not bound")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	runtime.setActiveToolsLocked(names, state)
	return nil
}

func (runtime *SessionRuntime) setActiveToolsLocked(names []string, state *extensionRuntimeState) {
	active := make([]agent.AgentTool, 0, len(names))
	valid := make([]string, 0, len(names))
	for _, name := range names {
		if tool := state.toolRegistry[name]; tool != nil {
			active = append(active, tool)
			valid = append(valid, name)
		}
	}
	runtime.agent.SetTools(active)
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
			guidelines = append(guidelines, normalizeGuidelines(definition.PromptGuidelines)...)
			continue
		}
		builtInSnippets, builtInGuidelines := BuiltInToolPromptData([]string{name})
		if snippet := normalizePromptText(builtInSnippets[name]); snippet != "" {
			snippets[name] = snippet
		}
		guidelines = append(guidelines, normalizeGuidelines(builtInGuidelines)...)
	}
	options.ToolSnippets = snippets
	options.PromptGuidelines = guidelines
	state.promptOptions = &options
	state.baseSystemPrompt = BuildSystemPrompt(options)
	if state.systemPromptOverride != nil {
		runtime.agent.SetSystemPrompt(*state.systemPromptOverride)
	} else {
		runtime.agent.SetSystemPrompt(state.baseSystemPrompt)
	}
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

func (runtime *SessionRuntime) beforeExtensionToolCall(ctx context.Context, call agent.BeforeToolCallContext) (*agent.BeforeToolCallResult, error) {
	state := runtime.extensionState
	if state == nil || state.runner == nil || !state.runner.HasHandlers(extensions.EventToolCall) {
		return nil, nil
	}
	result := state.runner.EmitToolCall(ctx, extensions.ToolCallEvent{ToolCallID: call.ToolCall.ID, ToolName: call.ToolCall.Name, Input: call.ToolCall.Arguments})
	if result == nil {
		return nil, nil
	}
	return &agent.BeforeToolCallResult{Block: result.Block, Reason: result.Reason}, nil
}

func (runtime *SessionRuntime) afterExtensionToolCall(ctx context.Context, call agent.AfterToolCallContext) (*agent.AfterToolCallResult, error) {
	state := runtime.extensionState
	if state == nil || state.runner == nil || !state.runner.HasHandlers(extensions.EventToolResult) {
		return nil, nil
	}
	result := state.runner.EmitToolResult(ctx, extensions.ToolResultEvent{
		ToolCallID: call.ToolCall.ID, ToolName: call.ToolCall.Name, Input: call.ToolCall.Arguments,
		Content: call.Result.Content, Details: call.Result.Details, IsError: call.IsError,
	})
	if result == nil {
		return nil, nil
	}
	patch := &agent.AfterToolCallResult{}
	if result.Content != nil {
		patch.Content = *result.Content
	}
	if result.Details != nil {
		patch.Details = *result.Details
		patch.DetailsSet = true
	}
	patch.IsError = result.IsError
	return patch, nil
}

func (runtime *SessionRuntime) sendExtensionMessage(ctx context.Context, message extensions.CustomMessage, options *extensions.SendMessageOptions) error {
	content := message.Content
	if content == nil {
		content = []any{}
	}
	appMessage := &harness.CustomMessage{Role: "custom", CustomType: message.CustomType, Content: content, Display: message.Display, Details: message.Details, Timestamp: time.Now().UnixMilli()}
	state := runtime.extensionState
	if options != nil && options.DeliverAs == extensions.DeliverNextTurn {
		state.mu.Lock()
		state.pendingNextTurn = append(state.pendingNextTurn, appMessage)
		state.mu.Unlock()
		return nil
	}
	if !runtime.agent.IsIdle() {
		if options != nil && options.DeliverAs == extensions.DeliverFollowUp {
			runtime.agent.FollowUp(appMessage)
		} else {
			runtime.agent.Steer(appMessage)
		}
		return nil
	}
	if options != nil && options.TriggerTurn {
		return runtime.runPolicies(ctx, func() error { return runtime.agent.Prompt(ctx, appMessage) })
	}
	runtime.agent.AppendMessage(appMessage)
	// Upstream persists the untyped input value even though its in-memory message is normalized.
	var err error
	if message.Details != nil {
		_, err = runtime.manager.AppendCustomMessageEntry(message.CustomType, message.Content, message.Display, message.Details)
	} else {
		_, err = runtime.manager.AppendCustomMessageEntry(message.CustomType, message.Content, message.Display)
	}
	if err != nil {
		return err
	}
	runtime.emit(agent.MessageStartEvent{Message: appMessage})
	runtime.emit(agent.MessageEndEvent{Message: appMessage})
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
	if options != nil && options.DeliverAs != "" {
		behavior := options.DeliverAs
		streamingBehavior = &behavior
	}
	return runtime.promptExtensionInput(ctx, text, images, extensions.InputExtension, false, streamingBehavior, true, nil)
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
	previous := runtime.agent.State().Model
	thinkingLevel := runtime.agent.State().ThinkingLevel
	if previous == nil || !previous.Reasoning {
		thinkingLevel = runtime.settings.GetDefaultThinkingLevel()
		if thinkingLevel == "" {
			thinkingLevel = ai.ModelThinkingMedium
		}
	}
	runtime.agent.SetModel(model)
	if _, err := runtime.manager.AppendModelChange(string(model.Provider), model.ID); err != nil {
		return false, err
	}
	if err := runtime.setExtensionThinkingLevel(thinkingLevel); err != nil {
		return false, err
	}
	if state.runner.HasHandlers(extensions.EventModelSelect) && !sameModel(previous, model) {
		state.runner.Emit(ctx, extensions.ModelSelectEvent{Model: model, PreviousModel: previous, Source: extensions.ModelSelectSet})
	}
	return true, nil
}

func (runtime *SessionRuntime) setExtensionThinkingLevel(level agent.ThinkingLevel) error {
	previous := runtime.agent.State().ThinkingLevel
	effective := clampExtensionThinkingLevel(runtime.agent.State().Model, level)
	runtime.agent.SetThinkingLevel(effective)
	if effective == previous {
		return nil
	}
	if _, err := runtime.manager.AppendThinkingLevelChange(string(effective)); err != nil {
		return err
	}
	runtime.emit(ThinkingLevelChangedEvent{Level: effective})
	state := runtime.extensionState
	if state != nil && state.runner.HasHandlers(extensions.EventThinkingLevelSelect) {
		state.runner.Emit(context.Background(), extensions.ThinkingLevelSelectEvent{Level: effective, PreviousLevel: previous})
	}
	return nil
}

func sameModel(left, right *ai.Model) bool {
	return left != nil && right != nil && left.Provider == right.Provider && left.ID == right.ID
}

func clampExtensionThinkingLevel(model *ai.Model, level agent.ThinkingLevel) agent.ThinkingLevel {
	levels := []agent.ThinkingLevel{
		agent.ThinkingOff, agent.ThinkingMinimal, agent.ThinkingLow, agent.ThinkingMedium,
		agent.ThinkingHigh, agent.ThinkingXHigh, agent.ThinkingMax,
	}
	available := levels[:5]
	if model == nil {
		for _, candidate := range available {
			if candidate == level {
				return level
			}
		}
		return agent.ThinkingOff
	}
	if !model.Reasoning {
		return agent.ThinkingOff
	}
	available = make([]agent.ThinkingLevel, 0, len(levels))
	for _, candidate := range levels {
		var mapped *string
		present := false
		if model.ThinkingLevelMap != nil {
			mapped, present = (*model.ThinkingLevelMap)[ai.ModelThinkingLevel(candidate)]
			if present && mapped == nil {
				continue
			}
		}
		if (candidate == agent.ThinkingXHigh || candidate == agent.ThinkingMax) && !present {
			continue
		}
		available = append(available, candidate)
	}
	for _, supported := range available {
		if supported == level {
			return level
		}
	}
	if len(available) == 0 {
		return agent.ThinkingOff
	}
	requested := -1
	for index, candidate := range levels {
		if candidate == level {
			requested = index
			break
		}
	}
	if requested < 0 {
		return available[0]
	}
	for index := requested; index < len(levels); index++ {
		for _, supported := range available {
			if supported == levels[index] {
				return supported
			}
		}
	}
	for index := requested - 1; index >= 0; index-- {
		for _, supported := range available {
			if supported == levels[index] {
				return supported
			}
		}
	}
	return agent.ThinkingOff
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
	if ctx == nil {
		ctx = context.Background()
	}
	var operations tools.BashOperations
	state := runtime.extensionState
	if state != nil && state.runner != nil && state.runner.HasHandlers(extensions.EventUserBash) {
		result := state.runner.EmitUserBash(ctx, extensions.UserBashEvent{Command: command, ExcludeFromContext: excludeFromContext, CWD: runtime.manager.GetCWD()})
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
	output := tools.NewOutputAccumulator()
	var outputErr error
	var outputErrMu sync.Mutex
	result, executeErr := operations.Exec(ctx, resolvedCommand, runtime.manager.GetCWD(), tools.BashExecOptions{
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
	cancelled := errors.Is(ctx.Err(), context.Canceled)
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

func (runtime *SessionRuntime) recordBashResult(command string, result extensions.BashResult, exclude bool) error {
	excludeFromContext := exclude
	message := harness.BashExecutionMessage{
		Role: "bashExecution", Command: command, Output: result.Output, ExitCode: result.ExitCode,
		Cancelled: result.Cancelled, Truncated: result.Truncated, FullOutputPath: result.FullOutput,
		ExcludeFromContext: &excludeFromContext, Timestamp: runtime.clock(),
	}
	if runtime.agent.State().IsStreaming {
		runtime.mu.Lock()
		runtime.pendingBash = append(runtime.pendingBash, message)
		runtime.mu.Unlock()
		return nil
	}
	return runtime.appendBash(message)
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

func (runtime *SessionRuntime) extensionLifecycleEvent(ctx context.Context, event agent.AgentEvent) agent.AgentEvent {
	state := runtime.extensionState
	if state == nil || state.runner == nil {
		return event
	}
	runner := state.runner
	switch typed := event.(type) {
	case agent.AgentStartEvent:
		state.mu.Lock()
		state.turnIndex = 0
		state.mu.Unlock()
		runner.Emit(ctx, extensions.AgentStartEvent{})
	case agent.AgentEndEvent:
		runner.Emit(ctx, extensions.AgentEndEvent{Messages: typed.Messages})
	case agent.TurnStartEvent:
		state.mu.Lock()
		index := state.turnIndex
		state.mu.Unlock()
		runner.Emit(ctx, extensions.TurnStartEvent{TurnIndex: index, Timestamp: time.Now().UnixMilli()})
	case agent.TurnEndEvent:
		state.mu.Lock()
		index := state.turnIndex
		state.turnIndex++
		state.mu.Unlock()
		runner.Emit(ctx, extensions.TurnEndEvent{TurnIndex: index, Message: typed.Message, ToolResults: typed.ToolResults})
	case agent.MessageStartEvent:
		runner.Emit(ctx, extensions.MessageStartEvent{Message: typed.Message})
	case agent.MessageUpdateEvent:
		runner.Emit(ctx, extensions.MessageUpdateEvent{Message: typed.Message, AssistantMessageEvent: typed.AssistantMessageEvent})
	case agent.MessageEndEvent:
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
	case agent.ToolExecutionStartEvent:
		runner.Emit(ctx, extensions.ToolExecutionStartEvent{ToolCallID: typed.ToolCallID, ToolName: typed.ToolName, Args: typed.Args})
	case agent.ToolExecutionUpdateEvent:
		runner.Emit(ctx, extensions.ToolExecutionUpdateEvent{ToolCallID: typed.ToolCallID, ToolName: typed.ToolName, Args: typed.Args, PartialResult: typed.PartialResult})
	case agent.ToolExecutionEndEvent:
		runner.Emit(ctx, extensions.ToolExecutionEndEvent{ToolCallID: typed.ToolCallID, ToolName: typed.ToolName, Result: typed.Result, IsError: typed.IsError})
	}
	return event
}

func normalizeExtensionMessage(message agent.AgentMessage) agent.AgentMessage {
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

func replaceAgentMessageInPlace(target, replacement agent.AgentMessage) bool {
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
	preflightResult func(bool),
) error {
	state := runtime.extensionState
	if state == nil || state.runner == nil {
		if runPreflight {
			if err := runtime.PromptPreflight(ctx); err != nil {
				if preflightResult != nil {
					preflightResult(false)
				}
				return err
			}
			if preflightResult != nil {
				preflightResult(true)
			}
		}
		return runtime.runPolicies(ctx, func() error { return runtime.agent.Prompt(ctx, text, images...) })
	}
	if commands && strings.HasPrefix(text, "/") {
		commandText := strings.TrimPrefix(text, "/")
		name, args, _ := strings.Cut(commandText, " ")
		if state.runner.ExecuteCommand(ctx, name, args) {
			if preflightResult != nil {
				preflightResult(true)
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
				preflightResult(true)
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
			if preflightResult != nil {
				preflightResult(false)
			}
			return errors.New("Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.") //nolint:staticcheck // User-visible error matches upstream.
		}
		message := userMessageWithImages(text, images)
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
			preflightResult(true)
		}
		return nil
	}
	if runPreflight {
		if err := runtime.PromptPreflight(ctx); err != nil {
			if preflightResult != nil {
				preflightResult(false)
			}
			return err
		}
		if preflightResult != nil {
			preflightResult(true)
		}
	}

	state.mu.Lock()
	basePrompt := state.baseSystemPrompt
	if state.promptOptions == nil {
		basePrompt = runtime.agent.State().SystemPrompt
	}
	state.systemPromptOverride = nil
	options := runtime.extensionSystemPromptOptionsLocked(state)
	pending := append(agent.AgentMessages(nil), state.pendingNextTurn...)
	state.pendingNextTurn = nil
	state.mu.Unlock()
	runtime.agent.SetSystemPrompt(basePrompt)

	var injected agent.AgentMessages
	if state.runner.HasHandlers(extensions.EventBeforeAgentStart) {
		result := state.runner.EmitBeforeAgentStart(ctx, text, images, basePrompt, options)
		if result != nil {
			for _, message := range result.Messages {
				content := message.Content
				if content == nil {
					content = []any{}
				}
				injected = append(injected, &harness.CustomMessage{Role: "custom", CustomType: message.CustomType, Content: content, Display: message.Display, Details: message.Details, Timestamp: time.Now().UnixMilli()})
			}
			if result.SystemPrompt != nil {
				state.mu.Lock()
				prompt := *result.SystemPrompt
				state.systemPromptOverride = &prompt
				state.mu.Unlock()
				runtime.agent.SetSystemPrompt(*result.SystemPrompt)
			}
		}
	}
	messages := make(agent.AgentMessages, 0, 1+len(pending)+len(injected))
	messages = append(messages, userMessageWithImages(text, images))
	messages = append(messages, pending...)
	messages = append(messages, injected...)
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
