package exporthtml

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/config"
	modetheme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
	"github.com/OrdalieTech/pigo/codingagent/session"
)

//go:embed assets/template.html
var templateHTML string

//go:embed assets/template.css
var templateCSS string

//go:embed assets/template.js
var templateJS string

//go:embed assets/vendor/marked.min.js
var markedJS string

//go:embed assets/vendor/highlight.min.js
var highlightJS string

type Options struct {
	OutputPath   string
	ThemeName    string
	SystemPrompt *string
	Tools        json.RawMessage
	ToolRenderer ToolHTMLRenderer
	Theme        *modetheme.Theme
}

type sessionData struct {
	Header        *session.SessionHeader  `json:"header"`
	Entries       []session.SessionEntry  `json:"entries"`
	LeafID        *string                 `json:"leafId"`
	SystemPrompt  *string                 `json:"systemPrompt,omitempty"`
	Tools         json.RawMessage         `json:"tools,omitempty"`
	RenderedTools *renderedToolCollection `json:"renderedTools,omitempty"`
}

// ExportSession writes a self-contained HTML view of a persisted session.
func ExportSession(manager *session.SessionManager, options Options) (string, error) {
	if manager == nil {
		return "", errors.New("session manager is required")
	}
	sessionFile := manager.GetSessionFile()
	if sessionFile == "" {
		return "", errors.New("Cannot export in-memory session to HTML") //nolint:staticcheck // Upstream error capitalization is observable.
	}
	if _, err := os.Stat(sessionFile); err != nil {
		return "", errors.New("Nothing to export yet - start a conversation first") //nolint:staticcheck // Upstream error capitalization is observable.
	}
	entries := manager.GetEntries()
	data := sessionData{
		Header: manager.GetHeader(), Entries: entries, LeafID: manager.GetLeafID(),
		SystemPrompt: options.SystemPrompt, Tools: options.Tools,
	}
	if options.ToolRenderer != nil {
		data.RenderedTools = preRenderCustomTools(entries, options.ToolRenderer)
	}
	contents, err := generateHTML(data, options.ThemeName, options.Theme)
	if err != nil {
		return "", err
	}
	outputPath, err := normalizePath(options.OutputPath)
	if err != nil {
		return "", err
	}
	if outputPath == "" {
		base := strings.TrimSuffix(filepath.Base(sessionFile), ".jsonl")
		outputPath = "pi-session-" + base + ".html"
	}
	if err := os.WriteFile(outputPath, []byte(contents), 0o666); err != nil {
		return "", err
	}
	return outputPath, nil
}

// ExportFromFile exports an existing session file without runtime state.
func ExportFromFile(inputPath string, options Options) (string, error) {
	resolvedInput, err := resolvePath(inputPath)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(resolvedInput); err != nil {
		return "", fmt.Errorf("File not found: %s", resolvedInput) //nolint:staticcheck // Upstream error capitalization is observable.
	}
	manager, err := session.Open(resolvedInput, "")
	if err != nil {
		return "", err
	}
	if options.OutputPath == "" {
		base := strings.TrimSuffix(filepath.Base(resolvedInput), ".jsonl")
		options.OutputPath = "pi-session-" + base + ".html"
	}
	// File exports have no live agent state or registered runtime renderers.
	options.SystemPrompt = nil
	options.Tools = nil
	options.ToolRenderer = nil
	return ExportSession(manager, options)
}

func generateHTML(data sessionData, themeName string, selectedTheme *modetheme.Theme) (string, error) {
	theme, err := resolveExportTheme(themeName, selectedTheme)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	encoded, err = ai.NormalizeJSONStringifyJSON(encoded)
	if err != nil {
		return "", err
	}
	sessionBase64 := base64.StdEncoding.EncodeToString(encoded)
	css := replaceOnce(templateCSS, "{{THEME_VARS}}", theme.variables)
	css = replaceOnce(css, "{{BODY_BG}}", theme.pageBg)
	css = replaceOnce(css, "{{CONTAINER_BG}}", theme.cardBg)
	css = replaceOnce(css, "{{INFO_BG}}", theme.infoBg)
	html := replaceOnce(templateHTML, "{{CSS}}", css)
	html = replaceOnce(html, "{{JS}}", templateJS)
	html = replaceOnce(html, "{{SESSION_DATA}}", sessionBase64)
	html = replaceOnce(html, "{{MARKED_JS}}", markedJS)
	html = replaceOnce(html, "{{HIGHLIGHT_JS}}", highlightJS)
	return html, nil
}

func replaceOnce(value, old, replacement string) string {
	// Upstream passes asset text as String.replace's replacement string, so its
	// dollar substitutions are observable in the self-contained renderer bytes.
	index := strings.Index(value, old)
	if index < 0 {
		return value
	}
	prefix := value[:index]
	suffix := value[index+len(old):]
	var expanded strings.Builder
	expanded.Grow(len(replacement))
	for offset := 0; offset < len(replacement); offset++ {
		if replacement[offset] != '$' || offset+1 >= len(replacement) {
			expanded.WriteByte(replacement[offset])
			continue
		}
		switch replacement[offset+1] {
		case '$':
			expanded.WriteByte('$')
			offset++
		case '&':
			expanded.WriteString(old)
			offset++
		case '`':
			expanded.WriteString(prefix)
			offset++
		case '\'':
			expanded.WriteString(suffix)
			offset++
		default:
			expanded.WriteByte('$')
		}
	}
	return prefix + expanded.String() + suffix
}

func normalizePath(path string) (string, error) {
	return config.NormalizePath(path)
}

func resolvePath(path string) (string, error) {
	normalized, err := normalizePath(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(normalized)
}
