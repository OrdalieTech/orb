package partialjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Allow controls which incomplete JSON values Parse may return.
type Allow uint16

const (
	AllowString Allow = 1 << iota
	AllowNumber
	AllowArray
	AllowObject
	AllowNull
	AllowBool
	AllowNaN
	AllowInfinity
	AllowNegativeInfinity

	AllowInf        = AllowInfinity | AllowNegativeInfinity
	AllowSpecial    = AllowNull | AllowBool | AllowInf | AllowNaN
	AllowAtom       = AllowString | AllowNumber | AllowSpecial
	AllowCollection = AllowArray | AllowObject
	AllowAll        = AllowAtom | AllowCollection
)

// PartialJSONError reports an incomplete value disallowed by Parse's mask.
type PartialJSONError struct {
	Message  string
	Position int
}

func (e *PartialJSONError) Error() string {
	return fmt.Sprintf("%s at position %d", e.Message, e.Position)
}

// MalformedJSONError reports input that cannot be parsed as JSON, partial or
// complete.
type MalformedJSONError struct {
	Message  string
	Position int
}

func (e *MalformedJSONError) Error() string {
	return fmt.Sprintf("%s at position %d", e.Message, e.Position)
}

// RepairJSON escapes raw control characters and invalid backslash escapes
// inside JSON strings. It intentionally leaves structure untouched.
func RepairJSON(input string) string {
	var repaired strings.Builder
	repaired.Grow(len(input))
	inString := false

	for i := 0; i < len(input); i++ {
		char := input[i]
		if !inString {
			repaired.WriteByte(char)
			if char == '"' {
				inString = true
			}
			continue
		}

		switch char {
		case '"':
			repaired.WriteByte(char)
			inString = false
		case '\\':
			if i+1 >= len(input) {
				repaired.WriteString(`\\`)
				continue
			}

			next := input[i+1]
			if next == 'u' && i+5 < len(input) && isHex4(input[i+2:i+6]) {
				repaired.WriteString(input[i : i+6])
				i += 5
				continue
			}
			if strings.ContainsRune(`"\\/bfnrtu`, rune(next)) {
				repaired.WriteByte(char)
				repaired.WriteByte(next)
				i++
				continue
			}
			repaired.WriteString(`\\`)
		default:
			if char <= 0x1f {
				writeEscapedControl(&repaired, char)
			} else {
				repaired.WriteByte(char)
			}
		}
	}

	return repaired.String()
}

// ParseJSONWithRepair first parses strict JSON, then retries after RepairJSON.
func ParseJSONWithRepair(input string) (any, error) {
	value, originalErr := decodeJSON(input)
	if originalErr == nil {
		return value, nil
	}

	repaired := RepairJSON(input)
	if repaired == input {
		return nil, originalErr
	}
	return decodeJSON(repaired)
}

// Parse parses incomplete JSON. With no allow mask it permits every partial
// value, matching partial-json 0.1.7's default.
func Parse(input string, allowed ...Allow) (any, error) {
	allow := AllowAll
	if len(allowed) > 0 {
		allow = allowed[0]
	}
	trimmed := trimSpace(input)
	if trimmed == "" {
		return nil, errors.New("JSON input is empty")
	}

	parser := parser{input: trimmed, allow: allow}
	return parser.parseAny()
}

// ParseStreamingJSON parses tool-call arguments as they arrive. It never
// returns an error; invalid, empty, and incomplete-null input becomes {}.
func ParseStreamingJSON(input string) any {
	if trimSpace(input) == "" {
		return emptyObject()
	}

	if value, err := ParseJSONWithRepair(input); err == nil {
		return value
	}
	if value, err := Parse(input); err == nil {
		if value == nil {
			return emptyObject()
		}
		return value
	}
	if value, err := Parse(RepairJSON(input)); err == nil {
		if value == nil {
			return emptyObject()
		}
		return value
	}
	return emptyObject()
}

