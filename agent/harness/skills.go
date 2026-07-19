package harness

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"unicode/utf16"

	"gopkg.in/yaml.v3"

	"github.com/OrdalieTech/pi-go/internal/ignorerules"
)

const (
	maxHarnessSkillNameLength        = 64
	maxHarnessSkillDescriptionLength = 1024
)

var harnessSkillIgnoreFiles = []string{".gitignore", ".ignore", ".fdignore"}

type SkillDiagnosticCode string

const (
	SkillDiagnosticFileInfoFailed SkillDiagnosticCode = "file_info_failed"
	SkillDiagnosticListFailed     SkillDiagnosticCode = "list_failed"
	SkillDiagnosticReadFailed     SkillDiagnosticCode = "read_failed"
	SkillDiagnosticParseFailed    SkillDiagnosticCode = "parse_failed"
	SkillDiagnosticInvalidMeta    SkillDiagnosticCode = "invalid_metadata"
)

type SkillDiagnostic struct {
	Type    string
	Code    SkillDiagnosticCode
	Message string
	Path    string
}

type HarnessSkillsResult struct {
	Skills      []Skill
	Diagnostics []SkillDiagnostic
}

type harnessIgnoreMatcher struct {
	rules []ignorerules.Rule
}

func (matcher *harnessIgnoreMatcher) add(line, prefix string) {
	trimmed := strings.TrimFunc(line, isHarnessTrimSpace)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(line, `\#`) {
		return
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
		return
	}
	matcher.rules = append(matcher.rules, ignorerules.Rule{
		Base: prefix, Pattern: line, Negated: negated, Directory: directory,
		BasenameOnly: !anchored && !strings.Contains(line, "/"),
	})
}

func (matcher *harnessIgnoreMatcher) ignores(relativePath string, directory bool) bool {
	return ignorerules.Ignores(matcher.rules, relativePath, directory)
}

func harnessFileErrorCode(err error) FileErrorCode {
	var fileError *FileError
	if errors.As(err, &fileError) {
		return fileError.Code
	}
	if errors.Is(err, fs.ErrNotExist) {
		return FileErrorNotFound
	}
	return FileErrorUnknown
}

func harnessWarning(code SkillDiagnosticCode, path string, err error) SkillDiagnostic {
	return SkillDiagnostic{Type: "warning", Code: code, Message: err.Error(), Path: path}
}

func resolveHarnessKind(env ResourceFileSystem, info FileInfo, diagnostics *[]SkillDiagnostic) FileKind {
	if info.Kind == FileKindFile || info.Kind == FileKindDirectory {
		return info.Kind
	}
	canonical, err := env.ResourceCanonicalPath(info.Path)
	if err != nil {
		if harnessFileErrorCode(err) != FileErrorNotFound {
			*diagnostics = append(*diagnostics, harnessWarning(SkillDiagnosticFileInfoFailed, info.Path, err))
		}
		return ""
	}
	target, err := env.ResourceFileInfo(canonical)
	if err != nil {
		if harnessFileErrorCode(err) != FileErrorNotFound {
			*diagnostics = append(*diagnostics, harnessWarning(SkillDiagnosticFileInfoFailed, info.Path, err))
		}
		return ""
	}
	if target.Kind == FileKindFile || target.Kind == FileKindDirectory {
		return target.Kind
	}
	return ""
}

func addHarnessIgnoreRules(env ResourceFileSystem, matcher *harnessIgnoreMatcher, dir, root string, diagnostics *[]SkillDiagnostic) {
	relativeDir := relativeHarnessPath(root, dir)
	for _, name := range harnessSkillIgnoreFiles {
		ignorePath := joinHarnessPath(dir, name)
		info, err := env.ResourceFileInfo(ignorePath)
		if err != nil {
			if harnessFileErrorCode(err) != FileErrorNotFound {
				*diagnostics = append(*diagnostics, harnessWarning(SkillDiagnosticFileInfoFailed, ignorePath, err))
			}
			continue
		}
		if info.Kind != FileKindFile {
			continue
		}
		contents, err := env.ResourceReadTextFile(ignorePath)
		if err != nil {
			*diagnostics = append(*diagnostics, harnessWarning(SkillDiagnosticReadFailed, ignorePath, err))
			continue
		}
		prefix := relativeDir
		for _, line := range strings.Split(strings.ReplaceAll(contents, "\r\n", "\n"), "\n") {
			matcher.add(line, prefix)
		}
	}
}

