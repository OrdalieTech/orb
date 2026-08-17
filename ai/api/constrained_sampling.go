package api

import (
	"encoding/json"
	"fmt"

	"github.com/OrdalieTech/orb/internal/jsonschema"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// unsupportedStrictSchemaError marks a schema that cannot be expressed in the
// strict subset provider constrained sampling accepts. Only this class of
// failure downgrades to "no strict sampling"; anything else propagates.
type unsupportedStrictSchemaError struct{ reason string }

func (err *unsupportedStrictSchemaError) Error() string { return err.reason }

func unsupportedStrict(format string, arguments ...any) error {
	return &unsupportedStrictSchemaError{reason: fmt.Sprintf(format, arguments...)}
}

// Declaration order matters: the first key present names the error.
var unsupportedStrictSchemaKeys = []string{
	"$ref", "$defs", "definitions", "allOf", "oneOf", "patternProperties",
	"dependentSchemas", "dependencies", "unevaluatedProperties", "propertyNames",
	"contains", "prefixItems", "not", "if", "then", "else",
}

// isStructuredStrictSchema reports whether a schema describes an object or an
// array, which strict unions cannot contain.
func isStructuredStrictSchema(schema any) bool {
	object, ok := schema.(jsonwire.OrderedObject)
	if !ok {
		return false
	}
	switch value, _ := object.Value("type"); typed := value.(type) {
	case string:
		if typed == "object" || typed == "array" {
			return true
		}
	case orderedJSONArray:
		for _, entry := range typed {
			if entry == "object" || entry == "array" {
				return true
			}
		}
	}
	if _, ok := object.Value("properties"); ok {
		return true
	}
	_, ok = object.Value("items")
	return ok
}

func strictSchemaAllowsNull(schema any) bool {
	object, ok := schema.(jsonwire.OrderedObject)
	if !ok {
		return false
	}
	schemaType, _ := object.Value("type")
	if schemaType == "null" || containsStrictSchemaEntry(schemaType, "null") {
		return true
	}
	if constant, ok := object.Value("const"); ok && constant == nil {
		return true
	}
	enum, _ := object.Value("enum")
	if containsStrictSchemaEntry(enum, nil) {
		return true
	}
	variants, _ := object.Value("anyOf")
	list, _ := variants.(orderedJSONArray)
	for _, variant := range list {
		if strictSchemaAllowsNull(variant) {
			return true
		}
	}
	return false
}

func containsStrictSchemaEntry(value, wanted any) bool {
	list, _ := value.(orderedJSONArray)
	for _, entry := range list {
		if entry == wanted {
			return true
		}
	}
	return false
}

// makeStrictJSONSchemaNode rewrites one schema node in place: every property
// becomes required, optional properties gain a null union, and additional
// properties are closed off.
func makeStrictJSONSchemaNode(schema any) (any, error) {
	object, ok := schema.(jsonwire.OrderedObject)
	if !ok {
		return nil, unsupportedStrict("boolean schemas are unsupported")
	}
	for _, key := range unsupportedStrictSchemaKeys {
		if _, present := object.Value(key); present {
			return nil, unsupportedStrict("%s schemas are unsupported", key)
		}
	}

	if value, present := object.Value("anyOf"); present {
		variants, ok := value.(orderedJSONArray)
		if !ok || len(variants) == 0 {
			return nil, unsupportedStrict("anyOf must contain at least one schema")
		}
		for index, variant := range variants {
			if isStructuredStrictSchema(variant) {
				return nil, unsupportedStrict("object and array unions are unsupported")
			}
			converted, err := makeStrictJSONSchemaNode(variant)
			if err != nil {
				return nil, err
			}
			variants[index] = converted
		}
	}

	if value, present := object.Value("items"); present {
		if _, isTuple := value.(orderedJSONArray); isTuple {
			return nil, unsupportedStrict("tuple schemas are unsupported")
		}
		items, err := makeStrictJSONSchemaNode(value)
		if err != nil {
			return nil, err
		}
		object.Set("items", items)
	}

	schemaType, _ := object.Value("type")
	rawProperties, hasProperties := object.Value("properties")
	if schemaType != "object" {
		if hasProperties {
			return nil, unsupportedStrict("properties require type object")
		}
		return object, nil
	}
	if additional, present := object.Value("additionalProperties"); present && additional != false {
		return nil, unsupportedStrict("schema-valued or true additionalProperties is unsupported")
	}
	properties, _ := rawProperties.(jsonwire.OrderedObject)
	if hasProperties && properties == nil {
		return nil, unsupportedStrict("object properties must be a schema map")
	}
	required, err := strictSchemaRequired(object, properties)
	if err != nil {
		return nil, err
	}

	names := make(orderedJSONArray, 0, len(properties))
	for index, member := range properties {
		names = append(names, member.Name)
		value, err := makeStrictJSONSchemaNode(member.Value)
		if err != nil {
			return nil, err
		}
		if _, isRequired := required[member.Name]; !isRequired && !strictSchemaAllowsNull(value) {
			value = jsonwire.OrderedObject{{
				Name:  "anyOf",
				Value: orderedJSONArray{value, jsonwire.OrderedObject{{Name: "type", Value: "null"}}},
			}}
		}
		properties[index].Value = value
	}
	object.Set("required", names)
	object.Set("additionalProperties", false)
	return object, nil
}

func strictSchemaRequired(object, properties jsonwire.OrderedObject) (map[string]struct{}, error) {
	raw, present := object.Value("required")
	list, ok := raw.(orderedJSONArray)
	if present && !ok {
		return nil, unsupportedStrict("object required must be a string array")
	}
	required := make(map[string]struct{}, len(list))
	for _, entry := range list {
		name, ok := entry.(string)
		if !ok {
			return nil, unsupportedStrict("object required must be a string array")
		}
		if _, declared := properties.Value(name); !declared {
			return nil, unsupportedStrict("required contains an unknown property")
		}
		required[name] = struct{}{}
	}
	return required, nil
}

// makeStrictJSONSchema converts a tool schema to the strict subset expected by
// provider constrained sampling.
func makeStrictJSONSchema(parameters jsonschema.Schema) (jsonschema.Schema, error) {
	raw, err := parameters.MarshalJSON()
	if err != nil {
		return nil, err
	}
	decoded, err := decodeOrderedJSON(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := decoded.(jsonwire.OrderedObject); !ok {
		return nil, unsupportedStrict("root schema must have type object")
	}
	converted, err := makeStrictJSONSchemaNode(decoded)
	if err != nil {
		return nil, err
	}
	if schemaType, _ := converted.(jsonwire.OrderedObject).Value("type"); schemaType != "object" {
		return nil, unsupportedStrict("root schema must have type object")
	}
	encoded, err := jsonwire.Marshal(converted)
	if err != nil {
		return nil, err
	}
	return jsonschema.Schema(json.RawMessage(encoded)), nil
}

// getJSONSchemaToolParameters returns the schema a provider should send: the
// strict rewrite when strict sampling is on, the original otherwise.
func getJSONSchemaToolParameters(parameters jsonschema.Schema, strict bool) (jsonschema.Schema, error) {
	if !strict {
		return parameters, nil
	}
	return makeStrictJSONSchema(parameters)
}
