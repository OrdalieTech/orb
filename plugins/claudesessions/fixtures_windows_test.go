//go:build windows

package claudesessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var systemSearchPath = filepath.Join(os.Getenv("SystemRoot"), "System32")

// writeFakeNPM writes the batch-file npm.cmd that PATHEXT resolves npm to: it
// records each attempt, lays down the SDK package under --prefix (%3), and
// fails its first run.
func writeFakeNPM(t *testing.T, path string) {
	t.Helper()
	script := `@echo off
>>"%TRACE_DIR%\attempts" echo attempt
mkdir "%~3\node_modules\@anthropic-ai\claude-agent-sdk"
>"%~3\node_modules\@anthropic-ai\claude-agent-sdk\sdk.mjs" echo fixture
>"%~3\node_modules\@anthropic-ai\claude-agent-sdk\package.json" echo {"version":"SDK_VERSION"}
if exist "%TRACE_DIR%\retry" exit /b 0
type nul > "%TRACE_DIR%\retry"
exit /b 1
`
	script = strings.ReplaceAll(strings.ReplaceAll(script, "SDK_VERSION", SDKVersion), "\n", "\r\n")
	if err := os.WriteFile(path+".cmd", []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}
