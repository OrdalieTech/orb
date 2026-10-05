package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

type jsonMember struct {
	name  string
	value json.RawMessage
}

type orderedObject struct {
	members []jsonMember
}

// parseOrderedObject keeps a record's members in order with their exact
// bytes, which share data: raw JSON is replaced, never changed in place.
func parseOrderedObject(data []byte) (*orderedObject, error) {
	if !jsonwire.Valid(data) {
		return nil, errors.New("session: invalid JSON record")
	}
	if trimmed := bytes.TrimLeft(data, " \t\r\n"); trimmed[0] != '{' {
		return nil, errors.New("session: JSON record is not an object")
	}
	object := &orderedObject{}
	jsonwire.EachMember(data, func(name, value []byte) bool {
		object.setOwned(memberName(name), value[:len(value):len(value)])
		return true
	})
	return object, nil
}

// memberName spares an allocation for the members every entry has.
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

func newOrderedObject(members ...jsonMember) *orderedObject {
	object := &orderedObject{members: make([]jsonMember, 0, len(members))}
	for _, member := range members {
		object.set(member.name, member.value)
	}
	return object
}

func member(name string, value json.RawMessage) jsonMember {
	return jsonMember{name: name, value: cloneRaw(value)}
}

func (object *orderedObject) get(name string) (json.RawMessage, bool) {
	if object == nil {
		return nil, false
	}
	for _, member := range object.members {
		if member.name == name {
			return cloneRaw(member.value), true
		}
	}
	return nil, false
}

// view returns a member without copying it: the entry parsed from object shares
// its largest fields (messages, data) instead of holding them twice. Members
// are replaced, never changed in place, and entries leave the manager cloned.
func (object *orderedObject) view(name string) (json.RawMessage, bool) {
	if object == nil {
		return nil, false
	}
	for _, member := range object.members {
		if member.name == name {
			return member.value, true
		}
	}
	return nil, false
}

func (object *orderedObject) set(name string, value json.RawMessage) {
	object.setOwned(name, cloneRaw(value))
}

// setOwned stores value without cloning; the caller must hand over ownership.
func (object *orderedObject) setOwned(name string, value json.RawMessage) {
	for index := range object.members {
		if object.members[index].name == name {
			object.members[index].value = value
			return
		}
	}
	object.members = append(object.members, jsonMember{name: name, value: value})
}

func (object *orderedObject) delete(name string) {
	for index := range object.members {
		if object.members[index].name == name {
			object.members = append(object.members[:index], object.members[index+1:]...)
			return
		}
	}
}

func (object *orderedObject) marshal() ([]byte, error) {
	if object == nil {
		return []byte("null"), nil
	}
	var output bytes.Buffer
	output.WriteByte('{')
	for index, member := range object.members {
		if index > 0 {
			output.WriteByte(',')
		}
		name, err := jsonwire.MarshalString(member.name)
		if err != nil {
			return nil, err
		}
		output.Write(name)
		output.WriteByte(':')
		if len(member.value) == 0 {
			output.WriteString("null")
		} else {
			output.Write(member.value)
		}
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}

func rawValue(value any) (json.RawMessage, error) {
	if raw, ok := value.(json.RawMessage); ok {
		if !jsonwire.Valid(raw) {
			return nil, fmt.Errorf("session: invalid raw JSON")
		}
		return cloneRaw(raw), nil
	}
	encoded, err := ai.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func mustRawString(value string) json.RawMessage {
	encoded, err := jsonwire.MarshalString(value)
	if err != nil {
		panic(err)
	}
	return json.RawMessage(encoded)
}

func rawInt(value int64) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(value, 10))
}

func rawNumber(value float64) json.RawMessage {
	encoded, err := ai.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func rawBool(value bool) json.RawMessage {
	return json.RawMessage(strconv.FormatBool(value))
}

func rawStringArray(values []string) json.RawMessage {
	var output bytes.Buffer
	output.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			output.WriteByte(',')
		}
		output.Write(mustRawString(value))
	}
	output.WriteByte(']')
	return output.Bytes()
}

func rawNull() json.RawMessage {
	return json.RawMessage("null")
}

func decodeString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	value, err := jsonwire.UnmarshalString(bytes.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	return value, true
}

func decodeInt(raw json.RawMessage) (int64, bool) {
	value, ok := decodeNumber(raw)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return 0, false
	}
	if value < math.MinInt64 || value >= -float64(math.MinInt64) {
		return 0, false
	}
	return int64(value), true
}

// decodeNumber reads a JSON number; a plain integer skips the decoder, which
// rounds it the same way.
func decodeNumber(raw json.RawMessage) (float64, bool) {
	if digits := bytes.TrimPrefix(raw, []byte("-")); len(digits) > 0 && (digits[0] != '0' || len(digits) == 1) &&
		!bytes.ContainsFunc(digits, func(r rune) bool { return r < '0' || r > '9' }) {
		value, err := strconv.ParseFloat(string(raw), 64)
		return value, err == nil
	}
	var value float64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func decodeBool(raw json.RawMessage) (*bool, bool) {
	var value bool
	switch string(raw) {
	case "true":
		value = true
	case "false":
	default:
		if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
			return nil, false
		}
	}
	return &value, true
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