func loadHarnessSkill(env ResourceFileSystem, filePath string) (*Skill, []SkillDiagnostic) {
	contents, err := env.ResourceReadTextFile(filePath)
	if err != nil {
		return nil, []SkillDiagnostic{harnessWarning(SkillDiagnosticReadFailed, filePath, err)}
	}
	frontmatter, body, err := parseHarnessFrontmatter(contents)
	if err != nil {
		return nil, []SkillDiagnostic{harnessWarning(SkillDiagnosticParseFailed, filePath, err)}
	}
	diagnostics := make([]SkillDiagnostic, 0, 6)
	description, _ := frontmatter["description"].(string)
	if strings.TrimFunc(description, isHarnessTrimSpace) == "" {
		diagnostics = append(diagnostics, SkillDiagnostic{Type: "warning", Code: SkillDiagnosticInvalidMeta, Message: "description is required", Path: filePath})
	} else if length := len(utf16.Encode([]rune(description))); length > maxHarnessSkillDescriptionLength {
		diagnostics = append(diagnostics, SkillDiagnostic{Type: "warning", Code: SkillDiagnosticInvalidMeta, Message: fmt.Sprintf("description exceeds %d characters (%d)", maxHarnessSkillDescriptionLength, length), Path: filePath})
	}
	name, _ := frontmatter["name"].(string)
	parentName := basenameHarnessPath(dirnameHarnessPath(filePath))
	if name == "" {
		name = parentName
	}
	for _, message := range validateHarnessSkillName(name, parentName) {
		diagnostics = append(diagnostics, SkillDiagnostic{Type: "warning", Code: SkillDiagnosticInvalidMeta, Message: message, Path: filePath})
	}
	if strings.TrimFunc(description, isHarnessTrimSpace) == "" {
		return nil, diagnostics
	}
	disable, _ := frontmatter["disable-model-invocation"].(bool)
	return &Skill{Name: name, Description: description, Content: body, FilePath: filePath, DisableModelInvocation: disable}, diagnostics
}

func parseHarnessFrontmatter(content string) (map[string]any, string, error) {
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	if !strings.HasPrefix(normalized, "---") {
		return map[string]any{}, normalized, nil
	}
	end := strings.Index(normalized[3:], "\n---")
	if end < 0 {
		return map[string]any{}, normalized, nil
	}
	end += 3
	frontmatter := map[string]any{}
	if yamlText := normalized[4:end]; yamlText != "" {
		if err := yaml.Unmarshal([]byte(yamlText), &frontmatter); err != nil {
			return nil, "", err
		}
	}
	return frontmatter, strings.TrimFunc(normalized[end+4:], isHarnessTrimSpace), nil
}

func validateHarnessSkillName(name, parentName string) []string {
	messages := make([]string, 0, 5)
	if name != parentName {
		messages = append(messages, fmt.Sprintf("name %q does not match parent directory %q", name, parentName))
	}
	if length := len(utf16.Encode([]rune(name))); length > maxHarnessSkillNameLength {
		messages = append(messages, fmt.Sprintf("name exceeds %d characters (%d)", maxHarnessSkillNameLength, length))
	}
	valid := name != ""
	for _, character := range name {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			valid = false
			break
		}
	}
	if !valid {
		messages = append(messages, "name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)")
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		messages = append(messages, "name must not start or end with a hyphen")
	}
	if strings.Contains(name, "--") {
		messages = append(messages, "name must not contain consecutive hyphens")
	}
	return messages
}

func loadHarnessSkillsDir(env ResourceFileSystem, dir string, includeRootFiles bool, matcher *harnessIgnoreMatcher, root string) HarnessSkillsResult {
	result := HarnessSkillsResult{Skills: []Skill{}, Diagnostics: []SkillDiagnostic{}}
	dirInfo, err := env.ResourceFileInfo(dir)
	if err != nil {
		if harnessFileErrorCode(err) != FileErrorNotFound {
			result.Diagnostics = append(result.Diagnostics, harnessWarning(SkillDiagnosticFileInfoFailed, dir, err))
		}
		return result
	}
	if resolveHarnessKind(env, dirInfo, &result.Diagnostics) != FileKindDirectory {
		return result
	}
	addHarnessIgnoreRules(env, matcher, dir, root, &result.Diagnostics)
	entries, err := env.ResourceListDir(dir)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, harnessWarning(SkillDiagnosticListFailed, dir, err))
		return result
	}
	for _, entry := range entries {
		if entry.Name != "SKILL.md" || resolveHarnessKind(env, entry, &result.Diagnostics) != FileKindFile || matcher.ignores(relativeHarnessPath(root, entry.Path), false) {
			continue
		}
		skill, diagnostics := loadHarnessSkill(env, entry.Path)
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
		if skill != nil {
			result.Skills = append(result.Skills, *skill)
		}
		return result
	}
	sort.SliceStable(entries, func(left, right int) bool { return entries[left].Name < entries[right].Name })
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, ".") || entry.Name == "node_modules" {
			continue
		}
		kind := resolveHarnessKind(env, entry, &result.Diagnostics)
		if kind == "" || matcher.ignores(relativeHarnessPath(root, entry.Path), kind == FileKindDirectory) {
			continue
		}
		if kind == FileKindDirectory {
			nested := loadHarnessSkillsDir(env, entry.Path, false, matcher, root)
			result.Skills = append(result.Skills, nested.Skills...)
			result.Diagnostics = append(result.Diagnostics, nested.Diagnostics...)
			continue
		}
		if kind != FileKindFile || !includeRootFiles || !strings.HasSuffix(entry.Name, ".md") {
			continue
		}
		skill, diagnostics := loadHarnessSkill(env, entry.Path)
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
		if skill != nil {
			result.Skills = append(result.Skills, *skill)
		}
	}
	return result
}

