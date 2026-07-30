package host

import (
	"os"
	"path/filepath"
	"testing"
)

// The expected locations are spelled out here rather than computed from the
// accessor the code uses, so the test pins the layout of a real tree
// (~/.pi/agent/npm/node_modules/...) instead of restating the implementation.
func userSDKPath(agentDir string) string {
	return filepath.Join(agentDir, "npm", "node_modules", "@earendil-works", "pi-coding-agent")
}

func projectSDKPath(cwd string) string {
	return filepath.Join(cwd, ".pi", "npm", "node_modules", "@earendil-works", "pi-coding-agent")
}

func writeSDKPackage(t *testing.T, directory, name string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(directory, "package.json"), `{"name":"`+name+`","version":"0.81.1"}`)
	writeFixtureFile(t, filepath.Join(directory, "dist", "index.js"), "export const sdk = true;\n")
}

func TestManagedSDKRootResolvesOnlyOrbsOwnNpmRoots(t *testing.T) {
	cases := []struct {
		name    string
		trusted bool
		build   func(t *testing.T, cwd, agentDir string)
		want    func(cwd, agentDir string) string
	}{
		{
			name: "user root",
			build: func(t *testing.T, _, agentDir string) {
				writeSDKPackage(t, userSDKPath(agentDir), piSDKPackage)
			},
			want: func(_, agentDir string) string { return userSDKPath(agentDir) },
		},
		{
			name:    "project root wins over user root when trusted",
			trusted: true,
			build: func(t *testing.T, cwd, agentDir string) {
				writeSDKPackage(t, userSDKPath(agentDir), piSDKPackage)
				writeSDKPackage(t, projectSDKPath(cwd), piSDKPackage)
			},
			want: func(cwd, _ string) string { return projectSDKPath(cwd) },
		},
		{
			name: "project root is invisible until the project is trusted",
			build: func(t *testing.T, cwd, agentDir string) {
				writeSDKPackage(t, userSDKPath(agentDir), piSDKPackage)
				writeSDKPackage(t, projectSDKPath(cwd), piSDKPackage)
			},
			want: func(_, agentDir string) string { return userSDKPath(agentDir) },
		},
		{
			name: "untrusted project SDK is not a substitute for a missing user SDK",
			build: func(t *testing.T, cwd, _ string) {
				writeSDKPackage(t, projectSDKPath(cwd), piSDKPackage)
			},
			want: func(string, string) string { return "" },
		},
		{
			name:  "no npm root at all",
			build: func(*testing.T, string, string) {},
			want:  func(string, string) string { return "" },
		},
		{
			name: "npm root without the SDK",
			build: func(t *testing.T, _, agentDir string) {
				writeFixtureFile(t, filepath.Join(agentDir, "npm", "package.json"), `{"name":"pi-extensions","private":true}`)
				writeSDKPackage(t, filepath.Join(agentDir, "npm", "node_modules", "some-extension"), "some-extension")
			},
			want: func(string, string) string { return "" },
		},
		{
			name: "half-written package directory",
			build: func(t *testing.T, _, agentDir string) {
				writeFixtureFile(t, filepath.Join(userSDKPath(agentDir), "dist", "index.js"), "export const sdk = true;\n")
			},
			want: func(string, string) string { return "" },
		},
		{
			name: "package.json truncated mid-write",
			build: func(t *testing.T, _, agentDir string) {
				writeFixtureFile(t, filepath.Join(userSDKPath(agentDir), "package.json"), `{"name":"@earendil-w`)
			},
			want: func(string, string) string { return "" },
		},
		{
			name: "directory holding a different package",
			build: func(t *testing.T, _, agentDir string) {
				writeSDKPackage(t, userSDKPath(agentDir), "@earendil-works/pi-ai")
			},
			want: func(string, string) string { return "" },
		},
		{
			name: "symlinked package directory",
			build: func(t *testing.T, _, agentDir string) {
				target := filepath.Join(t.TempDir(), "pi-coding-agent")
				writeSDKPackage(t, target, piSDKPackage)
				link := userSDKPath(agentDir)
				if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			},
			want: func(_, agentDir string) string { return userSDKPath(agentDir) },
		},
		{
			name: "unreadable npm root",
			build: func(t *testing.T, _, agentDir string) {
				if os.Geteuid() == 0 {
					t.Skip("root ignores directory permissions")
				}
				writeSDKPackage(t, userSDKPath(agentDir), piSDKPackage)
				modules := filepath.Join(agentDir, "npm", "node_modules")
				if err := os.Chmod(modules, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(modules, 0o700) })
			},
			want: func(string, string) string { return "" },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cwd, agentDir := t.TempDir(), t.TempDir()
			testCase.build(t, cwd, agentDir)
			options := Options{AgentDir: agentDir, CWD: cwd, ProjectTrusted: testCase.trusted}
			if got := managedSDKRoot(options); got != testCase.want(cwd, agentDir) {
				t.Fatalf("managedSDKRoot = %q, want %q", got, testCase.want(cwd, agentDir))
			}
		})
	}
}

