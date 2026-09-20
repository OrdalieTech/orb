package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jstrim"
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
		snippet:    "Execute bash commands (ls, grep, find, etc.)",
		guidelines: []string{"You can inspect PI_* environment variables for current model and session details."},
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
	ForceSystemPrompt  *string
	Sections           ai.SystemPromptSections
}

// ToolPromptContribution replaces one built-in tool's system-prompt
// contribution for a session. A present zero value suppresses the tool's
// contribution entirely — upstream's SDK-created coding tools
// (createCodingTools) drop every contribution except bash's, whose snippet
// and guideline wording differ from the interactive defaults; the extension
// session bridge mirrors that surface through these overrides.
type ToolPromptContribution struct {
	Snippet    string
	Guidelines []string
}

// BuiltInToolPromptData returns the prompt snippets and guidelines contributed
// by built-in tools, in active-tool order.
func BuiltInToolPromptData(toolNames []string) (map[string]string, []string) {
	return builtInToolPromptDataWithOverrides(toolNames, nil)
}

func builtInToolPromptDataWithOverrides(toolNames []string, overrides map[string]ToolPromptContribution) (map[string]string, []string) {
	snippets := make(map[string]string)
	var guidelines []string
	for _, name := range toolNames {
		if override, overridden := overrides[name]; overridden {
			if override.Snippet != "" {
				snippets[name] = override.Snippet
			}
			guidelines = append(guidelines, override.Guidelines...)
			continue
		}
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

// BuildSystemPrompt assembles the system prompt in upstream section order with Orb's D30 product identity.
func BuildSystemPrompt(options SystemPromptOptions) string {
	content, sections := BuildSystemPromptState(options)
	return ai.SystemMessageText(&ai.SystemMessage{Content: content, Sections: sections})
}

// BuildSystemPromptState returns the structured transcript state for options.
func BuildSystemPromptState(options SystemPromptOptions) (string, ai.SystemPromptSections) {
	if options.ForceSystemPrompt != nil {
		return *options.ForceSystemPrompt, nil
	}
	return "", BuildSystemPromptSections(options)
}

// BuildSystemPromptSections builds independently replaceable, ordered prompt sections.
func BuildSystemPromptSections(options SystemPromptOptions) ai.SystemPromptSections {
	promptCWD := strings.ReplaceAll(options.CWD, `\`, "/")
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
		normalized := strings.TrimFunc(guideline, jstrim.IsSpace)
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

	sections := ai.SystemPromptSections{}
	addSection := func(name, text string) {
		value := text
		sections = append(sections, ai.SystemPromptSection{Name: name, Text: &value})
	}
	if options.CustomPrompt != nil && *options.CustomPrompt != "" {
		addSection("preamble", *options.CustomPrompt)
	} else {
		addSection("preamble", "You are an expert problem-solving assistant operating inside Orb, a general-purpose agent harness for work and software development. You help users investigate, plan, create, and complete tasks using the available tools, including working with files, executing commands, and editing code or documents.")
		addSection("tools", wrapPromptSection("tools", toolsList+"\n\nIn addition to the tools above, you may have access to other custom tools depending on the project."))
		addSection("rules", wrapPromptSection("rules", strings.Join(formattedGuidelines, "\n")))
		packageDir := resolvePromptPackageDir(options.PackageDir)
		readmePath := filepath.Join(packageDir, "README.md")
		docsPath := filepath.Join(packageDir, "docs")
		examplesPath := filepath.Join(packageDir, "examples")
		docs := fmt.Sprintf(`Orb documentation (read only when the user asks about Orb itself, its SDK, extensions, themes, skills, or TUI):
- Main documentation: %s
- Additional docs: %s
- Examples: %s (extensions, custom tools, SDK)
- When reading Orb docs or examples, resolve docs/... under Additional docs and examples/... under Examples, not the current working directory
- When asked about: extensions (docs/extensions.md, examples/extensions/), themes (docs/themes.md), skills (docs/skills.md), prompt templates (docs/prompt-templates.md), TUI components (docs/tui.md), keybindings (docs/keybindings.md), SDK integrations (docs/sdk.md), custom providers (docs/custom-provider.md), adding models (docs/models.md), pi packages (docs/packages.md), environment variables (docs/environment-variables.md)
- When working on Orb topics, read the docs and examples, and follow .md cross-references before implementing
- Always read Orb documentation files completely and follow links to related docs (e.g., tui.md for TUI API details)`, readmePath, docsPath, examplesPath)
		addSection("docs", wrapPromptSection("docs", docs))
	}
	if options.AppendSystemPrompt != nil && *options.AppendSystemPrompt != "" {
		addSection("addendum", wrapPromptSection("addendum", *options.AppendSystemPrompt))
	}
	if len(options.ContextFiles) > 0 {
		parts := []string{"Project-specific instructions and guidelines:"}
		for _, file := range options.ContextFiles {
			parts = append(parts, `<project_instructions path="`+file.Path+`">`+"\n"+file.Content+"\n</project_instructions>")
		}
		addSection("project_context", wrapPromptSection("project_context", strings.Join(parts, "\n\n")))
	}
	if (slices.Contains(tools, "read") || slices.Contains(tools, "bash")) && len(options.Skills) > 0 {
		if skills := strings.TrimSpace(FormatSkillsForPrompt(options.Skills)); skills != "" {
			addSection("skills", wrapPromptSection("skills", skills))
		}
	}
	addSection("cwd", wrapPromptSection("cwd", promptCWD))
	for _, section := range options.Sections {
		if section.Text != nil && *section.Text != "" {
			text := *section.Text
			if section.Name != "preamble" {
				text = wrapPromptSection(section.Name, text)
			}
			replaced := false
			for index := range sections {
				if sections[index].Name == section.Name {
					sections[index].Text = &text
					replaced = true
					break
				}
			}
			if !replaced {
				addSection(section.Name, text)
			}
		}
	}
	return sections
}

func wrapPromptSection(name, content string) string {
	return "<" + name + ">\n" + content + "\n</" + name + ">"
}

func DiffSystemPromptSections(previous, current ai.SystemPromptSections) ai.SystemPromptSections {
	previousByName := make(map[string]*string, len(previous))
	currentNames := make(map[string]struct{}, len(current))
	for _, section := range previous {
		previousByName[section.Name] = section.Text
	}
	patch := ai.SystemPromptSections{}
	for _, section := range current {
		currentNames[section.Name] = struct{}{}
		before, exists := previousByName[section.Name]
		if !exists || before == nil || section.Text == nil || *before != *section.Text {
			patch = append(patch, section)
		}
	}
	for _, section := range previous {
		if _, exists := currentNames[section.Name]; !exists {
			patch = append(patch, ai.SystemPromptSection{Name: section.Name})
		}
	}
	if len(patch) == 0 {
		return nil
	}
	return patch
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
