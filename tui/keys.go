package tui

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

type KeyID string
type KeyEventType string

const (
	KeyPress   KeyEventType = "press"
	KeyRepeat  KeyEventType = "repeat"
	KeyRelease KeyEventType = "release"
)

const (
	modifierShift = 1
	modifierAlt   = 2
	modifierCtrl  = 4
	modifierSuper = 8
	lockMask      = 64 + 128
)

var kittyProtocolActive atomic.Bool

func SetKittyProtocolActive(active bool) { kittyProtocolActive.Store(active) }
func IsKittyProtocolActive() bool        { return kittyProtocolActive.Load() }

var symbolKeys = map[rune]bool{
	'`': true, '-': true, '=': true, '[': true, ']': true, '\\': true, ';': true, '\'': true,
	',': true, '.': true, '/': true, '!': true, '@': true, '#': true, '$': true, '%': true,
	'^': true, '&': true, '*': true, '(': true, ')': true, '_': true, '+': true, '|': true,
	'~': true, '{': true, '}': true, ':': true, '<': true, '>': true, '?': true,
}

var kittyFunctional = map[int]int{
	57399: '0', 57400: '1', 57401: '2', 57402: '3', 57403: '4', 57404: '5', 57405: '6', 57406: '7',
	57407: '8', 57408: '9', 57409: '.', 57410: '/', 57411: '*', 57412: '-', 57413: '+', 57415: '=',
	57416: ',', 57417: -4, 57418: -3, 57419: -1, 57420: -2, 57421: -12, 57422: -13,
	57423: -14, 57424: -15, 57425: -11, 57426: -10,
}

func normalizeFunctional(codepoint int) int {
	if normalized, ok := kittyFunctional[codepoint]; ok {
		return normalized
	}
	return codepoint
}

func normalizeShiftedLetter(codepoint, modifier int) int {
	if modifier&^lockMask&modifierShift != 0 && codepoint >= 'A' && codepoint <= 'Z' {
		return codepoint + 32
	}
	return codepoint
}

type parsedKitty struct {
	codepoint, shifted, base, modifier int
	hasShifted, hasBase                bool
	event                              KeyEventType
}

var (
	kittyCSIU       = regexp.MustCompile(`^\x1b\[([0-9]+)(:([0-9]*))?(:([0-9]+))?(;([0-9]+))?(:([0-9]+))?u$`)
	kittyArrow      = regexp.MustCompile(`^\x1b\[1;([0-9]+)(:([0-9]+))?([ABCD])$`)
	kittyFunction   = regexp.MustCompile(`^\x1b\[([0-9]+)(;([0-9]+))?(:([0-9]+))?~$`)
	kittyHomeEnd    = regexp.MustCompile(`^\x1b\[1;([0-9]+)(:([0-9]+))?([HF])$`)
	modifyOtherKeys = regexp.MustCompile(`^\x1b\[27;([0-9]+);([0-9]+)~$`)
)