// The rule this change exists to enforce: whatever `pi` is installed on the
// machine, however complete it looks, orb resolves the SDK from its own npm
// root or from nothing at all.
func TestPrepareHostEnvironmentNeverConsultsInstalledPi(t *testing.T) {
	installPi := func(t *testing.T) string {
		t.Helper()
		prefix := t.TempDir()
		installed := filepath.Join(prefix, "lib", "node_modules", "@earendil-works", "pi-coding-agent")
		writeSDKPackage(t, installed, piSDKPackage)
		writeFixtureFile(t, filepath.Join(installed, "dist", "cli.js"), "#!/usr/bin/env node\n")
		binDir := filepath.Join(prefix, "bin")
		if err := os.MkdirAll(binDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(installed, "dist", "cli.js"), filepath.Join(binDir, "pi")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(installed, "dist", "cli.js"), 0o755); err != nil {
			t.Fatal(err)
		}
		return binDir
	}

	for _, testCase := range []struct {
		name    string
		install bool
		want    func(agentDir string) string
	}{
		{name: "no SDK in orb's root", want: func(string) string { return "" }},
		{name: "SDK in orb's root", install: true, want: userSDKPath},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			agentDir, binary := t.TempDir(), filepath.Join(t.TempDir(), "orb")
			writeExecutable(t, binary, "#!/bin/sh\n")
			if testCase.install {
				writeSDKPackage(t, userSDKPath(agentDir), piSDKPackage)
			}
			// Both the inherited environment and the process environment carry the
			// installed pi, so neither lookup route can miss it.
			piBin := installPi(t)
			t.Setenv("PATH", piBin)
			environment, err := prepareHostEnvironment(
				Options{AgentDir: agentDir, OrbExecutable: binary},
				[]string{"PATH=" + piBin},
				"",
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := environmentValue(environment, piSDKRootEnv); got != testCase.want(agentDir) {
				t.Fatalf("%s = %q, want %q", piSDKRootEnv, got, testCase.want(agentDir))
			}
		})
	}
}

// An explicitly set ORB_PI_SDK_ROOT is a deliberate escape hatch, so it
// outranks orb's own root instead of merely filling in for it.
func TestPrepareHostEnvironmentHonoursExplicitSDKRootOverride(t *testing.T) {
	agentDir, binary := t.TempDir(), filepath.Join(t.TempDir(), "orb")
	writeExecutable(t, binary, "#!/bin/sh\n")
	writeSDKPackage(t, userSDKPath(agentDir), piSDKPackage)
	override := t.TempDir()

	environment, err := prepareHostEnvironment(
		Options{AgentDir: agentDir, OrbExecutable: binary},
		[]string{piSDKRootEnv + "=" + override},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := environmentValue(environment, piSDKRootEnv); got != override {
		t.Fatalf("%s = %q, want the override %q", piSDKRootEnv, got, override)
	}
}

// Both layouts orb's own installs produce: dependencies nested under the SDK
// package (native tarball extraction, then npm run inside that directory) and
// dependencies hoisted beside it (npm run in the install root itself).
func TestResolveRuntimeSDKCoversNestedAndHoistedInstalls(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		relative []string
		found    bool
	}{
		{name: "nested under the SDK", relative: []string{"@earendil-works", "pi-coding-agent", "node_modules", "@earendil-works", "pi-ai"}, found: true},
		{name: "hoisted beside the SDK", relative: []string{"@earendil-works", "pi-ai"}, found: true},
		{name: "absent", relative: nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			modules := filepath.Join(t.TempDir(), "npm", "node_modules")
			writeSDKPackage(t, filepath.Join(modules, "@earendil-works", "pi-coding-agent"), piSDKPackage)
			want := ""
			if testCase.relative != nil {
				want = filepath.Join(append([]string{modules}, testCase.relative...)...)
				writeSDKPackage(t, want, "@earendil-works/pi-ai")
			}
			if got := resolveRuntimeSDK(modules, "@earendil-works/pi-ai"); got != want {
				t.Fatalf("resolveRuntimeSDK = %q, want %q", got, want)
			}
		})
	}
}
