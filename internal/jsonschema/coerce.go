package jsonschema

import (
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// coercer is pi-schema's coerce(): three copy-on-write passes over a value,
// each keeping a value it cannot coerce as it is.
//  1. Optional nulls: a null in an optional property whose schema rejects
//     null is dropped, as if omitted.
//  2. Conversions ported from TypeBox 1.3.27's Value.Convert, keyed on the
//     first of $ref, const, enum, anyOf/oneOf, allOf and type.
//  3. Primitive coercion by type (type arrays too), as Ajv's coerceTypes.
type coercer struct {
	c *compiledSchema
	// entered holds the (schema, value) pairs pass 2 is converting through a
	// $ref or a wrap into an array: meeting one again, as a recursive schema
	// that accepts nothing would, leaves the value unchanged.
	entered []enteredPair
}

type enteredPair struct {
	node  *object
	value any
}

func (k *coercer) coerce(value any) any {
	root, base := k.c.root, k.c.rootBase
	return k.coerceTypes(k.convert(root, base, k.dropOptionalNulls(value, root, base, 0)), root, base)
}

// maxRefHops bounds a chain of references followed for optional nulls.
const maxRefHops = 32

func (k *coercer) dropOptionalNulls(value, schema any, base string, hops int) any {
	n, ok := schema.(*object)
	if !ok {
		return value
	}
	b := k.c.baseFor(n, base)
	value = k.dropOwnOptionalNulls(value, n, b)
	// Orb-owned (DECISIONS.md divergence ledger, strict sampling with internal
	// $ref): Orb samples definitions strict, so it also drops the nulls behind a
	// $ref, which pi-schema keeps.
	if ref, ok := n.values["$ref"].(string); ok && hops < maxRefHops {
		if target, targetBase, found := k.c.resolve(ref, b); found {
			value = k.dropOptionalNulls(value, target, targetBase, hops+1)
		}
	}
	return value
}

func (k *coercer) dropOwnOptionalNulls(value any, n *object, b string) any {
	if list, ok := value.(*array); ok {
		return mapItems(list, func(item any, index int) any {
			itemSchema, _ := itemSchemaAt(n, index)
			return k.dropOptionalNulls(item, itemSchema, b, 0)
		})
	}
	o, ok := value.(*object)
	if !ok {
		return value
	}
	properties, _ := n.values["properties"].(*object)
	patterns := patternSchemas(n)
	additional, _ := n.values["additionalProperties"].(*object)
	if properties == nil && len(patterns) == 0 && additional == nil {
		return value
	}
	required := map[string]bool{}
	if list, ok := n.values["required"].(*array); ok {
		for _, key := range list.items {
			if key, ok := key.(string); ok {
				required[key] = true
			}
		}
	}
	w := writer{source: o}
	if properties != nil {
		for _, key := range properties.keys {
			current, present := o.values[key]
			if !present {
				continue
			}
			property := properties.values[key]
			if current == nil && !required[key] && k.refResolves(property, b) && !k.c.valid(property, b, nil) {
				w.remove(key)
				continue
			}
			w.set(key, k.dropOptionalNulls(current, property, b, 0))
		}
	}
	// Values of pattern and additional properties are walked for their own
	// optional nulls; the keys themselves are not optional properties.
	if len(patterns) > 0 || additional != nil {
		for _, key := range o.keys {
			_, matched := declared(properties, key)
			for _, entry := range patterns {
				if entry.expression.MatchString(key) {
					matched = true
					w.set(key, k.dropOptionalNulls(w.current().values[key], entry.schema, b, 0))
				}
			}
			if !matched && additional != nil {
				w.set(key, k.dropOptionalNulls(o.values[key], additional, b, 0))
			}
		}
	}
	return w.result(value)
}

// refResolves reports whether a property schema that is a $ref names a
// schema; a null at a dangling reference stays, as pi-schema keeps every null
// at a reference.
func (k *coercer) refResolves(property any, base string) bool {
	n, ok := property.(*object)
	if !ok {
		return true
	}
	ref, ok := n.values["$ref"].(string)
	if !ok {
		return true
	}
	_, _, found := k.c.resolve(ref, k.c.baseFor(n, base))
	return found
}

// writer copies source on its first change.
type writer struct {
	source *object
	copied *object
}

func (w *writer) current() *object {
	if w.copied != nil {
		return w.copied
	}
	return w.source
}

func (w *writer) set(key string, next any) {
	if next == w.current().values[key] {
		return
	}
	if w.copied == nil {
		w.copied = w.source.clone()
	}
	w.copied.values[key] = next
}

func (w *writer) remove(key string) {
	if w.copied == nil {
		w.copied = w.source.clone()
	}
	w.copied.remove(key)
}

func (w *writer) result(unchanged any) any {
	if w.copied != nil {
		return w.copied
	}
	return unchanged
}

func (k *coercer) convert(node any, base string, value any) any {
	n, ok := node.(*object)
	if !ok {
		return value
	}
	b := k.c.baseFor(n, base)
	if ref, ok := n.values["$ref"].(string); ok {
		target, targetBase, found := k.c.resolve(ref, b)
		if t, isObject := target.(*object); found && isObject {
			return k.reenter(t, targetBase, value)
		}
		return value
	}
	return k.convertNode(n, b, value)
}

func (k *coercer) reenter(node *object, base string, value any) any {
	for _, pair := range k.entered {
		if pair.node == node && pair.value == value {
			return value
		}
	}
	k.entered = append(k.entered, enteredPair{node: node, value: value})
	result := k.convert(node, base, value)
	k.entered = k.entered[:len(k.entered)-1]
	return result
}

func (k *coercer) convertNode(n *object, b string, value any) any {
	if literal, ok := n.get("const"); ok {
		return convertLiteral(literal, value)
	}
	if enum, ok := n.values["enum"].(*array); ok {
		if slices.Contains(enum.items, value) {
			return value
		}
		for _, member := range enum.items {
			if candidate := convertLiteral(member, value); candidate != value && k.c.valid(n, b, candidate) {
				return candidate
			}
		}
		return value
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if branches, ok := n.values[keyword].(*array); ok {
			return k.convertUnion(n, branches.items, b, value)
		}
	}
	if members, ok := n.values["allOf"].(*array); ok {
		next := value
		for _, member := range members.items {
			next = k.convert(member, b, next)
		}
		return k.convertMembers(n, b, next)
	}
	schemaType, typed := n.get("type")
	if !typed {
		if hasAny(n, "properties", "patternProperties") {
			return k.convertObject(n, b, value)
		}
		if hasAny(n, "items", "prefixItems") {
			return k.convertArray(n, b, value)
		}
		return value
	}
	switch schemaType {
	case "string":
		if text, ok := tryString(value); ok {
			return text
		}
	case "number":
		if number, ok := tryNumber(value); ok {
			return number
		}
	case "integer":
		if number, ok := tryNumber(value); ok {
			return math.Trunc(number)
		}
	case "boolean":
		if boolean, ok := tryBoolean(value); ok {
			return boolean
		}
	case "null":
		if tryNull(value) {
			return nil
		}
	case "array":
		return k.convertArray(n, b, value)
	case "object":
		return k.convertObject(n, b, value)
	}
	return value
}

func hasAny(n *object, keywords ...string) bool {
	for _, keyword := range keywords {
		if _, ok := n.values[keyword]; ok {
			return true
		}
	}
	return false
}

// convertUnion keeps a value one branch accepts, else takes the first
// branch's conversion the whole union accepts. A union node that also
// declares members converts them into each candidate before checking it.
func (k *coercer) convertUnion(n *object, branches []any, b string, value any) any {
	if k.c.matches(branches, b, value) > 0 {
		return k.convertMembers(n, b, value)
	}
	for _, branch := range branches {
		candidate := k.convertMembers(n, b, k.convert(branch, b, value))
		if k.c.valid(n, b, candidate) {
			return candidate
		}
	}
	return k.convertMembers(n, b, value)
}

func (k *coercer) convertMembers(n *object, b string, value any) any {
	if hasAny(n, "properties", "patternProperties", "additionalProperties") {
		return k.convertObject(n, b, value)
	}
	return value
}

func (k *coercer) convertArray(n *object, b string, value any) any {
	var prefix []any
	tuple := false
	if items, ok := n.values["items"].(*array); ok {
		prefix, tuple = items.items, true
	} else if items, ok := n.values["prefixItems"].(*array); ok {
		prefix, tuple = items.items, true
	}
	list, isArray := value.(*array)
	if tuple {
		// A tuple converts its items and never wraps a single value.
		if !isArray {
			return value
		}
		rest, hasRest := n.get("items")
		if _, ok := rest.(*array); ok {
			hasRest = false
		}
		return mapItems(list, func(item any, index int) any {
			if index < len(prefix) {
				return k.convert(prefix[index], b, item)
			}
			if hasRest {
				return k.convert(rest, b, item)
			}
			return item
		})
	}
	items, hasItems := n.get("items")
	if isArray {
		if !hasItems {
			return value
		}
		return mapItems(list, func(item any, _ int) any { return k.convert(items, b, item) })
	}
	if item, ok := items.(*object); ok {
		return &array{items: []any{k.reenter(item, b, value)}}
	}
	return &array{items: []any{value}}
}

func (k *coercer) convertObject(n *object, b string, value any) any {
	o, ok := value.(*object)
	if !ok {
		return value
	}
	properties, _ := n.values["properties"].(*object)
	patterns := patternSchemas(n)
	additional, _ := n.values["additionalProperties"].(*object)
	w := writer{source: o}
	for _, key := range o.keys {
		next := o.values[key]
		property, matched := declared(properties, key)
		if matched {
			next = k.convert(property, b, next)
		}
		for _, entry := range patterns {
			if entry.expression.MatchString(key) {
				matched = true
				next = k.convert(entry.schema, b, next)
			}
		}
		if !matched && additional != nil {
			next = k.convert(additional, b, next)
		}
		w.set(key, next)
	}
	return w.result(value)
}

func (k *coercer) coerceTypes(value, schema any, base string) any {
	n, ok := schema.(*object)
	if !ok {
		return value
	}
	b := k.c.baseFor(n, base)
	next := value
	if members, ok := n.values["allOf"].(*array); ok {
		for _, member := range members.items {
			next = k.coerceTypes(next, member, b)
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if branches, ok := n.values[keyword].(*array); ok {
			next = k.coerceUnion(next, branches.items, b)
		}
	}
	var types []string
	switch schemaType := n.values["type"].(type) {
	case string:
		types = []string{schemaType}
	case *array:
		for _, item := range schemaType.items {
			if name, ok := item.(string); ok {
				types = append(types, name)
			}
		}
	}
	matchesMember := len(types) > 1 && slices.ContainsFunc(types, func(name string) bool { return hasType(next, name) })
	if !matchesMember {
		for _, name := range types {
			if candidate := coercePrimitive(next, name); candidate != next {
				next = candidate
				break
			}
		}
	}
	if o, ok := next.(*object); ok && slices.Contains(types, "object") {
		next = k.coerceProperties(o, n, b)
	}
	if list, ok := next.(*array); ok && slices.Contains(types, "array") {
		next = mapItems(list, func(item any, index int) any {
			itemSchema, _ := itemSchemaAt(n, index)
			if _, ok := itemSchema.(*object); !ok {
				return item
			}
			return k.coerceTypes(item, itemSchema, b)
		})
	}
	return next
}

func (k *coercer) coerceUnion(value any, branches []any, base string) any {
	if k.c.matches(branches, base, value) > 0 {
		return value
	}
	for _, branch := range branches {
		if coerced := k.coerceTypes(value, branch, base); k.c.valid(branch, base, coerced) {
			return coerced
		}
	}
	return value
}

func (k *coercer) coerceProperties(o *object, n *object, b string) any {
	properties, _ := n.values["properties"].(*object)
	patterns := patternSchemas(n)
	additional, _ := n.values["additionalProperties"].(*object)
	w := writer{source: o}
	if properties != nil {
		for _, key := range properties.keys {
			if current, ok := o.values[key]; ok {
				w.set(key, k.coerceTypes(current, properties.values[key], b))
			}
		}
	}
	for _, key := range o.keys {
		_, matched := declared(properties, key)
		for _, entry := range patterns {
			if entry.expression.MatchString(key) {
				matched = true
				w.set(key, k.coerceTypes(w.current().values[key], entry.schema, b))
			}
		}
		if !matched && additional != nil {
			w.set(key, k.coerceTypes(o.values[key], additional, b))
		}
	}
	return w.result(o)
}

// itemSchemaAt is the schema of an array's item at index: a tuple's for its
// prefix, then items.
func itemSchemaAt(n *object, index int) (any, bool) {
	if items, ok := n.values["items"].(*array); ok {
		if index < len(items.items) {
			return items.items[index], true
		}
		return nil, false
	}
	if items, ok := n.values["prefixItems"].(*array); ok && index < len(items.items) {
		return items.items[index], true
	}
	return n.get("items")
}

// mapItems maps list's items, copying list only when one changes.
func mapItems(list *array, convert func(any, int) any) any {
	var copied []any
	for index, item := range list.items {
		next := convert(item, index)
		if next == item {
			continue
		}
		if copied == nil {
			copied = slices.Clone(list.items)
		}
		copied[index] = next
	}
	if copied == nil {
		return list
	}
	return &array{items: copied}
}

// convertLiteral takes a const or enum member from a value of another type
// that converts to exactly it.
func convertLiteral(literal, value any) any {
	if literal == value {
		return value
	}
	var converted any
	var ok bool
	switch literal.(type) {
	case string:
		converted, ok = tryString(value)
	case float64:
		converted, ok = tryNumber(value)
	case bool:
		converted, ok = tryBoolean(value)
	}
	if ok && converted == literal {
		return converted
	}
	return value
}

func finite(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsInf(number, 0) && !math.IsNaN(number)
}

func tryString(value any) (string, bool) {
	if number, ok := finite(value); ok {
		return jsNumberString(number), true
	}
	switch value := value.(type) {
	case bool:
		return strconv.FormatBool(value), true
	case nil:
		return "null", true
	case string:
		return value, true
	}
	return "", false
}

const maxSafeInteger = 1<<53 - 1

var bigIntLiteral = regexp.MustCompile(`^-?(?:0|[1-9]\d*)n$`)

func tryNumber(value any) (float64, bool) {
	if number, ok := finite(value); ok {
		return number, true
	}
	switch value := value.(type) {
	case bool:
		if value {
			return 1, true
		}
		return 0, true
	case nil:
		return 0, true
	case string:
		if number := jsNumber(value); !math.IsInf(number, 0) && !math.IsNaN(number) {
			return number, true
		}
		switch strings.ToLower(value) {
		case "false":
			return 0, true
		case "true":
			return 1, true
		}
		// TypeBox's BigInt literal, such as 12n; other digit strings that are not
		// finite numbers are out of the safe integer range.
		if bigIntLiteral.MatchString(value) {
			if integer, ok := new(big.Int).SetString(value[:len(value)-1], 10); ok && integer.IsInt64() && math.Abs(float64(integer.Int64())) <= maxSafeInteger {
				return float64(integer.Int64()), true
			}
		}
	}
	return 0, false
}

func tryBoolean(value any) (bool, bool) {
	if number, ok := finite(value); ok {
		return number == 1, number == 0 || number == 1
	}
	switch value := value.(type) {
	case bool:
		return value, true
	case nil:
		return false, true
	case string:
		switch lower := strings.ToLower(value); {
		case lower == "false" || value == "0":
			return false, true
		case lower == "true" || value == "1":
			return true, true
		}
	}
	return false, false
}

func tryNull(value any) bool {
	if number, ok := finite(value); ok {
		return number == 0
	}
	switch value := value.(type) {
	case bool:
		return !value
	case nil:
		return true
	case string:
		lower := strings.ToLower(value)
		return lower == "undefined" || lower == "null" || value == "" || value == "0"
	}
	return false
}

func coercePrimitive(value any, schemaType string) any {
	switch schemaType {
	case "number", "integer":
		switch typed := value.(type) {
		case nil:
			return float64(0)
		case string:
			if strings.TrimFunc(typed, isJSSpace) != "" {
				number := jsNumber(typed)
				if _, isFinite := finite(number); isFinite && (schemaType == "number" || isInteger(number)) {
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
		switch value {
		case nil, "false", float64(0):
			return false
		case "true", float64(1):
			return true
		}
	case "string":
		switch typed := value.(type) {
		case nil:
			return ""
		case float64:
			return jsNumberString(typed)
		case bool:
			return strconv.FormatBool(typed)
		}
	case "null":
		if value == "" || value == float64(0) || value == false {
			return nil
		}
	}
	return value
}

// jsNumber is Number(text): NaN for text that is not a JavaScript numeric
// literal.
func jsNumber(text string) float64 {
	trimmed := strings.TrimFunc(text, isJSSpace)
	if trimmed == "" {
		return 0
	}
	if len(trimmed) > 2 && trimmed[0] == '0' {
		if base, digits := radix(trimmed[1]); base != 0 {
			if strings.Trim(trimmed[2:], digits) != "" {
				return math.NaN()
			}
			integer, _ := new(big.Int).SetString(trimmed[2:], base)
			number, _ := new(big.Float).SetInt(integer).Float64()
			return number
		}
	}
	switch trimmed {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if !decimalLiteral.MatchString(trimmed) {
		return math.NaN()
	}
	// Out of range is Infinity, as in JavaScript.
	number, _ := strconv.ParseFloat(trimmed, 64)
	return number
}

var decimalLiteral = regexp.MustCompile(`^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$`)

func radix(prefix byte) (int, string) {
	switch prefix {
	case 'x', 'X':
		return 16, "0123456789abcdefABCDEF"
	case 'o', 'O':
		return 8, "01234567"
	case 'b', 'B':
		return 2, "01"
	}
	return 0, ""
}

// isJSSpace is JavaScript's WhiteSpace and LineTerminator, which Number and
// String.prototype.trim strip.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

// jsNumberString is String(number).
func jsNumberString(number float64) string {
	switch {
	case math.IsNaN(number):
		return "NaN"
	case math.IsInf(number, 1):
		return "Infinity"
	case math.IsInf(number, -1):
		return "-Infinity"
	}
	return string(jsonwire.AppendFloat(nil, number))
}
