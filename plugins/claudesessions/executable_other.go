//go:build !windows

package claudesessions

import (
	"os"
	"strings"
)

// runnable reports whether path names a file the exec permission bits allow.
func runnable(path string) (string, bool) {
	info, err := os.Stat(path)
	return path, err == nil && !info.IsDir() && info.Mode().Perm()&0111 != 0
}

func hasPathSeparator(name string) bool {
	return strings.ContainsRune(name, os.PathSeparator)
}

func searchPath(item string) (string, bool) {
	return strings.CutPrefix(item, "PATH=")
}
