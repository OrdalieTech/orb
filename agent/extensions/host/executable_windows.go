//go:build windows

package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolveExecutable reports the file Windows runs for path: path itself when
// it carries an extension, else path plus the first PATHEXT extension present,
// the lookup cmd.exe performs. Windows has no exec permission bits.
func resolveExecutable(path string) (string, bool) {
	resolved, err := exec.LookPath(path)
	return resolved, err == nil
}

func hasPathSeparator(name string) bool {
	return strings.ContainsAny(name, `:\/`)
}

func environmentNameEqual(left, right string) bool {
	return strings.EqualFold(left, right)
}

// nodeSearchCandidate maps a nodeSearchPatterns match to the Node it names:
// win32 patterns match the install directory, and PATHEXT picks node.exe (or
// a manager's node.cmd) inside it.
func nodeSearchCandidate(match string) string {
	return filepath.Join(match, "node")
}

// nodeInstallLayouts are the win32 version-manager install trees: nvm-windows
// (NVM_HOME, NVM_SYMLINK), fnm, Volta, mise and Scoop, each at the root its
// installer records or at its documented default under %APPDATA%,
// %LOCALAPPDATA% or the profile.
var nodeInstallLayouts = []nodeInstallLayout{
	{env: []string{"NVM_HOME"}, base: "APPDATA", fallback: []string{"nvm"}, parts: []string{"v*"}},
	{env: []string{"NVM_SYMLINK"}},
	{env: []string{"FNM_DIR"}, base: "APPDATA", fallback: []string{"fnm"}, parts: []string{"node-versions", "*", "installation"}},
	{env: []string{"VOLTA_HOME"}, base: "LOCALAPPDATA", fallback: []string{"Volta"}, parts: []string{"tools", "image", "node", "*"}},
	{env: []string{"MISE_DATA_DIR"}, base: "LOCALAPPDATA", fallback: []string{"mise"}, parts: []string{"installs", "node", "*"}},
	{env: []string{"SCOOP"}, fallback: []string{"scoop"}, parts: []string{"apps", "nodejs", "current"}},
	{env: []string{"SCOOP"}, fallback: []string{"scoop"}, parts: []string{"apps", "nodejs-lts", "current"}},
}

// Overridden in tests, where a Node installed at a system prefix would otherwise
// decide the outcome.
var nodeSystemSearchPatterns = programFilesDirectories("nodejs")

func programFilesDirectories(name string) []string {
	var directories []string
	for _, variable := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if root := strings.TrimSpace(os.Getenv(variable)); root != "" {
			directories = append(directories, filepath.Join(root, name))
		}
	}
	return directories
}

// piShimName keeps the target's extension: PATH lookups and CreateProcess
// only run a file whose extension is in PATHEXT.
func piShimName(executable string) string {
	return "pi" + filepath.Ext(executable)
}

// Symbolic links need Developer Mode or elevation on Windows; a hard link is
// the unprivileged fallback on the same volume.
func linkExecutable(target, path string) error {
	symlinkErr := os.Symlink(target, path)
	if symlinkErr == nil {
		return nil
	}
	if linkErr := os.Link(target, path); linkErr != nil {
		return errors.Join(symlinkErr, linkErr)
	}
	return nil
}
