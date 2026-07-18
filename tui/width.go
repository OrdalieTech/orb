package tui

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

const fullReset = "\x1b[0m"

func extractANSI(text string, pos int) (string, int, bool) {
	if pos >= len(text) || text[pos] != '\x1b' || pos+1 >= len(text) {
		return "", pos, false
	}
	switch text[pos+1] {
	case '[':
		for end := pos + 2; end < len(text); end++ {
			if strings.ContainsRune("mGKHJ", rune(text[end])) {
				return text[pos : end+1], end + 1, true
			}
		}
	case ']', '_':
		for end := pos + 2; end < len(text); end++ {
			if text[end] == '\a' {
				return text[pos : end+1], end + 1, true
			}
			if text[end] == '\x1b' && end+1 < len(text) && text[end+1] == '\\' {
				return text[pos : end+2], end + 2, true
			}
		}
	}
	return "", pos, false
}

func graphemeWidth(value string) int {
	if value == "\t" {
		return 3
	}
	return uniseg.StringWidth(value)
}

func forEachGrapheme(value string, visit func(string) bool) {
	graphemes := uniseg.NewGraphemes(value)
	for graphemes.Next() {
		if !visit(graphemes.Str()) {
			return
		}
	}
}

// VisibleWidth returns the number of terminal cells occupied by text. Tabs
// are fixed at three cells and the ANSI, OSC, and APC sequences upstream
// recognizes are zero-width.
func VisibleWidth(text string) int {
	if text == "" {
		return 0
	}
	var plain strings.Builder
	for pos := 0; pos < len(text); {
		if _, next, ok := extractANSI(text, pos); ok {
			pos = next
			continue
		}
		if text[pos] == '\t' {
			plain.WriteString("   ")
			pos++
			continue
		}
		_, size := utf8.DecodeRuneInString(text[pos:])
		plain.WriteString(text[pos : pos+size])
		pos += size
	}
	return uniseg.StringWidth(plain.String())
}

// NormalizeTerminalOutput applies upstream's display-only Thai/Lao AM
// decomposition and expands visible tabs without changing terminal strings.
func NormalizeTerminalOutput(text string) string {
	text = strings.NewReplacer("\u0e33", "\u0e4d\u0e32", "\u0eb3", "\u0ecd\u0eb2").Replace(text)
	if !strings.ContainsRune(text, '\t') {
		return text
	}
	var result strings.Builder
	for pos := 0; pos < len(text); {
		if code, next, ok := extractANSI(text, pos); ok {
			result.WriteString(code)
			pos = next
			continue
		}
		if text[pos] == '\t' {
			result.WriteString("   ")
			pos++
			continue
		}
		_, size := utf8.DecodeRuneInString(text[pos:])
		result.WriteString(text[pos : pos+size])
		pos += size
	}
	return result.String()
}

func truncateFragment(text string, maxWidth int) (string, int) {
	if maxWidth <= 0 {
		return "", 0
	}
	var result strings.Builder
	width := 0
	forEachGrapheme(text, func(segment string) bool {
		segmentWidth := graphemeWidth(segment)
		if width+segmentWidth > maxWidth {
			return false
		}
		result.WriteString(segment)
		width += segmentWidth
		return true
	})
	return result.String(), width
}

