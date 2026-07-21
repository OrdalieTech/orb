package runner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/internal/jsonwire"
)

// Manifest identifies the upstream source and generator for a fixture family.
type Manifest struct {
	Family         string   `json:"family"`
	UpstreamCommit string   `json:"upstreamCommit"`
	Generator      string   `json:"generator"`
	Source         string   `json:"source"`
	Files          []string `json:"files"`
}

// FixtureRoot returns the absolute path to the committed fixture tree.
func FixtureRoot() string {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		panic("conformance: cannot locate fixture helper")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "fixtures"))
}

// ReplacePathAliases replaces both a path and its realpath alias.
func ReplacePathAliases(value, path, replacement string) string {
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		value = strings.ReplaceAll(value, canonical, replacement)
	}
	return strings.ReplaceAll(value, path, replacement)
}

// ReadFixture reads one file from a fixture family.
func ReadFixture(family, name string) ([]byte, error) {
	if !validPathElement(family) || !validPathElement(name) {
		return nil, fmt.Errorf("conformance: invalid fixture path %q/%q", family, name)
	}
	data, err := os.ReadFile(filepath.Join(FixtureRoot(), family, name))
	if err != nil {
		return nil, fmt.Errorf("conformance: read %s/%s: %w", family, name, err)
	}
	return data, nil
}

// LoadJSON loads one JSON fixture or fails the calling test.
func LoadJSON(t testing.TB, family, name string, target any) {
	t.Helper()
	data, err := ReadFixture(family, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("conformance: decode %s/%s: %v", family, name, err)
	}
}

// LoadManifest loads a fixture family's manifest or fails the calling test.
func LoadManifest(t testing.TB, family string) Manifest {
	t.Helper()
	var manifest Manifest
	LoadJSON(t, family, "manifest.json", &manifest)
	return manifest
}

// DecodeJSONLines validates strict LF-delimited JSONL and returns the original
// line bytes. Each line must contain exactly one JSON value and the stream must
// end immediately after a final LF.
func DecodeJSONLines(data []byte) ([]json.RawMessage, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("decode JSONL: empty stream")
	}
	reader := bufio.NewReader(bytes.NewReader(data))
	lines := make([]json.RawMessage, 0)
	for lineNumber := 1; ; lineNumber++ {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			if len(line) != 0 {
				return nil, fmt.Errorf("decode JSONL line %d: missing final LF", lineNumber)
			}
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode JSONL line %d: %w", lineNumber, err)
		}
		line = line[:len(line)-1]
		if len(line) == 0 {
			return nil, fmt.Errorf("decode JSONL line %d: empty line", lineNumber)
		}
		if line[len(line)-1] == '\r' {
			return nil, fmt.Errorf("decode JSONL line %d: CRLF framing is not allowed", lineNumber)
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.UseNumber()
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode JSONL line %d: %w", lineNumber, err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			if err == nil {
				return nil, fmt.Errorf("decode JSONL line %d: multiple values", lineNumber)
			}
			return nil, fmt.Errorf("decode JSONL line %d trailer: %w", lineNumber, err)
		}
		lines = append(lines, bytes.Clone(line))
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("decode JSONL: empty stream")
	}
	return lines, nil
}

// LoadJSONLines loads and validates one JSONL fixture or fails the calling test.
func LoadJSONLines(t testing.TB, family, name string) []json.RawMessage {
	t.Helper()
	data, err := ReadFixture(family, name)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := DecodeJSONLines(data)
	if err != nil {
		t.Fatalf("conformance: decode %s/%s: %v", family, name, err)
	}
	return lines
}

// CanonicalJSON normalizes insignificant JSON whitespace and object-key order.
func CanonicalJSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode JSON: multiple values")
		}
		return nil, fmt.Errorf("decode JSON trailer: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode canonical JSON: %w", err)
	}
	return canonical, nil
}

// CanonicalJSONLexemes sorts object keys while retaining scalar JSON spellings.
// It is used for wire fixtures where "<" and "\u003c", or 1 and 1.0, are
// semantically equal JSON but observably different protocol bytes.
func CanonicalJSONLexemes(data []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(data)
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("decode JSON: invalid value")
	}
	return canonicalizeLexemes(trimmed)
}

func canonicalizeLexemes(data []byte) ([]byte, error) {
	switch data[0] {
	case '{':
		var members map[string]json.RawMessage
		if err := json.Unmarshal(data, &members); err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(members))
		for key := range members {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var output bytes.Buffer
		output.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				output.WriteByte(',')
			}
			encodedKey, err := jsonwire.Marshal(key)
			if err != nil {
				return nil, err
			}
			output.Write(encodedKey)
			output.WriteByte(':')
			value, err := canonicalizeLexemes(bytes.TrimSpace(members[key]))
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", key, err)
			}
			output.Write(value)
		}
		output.WriteByte('}')
		return output.Bytes(), nil
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, err
		}
		var output bytes.Buffer
		output.WriteByte('[')
		for index, item := range items {
			if index > 0 {
				output.WriteByte(',')
			}
			value, err := canonicalizeLexemes(bytes.TrimSpace(item))
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", index, err)
			}
			output.Write(value)
		}
		output.WriteByte(']')
		return output.Bytes(), nil
	default:
		return bytes.Clone(data), nil
	}
}

// ByteDiff reports the first differing byte with compact quoted context.
func ByteDiff(want, got []byte) string {
	if bytes.Equal(want, got) {
		return ""
	}
	first := 0
	for first < len(want) && first < len(got) && want[first] == got[first] {
		first++
	}
	const contextBytes = 32
	start := max(0, first-contextBytes)
	wantEnd := min(len(want), first+contextBytes)
	gotEnd := min(len(got), first+contextBytes)
	return fmt.Sprintf(
		"first difference at byte %d (want %d bytes, got %d)\nwant: %q\n got: %q",
		first,
		len(want),
		len(got),
		want[start:wantEnd],
		got[start:gotEnd],
	)
}

func validPathElement(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}
