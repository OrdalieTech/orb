package harness

import (
	"runtime"
	"testing"
)

// RequireProcesses skips tests of the native shell backend on hosts without
// process execution; the Exec port is optional there (DECISIONS.md P10).
func RequireProcesses(t *testing.T) {
	t.Helper()
	if runtime.GOARCH == "wasm" {
		t.Skip("process execution is unavailable on " + runtime.GOOS + "/wasm")
	}
}
