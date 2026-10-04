package jsonschema

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// Schema contains a JSON Schema value without imposing a Go-side schema model.
// The zero value is the valid unconstrained schema {}.
type Schema json.RawMessage

// MarshalJSON returns the stored schema bytes unchanged.
func (s Schema) MarshalJSON() ([]byte, error) {
	if len(s) == 0 {
		return []byte("{}"), nil
	}
	if !json.Valid(s) {
		return nil, fmt.Errorf("jsonschema: invalid schema JSON")
	}
	return bytes.Clone(s), nil
}

// UnmarshalJSON retains the input representation so schemas received from JS
// extensions or MCP can pass through without a Go-side rewrite.
func (s *Schema) UnmarshalJSON(data []byte) error {
	if !json.Valid(data) {
		return fmt.Errorf("jsonschema: invalid schema JSON")
	}
	*s = bytes.Clone(data)
	return nil
}

type orderedMember = jsonwire.OrderedMember

type orderedObject = jsonwire.OrderedObject
