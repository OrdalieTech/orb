package jsonschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// ValidationIssue is one failure of a value, as pi-schema reports it: Path is
// a JSON Pointer, "" for the value itself.
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
		parts[index] = dottedPath(issue.Path) + ": " + issue.Message
	}
	return "jsonschema: validation failed: " + strings.Join(parts, "; ")
}

// Validate coerces a copy of value towards schema as pi-schema's parse does
// for tool arguments, and returns it when it then validates.
func Validate(schema Schema, value any) (any, error) {
	compiled, err := compileSchema(schema)
	if err != nil {
		return nil, err
	}
	data, err := jsonwire.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: encode value: %w", err)
	}
	decoded, err := decodeJSON(data)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: decode value: %w", err)
	}
	parsed, issues, err := compiled.parse(decoded)
	if err != nil {
		return nil, err
	}
	if issues != nil {
		return nil, ValidationErrors(issues)
	}
	return goValue(parsed), nil
}

// ValidateToolArgumentsJSON is pi's validateToolArguments for a tool call
// whose provider-decoded arguments are argumentsJSON and whose decoding
// failure, if any, is argumentsError. Its errors are the text the model reads.
func ValidateToolArgumentsJSON(toolName string, schema Schema, argumentsJSON []byte, argumentsError string) (any, error) {
	if argumentsError != "" {
		return nil, errors.New(`Invalid arguments for tool "` + toolName + `": ` + argumentsError)
	}
	compiled, err := compileSchema(schema)
	if err != nil {
		return nil, err
	}
	if _, ok := compiled.root.(bool); ok {
		// pi caches validators in a WeakMap keyed by the schema, which refuses a
		// boolean schema with V8's TypeError.
		return nil, errors.New("Invalid value used as weak map key") //nolint:staticcheck // V8's message, which the model reads.
	}
	arguments, err := decodeJSON(argumentsJSON)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: decode tool arguments: %w", err)
	}
	parsed, issues, err := compiled.parse(arguments)
	if err != nil {
		return nil, err
	}
	if issues == nil {
		return goValue(parsed), nil
	}
	message := []byte(`Validation failed for tool "` + toolName + "\":\n")
	for index, issue := range issues {
		if index > 0 {
			message = append(message, '\n')
		}
		message = append(message, "  - "+dottedPath(issue.Path)+": "+issue.Message...)
	}
	message = append(message, "\n\nReceived arguments:\n"...)
	return nil, errors.New(string(appendJSON(message, arguments, "  ", "")))
}

// dottedPath is a JSON Pointer as the dotted path the model sees.
func dottedPath(pointer string) string {
	if pointer == "" {
		return "root"
	}
	segments := strings.Split(pointer[1:], "/")
	for index, segment := range segments {
		segments[index] = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
	}
	return strings.Join(segments, ".")
}

func (c *compiledSchema) parse(value any) (parsed any, issues []ValidationIssue, err error) {
	run := *c
	run.refs = map[[2]any]bool{}
	c = &run
	defer func() {
		if r := recover(); r != nil {
			if r != any(errStackOverflow) {
				panic(r)
			}
			parsed, issues, err = nil, nil, errStackOverflow
		}
	}()
	coerced := (&coercer{c: c}).coerce(value)
	if c.valid(c.root, c.rootBase, coerced) {
		return coerced, nil, nil
	}
	c.walk(c.root, c.rootBase, coerced, "", &issues)
	if len(issues) == 0 {
		issues = []ValidationIssue{{Path: "", Message: "invalid"}}
	}
	return nil, issues, nil
}

// errStackOverflow is what pi-schema throws, through V8, when a $ref cycle
// re-enters a schema with the same value: the model reads it as the tool result.
var errStackOverflow = errors.New("Maximum call stack size exceeded") //nolint:staticcheck // V8's message, which the model reads.

// enterRef marks target as entered with v on the current $ref path, and
// panics with errStackOverflow when it already is: that recursion never ends.
func (c *compiledSchema) enterRef(target, v any) (leave func()) {
	key := [2]any{target, v}
	if c.refs[key] {
		panic(errStackOverflow)
	}
	c.refs[key] = true
	return func() { delete(c.refs, key) }
}

// object is a JSON object whose keys keep JavaScript's order: array indexes
// ascending, then the other keys in insertion order. Objects and arrays are
// pointers because coercion copies only what it changes and, as pi-schema,
// tells an unchanged value by identity.
type object struct {
	keys   []string
	values map[string]any
}

type array struct{ items []any }

func (o *object) get(key string) (any, bool) {
	value, ok := o.values[key]
	return value, ok
}

func (o *object) clone() *object {
	values := make(map[string]any, len(o.values))
	for key, value := range o.values {
		values[key] = value
	}
	return &object{keys: append([]string(nil), o.keys...), values: values}
}

func (o *object) remove(key string) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for index, name := range o.keys {
		if name == key {
			o.keys = append(o.keys[:index:index], o.keys[index+1:]...)
			return
		}
	}
}

// decodeJSON decodes one JSON value as JSON.parse does: a repeated key keeps
// its first position and its last value, and numbers are float64.
func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing data after JSON value")
	}
	return value, nil
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case json.Number:
		// Out of range is Infinity, as in JavaScript.
		number, _ := strconv.ParseFloat(string(token), 64)
		return number, nil
	case json.Delim:
		if token == '[' {
			list := &array{items: []any{}}
			for decoder.More() {
				item, err := decodeValue(decoder)
				if err != nil {
					return nil, err
				}
				list.items = append(list.items, item)
			}
			_, err := decoder.Token()
			return list, err
		}
		result := &object{values: map[string]any{}}
		for decoder.More() {
			name, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, _ := name.(string)
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			if _, exists := result.values[key]; !exists {
				result.keys = append(result.keys, key)
			}
			result.values[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		result.keys = jsKeyOrder(result.keys)
		return result, nil
	default:
		return token, nil
	}
}

