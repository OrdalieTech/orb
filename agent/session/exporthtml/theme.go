package exporthtml

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/internal/jstrim"
	"github.com/OrdalieTech/orb/internal/lazyregexp"
	"github.com/OrdalieTech/orb/internal/themefile"
)

type exportTheme struct {
	variables string
	pageBg    string
	cardBg    string
	infoBg    string
}

// ThemeRef identifies the theme a caller already selected for ThemeName. An
// empty SourcePath means the theme has no file to export from.
type ThemeRef struct {
	Name       string
	SourcePath string
}

func resolveExportTheme(name string, selected *ThemeRef) (exportTheme, error) {
	// The terminal theme takes the terminal's own colors, which a page cannot:
	// it exports as the palette for the terminal's background.
	if name == "" || name == "terminal" {
		name = defaultThemeName(os.Getenv("COLORFGBG"))
	}
	switch name {
	case "dark":
		return exportTheme{darkThemeVariables, "#21252c", "#282c34", "#4e2f1b"}, nil
	case "light":
		return exportTheme{lightThemeVariables, "#efeeee", "#f7f6f6", "#ede3dd"}, nil
	}
	if selected != nil && selected.Name == name {
		if selected.SourcePath == "" {
			return exportTheme{}, fmt.Errorf("Theme %q does not have a source path for export", name) //nolint:staticcheck // Upstream error capitalization is observable.
		}
		data, err := os.ReadFile(selected.SourcePath)
		if err != nil {
			return exportTheme{}, err
		}
		parsed, err := themefile.Parse(selected.SourcePath, data)
		if err != nil {
			return exportTheme{}, err
		}
		return exportThemeFrom(parsed), nil
	}
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return exportTheme{}, err
	}
	data, err := os.ReadFile(filepath.Join(agentDir, "themes", name+".json"))
	if os.IsNotExist(err) {
		return exportTheme{}, fmt.Errorf("Theme not found: %s", name) //nolint:staticcheck // Upstream error capitalization is observable.
	}
	if err != nil {
		return exportTheme{}, err
	}
	parsed, err := themefile.Parse(name, data)
	if err != nil {
		return exportTheme{}, err
	}
	return exportThemeFrom(parsed), nil
}

// exportBackgroundTokens are listed after every foreground token, as
// upstream's theme record keeps them.
var exportBackgroundTokens = map[string]bool{
	"selectedBg": true, "searchMatchBg": true, "userMessageBg": true, "customMessageBg": true,
	"toolPendingBg": true, "toolSuccessBg": true, "toolErrorBg": true,
}

// exportFallbacks are upstream's defaults for optional tokens a file omits.
var exportFallbacks = [][2]string{
	{"scrollbarTrack", "muted"}, {"scrollbarThumb", "text"}, {"thinkingMax", "thinkingXhigh"},
	{"searchMatchBg", "selectedBg"}, {"searchMatchText", "text"},
}

// exportColorOrder lists a theme's tokens as upstream's export does: every
// token the file defines plus omitted fallbacks, foreground before background,
// and tokens left at the terminal default last.
func exportColorOrder(selected *themefile.Theme) ([]string, map[string]themefile.Color) {
	colors := make(map[string]themefile.Color, len(selected.Keys)+len(exportFallbacks))
	keys := append([]string(nil), selected.Keys...)
	for _, key := range keys {
		colors[key] = selected.Colors[key]
	}
	for _, fallback := range exportFallbacks {
		if _, defined := colors[fallback[0]]; !defined {
			keys = append(keys, fallback[0])
			colors[fallback[0]] = colors[fallback[1]]
		}
	}
	var foreground, background, defaultForeground, defaultBackground []string
	for _, key := range keys {
		isDefault := colors[key].Index == nil && colors[key].Text == ""
		switch {
		case exportBackgroundTokens[key] && isDefault:
			defaultBackground = append(defaultBackground, key)
		case exportBackgroundTokens[key]:
			background = append(background, key)
		case isDefault:
			defaultForeground = append(defaultForeground, key)
		default:
			foreground = append(foreground, key)
		}
	}
	return append(append(append(foreground, background...), defaultForeground...), defaultBackground...), colors
}

