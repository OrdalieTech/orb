package harness

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

type PromptTemplateDiagnosticCode string

const (
	PromptTemplateDiagnosticFileInfoFailed PromptTemplateDiagnosticCode = "file_info_failed"
	PromptTemplateDiagnosticListFailed     PromptTemplateDiagnosticCode = "list_failed"
	PromptTemplateDiagnosticReadFailed     PromptTemplateDiagnosticCode = "read_failed"
	PromptTemplateDiagnosticParseFailed    PromptTemplateDiagnosticCode = "parse_failed"
)

type PromptTemplateDiagnostic struct {
	Type    string
	Code    PromptTemplateDiagnosticCode
	Message string
	Path    string
}

type HarnessPromptTemplatesResult struct {
	PromptTemplates []PromptTemplate
	Diagnostics     []PromptTemplateDiagnostic
}

func promptTemplateWarning(code PromptTemplateDiagnosticCode, path string, err error) PromptTemplateDiagnostic {
	return PromptTemplateDiagnostic{Type: "warning", Code: code, Message: err.Error(), Path: path}
}

func resolvePromptTemplateKind(env ResourceFileSystem, info FileInfo, diagnostics *[]PromptTemplateDiagnostic) FileKind {
	if info.Kind == FileKindFile || info.Kind == FileKindDirectory {
		return info.Kind
	}
	canonical, err := env.ResourceCanonicalPath(info.Path)
	if err != nil {
		if harnessFileErrorCode(err) != FileErrorNotFound {
			*diagnostics = append(*diagnostics, promptTemplateWarning(PromptTemplateDiagnosticFileInfoFailed, info.Path, err))
		}
		return ""
	}
	target, err := env.ResourceFileInfo(canonical)
	if err != nil {
		if harnessFileErrorCode(err) != FileErrorNotFound {
			*diagnostics = append(*diagnostics, promptTemplateWarning(PromptTemplateDiagnosticFileInfoFailed, info.Path, err))
		}
		return ""
	}
	if target.Kind == FileKindFile || target.Kind == FileKindDirectory {
		return target.Kind
	}
	return ""
}

func loadHarnessPromptTemplate(env ResourceFileSystem, path string) (*PromptTemplate, []PromptTemplateDiagnostic) {
	contents, err := env.ResourceReadTextFile(path)
	if err != nil {
		return nil, []PromptTemplateDiagnostic{promptTemplateWarning(PromptTemplateDiagnosticReadFailed, path, err)}
	}
	frontmatter, body, err := parseHarnessFrontmatter(contents)
	if err != nil {
		return nil, []PromptTemplateDiagnostic{promptTemplateWarning(PromptTemplateDiagnosticParseFailed, path, err)}
	}
	description, _ := frontmatter["description"].(string)
	if description == "" {
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimFunc(line, isHarnessTrimSpace) == "" {
				continue
			}
			units := utf16.Encode([]rune(line))
			if len(units) <= 60 {
				description = line
			} else {
				description = string(utf16.Decode(units[:60])) + "..."
			}
			break
		}
	}
	name := basenameHarnessPath(path)
	if strings.HasSuffix(strings.ToLower(name), ".md") {
		name = name[:len(name)-3]
	}
	return &PromptTemplate{Name: name, Description: description, Content: body}, nil
}

func loadHarnessPromptTemplatesDir(env ResourceFileSystem, dir string) HarnessPromptTemplatesResult {
	result := HarnessPromptTemplatesResult{PromptTemplates: []PromptTemplate{}, Diagnostics: []PromptTemplateDiagnostic{}}
	entries, err := env.ResourceListDir(dir)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, promptTemplateWarning(PromptTemplateDiagnosticListFailed, dir, err))
		return result
	}
	sort.SliceStable(entries, func(left, right int) bool { return entries[left].Name < entries[right].Name })
	for _, entry := range entries {
		kind := resolvePromptTemplateKind(env, entry, &result.Diagnostics)
		if kind != FileKindFile || !strings.HasSuffix(entry.Name, ".md") {
			continue
		}
		template, diagnostics := loadHarnessPromptTemplate(env, entry.Path)
		result.Diagnostics = append(result.Diagnostics, diagnostics...)
		if template != nil {
			result.PromptTemplates = append(result.PromptTemplates, *template)
		}
	}
	return result
}

