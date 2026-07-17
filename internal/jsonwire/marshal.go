package jsonwire

import (
	"bytes"
	"encoding/json"
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
