package codingagent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"

	"gopkg.in/yaml.v3"

	"github.com/OrdalieTech/pi-go/internal/ignorerules"
)

const (
	maxSkillNameLength        = 64
	maxSkillDescriptionLength = 1024
)

var skillIgnoreFiles = []string{".gitignore", ".ignore", ".fdignore"}

// SourceInfo identifies where a discovered slash-command resource came from.
type SourceInfo struct {
	Path    string `json:"path"`
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	Origin  string `json:"origin"`
	BaseDir string `json:"baseDir,omitempty"`
}

// Skill is the progressively-disclosed metadata for one Agent Skills file.
type Skill struct {
	Name                   string
	Description            string
	Content                string
	FilePath               string
	BaseDir                string
	AllowedTools           string
	SourceInfo             SourceInfo
	DisableModelInvocation bool
}

type ResourceCollision struct {
	ResourceType string
	Name         string
	WinnerPath   string
	LoserPath    string
}

type LoadSkillsResult struct {
	Skills      []Skill
	Diagnostics []ResourceDiagnostic
}

type LoadSkillsFromDirOptions struct {
	Dir    string
	Source string
}

type LoadSkillsOptions struct {
	CWD             string
	AgentDir        string
	SkillPaths      []string
	IncludeDefaults bool
}

type parsedFrontmatter struct {
	Values map[string]any
	Body   string
}

func parseResourceFrontmatter(content string) (parsedFrontmatter, error) {
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	if !strings.HasPrefix(normalized, "---") {
		return parsedFrontmatter{Values: map[string]any{}, Body: normalized}, nil
	}
	end := strings.Index(normalized[3:], "\n---")
	if end < 0 {
		return parsedFrontmatter{Values: map[string]any{}, Body: normalized}, nil
	}
	end += 3
	yamlText := normalized[4:end]
	body := strings.TrimFunc(normalized[end+4:], isJSTrimSpace)
	if yamlText == "" {
		return parsedFrontmatter{Values: map[string]any{}, Body: body}, nil
	}
	var decoded any
	if err := yaml.Unmarshal([]byte(yamlText), &decoded); err != nil {
		return parsedFrontmatter{}, err
	}
	values, _ := decoded.(map[string]any)
	if values == nil {
		values = map[string]any{}
	}
	return parsedFrontmatter{Values: values, Body: body}, nil
}

func utf16Length(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func validateSkillName(name string) []string {
	errors := make([]string, 0, 4)
	if length := utf16Length(name); length > maxSkillNameLength {
		errors = append(errors, fmt.Sprintf("name exceeds %d characters (%d)", maxSkillNameLength, length))
	}
	valid := name != ""
	for _, character := range name {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			valid = false
			break
		}
	}
	if !valid {
		errors = append(errors, "name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		errors = append(errors, "name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		errors = append(errors, "name must not contain consecutive hyphens")
	}
	return errors
}

func validateSkillDescription(description string) []string {
	if strings.TrimFunc(description, isJSTrimSpace) == "" {
		return []string{"description is required"}
	}
	if length := utf16Length(description); length > maxSkillDescriptionLength {
		return []string{fmt.Sprintf("description exceeds %d characters (%d)", maxSkillDescriptionLength, length)}
	}
	return nil
}

func skillSourceInfo(filePath, baseDir, source string) SourceInfo {
	info := SourceInfo{Path: filePath, Source: source, Scope: "temporary", Origin: "top-level", BaseDir: baseDir}
	switch source {
	case "user":
		info.Source = "local"
		info.Scope = "user"
	case "project":
		info.Source = "local"
		info.Scope = "project"
	case "path":
		info.Source = "local"
	}
	return info
}

func loadSkillFromFile(filePath, source string) (*Skill, []ResourceDiagnostic) {
	contents, err := os.ReadFile(filePath)
	if err != nil {
		return nil, []ResourceDiagnostic{{Type: "warning", Message: err.Error(), Path: filePath}}
	}
	parsed, err := parseResourceFrontmatter(decodeResourceUTF8(contents))
	if err != nil {
		return nil, []ResourceDiagnostic{{Type: "warning", Message: err.Error(), Path: filePath}}
	}
	description, _ := parsed.Values["description"].(string)
	diagnostics := make([]ResourceDiagnostic, 0, 5)
	for _, message := range validateSkillDescription(description) {
		diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: message, Path: filePath})
	}
	name, _ := parsed.Values["name"].(string)
	if name == "" {
		name = filepath.Base(filepath.Dir(filePath))
	}
	for _, message := range validateSkillName(name) {
		diagnostics = append(diagnostics, ResourceDiagnostic{Type: "warning", Message: message, Path: filePath})
	}
	if strings.TrimFunc(description, isJSTrimSpace) == "" {
		return nil, diagnostics
	}
	allowedTools, _ := parsed.Values["allowed-tools"].(string)
	disable, _ := parsed.Values["disable-model-invocation"].(bool)
	baseDir := filepath.Dir(filePath)
	return &Skill{
		Name: name, Description: description, Content: parsed.Body,
		FilePath: filePath, BaseDir: baseDir, AllowedTools: allowedTools,
		SourceInfo: skillSourceInfo(filePath, baseDir, source), DisableModelInvocation: disable,
	}, diagnostics
}

