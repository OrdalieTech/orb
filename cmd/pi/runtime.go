package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
)

type runtimeInputs struct {
	Agent           *agent.Agent
	Settings        *config.SettingsManager
	AvailableModels func() []ai.Model
	ScopedModels    []codingagent.ScopedModel
	GetAPIKey       agent.GetAPIKeyFunc
	GetRequestAuth  agent.GetRequestAuthFunc
	GetModelHeaders agent.GetModelHeadersFunc
	SlashResolver   *codingagent.SlashResolver
	ModelRegistry   *config.ModelRegistry
	Extensions      *extensions.Registry
	BaseTools       []agent.AgentTool
	ActiveToolNames []string
	AllowedTools    *[]string
	ExcludedTools   []string
	PromptOptions   codingagent.SystemPromptOptions
	Diagnostics     []string
}

func createRuntimeInputs(cwd string, args CLIArgs, priorMessages agent.AgentMessages) (runtimeInputs, error) {
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
	projectTrusted := true
	if args.ProjectTrusted != nil {
		projectTrusted = *args.ProjectTrusted
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir), config.WithProjectTrusted(projectTrusted))
	if err != nil {
		return runtimeInputs{}, err
	}
	resources := codingagent.LoadResources(codingagent.ResourceOptions{
		CWD:                        cwd,
		AgentDir:                   agentDir,
		ProjectTrusted:             &projectTrusted,
		NoContextFiles:             args.NoContextFiles,
		NoSkills:                   args.NoSkills,
		NoPromptTemplates:          args.NoPromptTemplates,
		SystemPrompt:               args.SystemPrompt,
		AppendSystemPrompt:         args.AppendSystemPrompt,
		SkillPaths:                 args.Skills,
		PromptTemplatePaths:        args.PromptTemplates,
		GlobalSkillPaths:           settings.GetGlobalSkillPaths(),
		ProjectSkillPaths:          settings.GetProjectSkillPaths(),
		GlobalPromptTemplatePaths:  settings.GetGlobalPromptTemplatePaths(),
		ProjectPromptTemplatePaths: settings.GetProjectPromptTemplatePaths(),
	})
	diagnostics := make([]string, 0)
	for _, diagnostic := range settings.DrainErrors() {
		diagnostics = append(diagnostics, diagnostic.Error())
	}
	for _, diagnostic := range resources.Diagnostics {
		diagnostics = append(diagnostics, diagnostic.Message)
	}
	extensionRegistry := args.extensionRegistry
	extensionDiagnostics := args.extensionWarnings
	if !args.extensionsLoaded {
		extensionRegistry, extensionDiagnostics = loadCompiledExtensions(cwd, args, settings)
	}
	diagnostics = append(diagnostics, extensionDiagnostics...)

	selection := ResolveBuiltInToolSelection(args)
	activeTools, err := createBuiltInTools(cwd, selection, settings)
	if err != nil {
		return runtimeInputs{}, err
	}
	activeNames := make([]string, 0, len(activeTools))
	for _, tool := range activeTools {
		activeNames = append(activeNames, tool.Spec().Name)
	}
	baseTools := activeTools
	initialNames := append([]string(nil), activeNames...)
	var allowedTools *[]string
	var excludedTools []string
	if extensionRegistry != nil {
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
	snippets, guidelines := codingagent.BuiltInToolPromptData(activeNames)
	promptOptions := codingagent.SystemPromptOptions{
		CustomPrompt:       resources.SystemPrompt,
		SelectedTools:      activeNames,
		ToolSnippets:       snippets,
		PromptGuidelines:   guidelines,
		AppendSystemPrompt: resources.JoinedAppendSystemPrompt(),
		CWD:                cwd,
		ContextFiles:       resources.ContextFiles,
		Skills:             resources.Skills,
	}
	systemPrompt := codingagent.BuildSystemPrompt(promptOptions)

	registry, err := config.NewModelRegistry(agentDir)
	if err != nil {
		return runtimeInputs{}, err
	}
	model, scopedThinking, scopedModels, modelDiagnostics, err := resolveRuntimeModel(args, settings, registry)
	if err != nil {
		return runtimeInputs{}, err
	}
	diagnostics = append(diagnostics, modelDiagnostics...)
	thinking := settings.GetDefaultThinkingLevel()
	if thinking == "" {
		thinking = ai.ModelThinkingMedium
	}
	if args.Thinking != nil {
		thinking = ai.ModelThinkingLevel(*args.Thinking)
	} else if scopedThinking != nil {
		thinking = *scopedThinking
	}
	transport := settings.GetTransport()
	providerRetry := settings.GetProviderRetrySettings()
	maxRetryDelay := providerRetry.MaxRetryDelayMS
	state := agent.AgentState{
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
	resolveRequestAuth := requestAuthResolverForProvider(args, cliAPIKeyProvider, registry, authStorage)
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
		all := registry.Models()
		result := make([]ai.Model, 0, len(all))
		for _, candidate := range all {
			if registry.HasConfiguredAuth(string(candidate.Provider), nil) || cliAPIKeyProvider != nil && candidate.Provider == *cliAPIKeyProvider {
				result = append(result, candidate)
			}
		}
		return result
	}
	created := agent.NewAgent(
		agent.WithInitialState(state),
		agent.WithConvertToLLM(codingagent.ConvertToLLMWithBlockImages(settings.GetBlockImages)),
		agent.WithSteeringMode(agent.QueueMode(settings.GetSteeringMode())),
		agent.WithFollowUpMode(agent.QueueMode(settings.GetFollowUpMode())),
		agent.WithSimpleStreamOptions(ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
			Transport: &transport, TimeoutMS: providerRetry.TimeoutMS, MaxRetries: providerRetry.MaxRetries,
			MaxRetryDelayMS: &maxRetryDelay,
		}}),
		agent.WithAPIKeyResolver(resolveAPIKey),
		agent.WithRequestAuthResolver(resolveRequestAuth),
		agent.WithModelHeadersResolver(resolveModelHeaders),
	)
	return runtimeInputs{
		Agent: created, Settings: settings, AvailableModels: availableModels, ScopedModels: scopedModels, GetAPIKey: resolveAPIKey,
		GetRequestAuth:  resolveRequestAuth,
		GetModelHeaders: resolveModelHeaders,
		SlashResolver:   &codingagent.SlashResolver{Skills: resources.Skills, PromptTemplates: resources.PromptTemplates},
		ModelRegistry:   registry,
		Extensions:      extensionRegistry, BaseTools: baseTools, ActiveToolNames: initialNames,
		AllowedTools: allowedTools, ExcludedTools: excludedTools, PromptOptions: promptOptions,
		Diagnostics: diagnostics,
	}, nil
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