// TruncateToWidth keeps a contiguous grapheme prefix and brackets an ellipsis
// with resets so styles from truncated content cannot bleed into it.
func TruncateToWidth(text string, maxWidth int, ellipsis string, pad bool) string {
	if maxWidth <= 0 {
		return ""
	}
	textWidth := VisibleWidth(text)
	if textWidth <= maxWidth {
		if pad {
			return text + strings.Repeat(" ", maxWidth-textWidth)
		}
		return text
	}
	ellipsisWidth := VisibleWidth(ellipsis)
	if ellipsisWidth >= maxWidth {
		clipped, width := truncateFragment(ellipsis, maxWidth)
		if width == 0 {
			if pad {
				return strings.Repeat(" ", maxWidth)
			}
			return ""
		}
		result := fullReset + clipped + fullReset
		if pad {
			result += strings.Repeat(" ", maxWidth-width)
		}
		return result
	}

	target := maxWidth - ellipsisWidth
	var prefix, pending strings.Builder
	keptWidth := 0
	keeping := true
	for pos := 0; pos < len(text); {
		if code, next, ok := extractANSI(text, pos); ok {
			if keeping {
				pending.WriteString(code)
			}
			pos = next
			continue
		}
		end := pos
		for end < len(text) {
			if _, _, ok := extractANSI(text, end); ok || text[end] == '\t' {
				break
			}
			_, size := utf8.DecodeRuneInString(text[end:])
			end += size
		}
		if end == pos && text[pos] == '\t' {
			if keeping && keptWidth+3 <= target {
				prefix.WriteString(pending.String())
				pending.Reset()
				prefix.WriteByte('\t')
				keptWidth += 3
			} else {
				keeping = false
				pending.Reset()
			}
			pos++
			continue
		}
		forEachGrapheme(text[pos:end], func(segment string) bool {
			segmentWidth := graphemeWidth(segment)
			if keeping && keptWidth+segmentWidth <= target {
				prefix.WriteString(pending.String())
				pending.Reset()
				prefix.WriteString(segment)
				keptWidth += segmentWidth
			} else {
				keeping = false
				pending.Reset()
			}
			return true
		})
		pos = end
	}

	result := prefix.String() + fullReset
	if ellipsis != "" {
		result += ellipsis + fullReset
	}
	if pad {
		result += strings.Repeat(" ", maxWidth-keptWidth-ellipsisWidth)
	}
	return result
}

// ApplyBackgroundToLine pads a line before applying its background callback.
func ApplyBackgroundToLine(line string, width int, background StyleFunc) string {
	line += strings.Repeat(" ", max(0, width-VisibleWidth(line)))
	if background == nil {
		return line
	}
	return background(line)
}

type ansiTracker struct {
	bold, dim, italic, underline, blink, inverse, hidden, strike bool
	fg, bg                                                       string
	hyperlink, hyperlinkEnd                                      string
}

func (tracker *ansiTracker) resetSGR() {
	tracker.bold, tracker.dim, tracker.italic = false, false, false
	tracker.underline, tracker.blink, tracker.inverse = false, false, false
	tracker.hidden, tracker.strike, tracker.fg, tracker.bg = false, false, "", ""
}

func (tracker *ansiTracker) process(code string) {
	if strings.HasPrefix(code, "\x1b]8;") {
		end := "\x1b\\"
		trim := 2
		if strings.HasSuffix(code, "\a") {
			end, trim = "\a", 1
		}
		body := code[4 : len(code)-trim]
		separator := strings.IndexByte(body, ';')
		if separator >= 0 && body[separator+1:] != "" {
			tracker.hyperlink, tracker.hyperlinkEnd = code, end
		} else if separator >= 0 {
			tracker.hyperlink, tracker.hyperlinkEnd = "", ""
		}
		return
	}
	if !strings.HasPrefix(code, "\x1b[") || !strings.HasSuffix(code, "m") {
		return
	}
	body := code[2 : len(code)-1]
	if body == "" || body == "0" {
		tracker.resetSGR()
		return
	}
	parts := strings.Split(body, ";")
	for index := 0; index < len(parts); index++ {
		value, _ := strconv.Atoi(parts[index])
		if (value == 38 || value == 48) && index+2 < len(parts) && parts[index+1] == "5" {
			color := strings.Join(parts[index:index+3], ";")
			if value == 38 {
				tracker.fg = color
			} else {
				tracker.bg = color
			}
			index += 2
			continue
		}
		if (value == 38 || value == 48) && index+4 < len(parts) && parts[index+1] == "2" {
			color := strings.Join(parts[index:index+5], ";")
			if value == 38 {
				tracker.fg = color
			} else {
				tracker.bg = color
			}
			index += 4
			continue
		}
		switch value {
		case 0:
			tracker.resetSGR()
		case 1:
			tracker.bold = true
		case 2:
			tracker.dim = true
		case 3:
			tracker.italic = true
		case 4:
			tracker.underline = true
		case 5:
			tracker.blink = true
		case 7:
			tracker.inverse = true
		case 8:
			tracker.hidden = true
		case 9:
			tracker.strike = true
		case 21:
			tracker.bold = false
		case 22:
			tracker.bold, tracker.dim = false, false
		case 23:
			tracker.italic = false
		case 24:
			tracker.underline = false
		case 25:
			tracker.blink = false
		case 27:
			tracker.inverse = false
		case 28:
			tracker.hidden = false
		case 29:
			tracker.strike = false
		case 39:
			tracker.fg = ""
		case 49:
			tracker.bg = ""
		default:
			if (value >= 30 && value <= 37) || (value >= 90 && value <= 97) {
				tracker.fg = parts[index]
			}
			if (value >= 40 && value <= 47) || (value >= 100 && value <= 107) {
				tracker.bg = parts[index]
			}
		}
	}
}