// jsKeyOrder orders keys as a JavaScript object enumerates them.
func jsKeyOrder(keys []string) []string {
	var indexes []uint64
	var others []string
	for _, key := range keys {
		if index, ok := arrayIndex(key); ok {
			indexes = append(indexes, index)
		} else {
			others = append(others, key)
		}
	}
	if len(indexes) == 0 {
		return keys
	}
	slices.Sort(indexes)
	ordered := make([]string, 0, len(keys))
	for _, index := range indexes {
		ordered = append(ordered, strconv.FormatUint(index, 10))
	}
	return append(ordered, others...)
}

func arrayIndex(key string) (uint64, bool) {
	if key == "" || len(key) > 1 && key[0] == '0' {
		return 0, false
	}
	index, err := strconv.ParseUint(key, 10, 32)
	return index, err == nil && index != math.MaxUint32
}

func goValue(value any) any {
	switch value := value.(type) {
	case *object:
		result := make(map[string]any, len(value.keys))
		for _, key := range value.keys {
			result[key] = goValue(value.values[key])
		}
		return result
	case *array:
		result := make([]any, len(value.items))
		for index, item := range value.items {
			result[index] = goValue(item)
		}
		return result
	default:
		return value
	}
}

// appendJSON appends value as JSON.stringify(value, null, indent) writes it.
func appendJSON(dst []byte, value any, indent, prefix string) []byte {
	switch value := value.(type) {
	case nil:
		return append(dst, "null"...)
	case bool:
		return strconv.AppendBool(dst, value)
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return append(dst, "null"...)
		}
		return jsonwire.AppendFloat(dst, value)
	case string:
		return jsonwire.AppendString(dst, value)
	case *array:
		if len(value.items) == 0 {
			return append(dst, "[]"...)
		}
		dst = append(dst, '[')
		for index, item := range value.items {
			if index > 0 {
				dst = append(dst, ',')
			}
			dst = appendIndent(dst, indent, prefix+indent)
			dst = appendJSON(dst, item, indent, prefix+indent)
		}
		return append(appendIndent(dst, indent, prefix), ']')
	case *object:
		if len(value.keys) == 0 {
			return append(dst, "{}"...)
		}
		dst = append(dst, '{')
		for index, key := range value.keys {
			if index > 0 {
				dst = append(dst, ',')
			}
			dst = appendIndent(dst, indent, prefix+indent)
			dst = append(jsonwire.AppendString(dst, key), ':')
			if indent != "" {
				dst = append(dst, ' ')
			}
			dst = appendJSON(dst, value.values[key], indent, prefix+indent)
		}
		return append(appendIndent(dst, indent, prefix), '}')
	default:
		return append(dst, "null"...)
	}
}

func appendIndent(dst []byte, indent, prefix string) []byte {
	if indent == "" {
		return dst
	}
	return append(append(dst, '\n'), prefix...)
}

func stringify(value any) string { return string(appendJSON(nil, value, "", "")) }

// compiledSchemas keeps compiled schemas, which nothing modifies, by their
// bytes: tool calls validate against the same few schemas, and compiling one
// again cost more than validating. At most maxCompiledSchemas are kept.
var (
	compiledSchemas    sync.Map
	compiledSchemaKept atomic.Int32
)

const maxCompiledSchemas = 512

// compiledSchema is a decoded schema with pi-schema's reference index.
type compiledSchema struct {
	root      any
	rootBase  string
	baseOf    map[*object]string
	resources map[string]any
	anchors   map[string]*object
	// refs is per parse call: the (schema, value) pairs on the $ref path.
	refs map[[2]any]bool
}

func compileSchema(schema Schema) (*compiledSchema, error) {
	if cached, ok := compiledSchemas.Load(string(schema)); ok {
		return cached.(*compiledSchema), nil
	}
	data, err := schema.MarshalJSON()
	if err != nil {
		return nil, err
	}
	root, err := decodeJSON(data)
	if err != nil {
		return nil, fmt.Errorf("jsonschema: decode schema: %w", err)
	}
	c := &compiledSchema{root: root, rootBase: defaultBase, baseOf: map[*object]string{}, resources: map[string]any{defaultBase: root}, anchors: map[string]*object{}}
	if node, ok := root.(*object); ok {
		c.rootBase = c.baseFor(node, defaultBase)
	}
	c.index(root, defaultBase)
	if err := c.compiles(root, c.rootBase, map[*object]bool{}); err != nil {
		return nil, err
	}
	if compiledSchemaKept.Add(1) <= maxCompiledSchemas {
		compiledSchemas.Store(string(schema), c)
	}
	return c, nil
}

