//go:build windows

package claudesessions

import (
	"os/exec"
	"strings"
)

// runnable reports the file Windows runs for path: path itself when it has an
// extension, else path plus the first PATHEXT extension present (npm.cmd).
// Windows has no exec permission bits.
func runnable(path string) (string, bool) {
	resolved, err := exec.LookPath(path)
	return resolved, err == nil
}

func hasPathSeparator(name string) bool {
	return strings.ContainsAny(name, `:\/`)
}

// Environment variable names are case-insensitive on Windows ("Path").
func searchPath(item string) (string, bool) {
	key, value, ok := strings.Cut(item, "=")
	return value, ok && strings.EqualFold(key, "PATH")
}
