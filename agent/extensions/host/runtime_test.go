package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverRuntimePrefersSupportedNode(t *testing.T) {
	directory := isolateRuntimeSearch(t)
	writeRuntimeFixture(t, directory, "node", "v22.13.0")
	writeRuntimeFixture(t, directory, "bun", "1.3.0")
	t.Setenv("PATH", directory)
	runtime := mustDiscover(t)
	if runtime.Name != "node" || runtime.Version != "22.13.0" {
		t.Fatalf("runtime = %#v", runtime)
	}
}

func TestDiscoverRuntimeFallsBackToBunForOldNode(t *testing.T) {
	directory := isolateRuntimeSearch(t)
	writeRuntimeFixture(t, directory, "node", "v22.5.9")
	writeRuntimeFixture(t, directory, "bun", "1.3.0")
	t.Setenv("PATH", directory)
	runtime := mustDiscover(t)
	if runtime.Name != "bun" || runtime.Version != "1.3.0" {
		t.Fatalf("runtime = %#v", runtime)
	}
}

// The override is the escape hatch for a setup no search reaches, so it wins
// over a perfectly good Node on PATH.
func TestDiscoverRuntimeUsesExplicitOverride(t *testing.T) {
	directory := isolateRuntimeSearch(t)
	writeRuntimeFixture(t, directory, "node", "v22.13.0")
	elsewhere := t.TempDir()
	writeRuntimeFixture(t, elsewhere, "node", "v24.4.0")
	t.Setenv("PATH", directory)
	t.Setenv(nodeOverrideEnv, filepath.Join(elsewhere, "node"))
	runtime := mustDiscover(t)
	if runtime.Version != "24.4.0" || runtime.Path != filepath.Join(elsewhere, "node") {
		t.Fatalf("runtime = %#v", runtime)
	}
}

// nvm installs a shell function, so a spawned process inherits a PATH with no
// node at all while the install itself stays exactly where nvm put it.
func TestDiscoverRuntimeFindsVersionManagerInstallWhenPathHasNone(t *testing.T) {
	empty := isolateRuntimeSearch(t)
	t.Setenv("PATH", empty)
	for _, manager := range versionManagerFixtures {
		t.Run(manager.env, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(append([]string{root}, manager.relative...)...)
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeRuntimeFixture(t, binDir, "node", "v22.14.0")
			t.Setenv(manager.env, root)
			defer t.Setenv(manager.env, "")
			runtime := mustDiscover(t)
			if runtime.Name != "node" || runtime.Version != "22.14.0" {
				t.Fatalf("runtime = %#v", runtime)
			}
		})
	}
}

// A Node that cannot compile TypeScript under node_modules is worth using only
// when nothing better is installed, so a capable one elsewhere wins.
func TestDiscoverRuntimePrefersCapableNodeOverUnderCapablePath(t *testing.T) {
	directory := isolateRuntimeSearch(t)
	writeRuntimeFixture(t, directory, "node", "v22.9.0")
	t.Setenv("PATH", directory)
	nvm := t.TempDir()
	nvmEnv, binDir := nvmLayout(nvm, "v22.13.0")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRuntimeFixture(t, binDir, "node", "v22.13.0")
	t.Setenv(nvmEnv, nvm)
	if runtime := mustDiscover(t); runtime.Version != "22.13.0" {
		t.Fatalf("runtime = %#v, want the capable 22.13.0", runtime)
	}
}

// The converse: a capable PATH runtime is the user's choice and ends the search
// even when a newer one is installed elsewhere.
func TestDiscoverRuntimeKeepsCapablePathNode(t *testing.T) {
	directory := isolateRuntimeSearch(t)
	writeRuntimeFixture(t, directory, "node", "v22.13.0")
	t.Setenv("PATH", directory)
	nvm := t.TempDir()
	nvmEnv, binDir := nvmLayout(nvm, "v24.4.0")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRuntimeFixture(t, binDir, "node", "v24.4.0")
	t.Setenv(nvmEnv, nvm)
	if runtime := mustDiscover(t); runtime.Version != "22.13.0" {
		t.Fatalf("runtime = %#v, want the PATH runtime", runtime)
	}
}

// A PATH entry can be a dangling symlink, a shim whose manager is half-installed,
// or a wrapper that prints its own chatter; none of them may end the search.
func TestDiscoverRuntimeSkipsUnusableCandidates(t *testing.T) {
	directory := isolateRuntimeSearch(t)
	broken := t.TempDir()
	if err := os.Symlink(filepath.Join(broken, "missing"), filepath.Join(broken, commandFileName("node"))); err != nil {
		t.Fatal(err)
	}
	failing := t.TempDir()
	writeFakeCommand(t, filepath.Join(failing, "node"),
		"echo 'fnm: no default version set' >&2\nexit 1\n",
		"echo fnm: no default version set 1>&2\nexit /b 1\n")
	writeFakeCommand(t, filepath.Join(directory, "node"), "printf 'Now using node v22.14.0\\n'\n", "echo Now using node v22.14.0\n")
	t.Setenv("PATH", strings.Join([]string{broken, failing, directory}, string(os.PathListSeparator)))
	runtime := mustDiscover(t)
	if runtime.Name != "node" || runtime.Version != "22.14.0" {
		t.Fatalf("runtime = %#v", runtime)
	}
}

func TestPrepareHostEnvironmentExposesRuntimeDirectory(t *testing.T) {
	agentDir := t.TempDir()
	runtimeDir := t.TempDir()
	binary := filepath.Join(t.TempDir(), "orb")
	writeExecutable(t, binary, "#!/bin/sh\n")
	environment, err := prepareHostEnvironment(Options{AgentDir: agentDir, OrbExecutable: binary}, []string{"PATH=/usr/bin"}, filepath.Join(runtimeDir, "node"))
	if err != nil {
		t.Fatal(err)
	}
	entries := filepath.SplitList(environmentValue(environment, "PATH"))
	if len(entries) < 3 || entries[0] != filepath.Join(agentDir, "host", "bin") || entries[1] != runtimeDir {
		t.Fatalf("PATH = %v, want the pi shim then the runtime directory", entries)
	}
}

// DiscoverRuntime consults the locations a real install uses, so a test that
// asserts what it picks must first take the developer's own Node installs out of
// scope. Returns an empty directory to use as PATH.
func isolateRuntimeSearch(t *testing.T) string {
	t.Helper()
	previous := nodeSystemSearchPatterns
	nodeSystemSearchPatterns = nil
	t.Cleanup(func() { nodeSystemSearchPatterns = previous })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{
		nodeOverrideEnv, "NVM_DIR", "FNM_DIR", "VOLTA_HOME", "ASDF_DATA_DIR", "ASDF_DIR", "MISE_DATA_DIR", "N_PREFIX",
		"NVM_HOME", "NVM_SYMLINK", "SCOOP", "APPDATA", "LOCALAPPDATA",
	} {
		t.Setenv(name, "")
	}
	return t.TempDir()
}

func mustDiscover(t *testing.T) Runtime {
	t.Helper()
	runtime, err := DiscoverRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func writeRuntimeFixture(t *testing.T, directory, name, version string) {
	t.Helper()
	writeFakeCommand(t, filepath.Join(directory, name), "printf '%s\\n' '"+version+"'\n", "echo "+version+"\n")
}
