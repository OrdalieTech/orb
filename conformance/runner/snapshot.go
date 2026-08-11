package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

// Add is Set for presentation members that may not exist yet: when the final
// path step names an object member that is absent, the member is appended, so
// Orb-owned presentation vocabulary can grow (a new theme role) without
// hand-editing fixture shape. Intermediate path steps must already exist, and
// behavior-shaped values keep no Add call sites, exactly like Set.
//
// Add is grow-only: a member whose source disappeared (a deleted theme role)
// stays in the fixture, and comparison mode fails on the stale value until it
// is removed by hand — the deliberate trade for preserving member order.
func (snapshot *Snapshot) Add(value any, path ...any) bool {
	if snapshot == nil {
		return false
	}
	snapshot.tb.Helper()
	if name, named := lastPathMember(path); named {
		if parent, ok := nodeAt(snapshot.root, path[:len(path)-1]); ok {
			if object, isObject := parent.(jsonwire.OrderedObject); isObject {
				if _, exists := object.Value(name); !exists {
					root, err := setSnapshotPath(snapshot.root, path[:len(path)-1], append(object, jsonwire.OrderedMember{Name: name, Value: nil}))
					if err != nil {
						snapshot.tb.Fatalf("conformance: add %s/%s %v: %v", snapshot.family, snapshot.name, path, err)
					}
					snapshot.root = root
				}
			}
		}
	}
	return snapshot.Set(value, path...)
}

// Has reports whether path exists in the snapshot. It is false in comparison
// mode, so it may only guard Set calls. Shape-preserving updates branch on it:
// a member the extraction wrote (even as null) is rewritten, an absent member
// stays absent.
func (snapshot *Snapshot) Has(path ...any) bool {
	if snapshot == nil {
		return false
	}
	_, ok := nodeAt(snapshot.root, path)
	return ok
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

// addSnapshotPath is setSnapshotPath except that a missing member named by
// the final path step is appended instead of rejected.
// nodeAt walks path (object member names and array indexes) and returns the
// node it names; it is the one path walker behind Has and Add.
func nodeAt(node any, path []any) (any, bool) {
	for _, step := range path {
		switch typed := step.(type) {
		case string:
			object, isObject := node.(jsonwire.OrderedObject)
			if !isObject {
				return nil, false
			}
			child, exists := object.Value(typed)
			if !exists {
				return nil, false
			}
			node = child
		case int:
			items, isArray := node.([]any)
			if !isArray || typed < 0 || typed >= len(items) {
				return nil, false
			}
			node = items[typed]
		default:
			return nil, false
		}
	}
	return node, true
}

// lastPathMember reports the member name a path ends with, false when it ends
// with an array index instead.
func lastPathMember(path []any) (string, bool) {
	if len(path) == 0 {
		return "", false
	}
	name, named := path[len(path)-1].(string)
	return name, named
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeOrderedValue(decoder, data)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing data at byte %d", decoder.InputOffset())
		}
		return nil, err
	}
	return value, nil
}

func decodeOrderedValue(decoder *json.Decoder, data []byte) (any, error) {
	start := decoder.InputOffset()
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case json.Delim:
		if token == '{' {
			object := jsonwire.OrderedObject{}
			for decoder.More() {
				start = decoder.InputOffset()
				if _, err := decoder.Token(); err != nil {
					return nil, err
				}
				name, err := jsonwire.UnmarshalStringToken(data[start:decoder.InputOffset()])
				if err != nil {
					return nil, err
				}
				value, err := decodeOrderedValue(decoder, data)
				if err != nil {
					return nil, err
				}
				object = append(object, jsonwire.OrderedMember{Name: name, Value: value})
			}
			_, err = decoder.Token()
			return object, err
		}
		if token == '[' {
			items := []any{}
			for decoder.More() {
				value, err := decodeOrderedValue(decoder, data)
				if err != nil {
					return nil, err
				}
				items = append(items, value)
			}
			_, err = decoder.Token()
			return items, err
		}
		return nil, fmt.Errorf("unexpected delimiter %q", token)
	case string:
		return jsonwire.UnmarshalStringToken(data[start:decoder.InputOffset()])
	default:
		return token, nil
	}
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