type skillIgnoreMatcher struct {
	rules []ignorerules.Rule
}

func (matcher *skillIgnoreMatcher) addDirectoryRules(dir, root string) {
	relativeDir, err := filepath.Rel(root, dir)
	if err != nil || relativeDir == "." {
		relativeDir = ""
	}
	relativeDir = filepath.ToSlash(relativeDir)
	for _, name := range skillIgnoreFiles {
		contents, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(contents), "\r\n", "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, `\#`) {
				continue
			}
			negated := false
			if strings.HasPrefix(line, "!") {
				negated = true
				line = line[1:]
			} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
				line = line[1:]
			}
			anchored := strings.HasPrefix(line, "/")
			line = strings.TrimPrefix(line, "/")
			directory := strings.HasSuffix(line, "/")
			line = strings.TrimSuffix(line, "/")
			if line == "" {
				continue
			}
			matcher.rules = append(matcher.rules, ignorerules.Rule{
				Base: relativeDir, Pattern: filepath.ToSlash(line), Negated: negated,
				Directory: directory, BasenameOnly: !anchored && !strings.Contains(line, "/"),
			})
		}
	}
}

func (matcher *skillIgnoreMatcher) ignores(relativePath string, directory bool) bool {
	return ignorerules.Ignores(matcher.rules, filepath.ToSlash(relativePath), directory)
}

