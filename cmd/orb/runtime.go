package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/modes"
	"github.com/OrdalieTech/orb/agent/plugins"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/sandbox"
)

type runtimeInputs struct {
	Agent            *engine.Agent
	Settings         *config.SettingsManager
	StreamFn         engine.StreamFn
	AvailableModels  func() []ai.Model
	ScopedModels     []agent.ScopedModel
	GetAPIKey        engine.GetAPIKeyFunc
	GetRequestAuth   engine.GetRequestAuthFunc
	GetModelHeaders  engine.GetModelHeadersFunc
	SlashResolver    *agent.SlashResolver
	ModelRegistry    *config.ModelRegistry
	Extensions       *extensions.Registry
	BaseTools        []engine.AgentTool
	ActiveToolNames  []string
	AllowedTools     *[]string
	ExcludedTools    []string
	RebuildBaseTools func() ([]engine.AgentTool, error)
	PromptOptions    agent.SystemPromptOptions
	Auth             *config.AuthStorage
	RuntimeAuth      *runtimeCredentials
	Diagnostics      []modes.StartupDiagnostic
	// ResourceDiagnostics carries skill/prompt resource warnings, shown in
	// interactive mode only; upstream print/RPC modes print none of them.
	ResourceDiagnostics []modes.StartupDiagnostic
	ResourceLoader      agent.ResourceLoader
}

// runtimeCredentials is the Go port of upstream RuntimeCredentials: CLI API
// keys form a non-persistent overlay over auth.json, but otherwise participate
// in credential reads, status enumeration, and logout like stored API keys.
type runtimeCredentials struct {
	store aiauth.CredentialStore

	mu            sync.RWMutex
	overrides     map[string]string
	overrideOrder []string
}

func newRuntimeCredentials(store aiauth.CredentialStore) *runtimeCredentials {
	if store == nil {
		store = aiauth.NewMemoryStore(nil)
	}
	return &runtimeCredentials{store: store, overrides: make(map[string]string)}
}

func (credentials *runtimeCredentials) SetRuntimeAPIKey(providerID, apiKey string) {
	credentials.mu.Lock()
	if _, exists := credentials.overrides[providerID]; !exists {
		credentials.overrideOrder = append(credentials.overrideOrder, providerID)
	}
	credentials.overrides[providerID] = apiKey
	credentials.mu.Unlock()
}

func (credentials *runtimeCredentials) RemoveRuntimeAPIKey(providerID string) {
	credentials.mu.Lock()
	delete(credentials.overrides, providerID)
	credentials.overrideOrder = slices.DeleteFunc(credentials.overrideOrder, func(id string) bool { return id == providerID })
	credentials.mu.Unlock()
}

func (credentials *runtimeCredentials) HasRuntimeAPIKey(providerID string) bool {
	if credentials == nil {
		return false
	}
	credentials.mu.RLock()
	_, exists := credentials.overrides[providerID]
	credentials.mu.RUnlock()
	return exists
}

func (credentials *runtimeCredentials) Read(ctx context.Context, providerID string) (*aiauth.Credential, error) {
	credentials.mu.RLock()
	apiKey, exists := credentials.overrides[providerID]
	credentials.mu.RUnlock()
	if exists {
		return aiauth.APIKeyCredential(apiKey), nil
	}
	return credentials.store.Read(ctx, providerID)
}

func (credentials *runtimeCredentials) List(ctx context.Context) ([]aiauth.CredentialInfo, error) {
	entries, err := credentials.store.List(ctx)
	if err != nil {
		return nil, err
	}
	result := append([]aiauth.CredentialInfo(nil), entries...)
	byProvider := make(map[string]int, len(result))
	for index, entry := range result {
		byProvider[entry.ProviderID] = index
	}
	credentials.mu.RLock()
	defer credentials.mu.RUnlock()
	for _, providerID := range credentials.overrideOrder {
		if _, exists := credentials.overrides[providerID]; !exists {
			continue
		}
		entry := aiauth.CredentialInfo{ProviderID: providerID, Type: aiauth.CredentialAPIKey}
		if index, exists := byProvider[providerID]; exists {
			result[index] = entry
		} else {
			byProvider[providerID] = len(result)
			result = append(result, entry)
		}
	}
	return result, nil
}

func (credentials *runtimeCredentials) Modify(
	ctx context.Context,
	providerID string,
	modify aiauth.ModifyFunc,
) (*aiauth.Credential, error) {
	return credentials.store.Modify(ctx, providerID, modify)
}

