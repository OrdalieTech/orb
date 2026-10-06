// Package toolenv is the environment of the processes Orb's tools start: bash,
// MCP servers, extension hosts and external agents.
package toolenv

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	allowed := Get(environ, Allow)
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

const windows = runtime.GOOS == "windows"

// Get returns name's last value in env; win32 matches names case-insensitively.
func Get(env []string, name string) string {
	for index := len(env) - 1; index >= 0; index-- {
		if key, value, ok := strings.Cut(env[index], "="); ok && nameEqual(key, name) {
			return value
		}
	}
	return ""
}

// Set replaces every entry for name in env with one name=value, keeping the
// spelling env already uses (Windows' Path stays Path), so a later lookup by
// that spelling finds it.
func Set(env []string, name, value string) []string {
	key := name
	env = slices.DeleteFunc(env, func(entry string) bool {
		existing, _, ok := strings.Cut(entry, "=")
		if ok && nameEqual(existing, name) {
			key = existing
			return true
		}
		return false
	})
	return append(env, key+"="+value)
}

// PrependPath puts dir first on the search list value unless it is already there.
func PrependPath(dir, value string) string {
	if value == "" {
		return dir
	}
	if slices.Contains(filepath.SplitList(value), dir) {
		return value
	}
	return dir + string(os.PathListSeparator) + value
}

// Merge overlays layers on base, later layers winning, sorted by name.
func Merge(base []string, layers ...map[string]string) []string {
	values := make(map[string]string, len(base))
	for _, entry := range base {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	for _, layer := range layers {
		maps.Copy(values, layer)
	}
	merged := make([]string, 0, len(values))
	for _, key := range slices.Sorted(maps.Keys(values)) {
		merged = append(merged, key+"="+values[key])
	}
	return merged
}

// LookPath finds the executable name runs under env: a name with a path
// separator as is, any other through env's PATH, never a relative entry.
func LookPath(name string, env []string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) || windows && strings.ContainsAny(name, ":/") {
		return exec.LookPath(name)
	}
	for _, dir := range filepath.SplitList(Get(env, "PATH")) {
		if filepath.IsAbs(dir) {
			if resolved, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
				return resolved, nil
			}
		}
	}
	return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
}

func nameEqual(left, right string) bool {
	return left == right || windows && strings.EqualFold(left, right)
}
