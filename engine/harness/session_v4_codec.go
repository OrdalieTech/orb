package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
)

// SessionV4Header is the identity a v4 session file is created with.
type SessionV4Header struct {
	ID                      string
	CreatedAt               int64
	CWD                     string
	ParentSessionID         *string
	LegacyParentSessionPath *string
	Metadata                json.RawMessage
}

// parseV4Object decodes a JSON object preserving top-level member order.
// Well-formed object lines — the hot path — are validated by the decode walk
// itself; the json.Valid scan runs only on failures so the original error
// split ("is not valid JSON" vs "is not a JSON object") is preserved.
func parseV4Object(data []byte) ([]harnessJSONMember, map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	malformed := func() ([]harnessJSONMember, map[string]json.RawMessage, error) {
		if !json.Valid(trimmed) {
			return nil, nil, errors.New("is not valid JSON")
		}
		return nil, nil, errors.New("is not a JSON object")
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return malformed()
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if _, err := decoder.Token(); err != nil {
		return malformed()
	}
	members := make([]harnessJSONMember, 0, 8)
	byName := make(map[string]json.RawMessage, 8)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return malformed()
		}
		name, ok := token.(string)
		if !ok {
			return malformed()
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return malformed()
		}
		members = append(members, harnessJSONMember{name: name, value: value})
		byName[name] = value
	}
	// Consume the closing brace and require the input to end exactly there:
	// this replaces the up-front json.Valid prescan's trailing-data rejection.
	if token, err := decoder.Token(); err != nil {
		return malformed()
	} else if delimiter, ok := token.(json.Delim); !ok || delimiter != '}' {
		return malformed()
	}
	if decoder.InputOffset() != int64(len(trimmed)) {
		return malformed()
	}
	return members, byName, nil
}

func v4String(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value string
	ok := decodeHarnessStringInto(raw, &value)
	return value, ok
}

func v4SafeInteger(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	if value != math.Trunc(value) || math.Abs(value) > float64(1<<53-1) {
		return 0, false
	}
	return int64(value), true
}
