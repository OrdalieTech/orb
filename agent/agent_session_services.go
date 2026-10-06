package agent

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/host"
)

type CreateAgentSessionServicesOptions struct {
	CWD      string
	AgentDir string
	// Host supplies the platform ports for services left nil (DECISIONS.md P10).
	Host                        *host.Host
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
	cwd, agentDir, err := resolveSessionDirs(options.Host, options.CWD, options.AgentDir)
	if err != nil {
		return nil, err
	}
	settings := options.SettingsManager
	if settings == nil {
		if settings, err = newSettings(options.Host, cwd, agentDir); err != nil {
			return nil, err
		}
	}
	modelRegistry := options.ModelRegistry
	if modelRegistry == nil {
		if modelRegistry, err = newModelRegistry(options.Host, agentDir); err != nil {
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
	flags := make([]ExtensionFlag, 0, len(options.ExtensionFlagValues))
	for _, name := range slices.Sorted(maps.Keys(options.ExtensionFlagValues)) {
		flag := ExtensionFlag{Name: name}
		if value, ok := options.ExtensionFlagValues[name].(string); ok {
			flag.Value = &value
		}
		flags = append(flags, flag)
	}
	for _, message := range ApplyExtensionFlags(registry, flags) {
		diagnostics = append(diagnostics, AgentSessionRuntimeDiagnostic{Type: "error", Message: message})
	}
	services := &AgentSessionServices{
		CWD: cwd, AgentDir: agentDir, SettingsManager: settings, ModelRegistry: modelRegistry,
		Resources: resources, ResourceLoader: resourceLoader, ExtensionRegistry: registry,
		Diagnostics: diagnostics,
	}
	return services, nil
}

// ExtensionFlag is an extension flag given on a command line; Value is nil
// for a bare --name.
type ExtensionFlag struct {
	Name  string
	Value *string
}

// ApplyExtensionFlags sets registry's extension flags from flags, in order,
// and returns a message for each flag it could not set.
func ApplyExtensionFlags(registry *extensions.Registry, flags []ExtensionFlag) []string {
	registered := make(map[string]extensions.Flag)
	if registry != nil {
		for _, flag := range registry.RegisteredFlags() {
			if _, exists := registered[flag.Name]; !exists {
				registered[flag.Name] = flag
			}
		}
	}
	var unknown, messages []string
	for _, supplied := range flags {
		flag, exists := registered[supplied.Name]
		switch {
		case !exists:
			unknown = append(unknown, supplied.Name)
		case flag.Type == extensions.FlagBoolean:
			registry.SetFlagValue(supplied.Name, true)
		case supplied.Value != nil:
			registry.SetFlagValue(supplied.Name, *supplied.Value)
		default:
			messages = append(messages, fmt.Sprintf("Extension flag \"--%s\" requires a value", supplied.Name))
		}
	}
	if len(unknown) > 0 {
		option := "option"
		if len(unknown) > 1 {
			option = "options"
		}
		messages = append(messages, "Unknown "+option+": --"+strings.Join(unknown, ", --"))
	}
	return messages
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
