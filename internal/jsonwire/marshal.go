package jsonwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Marshal follows JSON.stringify's string escaping rather than encoding/json's
// HTML-safe defaults. JavaScript leaves <, >, &, U+2028, and U+2029 literal.
func Marshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	return restoreLineSeparators(encoded), nil
}

// MarshalString preserves WTF-8 encoded UTF-16 surrogates so Go can carry
// JavaScript strings produced by code-unit slicing through a JSON wire format.
func MarshalString(value string) ([]byte, error) {
	if utf8.ValidString(value) {
		return Marshal(value)
	}
	var output bytes.Buffer
	output.WriteByte('"')
	validStart := 0
	for index := 0; index < len(value); {
		character, size := utf8.DecodeRuneInString(value[index:])
		if character != utf8.RuneError || size > 1 {
			index += size
			continue
		}
		if index > validStart {
			encoded, err := Marshal(value[validStart:index])
			if err != nil {
				return nil, err
			}
			output.Write(encoded[1 : len(encoded)-1])
		}
		if unit, ok := DecodeWTF8Surrogate(value[index:]); ok {
			if unit >= 0xd800 && unit <= 0xdbff {
				if next, nextOK := DecodeWTF8Surrogate(value[index+3:]); nextOK && next >= 0xdc00 && next <= 0xdfff {
					output.WriteRune(utf16.DecodeRune(rune(unit), rune(next)))
					index += 6
					validStart = index
					continue
				}
			}
			fmt.Fprintf(&output, `\u%04x`, unit)
			index += 3
			validStart = index
			continue
		}
		encoded, err := Marshal(string(utf8.RuneError))
		if err != nil {
			return nil, err
		}
		output.Write(encoded[1 : len(encoded)-1])
		index++
		validStart = index
	}
	if validStart < len(value) {
		encoded, err := Marshal(value[validStart:])
		if err != nil {
			return nil, err
		}
		output.Write(encoded[1 : len(encoded)-1])
	}
	output.WriteByte('"')
	return output.Bytes(), nil
}

// UnmarshalString decodes a JSON string while retaining lone UTF-16
// surrogates as WTF-8 so MarshalString can reproduce JSON.stringify output.
func UnmarshalString(data []byte) (string, error) {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return "", fmt.Errorf("jsonwire: invalid JSON string")
	}
	var output bytes.Buffer
	for index := 1; index < len(data)-1; {
		character := data[index]
		if character != '\\' {
			if character < 0x20 || character == '"' {
				return "", fmt.Errorf("jsonwire: invalid character in JSON string")
			}
			runeValue, size := utf8.DecodeRune(data[index : len(data)-1])
			if runeValue == utf8.RuneError && size == 1 {
				output.WriteRune(utf8.RuneError)
				index++
				continue
			}
			output.Write(data[index : index+size])
			index += size
			continue
		}

		if index+1 >= len(data)-1 {
			return "", fmt.Errorf("jsonwire: incomplete JSON escape")
		}
		switch escaped := data[index+1]; escaped {
		case '"', '\\', '/':
			output.WriteByte(escaped)
			index += 2
		case 'b':
			output.WriteByte('\b')
			index += 2
		case 'f':
			output.WriteByte('\f')
			index += 2
		case 'n':
			output.WriteByte('\n')
			index += 2
		case 'r':
			output.WriteByte('\r')
			index += 2
		case 't':
			output.WriteByte('\t')
			index += 2
		case 'u':
			unit, err := decodeEscapedCodeUnit(data, index, len(data)-1)
			if err != nil {
				return "", err
			}
			index += 6
			if unit >= 0xd800 && unit <= 0xdbff && index+6 <= len(data)-1 && data[index] == '\\' && data[index+1] == 'u' {
				next, nextErr := decodeEscapedCodeUnit(data, index, len(data)-1)
				if nextErr == nil && next >= 0xdc00 && next <= 0xdfff {
					output.WriteRune(utf16.DecodeRune(rune(unit), rune(next)))
					index += 6
					continue
				}
			}
			if unit >= 0xd800 && unit <= 0xdfff {
				writeWTF8CodeUnit(&output, unit)
			} else {
				output.WriteRune(rune(unit))
			}
		default:
			return "", fmt.Errorf("jsonwire: invalid JSON escape %q", escaped)
		}
	}
	return output.String(), nil
}

// UnmarshalStringToken decodes the first JSON string in a segment consumed by
// json.Decoder.Token. The segment may include separators and whitespace.
func UnmarshalStringToken(data []byte) (string, error) {
	start := bytes.IndexByte(data, '"')
	if start < 0 {
		return "", fmt.Errorf("jsonwire: JSON string token is missing")
	}
	escaped := false
	for index := start + 1; index < len(data); index++ {
		switch {
		case escaped:
			escaped = false
		case data[index] == '\\':
			escaped = true
		case data[index] == '"':
			return UnmarshalString(data[start : index+1])
		}
	}
	return "", fmt.Errorf("jsonwire: JSON string token is incomplete")
}

func decodeEscapedCodeUnit(data []byte, index, end int) (uint16, error) {
	if index+6 > end || data[index] != '\\' || data[index+1] != 'u' {
		return 0, fmt.Errorf("jsonwire: incomplete Unicode escape")
	}
	value, err := strconv.ParseUint(string(data[index+2:index+6]), 16, 16)
	if err != nil {
		return 0, fmt.Errorf("jsonwire: invalid Unicode escape: %w", err)
	}
	return uint16(value), nil
}

func writeWTF8CodeUnit(output *bytes.Buffer, unit uint16) {
	output.WriteByte(0xe0 | byte(unit>>12))
	output.WriteByte(0x80 | byte(unit>>6)&0x3f)
	output.WriteByte(0x80 | byte(unit)&0x3f)
}

// DecodeWTF8Surrogate returns the leading UTF-16 surrogate encoded as WTF-8.
func DecodeWTF8Surrogate(value string) (uint16, bool) {
	if len(value) < 3 || value[0] != 0xed || value[1] < 0xa0 || value[1] > 0xbf || value[2] < 0x80 || value[2] > 0xbf {
		return 0, false
	}
	unit := uint16(value[0]&0x0f)<<12 | uint16(value[1]&0x3f)<<6 | uint16(value[2]&0x3f)
	return unit, unit >= 0xd800 && unit <= 0xdfff
}

func restoreLineSeparators(data []byte) []byte {
	var output bytes.Buffer
	output.Grow(len(data))
	inString := false
	for index := 0; index < len(data); {
		char := data[index]
		if char == '"' {
			inString = !inString
			output.WriteByte(char)
			index++
			continue
		}
		if !inString || char != '\\' {
			output.WriteByte(char)
			index++
			continue
		}

		slashEnd := index
		for slashEnd < len(data) && data[slashEnd] == '\\' {
			slashEnd++
		}
		slashCount := slashEnd - index
		if slashCount%2 == 1 && slashEnd+5 <= len(data) && data[slashEnd] == 'u' {
			code := data[slashEnd+1 : slashEnd+5]
			if bytes.Equal(code, []byte("2028")) || bytes.Equal(code, []byte("2029")) {
				output.Write(data[index : slashEnd-1])
				if code[3] == '8' {
					output.WriteRune('\u2028')
				} else {
					output.WriteRune('\u2029')
				}
				index = slashEnd + 5
				continue
			}
		}

		output.Write(data[index:slashEnd])
		index = slashEnd
		if slashCount%2 == 1 && index < len(data) {
			output.WriteByte(data[index])
			index++
		}
	}
	return output.Bytes()
}