func (tracker *ansiTracker) active() string {
	codes := make([]string, 0, 10)
	if tracker.bold {
		codes = append(codes, "1")
	}
	if tracker.dim {
		codes = append(codes, "2")
	}
	if tracker.italic {
		codes = append(codes, "3")
	}
	if tracker.underline {
		codes = append(codes, "4")
	}
	if tracker.blink {
		codes = append(codes, "5")
	}
	if tracker.inverse {
		codes = append(codes, "7")
	}
	if tracker.hidden {
		codes = append(codes, "8")
	}
	if tracker.strike {
		codes = append(codes, "9")
	}
	if tracker.fg != "" {
		codes = append(codes, tracker.fg)
	}
	if tracker.bg != "" {
		codes = append(codes, tracker.bg)
	}
	result := ""
	if len(codes) > 0 {
		result = "\x1b[" + strings.Join(codes, ";") + "m"
	}
	return result + tracker.hyperlink
}

func (tracker *ansiTracker) lineEndReset() string {
	result := ""
	if tracker.underline {
		result += "\x1b[24m"
	}
	if tracker.hyperlink != "" {
		result += "\x1b]8;;" + tracker.hyperlinkEnd
	}
	return result
}

func updateTracker(text string, tracker *ansiTracker) {
	for pos := 0; pos < len(text); {
		if code, next, ok := extractANSI(text, pos); ok {
			tracker.process(code)
			pos = next
			continue
		}
		_, size := utf8.DecodeRuneInString(text[pos:])
		pos += size
	}
}

func isCJK(segment string) bool {
	r, _ := utf8.DecodeRuneInString(segment)
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Bopomofo)
}

func ansiTokens(text string) []string {
	tokens := make([]string, 0)
	current, pending := "", ""
	kind := byte(0)
	flush := func() {
		if current != "" {
			tokens = append(tokens, current)
			current, kind = "", 0
		}
	}
	for pos := 0; pos < len(text); {
		if code, next, ok := extractANSI(text, pos); ok {
			pending += code
			pos = next
			continue
		}
		end := pos
		for end < len(text) {
			if _, _, ok := extractANSI(text, end); ok {
				break
			}
			_, size := utf8.DecodeRuneInString(text[end:])
			end += size
		}
		forEachGrapheme(text[pos:end], func(segment string) bool {
			space := segment == " "
			if !space && isCJK(segment) {
				flush()
				tokens = append(tokens, pending+segment)
				pending = ""
				return true
			}
			nextKind := byte('w')
			if space {
				nextKind = 's'
			}
			if current != "" && kind != nextKind {
				flush()
			}
			current += pending + segment
			pending = ""
			kind = nextKind
			return true
		})
		pos = end
	}
	if pending != "" {
		if current != "" {
			current += pending
		} else if len(tokens) > 0 {
			tokens[len(tokens)-1] += pending
		} else {
			current = pending
		}
	}
	flush()
	return tokens
}

