package codingagent

import (
	"fmt"
	"os"
	"strings"
)

type SlashCommandSource string

const (
	SlashCommandExtension SlashCommandSource = "extension"
	SlashCommandPrompt    SlashCommandSource = "prompt"
	SlashCommandSkill     SlashCommandSource = "skill"
)

type SlashCommandInfo struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Source      SlashCommandSource `json:"source"`
	SourceInfo  SourceInfo         `json:"sourceInfo"`
}

type BuiltinSlashCommand struct {
	Name         string
	Description  string
	ArgumentHint string
}

var BuiltinSlashCommands = []BuiltinSlashCommand{
	{Name: "settings", Description: "Open settings menu"},
	{Name: "model", Description: "Select model (opens selector UI)", ArgumentHint: "<provider/model>"},
	{Name: "scoped-models", Description: "Enable/disable models for Ctrl+P cycling"},
	{Name: "export", Description: "Export session (HTML default, or specify path: .html/.jsonl)"},
	{Name: "import", Description: "Import and resume a session from a JSONL file"},
	{Name: "share", Description: "Export session as local HTML"},
	{Name: "copy", Description: "Copy last agent message to clipboard"},
	{Name: "name", Description: "Set session display name"},
	{Name: "session", Description: "Show session info and stats"},
	{Name: "changelog", Description: "Show changelog entries"},
	{Name: "hotkeys", Description: "Show all keyboard shortcuts"},
	{Name: "fork", Description: "Create a new fork from a previous user message"},
	{Name: "clone", Description: "Duplicate the current session at the current position"},
	{Name: "tree", Description: "Navigate session tree (switch branches)"},
	{Name: "trust", Description: "Save project trust decision for future sessions"},
	{Name: "login", Description: "Configure provider authentication", ArgumentHint: "<provider>"},
	{Name: "logout", Description: "Remove provider authentication"},
	{Name: "new", Description: "Start a new session"},
	{Name: "compact", Description: "Manually compact the session context"},
	{Name: "resume", Description: "Resume a different session"},
	{Name: "reload", Description: "Reload keybindings, extensions, skills, prompts, themes, and context files"},
	{Name: "quit", Description: "Quit pi"},
}

type InputAction string

const (
	InputPass      InputAction = "pass"
	InputHandled   InputAction = "handled"
	InputTransform InputAction = "transform"
)

type InputResult struct {
	Action InputAction
	Text   string
}

// SlashResolver preserves the extension, input, skill, then template resolution order.
type SlashResolver struct {
	Skills            []Skill
	PromptTemplates   []PromptTemplate
	ExtensionCommands []SlashCommandInfo
	ExecuteExtension  func(name, args string) (bool, error)
	InterceptInput    func(text string) (InputResult, error)
	OnError           func(error)
}

func (resolver *SlashResolver) report(err error) {
	if err != nil && resolver != nil && resolver.OnError != nil {
		resolver.OnError(err)
	}
}

func splitExtensionCommand(text string) (name, args string) {
	space := strings.IndexByte(text, ' ')
	if space < 0 {
		return strings.TrimPrefix(text, "/"), ""
	}
	return strings.TrimPrefix(text[:space], "/"), text[space+1:]
}

// ResolvePrompt applies every prompt-stage resolver and reports whether an extension consumed input.
func (resolver *SlashResolver) ResolvePrompt(text string) (string, bool) {
	if resolver == nil {
		return text, false
	}
	if strings.HasPrefix(text, "/") && resolver.ExecuteExtension != nil {
		name, args := splitExtensionCommand(text)
		handled, err := resolver.ExecuteExtension(name, args)
		resolver.report(err)
		if handled {
			return text, true
		}
	}
	current := text
	if resolver.InterceptInput != nil {
		result, err := resolver.InterceptInput(current)
		resolver.report(err)
		if err == nil {
			switch result.Action {
			case InputHandled:
				return current, true
			case InputTransform:
				current = result.Text
			}
		}
	}
	return resolver.Expand(current), false
}

// Expand applies skill expansion before prompt-template expansion.
func (resolver *SlashResolver) Expand(text string) string {
	if resolver == nil {
		return text
	}
	expanded, err := ExpandSkillCommand(text, resolver.Skills)
	resolver.report(err)
	return ExpandPromptTemplate(expanded, resolver.PromptTemplates)
}

// ExpandQueued rejects extension commands because their handlers must run synchronously through Prompt.
func (resolver *SlashResolver) ExpandQueued(text string) (string, error) {
	if resolver == nil {
		return text, nil
	}
	if strings.HasPrefix(text, "/") {
		name, _ := splitExtensionCommand(text)
		for _, command := range resolver.ExtensionCommands {
			if command.Name == name {
				return "", fmt.Errorf("extension command %q cannot be queued; use prompt() or execute the command when not streaming", "/"+name)
			}
		}
	}
	return resolver.Expand(text), nil
}

// ExpandSkillCommand reads a skill on invocation so edits are visible without resource reload.
func ExpandSkillCommand(text string, skills []Skill) (string, error) {
	if !strings.HasPrefix(text, "/skill:") {
		return text, nil
	}
	space := strings.IndexByte(text, ' ')
	name := ""
	args := ""
	if space < 0 {
		name = text[7:]
	} else {
		name = text[7:space]
		args = strings.TrimFunc(text[space+1:], isJSTrimSpace)
	}
	for _, skill := range skills {
		if skill.Name != name {
			continue
		}
		contents, err := os.ReadFile(skill.FilePath)
		if err != nil {
			return text, fmt.Errorf("expand skill %s: %w", skill.Name, err)
		}
		parsed, err := parseResourceFrontmatter(decodeResourceUTF8(contents))
		if err != nil {
			return text, fmt.Errorf("expand skill %s: %w", skill.Name, err)
		}
		body := strings.TrimFunc(parsed.Body, isJSTrimSpace)
		block := fmt.Sprintf("<skill name=\"%s\" location=\"%s\">\nReferences are relative to %s.\n\n%s\n</skill>", skill.Name, skill.FilePath, skill.BaseDir, body)
		if args != "" {
			block += "\n\n" + args
		}
		return block, nil
	}
	return text, nil
}

// FormatSkillInvocation formats an already-loaded harness skill.
func FormatSkillInvocation(skill Skill, additionalInstructions string) string {
	block := fmt.Sprintf("<skill name=\"%s\" location=\"%s\">\nReferences are relative to %s.\n\n%s\n</skill>", skill.Name, skill.FilePath, skill.BaseDir, skill.Content)
	if additionalInstructions != "" {
		return block + "\n\n" + additionalInstructions
	}
	return block
}

// Commands returns the RPC command-list order: extension, prompt, then skill.
func (resolver *SlashResolver) Commands(enableSkillCommands bool) []SlashCommandInfo {
	if resolver == nil {
		return []SlashCommandInfo{}
	}
	commands := append([]SlashCommandInfo(nil), resolver.ExtensionCommands...)
	for _, template := range resolver.PromptTemplates {
		commands = append(commands, SlashCommandInfo{
			Name: template.Name, Description: template.Description, Source: SlashCommandPrompt,
			SourceInfo: template.SourceInfo,
		})
	}
	if enableSkillCommands {
		for _, skill := range resolver.Skills {
			commands = append(commands, SlashCommandInfo{
				Name: "skill:" + skill.Name, Description: skill.Description, Source: SlashCommandSkill,
				SourceInfo: skill.SourceInfo,
			})
		}
	}
	return commands
}
