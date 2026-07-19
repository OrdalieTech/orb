// Package codingagent provides the high-level SDK for embedding pi's agent
// capabilities in Go programs.
//
// The primary entry point is [NewAgentSession], which creates a fully
// configured [AgentSession] from an [AgentSessionOptions] struct.
// The returned session supports [AgentSession.Prompt],
// [AgentSession.Subscribe], and [AgentSession.SubscribeChan] for
// event-driven interaction with the underlying agent.
//
// See docs/sdk.md for usage patterns and the examples/ directory for
// runnable programs covering minimal, custom-model, tools, settings,
// session management, and full-control configurations.
package codingagent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	aiapi "github.com/OrdalieTech/pi-go/ai/api"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
)

// AgentSession is the public embedding type. It wraps the internal
// SessionRuntime and exposes the full agent lifecycle: prompting, event
// subscription, model/thinking management, compaction, and tree navigation.
type AgentSession = SessionRuntime

// AgentSessionOptions configures [NewAgentSession]. Fields mirror upstream
// createAgentSession options; zero values select sensible defaults.
type AgentSessionOptions struct {
	// CWD is the working directory for tool execution and resource discovery.
	// Defaults to the SessionManager's CWD if set, else ".".
	CWD string

	// AgentDir is the global config directory (auth.json, models.json, skills,
	// extensions). Defaults to ~/.pi/agent.
	AgentDir string

	// Model selects the initial model. When nil, NewAgentSession restores the
	// session model, then tries the settings default and available models.
	Model *ai.Model

	// ThinkingLevel sets the initial thinking budget. Clamped to the model's
	// supported range. Zero value defaults to "medium" for reasoning models
	// or "off" otherwise.
	ThinkingLevel ai.ModelThinkingLevel

	// ScopedModels restricts model cycling (CycleModel) to this set.
	ScopedModels []ScopedModel

	// StreamFn provides the LLM streaming backend. Defaults to the built-in
	// provider dispatcher when nil.
	StreamFn agent.StreamFn

	// GetAPIKey resolves API keys at request time. With the default StreamFn,
	// a nil resolver is derived from ModelRegistry.
	GetAPIKey agent.GetAPIKeyFunc

	// GetRequestAuth resolves request-time auth (OAuth tokens, Copilot baseURL).
	// When set, takes precedence over GetAPIKey for API key resolution; with
	// the default StreamFn, a nil resolver is derived from ModelRegistry.
	GetRequestAuth agent.GetRequestAuthFunc

	// GetModelHeaders provides per-request headers (e.g. attribution).
	GetModelHeaders agent.GetModelHeadersFunc

	// AvailableModels returns all models the host considers available.
	// Used by CycleModel when ScopedModels is empty.
	AvailableModels func() []ai.Model

	// ModelRegistry provides model resolution, auth checking, available-model
	// discovery, and session model restoration. Created from AgentDir when nil.
	ModelRegistry *config.ModelRegistry

	// NoTools suppresses default tool construction:
	//   "all"     — start with no tools at all
	//   "builtin" — disable default built-ins but keep extension/custom tools
	NoTools string

	// Tools is an allowlist of tool names. When provided, only listed tools
	// are enabled. Applies to built-in, extension, and custom tools.
	// When nil, the default set (read, bash, edit, write) is used unless
	// NoTools changes that.
	Tools []string

	// ExcludeTools is a denylist of tool names. Applied after Tools.
	ExcludeTools []string

	// CustomTools registers additional tool definitions alongside built-ins.
	CustomTools []extensions.ToolDefinition

	// ToolOptions overrides per-tool construction options for built-in tools.
	// Nil fields keep local defaults; settings-derived fields (AutoResizeImages,
	// ShellPath, CommandPrefix) are still applied when unset on the override.
	//
	// The options — including the nested per-tool structs — are captured by
	// the session and re-read whenever tools are rebuilt (also by
	// extension-driven tool rebuilds); they must not be mutated after
	// NewAgentSession returns.
	ToolOptions *tools.ToolsOptions

	// SessionManager controls session persistence. When nil a persistent
	// session is created for CWD (matching upstream default).
	SessionManager *sessionstore.SessionManager

	// Settings controls compaction, retry, and other runtime behavior.
	// When nil a default SettingsManager is created for CWD.
	Settings *config.SettingsManager

	// Resources supplies context files, skills, prompt templates, and the
	// system prompt. ResourceLoader takes precedence when both are provided.
	Resources *Resources

	// ResourceLoader supplies reloadable resources and native extensions. When
	// nil with no Resources override, DefaultResourceLoader is used.
	ResourceLoader ResourceLoader

	// ExtensionRegistry holds registered extensions. When non-nil and
	// non-empty, extensions are bound to the session runtime.
	ExtensionRegistry *extensions.Registry

	// SessionStartEvent metadata emitted when extensions bind.
	SessionStartEvent *extensions.SessionStartEvent

	// DeferExtensionStart leaves session_start activation to
	// [SessionRuntime.BindExtensions]. Runtime hosts use it so setup and host
	// rebinding finish before extensions observe the new session.
	DeferExtensionStart bool

	// ProjectTrustContext is supplied by replacement hosts for the effective
	// CWD so a custom runtime factory can resolve project trust before loading
	// project-scoped services.
	ProjectTrustContext extensions.ProjectTrustContext

	// SlashResolver handles /command and /skill expansion. When nil it is
	// derived from discovered skills and prompt templates.
	SlashResolver *SlashResolver
}

