package mermaid

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/OrdalieTech/orb/internal/jstrim"
)

// wrapWidth is the most display columns a node label wraps to per line ...
const wrapWidth = 24

// maxLines caps wrapped label lines; overflow is truncated with an ellipsis.
const maxLines = 4

// maxLabel is the columns an edge label is truncated to.
const maxLabel = 28

// labelBreakChars are identifier-boundary characters preferred as break
// points when a single word is too wide to fit, so it is not sliced
// mid-segment. Mirrors TOKEN_BREAK_CHARS in grok-build's mermaid-to-svg.
const labelBreakChars = "_-./"

// jsTrim etc. mirror JS String.prototype.trim and the regex \s class, which
// share one whitespace set that differs from Go's unicode.IsSpace.
func jsTrim(s string) string      { return strings.TrimFunc(s, jstrim.IsSpace) }
func jsTrimStart(s string) string { return strings.TrimLeftFunc(s, jstrim.IsSpace) }
func jsTrimEnd(s string) string   { return strings.TrimRightFunc(s, jstrim.IsSpace) }

// jsFields is `s.split(/\s+/).filter((w) => w !== ”)`.
func jsFields(s string) []string { return strings.FieldsFunc(s, jstrim.IsSpace) }

// hasJSSpace is `/\s/.test(s)`.
func hasJSSpace(s string) bool { return strings.IndexFunc(s, jstrim.IsSpace) != -1 }

// asciiLower is ASCII-only case folding, matching Rust's to_ascii_lowercase.
// Full Unicode lowercasing can change a string's length, which would desync
// the offsets some parsers slice with.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

func asciiUpper(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}, s)
}

// isControl matches C0 and C1 controls, less the \t\n\r the parsers and
// srcLines read. They measure one column and paint none, so a box sized
// around one is drawn a column short of its own border; NUL also collides
// with the cont sentinel, and ESC would inject ANSI into scrollback.
func isControl(r rune) bool {
	switch {
	case r <= 0x08, r == 0x0b, r == 0x0c, r >= 0x0e && r <= 0x1f, r >= 0x7f && r <= 0x9f:
		return true
	}
	return false
}

// stripControls is applied by every public entry point taking untrusted source.
func stripControls(src string) string {
	if strings.IndexFunc(src, isControl) == -1 {
		return src
	}
	var out strings.Builder
	for _, r := range src {
		if !isControl(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// srcLines splits source into lines the way Rust's str::lines() does: on \n,
// with a trailing \r stripped, and without a final empty line when the input
// ends in a newline.
func srcLines(src string) []string {
	out := strings.Split(src, "\n")
	for i, l := range out {
		out[i] = strings.TrimSuffix(l, "\r")
	}
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// isAlphanumeric matches Rust's char::is_alphanumeric via the JS class
// [\p{Alphabetic}\p{N}]: Alphabetic is L + Nl + Other_Alphabetic, and N adds
// Nd and No.
func isAlphanumeric(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Other_Alphabetic, r)
}

// isIdChar reports a character allowed in a bare node/state/class identifier.
func isIdChar(r rune) bool { return isAlphanumeric(r) || r == '_' }

const entityLookahead = 10

var namedEntities = map[string]string{
	"lt":   "<",
	"gt":   ">",
	"amp":  "&",
	"quot": `"`,
	"apos": "'",
}

func isASCIIDigits(s string, hex bool) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case hex && (c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'):
		default:
			return false
		}
	}
	return true
}

func decodeEntityBody(body string) (string, bool) {
	if named, ok := namedEntities[body]; ok {
		return named, true
	}
	if !strings.HasPrefix(body, "#") {
		return "", false
	}
	num := body[1:]
	hex := strings.HasPrefix(num, "x") || strings.HasPrefix(num, "X")
	digits := num
	if hex {
		digits = num[1:]
	}
	if !isASCIIDigits(digits, hex) {
		return "", false
	}
	base := 10
	if hex {
		base = 16
	}
	// The bounded window keeps digits short enough that 64 bits never overflow.
	code, err := strconv.ParseInt(digits, base, 64)
	if err != nil {
		return "", false
	}
	// Surrogates and out-of-range values are not characters at all.
	if code > 0x10ffff || (code >= 0xd800 && code <= 0xdfff) {
		return "", false
	}
	// Reject control chars: NUL collides with the cont sentinel and ESC would
	// inject ANSI into scrollback.
	if code < 0x20 || (code >= 0x7f && code <= 0x9f) {
		return "", false
	}
	return string(rune(code)), true
}

// decodeHtmlEntities decodes HTML entities in label text. Called once per
// label: via cleanLabel for bracketed labels, or explicitly at each
// direct-push sink.
func decodeHtmlEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	chars := []rune(s)
	var out strings.Builder
	i := 0
	for i < len(chars) {
		if chars[i] != '&' {
			out.WriteRune(chars[i])
			i++
			continue
		}
		// Scan a bounded window including the terminating `;`, so a stray `&`
		// or an over-long run stays literal.
		hi := min(i+1+entityLookahead, len(chars))
		semi := -1
		for j := i + 1; j < hi; j++ {
			if chars[j] == ';' {
				semi = j
				break
			}
		}
		decoded, ok := "", false
		if semi != -1 {
			decoded, ok = decodeEntityBody(string(chars[i+1 : semi]))
		}
		if !ok {
			out.WriteByte('&')
			i++
		} else {
			// Resume past the `;`. The single pass never re-scans emitted text,
			// so `&amp;lt;` decodes to the literal `&lt;` rather than to `<`.
			out.WriteString(decoded)
			i = semi + 1
		}
	}
	return out.String()
}