func exportThemeFrom(selected *themefile.Theme) exportTheme {
	defaultText := "#e5e5e7"
	if selected.Name == "light" {
		defaultText = "#000000"
	}
	order, tokens := exportColorOrder(selected)
	colors := themefile.HexColors(tokens, defaultText)
	backgrounds := deriveExportColors(colors["userMessageBg"])
	for name, value := range themefile.HexColors(selected.Export, "") {
		if value == "" {
			continue
		}
		switch name {
		case "pageBg":
			backgrounds.pageBg = value
		case "cardBg":
			backgrounds.cardBg = value
		case "infoBg":
			backgrounds.infoBg = value
		}
	}
	lines := make([]string, 0, len(colors)+3)
	for _, name := range order {
		if value, ok := colors[name]; ok {
			lines = append(lines, "--"+name+": "+strings.ToLower(value)+";")
		}
	}
	lines = append(lines,
		"--exportPageBg: "+backgrounds.pageBg+";",
		"--exportCardBg: "+backgrounds.cardBg+";",
		"--exportInfoBg: "+backgrounds.infoBg+";",
	)
	backgrounds.variables = strings.Join(lines, "\n      ")
	return backgrounds
}

var rgbColorPattern = lazyregexp.New(`^rgb\s*\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\)$`)

func deriveExportColors(color string) exportTheme {
	r, g, b, ok := parseCSSColor(color)
	if !ok {
		return exportTheme{pageBg: "rgb(24, 24, 30)", cardBg: "rgb(30, 30, 36)", infoBg: "rgb(60, 55, 40)"}
	}
	if relativeLuminance(r, g, b) > 0.5 {
		return exportTheme{
			pageBg: adjustBrightness(r, g, b, 0.96), cardBg: color,
			infoBg: fmt.Sprintf("rgb(%d, %d, %d)", min(255, r+10), min(255, g+5), max(0, b-20)),
		}
	}
	return exportTheme{
		pageBg: adjustBrightness(r, g, b, 0.7), cardBg: adjustBrightness(r, g, b, 0.85),
		infoBg: fmt.Sprintf("rgb(%d, %d, %d)", min(255, r+20), min(255, g+15), b),
	}
}

func parseCSSColor(color string) (int, int, int, bool) {
	if len(color) == 7 && color[0] == '#' {
		value, err := strconv.ParseUint(color[1:], 16, 24)
		if err == nil {
			return int(value >> 16), int(value>>8) & 255, int(value) & 255, true
		}
	}
	match := rgbColorPattern().FindStringSubmatch(color)
	if len(match) == 4 {
		r, errR := strconv.Atoi(match[1])
		g, errG := strconv.Atoi(match[2])
		b, errB := strconv.Atoi(match[3])
		return r, g, b, errR == nil && errG == nil && errB == nil
	}
	return 0, 0, 0, false
}

func adjustBrightness(r, g, b int, factor float64) string {
	adjust := func(value int) int { return min(255, max(0, int(math.Round(float64(value)*factor)))) }
	return fmt.Sprintf("rgb(%d, %d, %d)", adjust(r), adjust(g), adjust(b))
}

func defaultThemeName(colorFgBg string) string {
	parts := strings.Split(colorFgBg, ";")
	for index := len(parts) - 1; index >= 0; index-- {
		colorIndex, ok := parseJSDecimalInteger(strings.TrimFunc(parts[index], jstrim.IsSpace))
		if !ok || colorIndex < 0 || colorIndex > 255 {
			continue
		}
		r, g, b := ansi256RGB(colorIndex)
		if relativeLuminance(r, g, b) >= 0.5 {
			return "light"
		}
		return "dark"
	}
	return "dark"
}

func parseJSDecimalInteger(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	end := 0
	if value[0] == '+' || value[0] == '-' {
		end++
	}
	startDigits := end
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == startDigits {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value[:end], 10, 64)
	if err != nil || int64(int(parsed)) != parsed {
		return 0, false
	}
	return int(parsed), true
}

func ansi256RGB(index int) (int, int, int) {
	basic := [16][3]int{
		{0x00, 0x00, 0x00}, {0x80, 0x00, 0x00}, {0x00, 0x80, 0x00}, {0x80, 0x80, 0x00},
		{0x00, 0x00, 0x80}, {0x80, 0x00, 0x80}, {0x00, 0x80, 0x80}, {0xc0, 0xc0, 0xc0},
		{0x80, 0x80, 0x80}, {0xff, 0x00, 0x00}, {0x00, 0xff, 0x00}, {0xff, 0xff, 0x00},
		{0x00, 0x00, 0xff}, {0xff, 0x00, 0xff}, {0x00, 0xff, 0xff}, {0xff, 0xff, 0xff},
	}
	if index < 16 {
		return basic[index][0], basic[index][1], basic[index][2]
	}
	if index < 232 {
		cube := index - 16
		component := func(value int) int {
			if value == 0 {
				return 0
			}
			return 55 + value*40
		}
		return component(cube / 36), component((cube % 36) / 6), component(cube % 6)
	}
	gray := 8 + (index-232)*10
	return gray, gray, gray
}

