package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/tui"
)

var backgroundTokens = map[string]bool{
	"selectedBg": true, "userMessageBg": true, "customMessageBg": true,
	"toolPendingBg": true, "toolSuccessBg": true, "toolErrorBg": true,
}

var requiredColors = []string{
	"accent", "border", "borderAccent", "borderMuted", "success", "error", "warning", "muted", "dim", "text", "thinkingText",
	"selectedBg", "userMessageBg", "userMessageText", "customMessageBg", "customMessageText", "customMessageLabel", "toolPendingBg", "toolSuccessBg", "toolErrorBg", "toolTitle", "toolOutput",
	"mdHeading", "mdLink", "mdLinkUrl", "mdCode", "mdCodeBlock", "mdCodeBlockBorder", "mdQuote", "mdQuoteBorder", "mdHr", "mdListBullet",
	"toolDiffAdded", "toolDiffRemoved", "toolDiffContext", "syntaxComment", "syntaxKeyword", "syntaxFunction", "syntaxVariable", "syntaxString", "syntaxNumber", "syntaxType", "syntaxOperator", "syntaxPunctuation",
	"thinkingOff", "thinkingMinimal", "thinkingLow", "thinkingMedium", "thinkingHigh", "thinkingXhigh", "bashMode",
}

type document struct {
	Schema string                `json:"$schema"`
	Name   string                `json:"name"`
	Vars   map[string]ColorValue `json:"vars"`
	Colors map[string]ColorValue `json:"colors"`
	Export struct {
		PageBG ColorValue `json:"pageBg"`
		CardBG ColorValue `json:"cardBg"`
		InfoBG ColorValue `json:"infoBg"`
	} `json:"export"`
}

type Theme struct {
	Name       string
	SourcePath string
	SourceInfo *extensions.SourceInfo
	mode       ColorMode
	foreground map[string]string
	background map[string]string
	resolved   map[string]resolvedColor
	export     map[string]resolvedColor
}