func (credentials *runtimeCredentials) Delete(ctx context.Context, providerID string) error {
	credentials.RemoveRuntimeAPIKey(providerID)
	return credentials.store.Delete(ctx, providerID)
}

func createRuntimeInputs(cwd string, args CLIArgs, priorMessages engine.AgentMessages) (runtimeInputs, error) {
	args = normalizeRuntimeCLIArgs(args)
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return runtimeInputs{}, err
	}
	if _, err := config.MigrateAuthToAuthJSON(agentDir); err != nil {
		return runtimeInputs{}, err
	}
	authStorage, err := config.NewAuthStorage(filepath.Join(agentDir, "auth.json"))
	if err != nil {
		return runtimeInputs{}, err
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir), config.WithProjectTrusted(false))
	if err != nil {
		return runtimeInputs{}, err
	}
	config.ApplyHTTPProxySettings(settings.GetHTTPProxy())
	var trust projectTrustResolution
	if args.extensionsLoaded && args.resolvedProjectTrust != nil {
		// loadStartupExtensions already resolved trust in this process (firing
		// project_trust once) and its registry's extension host is live; a second
		// pre-trust load would fire the event again and replace that host.
		trust = projectTrustResolution{Trusted: *args.resolvedProjectTrust}
		settings.SetProjectTrusted(trust.Trusted)
	} else {
		resolved, err := resolveStartupProjectTrust(context.Background(), cwd, agentDir, args, settings)
		if err != nil {
			return runtimeInputs{}, err
		}
		trust = resolved
	}
	packageManager := agent.NewPackageManager(agent.PackageManagerOptions{
		CWD: cwd, AgentDir: agentDir, Settings: settings,
	})
	resolvedPaths, err := packageManager.Resolve(nil)
	if err != nil {
		return runtimeInputs{}, err
	}
	diagnostics := make([]modes.StartupDiagnostic, 0)
	diagnostics = append(diagnostics, trust.Diagnostics...)
	for _, diagnostic := range settings.DrainErrors() {
		diagnostics = append(diagnostics, otherDiagnostic(diagnostic.Error()))
	}
	extensionRegistry := args.extensionRegistry
	extensionDiagnostics := args.extensionWarnings
	if !args.extensionsLoaded {
		extensionRegistry, extensionDiagnostics = loadCompiledExtensions(cwd, agentDir, args, settings, resolvedPaths)
	}
	hasExtensions := hasNonControlExtensions(extensionRegistry)
	diagnostics = append(diagnostics, extensionDiagnostics...)
	var resourceLoader agent.ResourceLoader
	var resources agent.Resources
	var resourceDiagnostics []modes.StartupDiagnostic
	var activeTools, baseTools []engine.AgentTool
	var activeNames, initialNames, baseToolNames []string
	var allowedTools *[]string
	var excludedTools []string
	var promptOptions agent.SystemPromptOptions
	systemPrompt := ""
	// metadataOnly runs (--help, --list-models) need only extension flag and
	// provider metadata: skill/prompt/theme discovery, tool construction and the
	// system prompt are skipped, and ResourceDiagnostics stays empty.
	if !args.metadataOnly {
		defaultLoader, err := agent.NewDefaultResourceLoader(agent.DefaultResourceLoaderOptions{
			CWD: cwd, AgentDir: agentDir, SettingsManager: settings,
			AdditionalSkillPaths: args.Skills, AdditionalPromptTemplatePaths: args.PromptTemplates, AdditionalThemePaths: args.Themes,
			PackageSkillPaths: enabledPackageResourcePaths(resolvedPaths.Skills), PackagePromptTemplatePaths: enabledPackageResourcePaths(resolvedPaths.Prompts),
			PackageThemePaths: enabledPackageThemePaths(resolvedPaths.Themes),
			ExtensionRegistry: extensionRegistry, NoExtensions: args.NoExtensions,
			NoContextFiles: args.NoContextFiles, NoSkills: args.NoSkills, NoPromptTemplates: args.NoPromptTemplates, NoThemes: args.NoThemes,
			SystemPrompt: args.SystemPrompt, AppendSystemPrompt: args.AppendSystemPrompt,
		})
		if err != nil {
			return runtimeInputs{}, err
		}
		if err := defaultLoader.Reload(context.Background(), nil); err != nil {
			return runtimeInputs{}, err
		}
		resourceLoader = defaultLoader
		extensionRegistry = defaultLoader.GetExtensions()
		skills := defaultLoader.GetSkills()
		prompts := defaultLoader.GetPrompts()
		resources = agent.Resources{
			ContextFiles: defaultLoader.GetAgentsFiles().AgentsFiles, SystemPrompt: defaultLoader.GetSystemPrompt(),
			AppendSystemPrompt: defaultLoader.GetAppendSystemPrompt(), Skills: skills.Skills, PromptTemplates: prompts.Prompts,
		}
		resources.Diagnostics = append(resources.Diagnostics, skills.Diagnostics...)
		resources.Diagnostics = append(resources.Diagnostics, prompts.Diagnostics...)
		resourceDiagnostics = make([]modes.StartupDiagnostic, 0, len(resources.Diagnostics))
		for _, diagnostic := range resources.Diagnostics {
			resourceDiagnostics = append(resourceDiagnostics, startupResourceDiagnostic(diagnostic))
		}

		selection := ResolveBuiltInToolSelection(args)
		activeTools, err = createBuiltInTools(cwd, selection, settings)
		if err != nil {
			return runtimeInputs{}, err
		}
		activeNames = make([]string, 0, len(activeTools))
		for _, tool := range activeTools {
			activeNames = append(activeNames, tool.Spec().Name)
		}
		baseTools = activeTools
		initialNames = append([]string(nil), activeNames...)
		if hasExtensions {
			baseTools, err = createBuiltInTools(cwd, defaultBuiltInTools, settings)
			if err != nil {
				return runtimeInputs{}, err
			}
			if args.Tools != nil {
				initialNames = filterExcludedTools(args.Tools, args.ExcludeTools)
				allowed := append([]string(nil), args.Tools...)
				allowedTools = &allowed
			} else if args.NoTools {
				empty := []string{}
				allowedTools = &empty
			}
			excludedTools = append([]string(nil), args.ExcludeTools...)
		}
		baseToolNames = make([]string, 0, len(baseTools))
		for _, tool := range baseTools {
			baseToolNames = append(baseToolNames, tool.Spec().Name)
		}
		snippets, guidelines := agent.BuiltInToolPromptData(activeNames)
		promptOptions = agent.SystemPromptOptions{
			CustomPrompt:       resources.SystemPrompt,
			SelectedTools:      activeNames,
			ToolSnippets:       snippets,
			PromptGuidelines:   guidelines,
			AppendSystemPrompt: resources.JoinedAppendSystemPrompt(),
			CWD:                cwd,
			ContextFiles:       resources.ContextFiles,
			Skills:             resources.Skills,
		}
		systemPrompt = agent.BuildSystemPrompt(promptOptions)
	}
	if extensionRegistry == nil {
		extensionRegistry = extensions.NewRegistry(cwd)
	}

	registry, err := config.NewModelRegistry(agentDir)
	if err != nil {
		return runtimeInputs{}, err
	}
	if extensionRegistry != nil {
		extensionRegistry.BindModelRegistry(registry, func(extensionError extensions.ExtensionError) {
			diagnostics = append(diagnostics, modes.StartupDiagnostic{
				Kind:    modes.StartupDiagnosticExtension,
				Path:    extensionError.ExtensionPath,
				Message: fmt.Sprintf("%s: %s", extensionError.Event, extensionError.Error),
			})
		})
	}
	model, scopedThinking, scopedModels, modelDiagnostics, err := resolveRuntimeModel(args, settings, registry)
	if err != nil {
		return runtimeInputs{}, err
	}
	diagnostics = append(diagnostics, otherDiagnostics(modelDiagnostics)...)
	thinking := settings.GetDefaultThinkingLevel()
	if thinking == "" {
		thinking = ai.ModelThinkingMedium
	}
	if args.Thinking != nil {
		thinking = ai.ModelThinkingLevel(*args.Thinking)
	} else if scopedThinking != nil {
		thinking = *scopedThinking
	}
	if model == nil {
		thinking = ai.ModelThinkingOff
	}
	transport := settings.GetTransport()
	providerRetry := settings.GetProviderRetrySettings()
	maxRetryDelay := providerRetry.MaxRetryDelayMS
	streamFn := func(
		ctx context.Context,
		model *ai.Model,
		request ai.Context,
		options *ai.SimpleStreamOptions,
	) (ai.AssistantMessageEventStream, error) {
		merged := ai.SimpleStreamOptions{}
		if options != nil {
			merged = *options
		}
		currentRetry := settings.GetProviderRetrySettings()
		if merged.TimeoutMS == nil {
			merged.TimeoutMS = currentRetry.TimeoutMS
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
			merged.MaxRetries = currentRetry.MaxRetries
		}
		return registry.StreamSimple(ctx, model, request, &merged)
	}
	state := engine.AgentState{
		SystemPrompt:  systemPrompt,
		Model:         model,
		ThinkingLevel: thinking,
		Tools:         activeTools,
		Messages:      priorMessages,
	}
	var cliAPIKeyProvider *ai.ProviderID
	if args.APIKey != nil && *args.APIKey != "" && model != nil {
		provider := model.Provider
		cliAPIKeyProvider = &provider
	}
	runtimeAuth := newRuntimeCredentials(authStorage)
	if cliAPIKeyProvider != nil {
		runtimeAuth.SetRuntimeAPIKey(string(*cliAPIKeyProvider), *args.APIKey)
	}
	resolveRequestAuth := requestAuthResolverWithCredentials(registry, runtimeAuth)
	resolveAPIKey := func(ctx context.Context, providerID ai.ProviderID) (*string, error) {
		resolved, err := resolveRequestAuth(ctx, providerID)
		if err != nil || resolved == nil {
			return nil, err
		}
		return resolved.APIKey, nil
	}
	resolveModelHeaders := func(ctx context.Context, model *ai.Model, apiKey *string, env ai.ProviderEnv) (*map[string]string, error) {
		return registry.ResolveModelHeaders(ctx, *model, map[string]string(env), apiKey)
	}
	availableModels := func() []ai.Model {
		result, _ := registry.AvailableWithError(nil)
		if cliAPIKeyProvider != nil && runtimeAuth.HasRuntimeAPIKey(string(*cliAPIKeyProvider)) {
			for _, candidate := range registry.Models() {
				if candidate.Provider != *cliAPIKeyProvider || slices.ContainsFunc(result, func(model ai.Model) bool {
					return model.Provider == candidate.Provider && model.ID == candidate.ID
				}) {
					continue
				}
				result = append(result, candidate)
			}
		}
		return result
	}
	created := engine.NewAgent(
		streamFn, engine.WithInitialState(state),
		engine.WithConvertToLLM(agent.ConvertToLLMWithBlockImages(settings.GetBlockImages)),
		engine.WithSteeringMode(engine.QueueMode(settings.GetSteeringMode())),
		engine.WithFollowUpMode(engine.QueueMode(settings.GetFollowUpMode())),
		engine.WithSimpleStreamOptions(ai.SimpleStreamOptions{
			StreamOptions: ai.StreamOptions{
				Transport: &transport, TimeoutMS: providerRetry.TimeoutMS, MaxRetries: providerRetry.MaxRetries,
				MaxRetryDelayMS: &maxRetryDelay,
			},
			ThinkingBudgets: settings.GetThinkingBudgets(),
		}),
		engine.WithAPIKeyResolver(resolveAPIKey),
		engine.WithRequestAuthResolver(resolveRequestAuth),
		engine.WithModelHeadersResolver(resolveModelHeaders),
	)
	return runtimeInputs{
		Agent: created, Settings: settings, StreamFn: streamFn, AvailableModels: availableModels, ScopedModels: scopedModels, GetAPIKey: resolveAPIKey,
		GetRequestAuth:  resolveRequestAuth,
		GetModelHeaders: resolveModelHeaders,
		SlashResolver:   &agent.SlashResolver{Skills: resources.Skills, PromptTemplates: resources.PromptTemplates},
		ModelRegistry:   registry,
		Extensions:      extensionRegistry, BaseTools: baseTools, ActiveToolNames: initialNames,
		AllowedTools: allowedTools, ExcludedTools: excludedTools, PromptOptions: promptOptions,
		RebuildBaseTools: func() ([]engine.AgentTool, error) {
			return createBuiltInTools(cwd, baseToolNames, settings)
		},
		Auth:                authStorage,
		RuntimeAuth:         runtimeAuth,
		Diagnostics:         diagnostics,
		ResourceDiagnostics: resourceDiagnostics,
		ResourceLoader:      resourceLoader,
	}, nil
}

