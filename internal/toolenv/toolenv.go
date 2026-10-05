// Package toolenv is the environment of the processes Orb's tools start: bash,
// MCP servers, extension hosts and external agents.
package toolenv

import (
	"os"
	"slices"
	"strings"
)

// Allow names the ORB_TOOL_ENV variable: a comma-separated allowlist of the
// variables tool processes inherit. Unset, they inherit Orb's whole
// environment, as pi's do; a team agent sets it so its credentials stay with Orb.
const Allow = "ORB_TOOL_ENV"

// Environ is os.Environ narrowed to the ORB_TOOL_ENV allowlist when one is set.
func Environ() []string {
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
