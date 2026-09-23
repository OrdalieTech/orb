//go:build !windows

package claudesessions

import (
	"os"
	"strings"
	"testing"
)

const systemSearchPath = "/usr/bin:/bin"

// writeFakeNPM writes an npm that records each attempt, lays down the SDK
// package under --prefix ($3), and fails its first run.
func writeFakeNPM(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
printf 'attempt\n' >> "$TRACE_DIR/attempts"
mkdir -p "$3/node_modules/@anthropic-ai/claude-agent-sdk"
printf 'fixture' > "$3/node_modules/@anthropic-ai/claude-agent-sdk/sdk.mjs"
printf '{"version":"SDK_VERSION"}' > "$3/node_modules/@anthropic-ai/claude-agent-sdk/package.json"
if [ ! -f "$TRACE_DIR/retry" ]; then touch "$TRACE_DIR/retry"; exit 1; fi
`
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(script, "SDK_VERSION", SDKVersion)), 0700); err != nil {
		t.Fatal(err)
	}
}