// compiles reports the first subschema pi-schema's compile() refuses, walking
// the keywords it compiles in its order.
func (c *compiledSchema) compiles(node any, base string, seen map[*object]bool) error {
	if _, ok := node.(bool); ok {
		return nil
	}
	n, ok := node.(*object)
	if !ok {
		return errors.New("A schema must be an object or a boolean, got " + stringify(node)) //nolint:staticcheck // pi-schema's message, which the model reads.
	}
	if seen[n] || onlyTypeAndAnnotations(n) {
		return nil
	}
	seen[n] = true
	b := c.baseFor(n, base)
	var subschemas []any
	if properties, ok := n.values["properties"].(*object); ok {
		subschemas = appendMembers(subschemas, properties)
	}
	if patterns, ok := n.values["patternProperties"].(*object); ok {
		subschemas = appendMembers(subschemas, patterns)
	}
	if additional, ok := n.get("additionalProperties"); ok && additional != true {
		subschemas = append(subschemas, additional)
	}
	if names, ok := n.get("propertyNames"); ok {
		subschemas = append(subschemas, names)
	}
	if dependent, ok := n.values["dependentSchemas"].(*object); ok {
		subschemas = appendMembers(subschemas, dependent)
	}
	if dependencies, ok := n.values["dependencies"].(*object); ok {
		for _, key := range dependencies.keys {
			if _, list := dependencies.values[key].(*array); !list {
				subschemas = append(subschemas, dependencies.values[key])
			}
		}
	}
	prefix, rest, hasRest := itemSchemas(n)
	subschemas = append(subschemas, prefix...)
	if hasRest && rest != true {
		subschemas = append(subschemas, rest)
	}
	if contains, ok := n.get("contains"); ok {
		subschemas = append(subschemas, contains)
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if list, ok := n.values[keyword].(*array); ok {
			subschemas = append(subschemas, list.items...)
		}
	}
	if not, ok := n.get("not"); ok {
		subschemas = append(subschemas, not)
	}
	if condition, ok := n.get("if"); ok {
		then, hasThen := n.get("then")
		otherwise, hasElse := n.get("else")
		if hasThen {
			subschemas = append(subschemas, then)
		}
		if hasElse {
			subschemas = append(subschemas, otherwise)
		}
		if hasThen || hasElse {
			subschemas = append(subschemas, condition)
		}
	}
	for _, subschema := range subschemas {
		if err := c.compiles(subschema, b, seen); err != nil {
			return err
		}
	}
	if ref, ok := n.values["$ref"].(string); ok {
		if target, targetBase, found := c.resolve(ref, b); found {
			return c.compiles(target, targetBase, seen)
		}
	}
	return nil
}

func appendMembers(list []any, members *object) []any {
	for _, key := range members.keys {
		list = append(list, members.values[key])
	}
	return list
}

// annotationKeywords never affect a check.
var annotationKeywords = map[string]bool{
	"$schema": true, "$comment": true, "title": true, "description": true, "default": true, "examples": true,
	"deprecated": true, "readOnly": true, "writeOnly": true, "$defs": true, "definitions": true,
	"contentEncoding": true, "contentMediaType": true, "minContains": true, "maxContains": true, "$vocabulary": true,
}

func onlyTypeAndAnnotations(node *object) bool {
	for _, key := range node.keys {
		if key != "type" && !annotationKeywords[key] {
			return false
		}
	}
	return true
}

// defaultBase is the base URI of a root schema without $id.
const defaultBase = "https://json-schema.invalid/root.json"

var (
	indexedSchemaKeywords = []string{"additionalProperties", "propertyNames", "contains", "not", "if", "then", "else", "unevaluatedProperties", "unevaluatedItems", "additionalItems", "contentSchema", "items"}
	indexedMapKeywords    = []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies"}
	indexedArrayKeywords  = []string{"allOf", "anyOf", "oneOf", "prefixItems", "items"}
)

// index records the base URI of every subschema and its $id resources and
// anchors; an $id inside enum, const or an unknown keyword names nothing.
func (c *compiledSchema) index(node any, base string) {
	n, ok := node.(*object)
	if !ok {
		return
	}
	if _, seen := c.baseOf[n]; seen {
		return
	}
	b := base
	if id, ok := n.values["$id"].(string); ok {
		uri := resolveURI(id, b)
		b = documentOf(uri)
		c.resources[b] = n
		if len(uri) > len(b)+1 {
			c.anchors[uri] = n
		}
	}
	c.baseOf[n] = b
	for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
		if anchor, ok := n.values[keyword].(string); ok {
			c.anchors[b+"#"+anchor] = n
		}
	}
	for _, keyword := range indexedSchemaKeywords {
		c.index(n.values[keyword], b)
	}
	for _, keyword := range indexedMapKeywords {
		if members, ok := n.values[keyword].(*object); ok {
			for _, key := range members.keys {
				c.index(members.values[key], b)
			}
		}
	}
	for _, keyword := range indexedArrayKeywords {
		if list, ok := n.values[keyword].(*array); ok {
			for _, item := range list.items {
				c.index(item, b)
			}
		}
	}
}

func (c *compiledSchema) baseFor(node *object, base string) string {
	if indexed, ok := c.baseOf[node]; ok {
		return indexed
	}
	if id, ok := node.values["$id"].(string); ok {
		return documentOf(resolveURI(id, base))
	}
	return base
}

