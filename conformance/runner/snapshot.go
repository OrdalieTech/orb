package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/OrdalieTech/orb/internal/jsonwire"
)

// Orb-owned TUI snapshot updating (D35). The F12-family render goldens are
// snapshots of Orb's own renderer: a deliberate presentation change reruns the
// consuming tests with ORB_UPDATE_F12=1, which rewrites the presentation
// values in place while leaving inputs and frozen behavior values untouched.
// Behavior-shaped values (focus traces, protocol writes, dispatch traces)
// deliberately have no Set call sites: they remain upstream-captured behavior
// gates, which D35 keeps at byte parity.
const updateSnapshotsEnv = "ORB_UPDATE_F12"

// UpdateTUISnapshots reports whether the run should rewrite Orb-owned TUI
// snapshots instead of comparing against them.
func UpdateTUISnapshots() bool {
	return os.Getenv(updateSnapshotsEnv) == "1"
}

// Snapshot is an in-place editor for one Orb-owned fixture file. A nil
// *Snapshot is the comparison-mode no-op, so call sites read
// `if !snap.Set(got, ...) { compare }`.
type Snapshot struct {
	tb     testing.TB
	family string
	name   string
	root   any
	dirty  bool
}

// OpenSnapshot returns an editor for family/name in update mode and nil
// otherwise. The rewritten file is saved on test cleanup.
func OpenSnapshot(tb testing.TB, family, name string) *Snapshot {
	if !UpdateTUISnapshots() {
		return nil
	}
	tb.Helper()
	data, err := ReadFixture(family, name)
	if err != nil {
		tb.Fatal(err)
	}
	root, err := decodeOrderedJSON(data)
	if err != nil {
		tb.Fatalf("conformance: parse %s/%s: %v", family, name, err)
	}
	snapshot := &Snapshot{tb: tb, family: family, name: name, root: root}
	tb.Cleanup(snapshot.save)
	return snapshot
}

// Set splices value at path (object member names and array indexes) and
// reports true when the caller should skip its comparison. Values marshal
// through the wire-format encoder, so replaced members keep JSON.stringify's
// spelling and untouched members keep their exact bytes.
func (snapshot *Snapshot) Set(value any, path ...any) bool {
	if snapshot == nil {
		return false
	}
	snapshot.tb.Helper()
	normalized, err := normalizeSnapshotValue(value)
	if err != nil {
		snapshot.tb.Fatalf("conformance: normalize %s/%s %v: %v", snapshot.family, snapshot.name, path, err)
	}
	root, err := setSnapshotPath(snapshot.root, path, normalized)
	if err != nil {
		snapshot.tb.Fatalf("conformance: set %s/%s %v: %v", snapshot.family, snapshot.name, path, err)
	}
	snapshot.root = root
	snapshot.dirty = true
	return true
}

// Has reports whether path exists in the snapshot. It is false in comparison
// mode, so it may only guard Set calls. Shape-preserving updates branch on it:
// a member the extraction wrote (even as null) is rewritten, an absent member
// stays absent.
func (snapshot *Snapshot) Has(path ...any) bool {
	if snapshot == nil {
		return false
	}
	node := snapshot.root
	for _, step := range path {
		switch typed := step.(type) {
		case string:
			object, ok := node.(jsonwire.OrderedObject)
			if !ok {
				return false
			}
			value, ok := object.Value(typed)
			if !ok {
				return false
			}
			node = value
		case int:
			items, ok := node.([]any)
			if !ok || typed < 0 || typed >= len(items) {
				return false
			}
			node = items[typed]
		default:
			return false
		}
	}
	return true
}

func (snapshot *Snapshot) save() {
	if !snapshot.dirty {
		return
	}
	var output []byte
	output, err := appendOrderedJSON(output, snapshot.root, 0)
	if err != nil {
		snapshot.tb.Errorf("conformance: encode %s/%s: %v", snapshot.family, snapshot.name, err)
		return
	}
	output = append(output, '\n')
	target := filepath.Join(FixtureRoot(), snapshot.family, filepath.FromSlash(snapshot.name))
	if err := os.WriteFile(target, output, 0o644); err != nil {
		snapshot.tb.Errorf("conformance: write %s: %v", target, err)
	}
}

