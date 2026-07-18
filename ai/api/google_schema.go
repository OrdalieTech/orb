package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/OrdalieTech/pi-go/ai"
)

type googleJSONMember struct {
	name  string
	value any
}

type googleJSONObject []googleJSONMember
type googleJSONArray []any

func (object googleJSONObject) MarshalJSON() ([]byte, error) {
	var output bytes.Buffer
	output.WriteByte('{')
	for index, member := range object {
		if index > 0 {
			output.WriteByte(',')
		}
		name, err := ai.Marshal(member.name)
		if err != nil {
			return nil, err
		}
		value, err := ai.Marshal(member.value)
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
			object = append(object, googleJSONMember{name: name.(string), value: value})
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
	typeValue, _ := object.value("type")
	anyOfValue, _ := object.value("anyOf")
	if googleJSONValueTruthy(typeValue) && googleJSONValueTruthy(anyOfValue) {
		return nil, errors.New("type and anyOf cannot be both populated.") //nolint:staticcheck // Exact SDK text.
	}
	output := googleJSONObject{}
	if nullable, replacement := googleNullableAnyOf(object); replacement != nil {
		if nullable {
			output.set("nullable", true)
		}
		object = replacement
	}
	if types, ok := object.value("type"); ok {
		if list, isList := types.(googleJSONArray); isList {
			if err := flattenGoogleSchemaTypes(&output, list); err != nil {
				return nil, err
			}
		}
	}
	for _, field := range object {
		if field.value == nil {
			continue
		}
		switch field.name {
		case "type":
			if _, isList := field.value.(googleJSONArray); isList {
				continue
			}
			typeName, ok := field.value.(string)
			if !ok {
				return nil, errors.New("google response schema type must be a string")
			}
			if typeName == "null" {
				return nil, errors.New("type: null can not be the only possible type for the field.") //nolint:staticcheck // Exact SDK text.
			}
			output.set("type", googleSchemaType(typeName))
		case "items":
			item, err := normalizeGoogleSchema(field.value)
			if err != nil {
				return nil, err
			}
			output.set(field.name, item)
		case "anyOf":
			list, ok := field.value.(googleJSONArray)
			if !ok {
				return nil, errors.New("google response schema anyOf must be an array")
			}
			normalized := googleJSONArray{}
			for _, item := range list {
				if googleSchemaIsNull(item) {
					output.set("nullable", true)
					continue
				}
				schema, err := normalizeGoogleSchema(item)
				if err != nil {
					return nil, err
				}
				normalized = append(normalized, schema)
			}
			output.set(field.name, normalized)
		case "properties":
			properties, ok := field.value.(googleJSONObject)
			if !ok {
				return nil, errors.New("google response schema properties must be an object")
			}
			normalized := googleJSONObject{}
			for _, property := range properties {
				schema, err := normalizeGoogleSchema(property.value)
				if err != nil {
					return nil, err
				}
				normalized = append(normalized, googleJSONMember{name: property.name, value: schema})
			}
			output.set(field.name, normalized)
		case "additionalProperties":
			continue
		default:
			output.set(field.name, field.value)
		}
	}
	return output, nil
}

func googleNullableAnyOf(object googleJSONObject) (bool, googleJSONObject) {
	value, ok := object.value("anyOf")
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
	typeName, ok := object.value("type")
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
			output.set("nullable", true)
			continue
		}
		types = append(types, googleSchemaType(typeName))
	}
	if len(types) == 1 {
		output.set("type", types[0])
		return nil
	}
	anyOf := make(googleJSONArray, 0, len(types))
	for _, typeName := range types {
		anyOf = append(anyOf, googleJSONObject{{name: "type", value: typeName}})
	}
	output.set("anyOf", anyOf)
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

func (object googleJSONObject) value(name string) (any, bool) {
	for _, field := range object {
		if field.name == name {
			return field.value, true
		}
	}
	return nil, false
}

func (object *googleJSONObject) set(name string, value any) {
	for index := range *object {
		if (*object)[index].name == name {
			(*object)[index].value = value
			return
		}
	}
	*object = append(*object, googleJSONMember{name: name, value: value})
}

func (object *googleJSONObject) delete(name string) {
	for index := range *object {
		if (*object)[index].name == name {
			*object = append((*object)[:index], (*object)[index+1:]...)
			return
		}
	}
}
