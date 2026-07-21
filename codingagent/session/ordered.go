package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

type jsonMember struct {
	name  string
	value json.RawMessage
}

type orderedObject struct {
	members []jsonMember
}

func parseOrderedObject(data []byte) (*orderedObject, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, fmt.Errorf("session: JSON record is not an object")
	}

	object := &orderedObject{}
	for decoder.More() {
		nameStart := decoder.InputOffset()
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		_, ok := nameToken.(string)
		if !ok {
			return nil, fmt.Errorf("session: JSON object member name is not a string")
		}
		name, err := jsonwire.UnmarshalStringToken(data[nameStart:decoder.InputOffset()])
		if err != nil {
			return nil, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object.set(name, value)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
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

func (object *orderedObject) set(name string, value json.RawMessage) {
	value = cloneRaw(value)
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
