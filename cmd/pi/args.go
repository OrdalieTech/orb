package main

import (
	"strings"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

var validThinkingLevels = map[string]struct{}{
	"off": {}, "minimal": {}, "low": {}, "medium": {}, "high": {}, "xhigh": {}, "max": {},
}

type CLIDiagnostic struct {
	Type    string
	Message string
}

type CLIUnknownFlag struct {
	Name  string
	Value *string
}

type CLIArgs struct {
	Command            string
	CommandArgs        []string
	Provider           *string
	Model              *string
	Models             []string
	ListModels         *string
	APIKey             *string
	SystemPrompt       *string
	AppendSystemPrompt []string
	Thinking           *string
	Continue           bool
	Resume             bool
	Help               bool
	Version            bool
	Name               *string
	NoSession          bool
	Session            *string
	SessionID          *string
	Fork               *string
	SessionDir         *string
	Tools              []string
	ExcludeTools       []string
	NoTools            bool
	NoBuiltinTools     bool
	NoExtensions       bool
	Extensions         []string
	Print              bool
	Mode               string
	Export             *string
	NoContextFiles     bool
	Skills             []string
	NoSkills           bool
	PromptTemplates    []string
	NoPromptTemplates  bool
	ProjectTrusted     *bool
	Messages           []string
	FileArgs           []string
	UnknownFlags       []CLIUnknownFlag
	Diagnostics        []CLIDiagnostic
	RestoredModel      bool
	allowNoModel       bool
	useUnknownModel    bool
	extensionsLoaded   bool
	extensionRegistry  *extensions.Registry
	extensionWarnings  []string
}

// ParseArgs follows upstream's sequential CLI parsing rules.
func ParseArgs(argv []string) CLIArgs {
	result := CLIArgs{
		CommandArgs:  []string{},
		Messages:     []string{},
		FileArgs:     []string{},
		UnknownFlags: []CLIUnknownFlag{},
		Diagnostics:  []CLIDiagnostic{},
	}
	if len(argv) > 0 && (argv[0] == "login" || argv[0] == "logout") {
		result.Command = argv[0]
		result.CommandArgs = append(result.CommandArgs, argv[1:]...)
		return result
	}
	for index := 0; index < len(argv); index++ {
		argument := argv[index]
		switch {
		case argument == "--help" || argument == "-h":
			result.Help = true
		case argument == "--version" || argument == "-v":
			result.Version = true
		case argument == "--continue" || argument == "-c":
			result.Continue = true
		case argument == "--resume" || argument == "-r":
			result.Resume = true
		case argument == "--mode" && index+1 < len(argv):
			index++
			if argv[index] == "text" || argv[index] == "json" || argv[index] == "rpc" {
				result.Mode = argv[index]
			}
		case argument == "--provider" && index+1 < len(argv):
			index++
			result.Provider = stringValue(argv[index])
		case argument == "--model" && index+1 < len(argv):
			index++
			result.Model = stringValue(argv[index])
		case argument == "--models" && index+1 < len(argv):
			index++
			result.Models = parseModelList(argv[index])
		case argument == "--list-models":
			search := ""
			if index+1 < len(argv) && !strings.HasPrefix(argv[index+1], "-") && !strings.HasPrefix(argv[index+1], "@") {
				index++
				search = argv[index]
			}
			result.ListModels = stringValue(search)
		case argument == "--api-key" && index+1 < len(argv):
			index++
			result.APIKey = stringValue(argv[index])
		case argument == "--system-prompt" && index+1 < len(argv):
			index++
			result.SystemPrompt = stringValue(argv[index])
		case argument == "--append-system-prompt" && index+1 < len(argv):
			index++
			if result.AppendSystemPrompt == nil {
				result.AppendSystemPrompt = make([]string, 0, 1)
			}
			result.AppendSystemPrompt = append(result.AppendSystemPrompt, argv[index])
		case argument == "--name" || argument == "-n":
			if index+1 < len(argv) {
				index++
				result.Name = stringValue(argv[index])
			} else {
				result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{Type: "error", Message: "--name requires a value"})
			}
		case argument == "--no-session":
			result.NoSession = true
		case argument == "--session" && index+1 < len(argv):
			index++
			result.Session = stringValue(argv[index])
		case argument == "--session-id" && index+1 < len(argv):
			index++
			result.SessionID = stringValue(argv[index])
		case argument == "--fork" && index+1 < len(argv):
			index++
			result.Fork = stringValue(argv[index])
		case argument == "--session-dir" && index+1 < len(argv):
			index++
			result.SessionDir = stringValue(argv[index])
		case argument == "--no-tools" || argument == "-nt":
			result.NoTools = true
		case argument == "--no-builtin-tools" || argument == "-nbt":
			result.NoBuiltinTools = true
		case (argument == "--extension" || argument == "-e") && index+1 < len(argv):
			index++
			result.Extensions = append(result.Extensions, argv[index])
		case argument == "--no-extensions" || argument == "-ne":
			result.NoExtensions = true
		case (argument == "--tools" || argument == "-t") && index+1 < len(argv):
			index++
			result.Tools = parseToolList(argv[index])
		case (argument == "--exclude-tools" || argument == "-xt") && index+1 < len(argv):
			index++
			result.ExcludeTools = parseToolList(argv[index])
		case argument == "--thinking" && index+1 < len(argv):
			index++
			level := argv[index]
			if _, valid := validThinkingLevels[level]; valid {
				result.Thinking = stringValue(level)
			} else {
				result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{
					Type:    "warning",
					Message: `Invalid thinking level "` + level + `". Valid values: off, minimal, low, medium, high, xhigh, max`,
				})
			}
		case argument == "--print" || argument == "-p":
			result.Print = true
			if index+1 < len(argv) {
				next := argv[index+1]
				if !strings.HasPrefix(next, "@") && (!strings.HasPrefix(next, "-") || strings.HasPrefix(next, "---")) {
					result.Messages = append(result.Messages, next)
					index++
				}
			}
		case argument == "--export" && index+1 < len(argv):
			index++
			result.Export = stringValue(argv[index])
		case argument == "--no-context-files" || argument == "-nc":
			result.NoContextFiles = true
		case argument == "--skill" && index+1 < len(argv):
			index++
			result.Skills = append(result.Skills, argv[index])
		case argument == "--no-skills" || argument == "-ns":
			result.NoSkills = true
		case argument == "--prompt-template" && index+1 < len(argv):
			index++
			result.PromptTemplates = append(result.PromptTemplates, argv[index])
		case argument == "--no-prompt-templates" || argument == "-np":
			result.NoPromptTemplates = true
		case argument == "--approve" || argument == "-a":
			trusted := true
			result.ProjectTrusted = &trusted
		case argument == "--no-approve" || argument == "-na":
			trusted := false
			result.ProjectTrusted = &trusted
		case strings.HasPrefix(argument, "@"):
			result.FileArgs = append(result.FileArgs, argument[1:])
		case strings.HasPrefix(argument, "--"):
			name, value := parseUnknownLongFlag(argv, &index)
			result.UnknownFlags = setUnknownLongFlag(result.UnknownFlags, CLIUnknownFlag{Name: name, Value: value})
		case strings.HasPrefix(argument, "-"):
			result.Diagnostics = append(result.Diagnostics, CLIDiagnostic{Type: "error", Message: "Unknown option: " + argument})
		default:
			result.Messages = append(result.Messages, argument)
		}
	}
	return result
}

func parseModelList(value string) []string {
	models := strings.Split(value, ",")
	for index := range models {
		models[index] = strings.TrimFunc(models[index], isJSTrimSpace)
	}
	return models
}

func parseUnknownLongFlag(argv []string, index *int) (string, *string) {
	argument := argv[*index]
	if equals := strings.IndexByte(argument, '='); equals >= 0 {
		return argument[2:equals], stringValue(argument[equals+1:])
	}
	name := argument[2:]
	if *index+1 < len(argv) {
		next := argv[*index+1]
		if !strings.HasPrefix(next, "-") && !strings.HasPrefix(next, "@") {
			(*index)++
			return name, stringValue(next)
		}
	}
	return name, nil
}

func setUnknownLongFlag(flags []CLIUnknownFlag, flag CLIUnknownFlag) []CLIUnknownFlag {
	for index := range flags {
		if flags[index].Name == flag.Name {
			flags[index].Value = flag.Value
			return flags
		}
	}
	return append(flags, flag)
}

func parseToolList(value string) []string {
	result := make([]string, 0)
	for _, name := range strings.Split(value, ",") {
		if trimmed := strings.TrimFunc(name, isJSTrimSpace); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func stringValue(value string) *string {
	copy := value
	return &copy
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
