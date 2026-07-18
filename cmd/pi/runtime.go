package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
)

type runtimeInputs struct {
	Agent       *agent.Agent
	Diagnostics []string
}

func createRuntimeInputs(cwd string, args CLIArgs, priorMessages agent.AgentMessages) (runtimeInputs, error) {
	args = normalizeSkeletonCLIArgs(args)
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return runtimeInputs{}, err
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		return runtimeInputs{}, err
	}
	resources := codingagent.LoadResources(codingagent.ResourceOptions{
		CWD:                cwd,
		AgentDir:           agentDir,
		NoContextFiles:     args.NoContextFiles,
		SystemPrompt:       args.SystemPrompt,
		AppendSystemPrompt: args.AppendSystemPrompt,
	})
	diagnostics := make([]string, 0)
	for _, diagnostic := range settings.DrainErrors() {
		diagnostics = append(diagnostics, diagnostic.Error())
	}
	for _, diagnostic := range resources.Diagnostics {
		diagnostics = append(diagnostics, diagnostic.Message)
	}

	selection := ResolveBuiltInToolSelection(args)
	activeTools, err := createBuiltInTools(cwd, selection, settings)
	if err != nil {
		return runtimeInputs{}, err
	}
	activeNames := make([]string, 0, len(activeTools))
	for _, tool := range activeTools {
		activeNames = append(activeNames, tool.Spec().Name)
	}
	snippets, guidelines := codingagent.BuiltInToolPromptData(activeNames)
	systemPrompt := codingagent.BuildSystemPrompt(codingagent.SystemPromptOptions{
		CustomPrompt:       resources.SystemPrompt,
		SelectedTools:      activeNames,
		ToolSnippets:       snippets,
		PromptGuidelines:   guidelines,
		AppendSystemPrompt: resources.JoinedAppendSystemPrompt(),
		CWD:                cwd,
		ContextFiles:       resources.ContextFiles,
	})

	model, err := resolveSkeletonModel(args, settings)
	if err != nil {
		return runtimeInputs{}, err
	}
	thinking := settings.GetDefaultThinkingLevel()
	if thinking == "" {
		thinking = ai.ModelThinkingMedium
	}
	if args.Thinking != nil {
		thinking = ai.ModelThinkingLevel(*args.Thinking)
	}
	transport := settings.GetTransport()
	state := agent.AgentState{
		SystemPrompt:  systemPrompt,
		Model:         model,
		ThinkingLevel: thinking,
		Tools:         activeTools,
		Messages:      priorMessages,
	}
	created := agent.NewAgent(
		agent.WithInitialState(state),
		agent.WithConvertToLLM(codingagent.ConvertToLLM),
		agent.WithSteeringMode(agent.QueueMode(settings.GetSteeringMode())),
		agent.WithFollowUpMode(agent.QueueMode(settings.GetFollowUpMode())),
		agent.WithSimpleStreamOptions(ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{Transport: &transport}}),
		agent.WithAPIKeyResolver(apiKeyResolver(args)),
	)
	return runtimeInputs{Agent: created, Diagnostics: diagnostics}, nil
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

func apiKeyResolver(args CLIArgs) agent.GetAPIKeyFunc {
	return func(_ context.Context, provider ai.ProviderID) (*string, error) {
		if args.APIKey != nil && *args.APIKey != "" {
			return args.APIKey, nil
		}
		if provider == "openai" {
			if value := os.Getenv("OPENAI_API_KEY"); value != "" {
				return stringValue(value), nil
			}
		}
		return nil, nil
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
