package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// parseObject keeps a JSON object record's members in order with their exact
// bytes, which it shares; nil when raw is not an object.
func parseObject(raw []byte) *jsonwire.RawObject {
	object, ok := jsonwire.ParseRawObject(raw)
	if !ok {
		return nil
	}
	return &object
}

func member(name string, value json.RawMessage) jsonwire.RawMember {
	return jsonwire.RawMember{Name: name, Value: value}
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
	return jsonwire.AppendString(nil, value)
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
	output := []byte{'['}
	for index, value := range values {
		if index > 0 {
			output = append(output, ',')
		}
		output = jsonwire.AppendString(output, value)
	}
	return append(output, ']')
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