type parser struct {
	input string
	index int
	allow Allow
}

func (p *parser) parseAny() (any, error) {
	p.skipBlank()
	if p.index >= len(p.input) {
		return nil, p.partial("Unexpected end of input")
	}

	switch p.input[p.index] {
	case '"':
		return p.parseString()
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	}

	if p.acceptLiteral("null", AllowNull) {
		return nil, nil
	}
	if p.acceptLiteral("true", AllowBool) {
		return true, nil
	}
	if p.acceptLiteral("false", AllowBool) {
		return false, nil
	}
	if p.acceptLiteral("Infinity", AllowInfinity) {
		return math.Inf(1), nil
	}
	remaining := p.input[p.index:]
	if len(remaining) > 1 && p.acceptLiteral("-Infinity", AllowNegativeInfinity) {
		return math.Inf(-1), nil
	}
	if p.acceptLiteral("NaN", AllowNaN) {
		return math.NaN(), nil
	}
	return p.parseNumber()
}

func (p *parser) acceptLiteral(literal string, partial Allow) bool {
	remaining := p.input[p.index:]
	if strings.HasPrefix(remaining, literal) {
		p.index += len(literal)
		return true
	}
	if p.allow&partial != 0 && len(remaining) < len(literal) && strings.HasPrefix(literal, remaining) {
		p.index += len(literal)
		return true
	}
	return false
}

func (p *parser) parseString() (string, error) {
	start := p.index
	escaped := false
	p.index++
	for p.index < len(p.input) && (p.input[p.index] != '"' || (escaped && p.input[p.index-1] == '\\')) {
		if p.input[p.index] == '\\' {
			escaped = !escaped
		} else {
			escaped = false
		}
		p.index++
	}

	if p.index < len(p.input) && p.input[p.index] == '"' {
		p.index++
		value, err := decodeJSONString(p.input[start:p.index])
		if err != nil {
			return "", p.malformed(err.Error())
		}
		return value, nil
	}
	if p.allow&AllowString == 0 {
		return "", p.partial("Unterminated string literal")
	}

	end := p.index
	if escaped {
		end--
	}
	value, candidateErr := decodeJSONString(p.input[start:end] + `"`)
	if candidateErr == nil {
		return value, nil
	}

	lastSlash := strings.LastIndexByte(p.input, '\\')
	if lastSlash < start {
		return "", candidateErr
	}
	value, err := decodeJSONString(p.input[start:lastSlash] + `"`)
	if err != nil {
		return "", err
	}
	return value, nil
}

func (p *parser) parseObject() (any, error) {
	p.index++
	p.skipBlank()
	object := make(map[string]any)

	for {
		if p.index < len(p.input) && p.input[p.index] == '}' {
			p.index++
			return object, nil
		}
		p.skipBlank()
		if p.index >= len(p.input) {
			if p.allow&AllowObject != 0 {
				return object, nil
			}
			return nil, p.partial("Expected '}' at end of object")
		}

		key, err := p.parseString()
		if err != nil {
			if p.allow&AllowObject != 0 {
				return object, nil
			}
			return nil, p.partial("Expected '}' at end of object")
		}
		p.skipBlank()
		p.index++

		value, err := p.parseAny()
		if err != nil {
			if p.allow&AllowObject != 0 {
				return object, nil
			}
			return nil, p.partial("Expected '}' at end of object")
		}
		object[key] = value
		p.skipBlank()
		if p.index < len(p.input) && p.input[p.index] == ',' {
			p.index++
		}
	}
}

func (p *parser) parseArray() (any, error) {
	p.index++
	array := make([]any, 0)

	for {
		if p.index < len(p.input) && p.input[p.index] == ']' {
			p.index++
			return array, nil
		}
		value, err := p.parseAny()
		if err != nil {
			if p.allow&AllowArray != 0 {
				return array, nil
			}
			return nil, p.partial("Expected ']' at end of array")
		}
		array = append(array, value)
		p.skipBlank()
		if p.index < len(p.input) && p.input[p.index] == ',' {
			p.index++
		}
	}
}