// stripMarkdown strips markdown emphasis from a `backtick` label string.
func stripMarkdown(s string) string {
	noCode := strings.ReplaceAll(s, "`", "")
	noStrong := strings.ReplaceAll(strings.ReplaceAll(noCode, "**", ""), "__", "")
	chars := []rune(noStrong)
	var out strings.Builder
	for i, c := range chars {
		// Keep `*`/`_` only when they sit inside a word, so snake_case survives.
		inWord := i > 0 && isAlphanumeric(chars[i-1]) &&
			i+1 < len(chars) && isAlphanumeric(chars[i+1])
		if (c == '*' || c == '_') && !inWord {
			continue
		}
		out.WriteRune(c)
	}
	return jsTrim(out.String())
}

// htmlFormatTags are inline formatting tags that carry no meaning in a
// terminal. Anything else that looks like a tag — `Vec<String>`, `<id>` — is
// left alone.
var htmlFormatTags = map[string]bool{
	"b": true, "strong": true, "i": true, "em": true, "u": true, "s": true,
	"strike": true, "del": true, "ins": true, "mark": true, "small": true,
	"big": true, "sub": true, "sup": true, "code": true, "kbd": true,
	"samp": true, "var": true, "tt": true, "span": true, "font": true,
	"q": true, "abbr": true, "cite": true, "pre": true,
}

func isASCIIAlnum(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
}

// htmlTagAt reads a tag starting at start, returning its name and the index
// after `>`.
func htmlTagAt(chars []rune, start int) (name string, end int, ok bool) {
	i := start + 1
	if i < len(chars) && chars[i] == '/' {
		i++
	}
	nameStart := i
	for i < len(chars) && isASCIIAlnum(chars[i]) {
		i++
	}
	if i == nameStart {
		return "", 0, false
	}
	name = string(chars[nameStart:i])
	for i < len(chars) && chars[i] != '>' {
		if chars[i] == '<' {
			return "", 0, false
		}
		i++
	}
	if i < len(chars) && chars[i] == '>' {
		return name, i + 1, true
	}
	return "", 0, false
}

