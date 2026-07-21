package jsonschema

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

// FromStruct derives a plain, inline JSON Schema from T. Fields are required
// unless their json tag contains omitempty/omitzero or their jsonschema tag
// contains optional. Supported jsonschema directives are description, enum,
// required, and optional.
func FromStruct[T any]() (Schema, error) {
	t := reflect.TypeFor[T]()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("jsonschema: %s is not a struct", t)
	}

	value, err := reflectSchema(t, make(map[reflect.Type]bool))
	if err != nil {
		return nil, err
	}
	b, err := jsonwire.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: marshal schema: %w", err)
	}
	return Schema(b), nil
}

func reflectSchema(t reflect.Type, stack map[reflect.Type]bool) (orderedObject, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Interface:
		return orderedObject{}, nil
	case reflect.Bool:
		return typedSchema("boolean"), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return typedSchema("integer"), nil
	case reflect.Float32, reflect.Float64:
		return typedSchema("number"), nil
	case reflect.String:
		return typedSchema("string"), nil
	case reflect.Array, reflect.Slice:
		if stack[t] {
			return nil, fmt.Errorf("jsonschema: recursive type %s", t)
		}
		stack[t] = true
		defer delete(stack, t)
		items, err := reflectSchema(t.Elem(), stack)
		if err != nil {
			return nil, err
		}
		return orderedObject{
			{Name: "type", Value: "array"},
			{Name: "items", Value: items},
		}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("jsonschema: map key %s is not a string", t.Key())
		}
		if stack[t] {
			return nil, fmt.Errorf("jsonschema: recursive type %s", t)
		}
		stack[t] = true
		defer delete(stack, t)
		values, err := reflectSchema(t.Elem(), stack)
		if err != nil {
			return nil, err
		}
		return orderedObject{
			{Name: "type", Value: "object"},
			{Name: "additionalProperties", Value: values},
		}, nil
	case reflect.Struct:
		return reflectStruct(t, stack)
	default:
		return nil, fmt.Errorf("jsonschema: unsupported type %s", t)
	}
}

func reflectStruct(t reflect.Type, stack map[reflect.Type]bool) (orderedObject, error) {
	if stack[t] {
		return nil, fmt.Errorf("jsonschema: recursive type %s", t)
	}
	stack[t] = true
	defer delete(stack, t)

	properties := orderedObject{}
	required := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		name, optional, skip := jsonField(field)
		if skip {
			continue
		}
		if field.Anonymous {
			tagName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if tagName == "" {
				return nil, fmt.Errorf("jsonschema: anonymous field %s needs an explicit json name", field.Name)
			}
		}

		options, err := parseFieldOptions(field)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: field %s: %w", field.Name, err)
		}
		if options.required != nil {
			optional = !*options.required
		}

		value, err := reflectSchema(field.Type, stack)
		if err != nil {
			return nil, fmt.Errorf("jsonschema: field %s: %w", field.Name, err)
		}
		if len(options.enum) > 0 {
			if dereference(field.Type).Kind() != reflect.String {
				return nil, fmt.Errorf("jsonschema: field %s: enum requires a string type", field.Name)
			}
			value = append(value, orderedMember{Name: "enum", Value: options.enum})
		}
		if options.description != "" {
			value = append(value, orderedMember{Name: "description", Value: options.description})
		}
		properties = append(properties, orderedMember{Name: name, Value: value})
		if !optional {
			required = append(required, name)
		}
	}

	value := orderedObject{{Name: "type", Value: "object"}}
	if len(required) > 0 {
		value = append(value, orderedMember{Name: "required", Value: required})
	}
	value = append(value, orderedMember{Name: "properties", Value: properties})
	return value, nil
}

func typedSchema(name string) orderedObject {
	return orderedObject{{Name: "type", Value: name}}
}

func dereference(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func jsonField(field reflect.StructField) (name string, optional, skip bool) {
	name = field.Name
	tag, ok := field.Tag.Lookup("json")
	if !ok {
		return name, false, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "-" {
		return "", false, true
	}
	if parts[0] != "" {
		name = parts[0]
	}
	for _, option := range parts[1:] {
		if option == "omitempty" || option == "omitzero" {
			optional = true
		}
	}
	return name, optional, false
}

type fieldOptions struct {
	description string
	enum        []string
	required    *bool
}

func parseFieldOptions(field reflect.StructField) (fieldOptions, error) {
	var options fieldOptions
	if description := field.Tag.Get("jsonschema_description"); description != "" {
		options.description = description
	}
	for _, directive := range strings.Split(field.Tag.Get("jsonschema"), ",") {
		key, value, hasValue := strings.Cut(directive, "=")
		switch key {
		case "", "-":
		case "description":
			if !hasValue {
				return options, fmt.Errorf("description requires a value")
			}
			options.description = value
		case "enum":
			if !hasValue {
				return options, fmt.Errorf("enum requires a value")
			}
			options.enum = append(options.enum, value)
		case "required", "optional":
			if hasValue {
				return options, fmt.Errorf("%s does not take a value", key)
			}
			required := key == "required"
			options.required = &required
		default:
			return options, fmt.Errorf("unsupported directive %q", key)
		}
	}
	return options, nil
}