// resolve returns the schema ref names from base; a reference that names
// nothing is not found, and rejects the values at its place.
func (c *compiledSchema) resolve(ref, base string) (any, string, bool) {
	uri := resolveURI(ref, base)
	doc := documentOf(uri)
	fragment := ""
	if len(uri) > len(doc) {
		decoded, err := url.PathUnescape(uri[len(doc)+1:])
		if err != nil || !utf8.ValidString(decoded) {
			return nil, "", false
		}
		fragment = decoded
	}
	root, ok := c.resources[doc]
	if !ok {
		return nil, "", false
	}
	if fragment == "" {
		return root, doc, true
	}
	if !strings.HasPrefix(fragment, "/") {
		target, ok := c.anchors[doc+"#"+fragment]
		if !ok {
			return nil, "", false
		}
		if targetBase, indexed := c.baseOf[target]; indexed {
			return target, targetBase, true
		}
		return target, doc, true
	}
	node, b := root, doc
	for _, raw := range strings.Split(fragment, "/")[1:] {
		key := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch parent := node.(type) {
		case *object:
			if node, ok = parent.values[key]; !ok {
				return nil, "", false
			}
		case *array:
			index, isIndex := arrayIndex(key)
			if !isIndex || index >= uint64(len(parent.items)) {
				return nil, "", false
			}
			node = parent.items[index]
		default:
			return nil, "", false
		}
		if child, ok := node.(*object); ok {
			if indexed, ok := c.baseOf[child]; ok {
				b = indexed
			}
		}
	}
	return node, b, true
}

// resolveURI resolves ref against base as new URL(ref, base) does for the
// references schemas use; the fragment is kept as written.
func resolveURI(ref, base string) string {
	doc, fragment, hasFragment := strings.Cut(ref, "#")
	resolved := documentOf(base)
	if doc != "" {
		resolved = doc
		baseURL, baseErr := url.Parse(documentOf(base))
		refURL, refErr := url.Parse(doc)
		if baseErr == nil && refErr == nil && (refURL.IsAbs() || baseURL.Opaque == "") {
			resolved = baseURL.ResolveReference(refURL).String()
		}
	}
	if hasFragment {
		return resolved + "#" + fragment
	}
	return resolved
}

func documentOf(uri string) string {
	doc, _, _ := strings.Cut(uri, "#")
	return doc
}

// valid reports whether v passes node, as pi-schema's compiled check does.
func (c *compiledSchema) valid(node any, base string, v any) bool {
	n, ok := node.(*object)
	if !ok {
		allowed, isBool := node.(bool)
		return !isBool || allowed
	}
	b := c.baseFor(n, base)
	if types, ok := n.get("type"); ok && !typeMatches(types, v) {
		return false
	}
	if enum, ok := n.values["enum"].(*array); ok && !containsValue(enum.items, v) {
		return false
	}
	if constant, ok := n.get("const"); ok && !deepEqual(constant, v) {
		return false
	}
	switch v := v.(type) {
	case string:
		if !c.validString(n, v) {
			return false
		}
	case float64:
		if !validNumber(n, v) {
			return false
		}
	case *object:
		if !c.validObject(n, b, v) {
			return false
		}
	case *array:
		if !c.validArray(n, b, v) {
			return false
		}
	}
	if list, ok := n.values["allOf"].(*array); ok {
		for _, member := range list.items {
			if !c.valid(member, b, v) {
				return false
			}
		}
	}
	if list, ok := n.values["anyOf"].(*array); ok && c.matches(list.items, b, v) == 0 {
		return false
	}
	if list, ok := n.values["oneOf"].(*array); ok && c.matches(list.items, b, v) != 1 {
		return false
	}
	if not, ok := n.get("not"); ok && c.valid(not, b, v) {
		return false
	}
	if branch, ok := c.conditional(n, b, v); ok && !c.valid(branch, b, v) {
		return false
	}
	if ref, ok := n.values["$ref"].(string); ok {
		target, targetBase, found := c.resolve(ref, b)
		if !found {
			return false
		}
		leave := c.enterRef(target, v)
		ok := c.valid(target, targetBase, v)
		leave()
		if !ok {
			return false
		}
	}
	return true
}

func (c *compiledSchema) matches(branches []any, base string, v any) int {
	count := 0
	for _, branch := range branches {
		if c.valid(branch, base, v) {
			count++
		}
	}
	return count
}

// conditional is the then or else schema that applies to v, if any.
func (c *compiledSchema) conditional(n *object, base string, v any) (any, bool) {
	condition, ok := n.get("if")
	if !ok {
		return nil, false
	}
	if c.valid(condition, base, v) {
		return n.get("then")
	}
	return n.get("else")
}

func typeMatches(types, v any) bool {
	list := []any{types}
	if multiple, ok := types.(*array); ok {
		list = multiple.items
	}
	for _, name := range list {
		if name, ok := name.(string); ok && hasType(v, name) {
			return true
		}
	}
	return false
}

func hasType(v any, name string) bool {
	switch name {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		number, ok := v.(float64)
		return ok && isInteger(number)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "array":
		_, ok := v.(*array)
		return ok
	case "object":
		_, ok := v.(*object)
		return ok
	default:
		return false
	}
}

func isInteger(number float64) bool {
	return !math.IsInf(number, 0) && math.Trunc(number) == number
}

func containsValue(values []any, v any) bool {
	for _, value := range values {
		if deepEqual(value, v) {
			return true
		}
	}
	return false
}

func deepEqual(left, right any) bool {
	switch left := left.(type) {
	case *object:
		right, ok := right.(*object)
		if !ok || len(left.keys) != len(right.keys) {
			return false
		}
		for _, key := range left.keys {
			value, ok := right.values[key]
			if !ok || !deepEqual(left.values[key], value) {
				return false
			}
		}
		return true
	case *array:
		right, ok := right.(*array)
		if !ok || len(left.items) != len(right.items) {
			return false
		}
		for index := range left.items {
			if !deepEqual(left.items[index], right.items[index]) {
				return false
			}
		}
		return true
	default:
		return left == right
	}
}