// startupResourceDiagnostic keeps the structure a resource diagnostic is born
// with; prompt collisions display under their slash-command spelling.
func startupResourceDiagnostic(diagnostic agent.ResourceDiagnostic) modes.StartupDiagnostic {
	if diagnostic.Type == "collision" && diagnostic.Collision != nil {
		name := diagnostic.Collision.Name
		if diagnostic.Collision.ResourceType == "prompt" {
			name = "/" + name
		}
		return modes.StartupDiagnostic{Kind: modes.StartupDiagnosticCollision, Path: diagnostic.Path, Message: fmt.Sprintf("%q", name)}
	}
	return modes.StartupDiagnostic{Kind: modes.StartupDiagnosticOther, Path: diagnostic.Path, Message: diagnostic.Message}
}

func hasNonControlExtensions(registry *extensions.Registry) bool {
	if registry == nil {
		return false
	}
	for _, extension := range registry.Extensions() {
		if extension.Path != "<inline:plugin-control>" {
			return true
		}
	}
	return false
}

func filterExcludedTools(names, excluded []string) []string {
	denied := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		denied[name] = struct{}{}
	}
	result := make([]string, 0, len(names))
	for _, name := range names {
		if _, exists := denied[name]; !exists {
			result = append(result, name)
		}
	}
	return result
}

