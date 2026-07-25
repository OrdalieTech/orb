package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeAtLeast226(t *testing.T) {
	for _, version := range []string{"22.6.0", "22.12.1", "23.0.0", "24.1.0-nightly"} {
		if !nodeAtLeast226(version) {
			t.Errorf("nodeAtLeast226(%q) = false", version)
		}
	}
	for _, version := range []string{"", "22", "22.5.9", "21.99.0", "dev"} {
		if nodeAtLeast226(version) {
			t.Errorf("nodeAtLeast226(%q) = true", version)
		}
	}
}

func TestParseRuntimeVersion(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		output string
		want   string
	}{
		{"node", "v22.13.0\n", "22.13.0"},
		{"bun", "1.3.0\n", "1.3.0"},
		// A version-manager shim forwards the version with its own chatter around
		// it, and a wrapper can announce the switch it just performed.
		{"chatty shim", "Now using node v22.14.1\n", "22.14.1"},
		{"trailing note", "v24.2.0\nsome note\n", "24.2.0"},
		{"nightly", "v25.0.0-nightly20240101\n", "25.0.0-nightly20240101"},
		{"major only", "v22\n", ""},
		{"no version", "command not found\n", ""},
		{"empty", "", ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseRuntimeVersion(testCase.output)
			if testCase.want == "" {
				if err == nil {
					t.Fatalf("parseRuntimeVersion(%q) = %q, want an error", testCase.output, got)
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Fatalf("parseRuntimeVersion(%q) = %q, %v; want %q", testCase.output, got, err, testCase.want)
			}
		})
	}
}

func TestVersionNewer(t *testing.T) {
	for _, testCase := range []struct {
		candidate string
		current   string
		want      bool
	}{
		{"22.13.0", "", true},
		{"22.13.0", "22.9.0", true},
		{"22.9.0", "22.13.0", false},
		{"24.0.0", "22.13.0", true},
		{"22.13.1", "22.13.0", true},
		{"22.13.0", "22.13.0", false},
		{"garbage", "22.13.0", false},
	} {
		if got := versionNewer(testCase.candidate, testCase.current); got != testCase.want {
			t.Errorf("versionNewer(%q, %q) = %v, want %v", testCase.candidate, testCase.current, got, testCase.want)
		}
	}
}

func TestNodeRuntimeArgs(t *testing.T) {
	accepting := filepath.Join(t.TempDir(), "node")
	writeExecutable(t, accepting, "#!/bin/sh\nexit 0\n")
	// Node 26 removed --experimental-transform-types and aborts on it, taking the
	// whole host down; 22.6 predates the flag and aborts the same way.
	rejecting := filepath.Join(t.TempDir(), "node")
	writeExecutable(t, rejecting, "#!/bin/sh\ncase \"$1\" in --experimental-transform-types) echo 'bad option' >&2; exit 9;; esac\nexit 0\n")

	if got := strings.Join(nodeRuntimeArgs(t.Context(), accepting, "22.6.0"), " "); strings.Contains(got, "transform-types") {
		t.Fatalf("22.6 arguments = %q, want no transform-types", got)
	}
	if got := strings.Join(nodeRuntimeArgs(t.Context(), rejecting, "26.0.0"), " "); strings.Contains(got, "transform-types") {
		t.Fatalf("Node 26 arguments = %q, want no transform-types", got)
	}
	for _, version := range []string{"22.7.0", "22.13.0", "24.0.0"} {
		if got := strings.Join(nodeRuntimeArgs(t.Context(), accepting, version), " "); !strings.Contains(got, "--experimental-transform-types") {
			t.Fatalf("%s arguments = %q, want transform-types", version, got)
		}
	}
	// --preserve-symlinks existed only to keep the staged entry links opaque, and
	// it breaks pnpm: a package reached through a store symlink then resolves its
	// dependencies from the link site instead of the store.
	if got := strings.Join(nodeRuntimeArgs(t.Context(), accepting, "24.0.0"), " "); strings.Contains(got, "preserve-symlinks") {
		t.Fatalf("arguments = %q, want no preserve-symlinks", got)
	}
}

// PIGO_NODE=none is the way to keep pigo from running JavaScript extensions at
// all, and it must read as "no runtime" rather than as a broken override.
func TestDiscoverRuntimeHonoursDisabledOverride(t *testing.T) {
	directory := isolateRuntimeSearch(t)
	writeRuntimeFixture(t, directory, "node", "v22.13.0")
	t.Setenv("PATH", directory)
	t.Setenv(nodeOverrideEnv, nodeOverrideDisabled)
	_, err := DiscoverRuntime(context.Background())
	var unavailable *RuntimeUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %#v, want RuntimeUnavailableError", err)
	}
}

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