func setSnapshotPath(node any, path []any, value any) (any, error) {
	if len(path) == 0 {
		return value, nil
	}
	switch step := path[0].(type) {
	case string:
		object, ok := node.(jsonwire.OrderedObject)
		if !ok {
			return nil, fmt.Errorf("member %q of non-object %T", step, node)
		}
		for index := range object {
			if object[index].Name == step {
				replaced, err := setSnapshotPath(object[index].Value, path[1:], value)
				if err != nil {
					return nil, err
				}
				object[index].Value = replaced
				return object, nil
			}
		}
		return nil, fmt.Errorf("member %q is missing", step)
	case int:
		items, ok := node.([]any)
		if !ok {
			return nil, fmt.Errorf("index %d of non-array %T", step, node)
		}
		if step < 0 || step >= len(items) {
			return nil, fmt.Errorf("index %d out of range 0..%d", step, len(items)-1)
		}
		replaced, err := setSnapshotPath(items[step], path[1:], value)
		if err != nil {
			return nil, err
		}
		items[step] = replaced
		return items, nil
	default:
		return nil, fmt.Errorf("path element %T is neither member name nor index", path[0])
	}
}

// normalizeSnapshotValue converts a Go value into the ordered representation.
// Strings pass through untouched (the encoder writes them WTF-8-safely);
// everything else round-trips through the wire marshaller so struct field
// order becomes JSON member order.
func normalizeSnapshotValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil, bool, string, json.Number, jsonwire.OrderedObject:
		return typed, nil
	case int:
		return json.Number(strconv.Itoa(typed)), nil
	case []string:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = item
		}
		return items, nil
	case []any:
		return typed, nil
	default:
		encoded, err := jsonwire.Marshal(value)
		if err != nil {
			return nil, err
		}
		return decodeOrderedJSON(encoded)
	}
}

// decodeOrderedJSON parses JSON preserving member order, number spellings
// (as json.Number), and WTF-8 lone surrogates in strings, so that an
// unmodified document re-encodes byte-identically.
func decodeOrderedJSON(data []byte) (any, error) {
	decoder := &orderedJSONDecoder{data: data}
	decoder.skipSpace()
	value, err := decoder.parseValue()
	if err != nil {
		return nil, err
	}
	decoder.skipSpace()
	if decoder.pos != len(decoder.data) {
		return nil, fmt.Errorf("trailing data at byte %d", decoder.pos)
	}
	return value, nil
}

type orderedJSONDecoder struct {
	data []byte
	pos  int
}

func (decoder *orderedJSONDecoder) skipSpace() {
	for decoder.pos < len(decoder.data) {
		switch decoder.data[decoder.pos] {
		case ' ', '\t', '\n', '\r':
			decoder.pos++
		default:
			return
		}
	}
}

func (decoder *orderedJSONDecoder) parseValue() (any, error) {
	if decoder.pos >= len(decoder.data) {
		return nil, fmt.Errorf("unexpected end of document")
	}
	switch char := decoder.data[decoder.pos]; {
	case char == '{':
		return decoder.parseObject()
	case char == '[':
		return decoder.parseArray()
	case char == '"':
		return decoder.parseString()
	case char == 't':
		return decoder.parseLiteral("true", true)
	case char == 'f':
		return decoder.parseLiteral("false", false)
	case char == 'n':
		return decoder.parseLiteral("null", nil)
	case char == '-' || (char >= '0' && char <= '9'):
		return decoder.parseNumber()
	default:
		return nil, fmt.Errorf("unexpected byte %q at %d", char, decoder.pos)
	}
}

func (decoder *orderedJSONDecoder) parseObject() (any, error) {
	decoder.pos++
	object := jsonwire.OrderedObject{}
	decoder.skipSpace()
	if decoder.pos < len(decoder.data) && decoder.data[decoder.pos] == '}' {
		decoder.pos++
		return object, nil
	}
	for {
		decoder.skipSpace()
		name, err := decoder.parseString()
		if err != nil {
			return nil, err
		}
		decoder.skipSpace()
		if decoder.pos >= len(decoder.data) || decoder.data[decoder.pos] != ':' {
			return nil, fmt.Errorf("missing ':' at byte %d", decoder.pos)
		}
		decoder.pos++
		decoder.skipSpace()
		value, err := decoder.parseValue()
		if err != nil {
			return nil, err
		}
		object = append(object, jsonwire.OrderedMember{Name: name.(string), Value: value})
		decoder.skipSpace()
		if decoder.pos >= len(decoder.data) {
			return nil, fmt.Errorf("unterminated object")
		}
		switch decoder.data[decoder.pos] {
		case ',':
			decoder.pos++
		case '}':
			decoder.pos++
			return object, nil
		default:
			return nil, fmt.Errorf("unexpected byte %q at %d", decoder.data[decoder.pos], decoder.pos)
		}
	}
}

