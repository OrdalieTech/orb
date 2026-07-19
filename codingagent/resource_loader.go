package codingagent

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	modetheme "github.com/OrdalieTech/pi-go/codingagent/modes/theme"
)

type ResourceSkillsResult struct {
	Skills      []Skill
	Diagnostics []ResourceDiagnostic
}

type ResourcePromptsResult struct {
	Prompts     []PromptTemplate
	Diagnostics []ResourceDiagnostic
}

type ResourceThemesResult struct {
	Themes      []extensions.ThemeInfo
	Diagnostics []ResourceDiagnostic
}

type ResourceAgentsFilesResult struct {
	AgentsFiles []ContextFile
}

type ResourcePath struct {
	Path     string
	Metadata PathMetadata
}

type ResourceExtensionPaths struct {
	SkillPaths  []ResourcePath
	PromptPaths []ResourcePath
	ThemePaths  []ResourcePath
}

type ResourceLoaderReloadOptions struct {
	ResolveProjectTrust func(context.Context, *extensions.Registry) (bool, error)
}

// ResourceLoader is the replaceable SDK resource seam used by AgentSession.
// Its method set mirrors upstream's ResourceLoader while keeping cancellation
// explicit for reloads.
type ResourceLoader interface {
	GetExtensions() *extensions.Registry
	GetSkills() ResourceSkillsResult
	GetPrompts() ResourcePromptsResult
	GetThemes() ResourceThemesResult
	GetAgentsFiles() ResourceAgentsFilesResult
	GetSystemPrompt() *string
	GetAppendSystemPrompt() []string
	ExtendResources(ResourceExtensionPaths)
	Reload(context.Context, *ResourceLoaderReloadOptions) error
}

type DefaultResourceLoaderOptions struct {
	CWD             string
	AgentDir        string
	SettingsManager *config.SettingsManager

	AdditionalSkillPaths          []string
	AdditionalPromptTemplatePaths []string
	AdditionalThemePaths          []string
	ExtensionFactories            []extensions.Factory
	NoExtensions                  bool
	NoSkills                      bool
	NoPromptTemplates             bool
	NoThemes                      bool
	NoContextFiles                bool
	SystemPrompt                  *string
	AppendSystemPrompt            []string

	SkillsOverride             func(ResourceSkillsResult) ResourceSkillsResult
	PromptsOverride            func(ResourcePromptsResult) ResourcePromptsResult
	ThemesOverride             func(ResourceThemesResult) ResourceThemesResult
	AgentsFilesOverride        func(ResourceAgentsFilesResult) ResourceAgentsFilesResult
	SystemPromptOverride       func(*string) *string
	AppendSystemPromptOverride func([]string) []string
}

type DefaultResourceLoader struct {
	mu       sync.RWMutex
	options  DefaultResourceLoaderOptions
	extended ResourceExtensionPaths

	registry  *extensions.Registry
	resources Resources
	themes    ResourceThemesResult

	skillDiagnostics  []ResourceDiagnostic
	promptDiagnostics []ResourceDiagnostic
}

func NewDefaultResourceLoader(options DefaultResourceLoaderOptions) (*DefaultResourceLoader, error) {
	cwd := options.CWD
	if cwd == "" {
		cwd = "."
	}
	resolved, err := config.NormalizePath(cwd)
	if err != nil {
		return nil, err
	}
	options.CWD, err = filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	if options.AgentDir == "" {
		options.AgentDir = DefaultAgentDir()
	}
	resolved, err = config.NormalizePath(options.AgentDir)
	if err != nil {
		return nil, err
	}
	options.AgentDir, err = filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	if options.SettingsManager == nil {
		options.SettingsManager, err = config.NewSettingsManager(options.CWD, config.WithAgentDir(options.AgentDir))
		if err != nil {
			return nil, err
		}
	}
	return &DefaultResourceLoader{options: options}, nil
}

