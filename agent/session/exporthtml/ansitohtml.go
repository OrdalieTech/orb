package exporthtml

import (
	"strconv"
	"strings"
)

// ANSI escape code to HTML converter, ported from upstream
// core/export-html/ansi-to-html.ts. Converts terminal ANSI color/style codes
// to HTML with inline styles: standard and bright foreground/background
// colors, the 256-color palette, RGB true color, bold/dim/italic/underline,
// and reset.

// ansiPalette is the standard ANSI color palette (0-15).
var ansiPalette = [16]string{
	"#000000", "#800000", "#008000", "#808000", "#000080", "#800080", "#008080", "#c0c0c0",
	"#808080", "#ff0000", "#00ff00", "#ffff00", "#0000ff", "#ff00ff", "#00ffff", "#ffffff",
}

// color256ToHex converts a 256-color index to hex. SGR params are parsed from
// digit runs, so index is never negative; oversized values follow upstream's
// unclamped grayscale arithmetic.
func color256ToHex(index int) string {
	if index < 16 {
		return ansiPalette[index]
	}
	pad2 := func(encoded string) string {
		if len(encoded) < 2 {
			return "0" + encoded
		}
		return encoded
	}
	if index < 232 {
		cubeIndex := index - 16
		component := func(value int) int {
			if value == 0 {
				return 0
			}
			return 55 + value*40
		}
		hex := func(value int) string {
			return pad2(strconv.FormatInt(int64(component(value)), 16))
		}
		return "#" + hex(cubeIndex/36) + hex((cubeIndex%36)/6) + hex(cubeIndex%6)
	}
	grayHex := pad2(strconv.FormatInt(int64(8+(index-232)*10), 16))
	return "#" + grayHex + grayHex + grayHex
}

func escapeHTML(text string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#039;",
	)
	return replacer.Replace(text)
}

type ansiTextStyle struct {
	fg        string
	bg        string
	bold      bool
	dim       bool
	italic    bool
	underline bool
}

func (style ansiTextStyle) inlineCSS() string {
	parts := make([]string, 0, 6)
	if style.fg != "" {
		parts = append(parts, "color:"+style.fg)
	}
	if style.bg != "" {
		parts = append(parts, "background-color:"+style.bg)
	}
	if style.bold {
		parts = append(parts, "font-weight:bold")
	}
	if style.dim {
		parts = append(parts, "opacity:0.6")
	}
	if style.italic {
		parts = append(parts, "font-style:italic")
	}
	if style.underline {
		parts = append(parts, "text-decoration:underline")
	}
	return strings.Join(parts, ";")
}

func (style ansiTextStyle) hasStyle() bool {
	return style.fg != "" || style.bg != "" || style.bold || style.dim || style.italic || style.underline
}

// applySGRCode parses SGR (Select Graphic Rendition) params and updates style.
func applySGRCode(params []int, style *ansiTextStyle) {
	for index := 0; index < len(params); index++ {
		code := params[index]
		switch {
		case code == 0:
			*style = ansiTextStyle{}
		case code == 1:
			style.bold = true
		case code == 2:
			style.dim = true
		case code == 3:
			style.italic = true
		case code == 4:
			style.underline = true
		case code == 22:
			style.bold = false
			style.dim = false
		case code == 23:
			style.italic = false
		case code == 24:
			style.underline = false
		case code >= 30 && code <= 37:
			style.fg = ansiPalette[code-30]
		case code == 38:
			if index+2 < len(params) && params[index+1] == 5 {
				style.fg = color256ToHex(params[index+2])
				index += 2
			} else if index+4 < len(params) && params[index+1] == 2 {
				style.fg = "rgb(" + strconv.Itoa(params[index+2]) + "," + strconv.Itoa(params[index+3]) + "," + strconv.Itoa(params[index+4]) + ")"
				index += 4
			}
		case code == 39:
			style.fg = ""
		case code >= 40 && code <= 47:
			style.bg = ansiPalette[code-40]
		case code == 48:
			if index+2 < len(params) && params[index+1] == 5 {
				style.bg = color256ToHex(params[index+2])
				index += 2
			} else if index+4 < len(params) && params[index+1] == 2 {
				style.bg = "rgb(" + strconv.Itoa(params[index+2]) + "," + strconv.Itoa(params[index+3]) + "," + strconv.Itoa(params[index+4]) + ")"
				index += 4
			}
		case code == 49:
			style.bg = ""
		case code >= 90 && code <= 97:
			style.fg = ansiPalette[code-90+8]
		case code >= 100 && code <= 107:
			style.bg = ansiPalette[code-100+8]
		}
		// Unrecognized codes are ignored.
	}
}

// nextSGRSequence finds the next ESC[<digits/;>m sequence at or after start,
// returning the match bounds and raw parameter text.
func nextSGRSequence(text string, start int) (matchStart, matchEnd int, params string, found bool) {
	for index := start; index < len(text); index++ {
		if text[index] != 0x1b || index+1 >= len(text) || text[index+1] != '[' {
			continue
		}
		cursor := index + 2
		for cursor < len(text) && (text[cursor] == ';' || text[cursor] >= '0' && text[cursor] <= '9') {
			cursor++
		}
		if cursor < len(text) && text[cursor] == 'm' {
			return index, cursor + 1, text[index+2 : cursor], true
		}
	}
	return 0, 0, "", false
}

// AnsiToHTML converts ANSI-escaped text to HTML with inline styles.
func AnsiToHTML(text string) string {
	style := ansiTextStyle{}
	var result strings.Builder
	lastIndex := 0
	inSpan := false
	for {
		matchStart, matchEnd, rawParams, found := nextSGRSequence(text, lastIndex)
		if !found {
			break
		}
		if before := text[lastIndex:matchStart]; before != "" {
			result.WriteString(escapeHTML(before))
		}
		params := []int{0}
		if rawParams != "" {
			pieces := strings.Split(rawParams, ";")
			params = make([]int, 0, len(pieces))
			for _, piece := range pieces {
				// parseInt(piece, 10) || 0 — empty pieces become 0.
				value, err := strconv.Atoi(piece)
				if err != nil {
					value = 0
				}
				params = append(params, value)
			}
		}
		if inSpan {
			result.WriteString("</span>")
			inSpan = false
		}
		applySGRCode(params, &style)
		if style.hasStyle() {
			result.WriteString(`<span style="` + style.inlineCSS() + `">`)
			inSpan = true
		}
		lastIndex = matchEnd
	}
	if remaining := text[lastIndex:]; remaining != "" {
		result.WriteString(escapeHTML(remaining))
	}
	if inSpan {
		result.WriteString("</span>")
	}
	return result.String()
}

// AnsiLinesToHTML converts ANSI-escaped lines to HTML, one div per line.
func AnsiLinesToHTML(lines []string) string {
	var result strings.Builder
	for _, line := range lines {
		converted := AnsiToHTML(line)
		if converted == "" {
			converted = "&nbsp;"
		}
		result.WriteString(`<div class="ansi-line">` + converted + "</div>")
	}
	return result.String()
}