func (p *parser) parseNumber() (any, error) {
	if p.index == 0 {
		if p.input == "-" {
			return nil, p.malformed("Not sure what '-' is")
		}
		if value, err := decodeJSON(p.input); err == nil {
			return value, nil
		}
		if p.allow&AllowNumber != 0 {
			if exponent := strings.LastIndexByte(p.input, 'e'); exponent >= 0 {
				if value, err := decodeJSON(p.input[:exponent]); err == nil {
					return value, nil
				}
			}
		}
		return nil, p.malformed("Invalid number")
	}

	start := p.index
	if p.input[p.index] == '-' {
		p.index++
	}
	for p.index < len(p.input) && !strings.ContainsRune(",]}", rune(p.input[p.index])) {
		p.index++
	}
	if p.index == len(p.input) && p.allow&AllowNumber == 0 {
		return nil, p.partial("Unterminated number literal")
	}

	token := p.input[start:p.index]
	if value, err := decodeJSON(token); err == nil {
		return value, nil
	}
	if token == "-" {
		return nil, p.partial("Not sure what '-' is")
	}
	if exponent := strings.LastIndexByte(p.input, 'e'); exponent >= start {
		if value, err := decodeJSON(p.input[start:exponent]); err == nil {
			return value, nil
		}
	}
	return nil, p.malformed("Invalid number")
}

func (p *parser) skipBlank() {
	for p.index < len(p.input) && strings.ContainsRune(" \n\r\t", rune(p.input[p.index])) {
		p.index++
	}
}

func (p *parser) partial(message string) error {
	return &PartialJSONError{Message: message, Position: p.index}
}

func (p *parser) malformed(message string) error {
	return &MalformedJSONError{Message: message, Position: p.index}
}

func decodeJSON(input string) (any, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(input))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	return numbersToFloat64(value)
}

func decodeJSONString(input string) (string, error) {
	value, err := decodeJSON(input)
	if err != nil {
		return "", err
	}
	stringValue, ok := value.(string)
	if !ok {
		return "", errors.New("value is not a string")
	}
	return stringValue, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected data after JSON value")
	}
	return err
}

func numbersToFloat64(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		result, err := strconv.ParseFloat(string(value), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return result, nil
	case []any:
		for i := range value {
			converted, err := numbersToFloat64(value[i])
			if err != nil {
				return nil, err
			}
			value[i] = converted
		}
	case map[string]any:
		for key, item := range value {
			converted, err := numbersToFloat64(item)
			if err != nil {
				return nil, err
			}
			value[key] = converted
		}
	}
	return value, nil
}

func isHex4(input string) bool {
	if len(input) != 4 {
		return false
	}
	for i := range 4 {
		char := input[i]
		switch {
		case '0' <= char && char <= '9':
		case 'a' <= char && char <= 'f':
		case 'A' <= char && char <= 'F':
		default:
			return false
		}
	}
	return true
}

func writeEscapedControl(output *strings.Builder, char byte) {
	switch char {
	case '\b':
		output.WriteString(`\b`)
	case '\f':
		output.WriteString(`\f`)
	case '\n':
		output.WriteString(`\n`)
	case '\r':
		output.WriteString(`\r`)
	case '\t':
		output.WriteString(`\t`)
	default:
		fmt.Fprintf(output, `\u%04x`, char)
	}
}

func trimSpace(input string) string {
	return strings.TrimFunc(input, isECMAScriptWhitespace)
}

func isECMAScriptWhitespace(char rune) bool {
	switch char {
	case '\u0009', '\u000b', '\u000c', '\u0020', '\u00a0', '\u1680',
		'\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff',
		'\u000a', '\u000d':
		return true
	default:
		return '\u2000' <= char && char <= '\u200a'
	}
}

func emptyObject() map[string]any {
	return map[string]any{}
}