func (loader *DefaultResourceLoader) Reload(ctx context.Context, reloadOptions *ResourceLoaderReloadOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	loader.mu.RLock()
	options := loader.options
	extended := cloneResourceExtensionPaths(loader.extended)
	loader.mu.RUnlock()

	registry := extensions.NewRegistry(options.CWD)
	diagnostics := make([]ResourceDiagnostic, 0)
	if !options.NoExtensions {
		for index, factory := range options.ExtensionFactories {
			path := fmt.Sprintf("<inline:sdk-%d>", index+1)
			if err := registry.Register(path, factory); err != nil {
				diagnostics = append(diagnostics, ResourceDiagnostic{Type: "error", Message: err.Error(), Path: path})
			}
		}
	}

	projectTrusted := options.SettingsManager.IsProjectTrusted()
	if reloadOptions != nil && reloadOptions.ResolveProjectTrust != nil {
		var err error
		projectTrusted, err = reloadOptions.ResolveProjectTrust(ctx, registry)
		if err != nil {
			return err
		}
		options.SettingsManager.SetProjectTrusted(projectTrusted)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	skillPaths := append([]string(nil), options.AdditionalSkillPaths...)
	skillMetadata := make(map[string]PathMetadata, len(extended.SkillPaths))
	for _, path := range extended.SkillPaths {
		skillPaths = append(skillPaths, path.Path)
		skillMetadata[path.Path] = path.Metadata
	}
	promptPaths := append([]string(nil), options.AdditionalPromptTemplatePaths...)
	promptMetadata := make(map[string]PathMetadata, len(extended.PromptPaths))
	for _, path := range extended.PromptPaths {
		promptPaths = append(promptPaths, path.Path)
		promptMetadata[path.Path] = path.Metadata
	}
	resources := LoadResources(ResourceOptions{
		CWD: options.CWD, AgentDir: options.AgentDir, ProjectTrusted: &projectTrusted,
		NoContextFiles: options.NoContextFiles, NoSkills: options.NoSkills,
		NoPromptTemplates: options.NoPromptTemplates, SystemPrompt: options.SystemPrompt,
		AppendSystemPrompt: options.AppendSystemPrompt, SkillPaths: skillPaths,
		PromptTemplatePaths:        promptPaths,
		GlobalSkillPaths:           options.SettingsManager.GetGlobalSkillPaths(),
		ProjectSkillPaths:          options.SettingsManager.GetProjectSkillPaths(),
		GlobalPromptTemplatePaths:  options.SettingsManager.GetGlobalPromptTemplatePaths(),
		ProjectPromptTemplatePaths: options.SettingsManager.GetProjectPromptTemplatePaths(),
		SkillPathMetadata:          skillMetadata,
		PromptPathMetadata:         promptMetadata,
	})
	skillDiagnostics := append([]ResourceDiagnostic(nil), resources.skillDiagnostics...)
	promptDiagnostics := append([]ResourceDiagnostic(nil), resources.promptDiagnostics...)
	if options.SkillsOverride != nil {
		result := options.SkillsOverride(ResourceSkillsResult{
			Skills: append([]Skill(nil), resources.Skills...), Diagnostics: append([]ResourceDiagnostic(nil), skillDiagnostics...),
		})
		resources.Skills = append([]Skill(nil), result.Skills...)
		skillDiagnostics = append([]ResourceDiagnostic(nil), result.Diagnostics...)
	}
	if options.PromptsOverride != nil {
		result := options.PromptsOverride(ResourcePromptsResult{
			Prompts: append([]PromptTemplate(nil), resources.PromptTemplates...), Diagnostics: append([]ResourceDiagnostic(nil), promptDiagnostics...),
		})
		resources.PromptTemplates = append([]PromptTemplate(nil), result.Prompts...)
		promptDiagnostics = append([]ResourceDiagnostic(nil), result.Diagnostics...)
	}
	resources.Diagnostics = append(diagnostics, resources.Diagnostics...)
	if options.AgentsFilesOverride != nil {
		result := options.AgentsFilesOverride(ResourceAgentsFilesResult{AgentsFiles: append([]ContextFile(nil), resources.ContextFiles...)})
		resources.ContextFiles = append([]ContextFile(nil), result.AgentsFiles...)
	}
	if options.SystemPromptOverride != nil {
		resources.SystemPrompt = options.SystemPromptOverride(cloneStringPointer(resources.SystemPrompt))
	}
	if options.AppendSystemPromptOverride != nil {
		resources.AppendSystemPrompt = append([]string(nil), options.AppendSystemPromptOverride(append([]string(nil), resources.AppendSystemPrompt...))...)
	}
	themes := loadResourceThemes(options, extended, projectTrusted)

	loader.mu.Lock()
	loader.registry = registry
	loader.resources = resources
	loader.themes = themes
	loader.skillDiagnostics = skillDiagnostics
	loader.promptDiagnostics = promptDiagnostics
	loader.mu.Unlock()
	return nil
}

func (loader *DefaultResourceLoader) GetExtensions() *extensions.Registry {
	loader.mu.RLock()
	defer loader.mu.RUnlock()
	return loader.registry
}

func (loader *DefaultResourceLoader) GetSkills() ResourceSkillsResult {
	loader.mu.RLock()
	defer loader.mu.RUnlock()
	return ResourceSkillsResult{
		Skills:      append([]Skill(nil), loader.resources.Skills...),
		Diagnostics: append([]ResourceDiagnostic(nil), loader.skillDiagnostics...),
	}
}

func (loader *DefaultResourceLoader) GetPrompts() ResourcePromptsResult {
	loader.mu.RLock()
	defer loader.mu.RUnlock()
	return ResourcePromptsResult{
		Prompts:     append([]PromptTemplate(nil), loader.resources.PromptTemplates...),
		Diagnostics: append([]ResourceDiagnostic(nil), loader.promptDiagnostics...),
	}
}

func (loader *DefaultResourceLoader) GetThemes() ResourceThemesResult {
	loader.mu.RLock()
	defer loader.mu.RUnlock()
	return ResourceThemesResult{
		Themes:      append([]extensions.ThemeInfo(nil), loader.themes.Themes...),
		Diagnostics: append([]ResourceDiagnostic(nil), loader.themes.Diagnostics...),
	}
}

func (loader *DefaultResourceLoader) GetAgentsFiles() ResourceAgentsFilesResult {
	loader.mu.RLock()
	defer loader.mu.RUnlock()
	return ResourceAgentsFilesResult{AgentsFiles: append([]ContextFile(nil), loader.resources.ContextFiles...)}
}

func (loader *DefaultResourceLoader) GetSystemPrompt() *string {
	loader.mu.RLock()
	defer loader.mu.RUnlock()
	return cloneStringPointer(loader.resources.SystemPrompt)
}

func (loader *DefaultResourceLoader) GetAppendSystemPrompt() []string {
	loader.mu.RLock()
	defer loader.mu.RUnlock()
	return append([]string(nil), loader.resources.AppendSystemPrompt...)
}

func (loader *DefaultResourceLoader) ExtendResources(paths ResourceExtensionPaths) {
	loader.mu.RLock()
	options := loader.options
	loader.mu.RUnlock()
	paths.SkillPaths = normalizeResourcePathEntries(paths.SkillPaths, options.CWD)
	paths.PromptPaths = normalizeResourcePathEntries(paths.PromptPaths, options.CWD)
	paths.ThemePaths = normalizeResourcePathEntries(paths.ThemePaths, options.CWD)

	loader.mu.Lock()
	loader.extended.SkillPaths = mergeResourcePathEntries(loader.extended.SkillPaths, paths.SkillPaths)
	loader.extended.PromptPaths = mergeResourcePathEntries(loader.extended.PromptPaths, paths.PromptPaths)
	loader.extended.ThemePaths = mergeResourcePathEntries(loader.extended.ThemePaths, paths.ThemePaths)
	extended := cloneResourceExtensionPaths(loader.extended)
	loader.mu.Unlock()

	projectTrusted := options.SettingsManager.IsProjectTrusted()
	commandOptions := resourceLoaderCommandOptions(options, extended, projectTrusted)
	if len(paths.SkillPaths) > 0 {
		result := loadCommandSkills(commandOptions)
		resolved := ResourceSkillsResult(result)
		if options.SkillsOverride != nil {
			resolved = options.SkillsOverride(resolved)
		}
		loader.mu.Lock()
		loader.resources.Skills = append([]Skill(nil), resolved.Skills...)
		loader.skillDiagnostics = append([]ResourceDiagnostic(nil), resolved.Diagnostics...)
		loader.mu.Unlock()
	}
	if len(paths.PromptPaths) > 0 {
		prompts, diagnostics := loadCommandPrompts(commandOptions)
		resolved := ResourcePromptsResult{Prompts: prompts, Diagnostics: diagnostics}
		if options.PromptsOverride != nil {
			resolved = options.PromptsOverride(resolved)
		}
		loader.mu.Lock()
		loader.resources.PromptTemplates = append([]PromptTemplate(nil), resolved.Prompts...)
		loader.promptDiagnostics = append([]ResourceDiagnostic(nil), resolved.Diagnostics...)
		loader.mu.Unlock()
	}
	if len(paths.ThemePaths) > 0 {
		themes := loadResourceThemes(options, extended, projectTrusted)
		loader.mu.Lock()
		loader.themes = themes
		loader.mu.Unlock()
	}
}

func loadResourceThemes(options DefaultResourceLoaderOptions, extended ResourceExtensionPaths, projectTrusted bool) ResourceThemesResult {
	extensionPaths := make([]string, 0, len(extended.ThemePaths))
	for _, entry := range extended.ThemePaths {
		extensionPaths = append(extensionPaths, entry.Path)
	}
	registry := modetheme.Load(modetheme.LoadOptions{
		CWD: options.CWD, AgentDir: options.AgentDir, ProjectTrusted: projectTrusted, NoThemes: options.NoThemes,
		GlobalPaths: options.SettingsManager.GetGlobalThemePaths(), ProjectPaths: options.SettingsManager.GetProjectThemePaths(),
		AdditionalPaths: options.AdditionalThemePaths, ResourceDiscoverPath: extensionPaths,
	})
	result := ResourceThemesResult{Themes: []extensions.ThemeInfo{}, Diagnostics: []ResourceDiagnostic{}}
	for _, name := range registry.Available() {
		theme, found := registry.Get(name)
		if !found || theme == nil || theme.SourcePath == "" {
			continue
		}
		path := theme.SourcePath
		result.Themes = append(result.Themes, extensions.ThemeInfo{Name: name, Path: &path})
	}
	for _, diagnostic := range registry.Diagnostics() {
		converted := ResourceDiagnostic{Type: diagnostic.Type, Message: diagnostic.Message, Path: diagnostic.Path}
		if diagnostic.Collision != nil {
			converted.Collision = &ResourceCollision{
				ResourceType: diagnostic.Collision.ResourceType, Name: diagnostic.Collision.Name,
				WinnerPath: diagnostic.Collision.WinnerPath, LoserPath: diagnostic.Collision.LoserPath,
			}
		}
		result.Diagnostics = append(result.Diagnostics, converted)
	}
	if options.ThemesOverride != nil {
		result = options.ThemesOverride(result)
	}
	return result
}

func resourceLoaderCommandOptions(options DefaultResourceLoaderOptions, extended ResourceExtensionPaths, projectTrusted bool) commandResourceOptions {
	metadata := resolveCommandResourceMetadata(options.CWD, options.AgentDir, projectTrusted)
	skillPaths := append([]string(nil), options.AdditionalSkillPaths...)
	for _, entry := range extended.SkillPaths {
		skillPaths = append(skillPaths, entry.Path)
		metadata.skills[canonicalResourcePath(entry.Path)] = entry.Metadata
	}
	promptPaths := append([]string(nil), options.AdditionalPromptTemplatePaths...)
	for _, entry := range extended.PromptPaths {
		promptPaths = append(promptPaths, entry.Path)
		metadata.prompts[canonicalResourcePath(entry.Path)] = entry.Metadata
	}
	return commandResourceOptions{
		cwd: options.CWD, agentDir: options.AgentDir, trusted: projectTrusted,
		noSkills: options.NoSkills, noPrompts: options.NoPromptTemplates,
		skillPaths: skillPaths, promptPaths: promptPaths,
		globalSkillPaths:   options.SettingsManager.GetGlobalSkillPaths(),
		projectSkillPaths:  options.SettingsManager.GetProjectSkillPaths(),
		globalPromptPaths:  options.SettingsManager.GetGlobalPromptTemplatePaths(),
		projectPromptPaths: options.SettingsManager.GetProjectPromptTemplatePaths(),
		metadata:           metadata,
	}
}

func normalizeResourcePathEntries(entries []ResourcePath, cwd string) []ResourcePath {
	resolved := make([]ResourcePath, 0, len(entries))
	for _, entry := range entries {
		entry.Path = resolveResourcePathFrom(entry.Path, cwd)
		if entry.Metadata.BaseDir != "" {
			entry.Metadata.BaseDir = resolveResourcePathFrom(entry.Metadata.BaseDir, cwd)
		}
		resolved = append(resolved, entry)
	}
	return resolved
}

func mergeResourcePathEntries(primary, additional []ResourcePath) []ResourcePath {
	merged := append([]ResourcePath(nil), primary...)
	indices := make(map[string]int, len(merged)+len(additional))
	for index, entry := range merged {
		indices[canonicalResourcePath(entry.Path)] = index
	}
	for _, entry := range additional {
		canonical := canonicalResourcePath(entry.Path)
		if index, exists := indices[canonical]; exists {
			merged[index] = entry
			continue
		}
		indices[canonical] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

func resourcesFromLoader(loader ResourceLoader) *Resources {
	if loader == nil {
		return nil
	}
	skills := loader.GetSkills()
	prompts := loader.GetPrompts()
	resources := &Resources{
		ContextFiles: loader.GetAgentsFiles().AgentsFiles,
		SystemPrompt: loader.GetSystemPrompt(), AppendSystemPrompt: loader.GetAppendSystemPrompt(),
		Skills: skills.Skills, PromptTemplates: prompts.Prompts,
	}
	resources.Diagnostics = append(resources.Diagnostics, skills.Diagnostics...)
	resources.Diagnostics = append(resources.Diagnostics, prompts.Diagnostics...)
	return resources
}

func cloneResourceExtensionPaths(paths ResourceExtensionPaths) ResourceExtensionPaths {
	return ResourceExtensionPaths{
		SkillPaths:  append([]ResourcePath(nil), paths.SkillPaths...),
		PromptPaths: append([]ResourcePath(nil), paths.PromptPaths...),
		ThemePaths:  append([]ResourcePath(nil), paths.ThemePaths...),
	}
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

var _ ResourceLoader = (*DefaultResourceLoader)(nil)
