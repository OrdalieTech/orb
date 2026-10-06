package jsonwire

import (
	"bytes"
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

// OrderedMember is one name/value pair of an OrderedObject.
type OrderedMember struct {
	Name  string
	Value any
}

// OrderedObject is a JSON object that marshals its members in insertion order,
// matching JavaScript's object key ordering on the wire.
type OrderedObject []OrderedMember

func (object OrderedObject) MarshalJSON() ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, member := range object {
		if index > 0 {
			output.WriteByte(',')
		}
		name, err := Marshal(member.Name)
		if err != nil {
			return nil, err
		}
		value, err := Marshal(member.Value)
		if err != nil {
			return nil, err
		}
		output.Write(name)
		output.WriteByte(':')
		output.Write(value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

// Value returns the value of the first member with the given name.
func (object OrderedObject) Value(name string) (any, bool) {
	for _, field := range object {
		if field.Name == name {
			return field.Value, true
		}
	}
	return nil, false
}

// Set replaces the value of the first member with the given name in place, or
// appends a new member.
func (object *OrderedObject) Set(name string, value any) {
	for index := range *object {
		if (*object)[index].Name == name {
			(*object)[index].Value = value
			return
		}
	}
	*object = append(*object, OrderedMember{Name: name, Value: value})
}

// Delete removes the first member with the given name, if present.
func (object *OrderedObject) Delete(name string) {
	for index := range *object {
		if (*object)[index].Name == name {
			*object = append((*object)[:index], (*object)[index+1:]...)
			return
		}
	}
}

// RawMember is one member of a RawObject: a name and its exact JSON bytes.
type RawMember struct {
	Name  string
	Value json.RawMessage
}

// RawObject is a JSON object kept as its members' exact bytes in order, so
// rewriting some members preserves the others byte for byte. Values are
// replaced, never changed in place, so objects may share them.
type RawObject []RawMember

// ParseRawObject reads the JSON object data, sharing its bytes. A repeated
// name keeps its first position and its last value, as JSON.parse does.
func ParseRawObject(data []byte) (RawObject, bool) {
	if index := skipSpace(data, 0); index >= len(data) || data[index] != '{' || !Valid(data) {
		return nil, false
	}
	object := RawObject{}
	EachMember(data, func(name, value []byte) bool {
		object.Set(memberName(name), value[:len(value):len(value)])
		return true
	})
	return object, true
}

// memberName spares an allocation for the members every session entry has.
func memberName(name []byte) string {
	switch string(name) {
	case "type":
		return "type"
	case "id":
		return "id"
	case "parentId":
		return "parentId"
	case "timestamp":
		return "timestamp"
	case "message":
		return "message"
	}
	return string(name)
}

// Get returns the value of the member name, sharing its bytes.
func (object RawObject) Get(name string) (json.RawMessage, bool) {
	for _, member := range object {
		if member.Name == name {
			return member.Value, true
		}
	}
	return nil, false
}

// Set replaces the value of the member name in place, or appends the member.
func (object *RawObject) Set(name string, value json.RawMessage) {
	for index := range *object {
		if (*object)[index].Name == name {
			(*object)[index].Value = value
			return
		}
	}
	*object = append(*object, RawMember{Name: name, Value: value})
}

// Delete removes the member name, if present.
func (object *RawObject) Delete(name string) {
	for index := range *object {
		if (*object)[index].Name == name {
			*object = append((*object)[:index], (*object)[index+1:]...)
			return
		}
	}
}

// MarshalJSON writes the members in order into a fresh buffer with room for a
// trailing line feed; an empty value is written as null.
func (object RawObject) MarshalJSON() ([]byte, error) {
	size := 3
	for _, member := range object {
		size += len(member.Name) + max(len(member.Value), 4) + 4
	}
	output := append(make([]byte, 0, size), '{')
	for index, member := range object {
		if index > 0 {
			output = append(output, ',')
		}
		output = append(AppendString(output, member.Name), ':')
		if len(member.Value) == 0 {
			output = append(output, "null"...)
		} else {
			output = append(output, member.Value...)
		}
	}
	return append(output, '}'), nil
}

// EachMember calls visit with the name, unescaped, and the raw value of each
// member of the JSON object data until visit returns false. data must be
// valid JSON; a value that is not an object has no members. Scanning
// validated JSON this way skips the allocations of decoding it again.
func EachMember(data []byte, visit func(name, value []byte) bool) {
	index := skipSpace(data, 0)
	if index >= len(data) || data[index] != '{' {
		return
	}
	for index = skipSpace(data, index+1); index < len(data) && data[index] == '"'; {
		nameEnd := skipValue(data, index)
		valueStart := skipSpace(data, skipSpace(data, nameEnd)+1)
		valueEnd := skipValue(data, valueStart)
		name := data[index+1 : nameEnd-1]
		if bytes.IndexByte(name, '\\') >= 0 {
			decoded, _ := UnmarshalString(data[index:nameEnd])
			name = []byte(decoded)
		}
		if !visit(name, data[valueStart:valueEnd]) {
			return
		}
		if index = skipSpace(data, valueEnd); index < len(data) && data[index] == ',' {
			index = skipSpace(data, index+1)
		}
	}
}

// Elements returns the raw elements of the JSON array data, which must be
// valid JSON; a value that is not an array has none.
func Elements(data []byte) []json.RawMessage {
	index := skipSpace(data, 0)
	if index >= len(data) || data[index] != '[' {
		return nil
	}
	var elements []json.RawMessage
	for index = skipSpace(data, index+1); index < len(data) && data[index] != ']'; {
		end := skipValue(data, index)
		elements = append(elements, data[index:end])
		if index = skipSpace(data, end); index < len(data) && data[index] == ',' {
			index = skipSpace(data, index+1)
		}
	}
	return elements
}

func skipSpace(data []byte, index int) int {
	for index < len(data) && (data[index] == ' ' || data[index] == '\t' || data[index] == '\n' || data[index] == '\r') {
		index++
	}
	return index
}

// skipValue returns the index just past the valid JSON value at index.
func skipValue(data []byte, index int) int {
	depth := 0
	for ; index < len(data); index++ {
		switch data[index] {
		case '"':
			for index++; index < len(data) && data[index] != '"'; index++ {
				if data[index] == '\\' {
					index++
				}
			}
			if depth == 0 {
				return index + 1
			}
		case '{', '[':
			depth++
		case '}', ']':
			if depth--; depth == 0 {
				return index + 1
			}
			if depth < 0 {
				return index
			}
		case ',', ' ', '\t', '\n', '\r':
			if depth == 0 {
				return index
			}
		}
	}
	return index
}

// Members decodes the JSON object data into its raw members as json.Unmarshal
// into a map[string]json.RawMessage does, sharing data's bytes. It reports
// false, leaving data to encoding/json, unless data is a valid object whose
// names are distinct and valid UTF-8.
func Members(data []byte) (map[string]json.RawMessage, bool) {
	if index := skipSpace(data, 0); index >= len(data) || data[index] != '{' || !Valid(data) {
		return nil, false
	}
	members, ok := map[string]json.RawMessage{}, true
	EachMember(data, func(name, value []byte) bool {
		_, duplicate := members[string(name)]
		members[string(name)] = value
		ok = !duplicate && utf8.Valid(name)
		return ok
	})
	return members, ok
}

// Valid reports whether data is one JSON value, as json.Valid does. It
// consults encoding/json only for input it does not accept itself: invalid,
// nested deeper than encoding/json allows, or escaping a UTF-16 surrogate.
func Valid(data []byte) bool {
	if _, end, ok := parseValue(data, skipSpace(data, 0), 0, false); ok && skipSpace(data, end) == len(data) {
		return true
	}
	return json.Valid(data)
}

// Decode decodes data as json.Unmarshal into an any does: objects as
// map[string]any, arrays as []any, numbers as float64. It reports false,
// leaving data to encoding/json, for anything it might decode differently:
// invalid JSON, duplicate names, strings that are not valid UTF-8 or escape a
// surrogate, and numbers out of float64's range.
func Decode(data []byte) (any, bool) {
	value, end, ok := parseValue(data, skipSpace(data, 0), 0, true)
	if !ok || skipSpace(data, end) != len(data) {
		return nil, false
	}
	return value, true
}

// maxDepth is encoding/json's nesting limit.
const maxDepth = 10000

// parseValue scans the JSON value at index, decoding it when decode is set,
// and returns the index past it.
func parseValue(data []byte, index, depth int, decode bool) (any, int, bool) {
	if index >= len(data) {
		return nil, index, false
	}
	switch data[index] {
	case '"':
		end, ok := scanString(data, index)
		if !ok || !decode {
			return nil, end, ok
		}
		text, ok := decodeString(data[index:end])
		return text, end, ok
	case '{', '[':
		if depth >= maxDepth {
			return nil, index, false
		}
		return parseContainer(data, index, depth+1, decode)
	case 't':
		return true, index + 4, bytes.HasPrefix(data[index:], []byte("true"))
	case 'f':
		return false, index + 5, bytes.HasPrefix(data[index:], []byte("false"))
	case 'n':
		return nil, index + 4, bytes.HasPrefix(data[index:], []byte("null"))
	}
	start := index
	if data[index] == '-' {
		index++
	}
	if index < len(data) && data[index] == '0' {
		index++
	} else if index = skipDigits(data, index); index == start || data[index-1] == '-' {
		return nil, index, false
	}
	if index < len(data) && data[index] == '.' {
		if index = skipDigits(data, index+1); data[index-1] == '.' {
			return nil, index, false
		}
	}
	if index < len(data) && (data[index] == 'e' || data[index] == 'E') {
		if index++; index < len(data) && (data[index] == '+' || data[index] == '-') {
			index++
		}
		exponent := index
		if index = skipDigits(data, index); index == exponent {
			return nil, index, false
		}
	}
	if !decode {
		return nil, index, true
	}
	number, err := strconv.ParseFloat(string(data[start:index]), 64)
	return number, index, err == nil
}

// parseContainer scans the object or array at index.
func parseContainer(data []byte, index, depth int, decode bool) (any, int, bool) {
	object, closing := data[index] == '{', byte(']')
	var members map[string]any
	var elements []any
	if object {
		closing = '}'
		if decode {
			members = map[string]any{}
		}
	} else if decode {
		elements = []any{}
	}
	result := func() any {
		if object {
			return members
		}
		return elements
	}
	if index = skipSpace(data, index+1); index < len(data) && data[index] == closing {
		return result(), index + 1, true
	}
	for {
		var name string
		if object {
			if index >= len(data) || data[index] != '"' {
				return nil, index, false
			}
			end, ok := scanString(data, index)
			if ok && decode {
				name, ok = decodeString(data[index:end])
				// encoding/json keeps the last of duplicate names; left to it.
				_, duplicate := members[name]
				ok = ok && !duplicate
			}
			if index = skipSpace(data, end); !ok || index >= len(data) || data[index] != ':' {
				return nil, index, false
			}
			index = skipSpace(data, index+1)
		}
		value, end, ok := parseValue(data, index, depth, decode)
		if !ok {
			return nil, end, false
		}
		if object && decode {
			members[name] = value
		} else if decode {
			elements = append(elements, value)
		}
		if index = skipSpace(data, end); index < len(data) && data[index] == ',' {
			index = skipSpace(data, index+1)
			continue
		}
		if index < len(data) && data[index] == closing {
			return result(), index + 1, true
		}
		return nil, index, false
	}
}

// scanString returns the index past the JSON string at index. Escapes of
// UTF-16 surrogates are refused: encoding/json replaces lone ones.
func scanString(data []byte, index int) (int, bool) {
	for index++; index < len(data); index++ {
		switch char := data[index]; {
		case char == '"':
			return index + 1, true
		case char < 0x20:
			return index, false
		case char == '\\':
			if index++; index >= len(data) {
				return index, false
			}
			switch data[index] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if index+4 >= len(data) {
					return index, false
				}
				unit, err := strconv.ParseUint(string(data[index+1:index+5]), 16, 16)
				if err != nil || unit >= 0xd800 && unit <= 0xdfff {
					return index, false
				}
				index += 4
			default:
				return index, false
			}
		}
	}
	return index, false
}

// decodeString decodes a scanned JSON string of valid UTF-8, the only kind
// encoding/json decodes as it is written.
func decodeString(raw []byte) (string, bool) {
	if !utf8.Valid(raw) {
		return "", false
	}
	if bytes.IndexByte(raw, '\\') < 0 {
		return string(raw[1 : len(raw)-1]), true
	}
	text, err := UnmarshalString(raw)
	return text, err == nil
}

// Stringified reports whether data is one JSON value spelled as
// JSON.stringify writes the value JSON.parse reads from it, so compacting or
// normalizing it changes nothing. It accepts a subset: no whitespace, strings
// escaping only quotes, backslashes and the named controls, integers of up to
// fifteen digits, and objects whose few names are distinct and do not start
// with a digit (JavaScript orders array indexes first).
func Stringified(data []byte) bool {
	end, ok := stringified(data, 0, 0)
	return ok && end == len(data)
}

func stringified(data []byte, index, depth int) (int, bool) {
	if index >= len(data) || depth > maxDepth {
		return index, false
	}
	switch char := data[index]; char {
	case '"':
		end, ok := scanString(data, index)
		if !ok || !utf8.Valid(data[index:end]) {
			return end, false
		}
		for at := index + 1; at < end-1; at++ {
			if data[at] == '\\' {
				if at++; data[at] == '/' || data[at] == 'u' {
					return end, false
				}
			}
		}
		return end, true
	case '{', '[':
		closing := byte(']')
		if char == '{' {
			closing = '}'
		}
		if index++; index < len(data) && data[index] == closing {
			return index + 1, true
		}
		var names [][]byte
		for {
			if char == '{' {
				end, ok := stringified(data, index, depth)
				if !ok || data[index] != '"' || end-index > 2 && data[index+1] >= '0' && data[index+1] <= '9' || len(names) == 32 {
					return end, false
				}
				for _, name := range names {
					if bytes.Equal(name, data[index:end]) {
						return end, false
					}
				}
				if names = append(names, data[index:end]); end >= len(data) || data[end] != ':' {
					return end, false
				}
				index = end + 1
			}
			end, ok := stringified(data, index, depth+1)
			switch {
			case !ok || end >= len(data):
				return end, false
			case data[end] == ',':
				index = end + 1
			case data[end] == closing:
				return end + 1, true
			default:
				return end, false
			}
		}
	case 't':
		return index + 4, bytes.HasPrefix(data[index:], []byte("true"))
	case 'f':
		return index + 5, bytes.HasPrefix(data[index:], []byte("false"))
	case 'n':
		return index + 4, bytes.HasPrefix(data[index:], []byte("null"))
	}
	start := index
	if data[index] == '-' {
		index++
	}
	end := skipDigits(data, index)
	if end == index || end-index > 15 || data[index] == '0' && (end-index > 1 || start < index) {
		return end, false
	}
	return end, end == len(data) || data[end] != '.' && data[end] != 'e' && data[end] != 'E'
}

func skipDigits(data []byte, index int) int {
	for index < len(data) && data[index] >= '0' && data[index] <= '9' {
		index++
	}
	return index
}