func loadSkillsFromDirInternal(dir, source string, includeRootFiles bool, matcher *skillIgnoreMatcher, root string, stack map[string]bool) LoadSkillsResult {
	result := LoadSkillsResult{Skills: []Skill{}, Diagnostics: []ResourceDiagnostic{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result
	}
	matcher.addDirectoryRules(dir, root)
	canonical := canonicalResourcePath(dir)
	if stack[canonical] {
		return result
	}
	stack[canonical] = true
	defer delete(stack, canonical)

	for _, entry := range entries {
		if entry.Name() != "SKILL.md" {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		info, statErr := os.Stat(fullPath)
		relative, relErr := filepath.Rel(root, fullPath)
		if statErr != nil || relErr != nil || !info.Mode().IsRegular() || matcher.ignores(relative, false) {
			continue
		}
		skill, diagnostics := loadSkillFromFile(fullPath, source)
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
		if skill != nil {
			result.Skills = append(result.Skills, *skill)
		}
		return result
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		fullPath := filepath.Join(dir, name)
		info, statErr := os.Stat(fullPath)
		if statErr != nil {
			continue
		}
		relative, relErr := filepath.Rel(root, fullPath)
		if relErr != nil || matcher.ignores(relative, info.IsDir()) {
			continue
		}
		if info.IsDir() {
			nested := loadSkillsFromDirInternal(fullPath, source, false, matcher, root, stack)
			result.Skills = append(result.Skills, nested.Skills...)
			result.Diagnostics = append(result.Diagnostics, nested.Diagnostics...)
			continue
		}
		if !info.Mode().IsRegular() || !includeRootFiles || !strings.HasSuffix(name, ".md") {
			continue
		}
		skill, diagnostics := loadSkillFromFile(fullPath, source)
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
		if skill != nil {
			result.Skills = append(result.Skills, *skill)
		}
	}
	return result
}

// LoadSkillsFromDir follows upstream's root-file and recursive SKILL.md discovery rules.
func LoadSkillsFromDir(options LoadSkillsFromDirOptions) LoadSkillsResult {
	dir := resolveResourcePath(options.Dir)
	if _, err := os.Stat(dir); err != nil {
		return LoadSkillsResult{Skills: []Skill{}, Diagnostics: []ResourceDiagnostic{}}
	}
	return loadSkillsFromDirInternal(dir, options.Source, true, &skillIgnoreMatcher{}, dir, map[string]bool{})
}

func canonicalResourcePath(path string) string {
	canonical, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = canonical
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

func pathIsWithin(target, root string) bool {
	target = resolveResourcePath(target)
	root = resolveResourcePath(root)
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// LoadSkills loads default and explicit locations, keeping the first name collision.
func LoadSkills(options LoadSkillsOptions) LoadSkillsResult {
	cwd := resolveResourcePath(options.CWD)
	agentDir := resolveResourcePath(options.AgentDir)
	userDir := filepath.Join(agentDir, "skills")
	projectDir := filepath.Join(cwd, ".pi", "skills")
	paths := make([]struct {
		path   string
		source string
	}, 0, len(options.SkillPaths)+2)
	if options.IncludeDefaults {
		paths = append(paths, struct{ path, source string }{userDir, "user"}, struct{ path, source string }{projectDir, "project"})
	}
	for _, rawPath := range options.SkillPaths {
		resolved := resolveResourcePathFrom(rawPath, cwd)
		source := "path"
		if !options.IncludeDefaults {
			if pathIsWithin(resolved, userDir) {
				source = "user"
			} else if pathIsWithin(resolved, projectDir) {
				source = "project"
			}
		}
		paths = append(paths, struct{ path, source string }{resolved, source})
	}

	result := LoadSkillsResult{Skills: []Skill{}, Diagnostics: []ResourceDiagnostic{}}
	byName := make(map[string]Skill)
	realPaths := make(map[string]struct{})
	for _, input := range paths {
		info, err := os.Stat(input.path)
		if err != nil {
			if !options.IncludeDefaults || input.source == "path" {
				result.Diagnostics = append(result.Diagnostics, ResourceDiagnostic{Type: "warning", Message: "skill path does not exist", Path: input.path})
			}
			continue
		}
		loaded := LoadSkillsResult{Skills: []Skill{}, Diagnostics: []ResourceDiagnostic{}}
		switch {
		case info.IsDir():
			loaded = loadSkillsFromDirInternal(input.path, input.source, true, &skillIgnoreMatcher{}, input.path, map[string]bool{})
		case info.Mode().IsRegular() && strings.HasSuffix(input.path, ".md"):
			skill, diagnostics := loadSkillFromFile(input.path, input.source)
			loaded.Diagnostics = diagnostics
			if skill != nil {
				loaded.Skills = append(loaded.Skills, *skill)
			}
		default:
			loaded.Diagnostics = append(loaded.Diagnostics, ResourceDiagnostic{Type: "warning", Message: "skill path is not a markdown file", Path: input.path})
		}
		result.Diagnostics = append(result.Diagnostics, loaded.Diagnostics...)
		for _, skill := range loaded.Skills {
			realPath := canonicalResourcePath(skill.FilePath)
			if _, duplicate := realPaths[realPath]; duplicate {
				continue
			}
			if winner, collision := byName[skill.Name]; collision {
				result.Diagnostics = append(result.Diagnostics, ResourceDiagnostic{
					Type: "collision", Message: fmt.Sprintf("name %q collision", skill.Name), Path: skill.FilePath,
					Collision: &ResourceCollision{ResourceType: "skill", Name: skill.Name, WinnerPath: winner.FilePath, LoserPath: skill.FilePath},
				})
				continue
			}
			byName[skill.Name] = skill
			realPaths[realPath] = struct{}{}
			result.Skills = append(result.Skills, skill)
		}
	}
	return result
}

func resolveResourcePathFrom(path, cwd string) string {
	path = normalizeResourcePath(strings.TrimFunc(path, isJSTrimSpace))
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	return resolveResourcePath(path)
}

// FormatSkillsForPrompt emits the Agent Skills progressive-disclosure XML block.
func FormatSkillsForPrompt(skills []Skill) string {
	visible := make([]Skill, 0, len(skills))
	for _, skill := range skills {
		if !skill.DisableModelInvocation {
			visible = append(visible, skill)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	lines := []string{
		"", "", "The following skills provide specialized instructions for specific tasks.",
		"Use the read tool to load a skill's file when the task matches its description.",
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.",
		"", "<available_skills>",
	}
	for _, skill := range visible {
		lines = append(lines,
			"  <skill>",
			"    <name>"+escapeSkillXML(skill.Name)+"</name>",
			"    <description>"+escapeSkillXML(skill.Description)+"</description>",
			"    <location>"+escapeSkillXML(skill.FilePath)+"</location>",
			"  </skill>",
		)
	}
	lines = append(lines, "</available_skills>")
	return strings.Join(lines, "\n")
}

func escapeSkillXML(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}

// SortSkillsByName is useful to applications that need a stable presentation order.
func SortSkillsByName(skills []Skill) {
	sort.SliceStable(skills, func(left, right int) bool { return skills[left].Name < skills[right].Name })
}
