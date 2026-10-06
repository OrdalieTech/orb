// Package toolenv is the environment of the processes Orb's tools start: bash,
// MCP servers, extension hosts and external agents.
package toolenv

import (
	"os"
	"slices"
	"strings"
	"sync"
)

// Allow names the ORB_TOOL_ENV variable: a comma-separated allowlist of the
// variables tool processes inherit. Unset, they inherit Orb's whole
// environment, as pi's do; a team agent sets it so its credentials stay with Orb.
const Allow = "ORB_TOOL_ENV"

var (
	exportsMu sync.Mutex
	exports   []string
)

// Export adds name=value to every tool process's environment from now on,
// whatever the allowlist says and whenever it is set: how a component hands
// its tools something, such as a socket to call it back on, without widening
// what they inherit from Orb.
func Export(name, value string) {
	exportsMu.Lock()
	defer exportsMu.Unlock()
	exports = append(exports, name+"="+value)
}

// Environ is os.Environ narrowed to the ORB_TOOL_ENV allowlist when one is
// set, plus the exported variables, which win over Orb's own.
func Environ() []string {
	exportsMu.Lock()
	exported := slices.Clone(exports)
	exportsMu.Unlock()
	return append(allowed(), exported...)
}

func allowed() []string {
	environ := os.Environ()
	allowed := os.Getenv(Allow)
	if allowed == "" {
		return environ
	}
	names := strings.Split(allowed, ",")
	return slices.DeleteFunc(environ, func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		// Windows names variables case-insensitively (PATH is Path there).
		return !slices.ContainsFunc(names, func(allowed string) bool { return strings.EqualFold(strings.TrimSpace(allowed), name) })
	})
}