func resolveRuntimeModel(
	args CLIArgs,
	settings *config.SettingsManager,
	registry *config.ModelRegistry,
) (*ai.Model, *ai.ModelThinkingLevel, []codingagent.ScopedModel, []string, error) {
	args = normalizeRuntimeCLIArgs(args)
	all := registry.Models()
	diagnostics := make([]string, 0)
	patterns := args.Models
	if patterns == nil {
		patterns = settings.GetEnabledModels()
	}
	var scoped []codingagent.ScopedModel
	if len(patterns) > 0 {
		var warnings []codingagent.ModelDiagnostic
		scoped, warnings = codingagent.ResolveModelScope(patterns, registry.Available(nil))
		for _, warning := range warnings {
			diagnostics = append(diagnostics, warning.Message)
		}
	}
	if args.Model == nil && len(scoped) > 0 && !args.RestoredModel {
		selected := 0
		defaultProvider, defaultID := settings.GetDefaultProvider(), settings.GetDefaultModel()
		if defaultProvider != "" && defaultID != "" {
			if index := slices.IndexFunc(scoped, func(candidate codingagent.ScopedModel) bool {
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
			resolved := codingagent.ResolveCLIModel(provider, pattern, cliThinking, all, func(provider string) bool {
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
	model := codingagent.PreferredAvailableModel(registry.Available(nil))
	if model == nil {
		return nil, nil, scoped, diagnostics, fmt.Errorf("no model available; configure provider auth or use --model")
	}
	if restoreWarning != "" {
		diagnostics = append(diagnostics, fmt.Sprintf("%s. Using %s/%s", restoreWarning, model.Provider, model.ID))
	}
	return model, nil, scoped, diagnostics, nil
}

func resolveSkeletonModel(args CLIArgs, settings *config.SettingsManager) (*ai.Model, error) {
	args = normalizeSkeletonCLIArgs(args)
	providerID := ""
	modelID := ""
	if args.Model != nil {
		providerID = "openai"
		if args.Provider != nil {
			providerID = *args.Provider
		}
		modelID = *args.Model
	} else {
		// Settings defaults are a pair. A provider-only CLI argument is ignored,
		// matching upstream model selection precedence.
		providerID = settings.GetDefaultProvider()
		modelID = settings.GetDefaultModel()
		if providerID == "" || modelID == "" {
			return nil, fmt.Errorf("no model specified; use --model")
		}
	}
	if !strings.EqualFold(providerID, "openai") {
		return nil, fmt.Errorf("provider %q is not available in the phase-1 skeleton", providerID)
	}
	if modelID == "" {
		return nil, fmt.Errorf("no model specified; use --model")
	}
	providerID = "openai"
	return &ai.Model{
		ID:            modelID,
		Name:          modelID,
		API:           ai.APIOpenAIResponses,
		Provider:      ai.ProviderID(providerID),
		BaseURL:       "https://api.openai.com/v1",
		Reasoning:     true,
		Input:         ai.InputModalities{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{},
		ContextWindow: 128_000,
		MaxTokens:     16_384,
	}, nil
}

func normalizeSkeletonCLIArgs(args CLIArgs) CLIArgs {
	if args.Provider != nil {
		switch {
		case *args.Provider == "":
			args.Provider = nil
		case strings.EqualFold(*args.Provider, "openai"):
			args.Provider = stringValue("openai")
		}
	}
	if args.Model == nil {
		return args
	}
	if *args.Model == "" {
		args.Model = nil
		return args
	}

	modelID := *args.Model
	if slash := strings.IndexByte(modelID, '/'); slash >= 0 && strings.EqualFold(modelID[:slash], "openai") {
		modelID = modelID[slash+1:]
		if args.Provider == nil {
			args.Provider = stringValue("openai")
		}
	}
	if args.Thinking == nil {
		if colon := strings.LastIndexByte(modelID, ':'); colon >= 0 {
			suffix := modelID[colon+1:]
			if _, valid := validThinkingLevels[suffix]; valid {
				modelID = modelID[:colon]
				args.Thinking = stringValue(suffix)
			}
		}
	}
	args.Model = stringValue(modelID)
	return args
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

func apiKeyResolver(args CLIArgs, registry *config.ModelRegistry, credentials aiauth.CredentialStore) agent.GetAPIKeyFunc {
	resolve := requestAuthResolver(args, registry, credentials)
	return func(ctx context.Context, providerID ai.ProviderID) (*string, error) {
		resolved, err := resolve(ctx, providerID)
		if err != nil || resolved == nil {
			return nil, err
		}
		return resolved.APIKey, nil
	}
}

func requestAuthResolver(args CLIArgs, registry *config.ModelRegistry, credentials aiauth.CredentialStore) agent.GetRequestAuthFunc {
	return requestAuthResolverForProvider(args, nil, registry, credentials)
}

func requestAuthResolverForProvider(
	args CLIArgs,
	cliProvider *ai.ProviderID,
	registry *config.ModelRegistry,
	credentials aiauth.CredentialStore,
) agent.GetRequestAuthFunc {
	var baseResolver func(context.Context, ai.ProviderID) (*config.RequestAuth, error)
	if registry != nil {
		baseResolver = registry.DefaultRequestAuthResolver(credentials)
	} else {
		baseResolver = config.FallbackRequestAuthResolver(credentials)
	}
	return func(ctx context.Context, providerID ai.ProviderID) (*agent.RequestAuth, error) {
		if args.APIKey != nil && *args.APIKey != "" && (cliProvider == nil || providerID == *cliProvider) {
			return &agent.RequestAuth{APIKey: args.APIKey}, nil
		}
		resolved, err := baseResolver(ctx, providerID)
		if err != nil || resolved == nil {
			return nil, err
		}
		return &agent.RequestAuth{
			APIKey: resolved.APIKey, Headers: resolved.Headers,
			Env: resolved.Env, BaseURL: resolved.BaseURL,
		}, nil
	}
}

func createBuiltInTools(cwd string, names []string, settings *config.SettingsManager) ([]agent.AgentTool, error) {
	result := make([]agent.AgentTool, 0, len(names))
	for _, name := range names {
		switch name {
		case "read":
			result = append(result, tools.NewReadTool(cwd, nil))
		case "bash":
			shellPath, err := settings.GetShellPath()
			if err != nil {
				return nil, err
			}
			result = append(result, tools.NewBashTool(cwd, &tools.BashToolOptions{
				ShellPath:     shellPath,
				CommandPrefix: settings.GetShellCommandPrefix(),
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