func relativeLuminance(r, g, b int) float64 {
	linear := func(channel int) float64 {
		value := float64(channel) / 255
		if value <= 0.03928 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}

const darkThemeVariables = `--accent: #a798d7;
      --border: #5fa8cc;
      --borderAccent: #a08ed5;
      --borderMuted: #768186;
      --success: #68b78d;
      --error: #ea7f81;
      --warning: #cd9a22;
      --muted: #9da5a9;
      --dim: #7e888e;
      --text: #dee0e1;
      --thinkingText: #96a0a4;
      --scrollbarTrack: #484e52;
      --scrollbarThumb: #97a0a5;
      --searchMatchText: #9da5a9;
      --userMessageText: #dee0e1;
      --customMessageText: #9da5a9;
      --customMessageLabel: #a798d7;
      --toolTitle: #dee0e1;
      --toolOutput: #9da5a9;
      --mdHeading: #cd9a22;
      --mdLink: #69add0;
      --mdLinkUrl: #9da5a9;
      --mdCode: #a798d7;
      --mdCodeBlock: #68b78d;
      --mdCodeBlockBorder: #9da5a9;
      --mdQuote: #9da5a9;
      --mdQuoteBorder: #9da5a9;
      --mdHr: #9da5a9;
      --mdListBullet: #a798d7;
      --toolDiffAdded: #68b78d;
      --toolDiffRemoved: #ea7f81;
      --toolDiffContext: #9da5a9;
      --syntaxComment: #9da5a9;
      --syntaxKeyword: #69add0;
      --syntaxFunction: #cd9a22;
      --syntaxVariable: #5db3ba;
      --syntaxString: #de8d5a;
      --syntaxNumber: #68b78d;
      --syntaxType: #a798d7;
      --syntaxOperator: #9da5a9;
      --syntaxPunctuation: #9da5a9;
      --thinkingOff: #6c767b;
      --thinkingMinimal: #68808d;
      --thinkingLow: #5489a4;
      --thinkingMedium: #6185cc;
      --thinkingHigh: #9776e5;
      --thinkingXhigh: #de54c1;
      --thinkingMax: #fe5462;
      --bashMode: #5eb286;
      --selectedBg: #213b49;
      --searchMatchBg: #4e2f1b;
      --userMessageBg: #213b49;
      --customMessageBg: #3a3055;
      --toolPendingBg: #34383a;
      --toolSuccessBg: #254131;
      --toolErrorBg: #5b282a;
      --exportPageBg: #21252c;
      --exportCardBg: #282c34;
      --exportInfoBg: #4e2f1b;`

const lightThemeVariables = `--accent: #7459b4;
      --border: #3d8eb3;
      --borderAccent: #8a72cb;
      --borderMuted: #9aa2a7;
      --success: #337e58;
      --error: #c8253d;
      --warning: #8f6802;
      --muted: #677176;
      --dim: #879095;
      --text: #3b3f41;
      --thinkingText: #7c868c;
      --scrollbarTrack: #e1e3e4;
      --scrollbarThumb: #96a0a4;
      --searchMatchText: #677176;
      --userMessageText: #3b3f41;
      --customMessageText: #677176;
      --customMessageLabel: #7459b4;
      --toolTitle: #3b3f41;
      --toolOutput: #677176;
      --mdHeading: #8f6802;
      --mdLink: #2f7899;
      --mdLinkUrl: #677176;
      --mdCode: #7459b4;
      --mdCodeBlock: #337e58;
      --mdCodeBlockBorder: #677176;
      --mdQuote: #677176;
      --mdQuoteBorder: #677176;
      --mdHr: #677176;
      --mdListBullet: #7459b4;
      --toolDiffAdded: #337e58;
      --toolDiffRemoved: #c8253d;
      --toolDiffContext: #677176;
      --syntaxComment: #677176;
      --syntaxKeyword: #2f7899;
      --syntaxFunction: #8f6802;
      --syntaxVariable: #287a81;
      --syntaxString: #a45417;
      --syntaxNumber: #337e58;
      --syntaxType: #7459b4;
      --syntaxOperator: #677176;
      --syntaxPunctuation: #677176;
      --thinkingOff: #c2c8ca;
      --thinkingMinimal: #b5c4cb;
      --thinkingLow: #9fc2d5;
      --thinkingMedium: #a2b7e0;
      --thinkingHigh: #b5a5e8;
      --thinkingXhigh: #e585cd;
      --thinkingMax: #fe7479;
      --bashMode: #40976c;
      --selectedBg: #dfe7ec;
      --searchMatchBg: #ede3dd;
      --userMessageBg: #dfe7ec;
      --customMessageBg: #e6e4ee;
      --toolPendingBg: #e4e5e6;
      --toolSuccessBg: #dee9e1;
      --toolErrorBg: #eee2e1;
      --exportPageBg: #efeeee;
      --exportCardBg: #f7f6f6;
      --exportInfoBg: #ede3dd;`