func schemaNumber(n *object, keyword string) (float64, bool) {
	number, ok := n.values[keyword].(float64)
	return number, ok
}

func (c *compiledSchema) validString(n *object, v string) bool {
	length := float64(utf8.RuneCountInString(v))
	if minimum, ok := schemaNumber(n, "minLength"); ok && length < minimum {
		return false
	}
	if maximum, ok := schemaNumber(n, "maxLength"); ok && length > maximum {
		return false
	}
	if source, ok := n.values["pattern"].(string); ok {
		if expression := pattern(source); expression != nil && !expression.MatchString(v) {
			return false
		}
	}
	if name, ok := n.values["format"].(string); ok {
		if check := formatChecks[name]; check != nil && !check(v) {
			return false
		}
	}
	return true
}

func validNumber(n *object, v float64) bool {
	if minimum, ok := schemaNumber(n, "minimum"); ok && v < minimum {
		return false
	}
	if maximum, ok := schemaNumber(n, "maximum"); ok && v > maximum {
		return false
	}
	if minimum, ok := schemaNumber(n, "exclusiveMinimum"); ok && v <= minimum {
		return false
	}
	if maximum, ok := schemaNumber(n, "exclusiveMaximum"); ok && v >= maximum {
		return false
	}
	if divisor, ok := schemaNumber(n, "multipleOf"); ok && !isMultiple(v, divisor) {
		return false
	}
	return true
}

// isMultiple is exact for decimals: 0.0075 / 0.0001 is 74.99999999999999 in
// floating point.
func isMultiple(v, divisor float64) bool {
	quotient := v / divisor
	if isInteger(quotient) {
		return true
	}
	if math.IsInf(quotient, 0) || math.IsNaN(quotient) {
		return math.Mod(v, divisor) == 0
	}
	vCoefficient, vExponent := decimal(v)
	dCoefficient, dExponent := decimal(divisor)
	scale := func(exponent int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil) }
	remainder := new(big.Int)
	if vExponent >= dExponent {
		remainder.Rem(vCoefficient.Mul(vCoefficient, scale(vExponent-dExponent)), dCoefficient)
	} else {
		remainder.Rem(vCoefficient, dCoefficient.Mul(dCoefficient, scale(dExponent-vExponent)))
	}
	return remainder.Sign() == 0
}

// decimal is |x| as an integer coefficient and a power of ten, read from its
// JavaScript text.
func decimal(x float64) (*big.Int, int) {
	mantissa, exponentText, _ := strings.Cut(jsNumberString(math.Abs(x)), "e")
	exponent, _ := strconv.Atoi(exponentText)
	if point := strings.IndexByte(mantissa, '.'); point >= 0 {
		exponent -= len(mantissa) - point - 1
	}
	coefficient, _ := new(big.Int).SetString(strings.Replace(mantissa, ".", "", 1), 10)
	return coefficient, exponent
}

func (c *compiledSchema) validObject(n *object, base string, v *object) bool {
	properties, _ := n.values["properties"].(*object)
	if properties != nil {
		for _, key := range properties.keys {
			if value, ok := v.values[key]; ok && !c.valid(properties.values[key], base, value) {
				return false
			}
		}
	}
	if required, ok := n.values["required"].(*array); ok {
		for _, key := range required.items {
			if key, ok := key.(string); ok {
				if _, present := v.values[key]; !present {
					return false
				}
			}
		}
	}
	patterns := patternSchemas(n)
	additional, hasAdditional := n.get("additionalProperties")
	names, hasNames := n.get("propertyNames")
	for _, key := range v.keys {
		_, matched := declared(properties, key)
		for _, entry := range patterns {
			if entry.expression.MatchString(key) {
				matched = true
				if !c.valid(entry.schema, base, v.values[key]) {
					return false
				}
			}
		}
		if !matched && hasAdditional && !c.valid(additional, base, v.values[key]) {
			return false
		}
		if hasNames && !c.valid(names, base, key) {
			return false
		}
	}
	count := float64(len(v.keys))
	if minimum, ok := schemaNumber(n, "minProperties"); ok && count < minimum {
		return false
	}
	if maximum, ok := schemaNumber(n, "maxProperties"); ok && count > maximum {
		return false
	}
	for _, dependency := range dependencies(n) {
		if _, present := v.values[dependency.key]; !present {
			continue
		}
		if dependency.required != nil {
			for _, key := range dependency.required.items {
				if key, ok := key.(string); ok {
					if _, present := v.values[key]; !present {
						return false
					}
				}
			}
		} else if !c.valid(dependency.schema, base, v) {
			return false
		}
	}
	return true
}

func declared(properties *object, key string) (any, bool) {
	if properties == nil {
		return nil, false
	}
	return properties.get(key)
}

type patternSchema struct {
	expression *regexp.Regexp
	schema     any
}

// patternSchemas are n's patternProperties; a pattern Go cannot compile
// matches no key.
func patternSchemas(n *object) []patternSchema {
	members, ok := n.values["patternProperties"].(*object)
	if !ok {
		return nil
	}
	var result []patternSchema
	for _, source := range members.keys {
		if expression := pattern(source); expression != nil {
			result = append(result, patternSchema{expression: expression, schema: members.values[source]})
		}
	}
	return result
}

type dependency struct {
	key      string
	required *array
	schema   any
}

