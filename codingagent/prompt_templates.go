package codingagent

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// PromptTemplate is a file-backed slash command expanded before a prompt is sent.
type PromptTemplate struct {
	Name         string
	Description  string
	ArgumentHint string
	Content      string
	SourceInfo   SourceInfo
	FilePath     string
}

type LoadPromptTemplatesOptions struct {
	CWD             string
	AgentDir        string
	PromptPaths     []string
	IncludeDefaults bool
}

func promptSourceInfo(filePath, baseDir, scope string) SourceInfo {
	return SourceInfo{
		Path: filePath, Source: "local", Scope: scope, Origin: "top-level", BaseDir: baseDir,
	}
}

func firstPromptDescriptionLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimFunc(line, isJSTrimSpace) == "" {
			continue
		}
		codeUnits := utf16.Encode([]rune(line))
		if len(codeUnits) <= 60 {
			return line
		}
		return string(utf16.Decode(codeUnits[:60])) + "..."
	}
	return ""
}

func loadPromptTemplateFile(filePath string, sourceInfo SourceInfo) (*PromptTemplate, error) {
	contents, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	parsed, err := parseResourceFrontmatter(decodeResourceUTF8(contents))
	if err != nil {
		return nil, err
	}
	description, _ := parsed.Values["description"].(string)
	if description == "" {
		description = firstPromptDescriptionLine(parsed.Body)
	}
	argumentHint, _ := parsed.Values["argument-hint"].(string)
	return &PromptTemplate{
		Name: strings.TrimSuffix(filepath.Base(filePath), ".md"), Description: description,
		ArgumentHint: argumentHint, Content: parsed.Body, SourceInfo: sourceInfo, FilePath: filePath,
	}, nil
}

func loadPromptTemplatesFromDir(dir string, sourceFor func(string) SourceInfo) []PromptTemplate {
	templates := make([]PromptTemplate, 0)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return templates
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		info, err := os.Stat(fullPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		template, err := loadPromptTemplateFile(fullPath, sourceFor(fullPath))
		if err == nil && template != nil {
			templates = append(templates, *template)
		}
	}
	return templates
}

// LoadPromptTemplates discovers non-recursive markdown templates from default and explicit paths.
func LoadPromptTemplates(options LoadPromptTemplatesOptions) []PromptTemplate {
	cwd := resolveResourcePath(options.CWD)
	agentDir := resolveResourcePath(options.AgentDir)
	globalDir := filepath.Join(agentDir, "prompts")
	projectDir := filepath.Join(cwd, ".pi", "prompts")
	templates := make([]PromptTemplate, 0)

	loadPath := func(rawPath string) {
		resolved := resolveResourcePathFrom(rawPath, cwd)
		info, err := os.Stat(resolved)
		if err != nil {
			return
		}
		sourceFor := func(filePath string) SourceInfo {
			scope := "temporary"
			baseDir := filepath.Dir(filePath)
			if pathIsWithin(filePath, globalDir) {
				scope, baseDir = "user", globalDir
			} else if pathIsWithin(filePath, projectDir) {
				scope, baseDir = "project", projectDir
			} else if info.IsDir() {
				baseDir = resolved
			}
			return promptSourceInfo(filePath, baseDir, scope)
		}
		if info.IsDir() {
			templates = append(templates, loadPromptTemplatesFromDir(resolved, sourceFor)...)
		} else if info.Mode().IsRegular() && strings.HasSuffix(resolved, ".md") {
			template, loadErr := loadPromptTemplateFile(resolved, sourceFor(resolved))
			if loadErr == nil && template != nil {
				templates = append(templates, *template)
			}
		}
	}

	if options.IncludeDefaults {
		loadPath(globalDir)
		loadPath(projectDir)
	}
	for _, path := range options.PromptPaths {
		loadPath(path)
	}
	return templates
}

// ParseCommandArgs tokenizes template arguments with upstream's deliberately small quote grammar.
func ParseCommandArgs(argsString string) []string {
	args := make([]string, 0)
	var current strings.Builder
	var quote rune
	for _, character := range argsString {
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if isJSTrimSpace(character) {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(character)
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

var promptArgumentPattern = regexp.MustCompile(`\$\{([0-9]+|ARGUMENTS|@):-([^}]*)\}|\$\{@:([0-9]+)(:([0-9]+))?\}|\$(ARGUMENTS|@|[0-9]+)`)

func captureString(source string, indexes []int, group int) (string, bool) {
	startIndex := group * 2
	if startIndex+1 >= len(indexes) || indexes[startIndex] < 0 {
		return "", false
	}
	return source[indexes[startIndex]:indexes[startIndex+1]], true
}

func parseArgumentIndex(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return parsed
}

// SubstituteArgs replaces all placeholders in one pass, so inserted values are never re-expanded.
func SubstituteArgs(content string, args []string) string {
	allArgs := strings.Join(args, " ")
	matches := promptArgumentPattern.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content
	}
	var result strings.Builder
	last := 0
	for _, indexes := range matches {
		result.WriteString(content[last:indexes[0]])
		replacement := ""
		if target, exists := captureString(content, indexes, 1); exists {
			value := allArgs
			if target != "@" && target != "ARGUMENTS" {
				index := parseArgumentIndex(target) - 1
				if index >= 0 && index < len(args) {
					value = args[index]
				} else {
					value = ""
				}
			}
			if value == "" {
				value, _ = captureString(content, indexes, 2)
			}
			replacement = value
		} else if startText, exists := captureString(content, indexes, 3); exists {
			start := parseArgumentIndex(startText) - 1
			if start < 0 {
				start = 0
			}
			if start < len(args) {
				end := len(args)
				if lengthText, hasLength := captureString(content, indexes, 5); hasLength {
					length := parseArgumentIndex(lengthText)
					if length < 0 {
						length = 0
					}
					if length <= len(args)-start {
						end = start + length
					}
				}
				replacement = strings.Join(args[start:end], " ")
			}
		} else if simple, exists := captureString(content, indexes, 6); exists {
			if simple == "ARGUMENTS" || simple == "@" {
				replacement = allArgs
			} else {
				index := parseArgumentIndex(simple) - 1
				if index >= 0 && index < len(args) {
					replacement = args[index]
				}
			}
		}
		result.WriteString(replacement)
		last = indexes[1]
	}
	result.WriteString(content[last:])
	return result.String()
}

func splitSlashInvocation(text string) (name, args string, matched bool) {
	if !strings.HasPrefix(text, "/") || len(text) == 1 {
		return "", "", false
	}
	runes := []rune(text[1:])
	commandEnd := len(runes)
	for index, character := range runes {
		if isJSTrimSpace(character) {
			commandEnd = index
			break
		}
	}
	if commandEnd == 0 {
		return "", "", false
	}
	name = string(runes[:commandEnd])
	argumentStart := commandEnd
	for argumentStart < len(runes) && isJSTrimSpace(runes[argumentStart]) {
		argumentStart++
	}
	if commandEnd < len(runes) {
		args = string(runes[argumentStart:])
	}
	return name, args, true
}

// ExpandPromptTemplate expands a matching slash template or returns text unchanged.
func ExpandPromptTemplate(text string, templates []PromptTemplate) string {
	name, argsString, matched := splitSlashInvocation(text)
	if !matched {
		return text
	}
	for _, template := range templates {
		if template.Name == name {
			return SubstituteArgs(template.Content, ParseCommandArgs(argsString))
		}
	}
	return text
}
