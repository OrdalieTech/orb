package jsonwire

import (
	"bytes"
	"encoding/json"
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
