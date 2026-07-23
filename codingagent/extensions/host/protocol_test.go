package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fragmentedReader struct {
	data []byte
	step int
}

func (reader *fragmentedReader) Read(target []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	count := reader.step
	if count > len(reader.data) {
		count = len(reader.data)
	}
	if count > len(target) {
		count = len(target)
	}
	copy(target, reader.data[:count])
	reader.data = reader.data[count:]
	return count, nil
}

func TestCodecReadsFragmentedLines(t *testing.T) {
	encoded := []byte(`{"protocol":"pigo-extension-host","version":1,"kind":"event","method":"log","params":{"message":"ok"}}` + "\n")
	codec := newCodec(&fragmentedReader{data: encoded, step: 3}, io.Discard)
	value, err := codec.read()
	if err != nil {
		t.Fatal(err)
	}
	if value.Kind != frameEvent || value.Method != "log" || !bytes.Contains(value.Params, []byte(`"ok"`)) {
		t.Fatalf("frame = %#v", value)
	}
}

func TestGenerationCorrelatesInterleavedResponses(t *testing.T) {
	first := make(chan pendingResponse, 1)
	second := make(chan pendingResponse, 1)
	generation := &generation{
		pending: map[string]chan pendingResponse{"pigo-1": first, "pigo-2": second},
		updates: make(map[string]func(json.RawMessage)),
	}
	generation.routeResponse(frame{ID: "pigo-2", Result: json.RawMessage(`{"order":2}`)})
	generation.routeResponse(frame{ID: "pigo-1", Result: json.RawMessage(`{"order":1}`)})
	if got := string((<-first).result); got != `{"order":1}` {
		t.Fatalf("first response = %s", got)
	}
	if got := string((<-second).result); got != `{"order":2}` {
		t.Fatalf("second response = %s", got)
	}
}

func TestCodecRejectsOversizedFrames(t *testing.T) {
	encoded := append(bytes.Repeat([]byte{'x'}, MaxFrameSize+1), '\n')
	_, err := newCodec(bytes.NewReader(encoded), io.Discard).read()
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("error = %v, want ErrFrameTooLarge", err)
	}
}

func TestCodecWritesOneJSONLine(t *testing.T) {
	var output bytes.Buffer
	codec := newCodec(strings.NewReader(""), &output)
	value, err := eventFrame("log", map[string]string{"message": "line\nvalue"})
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.write(value); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(output.Bytes(), []byte{'\n'}) != 1 || !json.Valid(bytes.TrimSuffix(output.Bytes(), []byte{'\n'})) {
		t.Fatalf("encoded frame = %q", output.String())
	}
}

func TestNodeAtLeast226(t *testing.T) {
	for _, version := range []string{"22.6.0", "22.12.1", "23.0.0", "24.1.0-nightly"} {
		if !nodeAtLeast226(version) {
			t.Errorf("nodeAtLeast226(%q) = false", version)
		}
	}
	for _, version := range []string{"", "22", "22.5.9", "21.99.0", "dev"} {
		if nodeAtLeast226(version) {
			t.Errorf("nodeAtLeast226(%q) = true", version)
		}
	}
}

func TestDiscoverRuntimePrefersSupportedNode(t *testing.T) {
	directory := t.TempDir()
	writeRuntimeFixture(t, directory, "node", "v22.6.0")
	writeRuntimeFixture(t, directory, "bun", "1.3.0")
	t.Setenv("PATH", directory)
	runtime, err := DiscoverRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Name != "node" || runtime.Version != "22.6.0" {
		t.Fatalf("runtime = %#v", runtime)
	}
	if got := strings.Join(runtime.Args, " "); got != "--experimental-strip-types --disable-warning=ExperimentalWarning --disable-warning=MODULE_TYPELESS_PACKAGE_JSON --preserve-symlinks" {
		t.Fatalf("node runtime arguments = %q", got)
	}
}

func TestDiscoverRuntimeTransformsNonErasableTypeScriptWhenSupported(t *testing.T) {
	directory := t.TempDir()
	writeRuntimeFixture(t, directory, "node", "v22.7.0")
	t.Setenv("PATH", directory)
	runtime, err := DiscoverRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(runtime.Args, " "); got != "--experimental-strip-types --disable-warning=ExperimentalWarning --disable-warning=MODULE_TYPELESS_PACKAGE_JSON --experimental-transform-types --preserve-symlinks" {
		t.Fatalf("node runtime arguments = %q", got)
	}
}

func TestDiscoverRuntimeFallsBackToBunForOldNode(t *testing.T) {
	directory := t.TempDir()
	writeRuntimeFixture(t, directory, "node", "v22.5.9")
	writeRuntimeFixture(t, directory, "bun", "1.3.0")
	t.Setenv("PATH", directory)
	runtime, err := DiscoverRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Name != "bun" || runtime.Version != "1.3.0" {
		t.Fatalf("runtime = %#v", runtime)
	}
}

func TestDiscoverRuntimeReturnsTypedError(t *testing.T) {
	directory := t.TempDir()
	writeRuntimeFixture(t, directory, "node", "v22.5.9")
	t.Setenv("PATH", directory)
	_, err := DiscoverRuntime(context.Background())
	var unavailable *RuntimeUnavailableError
	if !errors.As(err, &unavailable) || unavailable.NodeVersion != "22.5.9" {
		t.Fatalf("error = %#v", err)
	}
	if unavailable.Diagnostic().Message != runtimeUnavailableMessage {
		t.Fatalf("diagnostic = %#v", unavailable.Diagnostic())
	}
}

func writeRuntimeFixture(t *testing.T, directory, name, version string) {
	t.Helper()
	path := filepath.Join(directory, name)
	source := "#!/bin/sh\nprintf '%s\\n' '" + version + "'\n"
	if err := os.WriteFile(path, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
}
