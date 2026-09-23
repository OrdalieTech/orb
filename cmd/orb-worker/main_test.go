package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDurableObjectBundle runs the deployable bundle — wasm_exec.js, the
// scripted model and platforms/worker/deploy/worker.mjs over this program —
// in Node against Map-backed Durable Object storage, across an object restart.
func TestDurableObjectBundle(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to run the Worker bundle")
	}
	dir := t.TempDir()
	build := exec.CommandContext(t.Context(), "go", "build", "-o", filepath.Join(dir, "orb.wasm"), ".")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("Wasm build: %v\n%s", err, output)
	}
	root, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	var bundle []byte
	for _, part := range []string{
		filepath.Join(strings.TrimSpace(string(root)), "lib/wasm/wasm_exec.js"),
		"../../platforms/worker/e2e/fake-model.js",
		"../../platforms/worker/deploy/worker.mjs",
	} {
		data, err := os.ReadFile(part)
		if err != nil {
			t.Fatal(err)
		}
		bundle = append(append(bundle, data...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "worker.mjs"), bundle, 0o644); err != nil {
		t.Fatal(err)
	}
	run := exec.CommandContext(t.Context(), node, "testdata/durable.mjs", filepath.Join(dir, "worker.mjs"))
	// Go's js/wasm net/http bypasses fetch when argv0 starts with "node".
	run.Args[0] = "orb-worker-test"
	output, err := run.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "durable harness OK") {
		t.Fatalf("Durable Object bundle: %v\n%s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
}
