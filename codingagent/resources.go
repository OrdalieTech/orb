package codingagent

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	textunicode "golang.org/x/text/encoding/unicode"
)

var contextFileCandidates = []string{"AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"}

// ContextFile is inserted verbatim into the project_context prompt block.
type ContextFile struct {
	Path    string
	Content string
}

type ResourceDiagnostic struct {
	Type      string
	Message   string
	Path      string
	Collision *ResourceCollision
}

type ResourceOptions struct {
	CWD               string
	AgentDir          string
	ProjectTrusted    *bool
	NoContextFiles    bool
	NoSkills          bool
	NoPromptTemplates bool
	SystemPrompt      *string
	// Nil means discover APPEND_SYSTEM.md; a non-nil empty slice disables discovery.
	AppendSystemPrompt         []string
	SkillPaths                 []string
	PromptTemplatePaths        []string
	GlobalSkillPaths           []string
	ProjectSkillPaths          []string
	GlobalPromptTemplatePaths  []string
	ProjectPromptTemplatePaths []string
	PackageSkillPaths          []string
	PackagePromptTemplatePaths []string
}

type Resources struct {
	ContextFiles       []ContextFile
	SystemPrompt       *string
	AppendSystemPrompt []string
	Skills             []Skill
	PromptTemplates    []PromptTemplate
	Diagnostics        []ResourceDiagnostic
}

// JoinedAppendSystemPrompt applies the separator used before prompt assembly.
func (resources Resources) JoinedAppendSystemPrompt() *string {
	if len(resources.AppendSystemPrompt) == 0 {
		return nil
	}
	joined := strings.Join(resources.AppendSystemPrompt, "\n\n")
	return &joined
}

// DefaultAgentDir returns the upstream global resource directory.
func DefaultAgentDir() string {
	if configured := os.Getenv("PI_CODING_AGENT_DIR"); configured != "" {
		return normalizeResourcePath(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".pi", "agent")
	}
	return filepath.Join(home, ".pi", "agent")
}

// LoadResources discovers context and prompt files, then applies CLI overrides.
func LoadResources(options ResourceOptions) Resources {
	cwd := resolveResourcePath(options.CWD)
	agentDir := options.AgentDir
	if agentDir == "" {
		agentDir = DefaultAgentDir()
	}
	agentDir = resolveResourcePath(agentDir)
	trusted := true
	if options.ProjectTrusted != nil {
		trusted = *options.ProjectTrusted
	}

	resources := Resources{}
	if !options.NoContextFiles {
		resources.ContextFiles, resources.Diagnostics = LoadProjectContextFiles(cwd, agentDir)
	} else {
		resources.ContextFiles = []ContextFile{}
	}

	var systemSource *string
	if options.SystemPrompt != nil {
		systemSource = options.SystemPrompt
	} else if discovered := discoverPromptFile(cwd, agentDir, trusted, "SYSTEM.md"); discovered != "" {
		systemSource = &discovered
	}
	if systemSource != nil {
		resolved, diagnostic := resolvePromptInput(*systemSource, "system prompt")
		resources.SystemPrompt = resolved
		if diagnostic != nil {
			resources.Diagnostics = append(resources.Diagnostics, *diagnostic)
		}
	}

	appendSources := options.AppendSystemPrompt
	if appendSources == nil {
		if discovered := discoverPromptFile(cwd, agentDir, trusted, "APPEND_SYSTEM.md"); discovered != "" {
			appendSources = []string{discovered}
		} else {
			appendSources = []string{}
		}
	}
	resources.AppendSystemPrompt = make([]string, 0, len(appendSources))
	for _, source := range appendSources {
		resolved, diagnostic := resolvePromptInput(source, "append system prompt")
		if diagnostic != nil {
			resources.Diagnostics = append(resources.Diagnostics, *diagnostic)
		}
		if resolved != nil {
			resources.AppendSystemPrompt = append(resources.AppendSystemPrompt, *resolved)
		}
	}
	var fileDiagnostics []ResourceDiagnostic
	resources.Skills, resources.PromptTemplates, fileDiagnostics = loadCommandResources(commandResourceOptions{
		cwd: cwd, agentDir: agentDir, trusted: trusted,
		noSkills: options.NoSkills, noPrompts: options.NoPromptTemplates,
		skillPaths: options.SkillPaths, promptPaths: options.PromptTemplatePaths,
		globalSkillPaths: options.GlobalSkillPaths, projectSkillPaths: options.ProjectSkillPaths,
		globalPromptPaths: options.GlobalPromptTemplatePaths, projectPromptPaths: options.ProjectPromptTemplatePaths,
		packageSkillPaths:  options.PackageSkillPaths,
		packagePromptPaths: options.PackagePromptTemplatePaths,
	})
	resources.Diagnostics = append(resources.Diagnostics, fileDiagnostics...)
	return resources
}