func breakLongWord(word string, width int, tracker *ansiTracker) []string {
	lines := make([]string, 0)
	line, lineWidth := tracker.active(), 0
	for pos := 0; pos < len(word); {
		if code, next, ok := extractANSI(word, pos); ok {
			line += code
			tracker.process(code)
			pos = next
			continue
		}
		end := pos
		for end < len(word) {
			if _, _, ok := extractANSI(word, end); ok {
				break
			}
			_, size := utf8.DecodeRuneInString(word[end:])
			end += size
		}
		forEachGrapheme(word[pos:end], func(segment string) bool {
			segmentWidth := graphemeWidth(segment)
			if lineWidth+segmentWidth > width {
				lines = append(lines, line+tracker.lineEndReset())
				line, lineWidth = tracker.active(), 0
			}
			line += segment
			lineWidth += segmentWidth
			return true
		})
		pos = end
	}
	if line != "" {
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func wrapSingleLine(line string, width int) []string {
	if line == "" {
		return []string{""}
	}
	if VisibleWidth(line) <= width {
		return []string{line}
	}
	wrapped := make([]string, 0)
	tracker := &ansiTracker{}
	current, currentWidth := "", 0
	for _, token := range ansiTokens(line) {
		tokenWidth := VisibleWidth(token)
		whitespace := strings.TrimSpace(token) == ""
		if tokenWidth > width && !whitespace {
			if current != "" {
				wrapped = append(wrapped, current+tracker.lineEndReset())
			}
			broken := breakLongWord(token, width, tracker)
			wrapped = append(wrapped, broken[:len(broken)-1]...)
			current, currentWidth = broken[len(broken)-1], VisibleWidth(broken[len(broken)-1])
			continue
		}
		if currentWidth+tokenWidth > width && currentWidth > 0 {
			wrapped = append(wrapped, strings.TrimRightFunc(current, unicode.IsSpace)+tracker.lineEndReset())
			if whitespace {
				current, currentWidth = tracker.active(), 0
			} else {
				current, currentWidth = tracker.active()+token, tokenWidth
			}
		} else {
			current += token
			currentWidth += tokenWidth
		}
		updateTracker(token, tracker)
	}
	if current != "" {
		wrapped = append(wrapped, current)
	}
	if len(wrapped) == 0 {
		return []string{""}
	}
	for index := range wrapped {
		wrapped[index] = strings.TrimRightFunc(wrapped[index], unicode.IsSpace)
	}
	return wrapped
}

// WrapTextWithANSI word-wraps while reopening active SGR and OSC-8 state on
// physical lines, matching upstream's line-isolated renderer contract.
func WrapTextWithANSI(text string, width int) []string {
	if text == "" {
		return []string{""}
	}
	if width < 1 {
		width = 1
	}
	tracker := &ansiTracker{}
	result := make([]string, 0)
	for index, input := range splitLines(text) {
		prefix := ""
		if index > 0 {
			prefix = tracker.active()
		}
		result = append(result, wrapSingleLine(prefix+input, width)...)
		updateTracker(input, tracker)
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

// SliceByColumn extracts a range of visible columns from a line. ANSI codes
// pending at the slice start are re-emitted before the first kept grapheme;
// strict excludes boundary wide chars that would extend past the range.
func SliceByColumn(line string, startCol, length int, strict bool) string {
	text, _ := sliceWithWidth(line, startCol, length, strict)
	return text
}

func sliceWithWidth(line string, startCol, length int, strict bool) (string, int) {
	if length <= 0 {
		return "", 0
	}
	endCol := startCol + length
	var result, pendingANSI strings.Builder
	resultWidth, currentCol := 0, 0
	for pos := 0; pos < len(line) && currentCol < endCol; {
		if code, next, ok := extractANSI(line, pos); ok {
			if currentCol >= startCol && currentCol < endCol {
				result.WriteString(code)
			} else if currentCol < startCol {
				pendingANSI.WriteString(code)
			}
			pos = next
			continue
		}
		end := pos
		for end < len(line) {
			if _, _, ok := extractANSI(line, end); ok {
				break
			}
			_, size := utf8.DecodeRuneInString(line[end:])
			end += size
		}
		forEachGrapheme(line[pos:end], func(grapheme string) bool {
			width := graphemeWidth(grapheme)
			inRange := currentCol >= startCol && currentCol < endCol
			fits := !strict || currentCol+width <= endCol
			if inRange && fits {
				if pendingANSI.Len() > 0 {
					result.WriteString(pendingANSI.String())
					pendingANSI.Reset()
				}
				result.WriteString(grapheme)
				resultWidth += width
			}
			currentCol += width
			return currentCol < endCol
		})
		pos = end
	}
	return result.String(), resultWidth
}

func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}