func TestDiscoverRuntimeReturnsTypedError(t *testing.T) {
	directory := isolateRuntimeSearch(t)
	writeRuntimeFixture(t, directory, "node", "v22.5.9")
	t.Setenv("PATH", directory)
	_, err := DiscoverRuntime(context.Background())
	var unavailable *RuntimeUnavailableError
	if !errors.As(err, &unavailable) || unavailable.NodeVersion != "22.5.9" {
		t.Fatalf("error = %#v", err)
	}
	if unavailable.Diagnostic().Message != runtimeUnavailableMessage {
		t.Fatalf("diagnostic = %#v", unavailable.Diagnostic())
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

// Silently ignoring a broken override reproduces the confusion it exists to end,
// so it is reported instead of being replaced by the PATH runtime.
func TestDiscoverRuntimeReportsUnusableOverride(t *testing.T) {
	directory := isolateRuntimeSearch(t)
	writeRuntimeFixture(t, directory, "node", "v22.13.0")
	t.Setenv("PATH", directory)
	for _, override := range []string{filepath.Join(t.TempDir(), "absent"), mustOldNode(t)} {
		t.Setenv(nodeOverrideEnv, override)
		runtime, err := DiscoverRuntime(context.Background())
		if err == nil {
			t.Fatalf("override %q silently resolved to %#v", override, runtime)
		}
		if !strings.Contains(err.Error(), nodeOverrideEnv) {
			t.Fatalf("error = %q, want it to name %s", err, nodeOverrideEnv)
		}
	}
}

// nvm installs a shell function, so a spawned process inherits a PATH with no
// node at all while the install itself stays exactly where nvm put it.
func TestDiscoverRuntimeFindsVersionManagerInstallWhenPathHasNone(t *testing.T) {
	empty := isolateRuntimeSearch(t)
	t.Setenv("PATH", empty)
	for _, manager := range []struct {
		env      string
		relative []string
	}{
		{"NVM_DIR", []string{"versions", "node", "v22.14.0", "bin"}},
		{"FNM_DIR", []string{"node-versions", "v22.14.0", "installation", "bin"}},
		{"VOLTA_HOME", []string{"tools", "image", "node", "22.14.0", "bin"}},
		{"ASDF_DATA_DIR", []string{"installs", "nodejs", "22.14.0", "bin"}},
		{"MISE_DATA_DIR", []string{"installs", "node", "22.14.0", "bin"}},
	} {
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
	binDir := filepath.Join(nvm, "versions", "node", "v22.13.0", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRuntimeFixture(t, binDir, "node", "v22.13.0")
	t.Setenv("NVM_DIR", nvm)
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
	binDir := filepath.Join(nvm, "versions", "node", "v24.4.0", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRuntimeFixture(t, binDir, "node", "v24.4.0")
	t.Setenv("NVM_DIR", nvm)
	if runtime := mustDiscover(t); runtime.Version != "22.13.0" {
		t.Fatalf("runtime = %#v, want the PATH runtime", runtime)
	}
}

// A PATH entry can be a dangling symlink, a shim whose manager is half-installed,
// or a wrapper that prints its own chatter; none of them may end the search.
func TestDiscoverRuntimeSkipsUnusableCandidates(t *testing.T) {
	directory := isolateRuntimeSearch(t)
	broken := t.TempDir()
	if err := os.Symlink(filepath.Join(broken, "missing"), filepath.Join(broken, "node")); err != nil {
		t.Fatal(err)
	}
	failing := t.TempDir()
	writeExecutable(t, filepath.Join(failing, "node"), "#!/bin/sh\necho 'fnm: no default version set' >&2\nexit 1\n")
	writeExecutable(t, filepath.Join(directory, "node"), "#!/bin/sh\nprintf 'Now using node v22.14.0\\n'\n")
	t.Setenv("PATH", strings.Join([]string{broken, failing, directory}, string(os.PathListSeparator)))
	runtime := mustDiscover(t)
	if runtime.Name != "node" || runtime.Version != "22.14.0" {
		t.Fatalf("runtime = %#v", runtime)
	}
}

func TestPrepareHostEnvironmentExposesRuntimeDirectory(t *testing.T) {
	agentDir := t.TempDir()
	runtimeDir := t.TempDir()
	binary := filepath.Join(t.TempDir(), "pigo")
	writeExecutable(t, binary, "#!/bin/sh\n")
	environment, err := prepareHostEnvironment(agentDir, []string{"PATH=/usr/bin"}, binary, filepath.Join(runtimeDir, "node"))
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
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{nodeOverrideEnv, "NVM_DIR", "FNM_DIR", "VOLTA_HOME", "ASDF_DATA_DIR", "ASDF_DIR", "MISE_DATA_DIR", "N_PREFIX"} {
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

func mustOldNode(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	writeRuntimeFixture(t, directory, "node", "v20.11.0")
	return filepath.Join(directory, "node")
}

func writeRuntimeFixture(t *testing.T, directory, name, version string) {
	t.Helper()
	writeExecutable(t, filepath.Join(directory, name), "#!/bin/sh\nprintf '%s\\n' '"+version+"'\n")
}