func number(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func eventType(value string) KeyEventType {
	switch value {
	case "2":
		return KeyRepeat
	case "3":
		return KeyRelease
	default:
		return KeyPress
	}
}

func parseKitty(data string) (parsedKitty, bool) {
	if match := kittyCSIU.FindStringSubmatch(data); match != nil {
		parsed := parsedKitty{codepoint: number(match[1], 0), modifier: number(match[7], 1) - 1, event: eventType(match[9])}
		if match[3] != "" {
			parsed.shifted, parsed.hasShifted = number(match[3], 0), true
		}
		if match[5] != "" {
			parsed.base, parsed.hasBase = number(match[5], 0), true
		}
		return parsed, true
	}
	if match := kittyArrow.FindStringSubmatch(data); match != nil {
		codes := map[string]int{"A": -1, "B": -2, "C": -3, "D": -4}
		return parsedKitty{codepoint: codes[match[4]], modifier: number(match[1], 1) - 1, event: eventType(match[3])}, true
	}
	if match := kittyFunction.FindStringSubmatch(data); match != nil {
		codes := map[int]int{2: -11, 3: -10, 5: -12, 6: -13, 7: -14, 8: -15}
		if codepoint, ok := codes[number(match[1], 0)]; ok {
			return parsedKitty{codepoint: codepoint, modifier: number(match[3], 1) - 1, event: eventType(match[5])}, true
		}
	}
	if match := kittyHomeEnd.FindStringSubmatch(data); match != nil {
		codepoint := -14
		if match[4] == "F" {
			codepoint = -15
		}
		return parsedKitty{codepoint: codepoint, modifier: number(match[1], 1) - 1, event: eventType(match[3])}, true
	}
	return parsedKitty{}, false
}

func parseModifyOtherKeys(data string) (codepoint, modifier int, ok bool) {
	match := modifyOtherKeys.FindStringSubmatch(data)
	if match == nil {
		return 0, 0, false
	}
	return number(match[2], 0), number(match[1], 1) - 1, true
}

func IsKeyRelease(data string) bool {
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	for _, suffix := range []string{":3u", ":3~", ":3A", ":3B", ":3C", ":3D", ":3H", ":3F"} {
		if strings.Contains(data, suffix) {
			return true
		}
	}
	return false
}

func IsKeyRepeat(data string) bool {
	if strings.Contains(data, "\x1b[200~") {
		return false
	}
	for _, suffix := range []string{":2u", ":2~", ":2A", ":2B", ":2C", ":2D", ":2H", ":2F"} {
		if strings.Contains(data, suffix) {
			return true
		}
	}
	return false
}

func KeyEventTypeOf(data string) KeyEventType {
	if IsKeyRelease(data) {
		return KeyRelease
	}
	if IsKeyRepeat(data) {
		return KeyRepeat
	}
	return KeyPress
}

func parseKeyID(keyID KeyID) (key string, modifier int, ok bool) {
	parts := strings.Split(strings.ToLower(string(keyID)), "+")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return "", 0, false
	}
	key = parts[len(parts)-1]
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "shift":
			modifier |= modifierShift
		case "alt":
			modifier |= modifierAlt
		case "ctrl":
			modifier |= modifierCtrl
		case "super":
			modifier |= modifierSuper
		default:
			return "", 0, false
		}
	}
	return key, modifier, true
}

func matchesKitty(data string, codepoint, modifier int) bool {
	parsed, ok := parseKitty(data)
	if !ok || parsed.modifier&^lockMask != modifier&^lockMask {
		return false
	}
	actual := normalizeShiftedLetter(normalizeFunctional(parsed.codepoint), parsed.modifier)
	expected := normalizeShiftedLetter(normalizeFunctional(codepoint), modifier)
	if actual == expected {
		return true
	}
	if parsed.hasBase && parsed.base == codepoint {
		isLatin := actual >= 'a' && actual <= 'z'
		if !isLatin && !symbolKeys[rune(actual)] {
			return true
		}
	}
	return false
}

func matchesModify(data string, codepoint, modifier int) bool {
	actual, actualModifier, ok := parseModifyOtherKeys(data)
	return ok && actual == codepoint && actualModifier == modifier
}

var legacyKeys = map[string][]string{
	"up": {"\x1b[A", "\x1bOA"}, "down": {"\x1b[B", "\x1bOB"}, "right": {"\x1b[C", "\x1bOC"}, "left": {"\x1b[D", "\x1bOD"},
	"home": {"\x1b[H", "\x1bOH", "\x1b[1~", "\x1b[7~"}, "end": {"\x1b[F", "\x1bOF", "\x1b[4~", "\x1b[8~"},
	"insert": {"\x1b[2~"}, "delete": {"\x1b[3~"}, "pageup": {"\x1b[5~", "\x1b[[5~"}, "pagedown": {"\x1b[6~", "\x1b[[6~"},
	"clear": {"\x1b[E", "\x1bOE"}, "f1": {"\x1bOP", "\x1b[11~", "\x1b[[A"}, "f2": {"\x1bOQ", "\x1b[12~", "\x1b[[B"},
	"f3": {"\x1bOR", "\x1b[13~", "\x1b[[C"}, "f4": {"\x1bOS", "\x1b[14~", "\x1b[[D"}, "f5": {"\x1b[15~", "\x1b[[E"},
	"f6": {"\x1b[17~"}, "f7": {"\x1b[18~"}, "f8": {"\x1b[19~"}, "f9": {"\x1b[20~"}, "f10": {"\x1b[21~"}, "f11": {"\x1b[23~"}, "f12": {"\x1b[24~"},
}

var shiftedLegacy = map[string][]string{"up": {"\x1b[a"}, "down": {"\x1b[b"}, "right": {"\x1b[c"}, "left": {"\x1b[d"}, "clear": {"\x1b[e"}, "insert": {"\x1b[2$"}, "delete": {"\x1b[3$"}, "pageup": {"\x1b[5$"}, "pagedown": {"\x1b[6$"}, "home": {"\x1b[7$"}, "end": {"\x1b[8$"}}
var ctrlLegacy = map[string][]string{"up": {"\x1bOa"}, "down": {"\x1bOb"}, "right": {"\x1bOc"}, "left": {"\x1bOd"}, "clear": {"\x1bOe"}, "insert": {"\x1b[2^"}, "delete": {"\x1b[3^"}, "pageup": {"\x1b[5^"}, "pagedown": {"\x1b[6^"}, "home": {"\x1b[7^"}, "end": {"\x1b[8^"}}

