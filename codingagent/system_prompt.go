package codingagent

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OrdalieTech/pi-go/codingagent/config"
)

var defaultSystemPromptTools = []string{"read", "bash", "edit", "write"}

type toolPromptMetadata struct {
	snippet    string
	guidelines []string
}

var builtInToolPromptMetadata = map[string]toolPromptMetadata{
	"read": {
		snippet:    "Read file contents",
		guidelines: []string{"Use read to examine files instead of cat or sed."},
	},
	"bash": {
		snippet: "Execute bash commands (ls, grep, find, etc.)",
	},
	"edit": {
		snippet: "Make precise file edits with exact text replacement, including multiple disjoint edits in one call",
		guidelines: []string{
			"Use edit for precise changes (edits[].oldText must match exactly)",
			"When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls",
			"Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
			"Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
		},
	},
	"write": {
		snippet:    "Create or overwrite files",
		guidelines: []string{"Use write only for new files or complete rewrites."},
	},
	"grep": {
		snippet: "Search file contents for patterns (respects .gitignore)",
	},
	"find": {
		snippet: "Find files by glob pattern (respects .gitignore)",
	},
	"ls": {
		snippet: "List directory contents",
	},
}

// SystemPromptOptions contains the already-resolved inputs to the upstream
// prompt builder. Nil SelectedTools means the upstream default tool set;
// a non-nil empty slice means no tools.
type SystemPromptOptions struct {
	CustomPrompt       *string
	SelectedTools      []string
	ToolSnippets       map[string]string
	PromptGuidelines   []string
	AppendSystemPrompt *string
	CWD                string
	ContextFiles       []ContextFile
	Skills             []Skill
	PackageDir         string
}

// BuiltInToolPromptData returns the prompt snippets and guidelines contributed
// by built-in tools, in active-tool order.
func BuiltInToolPromptData(toolNames []string) (map[string]string, []string) {
	snippets := make(map[string]string)
	var guidelines []string
	for _, name := range toolNames {
		metadata, ok := builtInToolPromptMetadata[name]
		if !ok {
			continue
		}
		if metadata.snippet != "" {
			snippets[name] = metadata.snippet
		}
		guidelines = append(guidelines, metadata.guidelines...)
	}
	return snippets, guidelines
}

// BuildSystemPrompt assembles the system prompt in upstream byte order.
func BuildSystemPrompt(options SystemPromptOptions) string {
	promptCWD := strings.ReplaceAll(options.CWD, `\`, "/")
	appendSection := ""
	if options.AppendSystemPrompt != nil && *options.AppendSystemPrompt != "" {
		appendSection = "\n\n" + *options.AppendSystemPrompt
	}

	if options.CustomPrompt != nil && *options.CustomPrompt != "" {
		prompt := *options.CustomPrompt + appendSection
		prompt += formatProjectContext(options.ContextFiles)
		if options.SelectedTools == nil || slices.Contains(options.SelectedTools, "read") {
			prompt += FormatSkillsForPrompt(options.Skills)
		}
		return prompt + "\nCurrent working directory: " + promptCWD
	}

	tools := options.SelectedTools
	if tools == nil {
		tools = defaultSystemPromptTools
	}
	visibleTools := make([]string, 0, len(tools))
	for _, name := range tools {
		if options.ToolSnippets[name] != "" {
			visibleTools = append(visibleTools, "- "+name+": "+options.ToolSnippets[name])
		}
	}
	toolsList := "(none)"
	if len(visibleTools) > 0 {
		toolsList = strings.Join(visibleTools, "\n")
	}

	guidelines := make([]string, 0, len(options.PromptGuidelines)+3)
	seenGuidelines := make(map[string]struct{}, len(options.PromptGuidelines)+3)
	addGuideline := func(guideline string) {
		if _, seen := seenGuidelines[guideline]; seen {
			return
		}
		seenGuidelines[guideline] = struct{}{}
		guidelines = append(guidelines, guideline)
	}
	if slices.Contains(tools, "bash") && !slices.Contains(tools, "grep") && !slices.Contains(tools, "find") && !slices.Contains(tools, "ls") {
		addGuideline("Use bash for file operations like ls, rg, find")
	}
	for _, guideline := range options.PromptGuidelines {
		normalized := strings.TrimFunc(guideline, isJSTrimSpace)
		if normalized != "" {
			addGuideline(normalized)
		}
	}
	addGuideline("Be concise in your responses")
	addGuideline("Show file paths clearly when working with files")

	formattedGuidelines := make([]string, len(guidelines))
	for index, guideline := range guidelines {
		formattedGuidelines[index] = "- " + guideline
	}

	packageDir := resolvePromptPackageDir(options.PackageDir)
	readmePath := filepath.Join(packageDir, "README.md")
	docsPath := filepath.Join(packageDir, "docs")
	examplesPath := filepath.Join(packageDir, "examples")
	prompt := fmt.Sprintf(`You are an expert coding assistant operating inside pi, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.

Available tools:
%s

In addition to the tools above, you may have access to other custom tools depending on the project.

Guidelines:
%s

Pi documentation (read only when the user asks about pi itself, its SDK, extensions, themes, skills, or TUI):
- Main documentation: %s
- Additional docs: %s
- Examples: %s (extensions, custom tools, SDK)
- When reading pi docs or examples, resolve docs/... under Additional docs and examples/... under Examples, not the current working directory
- When asked about: extensions (docs/extensions.md, examples/extensions/), themes (docs/themes.md), skills (docs/skills.md), prompt templates (docs/prompt-templates.md), TUI components (docs/tui.md), keybindings (docs/keybindings.md), SDK integrations (docs/sdk.md), custom providers (docs/custom-provider.md), adding models (docs/models.md), pi packages (docs/packages.md)
- When working on pi topics, read the docs and examples, and follow .md cross-references before implementing
- Always read pi .md files completely and follow links to related docs (e.g., tui.md for TUI API details)`, toolsList, strings.Join(formattedGuidelines, "\n"), readmePath, docsPath, examplesPath)

	prompt += appendSection
	prompt += formatProjectContext(options.ContextFiles)
	if slices.Contains(tools, "read") {
		prompt += FormatSkillsForPrompt(options.Skills)
	}
	return prompt + "\nCurrent working directory: " + promptCWD
}

func formatProjectContext(contextFiles []ContextFile) string {
	if len(contextFiles) == 0 {
		return ""
	}
	var context strings.Builder
	context.WriteString("\n\n<project_context>\n\n")
	context.WriteString("Project-specific instructions and guidelines:\n\n")
	for _, file := range contextFiles {
		context.WriteString(`<project_instructions path="`)
		context.WriteString(file.Path)
		context.WriteString("\">\n")
		context.WriteString(file.Content)
		context.WriteString("\n</project_instructions>\n\n")
	}
	context.WriteString("</project_context>\n")
	return context.String()
}

func resolvePromptPackageDir(packageDir string) string {
	if packageDir == "" {
		packageDir = os.Getenv("PI_PACKAGE_DIR")
	}
	if packageDir == "" {
		if executable, err := os.Executable(); err == nil {
			packageDir = filepath.Dir(executable)
		} else {
			packageDir = "."
		}
	}
	if normalized, err := config.NormalizePath(packageDir); err == nil {
		packageDir = normalized
	}
	if absolute, err := filepath.Abs(packageDir); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(packageDir)
}

func isJSTrimSpace(character rune) bool {
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