// LoadPromptTemplates loads explicit files or direct markdown children through the execution environment.
func LoadPromptTemplates(env ResourceFileSystem, paths ...string) HarnessPromptTemplatesResult {
	result := HarnessPromptTemplatesResult{PromptTemplates: []PromptTemplate{}, Diagnostics: []PromptTemplateDiagnostic{}}
	for _, path := range paths {
		info, err := env.ResourceFileInfo(path)
		if err != nil {
			if harnessFileErrorCode(err) != FileErrorNotFound {
				result.Diagnostics = append(result.Diagnostics, promptTemplateWarning(PromptTemplateDiagnosticFileInfoFailed, path, err))
			}
			continue
		}
		kind := resolvePromptTemplateKind(env, info, &result.Diagnostics)
		if kind == FileKindDirectory {
			loaded := loadHarnessPromptTemplatesDir(env, info.Path)
			result.PromptTemplates = append(result.PromptTemplates, loaded.PromptTemplates...)
			result.Diagnostics = append(result.Diagnostics, loaded.Diagnostics...)
		} else if kind == FileKindFile && strings.HasSuffix(info.Name, ".md") {
			template, diagnostics := loadHarnessPromptTemplate(env, info.Path)
			result.Diagnostics = append(result.Diagnostics, diagnostics...)
			if template != nil {
				result.PromptTemplates = append(result.PromptTemplates, *template)
			}
		}
	}
	return result
}

type SourcedPromptTemplateInput[T any] struct {
	Path   string
	Source T
}

type SourcedPromptTemplate[T any] struct {
	PromptTemplate PromptTemplate
	Source         T
}

type SourcedPromptTemplateDiagnostic[T any] struct {
	PromptTemplateDiagnostic
	Source T
}

func LoadSourcedPromptTemplates[T any](env ResourceFileSystem, inputs []SourcedPromptTemplateInput[T], mapTemplate ...func(PromptTemplate, T) PromptTemplate) ([]SourcedPromptTemplate[T], []SourcedPromptTemplateDiagnostic[T]) {
	var templates []SourcedPromptTemplate[T]
	var diagnostics []SourcedPromptTemplateDiagnostic[T]
	for _, input := range inputs {
		result := LoadPromptTemplates(env, input.Path)
		for _, template := range result.PromptTemplates {
			if len(mapTemplate) > 0 && mapTemplate[0] != nil {
				template = mapTemplate[0](template, input.Source)
			}
			templates = append(templates, SourcedPromptTemplate[T]{PromptTemplate: template, Source: input.Source})
		}
		for _, diagnostic := range result.Diagnostics {
			diagnostics = append(diagnostics, SourcedPromptTemplateDiagnostic[T]{PromptTemplateDiagnostic: diagnostic, Source: input.Source})
		}
	}
	return templates, diagnostics
}

var (
	harnessPromptPositionalPattern = regexp.MustCompile(`\$([0-9]+)`)
	harnessPromptSlicePattern      = regexp.MustCompile(`\$\{@:([0-9]+)(?::([0-9]+))?\}`)
	harnessPromptAllPattern        = regexp.MustCompile(`\$ARGUMENTS`)
	harnessPromptAtPattern         = regexp.MustCompile(`\$@`)
)

func harnessPromptIndex(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return parsed
}

// FormatPromptTemplateInvocation follows the harness's sequential replacement
// passes; unlike coding-agent templates, the harness treats default syntax as
// literal text and later passes can expand placeholders introduced earlier.
func FormatPromptTemplateInvocation(template PromptTemplate, args []string) string {
	allArgs := strings.Join(args, " ")
	result := harnessPromptPositionalPattern.ReplaceAllStringFunc(template.Content, func(match string) string {
		index := harnessPromptIndex(match[1:]) - 1
		if index < 0 || index >= len(args) {
			return ""
		}
		return args[index]
	})
	result = harnessPromptSlicePattern.ReplaceAllStringFunc(result, func(match string) string {
		parts := harnessPromptSlicePattern.FindStringSubmatch(match)
		start := harnessPromptIndex(parts[1]) - 1
		if start < 0 {
			start = 0
		}
		if start >= len(args) {
			return ""
		}
		end := len(args)
		if parts[2] != "" {
			length := harnessPromptIndex(parts[2])
			if length <= len(args)-start {
				end = start + length
			}
		}
		return strings.Join(args[start:end], " ")
	})
	result = harnessPromptAllPattern.ReplaceAllStringFunc(result, func(string) string { return allArgs })
	return harnessPromptAtPattern.ReplaceAllStringFunc(result, func(string) string { return allArgs })
}