func includes(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func matchesLegacyModifier(data, key string, modifier int) bool {
	if modifier == modifierShift {
		return includes(shiftedLegacy[key], data)
	}
	if modifier == modifierCtrl {
		return includes(ctrlLegacy[key], data)
	}
	return false
}

func rawCtrl(key rune) (string, bool) {
	key = []rune(strings.ToLower(string(key)))[0]
	if (key >= 'a' && key <= 'z') || key == '[' || key == '\\' || key == ']' || key == '_' {
		return string(rune(int(key) & 0x1f)), true
	}
	if key == '-' {
		return string(rune(31)), true
	}
	return "", false
}

func windowsTerminalSession() bool {
	return os.Getenv("WT_SESSION") != "" && os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_CLIENT") == "" && os.Getenv("SSH_TTY") == ""
}

func matchesRawBackspace(data string, modifier int) bool {
	if data == "\x7f" {
		return modifier == 0
	}
	if data != "\x08" {
		return false
	}
	if windowsTerminalSession() {
		return modifier == modifierCtrl
	}
	return modifier == 0
}

func printableKey(key string) (rune, bool) {
	runes := []rune(key)
	if len(runes) != 1 {
		return 0, false
	}
	r := runes[0]
	return r, (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || symbolKeys[r]
}

// MatchesKey checks one raw terminal sequence against a namespaced binding key.
func MatchesKey(data string, keyID KeyID) bool {
	key, modifier, ok := parseKeyID(keyID)
	if !ok {
		return false
	}
	switch key {
	case "escape", "esc":
		return modifier == 0 && (data == "\x1b" || matchesKitty(data, 27, 0) || matchesModify(data, 27, 0))
	case "space":
		if !IsKittyProtocolActive() && ((modifier == modifierCtrl && data == "\x00") || (modifier == modifierAlt && data == "\x1b ")) {
			return true
		}
		if modifier == 0 && data == " " {
			return true
		}
		return matchesKitty(data, 32, modifier) || matchesModify(data, 32, modifier)
	case "tab":
		if modifier == modifierShift && data == "\x1b[Z" {
			return true
		}
		if modifier == 0 && data == "\t" {
			return true
		}
		return matchesKitty(data, 9, modifier) || matchesModify(data, 9, modifier)
	case "enter", "return":
		if modifier == modifierShift && IsKittyProtocolActive() && (data == "\x1b\r" || data == "\n") {
			return true
		}
		if modifier == modifierAlt && !IsKittyProtocolActive() && data == "\x1b\r" {
			return true
		}
		if modifier == 0 && (data == "\r" || (!IsKittyProtocolActive() && data == "\n") || data == "\x1bOM") {
			return true
		}
		return matchesKitty(data, 13, modifier) || matchesKitty(data, 57414, modifier) || matchesModify(data, 13, modifier)
	case "backspace":
		if modifier == modifierAlt && (data == "\x1b\x7f" || data == "\x1b\x08") {
			return true
		}
		return matchesRawBackspace(data, modifier) || matchesKitty(data, 127, modifier) || matchesModify(data, 127, modifier)
	case "insert", "delete", "home", "end", "pageup", "pagedown":
		codes := map[string]int{"insert": -11, "delete": -10, "pageup": -12, "pagedown": -13, "home": -14, "end": -15}
		if modifier == 0 && includes(legacyKeys[key], data) {
			return true
		}
		if matchesLegacyModifier(data, key, modifier) {
			return true
		}
		return matchesKitty(data, codes[key], modifier)
	case "clear":
		if modifier == 0 {
			return includes(legacyKeys[key], data)
		}
		return matchesLegacyModifier(data, key, modifier)
	case "up", "down", "left", "right":
		codes := map[string]int{"up": -1, "down": -2, "right": -3, "left": -4}
		if modifier == modifierAlt {
			legacy := map[string][]string{"up": {"\x1bp"}, "down": {"\x1bn"}, "left": {"\x1b[1;3D", "\x1bb"}, "right": {"\x1b[1;3C", "\x1bf"}}
			if includes(legacy[key], data) || (!IsKittyProtocolActive() && ((key == "left" && data == "\x1bB") || (key == "right" && data == "\x1bF"))) {
				return true
			}
		}
		if modifier == modifierCtrl && ((key == "left" && data == "\x1b[1;5D") || (key == "right" && data == "\x1b[1;5C")) {
			return true
		}
		if modifier == 0 && includes(legacyKeys[key], data) {
			return true
		}
		if matchesLegacyModifier(data, key, modifier) {
			return true
		}
		return matchesKitty(data, codes[key], modifier)
	case "f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12":
		return modifier == 0 && includes(legacyKeys[key], data)
	}
	r, printable := printableKey(key)
	if !printable {
		return false
	}
	raw, hasRaw := rawCtrl(r)
	if modifier == modifierCtrl|modifierAlt && !IsKittyProtocolActive() && hasRaw && data == "\x1b"+raw {
		return true
	}
	if modifier == modifierAlt && !IsKittyProtocolActive() && data == "\x1b"+string(r) {
		return true
	}
	if modifier == modifierCtrl && hasRaw && data == raw {
		return true
	}
	if modifier == modifierShift && r >= 'a' && r <= 'z' && data == strings.ToUpper(string(r)) {
		return true
	}
	if modifier == 0 && data == string(r) {
		return true
	}
	return matchesKitty(data, int(r), modifier) || matchesModifyPrintable(data, int(r), modifier)
}

func matchesModifyPrintable(data string, expected, modifier int) bool {
	actual, actualModifier, ok := parseModifyOtherKeys(data)
	if !ok || modifier == 0 || actualModifier != modifier {
		return false
	}
	return normalizeShiftedLetter(actual, actualModifier) == normalizeShiftedLetter(expected, modifier)
}

func formatKey(codepoint, modifier int, base *int) string {
	codepoint = normalizeShiftedLetter(normalizeFunctional(codepoint), modifier)
	known := (codepoint >= 'a' && codepoint <= 'z') || (codepoint >= '0' && codepoint <= '9') || symbolKeys[rune(codepoint)]
	if !known && base != nil {
		codepoint = *base
	}
	name := ""
	switch codepoint {
	case 27:
		name = "escape"
	case 9:
		name = "tab"
	case 13, 57414:
		name = "enter"
	case 32:
		name = "space"
	case 127:
		name = "backspace"
	case -10:
		name = "delete"
	case -11:
		name = "insert"
	case -12:
		name = "pageUp"
	case -13:
		name = "pageDown"
	case -14:
		name = "home"
	case -15:
		name = "end"
	case -1:
		name = "up"
	case -2:
		name = "down"
	case -3:
		name = "right"
	case -4:
		name = "left"
	}
	if name == "" && ((codepoint >= 'a' && codepoint <= 'z') || (codepoint >= '0' && codepoint <= '9') || symbolKeys[rune(codepoint)]) {
		name = string(rune(codepoint))
	}
	if name == "" || modifier&^lockMask&^(modifierShift|modifierCtrl|modifierAlt|modifierSuper) != 0 {
		return ""
	}
	parts := make([]string, 0, 5)
	effective := modifier &^ lockMask
	if effective&modifierShift != 0 {
		parts = append(parts, "shift")
	}
	if effective&modifierCtrl != 0 {
		parts = append(parts, "ctrl")
	}
	if effective&modifierAlt != 0 {
		parts = append(parts, "alt")
	}
	if effective&modifierSuper != 0 {
		parts = append(parts, "super")
	}
	parts = append(parts, name)
	return strings.Join(parts, "+")
}

var legacyParsed = func() map[string]string {
	result := map[string]string{
		"\x1bOA": "up", "\x1bOB": "down", "\x1bOC": "right", "\x1bOD": "left", "\x1bOH": "home", "\x1bOF": "end",
		"\x1b[E": "clear", "\x1bOE": "clear", "\x1bOe": "ctrl+clear", "\x1b[e": "shift+clear",
		"\x1b[2~": "insert", "\x1b[2$": "shift+insert", "\x1b[2^": "ctrl+insert", "\x1b[3$": "shift+delete", "\x1b[3^": "ctrl+delete",
		"\x1b[[5~": "pageUp", "\x1b[[6~": "pageDown", "\x1b[5$": "shift+pageUp", "\x1b[6$": "shift+pageDown",
		"\x1b[7$": "shift+home", "\x1b[8$": "shift+end", "\x1b[5^": "ctrl+pageUp", "\x1b[6^": "ctrl+pageDown",
		"\x1b[7^": "ctrl+home", "\x1b[8^": "ctrl+end",
		"\x1b[a": "shift+up", "\x1b[b": "shift+down", "\x1b[c": "shift+right", "\x1b[d": "shift+left",
		"\x1bOa": "ctrl+up", "\x1bOb": "ctrl+down", "\x1bOc": "ctrl+right", "\x1bOd": "ctrl+left",
		"\x1bp": "alt+up", "\x1bn": "alt+down", "\x1bb": "alt+left", "\x1bf": "alt+right",
	}
	for key, values := range legacyKeys {
		for _, value := range values {
			if _, exists := result[value]; !exists {
				normalized := key
				if key == "pageup" {
					normalized = "pageUp"
				}
				if key == "pagedown" {
					normalized = "pageDown"
				}
				result[value] = normalized
			}
		}
	}
	return result
}()

// ParseKey returns the canonical identifier for a recognized terminal sequence.
func ParseKey(data string) string {
	if parsed, ok := parseKitty(data); ok {
		var base *int
		if parsed.hasBase {
			value := parsed.base
			base = &value
		}
		return formatKey(parsed.codepoint, parsed.modifier, base)
	}
	if codepoint, modifier, ok := parseModifyOtherKeys(data); ok {
		return formatKey(codepoint, modifier, nil)
	}
	if IsKittyProtocolActive() && (data == "\x1b\r" || data == "\n") {
		return "shift+enter"
	}
	if key := legacyParsed[data]; key != "" {
		return key
	}
	switch data {
	case "\x1b":
		return "escape"
	case "\x1c":
		return "ctrl+\\"
	case "\x1d":
		return "ctrl+]"
	case "\x1f":
		return "ctrl+-"
	case "\x1b\x1b":
		return "ctrl+alt+["
	case "\x1b\x1c":
		return "ctrl+alt+\\"
	case "\x1b\x1d":
		return "ctrl+alt+]"
	case "\x1b\x1f":
		return "ctrl+alt+-"
	case "\t":
		return "tab"
	case "\r", "\x1bOM":
		return "enter"
	case "\x00":
		return "ctrl+space"
	case " ":
		return "space"
	case "\x7f":
		return "backspace"
	case "\x08":
		if windowsTerminalSession() {
			return "ctrl+backspace"
		}
		return "backspace"
	case "\x1b[Z":
		return "shift+tab"
	case "\x1b\x7f", "\x1b\x08":
		return "alt+backspace"
	}
	if !IsKittyProtocolActive() {
		if data == "\n" {
			return "enter"
		}
		if data == "\x1b\r" {
			return "alt+enter"
		}
		if data == "\x1b " {
			return "alt+space"
		}
		if data == "\x1bB" {
			return "alt+left"
		}
		if data == "\x1bF" {
			return "alt+right"
		}
		if len(data) == 2 && data[0] == '\x1b' {
			code := data[1]
			if code >= 1 && code <= 26 {
				return "ctrl+alt+" + string(rune(code+96))
			}
			r := rune(code)
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || symbolKeys[r] {
				return "alt+" + string(r)
			}
		}
	}
	if utf8.RuneCountInString(data) == 1 {
		r, _ := utf8.DecodeRuneInString(data)
		if r >= 1 && r <= 26 {
			return "ctrl+" + string(r+96)
		}
		if r >= 32 && r <= 126 {
			return string(r)
		}
	}
	return ""
}

// DecodeKittyPrintable decodes unmodified or Shift-only CSI-u input.
func DecodeKittyPrintable(data string) string {
	parsed, ok := parseKitty(data)
	if !ok {
		return ""
	}
	modifier := parsed.modifier &^ lockMask
	if modifier&^(modifierShift) != 0 || modifier&(modifierAlt|modifierCtrl) != 0 {
		return ""
	}
	codepoint := parsed.codepoint
	if modifier&modifierShift != 0 && parsed.hasShifted {
		codepoint = parsed.shifted
	}
	codepoint = normalizeFunctional(codepoint)
	if codepoint < 32 || !utf8.ValidRune(rune(codepoint)) {
		return ""
	}
	return string(rune(codepoint))
}

func DecodePrintableKey(data string) string {
	if decoded := DecodeKittyPrintable(data); decoded != "" {
		return decoded
	}
	codepoint, modifier, ok := parseModifyOtherKeys(data)
	if !ok {
		return ""
	}
	modifier &= ^lockMask
	if modifier&^modifierShift != 0 || codepoint < 32 || !utf8.ValidRune(rune(codepoint)) {
		return ""
	}
	return string(rune(codepoint))
}
