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

	// SessionManager controls session persistence. When nil a persistent
	// session is created for CWD (matching upstream default).
	SessionManager *sessionstore.SessionManager

	// Settings controls compaction, retry, and other runtime behavior.
	// When nil a default SettingsManager is created for CWD.
	Settings *config.SettingsManager

	// Resources supplies context files, skills, prompt templates, and the
	// system prompt. When nil resources are discovered from CWD and AgentDir.
	Resources *Resources

	// ExtensionRegistry holds registered extensions. When non-nil and
	// non-empty, extensions are bound to the session runtime.
	ExtensionRegistry *extensions.Registry

	// SessionStartEvent metadata emitted when extensions bind.
	SessionStartEvent *extensions.SessionStartEvent

	// SlashResolver handles /command and /skill expansion. When nil prompts
	// are sent verbatim.
	SlashResolver *SlashResolver
}

// AgentSessionResult is returned by [NewAgentSession].
type AgentSessionResult struct {
	// Session is the created agent session, ready for prompting.
	Session *AgentSession

	// ExtensionRegistry is the extension registry used (may be nil if no
	// extensions were configured).
	ExtensionRegistry *extensions.Registry

	// ModelFallbackMessage is set when a continued session's saved model
	// could not be restored.
	ModelFallbackMessage string
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

	streamFn := opts.StreamFn
	if streamFn == nil {
		streamFn = aiapi.StreamSimple
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
		thinking = clampThinkingLevel(model, thinking)
	} else {
		thinking = ai.ModelThinkingOff
	}

	// Resolve tool allowlist.
	var allowedToolNames *[]string
	initialActiveToolNames := resolveInitialTools(opts.Tools, opts.NoTools, opts.ExcludeTools)

	if opts.Tools != nil {
		names := filterExcluded(opts.Tools, opts.ExcludeTools)
		allowedToolNames = &names
	} else if opts.NoTools == "all" {
		empty := []string{}
		allowedToolNames = &empty
	}

	// Build system prompt from resources.
	resources := opts.Resources
	if resources == nil {
		loaded := LoadResources(ResourceOptions{CWD: cwd, AgentDir: agentDir})
		resources = &loaded
	}

	systemPrompt := buildSystemPromptFromResources(resources)

	// Construct built-in tools for the resolved CWD.
	baseTools, err := buildBuiltInTools(cwd, settings)
	if err != nil {
		return nil, err
	}

	// Register custom tools as extension tools if an extension registry is
	// provided, or wrap them into base tools.
	registry := opts.ExtensionRegistry
	if len(opts.CustomTools) > 0 {
		if registry == nil {
			registry = extensions.NewRegistry(cwd)
		}
		_ = registry.Register("<sdk:custom-tools>", func(api extensions.API) error {
			for _, tool := range opts.CustomTools {
				api.RegisterTool(tool)
			}
			return nil
		})
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
	agentOpts = append(agentOpts, agent.WithSimpleStreamOptions(ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		Transport:       &transport,
		SessionID:       &sessionID,
		TimeoutMS:       providerRetry.TimeoutMS,
		MaxRetries:      providerRetry.MaxRetries,
		MaxRetryDelayMS: &maxRetryDelay,
	}}))
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
		SystemPromptOptions:    promptOptions,
		SessionStartEvent:      opts.SessionStartEvent,
	}

	runtime, err := NewSessionRuntime(runtimeCfg)
	if err != nil {
		return nil, err
	}

	return &AgentSessionResult{
		Session:              runtime,
		ExtensionRegistry:    registry,
		ModelFallbackMessage: fallback,
	}, nil
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

func buildBuiltInTools(cwd string, settings *config.SettingsManager) ([]agent.AgentTool, error) {
	shellPath, err := settings.GetShellPath()
	if err != nil {
		return nil, err
	}
	autoResizeImages := settings.GetImageAutoResize()
	return []agent.AgentTool{
		tools.NewReadTool(cwd, &tools.ReadToolOptions{AutoResizeImages: &autoResizeImages}),
		tools.NewBashTool(cwd, &tools.BashToolOptions{
			ShellPath:     shellPath,
			CommandPrefix: settings.GetShellCommandPrefix(),
		}),
		tools.NewEditTool(cwd, nil),
		tools.NewWriteTool(cwd, nil),
		tools.NewGrepTool(cwd, nil),
		tools.NewFindTool(cwd, nil),
		tools.NewLsTool(cwd, nil),
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
// The channel is closed when cancel is called. Slow consumers that fill the
// buffer cause events to be dropped silently; size the buffer for your
// consumption rate.
func (runtime *SessionRuntime) SubscribeChan(bufferSize int) (<-chan any, func()) {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	ch := make(chan any, bufferSize)
	// ponytail: mutex makes send and close mutually exclusive; atomic would race between check and send.
	var mu sync.Mutex
	closed := false
	unsub := runtime.Subscribe(func(event any) {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		select {
		case ch <- event:
		default:
		}
	})
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			mu.Lock()
			closed = true
			close(ch)
			mu.Unlock()
			unsub()
		})
	}
	return ch, cancel
}

// PromptSync sends a prompt and blocks until the agent settles. It is a
// convenience wrapper combining Prompt + WaitForIdle.
func (runtime *SessionRuntime) PromptSync(ctx context.Context, text string) error {
	if err := runtime.Prompt(ctx, text); err != nil {
		return err
	}
	return runtime.WaitForIdle(ctx)
}
