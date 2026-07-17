package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