type commandResourceOptions struct {
	cwd, agentDir                         string
	trusted                               bool
	noSkills, noPrompts                   bool
	skillPaths, promptPaths               []string
	globalSkillPaths, projectSkillPaths   []string
	globalPromptPaths, projectPromptPaths []string
	packageSkillPaths, packagePromptPaths []string
}

func retagSkills(result LoadSkillsResult, scope, baseDir, source string) LoadSkillsResult {
	for index := range result.Skills {
		result.Skills[index].SourceInfo = SourceInfo{
			Path: result.Skills[index].FilePath, Source: source, Scope: scope,
			Origin: "top-level", BaseDir: baseDir,
		}
	}
	return result
}

func combineSkills(inputs []LoadSkillsResult) LoadSkillsResult {
	combined := LoadSkillsResult{Skills: []Skill{}, Diagnostics: []ResourceDiagnostic{}}
	seenNames := make(map[string]Skill)
	seenPaths := make(map[string]struct{})
	for _, input := range inputs {
		combined.Diagnostics = append(combined.Diagnostics, input.Diagnostics...)
		for _, skill := range input.Skills {
			canonical := canonicalResourcePath(skill.FilePath)
			if _, duplicate := seenPaths[canonical]; duplicate {
				continue
			}
			if winner, collision := seenNames[skill.Name]; collision {
				combined.Diagnostics = append(combined.Diagnostics, ResourceDiagnostic{
					Type: "collision", Message: fmt.Sprintf("name %q collision", skill.Name), Path: skill.FilePath,
					Collision: &ResourceCollision{ResourceType: "skill", Name: skill.Name, WinnerPath: winner.FilePath, LoserPath: skill.FilePath},
				})
				continue
			}
			seenNames[skill.Name] = skill
			seenPaths[canonical] = struct{}{}
			combined.Skills = append(combined.Skills, skill)
		}
	}
	return combined
}

func findGitResourceRoot(start string) string {
	for current := resolveResourcePath(start); ; current = filepath.Dir(current) {
		if pathExists(filepath.Join(current, ".git")) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
	}
}

