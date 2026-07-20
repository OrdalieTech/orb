package codingagent

import (
	"context"
	"fmt"
	"os"
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
	Themes      []*modetheme.Theme
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
	PackageSkillPaths             []string
	PackagePromptTemplatePaths    []string
	PackageThemePaths             []ResourcePath
	ExtensionFactories            []extensions.Factory
	ExtensionRegistry             *extensions.Registry
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
	resolved ResourceExtensionPaths
	extended ResourceExtensionPaths
	loaded   bool

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
	loaded := loader.loaded
	loader.mu.RUnlock()
	extended := ResourceExtensionPaths{}

	registry := extensions.NewRegistry(options.CWD)
	diagnostics := make([]ResourceDiagnostic, 0)
	if !options.NoExtensions && options.ExtensionRegistry != nil {
		if !loaded {
			// First load adopts the already-materialized instances, mirroring
			// upstream loadFinalExtensionSet reusing pre-trust-loaded extensions
			// (resource-loader.ts:517-560) so factories run once per startup.
			// Later reloads (/reload) re-run every factory against a fresh registry.
			registry = options.ExtensionRegistry
		} else {
			var err error
			registry, err = options.ExtensionRegistry.Fresh(options.CWD)
			if err != nil {
				return err
			}
		}
	} else if !options.NoExtensions {
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
	options.SettingsManager.Reload()
	projectTrusted = options.SettingsManager.IsProjectTrusted()
	resolved, err := resolveResourceLoaderPaths(options)
	if err != nil {
		return err
	}

	commandOptions := resourceLoaderCommandOptions(options, resolved, extended, projectTrusted)
	resources := LoadResources(ResourceOptions{
		CWD: options.CWD, AgentDir: options.AgentDir, ProjectTrusted: &projectTrusted,
		NoContextFiles: options.NoContextFiles, NoSkills: true,
		NoPromptTemplates: true, SystemPrompt: options.SystemPrompt,
		AppendSystemPrompt: options.AppendSystemPrompt, SkillPaths: commandOptions.skillPaths,
		PromptTemplatePaths: commandOptions.promptPaths,
		SkillPathMetadata:   commandOptions.metadata.skills,
		PromptPathMetadata:  commandOptions.metadata.prompts,
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
	themes := loadResourceThemes(options, resolved, extended)

	loader.mu.Lock()
	loader.resolved = resolved
	loader.extended = extended
	loader.loaded = true
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
		Themes:      append([]*modetheme.Theme(nil), loader.themes.Themes...),
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
	resolved := cloneResourceExtensionPaths(loader.resolved)
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
	commandOptions := resourceLoaderCommandOptions(options, resolved, extended, projectTrusted)
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
		themes := loadResourceThemes(options, resolved, extended)
		loader.mu.Lock()
		loader.themes = themes
		loader.mu.Unlock()
	}
}

func loadResourceThemes(options DefaultResourceLoaderOptions, resolved, extended ResourceExtensionPaths) ResourceThemesResult {
	paths, _ := resourceLoaderPaths(options.CWD, resolved.ThemePaths, options.AdditionalThemePaths, extended.ThemePaths)
	registry := modetheme.Load(modetheme.LoadOptions{
		CWD: options.CWD, AgentDir: options.AgentDir, NoThemes: true, AdditionalPaths: paths,
	})
	result := ResourceThemesResult{Themes: []*modetheme.Theme{}, Diagnostics: []ResourceDiagnostic{}}
	for _, theme := range registry.Loaded() {
		if theme == nil || theme.SourcePath == "" {
			continue
		}
		result.Themes = append(result.Themes, theme)
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
	for _, theme := range result.Themes {
		if theme == nil || theme.SourcePath == "" {
			continue
		}
		if sourceInfo := resourceThemeMetadataSourceInfo(theme.SourcePath, resolved, extended); sourceInfo != nil {
			theme.SourceInfo = sourceInfo
		} else if theme.SourceInfo == nil {
			theme.SourceInfo = defaultThemeSourceInfo(theme.SourcePath, options)
		}
	}
	return result
}

func resourceThemeMetadataSourceInfo(path string, resolved, extended ResourceExtensionPaths) *extensions.SourceInfo {
	for _, entry := range extended.ThemePaths {
		if pathIsWithin(path, entry.Path) {
			return themeSourceInfoFromMetadata(path, entry.Metadata)
		}
	}
	for _, entry := range resolved.ThemePaths {
		if pathIsWithin(path, entry.Path) {
			return themeSourceInfoFromMetadata(path, entry.Metadata)
		}
	}
	return nil
}

func themeSourceInfoFromMetadata(path string, metadata PathMetadata) *extensions.SourceInfo {
	var baseDir *string
	if metadata.BaseDir != "" {
		value := metadata.BaseDir
		baseDir = &value
	}
	return &extensions.SourceInfo{
		Path: path, Source: metadata.Source, Scope: extensions.SourceScope(metadata.Scope),
		Origin: extensions.SourceOrigin(metadata.Origin), BaseDir: baseDir,
	}
}

func defaultThemeSourceInfo(path string, options DefaultResourceLoaderOptions) *extensions.SourceInfo {
	baseDir := filepath.Dir(path)
	scope := extensions.SourceScopeTemporary
	for _, candidate := range []struct {
		root  string
		scope extensions.SourceScope
	}{{filepath.Join(options.AgentDir, "themes"), extensions.SourceScopeUser}, {filepath.Join(options.CWD, ".pi", "themes"), extensions.SourceScopeProject}} {
		if pathIsWithin(path, candidate.root) {
			baseDir, scope = candidate.root, candidate.scope
			break
		}
	}
	return &extensions.SourceInfo{
		Path: path, Source: "local", Scope: scope, Origin: extensions.SourceOriginTopLevel, BaseDir: &baseDir,
	}
}

func resolveResourceLoaderPaths(options DefaultResourceLoaderOptions) (ResourceExtensionPaths, error) {
	resolved, err := NewPackageManager(PackageManagerOptions{
		CWD: options.CWD, AgentDir: options.AgentDir, Settings: options.SettingsManager,
	}).Resolve(nil)
	if err != nil {
		return ResourceExtensionPaths{}, err
	}
	paths := ResourceExtensionPaths{}
	if !options.NoSkills {
		paths.SkillPaths = enabledResourcePaths(resolved.Skills, true)
		packageMetadata := PathMetadata{Source: "package", Scope: "temporary", Origin: "package", BaseDir: options.CWD}
		for _, path := range options.PackageSkillPaths {
			paths.SkillPaths = appendUniqueResourcePath(paths.SkillPaths, mapSkillResourcePath(ResourcePath{Path: path, Metadata: packageMetadata}))
		}
	}
	if !options.NoPromptTemplates {
		paths.PromptPaths = enabledResourcePaths(resolved.Prompts, false)
		packageMetadata := PathMetadata{Source: "package", Scope: "temporary", Origin: "package", BaseDir: options.CWD}
		for _, path := range options.PackagePromptTemplatePaths {
			paths.PromptPaths = appendUniqueResourcePath(paths.PromptPaths, ResourcePath{Path: path, Metadata: packageMetadata})
		}
	}
	if !options.NoThemes {
		paths.ThemePaths = enabledResourcePaths(resolved.Themes, false)
		for _, entry := range options.PackageThemePaths {
			paths.ThemePaths = appendUniqueResourcePath(paths.ThemePaths, entry)
		}
	}
	paths.SkillPaths = normalizeResourcePathEntries(paths.SkillPaths, options.CWD)
	paths.PromptPaths = normalizeResourcePathEntries(paths.PromptPaths, options.CWD)
	paths.ThemePaths = normalizeResourcePathEntries(paths.ThemePaths, options.CWD)
	return paths, nil
}

func enabledResourcePaths(resources []ResolvedResource, skills bool) []ResourcePath {
	paths := make([]ResourcePath, 0, len(resources))
	for _, resource := range resources {
		if !resource.Enabled {
			continue
		}
		entry := ResourcePath{Path: resource.Path, Metadata: resource.Metadata}
		if skills {
			entry = mapSkillResourcePath(entry)
		}
		paths = appendUniqueResourcePath(paths, entry)
	}
	return paths
}

func mapSkillResourcePath(resource ResourcePath) ResourcePath {
	if resource.Metadata.Source != "auto" && resource.Metadata.Origin != "package" {
		return resource
	}
	info, err := os.Stat(resource.Path)
	if err != nil || !info.IsDir() {
		return resource
	}
	skillFile := filepath.Join(resource.Path, "SKILL.md")
	if pathExists(skillFile) {
		resource.Path = skillFile
	}
	return resource
}

func appendUniqueResourcePath(paths []ResourcePath, entry ResourcePath) []ResourcePath {
	canonical := canonicalResourcePath(entry.Path)
	for _, existing := range paths {
		if canonicalResourcePath(existing.Path) == canonical {
			return paths
		}
	}
	return append(paths, entry)
}

func resourceLoaderPaths(cwd string, resolved []ResourcePath, additional []string, extended []ResourcePath) ([]string, map[string]PathMetadata) {
	paths := make([]string, 0, len(resolved)+len(additional)+len(extended))
	metadata := make(map[string]PathMetadata, len(resolved)+len(extended))
	seen := make(map[string]struct{}, cap(paths))
	appendPath := func(path string) {
		path = resolveResourcePathFrom(path, cwd)
		canonical := canonicalResourcePath(path)
		if _, exists := seen[canonical]; exists {
			return
		}
		seen[canonical] = struct{}{}
		paths = append(paths, path)
	}
	for _, entry := range resolved {
		appendPath(entry.Path)
		metadata[canonicalResourcePath(entry.Path)] = entry.Metadata
	}
	for _, path := range additional {
		appendPath(path)
	}
	for _, entry := range extended {
		appendPath(entry.Path)
		metadata[canonicalResourcePath(entry.Path)] = entry.Metadata
	}
	return paths, metadata
}

func resourceLoaderCommandOptions(options DefaultResourceLoaderOptions, resolved, extended ResourceExtensionPaths, projectTrusted bool) commandResourceOptions {
	skillPaths, skillMetadata := resourceLoaderPaths(options.CWD, resolved.SkillPaths, options.AdditionalSkillPaths, extended.SkillPaths)
	promptPaths, promptMetadata := resourceLoaderPaths(options.CWD, resolved.PromptPaths, options.AdditionalPromptTemplatePaths, extended.PromptPaths)
	return commandResourceOptions{
		cwd: options.CWD, agentDir: options.AgentDir, trusted: projectTrusted,
		noSkills: true, noPrompts: true,
		skillPaths: skillPaths, promptPaths: promptPaths,
		metadata: commandResourceMetadata{skills: skillMetadata, prompts: promptMetadata},
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
