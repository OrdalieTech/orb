package jsonschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/OrdalieTech/pi-go/internal/jsonwire"
)

type ValidationIssue struct {
	Path    string
	Message string
}

type ValidationErrors []ValidationIssue

func (errors ValidationErrors) Error() string {
	if len(errors) == 0 {
		return "jsonschema: validation failed"
	}
	parts := make([]string, len(errors))
	for index, issue := range errors {
		parts[index] = issue.Path + ": " + issue.Message
	}
	return "jsonschema: validation failed: " + strings.Join(parts, "; ")
}

// Validate clones value, applies the same primitive coercions used by
// upstream tool validation, and validates the converted value against schema.
func Validate(schema Schema, value any) (any, error) {
	decodedSchema, err := decodeSchema(schema)
	if err != nil {
		return nil, err
	}
	cloned, err := cloneJSONValue(value)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: clone value: %w", err)
	}
	coerced := coerceValue(cloned, decodedSchema)
	issues := validateValue(coerced, decodedSchema, "")
	if len(issues) > 0 {
		return nil, ValidationErrors(issues)
	}
	return coerced, nil
}

// ValidateToolArguments formats failures like the upstream TypeBox adapter so
// loop-generated error tool results remain trace-compatible.
func ValidateToolArguments(toolName string, schema Schema, arguments any) (any, error) {
	validated, err := Validate(schema, arguments)
	if err == nil {
		return validated, nil
	}
	return nil, formatToolValidationError(toolName, err, indentJSON(arguments))
}

// ValidateToolArgumentsJSON preserves the provider's argument member order in
// the diagnostic's pretty-printed input. Callers with a retained raw tool call
// should prefer this form over re-encoding its arguments map.
func ValidateToolArgumentsJSON(toolName string, schema Schema, argumentsJSON []byte) (any, error) {
	var arguments any
	if err := json.Unmarshal(argumentsJSON, &arguments); err != nil {
		return nil, fmt.Errorf("jsonschema: decode tool arguments: %w", err)
	}
	validated, err := Validate(schema, arguments)
	if err == nil {
		return validated, nil
	}
	return nil, formatToolValidationError(toolName, err, indentRawJSON(argumentsJSON))
}

func formatToolValidationError(toolName string, err error, received string) error {
	var validationErrors ValidationErrors
	if !errors.As(err, &validationErrors) {
		return err
	}

	var message strings.Builder
	fmt.Fprintf(&message, "Validation failed for tool %q:\n", toolName)
	for _, issue := range validationErrors {
		path := issue.Path
		if path == "" {
			path = "root"
		}
		fmt.Fprintf(&message, "  - %s: %s\n", path, issue.Message)
	}
	message.WriteString("\nReceived arguments:\n")
	message.WriteString(received)
	return errors.New(message.String())
}

func decodeSchema(schema Schema) (any, error) {
	data, err := schema.MarshalJSON()
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoded, err := decodeSchemaValue(decoder)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: decode schema: %w", err)
	}
	return decoded, nil
}

type schemaObject struct {
	values map[string]any
	order  []string
}

func (object *schemaObject) get(name string) (any, bool) {
	if object == nil {
		return nil, false
	}
	value, ok := object.values[name]
	return value, ok
}

