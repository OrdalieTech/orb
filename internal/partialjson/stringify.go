package partialjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

// StringifyStreamingJSON parses an incomplete value and serializes the
// currently available result with JSON.parse/JSON.stringify property order.
func StringifyStreamingJSON(input string) ([]byte, error) {
	value := parseStreamingJSONOrdered(input)
	var output bytes.Buffer
	if err := writeStreamingJSON(&output, value); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func parseStreamingJSONOrdered(input string) any {
	trimmed := trimSpace(input)
	if trimmed == "" {
		return orderedObject{}
	}
	repaired := RepairJSON(trimmed)
	candidates := []string{trimmed}
	if repaired != trimmed {
		candidates = append(candidates, repaired)
	}
	// Match upstream precedence: strict original, strict repaired, then the
	// same candidates through the permissive streaming parser.
	for _, candidate := range candidates {
		if !json.Valid([]byte(candidate)) {
			continue
		}
		value, err := (&parser{input: candidate, allow: AllowAll, preserveObjectOrder: true}).parseAny()
		if err == nil {
			if value == nil {
				return orderedObject{}
			}
			return value
		}
	}
	for _, candidate := range candidates {
		value, err := (&parser{input: candidate, allow: AllowAll, preserveObjectOrder: true}).parseAny()
		if err == nil {
			if value == nil {
				return orderedObject{}
			}
			return value
		}
	}
	return orderedObject{}
}

func writeStreamingJSON(output *bytes.Buffer, value any) error {
	switch value := value.(type) {
	case orderedObject:
		members := append(orderedObject(nil), value...)
		sort.SliceStable(members, func(left, right int) bool {
			leftIndex, leftIsIndex := streamingJSONArrayIndex(members[left].name)
			rightIndex, rightIsIndex := streamingJSONArrayIndex(members[right].name)
			if leftIsIndex && rightIsIndex {
				return leftIndex < rightIndex
			}
			return leftIsIndex && !rightIsIndex
		})
		output.WriteByte('{')
		for index, member := range members {
			if index > 0 {
				output.WriteByte(',')
			}
			name, err := jsonwire.Marshal(member.name)
			if err != nil {
				return err
			}
			output.Write(name)
			output.WriteByte(':')
			if err := writeStreamingJSON(output, member.value); err != nil {
				return err
			}
		}
		output.WriteByte('}')
		return nil
	case []any:
		output.WriteByte('[')
		for index, item := range value {
			if index > 0 {
				output.WriteByte(',')
			}
			if err := writeStreamingJSON(output, item); err != nil {
				return err
			}
		}
		output.WriteByte(']')
		return nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			output.WriteString("null")
			return nil
		}
		if value == 0 {
			output.WriteByte('0')
			return nil
		}
	}
	encoded, err := jsonwire.Marshal(value)
	if err != nil {
		return fmt.Errorf("partialjson: stringify: %w", err)
	}
	output.Write(encoded)
	return nil
}

func streamingJSONArrayIndex(name string) (uint64, bool) {
	if name == "0" {
		return 0, true
	}
	if name == "" || name[0] == '0' {
		return 0, false
	}
	value, err := strconv.ParseUint(name, 10, 32)
	if err != nil || value == math.MaxUint32 || strconv.FormatUint(value, 10) != name {
		return 0, false
	}
	return value, true
}