// dependencies are dependentRequired, then draft 7's dependencies (which
// replace a dependentRequired entry for the same key in place), then
// dependentSchemas.
func dependencies(n *object) []dependency {
	var result []dependency
	position := map[string]int{}
	add := func(key string, value any) {
		entry := dependency{key: key, schema: value}
		entry.required, _ = value.(*array)
		if index, ok := position[key]; ok {
			result[index] = entry
			return
		}
		position[key] = len(result)
		result = append(result, entry)
	}
	for _, keyword := range []string{"dependentRequired", "dependencies"} {
		if members, ok := n.values[keyword].(*object); ok {
			for _, key := range members.keys {
				add(key, members.values[key])
			}
		}
	}
	if members, ok := n.values["dependentSchemas"].(*object); ok {
		for _, key := range members.keys {
			result = append(result, dependency{key: key, schema: members.values[key]})
		}
	}
	return result
}

// itemSchemas are the schemas of a tuple's leading items and of the items
// after them: a draft-7 items array and additionalItems, else prefixItems and
// items.
func itemSchemas(n *object) ([]any, any, bool) {
	if items, ok := n.values["items"].(*array); ok {
		rest, hasRest := n.get("additionalItems")
		return items.items, rest, hasRest
	}
	var prefix []any
	if items, ok := n.values["prefixItems"].(*array); ok {
		prefix = items.items
	}
	rest, hasRest := n.get("items")
	return prefix, rest, hasRest
}

func (c *compiledSchema) validArray(n *object, base string, v *array) bool {
	prefix, rest, hasRest := itemSchemas(n)
	for index, item := range v.items {
		if index < len(prefix) {
			if !c.valid(prefix[index], base, item) {
				return false
			}
		} else if hasRest && !c.valid(rest, base, item) {
			return false
		}
	}
	count := float64(len(v.items))
	if minimum, ok := schemaNumber(n, "minItems"); ok && count < minimum {
		return false
	}
	if maximum, ok := schemaNumber(n, "maxItems"); ok && count > maximum {
		return false
	}
	if unique, _ := n.values["uniqueItems"].(bool); unique && !uniqueItems(v.items) {
		return false
	}
	if contains, ok := n.get("contains"); ok {
		minimum, maximum := containsBounds(n)
		if !containsCount(c, contains, base, v.items, minimum, maximum) {
			return false
		}
	}
	return true
}

func uniqueItems(items []any) bool {
	for index := range items {
		for other := 0; other < index; other++ {
			if deepEqual(items[index], items[other]) {
				return false
			}
		}
	}
	return true
}

func containsBounds(n *object) (float64, float64) {
	minimum, ok := schemaNumber(n, "minContains")
	if !ok {
		minimum = 1
	}
	maximum, ok := schemaNumber(n, "maxContains")
	if !ok {
		maximum = math.Inf(1)
	}
	return minimum, maximum
}

func containsCount(c *compiledSchema, contains any, base string, items []any, minimum, maximum float64) bool {
	count := 0.0
	for _, item := range items {
		if c.valid(contains, base, item) {
			if count++; count > maximum {
				return false
			}
		}
	}
	return count >= minimum
}

// patterns caches compiled pattern sources, nil for one Go cannot compile.
// At most maxPatterns are kept.
var (
	patterns     sync.Map
	patternsKept atomic.Int32
)

const maxPatterns = 4096

func pattern(source string) *regexp.Regexp {
	if cached, ok := patterns.Load(source); ok {
		return cached.(*regexp.Regexp)
	}
	expression, err := regexp.Compile(source)
	if err != nil {
		expression = nil
	}
	if patternsKept.Add(1) <= maxPatterns {
		patterns.Store(source, expression)
	}
	return expression
}

// walk appends why v fails node, one issue per failing keyword at the value
// it failed on, in pi-schema's order.
func (c *compiledSchema) walk(node any, base string, v any, path string, out *[]ValidationIssue) {
	if allowed, ok := node.(bool); ok {
		if !allowed {
			*out = append(*out, ValidationIssue{Path: path, Message: "no value is allowed here"})
		}
		return
	}
	n, ok := node.(*object)
	if !ok {
		return
	}
	b := c.baseFor(n, base)
	if c.valid(n, b, v) {
		return
	}
	push := func(at, message string) { *out = append(*out, ValidationIssue{Path: at, Message: message}) }
	if types, ok := n.get("type"); ok && !typeMatches(types, v) {
		push(path, "expected "+joinTypes(types)+", got "+typeName(v))
		return
	}
	if enum, ok := n.values["enum"].(*array); ok && !containsValue(enum.items, v) {
		push(path, "expected one of "+listValues(enum.items))
	}
	if constant, ok := n.get("const"); ok && !deepEqual(constant, v) {
		push(path, "expected "+stringify(constant))
	}
	switch v := v.(type) {
	case string:
		c.walkString(n, v, path, push)
	case float64:
		walkNumber(n, v, path, push)
	case *object:
		c.walkObject(n, b, v, path, out)
	case *array:
		c.walkArray(n, b, v, path, out)
	}
	if list, ok := n.values["allOf"].(*array); ok {
		for _, member := range list.items {
			c.walk(member, b, v, path, out)
		}
	}
	if list, ok := n.values["anyOf"].(*array); ok {
		c.walkUnion(list.items, b, v, path, out, false)
	}
	if list, ok := n.values["oneOf"].(*array); ok {
		c.walkUnion(list.items, b, v, path, out, true)
	}
	if not, ok := n.get("not"); ok && c.valid(not, b, v) {
		push(path, "must not match the schema in not")
	}
	if branch, ok := c.conditional(n, b, v); ok {
		c.walk(branch, b, v, path, out)
	}
	if ref, ok := n.values["$ref"].(string); ok {
		target, targetBase, found := c.resolve(ref, b)
		if !found {
			push(path, "unresolved reference "+ref)
		} else {
			leave := c.enterRef(target, v)
			c.walk(target, targetBase, v, path, out)
			leave()
		}
	}
}