func (decoder *orderedJSONDecoder) parseArray() (any, error) {
	decoder.pos++
	items := []any{}
	decoder.skipSpace()
	if decoder.pos < len(decoder.data) && decoder.data[decoder.pos] == ']' {
		decoder.pos++
		return items, nil
	}
	for {
		decoder.skipSpace()
		value, err := decoder.parseValue()
		if err != nil {
			return nil, err
		}
		items = append(items, value)
		decoder.skipSpace()
		if decoder.pos >= len(decoder.data) {
			return nil, fmt.Errorf("unterminated array")
		}
		switch decoder.data[decoder.pos] {
		case ',':
			decoder.pos++
		case ']':
			decoder.pos++
			return items, nil
		default:
			return nil, fmt.Errorf("unexpected byte %q at %d", decoder.data[decoder.pos], decoder.pos)
		}
	}
}

func (decoder *orderedJSONDecoder) parseString() (any, error) {
	start := decoder.pos
	if decoder.data[start] != '"' {
		return nil, fmt.Errorf("expected string at byte %d", start)
	}
	escaped := false
	for index := start + 1; index < len(decoder.data); index++ {
		switch {
		case escaped:
			escaped = false
		case decoder.data[index] == '\\':
			escaped = true
		case decoder.data[index] == '"':
			decoder.pos = index + 1
			return jsonwire.UnmarshalString(decoder.data[start : index+1])
		}
	}
	return nil, fmt.Errorf("unterminated string at byte %d", start)
}

func (decoder *orderedJSONDecoder) parseLiteral(literal string, value any) (any, error) {
	if decoder.pos+len(literal) > len(decoder.data) || string(decoder.data[decoder.pos:decoder.pos+len(literal)]) != literal {
		return nil, fmt.Errorf("invalid literal at byte %d", decoder.pos)
	}
	decoder.pos += len(literal)
	return value, nil
}

func (decoder *orderedJSONDecoder) parseNumber() (any, error) {
	start := decoder.pos
	for decoder.pos < len(decoder.data) {
		switch char := decoder.data[decoder.pos]; {
		case char >= '0' && char <= '9', char == '-', char == '+', char == '.', char == 'e', char == 'E':
			decoder.pos++
		default:
			return json.Number(decoder.data[start:decoder.pos]), nil
		}
	}
	return json.Number(decoder.data[start:decoder.pos]), nil
}

// appendOrderedJSON writes the ordered representation in
// JSON.stringify(value, null, 2) layout, matching the extraction scripts'
// historical serialization byte for byte.
func appendOrderedJSON(output []byte, value any, depth int) ([]byte, error) {
	switch typed := value.(type) {
	case jsonwire.OrderedObject:
		if len(typed) == 0 {
			return append(output, '{', '}'), nil
		}
		output = append(output, '{')
		for index, member := range typed {
			if index > 0 {
				output = append(output, ',')
			}
			output = appendSnapshotIndent(output, depth+1)
			name, err := jsonwire.MarshalString(member.Name)
			if err != nil {
				return nil, err
			}
			output = append(output, name...)
			output = append(output, ':', ' ')
			if output, err = appendOrderedJSON(output, member.Value, depth+1); err != nil {
				return nil, err
			}
		}
		output = appendSnapshotIndent(output, depth)
		return append(output, '}'), nil
	case []any:
		if len(typed) == 0 {
			return append(output, '[', ']'), nil
		}
		output = append(output, '[')
		for index, item := range typed {
			if index > 0 {
				output = append(output, ',')
			}
			output = appendSnapshotIndent(output, depth+1)
			var err error
			if output, err = appendOrderedJSON(output, item, depth+1); err != nil {
				return nil, err
			}
		}
		output = appendSnapshotIndent(output, depth)
		return append(output, ']'), nil
	case string:
		encoded, err := jsonwire.MarshalString(typed)
		if err != nil {
			return nil, err
		}
		return append(output, encoded...), nil
	case json.Number:
		return append(output, typed...), nil
	case bool:
		if typed {
			return append(output, "true"...), nil
		}
		return append(output, "false"...), nil
	case nil:
		return append(output, "null"...), nil
	default:
		return nil, fmt.Errorf("unsupported snapshot value %T", value)
	}
}

func appendSnapshotIndent(output []byte, depth int) []byte {
	output = append(output, '\n')
	for range depth {
		output = append(output, ' ', ' ')
	}
	return output
}
