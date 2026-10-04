package session

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync"

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

// orderedDecoders keeps decoders and their buffers across records.
var orderedDecoders = sync.Pool{New: func() any { return new(jsontext.Decoder) }}

// parseOrderedObject keeps a record's members in order with their exact
// bytes, scanning it once with jsontext: the token API of encoding/json
// allocated for every member.
func parseOrderedObject(data []byte) (*orderedObject, error) {
	decoder := orderedDecoders.Get().(*jsontext.Decoder)
	defer orderedDecoders.Put(decoder)
	decoder.Reset(bytes.NewReader(data), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	if token, err := decoder.ReadToken(); err != nil {
		return nil, err
	} else if token.Kind() != '{' {
		return nil, fmt.Errorf("session: JSON record is not an object")
	}
	object := &orderedObject{}
	for decoder.PeekKind() == '"' {
		rawName, err := decoder.ReadValue()
		if err != nil {
			return nil, err
		}
		name, err := jsonwire.UnmarshalString(rawName)
		if err != nil {
			return nil, err
		}
		value, err := decoder.ReadValue()
		if err != nil {
			return nil, err
		}
		object.setOwned(name, bytes.Clone(value))
	}
	if token, err := decoder.ReadToken(); err != nil {
		return nil, err
	} else if token.Kind() != '}' {
		return nil, fmt.Errorf("session: JSON object member name is not a string")
	}
	if _, err := decoder.ReadToken(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("session: multiple JSON values in one record")
		}
		return nil, err
	}
	return object, nil
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
		if !json.Valid(raw) {
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
	var value float64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value {
		return 0, false
	}
	if value < math.MinInt64 || value >= -float64(math.MinInt64) {
		return 0, false
	}
	return int64(value), true
}

func decodeNumber(raw json.RawMessage) (float64, bool) {
	var value float64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func decodeBool(raw json.RawMessage) (*bool, bool) {
	var value bool
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	return &value, true
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