func Parse(label string, data []byte, mode ColorMode) (*Theme, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var source document
	if err := decoder.Decode(&source); err != nil {
		return nil, fmt.Errorf("failed to parse theme %s: %w", label, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("failed to parse theme %s: multiple JSON values", label)
		}
		return nil, fmt.Errorf("failed to parse theme %s: %w", label, err)
	}
	if strings.Contains(source.Name, "/") {
		return nil, fmt.Errorf("invalid theme name %q: theme names cannot contain / because it is reserved for automatic light/dark theme settings", source.Name)
	}
	missing := make([]string, 0)
	for _, name := range requiredColors {
		if _, ok := source.Colors[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("invalid theme %q: missing required color tokens: %s", label, strings.Join(missing, ", "))
	}
	if _, ok := source.Colors["thinkingMax"]; !ok {
		source.Colors["thinkingMax"] = source.Colors["thinkingXhigh"]
	}
	if mode == "" {
		mode = DetectColorMode(nil)
	}
	theme := &Theme{Name: source.Name, mode: mode, foreground: map[string]string{}, background: map[string]string{}, resolved: map[string]resolvedColor{}, export: map[string]resolvedColor{}}
	for name, value := range source.Colors {
		resolved, err := value.resolve(source.Vars, map[string]bool{})
		if err != nil {
			return nil, fmt.Errorf("theme %s color %s: %w", label, name, err)
		}
		theme.resolved[name] = resolved
		if backgroundTokens[name] {
			theme.background[name], err = resolved.background(mode)
		} else {
			theme.foreground[name], err = resolved.foreground(mode)
		}
		if err != nil {
			return nil, fmt.Errorf("theme %s color %s: %w", label, name, err)
		}
	}
	for name, value := range map[string]ColorValue{"pageBg": source.Export.PageBG, "cardBg": source.Export.CardBG, "infoBg": source.Export.InfoBG} {
		if value.String == nil && value.Index == nil {
			continue
		}
		resolved, err := value.resolve(source.Vars, map[string]bool{})
		if err != nil {
			return nil, fmt.Errorf("theme %s export %s: %w", label, name, err)
		}
		theme.export[name] = resolved
	}
	return theme, nil
}

func (theme *Theme) ColorMode() ColorMode { return theme.mode }

func (theme *Theme) ForegroundANSI(name string) (string, error) {
	value, ok := theme.foreground[name]
	if !ok {
		return "", fmt.Errorf("unknown theme color: %s", name)
	}
	return value, nil
}

func (theme *Theme) BackgroundANSI(name string) (string, error) {
	value, ok := theme.background[name]
	if !ok {
		return "", fmt.Errorf("unknown theme background color: %s", name)
	}
	return value, nil
}

func (theme *Theme) Foreground(name, value string) string {
	prefix, err := theme.ForegroundANSI(name)
	if err != nil {
		panic(err)
	}
	return prefix + value + "\x1b[39m"
}

func (theme *Theme) Background(name, value string) string {
	prefix, err := theme.BackgroundANSI(name)
	if err != nil {
		panic(err)
	}
	return prefix + value + "\x1b[49m"
}

func Bold(value string) string          { return "\x1b[1m" + value + "\x1b[22m" }
func Italic(value string) string        { return "\x1b[3m" + value + "\x1b[23m" }
func Underline(value string) string     { return "\x1b[4m" + value + "\x1b[24m" }
func Inverse(value string) string       { return "\x1b[7m" + value + "\x1b[27m" }
func Strikethrough(value string) string { return "\x1b[9m" + value + "\x1b[29m" }

func (theme *Theme) Markdown(codeBlockIndent string) tui.MarkdownTheme {
	style := func(name string) tui.StyleFunc {
		return func(value string) string { return theme.Foreground(name, value) }
	}
	result := tui.MarkdownTheme{
		Heading: style("mdHeading"), Link: style("mdLink"), LinkURL: style("mdLinkUrl"), Code: style("mdCode"),
		CodeBlock: style("mdCodeBlock"), CodeBlockBorder: style("mdCodeBlockBorder"), Quote: style("mdQuote"),
		QuoteBorder: style("mdQuoteBorder"), HorizontalRule: style("mdHr"), ListBullet: style("mdListBullet"),
		Bold: Bold, Italic: Italic, Underline: Underline, Strikethrough: Strikethrough,
		HighlightCode:   func(code, language string) []string { return Highlight(code, language, theme) },
		CodeBlockIndent: codeBlockIndent,
	}
	if result.CodeBlockIndent == "" {
		result.CodeBlockIndent = "  "
	}
	return result
}

func (theme *Theme) ResolvedColors(light bool) map[string]string {
	defaultText := "#e5e5e7"
	if light {
		defaultText = "#000000"
	}
	result := make(map[string]string, len(theme.resolved))
	for name, color := range theme.resolved {
		value, err := color.hex(defaultText)
		if err == nil {
			result[name] = value
		}
	}
	return result
}

// ─────────────────────────────────────────────────────────────
// Package-level current-theme accessors
// ─────────────────────────────────────────────────────────────

var (
	currentMu         sync.RWMutex
	currentTheme      *Theme
	currentRegistry   *Registry
	currentController *Controller
)

func SetCurrent(t *Theme) { currentMu.Lock(); currentTheme = t; currentMu.Unlock() }
func Current() *Theme     { currentMu.RLock(); defer currentMu.RUnlock(); return currentTheme }

// Initialize installs the production registry/controller used by package-level
// render helpers and extension theme APIs.
func Initialize(registry *Registry, setting string, terminal TerminalTheme, onChange func()) *Controller {
	controller := NewController(registry, setting, terminal, onChange)
	currentMu.Lock()
	currentRegistry, currentController = registry, controller
	currentTheme = controller.Current()
	currentMu.Unlock()
	return controller
}

func FG(color, text string) string {
	t := Current()
	if t == nil {
		return text
	}
	return t.Foreground(color, text)
}

func BG(color, text string) string {
	t := Current()
	if t == nil {
		return text
	}
	return t.Background(color, text)
}

func FGANSI(color string) string {
	t := Current()
	if t == nil {
		return ""
	}
	v, _ := t.ForegroundANSI(color)
	return v
}

func BGANSI(color string) string {
	t := Current()
	if t == nil {
		return ""
	}
	v, _ := t.BackgroundANSI(color)
	return v
}

func ColorModeGlobal() string {
	t := Current()
	if t == nil {
		return ""
	}
	return string(t.ColorMode())
}

func MarkdownTheme() tui.MarkdownTheme {
	t := Current()
	if t == nil {
		return tui.MarkdownTheme{}
	}
	return t.Markdown("")
}

func EditorTheme() tui.EditorTheme {
	return tui.EditorTheme{
		BorderColor: func(s string) string { return FG("borderMuted", s) },
	}
}

func ThinkingBorderColor(level agent.ThinkingLevel) func(string) string {
	key := "thinkingMedium"
	switch level {
	case "off":
		key = "thinkingOff"
	case "minimal":
		key = "thinkingMinimal"
	case "low":
		key = "thinkingLow"
	case "medium":
		key = "thinkingMedium"
	case "high":
		key = "thinkingHigh"
	case "xhigh":
		key = "thinkingXhigh"
	case "max":
		key = "thinkingXhigh"
	}
	return func(s string) string { return FG(key, s) }
}

func BashModeBorderColor() func(string) string {
	return func(s string) string { return FG("bashMode", s) }
}

func GetAllThemes() []extensions.ThemeInfo {
	currentMu.RLock()
	registry := currentRegistry
	currentMu.RUnlock()
	if registry == nil {
		return []extensions.ThemeInfo{}
	}
	result := make([]extensions.ThemeInfo, 0)
	for _, name := range registry.Available() {
		value, _ := registry.Get(name)
		var path *string
		if value != nil && value.SourcePath != "" {
			copy := value.SourcePath
			path = &copy
		}
		result = append(result, extensions.ThemeInfo{Name: name, Path: path})
	}
	return result
}

func SetTheme(name string) error {
	currentMu.RLock()
	controller := currentController
	currentMu.RUnlock()
	if controller == nil {
		return fmt.Errorf("theme controller is not initialized")
	}
	return controller.Set(name)
}

func GetTheme(name string) *Theme {
	currentMu.RLock()
	registry := currentRegistry
	currentMu.RUnlock()
	if registry == nil {
		return nil
	}
	value, _ := registry.Get(name)
	return value
}

func (theme *Theme) ExportColors() map[string]string {
	result := map[string]string{}
	for name, color := range theme.export {
		value, err := color.hex("")
		if err == nil && value != "" {
			result[name] = value
		}
	}
	return result
}
