package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

type googleJSONMember = jsonwire.OrderedMember

type googleJSONObject = jsonwire.OrderedObject
type googleJSONArray []any

func normalizeGoogleResponseSchema(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	value, err := decodeGoogleOrderedJSON(trimmed)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeGoogleSchema(value)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(normalized)
}

func decodeGoogleOrderedJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeGoogleJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("google schema contains multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func decodeGoogleJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := googleJSONObject{}
		for decoder.More() {
			name, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			value, err := decodeGoogleJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object = append(object, googleJSONMember{Name: name.(string), Value: value})
		}
		_, err := decoder.Token()
		return object, err
	case '[':
		array := googleJSONArray{}
		for decoder.More() {
			value, err := decodeGoogleJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err := decoder.Token()
		return array, err
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func normalizeGoogleSchema(value any) (googleJSONObject, error) {
	object, ok := value.(googleJSONObject)
	if !ok {
		return nil, errors.New("google response schema must be an object")
	}
	typeValue, _ := object.Value("type")
	anyOfValue, _ := object.Value("anyOf")
	if googleJSONValueTruthy(typeValue) && googleJSONValueTruthy(anyOfValue) {
		return nil, errors.New("type and anyOf cannot be both populated.") //nolint:staticcheck // Exact SDK text.
	}
	output := googleJSONObject{}
	if nullable, replacement := googleNullableAnyOf(object); replacement != nil {
		if nullable {
			output.Set("nullable", true)
		}
		object = replacement
	}
	if types, ok := object.Value("type"); ok {
		if list, isList := types.(googleJSONArray); isList {
			if err := flattenGoogleSchemaTypes(&output, list); err != nil {
				return nil, err
			}
		}
	}
	for _, field := range object {
		if field.Value == nil {
			continue
		}
		switch field.Name {
		case "type":
			if _, isList := field.Value.(googleJSONArray); isList {
				continue
			}
			typeName, ok := field.Value.(string)
			if !ok {
				return nil, errors.New("google response schema type must be a string")
			}
			if typeName == "null" {
				return nil, errors.New("type: null can not be the only possible type for the field.") //nolint:staticcheck // Exact SDK text.
			}
			output.Set("type", googleSchemaType(typeName))
		case "items":
			item, err := normalizeGoogleSchema(field.Value)
			if err != nil {
				return nil, err
			}
			output.Set(field.Name, item)
		case "anyOf":
			list, ok := field.Value.(googleJSONArray)
			if !ok {
				return nil, errors.New("google response schema anyOf must be an array")
			}
			normalized := googleJSONArray{}
			for _, item := range list {
				if googleSchemaIsNull(item) {
					output.Set("nullable", true)
					continue
				}
				schema, err := normalizeGoogleSchema(item)
				if err != nil {
					return nil, err
				}
				normalized = append(normalized, schema)
			}
			output.Set(field.Name, normalized)
		case "properties":
			properties, ok := field.Value.(googleJSONObject)
			if !ok {
				return nil, errors.New("google response schema properties must be an object")
			}
			normalized := googleJSONObject{}
			for _, property := range properties {
				schema, err := normalizeGoogleSchema(property.Value)
				if err != nil {
					return nil, err
				}
				normalized = append(normalized, googleJSONMember{Name: property.Name, Value: schema})
			}
			output.Set(field.Name, normalized)
		case "additionalProperties":
			continue
		default:
			output.Set(field.Name, field.Value)
		}
	}
	return output, nil
}

func googleNullableAnyOf(object googleJSONObject) (bool, googleJSONObject) {
	value, ok := object.Value("anyOf")
	if !ok {
		return false, nil
	}
	list, ok := value.(googleJSONArray)
	if !ok || len(list) != 2 {
		return false, nil
	}
	if googleSchemaIsNull(list[0]) {
		replacement, _ := list[1].(googleJSONObject)
		return true, replacement
	}
	if googleSchemaIsNull(list[1]) {
		replacement, _ := list[0].(googleJSONObject)
		return true, replacement
	}
	return false, nil
}

func googleSchemaIsNull(value any) bool {
	object, ok := value.(googleJSONObject)
	if !ok {
		return false
	}
	typeName, ok := object.Value("type")
	return ok && typeName == "null"
}

func flattenGoogleSchemaTypes(output *googleJSONObject, values googleJSONArray) error {
	types := make([]string, 0, len(values))
	for _, value := range values {
		typeName, ok := value.(string)
		if !ok {
			return errors.New("google response schema type array must contain strings")
		}
		if typeName == "null" {
			output.Set("nullable", true)
			continue
		}
		types = append(types, googleSchemaType(typeName))
	}
	if len(types) == 1 {
		output.Set("type", types[0])
		return nil
	}
	anyOf := make(googleJSONArray, 0, len(types))
	for _, typeName := range types {
		anyOf = append(anyOf, googleJSONObject{{Name: "type", Value: typeName}})
	}
	output.Set("anyOf", anyOf)
	return nil
}

func googleSchemaType(value string) string {
	value = strings.ToUpper(value)
	switch value {
	case "TYPE_UNSPECIFIED", "STRING", "NUMBER", "INTEGER", "BOOLEAN", "ARRAY", "OBJECT", "NULL":
		return value
	default:
		return "TYPE_UNSPECIFIED"
	}
}
