//go:build !windows

package host

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// writeFakeCommand writes an executable whose POSIX shell body is sh and
// returns the path to run it by.
func writeFakeCommand(t *testing.T, path, sh, _ string) string {
	t.Helper()
	writeExecutable(t, path, "#!/bin/sh\n"+sh)
	return path
}

// commandFileName is the on-disk name writeFakeCommand gives name.
func commandFileName(name string) string {
	return name
}

func shellCommand(line string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", line)
}

const shellSearchPath = "/usr/bin:/bin"

// realTempDir is t.TempDir: POSIX has no short path names for Node's
// realpath to leave unexpanded.
func realTempDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// nvmLayout is where nvm installs version below root, and the variable naming root.
func nvmLayout(root, version string) (string, string) {
	return "NVM_DIR", filepath.Join(root, "versions", "node", version, "bin")
}

var versionManagerFixtures = []struct {
	env      string
	relative []string
}{
	{"NVM_DIR", []string{"versions", "node", "v22.14.0", "bin"}},
	{"FNM_DIR", []string{"node-versions", "v22.14.0", "installation", "bin"}},
	{"VOLTA_HOME", []string{"tools", "image", "node", "22.14.0", "bin"}},
	{"ASDF_DATA_DIR", []string{"installs", "nodejs", "22.14.0", "bin"}},
	{"MISE_DATA_DIR", []string{"installs", "node", "22.14.0", "bin"}},
}
