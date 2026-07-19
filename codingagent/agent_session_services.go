package codingagent

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
)

type CreateAgentSessionServicesOptions struct {
	CWD                         string
	AgentDir                    string
	SettingsManager             *config.SettingsManager
	ModelRegistry               *config.ModelRegistry
	ResourceOptions             *ResourceOptions
	ResourceLoaderOptions       *DefaultResourceLoaderOptions
	ResourceLoaderReloadOptions *ResourceLoaderReloadOptions
	ExtensionRegistry           *extensions.Registry
	ExtensionFlagValues         map[string]any
}

type CreateAgentSessionFromServicesOptions struct {
	Services          *AgentSessionServices
	SessionManager    *sessionstore.SessionManager
	SessionStartEvent *extensions.SessionStartEvent
	Model             *ai.Model
	ThinkingLevel     ai.ModelThinkingLevel
	ScopedModels      []ScopedModel
	Tools             []string
	ExcludeTools      []string
	NoTools           string
	CustomTools       []extensions.ToolDefinition
	ToolOptions       *tools.ToolsOptions
}

func CreateAgentSessionServices(options CreateAgentSessionServicesOptions) (*AgentSessionServices, error) {
	cwd := options.CWD
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
	agentDir := options.AgentDir
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
	settings := options.SettingsManager
	if settings == nil {
		settings, err = config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
		if err != nil {
			return nil, err
		}
	}
	modelRegistry := options.ModelRegistry
	if modelRegistry == nil {
		modelRegistry, err = config.NewModelRegistry(agentDir)
		if err != nil {
			return nil, err
		}
	}
	loaderOptions := DefaultResourceLoaderOptions{CWD: cwd, AgentDir: agentDir, SettingsManager: settings}
	if options.ResourceLoaderOptions != nil {
		loaderOptions = *options.ResourceLoaderOptions
		loaderOptions.CWD, loaderOptions.AgentDir, loaderOptions.SettingsManager = cwd, agentDir, settings
	} else if options.ResourceOptions != nil {
		resourceOptions := *options.ResourceOptions
		loaderOptions.NoContextFiles = resourceOptions.NoContextFiles
		loaderOptions.NoSkills = resourceOptions.NoSkills
		loaderOptions.NoPromptTemplates = resourceOptions.NoPromptTemplates
		loaderOptions.SystemPrompt = resourceOptions.SystemPrompt
		loaderOptions.AppendSystemPrompt = resourceOptions.AppendSystemPrompt
		loaderOptions.AdditionalSkillPaths = resourceOptions.SkillPaths
		loaderOptions.AdditionalPromptTemplatePaths = resourceOptions.PromptTemplatePaths
	}
	resourceLoader, err := NewDefaultResourceLoader(loaderOptions)
	if err != nil {
		return nil, err
	}
	if err := resourceLoader.Reload(context.Background(), options.ResourceLoaderReloadOptions); err != nil {
		return nil, err
	}
	resources := resourcesFromLoader(resourceLoader)
	registry := options.ExtensionRegistry
	if registry == nil {
		registry = resourceLoader.GetExtensions()
	}
	diagnostics := resourceRuntimeDiagnostics(resources)
	registry.BindModelRegistry(modelRegistry, func(extensionError extensions.ExtensionError) {
		diagnostics = append(diagnostics, AgentSessionRuntimeDiagnostic{
			Type: "error", Message: fmt.Sprintf("Extension %q error: %s", extensionError.ExtensionPath, extensionError.Error),
		})
	})
	diagnostics = append(diagnostics, applyExtensionFlagValues(registry, options.ExtensionFlagValues)...)
	services := &AgentSessionServices{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings, ModelRegistry: modelRegistry,
		Resources: resources, ResourceLoader: resourceLoader, ExtensionRegistry: registry,
		Diagnostics: diagnostics,
	}
	return services, nil
}

func applyExtensionFlagValues(registry *extensions.Registry, values map[string]any) []AgentSessionRuntimeDiagnostic {
	if len(values) == 0 {
		return nil
	}
	registered := make(map[string]extensions.FlagType)
	for _, flag := range registry.RegisteredFlags() {
		registered[flag.Name] = flag.Type
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var diagnostics []AgentSessionRuntimeDiagnostic
	var unknown []string
	for _, name := range names {
		value := values[name]
		flagType, ok := registered[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		if flagType == extensions.FlagBoolean {
			registry.SetFlagValue(name, true)
			continue
		}
		if stringValue, ok := value.(string); ok {
			registry.SetFlagValue(name, stringValue)
			continue
		}
		diagnostics = append(diagnostics, AgentSessionRuntimeDiagnostic{
			Type: "error", Message: fmt.Sprintf(`Extension flag "--%s" requires a value`, name),
		})
	}
	if len(unknown) > 0 {
		label := "Unknown option"
		if len(unknown) > 1 {
			label += "s"
		}
		for index := range unknown {
			unknown[index] = "--" + unknown[index]
		}
		diagnostics = append(diagnostics, AgentSessionRuntimeDiagnostic{
			Type: "error", Message: label + ": " + strings.Join(unknown, ", "),
		})
	}
	return diagnostics
}

func CreateAgentSessionFromServices(options CreateAgentSessionFromServicesOptions) (*AgentSessionResult, error) {
	services := options.Services
	if services == nil {
		return nil, errMissingAgentSessionServices
	}
	result, err := NewAgentSession(AgentSessionOptions{
		CWD: services.CWD, AgentDir: services.AgentDir, Model: options.Model,
		ThinkingLevel: options.ThinkingLevel, ScopedModels: options.ScopedModels,
		ModelRegistry: services.ModelRegistry, Tools: options.Tools, ExcludeTools: options.ExcludeTools,
		NoTools: options.NoTools, CustomTools: options.CustomTools, ToolOptions: options.ToolOptions,
		SessionManager: options.SessionManager,
		Settings:       services.SettingsManager, Resources: services.Resources,
		ResourceLoader:    services.ResourceLoader,
		ExtensionRegistry: services.ExtensionRegistry, SessionStartEvent: options.SessionStartEvent,
	})
	if err != nil {
		return nil, err
	}
	result.Services = services
	result.Diagnostics = append([]AgentSessionRuntimeDiagnostic(nil), services.Diagnostics...)
	return result, nil
}