func decodeSchemaValue(decoder *json.Decoder) (any, error) {
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
		object := &schemaObject{values: make(map[string]any)}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := nameToken.(string)
			if !ok {
				return nil, fmt.Errorf("object member name is %T", nameToken)
			}
			value, err := decodeSchemaValue(decoder)
			if err != nil {
				return nil, err
			}
			if _, exists := object.values[name]; !exists {
				object.order = append(object.order, name)
			}
			object.values[name] = value
		}
		closing, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if closing != json.Delim('}') {
			return nil, fmt.Errorf("object is not closed")
		}
		return object, nil
	case '[':
		var values []any
		for decoder.More() {
			value, err := decodeSchemaValue(decoder)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		closing, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if closing != json.Delim(']') {
			return nil, fmt.Errorf("array is not closed")
		}
		return values, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

func cloneJSONValue(value any) (any, error) {
	data, err := jsonwire.Marshal(value)
	if err != nil {
		return nil, err
	}
	var cloned any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func coerceValue(value, schema any) any {
	object, ok := schema.(*schemaObject)
	if !ok {
		return value
	}
	next := value
	allOf, _ := object.get("allOf")
	for _, nested := range schemaList(allOf) {
		next = coerceValue(next, nested)
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		union, _ := object.get(keyword)
		if variants := schemaList(union); len(variants) > 0 {
			next = coerceUnion(next, variants)
		}
	}

	typeValue, _ := object.get("type")
	types := schemaTypes(typeValue)
	unionAlreadyMatches := len(types) > 1 && anyTypeMatches(next, types)
	if len(types) > 0 && !unionAlreadyMatches {
		for _, schemaType := range types {
			candidate := coercePrimitive(next, schemaType)
			if !reflect.DeepEqual(candidate, next) {
				next = candidate
				break
			}
		}
	}

	if slices.Contains(types, "object") {
		if values, ok := next.(map[string]any); ok {
			propertyValue, _ := object.get("properties")
			properties, _ := propertyValue.(*schemaObject)
			if properties == nil {
				properties = &schemaObject{values: map[string]any{}}
			}
			for _, name := range properties.order {
				propertySchema := properties.values[name]
				if property, exists := values[name]; exists {
					values[name] = coerceValue(property, propertySchema)
				}
			}
			additionalValue, _ := object.get("additionalProperties")
			if additional, ok := additionalValue.(*schemaObject); ok {
				for name, property := range values {
					if _, defined := properties.values[name]; !defined {
						values[name] = coerceValue(property, additional)
					}
				}
			}
		}
	}
	if slices.Contains(types, "array") {
		if values, ok := next.([]any); ok {
			itemValue, _ := object.get("items")
			switch items := itemValue.(type) {
			case *schemaObject:
				for index := range values {
					values[index] = coerceValue(values[index], items)
				}
			case []any:
				for index := range values {
					if index < len(items) {
						values[index] = coerceValue(values[index], items[index])
					}
				}
			}
		}
	}
	return next
}

func coerceUnion(value any, schemas []any) any {
	for _, schema := range schemas {
		candidate, err := cloneJSONValue(value)
		if err != nil {
			continue
		}
		candidate = coerceValue(candidate, schema)
		if len(validateValue(candidate, schema, "")) == 0 {
			return candidate
		}
	}
	return value
}

func coercePrimitive(value any, schemaType string) any {
	switch schemaType {
	case "number":
		switch typed := value.(type) {
		case nil:
			return float64(0)
		case string:
			if strings.TrimSpace(typed) != "" {
				if number, ok := parseJSNumber(typed); ok && !math.IsInf(number, 0) && !math.IsNaN(number) {
					return number
				}
			}
		case bool:
			if typed {
				return float64(1)
			}
			return float64(0)
		}
	case "integer":
		switch typed := value.(type) {
		case nil:
			return float64(0)
		case string:
			if strings.TrimSpace(typed) != "" {
				if number, ok := parseJSNumber(typed); ok && !math.IsInf(number, 0) && !math.IsNaN(number) && math.Trunc(number) == number {
					return number
				}
			}
		case bool:
			if typed {
				return float64(1)
			}
			return float64(0)
		}
	case "boolean":
		switch typed := value.(type) {
		case nil:
			return false
		case string:
			if typed == "true" {
				return true
			}
			if typed == "false" {
				return false
			}
		case float64:
			if typed == 1 {
				return true
			}
			if typed == 0 {
				return false
			}
		}
	case "string":
		switch typed := value.(type) {
		case nil:
			return ""
		case bool:
			return strconv.FormatBool(typed)
		case float64:
			if typed == 0 {
				return "0"
			}
			encoded, err := jsonwire.Marshal(typed)
			if err == nil {
				return string(encoded)
			}
		}
	case "null":
		switch typed := value.(type) {
		case string:
			if typed == "" {
				return nil
			}
		case float64:
			if typed == 0 {
				return nil
			}
		case bool:
			if !typed {
				return nil
			}
		}
	}
	return value
}

func parseJSNumber(value string) (float64, bool) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > 2 {
		base := 0
		switch trimmed[:2] {
		case "0x", "0X":
			base = 16
		case "0b", "0B":
			base = 2
		case "0o", "0O":
			base = 8
		}
		if base != 0 {
			integer, err := strconv.ParseUint(trimmed[2:], base, 64)
			return float64(integer), err == nil
		}
	}
	number, err := strconv.ParseFloat(trimmed, 64)
	return number, err == nil
}

func validateValue(value, schema any, path string) []ValidationIssue {
	if boolean, ok := schema.(bool); ok {
		if boolean {
			return nil
		}
		return []ValidationIssue{{Path: path, Message: "must not be valid"}}
	}
	object, ok := schema.(*schemaObject)
	if !ok {
		return nil
	}
	var issues []ValidationIssue

	allOf, _ := object.get("allOf")
	for _, nested := range schemaList(allOf) {
		issues = append(issues, validateValue(value, nested, path)...)
	}
	anyOf, _ := object.get("anyOf")
	if variants := schemaList(anyOf); len(variants) > 0 {
		if validVariantCount(value, variants) == 0 {
			for _, variant := range variants {
				issues = append(issues, validateValue(value, variant, path)...)
			}
			issues = append(issues, ValidationIssue{Path: path, Message: "must match a schema in anyOf"})
		}
	}
	oneOf, _ := object.get("oneOf")
	if variants := schemaList(oneOf); len(variants) > 0 {
		valid := validVariantCount(value, variants)
		if valid == 0 {
			for _, variant := range variants {
				issues = append(issues, validateValue(value, variant, path)...)
			}
		}
		if valid != 1 {
			issues = append(issues, ValidationIssue{Path: path, Message: "must match exactly one schema in oneOf"})
		}
	}
	if nested, exists := object.get("not"); exists && len(validateValue(value, nested, path)) == 0 {
		issues = append(issues, ValidationIssue{Path: path, Message: "must not match schema in not"})
	}

	typeValue, _ := object.get("type")
	types := schemaTypes(typeValue)
	if len(types) > 0 && !anyTypeMatches(value, types) {
		return append(issues, ValidationIssue{Path: path, Message: "must be " + strings.Join(types, " or ")})
	}
	enumValue, _ := object.get("enum")
	if enum, ok := enumValue.([]any); ok && !containsJSONValue(enum, value) {
		issues = append(issues, ValidationIssue{Path: path, Message: "must be equal to one of the allowed values"})
	}
	if constant, exists := object.get("const"); exists && !jsonValuesEqual(constant, value) {
		issues = append(issues, ValidationIssue{Path: path, Message: "must be equal to the constant"})
	}

	switch typed := value.(type) {
	case map[string]any:
		issues = append(issues, validateObject(typed, object, path)...)
	case []any:
		issues = append(issues, validateArray(typed, object, path)...)
	case string:
		issues = append(issues, validateString(typed, object, path)...)
	case float64:
		issues = append(issues, validateNumber(typed, object, path)...)
	}
	return issues
}

func validateObject(value map[string]any, schema *schemaObject, path string) []ValidationIssue {
	var issues []ValidationIssue
	propertyValue, _ := schema.get("properties")
	properties, _ := propertyValue.(*schemaObject)
	if properties == nil {
		properties = &schemaObject{values: map[string]any{}}
	}
	requiredValue, _ := schema.get("required")
	for _, required := range stringList(requiredValue) {
		if _, exists := value[required]; !exists {
			issues = append(issues, ValidationIssue{
				Path:    requiredPath(path, required),
				Message: "must have required properties " + required,
			})
		}
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		if _, defined := properties.values[key]; !defined {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	additionalValue, _ := schema.get("additionalProperties")
	if additional, ok := additionalValue.(bool); ok && !additional && len(keys) > 0 {
		issues = append(issues, ValidationIssue{Path: path, Message: "must not have additional properties"})
	}
	if additional, ok := additionalValue.(*schemaObject); ok {
		for _, key := range keys {
			property := value[key]
			issues = append(issues, validateValue(property, additional, childPath(path, key))...)
		}
	}
	for _, key := range properties.order {
		if property, exists := value[key]; exists {
			propertySchema := properties.values[key]
			issues = append(issues, validateValue(property, propertySchema, childPath(path, key))...)
		}
	}
	minimumValue, _ := schema.get("minProperties")
	if minimum, ok := schemaInteger(minimumValue); ok && len(value) < minimum {
		issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must not have fewer than %d properties", minimum)})
	}
	maximumValue, _ := schema.get("maxProperties")
	if maximum, ok := schemaInteger(maximumValue); ok && len(value) > maximum {
		issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must not have more than %d properties", maximum)})
	}
	return issues
}

func validateArray(value []any, schema *schemaObject, path string) []ValidationIssue {
	var issues []ValidationIssue
	itemValue, _ := schema.get("items")
	switch items := itemValue.(type) {
	case *schemaObject:
		for index, item := range value {
			issues = append(issues, validateValue(item, items, childPath(path, strconv.Itoa(index)))...)
		}
	case []any:
		for index, item := range value {
			if index < len(items) {
				issues = append(issues, validateValue(item, items[index], childPath(path, strconv.Itoa(index)))...)
				continue
			}
			additionalValue, _ := schema.get("additionalItems")
			if additional, ok := additionalValue.(bool); ok && !additional {
				issues = append(issues, ValidationIssue{Path: childPath(path, strconv.Itoa(index)), Message: "must not have additional items"})
			}
		}
	}
	minimumValue, _ := schema.get("minItems")
	if minimum, ok := schemaInteger(minimumValue); ok && len(value) < minimum {
		issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must not have fewer than %d items", minimum)})
	}
	maximumValue, _ := schema.get("maxItems")
	if maximum, ok := schemaInteger(maximumValue); ok && len(value) > maximum {
		issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must not have more than %d items", maximum)})
	}
	uniqueValue, _ := schema.get("uniqueItems")
	if unique, _ := uniqueValue.(bool); unique {
		duplicate := false
		for index := range value {
			for other := 0; other < index; other++ {
				if jsonValuesEqual(value[index], value[other]) {
					duplicate = true
					break
				}
			}
			if duplicate {
				break
			}
		}
		if duplicate {
			issues = append(issues, ValidationIssue{Path: path, Message: "must not have duplicate items"})
		}
	}
	return issues
}

func validateString(value string, schema *schemaObject, path string) []ValidationIssue {
	var issues []ValidationIssue
	length := utf8.RuneCountInString(value)
	minimumValue, _ := schema.get("minLength")
	if minimum, ok := schemaInteger(minimumValue); ok && length < minimum {
		issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must not have fewer than %d characters", minimum)})
	}
	maximumValue, _ := schema.get("maxLength")
	if maximum, ok := schemaInteger(maximumValue); ok && length > maximum {
		issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must not have more than %d characters", maximum)})
	}
	patternValue, _ := schema.get("pattern")
	if pattern, ok := patternValue.(string); ok {
		expression, err := regexp.Compile(pattern)
		if err == nil && !expression.MatchString(value) {
			issues = append(issues, ValidationIssue{Path: path, Message: "must match pattern " + pattern})
		}
	}
	return issues
}

func validateNumber(value float64, schema *schemaObject, path string) []ValidationIssue {
	var issues []ValidationIssue
	minimumValue, _ := schema.get("minimum")
	if minimum, ok := schemaNumber(minimumValue); ok && value < minimum {
		issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must be >= %v", minimum)})
	}
	maximumValue, _ := schema.get("maximum")
	if maximum, ok := schemaNumber(maximumValue); ok && value > maximum {
		issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must be <= %v", maximum)})
	}
	exclusiveMinimumValue, _ := schema.get("exclusiveMinimum")
	if minimum, ok := schemaNumber(exclusiveMinimumValue); ok && value <= minimum {
		issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must be > %v", minimum)})
	}
	exclusiveMaximumValue, _ := schema.get("exclusiveMaximum")
	if maximum, ok := schemaNumber(exclusiveMaximumValue); ok && value >= maximum {
		issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must be < %v", maximum)})
	}
	multipleValue, _ := schema.get("multipleOf")
	if multiple, ok := schemaNumber(multipleValue); ok && multiple != 0 {
		quotient := value / multiple
		if math.Abs(quotient-math.Round(quotient)) > 1e-12 {
			issues = append(issues, ValidationIssue{Path: path, Message: fmt.Sprintf("must be a multiple of %v", multiple)})
		}
	}
	return issues
}

func validVariantCount(value any, variants []any) int {
	count := 0
	for _, variant := range variants {
		if len(validateValue(value, variant, "")) == 0 {
			count++
		}
	}
	return count
}

func schemaTypes(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if name, ok := item.(string); ok {
				result = append(result, name)
			}
		}
		return result
	default:
		return nil
	}
}

func schemaList(value any) []any {
	values, _ := value.([]any)
	return values
}

func stringList(value any) []string {
	values, _ := value.([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func anyTypeMatches(value any, types []string) bool {
	for _, schemaType := range types {
		if matchesType(value, schemaType) {
			return true
		}
	}
	return false
}

func matchesType(value any, schemaType string) bool {
	switch schemaType {
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && math.Trunc(number) == number
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "null":
		return value == nil
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return false
	}
}

func containsJSONValue(values []any, wanted any) bool {
	for _, value := range values {
		if jsonValuesEqual(value, wanted) {
			return true
		}
	}
	return false
}

func jsonValuesEqual(left, right any) bool {
	leftJSON, leftErr := jsonwire.Marshal(schemaLiteralValue(left))
	rightJSON, rightErr := jsonwire.Marshal(schemaLiteralValue(right))
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func schemaLiteralValue(value any) any {
	switch typed := value.(type) {
	case *schemaObject:
		result := make(map[string]any, len(typed.values))
		for name, member := range typed.values {
			result[name] = schemaLiteralValue(member)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, member := range typed {
			result[index] = schemaLiteralValue(member)
		}
		return result
	default:
		return value
	}
}

func schemaInteger(value any) (int, bool) {
	number, ok := schemaNumber(value)
	if !ok || math.Trunc(number) != number || number < 0 || number > float64(math.MaxInt) {
		return 0, false
	}
	return int(number), true
}

func schemaNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func childPath(parent, child string) string {
	child = strings.ReplaceAll(strings.ReplaceAll(child, "~", "~0"), "/", "~1")
	if parent == "" {
		return child
	}
	return parent + "." + child
}

func requiredPath(parent, required string) string {
	if parent == "" {
		return required
	}
	return parent + "." + required
}

func indentJSON(value any) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return "null"
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

func indentRawJSON(value []byte) string {
	var buffer bytes.Buffer
	if err := json.Indent(&buffer, value, "", "  "); err != nil {
		return "null"
	}
	return buffer.String()
}