// enabledPackageResourcePaths keeps enabled package-contributed resources;
// local and auto-discovered entries stay with the existing resource loaders.
func enabledPackageResourcePaths(resources []agent.ResolvedResource) []string {
	paths := make([]string, 0, len(resources))
	for _, resource := range resources {
		if resource.Enabled && resource.Metadata.Origin == "package" {
			paths = append(paths, resource.Path)
		}
	}
	return paths
}

func enabledPackageThemePaths(resources []agent.ResolvedResource) []agent.ResourcePath {
	paths := make([]agent.ResourcePath, 0, len(resources))
	for _, resource := range resources {
		if resource.Enabled && resource.Metadata.Origin == "package" {
			paths = append(paths, agent.ResourcePath{Path: resource.Path, Metadata: resource.Metadata})
		}
	}
	return paths
}

func resolveRuntimeModel(
	args CLIArgs,
	settings *config.SettingsManager,
	registry *config.ModelRegistry,
) (*ai.Model, *ai.ModelThinkingLevel, []agent.ScopedModel, []string, error) {
	args = normalizeRuntimeCLIArgs(args)
	all := registry.Models()
	available, err := registry.AvailableWithError(nil)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	diagnostics := make([]string, 0)
	patterns := args.Models
	if patterns == nil {
		patterns = settings.GetEnabledModels()
	}
	var scoped []agent.ScopedModel
	if len(patterns) > 0 {
		var warnings []agent.ModelDiagnostic
		scoped, warnings = agent.ResolveModelScope(patterns, available)
		for _, warning := range warnings {
			diagnostics = append(diagnostics, warning.Message)
		}
	}
	if args.Model == nil && len(scoped) > 0 && !args.RestoredModel {
		selected := 0
		defaultProvider, defaultID := settings.GetDefaultProvider(), settings.GetDefaultModel()
		if defaultProvider != "" && defaultID != "" {
			if index := slices.IndexFunc(scoped, func(candidate agent.ScopedModel) bool {
				return string(candidate.Model.Provider) == defaultProvider && candidate.Model.ID == defaultID
			}); index >= 0 {
				selected = index
			}
		}
		model := scoped[selected].Model
		return &model, scoped[selected].ThinkingLevel, scoped, diagnostics, nil
	}
	provider, pattern := "", ""
	restoreWarning := ""
	if args.Model != nil {
		pattern = *args.Model
		if args.Provider != nil {
			provider = *args.Provider
		}
		if args.RestoredModel {
			restored, found := registry.Find(provider, pattern)
			if found && registry.HasConfiguredAuth(string(restored.Provider), nil) {
				return &restored, nil, scoped, diagnostics, nil
			}
			restoreWarning = fmt.Sprintf("Could not restore model %s/%s", provider, pattern)
		} else {
			var cliThinking *ai.ModelThinkingLevel
			if args.Thinking != nil {
				level := ai.ModelThinkingLevel(*args.Thinking)
				cliThinking = &level
			}
			resolved := agent.ResolveCLIModel(provider, pattern, cliThinking, all, func(provider string) bool {
				return registry.HasConfiguredAuth(provider, nil)
			})
			if resolved.Error != "" {
				return nil, nil, scoped, diagnostics, fmt.Errorf("%s", resolved.Error)
			}
			if resolved.Warning != "" {
				diagnostics = append(diagnostics, resolved.Warning)
			}
			return resolved.Model, resolved.ThinkingLevel, scoped, diagnostics, nil
		}
	}
	defaultProvider, defaultID := settings.GetDefaultProvider(), settings.GetDefaultModel()
	if defaultProvider != "" && defaultID != "" && registry.HasConfiguredAuth(defaultProvider, nil) {
		if model, found := registry.Find(defaultProvider, defaultID); found {
			if restoreWarning != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s. Using %s/%s", restoreWarning, model.Provider, model.ID))
			}
			return &model, nil, scoped, diagnostics, nil
		}
	}
	model := agent.PreferredAvailableModel(available)
	if model == nil {
		if args.allowNoModel || args.useUnknownModel {
			if args.allowNoModel {
				diagnostics = append(diagnostics, strings.TrimSuffix(formatModelList(nil, ""), "\n"))
			}
			return nil, nil, scoped, diagnostics, nil
		}
		return nil, nil, scoped, diagnostics, fmt.Errorf("no model available; configure provider auth or use --model")
	}
	if restoreWarning != "" {
		diagnostics = append(diagnostics, fmt.Sprintf("%s. Using %s/%s", restoreWarning, model.Provider, model.ID))
	}
	return model, nil, scoped, diagnostics, nil
}

