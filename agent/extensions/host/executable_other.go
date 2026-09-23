//go:build !windows

package host

import (
	"os"
	"strings"
)

// resolveExecutable reports whether path names a runnable file, as the exec
// permission bits say.
func resolveExecutable(path string) (string, bool) {
	info, err := os.Stat(path)
	return path, err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

func hasPathSeparator(name string) bool {
	return strings.ContainsRune(name, os.PathSeparator)
}

func environmentNameEqual(left, right string) bool {
	return left == right
}

// nodeSearchCandidate maps a nodeSearchPatterns match to the Node it names:
// POSIX patterns match the executable itself.
func nodeSearchCandidate(match string) string {
	return match
}

// nodeInstallLayouts are the version-manager install trees, relative to the
// manager's root: the first environment variable set, else home plus fallback.
var nodeInstallLayouts = []nodeInstallLayout{
	{env: []string{"NVM_DIR"}, fallback: []string{".nvm"}, parts: []string{"versions", "node", "*", "bin", "node"}},
	{env: []string{"FNM_DIR"}, fallback: []string{".local", "share", "fnm"}, parts: []string{"node-versions", "*", "installation", "bin", "node"}},
	{fallback: []string{"Library", "Application Support", "fnm"}, parts: []string{"node-versions", "*", "installation", "bin", "node"}},
	{env: []string{"VOLTA_HOME"}, fallback: []string{".volta"}, parts: []string{"tools", "image", "node", "*", "bin", "node"}},
	{env: []string{"ASDF_DATA_DIR", "ASDF_DIR"}, fallback: []string{".asdf"}, parts: []string{"installs", "nodejs", "*", "bin", "node"}},
	{env: []string{"MISE_DATA_DIR"}, fallback: []string{".local", "share", "mise"}, parts: []string{"installs", "node", "*", "bin", "node"}},
	{fallback: []string{".nodenv", "versions"}, parts: []string{"*", "bin", "node"}},
	{env: []string{"N_PREFIX"}, parts: []string{"bin", "node"}},
}

// Overridden in tests, where a Node installed at a system prefix would otherwise
// decide the outcome.
var nodeSystemSearchPatterns = []string{
	"/opt/homebrew/opt/node@*/bin/node",
	"/usr/local/opt/node@*/bin/node",
	"/usr/local/n/versions/node/*/bin/node",
	"/opt/homebrew/bin/node",
	"/usr/local/bin/node",
	"/usr/bin/node",
	"/snap/bin/node",
}

// piShimName is the name extensions spawn orb under.
func piShimName(string) string {
	return "pi"
}

func linkExecutable(target, path string) error {
	return os.Symlink(target, path)
}
