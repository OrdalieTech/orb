//go:build windows

package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeCommand writes a batch file whose body is cmd, next to path, and
// returns its path; exec and PATH lookups find it through PATHEXT as they
// find a real .exe.
func writeFakeCommand(t *testing.T, path, _, cmd string) string {
	t.Helper()
	path += ".cmd"
	writeExecutable(t, path, "@echo off\r\n"+strings.ReplaceAll(cmd, "\n", "\r\n"))
	return path
}

// commandFileName is the on-disk name writeFakeCommand gives name.
func commandFileName(name string) string {
	return name + ".cmd"
}

func shellCommand(line string) *exec.Cmd {
	return exec.Command("cmd", "/d", "/c", line)
}

var shellSearchPath = filepath.Join(os.Getenv("SystemRoot"), "System32")

// realTempDir expands the 8.3 short names t.TempDir inherits from %TEMP%:
// Node's realpath keeps them while filepath.EvalSymlinks expands them, so a
// path compared across the two must not contain any.
func realTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// nvmLayout is where nvm-windows installs version below root, and the variable naming root.
func nvmLayout(root, version string) (string, string) {
	return "NVM_HOME", filepath.Join(root, version)
}

var versionManagerFixtures = []struct {
	env      string
	relative []string
}{
	{"NVM_HOME", []string{"v22.14.0"}},
	{"NVM_SYMLINK", nil},
	{"FNM_DIR", []string{"node-versions", "v22.14.0", "installation"}},
	{"VOLTA_HOME", []string{"tools", "image", "node", "22.14.0"}},
	{"MISE_DATA_DIR", []string{"installs", "node", "22.14.0"}},
	{"SCOOP", []string{"apps", "nodejs", "current"}},
}
