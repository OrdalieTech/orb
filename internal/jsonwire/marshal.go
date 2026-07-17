package jsonwire

import (
	"bytes"
	"encoding/json"
	"fmt"
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
		if unit, ok := decodeWTF8Surrogate(value[index:]); ok {
			if unit >= 0xd800 && unit <= 0xdbff {
				if next, nextOK := decodeWTF8Surrogate(value[index+3:]); nextOK && next >= 0xdc00 && next <= 0xdfff {
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

func decodeWTF8Surrogate(value string) (uint16, bool) {
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
