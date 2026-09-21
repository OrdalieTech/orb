package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/tui"
)

var backgroundTokens = map[string]bool{
	"selectedBg": true, "scrollbarThumb": true, "searchMatchBg": true, "userMessageBg": true, "customMessageBg": true,
	"toolPendingBg": true, "toolSuccessBg": true, "toolErrorBg": true,
	"diffAddedBg": true, "diffRemovedBg": true, "diffGutterBg": true, "modalBackdropBg": true,
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
	terminalPalette *atomic.Pointer[Theme]
	Name            string
	SourcePath      string
	SourceInfo      *extensions.SourceInfo
	mode            ColorMode
	foreground      map[string]string
	background      map[string]string
	resolved        map[string]resolvedColor
	export          map[string]resolvedColor
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
	if _, ok := source.Colors["scrollbarThumb"]; !ok {
		source.Colors["scrollbarThumb"] = source.Colors["selectedBg"]
	}
	if _, ok := source.Colors["searchMatchBg"]; !ok {
		source.Colors["searchMatchBg"] = source.Colors["selectedBg"]
	}
	if _, ok := source.Colors["searchMatchText"]; !ok {
		source.Colors["searchMatchText"] = source.Colors["text"]
	}
	// Diff tint roles are optional so pre-existing user themes keep parsing;
	// the tool-band backgrounds are the closest semantic stand-ins.
	if _, ok := source.Colors["diffAddedBg"]; !ok {
		source.Colors["diffAddedBg"] = source.Colors["toolSuccessBg"]
	}
	if _, ok := source.Colors["diffRemovedBg"]; !ok {
		source.Colors["diffRemovedBg"] = source.Colors["toolErrorBg"]
	}
	if _, ok := source.Colors["diffGutterBg"]; !ok {
		source.Colors["diffGutterBg"] = source.Colors["toolPendingBg"]
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

func terminalTheme(mode ColorMode) *Theme {
	theme := &Theme{terminalPalette: &atomic.Pointer[Theme]{}, Name: "terminal", mode: mode, foreground: map[string]string{}, background: map[string]string{}, resolved: map[string]resolvedColor{}}
	colors := map[string]int{
		"accent": 6, "borderAccent": 6, "mdHeading": 6, "mdLink": 6, "mdCode": 6, "syntaxFunction": 6, "thinkingMedium": 6, "thinkingHigh": 5, "thinkingXhigh": 5, "thinkingMax": 5,
		"success": 2, "toolDiffAdded": 2, "syntaxString": 2,
		"error": 1, "toolDiffRemoved": 1,
		"warning": 3, "syntaxNumber": 3,
		"customMessageLabel": 5, "syntaxKeyword": 5,
	}
	for _, name := range append(append([]string{}, requiredColors...), "thinkingMax", "searchMatchText", "scrollbarThumb", "searchMatchBg", "diffAddedBg", "diffRemovedBg", "diffGutterBg", "modalBackdropBg", "modalBackdropText") {
		value := ""
		color := resolvedColor{text: &value}
		if index, ok := colors[name]; ok {
			color = resolvedColor{index: &index}
		}
		theme.resolved[name] = color
		if backgroundTokens[name] {
			theme.background[name], _ = color.background(mode)
		} else {
			theme.foreground[name], _ = color.foreground(mode)
		}
	}
	for _, name := range []string{"selectedBg", "searchMatchBg", "scrollbarThumb"} {
		theme.background[name] = "\x1b[4m"
	}
	return theme
}

// SetTerminalBackground swaps an immutable palette so existing render closures
// adopt terminal appearance changes without rebuilding conversation components.
func (theme *Theme) SetTerminalBackground(background tui.RgbColor) {
	if theme.terminalPalette == nil {
		return
	}
	next := terminalTheme(theme.mode)
	bg := fmt.Sprintf("#%02x%02x%02x", background.R, background.G, background.B)
	light := luminance(background.R, background.G, background.B) > .179
	ink, accent, purple, green, red, amber := "#eeeeee", "#70c9bf", "#c4a7e7", "#91c789", "#ed9993", "#dfba73"
	if light {
		ink, accent, purple, green, red, amber = "#202428", "#087f83", "#8552a0", "#387348", "#b04040", "#916018"
	}
	blend := func(a, b string, amount float64) string {
		ar, ag, ab, _ := parseHex(a)
		br, bg, bb, _ := parseHex(b)
		return fmt.Sprintf("#%02x%02x%02x", int(float64(ar)*(1-amount)+float64(br)*amount), int(float64(ag)*(1-amount)+float64(bg)*amount), int(float64(ab)*(1-amount)+float64(bb)*amount))
	}
	accent, purple = blend(accent, ink, .4), blend(purple, ink, .3)
	panel, selected := blend(bg, ink, .045), blend(bg, ink, .10)
	backdrop := blend(bg, "#000000", .14)
	set := func(names, value string) {
		for _, name := range strings.Fields(names) {
			color := resolvedColor{text: &value}
			next.resolved[name] = color
			if backgroundTokens[name] {
				next.background[name], _ = color.background(theme.mode)
			} else {
				next.foreground[name], _ = color.foreground(theme.mode)
			}
		}
	}
	readable := func(value string) string {
		for range 32 {
			minimum := 21.0
			for _, surface := range []string{bg, panel, selected} {
				a, b := luminanceHex(value), luminanceHex(surface)
				if a < b {
					a, b = b, a
				}
				minimum = min(minimum, (a+.05)/(b+.05))
			}
			if minimum >= 4.5 {
				break
			}
			value = blend(value, ink, .12)
		}
		return value
	}
	set("accent borderAccent mdHeading mdLink mdCode syntaxFunction syntaxType thinkingLow thinkingMedium", readable(accent))
	set("customMessageLabel syntaxKeyword", readable(purple))
	set("success toolDiffAdded syntaxString", readable(green))
	set("error toolDiffRemoved", readable(red))
	set("warning syntaxNumber bashMode", readable(amber))
	set("muted dim border borderMuted thinkingText syntaxComment mdLinkUrl mdCodeBlockBorder mdQuote mdQuoteBorder mdHr toolDiffContext thinkingOff thinkingMinimal thinkingHigh thinkingXhigh thinkingMax", readable(blend(bg, ink, .65)))
	set("toolPendingBg userMessageBg customMessageBg", panel)
	set("selectedBg searchMatchBg scrollbarThumb", selected)
	set("toolSuccessBg diffAddedBg", blend(bg, green, .09))
	set("toolErrorBg diffRemovedBg", blend(bg, red, .09))
	set("diffGutterBg", bg)
	set("modalBackdropBg", backdrop)
	set("modalBackdropText", blend(backdrop, ink, .38))
	next.export = map[string]resolvedColor{"pageBg": {text: &bg}, "cardBg": {text: &panel}, "infoBg": {text: &panel}}
	theme.terminalPalette.Store(next)
}

func (theme *Theme) ColorMode() ColorMode { return theme.mode }

func (theme *Theme) ForegroundANSI(name string) (string, error) {
	if theme.terminalPalette != nil {
		if palette := theme.terminalPalette.Load(); palette != nil {
			theme = palette
		}
	}
	value, ok := theme.foreground[name]
	if !ok {
		return "", fmt.Errorf("unknown theme color: %s", name)
	}
	return value, nil
}

func (theme *Theme) BackgroundANSI(name string) (string, error) {
	if theme.terminalPalette != nil {
		if palette := theme.terminalPalette.Load(); palette != nil {
			theme = palette
		}
	}
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
	if prefix == "\x1b[4m" {
		return prefix + tui.ReopenAfterReset(prefix, value) + "\x1b[24m"
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
	if theme.terminalPalette != nil {
		if palette := theme.terminalPalette.Load(); palette != nil {
			theme = palette
		}
	}
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
		Selection:   func(s string) string { return BG("selectedBg", FG("text", s)) },
		BorderColor: func(s string) string { return FG("borderMuted", s) },
		SelectList: tui.SelectListTheme{
			SelectedPrefix: func(s string) string { return FG("accent", Bold(s)) },
			SelectedText:   func(s string) string { return BG("selectedBg", FG("accent", s)) },
			Description:    func(s string) string { return FG("muted", s) },
			ScrollInfo:     func(s string) string { return FG("dim", s) },
			NoMatch:        func(s string) string { return FG("warning", s) },
		},
	}
}

func ThinkingBorderColor(level engine.ThinkingLevel) func(string) string {
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
	if theme.terminalPalette != nil {
		if palette := theme.terminalPalette.Load(); palette != nil {
			theme = palette
		}
	}
	result := map[string]string{}
	for name, color := range theme.export {
		value, err := color.hex("")
		if err == nil && value != "" {
			result[name] = value
		}
	}
	return result
}
