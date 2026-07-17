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
	Message string
	Path    string
}

type ResourceOptions struct {
	CWD            string
	AgentDir       string
	ProjectTrusted *bool
	NoContextFiles bool
	SystemPrompt   *string
	// Nil means discover APPEND_SYSTEM.md; a non-nil empty slice disables discovery.
	AppendSystemPrompt []string
}

type Resources struct {
	ContextFiles       []ContextFile
	SystemPrompt       *string
	AppendSystemPrompt []string
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
	return resources
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