// AgentSessionRuntimeDiagnostic is a non-fatal issue collected while creating
// cwd-bound SDK services.
type AgentSessionRuntimeDiagnostic struct {
	Type    string
	Message string
}

// AgentSessionServices are the cwd-bound services used by one session
// instance. A replacement runtime exposes the newly resolved set after every
// switch, fork, or new-session operation.
type AgentSessionServices struct {
	CWD               string
	AgentDir          string
	SettingsManager   *config.SettingsManager
	ModelRegistry     *config.ModelRegistry
	Resources         *Resources
	ResourceLoader    ResourceLoader
	ExtensionRegistry *extensions.Registry
	Diagnostics       []AgentSessionRuntimeDiagnostic
}

var errMissingAgentSessionServices = errors.New("codingagent: agent session services are required")

// AgentSessionResult is returned by [NewAgentSession].
type AgentSessionResult struct {
	// Session is the created agent session, ready for prompting.
	Session *AgentSession

	// ExtensionRegistry is the extension registry used (may be nil if no
	// extensions were configured).
	ExtensionRegistry *extensions.Registry

	// ModelFallbackMessage is set when no model can be selected or when a
	// continued session's saved model cannot be restored.
	ModelFallbackMessage string

	// Services contains the cwd-bound services used to build Session.
	Services *AgentSessionServices

	// Diagnostics contains non-fatal creation issues for the host to present.
	Diagnostics []AgentSessionRuntimeDiagnostic
}

// DefaultActiveToolNames is the upstream default tool set.
var DefaultActiveToolNames = []string{"read", "bash", "edit", "write"}