func normalizeRuntimeCLIArgs(args CLIArgs) CLIArgs {
	if args.Provider != nil && *args.Provider == "" {
		args.Provider = nil
	}
	if args.Model != nil && *args.Model == "" {
		args.Model = nil
	}
	return args
}

func requestAuthResolverWithCredentials(
	registry *config.ModelRegistry,
	credentials aiauth.CredentialStore,
) engine.GetRequestAuthFunc {
	var baseResolver func(context.Context, ai.ProviderID) (*config.RequestAuth, error)
	if registry != nil {
		baseResolver = registry.DefaultRequestAuthResolver(credentials)
	} else {
		baseResolver = config.FallbackRequestAuthResolver(credentials)
	}
	return func(ctx context.Context, providerID ai.ProviderID) (*engine.RequestAuth, error) {
		resolved, err := baseResolver(ctx, providerID)
		if err != nil || resolved == nil {
			return nil, err
		}
		return &engine.RequestAuth{
			APIKey: resolved.APIKey, Headers: resolved.Headers,
			Env: resolved.Env, BaseURL: resolved.BaseURL,
		}, nil
	}
}

func createBuiltInTools(cwd string, names []string, settings *config.SettingsManager) ([]engine.AgentTool, error) {
	result := make([]engine.AgentTool, 0, len(names))
	for _, name := range names {
		switch name {
		case "read":
			autoResize := settings.GetImageAutoResize()
			result = append(result, tools.NewReadTool(cwd, &tools.ReadToolOptions{AutoResizeImages: &autoResize}))
		case "bash":
			shellPath, err := settings.GetShellPath()
			if err != nil {
				return nil, err
			}
			var spawnHook tools.BashSpawnHook
			if mode := plugins.SandboxMode(settings); mode != sandbox.ModeDangerFullAccess {
				self, err := os.Executable()
				if err != nil {
					return nil, err
				}
				spawnHook = func(spawn tools.BashSpawnContext) tools.BashSpawnContext {
					spawn.Command, spawn.Env, _ = sandbox.Wrap(mode, spawn.Cwd, self, shellPath, spawn.Command, spawn.Env)
					return spawn
				}
			}
			result = append(result, tools.NewBashTool(cwd, &tools.BashToolOptions{
				ShellPath:     shellPath,
				CommandPrefix: settings.GetShellCommandPrefix(),
				SpawnHook:     spawnHook,
			}))
		case "edit":
			result = append(result, tools.NewEditTool(cwd, nil))
		case "write":
			result = append(result, tools.NewWriteTool(cwd, nil))
		case "grep":
			result = append(result, tools.NewGrepTool(cwd, nil))
		case "find":
			result = append(result, tools.NewFindTool(cwd, nil))
		case "ls":
			result = append(result, tools.NewLsTool(cwd, nil))
		}
	}
	return result, nil
}
