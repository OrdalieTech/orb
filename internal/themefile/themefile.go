// Package themefile is the pi theme JSON resource format: parsing, validation,
// variable resolution, and path discovery. It is headless: the agent resource
// loader and HTML export read it, and agent/modes/theme renders it.
package themefile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// RequiredColors are the tokens every theme file must define.
var RequiredColors = []string{
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

// Theme is a parsed theme resource with every color reference resolved.
type Theme struct {
	Name       string
	SourcePath string
	Colors     map[string]Color
	Export     map[string]Color
}

// Color is a resolved theme color: a 256-color palette index, or hex text
// where "" means the terminal default.
type Color struct {
	Index *int
	Text  string
}

// Hex renders the color as CSS hex; the terminal default becomes defaultColor.
func (color Color) Hex(defaultColor string) (string, error) {
	if color.Index != nil {
		return ANSI256ToHex(*color.Index), nil
	}
	if color.Text == "" {
		return defaultColor, nil
	}
	_, _, _, err := ParseHex(color.Text)
	return color.Text, err
}

// HexColors renders colors as CSS hex, dropping entries that do not render.
func HexColors(colors map[string]Color, defaultColor string) map[string]string {
	result := make(map[string]string, len(colors))
	for name, color := range colors {
		if value, err := color.Hex(defaultColor); err == nil {
			result[name] = value
		}
	}
	return result
}

// Parse decodes and validates one theme file. label names it in errors.
func Parse(label string, data []byte) (*Theme, error) {
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
	for _, name := range RequiredColors {
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
	theme := &Theme{Name: source.Name, Colors: map[string]Color{}, Export: map[string]Color{}}
	for name, value := range source.Colors {
		resolved, err := value.resolve(source.Vars, map[string]bool{})
		if err == nil && resolved.Index == nil && resolved.Text != "" {
			_, _, _, err = ParseHex(resolved.Text)
		}
		if err != nil {
			return nil, fmt.Errorf("theme %s color %s: %w", label, name, err)
		}
		theme.Colors[name] = resolved
	}
	for name, value := range map[string]ColorValue{"pageBg": source.Export.PageBG, "cardBg": source.Export.CardBG, "infoBg": source.Export.InfoBG} {
		if value.String == nil && value.Index == nil {
			continue
		}
		resolved, err := value.resolve(source.Vars, map[string]bool{})
		if err != nil {
			return nil, fmt.Errorf("theme %s export %s: %w", label, name, err)
		}
		theme.Export[name] = resolved
	}
	return theme, nil
}

// ColorValue is a theme JSON color: a hex string, a variable name, "" for the
// terminal default, or a 256-color index.
type ColorValue struct {
	String *string
	Index  *int
}

func (value *ColorValue) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		value.String, value.Index = &text, nil
		return nil
	}
	var index int
	if err := json.Unmarshal(data, &index); err == nil {
		if index < 0 || index > 255 {
			return fmt.Errorf("256-color index %d is outside 0-255", index)
		}
		value.String, value.Index = nil, &index
		return nil
	}
	return errors.New("color must be a string or an integer from 0 to 255")
}

func (value ColorValue) resolve(variables map[string]ColorValue, visited map[string]bool) (Color, error) {
	if value.Index != nil {
		return Color{Index: value.Index}, nil
	}
	if value.String == nil {
		return Color{}, errors.New("empty color value")
	}
	text := *value.String
	if text == "" || strings.HasPrefix(text, "#") {
		return Color{Text: text}, nil
	}
	if visited[text] {
		return Color{}, fmt.Errorf("circular variable reference detected: %s", text)
	}
	next, ok := variables[text]
	if !ok {
		return Color{}, fmt.Errorf("variable reference not found: %s", text)
	}
	visited[text] = true
	return next.resolve(variables, visited)
}

// ParseHex parses a #rrggbb color.
func ParseHex(value string) (int, int, int, error) {
	if len(value) != 7 || value[0] != '#' {
		return 0, 0, 0, fmt.Errorf("invalid hex color: %s", value)
	}
	parsed, err := strconv.ParseUint(value[1:], 16, 24)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid hex color: %s", value)
	}
	return int(parsed >> 16), int((parsed >> 8) & 255), int(parsed & 255), nil
}

// ANSI256ToHex maps a 256-color palette index to its xterm hex value.
func ANSI256ToHex(index int) string {
	basic := [...]string{"#000000", "#800000", "#008000", "#808000", "#000080", "#800080", "#008080", "#c0c0c0", "#808080", "#ff0000", "#00ff00", "#ffff00", "#0000ff", "#ff00ff", "#00ffff", "#ffffff"}
	if index < 16 {
		return basic[index]
	}
	if index < 232 {
		cube := index - 16
		component := func(value int) int {
			if value == 0 {
				return 0
			}
			return 55 + value*40
		}
		return fmt.Sprintf("#%02x%02x%02x", component(cube/36), component((cube%36)/6), component(cube%6))
	}
	gray := 8 + (index-232)*10
	return fmt.Sprintf("#%02x%02x%02x", gray, gray, gray)
}