// NewAgentSession creates a fully configured [AgentSession]. It mirrors
// upstream's createAgentSession: it creates the internal Agent, wires
// streaming, resolves model and thinking-level defaults from any existing
// session state, constructs built-in tools, and returns a ready-to-prompt
// session.
//
//	result, err := codingagent.NewAgentSession(codingagent.AgentSessionOptions{
//	    StreamFn:       provider.StreamSimple,
//	    SessionManager: sessionMgr,
//	    Model:          &model,
//	})
//	if err != nil { ... }
//	defer result.Session.Dispose()
//	result.Session.Prompt(ctx, "Hello")
func NewAgentSession(opts AgentSessionOptions) (*AgentSessionResult, error) {
	cwd := opts.CWD
	if cwd == "" && opts.SessionManager != nil {
		cwd = opts.SessionManager.GetCWD()
	}
	if cwd == "" {
		cwd = "."
	}
	normalizedCWD, err := config.NormalizePath(cwd)
	if err != nil {
		return nil, err
	}
	cwd, err = filepath.Abs(normalizedCWD)
	if err != nil {
		return nil, err
	}

	agentDir := opts.AgentDir
	if agentDir == "" {
		agentDir = DefaultAgentDir()
	}
	agentDir, err = config.NormalizePath(agentDir)
	if err != nil {
		return nil, err
	}
	agentDir, err = filepath.Abs(agentDir)
	if err != nil {
		return nil, err
	}

	modelRegistry := opts.ModelRegistry
	if modelRegistry == nil {
		modelRegistry, err = config.NewModelRegistry(agentDir)
		if err != nil {
			return nil, err
		}
	}

	sm := opts.SessionManager
	if sm == nil {
		sessionDir, err := sessionstore.DefaultSessionDir(cwd, agentDir)
		if err != nil {
			return nil, err
		}
		sm, err = sessionstore.Create(cwd, sessionDir)
		if err != nil {
			return nil, err
		}
	}

	settings := opts.Settings
	if settings == nil {
		var err error
		settings, err = config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
		if err != nil {
			return nil, err
		}
	}

	// Resolve resources and bind extension providers before model selection so
	// extension-supplied models participate in the same initial-model path as
	// built-ins and models.json entries.
	resourceLoader := opts.ResourceLoader
	resources := opts.Resources
	registry := opts.ExtensionRegistry
	if resourceLoader == nil && resources == nil {
		defaultLoader, loaderErr := NewDefaultResourceLoader(DefaultResourceLoaderOptions{
			CWD: cwd, AgentDir: agentDir, SettingsManager: settings,
		})
		if loaderErr != nil {
			return nil, loaderErr
		}
		if loaderErr = defaultLoader.Reload(context.Background(), nil); loaderErr != nil {
			return nil, loaderErr
		}
		resourceLoader = defaultLoader
	}
	if resourceLoader != nil {
		resources = resourcesFromLoader(resourceLoader)
		if registry == nil {
			registry = resourceLoader.GetExtensions()
		}
	}
	if resources == nil {
		resources = &Resources{}
	}
	if registry == nil {
		registry = extensions.NewRegistry(cwd)
	}
	providerDiagnostics := make([]AgentSessionRuntimeDiagnostic, 0)
	registry.BindModelRegistry(modelRegistry, func(extensionError extensions.ExtensionError) {
		providerDiagnostics = append(providerDiagnostics, AgentSessionRuntimeDiagnostic{
			Type: "error", Message: fmt.Sprintf("Extension %q error: %s", extensionError.ExtensionPath, extensionError.Error),
		})
	})

	streamFn := opts.StreamFn
	if streamFn == nil {
		streamFn = aiapi.StreamSimple
	}
	providerStreamFn := streamFn
	streamFn = func(
		ctx context.Context,
		model *ai.Model,
		request ai.Context,
		options *ai.SimpleStreamOptions,
	) (ai.AssistantMessageEventStream, error) {
		merged := ai.SimpleStreamOptions{}
		if options != nil {
			merged = *options
		}
		providerRetry := settings.GetProviderRetrySettings()
		if merged.TimeoutMS == nil {
			merged.TimeoutMS = providerRetry.TimeoutMS
		}
		if merged.TimeoutMS == nil {
			httpIdleTimeout, timeoutErr := settings.GetHTTPIdleTimeoutMS()
			if timeoutErr != nil {
				return nil, timeoutErr
			}
			if httpIdleTimeout == 0 {
				httpIdleTimeout = 2147483647
			}
			merged.TimeoutMS = &httpIdleTimeout
		}
		if merged.WebSocketConnectTimeoutMS == nil {
			webSocketConnectTimeout, timeoutErr := settings.GetWebSocketConnectTimeoutMS()
			if timeoutErr != nil {
				return nil, timeoutErr
			}
			merged.WebSocketConnectTimeoutMS = webSocketConnectTimeout
		}
		if merged.MaxRetries == nil {
			merged.MaxRetries = providerRetry.MaxRetries
		}
		return providerStreamFn(ctx, model, request, &merged)
	}

	existing := sm.BuildSessionContext()
	hasExisting := len(existing.Messages) > 0
	hasThinkingEntry := false
	for _, entry := range sm.GetBranch() {
		if entry.Type == "thinking_level_change" {
			hasThinkingEntry = true
			break
		}
	}

	model := opts.Model
	var fallback string

	// Restore saved model from session when no explicit model is provided.
	if model == nil && hasExisting && existing.Model != nil {
		restored, found := modelRegistry.Find(existing.Model.Provider, existing.Model.ModelID)
		if found && modelRegistry.HasConfiguredAuth(existing.Model.Provider, nil) {
			model = &restored
		}
		if model == nil {
			fallback = "Could not restore model " + existing.Model.Provider + "/" + existing.Model.ModelID
		}
	}

	// Fall back to settings default, then first available model.
	if model == nil {
		defaultProvider := settings.GetDefaultProvider()
		defaultModelID := settings.GetDefaultModel()
		if defaultProvider != "" && defaultModelID != "" {
			found, ok := modelRegistry.Find(defaultProvider, defaultModelID)
			if ok && modelRegistry.HasConfiguredAuth(defaultProvider, nil) {
				model = &found
			}
		}
		if model == nil {
			model = PreferredAvailableModel(modelRegistry.Available(nil))
		}
		if model == nil {
			fallback = formatNoModelsAvailableMessage()
		} else if fallback != "" {
			fallback += ". Using " + string(model.Provider) + "/" + model.ID
		}
	}

	thinking := opts.ThinkingLevel
	if thinking == "" {
		if hasExisting && hasThinkingEntry {
			thinking = ai.ModelThinkingLevel(existing.ThinkingLevel)
		} else {
			thinking = settings.GetDefaultThinkingLevel()
			if thinking == "" {
				thinking = ai.ModelThinkingMedium
			}
		}
	}
	if model != nil {
		thinking = ai.ClampThinkingLevel(model, thinking)
	} else {
		thinking = ai.ModelThinkingOff
	}

	// Resolve tool allowlist.
	var allowedToolNames *[]string
	initialActiveToolNames := resolveInitialTools(opts.Tools, opts.NoTools, opts.ExcludeTools)
	if sm.IsHarnessBacked() && opts.Tools == nil && opts.NoTools == "" && existing.ActiveToolNames != nil {
		initialActiveToolNames = filterExcluded(existing.ActiveToolNames, opts.ExcludeTools)
	}

	if opts.Tools != nil {
		names := filterExcluded(opts.Tools, opts.ExcludeTools)
		allowedToolNames = &names
	} else if opts.NoTools == "all" {
		empty := []string{}
		allowedToolNames = &empty
	}

	systemPrompt := buildSystemPromptFromResources(resources)

	// Construct built-in tools for the resolved CWD.
	baseTools, err := buildBuiltInTools(cwd, settings, opts.ToolOptions)
	if err != nil {
		return nil, err
	}

	// Register custom tools as synthetic SDK extensions so their source metadata
	// matches upstream's per-tool <sdk:name> paths.
	if len(opts.CustomTools) > 0 {
		for _, definition := range opts.CustomTools {
			definition := definition
			path := "<sdk:" + definition.Name + ">"
			if registry.HasPath(path) {
				continue
			}
			if err := registry.Register(path, func(api extensions.API) error {
				api.RegisterTool(definition)
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}

	// Build prompt options for system prompt assembly.
	promptOptions := buildPromptOptions(cwd, resources, initialActiveToolNames)
	assembledPrompt := systemPrompt
	if promptOptions != nil {
		assembledPrompt = BuildSystemPrompt(*promptOptions)
	}

	// Resolve active tools for the agent state. When extensions are present,
	// refreshExtensionTools handles this; otherwise we set tools directly.
	activeTools := resolveActiveTools(baseTools, initialActiveToolNames, allowedToolNames, opts.ExcludeTools)

	// Resolve auth callbacks. When the caller provides a custom StreamFn
	// they handle auth themselves (e.g. faux provider). When StreamFn is
	// nil (defaulting to real HTTP streaming), auto-construct resolvers
	// from the ModelRegistry so auth.json credentials, models.json
	// overrides, and built-in provider auth work automatically.
	getRequestAuth := opts.GetRequestAuth
	getAPIKey := opts.GetAPIKey
	getModelHeaders := opts.GetModelHeaders
	if getRequestAuth == nil && getAPIKey == nil && opts.StreamFn == nil {
		registryResolver := modelRegistry.DefaultRequestAuthResolver(nil)
		getRequestAuth = func(ctx context.Context, provider ai.ProviderID) (*agent.RequestAuth, error) {
			resolved, err := registryResolver(ctx, provider)
			if err != nil || resolved == nil {
				return nil, err
			}
			return &agent.RequestAuth{
				APIKey: resolved.APIKey, Headers: resolved.Headers,
				Env: resolved.Env, BaseURL: resolved.BaseURL,
			}, nil
		}
		getAPIKey = func(ctx context.Context, provider ai.ProviderID) (*string, error) {
			resolved, err := getRequestAuth(ctx, provider)
			if err != nil || resolved == nil {
				return nil, err
			}
			return resolved.APIKey, nil
		}
	}
	if getModelHeaders == nil && opts.StreamFn == nil {
		getModelHeaders = modelRegistry.DefaultModelHeadersResolver()
	}

	agentOpts := []agent.AgentOption{
		agent.WithInitialState(agent.AgentState{
			SystemPrompt:  assembledPrompt,
			Model:         model,
			ThinkingLevel: thinking,
			Tools:         activeTools,
		}),
		agent.WithStreamFn(streamFn),
		agent.WithConvertToLLM(ConvertToLLMWithBlockImages(settings.GetBlockImages)),
		agent.WithSteeringMode(agent.QueueMode(settings.GetSteeringMode())),
		agent.WithFollowUpMode(agent.QueueMode(settings.GetFollowUpMode())),
	}
	providerRetry := settings.GetProviderRetrySettings()
	maxRetryDelay := providerRetry.MaxRetryDelayMS
	transport := settings.GetTransport()
	sessionID := sm.GetSessionID()
	agentOpts = append(agentOpts, agent.WithSimpleStreamOptions(ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			Transport:       &transport,
			SessionID:       &sessionID,
			MaxRetryDelayMS: &maxRetryDelay,
		},
		ThinkingBudgets: settings.GetThinkingBudgets(),
	}))
	if getAPIKey != nil {
		agentOpts = append(agentOpts, agent.WithAPIKeyResolver(getAPIKey))
	}
	if getRequestAuth != nil {
		agentOpts = append(agentOpts, agent.WithRequestAuthResolver(getRequestAuth))
	}
	if getModelHeaders != nil {
		agentOpts = append(agentOpts, agent.WithModelHeadersResolver(getModelHeaders))
	}
	a := agent.NewAgent(agentOpts...)

	if hasExisting {
		messages := make(agent.AgentMessages, 0, len(existing.Messages))
		for _, raw := range existing.Messages {
			messages = append(messages, decodeSessionMessage(raw))
		}
		a.SetMessages(messages)
		if !hasThinkingEntry {
			if _, err := sm.AppendThinkingLevelChange(string(thinking)); err != nil {
				return nil, err
			}
		}
	} else {
		if model != nil {
			if _, err := sm.AppendModelChange(string(model.Provider), model.ID); err != nil {
				return nil, err
			}
		}
		if _, err := sm.AppendThinkingLevelChange(string(thinking)); err != nil {
			return nil, err
		}
	}

	// Prepare slash resolver with discovered resources.
	slashResolver := opts.SlashResolver
	if slashResolver == nil && resources != nil && (len(resources.Skills) > 0 || len(resources.PromptTemplates) > 0) {
		slashResolver = &SlashResolver{
			Skills:          resources.Skills,
			PromptTemplates: resources.PromptTemplates,
		}
	}

	availableModels := opts.AvailableModels
	if availableModels == nil {
		availableModels = func() []ai.Model { return modelRegistry.Available(nil) }
	}
	runtimeCfg := SessionRuntimeConfig{
		Agent:                  a,
		SessionManager:         sm,
		Settings:               settings,
		StreamFn:               streamFn,
		GetAPIKey:              getAPIKey,
		GetRequestAuth:         getRequestAuth,
		GetModelHeaders:        getModelHeaders,
		AvailableModels:        availableModels,
		ScopedModels:           opts.ScopedModels,
		SlashResolver:          slashResolver,
		ExtensionRegistry:      registry,
		ModelRegistry:          modelRegistry,
		BaseTools:              baseTools,
		InitialActiveToolNames: initialActiveToolNames,
		AllowedToolNames:       allowedToolNames,
		ExcludedToolNames:      opts.ExcludeTools,
		RebuildBaseTools: func() ([]agent.AgentTool, error) {
			return buildBuiltInTools(cwd, settings, opts.ToolOptions)
		},
		ResourceLoader:      resourceLoader,
		SystemPromptOptions: promptOptions,
		SessionStartEvent:   opts.SessionStartEvent,
		DeferExtensionStart: opts.DeferExtensionStart,
	}

	runtime, err := NewSessionRuntime(runtimeCfg)
	if err != nil {
		return nil, err
	}

	diagnostics := append(resourceRuntimeDiagnostics(resources), providerDiagnostics...)
	return &AgentSessionResult{
		Session:              runtime,
		ExtensionRegistry:    registry,
		ModelFallbackMessage: fallback,
		Services: &AgentSessionServices{
			CWD: cwd, AgentDir: agentDir, SettingsManager: settings,
			ModelRegistry: modelRegistry, Resources: resources, ResourceLoader: resourceLoader,
			ExtensionRegistry: registry,
			Diagnostics:       append([]AgentSessionRuntimeDiagnostic(nil), diagnostics...),
		},
		Diagnostics: diagnostics,
	}, nil
}

func resourceRuntimeDiagnostics(resources *Resources) []AgentSessionRuntimeDiagnostic {
	if resources == nil || len(resources.Diagnostics) == 0 {
		return nil
	}
	diagnostics := make([]AgentSessionRuntimeDiagnostic, 0, len(resources.Diagnostics))
	for _, diagnostic := range resources.Diagnostics {
		typeName := diagnostic.Type
		if typeName != "info" && typeName != "warning" && typeName != "error" {
			typeName = "warning"
		}
		diagnostics = append(diagnostics, AgentSessionRuntimeDiagnostic{Type: typeName, Message: diagnostic.Message})
	}
	return diagnostics
}

func formatNoModelsAvailableMessage() string {
	docsDir := filepath.Join(resolvePromptPackageDir(""), "docs")
	return "No models available. Use /login to log into a provider via OAuth or API key. See:\n  " +
		filepath.Join(docsDir, "providers.md") + "\n  " + filepath.Join(docsDir, "models.md")
}

func buildSystemPromptFromResources(res *Resources) string {
	if res == nil || res.SystemPrompt == nil {
		return ""
	}
	return *res.SystemPrompt
}

func buildPromptOptions(cwd string, res *Resources, activeTools []string) *SystemPromptOptions {
	if res == nil {
		return nil
	}
	snippets, guidelines := BuiltInToolPromptData(activeTools)
	var appendPrompt *string
	if joined := res.JoinedAppendSystemPrompt(); joined != nil {
		appendPrompt = joined
	}
	contextFiles := make([]ContextFile, len(res.ContextFiles))
	copy(contextFiles, res.ContextFiles)
	return &SystemPromptOptions{
		CustomPrompt:       res.SystemPrompt,
		SelectedTools:      activeTools,
		ToolSnippets:       snippets,
		PromptGuidelines:   guidelines,
		AppendSystemPrompt: appendPrompt,
		CWD:                cwd,
		ContextFiles:       contextFiles,
		Skills:             res.Skills,
	}
}

func buildBuiltInTools(cwd string, settings *config.SettingsManager, overrides *tools.ToolsOptions) ([]agent.AgentTool, error) {
	shellPath, err := settings.GetShellPath()
	if err != nil {
		return nil, err
	}
	autoResizeImages := settings.GetImageAutoResize()
	var resolved tools.ToolsOptions
	if overrides != nil {
		resolved = *overrides
	}
	var readOptions tools.ReadToolOptions
	if resolved.Read != nil {
		readOptions = *resolved.Read
	}
	if readOptions.AutoResizeImages == nil {
		readOptions.AutoResizeImages = &autoResizeImages
	}
	var bashOptions tools.BashToolOptions
	if resolved.Bash != nil {
		bashOptions = *resolved.Bash
	}
	if bashOptions.ShellPath == "" {
		bashOptions.ShellPath = shellPath
	}
	if bashOptions.CommandPrefix == "" {
		bashOptions.CommandPrefix = settings.GetShellCommandPrefix()
	}
	return []agent.AgentTool{
		tools.NewReadTool(cwd, &readOptions),
		tools.NewBashTool(cwd, &bashOptions),
		tools.NewEditTool(cwd, resolved.Edit),
		tools.NewWriteTool(cwd, resolved.Write),
		tools.NewGrepTool(cwd, resolved.Grep),
		tools.NewFindTool(cwd, resolved.Find),
		tools.NewLsTool(cwd, resolved.Ls),
	}, nil
}

func resolveInitialTools(toolsList []string, noTools string, excludeTools []string) []string {
	if toolsList != nil {
		return filterExcluded(toolsList, excludeTools)
	}
	if noTools == "all" || noTools == "builtin" {
		return []string{}
	}
	return filterExcluded(DefaultActiveToolNames, excludeTools)
}

func resolveActiveTools(baseTools []agent.AgentTool, activeNames []string, allowedNames *[]string, excluded []string) []agent.AgentTool {
	byName := make(map[string]agent.AgentTool, len(baseTools))
	for _, t := range baseTools {
		byName[t.Spec().Name] = t
	}
	excludeSet := make(map[string]struct{}, len(excluded))
	for _, n := range excluded {
		excludeSet[n] = struct{}{}
	}
	result := make([]agent.AgentTool, 0, len(activeNames))
	for _, name := range activeNames {
		if _, skip := excludeSet[name]; skip {
			continue
		}
		if allowedNames != nil {
			found := false
			for _, a := range *allowedNames {
				if a == name {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if t := byName[name]; t != nil {
			result = append(result, t)
		}
	}
	return result
}

func filterExcluded(names []string, excluded []string) []string {
	if len(excluded) == 0 {
		return append([]string(nil), names...)
	}
	excludeSet := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		excludeSet[name] = struct{}{}
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		if _, skip := excludeSet[name]; !skip {
			result = append(result, name)
		}
	}
	return result
}

// SubscribeChan returns a buffered channel of session events and a cancel
// function. Events are the same types delivered to [AgentSession.Subscribe]
// callbacks: [agent.AgentEvent] variants and session-level event structs
// ([AgentSettledEvent], [QueueUpdateEvent], etc.).
//
// Delivery is ordered and lossless while the subscription is active. The
// channel is closed promptly when cancel is called; events still queued at
// cancellation are discarded so cancellation never waits for a consumer.
func (runtime *SessionRuntime) SubscribeChan(bufferSize int) (<-chan any, func()) {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	out := make(chan any, bufferSize)
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	var mu sync.Mutex
	queue := make([]any, 0, bufferSize)
	stopped := false
	unsub := runtime.Subscribe(func(event any) {
		mu.Lock()
		if stopped {
			mu.Unlock()
			return
		}
		queue = append(queue, event)
		mu.Unlock()
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	go func() {
		defer close(out)
		for {
			mu.Lock()
			if len(queue) > 0 {
				event := queue[0]
				queue[0] = nil
				queue = queue[1:]
				mu.Unlock()
				select {
				case out <- event:
				case <-done:
					return
				}
				continue
			}
			isStopped := stopped
			mu.Unlock()
			if isStopped {
				return
			}
			select {
			case <-wake:
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			unsub()
			mu.Lock()
			stopped = true
			queue = nil
			mu.Unlock()
			close(done)
		})
	}
	return out, cancel
}

// PromptSync sends a prompt and blocks until the agent settles. It is a
// convenience wrapper combining Prompt + WaitForIdle.
func (runtime *SessionRuntime) PromptSync(ctx context.Context, text string) error {
	if err := runtime.Prompt(ctx, text); err != nil {
		return err
	}
	return runtime.WaitForIdle(ctx)
}
