package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWasmToolsUseInjectedOperationsWithoutHostFilesystem(t *testing.T) {
	for _, target := range []string{"js", "wasip1"} {
		t.Run(target, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "tools.wasm")
			build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./testdata/wasm")
			build.Env = append(os.Environ(), "GOOS="+target, "GOARCH=wasm", "CGO_ENABLED=0")
			if output, err := build.CombinedOutput(); err != nil {
				t.Fatalf("Wasm build: %v\n%s", err, output)
			}
			if target == "js" {
				goRoot, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output()
				if err != nil {
					t.Fatal(err)
				}
				run := exec.CommandContext(t.Context(), "node", "testdata/wasm/run.cjs", filepath.Join(strings.TrimSpace(string(goRoot)), "lib/wasm/wasm_exec.js"), binary)
				if output, err := run.CombinedOutput(); err != nil || !strings.Contains(string(output), "portable tools OK") {
					t.Fatalf("Wasm tools: %v\n%s", err, output)
				}
			}
		})
	}
}
