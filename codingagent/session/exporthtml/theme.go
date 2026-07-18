package exporthtml

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

type exportTheme struct {
	variables string
	pageBg    string
	cardBg    string
	infoBg    string
}

func resolveExportTheme(name string) (exportTheme, error) {
	if name == "" {
		name = defaultThemeName(os.Getenv("COLORFGBG"))
	}
	switch name {
	case "dark":
		return exportTheme{darkThemeVariables, "#18181e", "#1e1e24", "#3c3728"}, nil
	case "light":
		return exportTheme{lightThemeVariables, "#f8f8f8", "#ffffff", "#fffae6"}, nil
	default:
		// TODO(WP-430): resolve custom theme files through the landed theme registry.
		return exportTheme{}, fmt.Errorf("Theme not found: %s", name) //nolint:staticcheck // Upstream error capitalization is observable.
	}
}

func defaultThemeName(colorFgBg string) string {
	parts := strings.Split(colorFgBg, ";")
	for index := len(parts) - 1; index >= 0; index-- {
		colorIndex, ok := parseJSDecimalInteger(trimJSSpace(parts[index]))
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

func trimJSSpace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
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
	})
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

const darkThemeVariables = `--accent: #8abeb7;
      --border: #5f87ff;
      --borderAccent: #00d7ff;
      --borderMuted: #505050;
      --success: #b5bd68;
      --error: #cc6666;
      --warning: #ffff00;
      --muted: #808080;
      --dim: #666666;
      --text: #d4d4d4;
      --thinkingText: #808080;
      --selectedBg: #3a3a4a;
      --userMessageBg: #343541;
      --userMessageText: #d4d4d4;
      --customMessageBg: #2d2838;
      --customMessageText: #d4d4d4;
      --customMessageLabel: #9575cd;
      --toolPendingBg: #282832;
      --toolSuccessBg: #283228;
      --toolErrorBg: #3c2828;
      --toolTitle: #d4d4d4;
      --toolOutput: #808080;
      --mdHeading: #f0c674;
      --mdLink: #81a2be;
      --mdLinkUrl: #666666;
      --mdCode: #8abeb7;
      --mdCodeBlock: #b5bd68;
      --mdCodeBlockBorder: #808080;
      --mdQuote: #808080;
      --mdQuoteBorder: #808080;
      --mdHr: #808080;
      --mdListBullet: #8abeb7;
      --toolDiffAdded: #b5bd68;
      --toolDiffRemoved: #cc6666;
      --toolDiffContext: #808080;
      --syntaxComment: #6A9955;
      --syntaxKeyword: #569CD6;
      --syntaxFunction: #DCDCAA;
      --syntaxVariable: #9CDCFE;
      --syntaxString: #CE9178;
      --syntaxNumber: #B5CEA8;
      --syntaxType: #4EC9B0;
      --syntaxOperator: #D4D4D4;
      --syntaxPunctuation: #D4D4D4;
      --thinkingOff: #505050;
      --thinkingMinimal: #6e6e6e;
      --thinkingLow: #5f87af;
      --thinkingMedium: #81a2be;
      --thinkingHigh: #b294bb;
      --thinkingXhigh: #d183e8;
      --thinkingMax: #ff5fff;
      --bashMode: #b5bd68;
      --exportPageBg: #18181e;
      --exportCardBg: #1e1e24;
      --exportInfoBg: #3c3728;`

const lightThemeVariables = `--accent: #5a8080;
      --border: #547da7;
      --borderAccent: #5a8080;
      --borderMuted: #b0b0b0;
      --success: #588458;
      --error: #aa5555;
      --warning: #9a7326;
      --muted: #6c6c6c;
      --dim: #767676;
      --text: #1f2328;
      --thinkingText: #6c6c6c;
      --selectedBg: #d0d0e0;
      --userMessageBg: #e8e8e8;
      --userMessageText: #1f2328;
      --customMessageBg: #ede7f6;
      --customMessageText: #1f2328;
      --customMessageLabel: #7e57c2;
      --toolPendingBg: #e8e8f0;
      --toolSuccessBg: #e8f0e8;
      --toolErrorBg: #f0e8e8;
      --toolTitle: #1f2328;
      --toolOutput: #6c6c6c;
      --mdHeading: #9a7326;
      --mdLink: #547da7;
      --mdLinkUrl: #767676;
      --mdCode: #5a8080;
      --mdCodeBlock: #588458;
      --mdCodeBlockBorder: #6c6c6c;
      --mdQuote: #6c6c6c;
      --mdQuoteBorder: #6c6c6c;
      --mdHr: #6c6c6c;
      --mdListBullet: #588458;
      --toolDiffAdded: #588458;
      --toolDiffRemoved: #aa5555;
      --toolDiffContext: #6c6c6c;
      --syntaxComment: #008000;
      --syntaxKeyword: #0000FF;
      --syntaxFunction: #795E26;
      --syntaxVariable: #001080;
      --syntaxString: #A31515;
      --syntaxNumber: #098658;
      --syntaxType: #267F99;
      --syntaxOperator: #000000;
      --syntaxPunctuation: #000000;
      --thinkingOff: #b0b0b0;
      --thinkingMinimal: #767676;
      --thinkingLow: #547da7;
      --thinkingMedium: #5a8080;
      --thinkingHigh: #875f87;
      --thinkingXhigh: #8b008b;
      --thinkingMax: #af005f;
      --bashMode: #588458;
      --exportPageBg: #f8f8f8;
      --exportCardBg: #ffffff;
      --exportInfoBg: #fffae6;`