func ancestorAgentsSkillDirs(cwd string) []string {
	root := findGitResourceRoot(cwd)
	directories := make([]string, 0)
	for current := resolveResourcePath(cwd); ; current = filepath.Dir(current) {
		directories = append(directories, filepath.Join(current, ".agents", "skills"))
		if root != "" && current == root {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return directories
}

func loadAutomaticSkills(dir, source string, includeRootFiles bool) LoadSkillsResult {
	if _, err := os.Stat(dir); err != nil {
		return LoadSkillsResult{Skills: []Skill{}, Diagnostics: []ResourceDiagnostic{}}
	}
	return loadSkillsFromDirInternal(dir, source, includeRootFiles, &skillIgnoreMatcher{}, dir, map[string]bool{})
}

func resolveConfiguredPaths(paths []string, baseDir string) []string {
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		resolved = append(resolved, resolveResourcePathFrom(path, baseDir))
	}
	return resolved
}

func loadCommandSkills(options commandResourceOptions) LoadSkillsResult {
	inputs := make([]LoadSkillsResult, 0)
	if len(options.skillPaths) > 0 {
		inputs = append(inputs, LoadSkills(LoadSkillsOptions{
			CWD: options.cwd, AgentDir: options.agentDir, SkillPaths: options.skillPaths,
		}))
	}
	projectBase := filepath.Join(options.cwd, ".pi")
	if !options.noSkills && options.trusted {
		configured := resolveConfiguredPaths(options.projectSkillPaths, projectBase)
		if len(configured) > 0 {
			inputs = append(inputs, retagSkills(LoadSkills(LoadSkillsOptions{
				CWD: options.cwd, AgentDir: options.agentDir, SkillPaths: configured,
			}), "project", projectBase, "local"))
		}
		inputs = append(inputs, loadAutomaticSkills(filepath.Join(projectBase, "skills"), "project", true))
		for _, dir := range ancestorAgentsSkillDirs(options.cwd) {
			inputs = append(inputs, loadAutomaticSkills(dir, "project", false))
		}
	}
	if !options.noSkills {
		configured := resolveConfiguredPaths(options.globalSkillPaths, options.agentDir)
		if len(configured) > 0 {
			inputs = append(inputs, retagSkills(LoadSkills(LoadSkillsOptions{
				CWD: options.cwd, AgentDir: options.agentDir, SkillPaths: configured,
			}), "user", options.agentDir, "local"))
		}
		inputs = append(inputs, loadAutomaticSkills(filepath.Join(options.agentDir, "skills"), "user", true))
		if home, err := os.UserHomeDir(); err == nil {
			userAgentsDir := filepath.Join(home, ".agents", "skills")
			inputs = append(inputs, loadAutomaticSkills(userAgentsDir, "user", false))
		}
		if len(options.packageSkillPaths) > 0 {
			inputs = append(inputs, retagSkills(LoadSkills(LoadSkillsOptions{
				CWD: options.cwd, AgentDir: options.agentDir, SkillPaths: options.packageSkillPaths,
			}), "temporary", options.cwd, "package"))
		}
	}
	return combineSkills(inputs)
}

func retagPrompts(prompts []PromptTemplate, scope, baseDir, source string) []PromptTemplate {
	for index := range prompts {
		prompts[index].SourceInfo = SourceInfo{
			Path: prompts[index].FilePath, Source: source, Scope: scope,
			Origin: "top-level", BaseDir: baseDir,
		}
	}
	return prompts
}

func combinePrompts(inputs [][]PromptTemplate) ([]PromptTemplate, []ResourceDiagnostic) {
	combined := make([]PromptTemplate, 0)
	diagnostics := make([]ResourceDiagnostic, 0)
	seen := make(map[string]PromptTemplate)
	for _, input := range inputs {
		for _, prompt := range input {
			if winner, collision := seen[prompt.Name]; collision {
				diagnostics = append(diagnostics, ResourceDiagnostic{
					Type: "collision", Message: fmt.Sprintf("name %q collision", prompt.Name), Path: prompt.FilePath,
					Collision: &ResourceCollision{ResourceType: "prompt", Name: prompt.Name, WinnerPath: winner.FilePath, LoserPath: prompt.FilePath},
				})
				continue
			}
			seen[prompt.Name] = prompt
			combined = append(combined, prompt)
		}
	}
	return combined, diagnostics
}

func loadPromptsAtPaths(paths []string, cwd, agentDir string) []PromptTemplate {
	if len(paths) == 0 {
		return []PromptTemplate{}
	}
	return LoadPromptTemplates(LoadPromptTemplatesOptions{CWD: cwd, AgentDir: agentDir, PromptPaths: paths})
}

func loadCommandPrompts(options commandResourceOptions) ([]PromptTemplate, []ResourceDiagnostic) {
	inputs := make([][]PromptTemplate, 0)
	inputs = append(inputs, loadPromptsAtPaths(options.promptPaths, options.cwd, options.agentDir))
	projectBase := filepath.Join(options.cwd, ".pi")
	if !options.noPrompts && options.trusted {
		paths := resolveConfiguredPaths(options.projectPromptPaths, projectBase)
		inputs = append(inputs, retagPrompts(loadPromptsAtPaths(paths, options.cwd, options.agentDir), "project", projectBase, "local"))
		inputs = append(inputs, loadPromptsAtPaths([]string{filepath.Join(projectBase, "prompts")}, options.cwd, options.agentDir))
	}
	if !options.noPrompts {
		paths := resolveConfiguredPaths(options.globalPromptPaths, options.agentDir)
		inputs = append(inputs, retagPrompts(loadPromptsAtPaths(paths, options.cwd, options.agentDir), "user", options.agentDir, "local"))
		inputs = append(inputs, loadPromptsAtPaths([]string{filepath.Join(options.agentDir, "prompts")}, options.cwd, options.agentDir))
		if len(options.packagePromptPaths) > 0 {
			inputs = append(inputs, retagPrompts(loadPromptsAtPaths(options.packagePromptPaths, options.cwd, options.agentDir), "temporary", options.cwd, "package"))
		}
	}
	return combinePrompts(inputs)
}

func loadCommandResources(options commandResourceOptions) ([]Skill, []PromptTemplate, []ResourceDiagnostic) {
	skills := loadCommandSkills(options)
	prompts, promptDiagnostics := loadCommandPrompts(options)
	diagnostics := append(skills.Diagnostics, promptDiagnostics...)
	return skills.Skills, prompts, diagnostics
}

// LoadProjectContextFiles loads the global context file followed by one file
// per directory from the filesystem root through cwd.
func LoadProjectContextFiles(cwd, agentDir string) ([]ContextFile, []ResourceDiagnostic) {
	cwd = resolveResourcePath(cwd)
	agentDir = resolveResourcePath(agentDir)
	contextFiles := make([]ContextFile, 0)
	diagnostics := make([]ResourceDiagnostic, 0)
	seen := make(map[string]struct{})

	global, fileDiagnostics := loadContextFileFromDir(agentDir)
	diagnostics = append(diagnostics, fileDiagnostics...)
	if global != nil {
		contextFiles = append(contextFiles, *global)
		seen[global.Path] = struct{}{}
	}

	ancestorFiles := make([]ContextFile, 0)
	for current := cwd; ; current = filepath.Dir(current) {
		contextFile, fileDiagnostics := loadContextFileFromDir(current)
		diagnostics = append(diagnostics, fileDiagnostics...)
		if contextFile != nil {
			if _, duplicate := seen[contextFile.Path]; !duplicate {
				ancestorFiles = append([]ContextFile{*contextFile}, ancestorFiles...)
				seen[contextFile.Path] = struct{}{}
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return append(contextFiles, ancestorFiles...), diagnostics
}

func loadContextFileFromDir(dir string) (*ContextFile, []ResourceDiagnostic) {
	diagnostics := make([]ResourceDiagnostic, 0)
	for _, filename := range contextFileCandidates {
		path := filepath.Join(dir, filename)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		content, err := os.ReadFile(path)
		if err == nil {
			return &ContextFile{Path: path, Content: decodeResourceUTF8(content)}, diagnostics
		}
		diagnostic := ResourceDiagnostic{
			Path:    path,
			Message: fmt.Sprintf("Could not read %s: %v", path, err),
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	return nil, diagnostics
}

func discoverPromptFile(cwd, agentDir string, projectTrusted bool, filename string) string {
	projectPath := filepath.Join(cwd, ".pi", filename)
	if projectTrusted && pathExists(projectPath) {
		return projectPath
	}
	globalPath := filepath.Join(agentDir, filename)
	if pathExists(globalPath) {
		return globalPath
	}
	return ""
}

func resolvePromptInput(input, description string) (*string, *ResourceDiagnostic) {
	if input == "" {
		return nil, nil
	}
	if pathExists(input) {
		content, err := os.ReadFile(input)
		if err == nil {
			resolved := decodeResourceUTF8(content)
			return &resolved, nil
		}
		diagnostic := ResourceDiagnostic{
			Path:    input,
			Message: fmt.Sprintf("Could not read %s file %s: %v", description, input, err),
		}
		literal := input
		return &literal, &diagnostic
	}
	literal := input
	return &literal, nil
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func resolveResourcePath(path string) string {
	path = normalizeResourcePath(path)
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func normalizeResourcePath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`)) {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, path[2:])
		}
	}
	if strings.HasPrefix(path, "file://") {
		if parsed, err := url.Parse(path); err == nil && (parsed.Host == "" || strings.EqualFold(parsed.Host, "localhost")) {
			return filepath.FromSlash(parsed.Path)
		}
	}
	return path
}

func decodeResourceUTF8(data []byte) string {
	decoded, _ := textunicode.UTF8.NewDecoder().Bytes(data)
	return string(decoded)
}