func stripHtmlTags(s string) string {
	chars := []rune(s)
	var out strings.Builder
	i := 0
	for i < len(chars) {
		if chars[i] == '<' {
			if name, end, ok := htmlTagAt(chars, i); ok {
				lower := asciiLower(name)
				if lower == "br" {
					out.WriteByte(' ')
					i = end
					continue
				}
				if htmlFormatTags[lower] {
					i = end
					continue
				}
			}
		}
		out.WriteRune(chars[i])
		i++
	}
	return out.String()
}

// unwrap strips one matching pair of wrapping delimiters, if present.
func unwrap(s, open, close string) (string, bool) {
	if len(s) >= len(open)+len(close) && strings.HasPrefix(s, open) && strings.HasSuffix(s, close) {
		return s[len(open) : len(s)-len(close)], true
	}
	return "", false
}

// cleanLabel normalises raw label text: strip markup, unquote, and decode
// entities. Decoding happens after tag-stripping so `<b>` is removed as
// markup while `&lt;b&gt;` survives as the literal text `<b>`.
func cleanLabel(raw string) string {
	trimmed := jsTrim(stripHtmlTags(jsTrim(raw)))
	unquoted := trimmed
	if u, ok := unwrap(trimmed, `"`, `"`); ok {
		unquoted = u
	} else if u, ok := unwrap(trimmed, "'", "'"); ok {
		unquoted = u
	}
	unquoted = jsTrim(unquoted)
	if md, ok := unwrap(unquoted, "`", "`"); ok {
		return decodeHtmlEntities(stripMarkdown(jsTrim(md)))
	}
	return decodeHtmlEntities(unquoted)
}

// lastBreak is the index of the last identifier-boundary character, or -1.
func lastBreak(s string) int {
	best := -1
	for _, c := range labelBreakChars {
		best = max(best, strings.LastIndexByte(s, byte(c)))
	}
	return best
}

// wrapLabel wraps a label to width columns over at most maxLines lines,
// truncating the last line with an ellipsis if it overflows.
//
// A word too wide to fit is broken after the last identifier boundary
// (`_-./`) that fits, falling back to a per-character break when it has none.
func wrapLabel(label string, width, maxLines int) []string {
	width = max(1, width)
	var lines []string
	cur := ""
	curW := 0

	for _, word := range jsFields(label) {
		ww := stringWidth(word)
		switch {
		case ww > width:
			if cur != "" {
				lines = append(lines, cur)
			}
			chunk := ""
			chunkW := 0
			for _, mc := range measured(word) {
				if chunkW+mc.width > width && chunk != "" {
					p := lastBreak(chunk)
					carry := ""
					if p != -1 {
						carry = chunk[p+1:]
					}
					if p == -1 {
						lines = append(lines, chunk)
					} else {
						lines = append(lines, chunk[:p+1])
					}
					chunk = carry
					chunkW = stringWidth(carry)
				}
				chunk += mc.cluster
				chunkW += mc.width
			}
			cur = chunk
			curW = chunkW
		case cur == "":
			cur = word
			curW = ww
		case curW+1+ww <= width:
			cur += " " + word
			curW += 1 + ww
		default:
			lines = append(lines, cur)
			cur = word
			curW = ww
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}

	if len(lines) > maxLines {
		lines = lines[:maxLines]
		target := max(1, width-1)
		s := ""
		sw := 0
		for _, mc := range measured(lines[len(lines)-1]) {
			if sw+mc.width > target {
				break
			}
			s += mc.cluster
			sw += mc.width
		}
		lines[len(lines)-1] = s + "…"
	}
	return lines
}

// fitLabel truncates to inner columns, leaving room for the ellipsis.
func fitLabel(label string, inner int) string {
	if stringWidth(label) <= inner {
		return label
	}
	out := ""
	used := 0
	for _, mc := range measured(label) {
		if used+mc.width+1 > inner {
			break
		}
		out += mc.cluster
		used += mc.width
	}
	return out + "…"
}
