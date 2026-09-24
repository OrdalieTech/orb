// Package scenario_test is the cross-host conformance gate (DECISIONS.md P10): the same
// scripted agent turn, with every port supplied in memory, must produce the same session
// natively, in a browser-like js/wasm worker without a host filesystem, and under WASI
// without mounts.
package scenario_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScenarioIsIdenticalOnEveryHost(t *testing.T) {
	root := moduleRoot(t)
	goRoot := strings.TrimSpace(command(t, root, nil, "go", "env", "GOROOT"))
	dir := t.TempDir()
	build := func(goos, goarch, output string) string {
		binary := filepath.Join(dir, output)
		command(t, root, []string{"GOOS=" + goos, "GOARCH=" + goarch, "CGO_ENABLED=0"}, "go", "build", "-o", binary, "./conformance/scenario/testdata/session")
		return binary
	}
	hosts := map[string]string{
		"native": command(t, root, nil, build(runtime.GOOS, runtime.GOARCH, "session-native"+nativeSuffix())),
		"js/wasm": command(t, root, nil, "node", "conformance/scenario/testdata/run.cjs",
			filepath.Join(goRoot, "lib/wasm/wasm_exec.js"), build("js", "wasm", "session-js.wasm")),
		"wasip1/wasm": command(t, root, nil, tool(t, root, "wazero"), "run", build("wasip1", "wasm", "session-wasi.wasm")),
	}
	want := hosts["native"]
	if !strings.HasSuffix(want, "scenario OK\n") {
		t.Fatalf("native scenario did not complete:\n%s", want)
	}
	for host, got := range hosts {
		if got != want {
			t.Errorf("%s diverges from native:\n%s\nwant:\n%s", host, got, want)
		}
	}
}

func command(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return string(output)
}

func nativeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// tool finds a development runtime on PATH or where the Makefile installs it.
func tool(t *testing.T, root, name string) string {
	t.Helper()
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	path := filepath.Join(root, ".tools", "bin", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s is required; run `make portability` to install it", name)
	}
	return path
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	return filepath.Dir(strings.TrimSpace(command(t, ".", nil, "go", "env", "GOMOD")))
}