// LoadSkills traverses one or more roots with upstream's SKILL.md stopping rule.
func LoadSkills(env ResourceFileSystem, dirs ...string) HarnessSkillsResult {
	result := HarnessSkillsResult{Skills: []Skill{}, Diagnostics: []SkillDiagnostic{}}
	for _, dir := range dirs {
		info, err := env.ResourceFileInfo(dir)
		if err != nil {
			if harnessFileErrorCode(err) != FileErrorNotFound {
				result.Diagnostics = append(result.Diagnostics, harnessWarning(SkillDiagnosticFileInfoFailed, dir, err))
			}
			continue
		}
		if resolveHarnessKind(env, info, &result.Diagnostics) != FileKindDirectory {
			continue
		}
		loaded := loadHarnessSkillsDir(env, info.Path, true, &harnessIgnoreMatcher{}, info.Path)
		result.Skills = append(result.Skills, loaded.Skills...)
		result.Diagnostics = append(result.Diagnostics, loaded.Diagnostics...)
	}
	return result
}

type SourcedSkillInput[T any] struct {
	Path   string
	Source T
}

type SourcedSkill[T any] struct {
	Skill  Skill
	Source T
}

type SourcedSkillDiagnostic[T any] struct {
	SkillDiagnostic
	Source T
}

func LoadSourcedSkills[T any](env ResourceFileSystem, inputs []SourcedSkillInput[T], mapSkill ...func(Skill, T) Skill) ([]SourcedSkill[T], []SourcedSkillDiagnostic[T]) {
	var skills []SourcedSkill[T]
	var diagnostics []SourcedSkillDiagnostic[T]
	for _, input := range inputs {
		result := LoadSkills(env, input.Path)
		for _, skill := range result.Skills {
			if len(mapSkill) > 0 && mapSkill[0] != nil {
				skill = mapSkill[0](skill, input.Source)
			}
			skills = append(skills, SourcedSkill[T]{Skill: skill, Source: input.Source})
		}
		for _, diagnostic := range result.Diagnostics {
			diagnostics = append(diagnostics, SourcedSkillDiagnostic[T]{SkillDiagnostic: diagnostic, Source: input.Source})
		}
	}
	return skills, diagnostics
}

// FormatSkillInvocation embeds a loaded skill and optional explicit instructions.
func FormatSkillInvocation(skill Skill, additionalInstructions string) string {
	block := fmt.Sprintf("<skill name=\"%s\" location=\"%s\">\nReferences are relative to %s.\n\n%s\n</skill>", skill.Name, skill.FilePath, dirnameHarnessPath(skill.FilePath), skill.Content)
	if additionalInstructions != "" {
		return block + "\n\n" + additionalInstructions
	}
	return block
}

func joinHarnessPath(base, child string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(child, "/")
}

func dirnameHarnessPath(path string) string {
	normalized := strings.TrimRight(path, "/")
	index := strings.LastIndex(normalized, "/")
	if index <= 0 {
		return "/"
	}
	return normalized[:index]
}

func basenameHarnessPath(path string) string {
	normalized := strings.TrimRight(path, "/")
	index := strings.LastIndex(normalized, "/")
	if index < 0 {
		return normalized
	}
	return normalized[index+1:]
}

func relativeHarnessPath(root, path string) string {
	normalizedRoot := strings.TrimRight(root, "/")
	normalizedPath := strings.TrimRight(path, "/")
	if normalizedPath == normalizedRoot {
		return ""
	}
	if strings.HasPrefix(normalizedPath, normalizedRoot+"/") {
		return normalizedPath[len(normalizedRoot)+1:]
	}
	return strings.TrimLeft(normalizedPath, "/")
}

func isHarnessTrimSpace(character rune) bool {
	switch {
	case character >= '\t' && character <= '\r':
		return true
	case character == ' ', character == '\u00a0', character == '\u1680', character == '\u2028', character == '\u2029', character == '\u202f', character == '\u205f', character == '\u3000', character == '\ufeff':
		return true
	case character >= '\u2000' && character <= '\u200a':
		return true
	default:
		return false
	}
}