func (c *compiledSchema) walkString(n *object, v, path string, push func(string, string)) {
	length := float64(utf8.RuneCountInString(v))
	if minimum, ok := schemaNumber(n, "minLength"); ok && length < minimum {
		push(path, "expected at least "+jsNumberString(minimum)+" characters")
	}
	if maximum, ok := schemaNumber(n, "maxLength"); ok && length > maximum {
		push(path, "expected at most "+jsNumberString(maximum)+" characters")
	}
	if source, ok := n.values["pattern"].(string); ok {
		if expression := pattern(source); expression != nil && !expression.MatchString(v) {
			push(path, "expected to match "+source)
		}
	}
	if name, ok := n.values["format"].(string); ok {
		if check := formatChecks[name]; check != nil && !check(v) {
			push(path, "expected format "+name)
		}
	}
}

func walkNumber(n *object, v float64, path string, push func(string, string)) {
	if minimum, ok := schemaNumber(n, "minimum"); ok && v < minimum {
		push(path, "expected >= "+jsNumberString(minimum))
	}
	if maximum, ok := schemaNumber(n, "maximum"); ok && v > maximum {
		push(path, "expected <= "+jsNumberString(maximum))
	}
	if minimum, ok := schemaNumber(n, "exclusiveMinimum"); ok && v <= minimum {
		push(path, "expected > "+jsNumberString(minimum))
	}
	if maximum, ok := schemaNumber(n, "exclusiveMaximum"); ok && v >= maximum {
		push(path, "expected < "+jsNumberString(maximum))
	}
	if divisor, ok := schemaNumber(n, "multipleOf"); ok && !isMultiple(v, divisor) {
		push(path, "expected a multiple of "+jsNumberString(divisor))
	}
}

func (c *compiledSchema) walkObject(n *object, base string, v *object, path string, out *[]ValidationIssue) {
	push := func(at, message string) { *out = append(*out, ValidationIssue{Path: at, Message: message}) }
	properties, _ := n.values["properties"].(*object)
	if required, ok := n.values["required"].(*array); ok {
		for _, key := range required.items {
			if key, ok := key.(string); ok {
				if _, present := v.values[key]; !present {
					push(pointer(path, key), "missing")
				}
			}
		}
	}
	if properties != nil {
		for _, key := range properties.keys {
			if value, ok := v.values[key]; ok {
				c.walk(properties.values[key], base, value, pointer(path, key), out)
			}
		}
	}
	patterns := patternSchemas(n)
	additional, hasAdditional := n.get("additionalProperties")
	names, hasNames := n.get("propertyNames")
	for _, key := range v.keys {
		_, matched := declared(properties, key)
		for _, entry := range patterns {
			if entry.expression.MatchString(key) {
				matched = true
				c.walk(entry.schema, base, v.values[key], pointer(path, key), out)
			}
		}
		if !matched && hasAdditional {
			if additional == false {
				push(pointer(path, key), "unknown field")
			} else {
				c.walk(additional, base, v.values[key], pointer(path, key), out)
			}
		}
		if hasNames && !c.valid(names, base, key) {
			push(pointer(path, key), "invalid property name")
		}
	}
	count := float64(len(v.keys))
	if minimum, ok := schemaNumber(n, "minProperties"); ok && count < minimum {
		push(path, "expected at least "+jsNumberString(minimum)+" properties")
	}
	if maximum, ok := schemaNumber(n, "maxProperties"); ok && count > maximum {
		push(path, "expected at most "+jsNumberString(maximum)+" properties")
	}
	for _, dependency := range dependencies(n) {
		if _, present := v.values[dependency.key]; !present {
			continue
		}
		if dependency.required == nil {
			c.walk(dependency.schema, base, v, path, out)
			continue
		}
		for _, key := range dependency.required.items {
			name := jsString(key)
			if _, present := v.values[name]; !present {
				push(pointer(path, name), "required when "+dependency.key+" is present")
			}
		}
	}
}

func (c *compiledSchema) walkArray(n *object, base string, v *array, path string, out *[]ValidationIssue) {
	push := func(message string) { *out = append(*out, ValidationIssue{Path: path, Message: message}) }
	prefix, rest, hasRest := itemSchemas(n)
	for index, item := range v.items {
		at := path + "/" + strconv.Itoa(index)
		if index < len(prefix) {
			c.walk(prefix[index], base, item, at, out)
		} else if hasRest {
			c.walk(rest, base, item, at, out)
		}
	}
	count := float64(len(v.items))
	if minimum, ok := schemaNumber(n, "minItems"); ok && count < minimum {
		push("expected at least " + jsNumberString(minimum) + " items")
	}
	if maximum, ok := schemaNumber(n, "maxItems"); ok && count > maximum {
		push("expected at most " + jsNumberString(maximum) + " items")
	}
	if unique, _ := n.values["uniqueItems"].(bool); unique && !uniqueItems(v.items) {
		push("expected unique items")
	}
	if contains, ok := n.get("contains"); ok {
		minimum, maximum := containsBounds(n)
		if !containsCount(c, contains, base, v.items, minimum, maximum) {
			push(containsMessage(minimum, maximum))
		}
	}
}

func containsMessage(minimum, maximum float64) string {
	switch {
	case math.IsInf(maximum, 1):
		if minimum == 1 {
			return "expected at least 1 matching item"
		}
		return "expected at least " + jsNumberString(minimum) + " matching items"
	case minimum == 0:
		if maximum == 1 {
			return "expected at most 1 matching item"
		}
		return "expected at most " + jsNumberString(maximum) + " matching items"
	default:
		return "expected between " + jsNumberString(minimum) + " and " + jsNumberString(maximum) + " matching items"
	}
}

// walkUnion blames a failing union on one branch: the one a tag selects,
// else the one declaring most of an object's keys, else the one whose first
// error is deepest.
func (c *compiledSchema) walkUnion(branches []any, base string, v any, path string, out *[]ValidationIssue, exactlyOne bool) {
	matches := c.matches(branches, base, v)
	if matches == 1 || !exactlyOne && matches > 0 {
		return
	}
	if matches > 1 {
		*out = append(*out, ValidationIssue{Path: path, Message: "matches more than one alternative"})
		return
	}
	value, isObject := v.(*object)
	if key, tags, ok := c.unionTag(branches, base); ok && isObject {
		tag, present := value.values[key]
		for index, candidate := range tags {
			if present && candidate == tag {
				c.walk(branches[index], base, v, path, out)
				return
			}
		}
		*out = append(*out, ValidationIssue{Path: pointer(path, key), Message: "expected one of " + listValues(tags)})
		return
	}
	var best []ValidationIssue
	bestDeclared := 0
	for _, branch := range branches {
		var issues []ValidationIssue
		c.walk(branch, base, v, path, &issues)
		if len(issues) == 0 {
			continue
		}
		declaredKeys := 0
		if isObject {
			declaredKeys = c.declaredKeys(branch, base, value)
		}
		if best == nil || declaredKeys > bestDeclared || declaredKeys == bestDeclared && len(issues[0].Path) > len(best[0].Path) {
			best, bestDeclared = issues, declaredKeys
		}
	}
	if best == nil {
		best = []ValidationIssue{{Path: path, Message: "matches no alternative"}}
	}
	*out = append(*out, best...)
}

// branchSchema is a union branch, through its $ref.
func (c *compiledSchema) branchSchema(branch any, base string) any {
	if n, ok := branch.(*object); ok {
		if ref, ok := n.values["$ref"].(string); ok {
			target, _, found := c.resolve(ref, base)
			if !found {
				return false
			}
			return target
		}
	}
	return branch
}

func (c *compiledSchema) declaredKeys(branch any, base string, v *object) int {
	n, _ := c.branchSchema(branch, base).(*object)
	if n == nil {
		return 0
	}
	properties, _ := n.values["properties"].(*object)
	count := 0
	for _, key := range v.keys {
		if _, ok := declared(properties, key); ok {
			count++
		}
	}
	return count
}

// unionTag finds the first property of the first branch on which every
// branch declares a distinct primitive const.
func (c *compiledSchema) unionTag(branches []any, base string) (string, []any, bool) {
	resolved := make([]*object, len(branches))
	for index, branch := range branches {
		resolved[index], _ = c.branchSchema(branch, base).(*object)
	}
	if resolved[0] == nil {
		return "", nil, false
	}
	first, _ := resolved[0].values["properties"].(*object)
	if first == nil {
		return "", nil, false
	}
keys:
	for _, key := range first.keys {
		tags := make([]any, len(branches))
		for index, branch := range resolved {
			var properties *object
			if branch != nil {
				properties, _ = branch.values["properties"].(*object)
			}
			var tag *object
			if properties != nil {
				tag, _ = properties.values[key].(*object)
			}
			if tag == nil {
				continue keys
			}
			constant, ok := tag.get("const")
			_, isArray := constant.(*array)
			_, isObject := constant.(*object)
			if !ok || isArray || isObject {
				continue keys
			}
			for _, earlier := range tags[:index] {
				if earlier == constant {
					continue keys
				}
			}
			tags[index] = constant
		}
		return key, tags, true
	}
	return "", nil, false
}

func pointer(path, key string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// listValuesShown caps an enum error's list, so an enum of thousands stays
// readable.
const listValuesShown = 20

func listValues(values []any) string {
	if len(values) <= listValuesShown {
		return stringify(&array{items: values})
	}
	listed := stringify(&array{items: values[:listValuesShown]})
	return listed[:len(listed)-1] + ", ... and " + strconv.Itoa(len(values)-listValuesShown) + " more]"
}

func typeName(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case *array:
		return "array"
	case *object:
		return "object"
	case float64:
		if isInteger(v) {
			return "integer"
		}
		return "number"
	case bool:
		return "boolean"
	default:
		return "string"
	}
}

// joinTypes is Array.prototype.join(" or ") of a type keyword.
func joinTypes(types any) string {
	list, ok := types.(*array)
	if !ok {
		return jsString(types)
	}
	parts := make([]string, len(list.items))
	for index, item := range list.items {
		parts[index] = jsString(item)
	}
	return strings.Join(parts, " or ")
}

// jsString is String(value) as Array.prototype.join writes an element.
func jsString(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return jsNumberString(value)
	case *array:
		parts := make([]string, len(value.items))
		for index, item := range value.items {
			parts[index] = jsString(item)
		}
		return strings.Join(parts, ",")
	default:
		return "[object Object]"
	}
}
