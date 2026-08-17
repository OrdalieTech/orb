package host

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded orb-extension-sdk is the only pi SDK surface orb ships: the
// tree must not smuggle in an npm-installed package — no node_modules, no
// scoped package directories.
func TestEmbeddedSDKTreeCarriesNoRealPiPackages(t *testing.T) {
	err := fs.WalkDir(sdkFS, "sdk", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, fragment := range []string{"node_modules", "@earendil-works", "@mariozechner"} {
			if strings.Contains(path, fragment) {
				t.Errorf("embedded SDK entry %q contains %q", path, fragment)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The rule this lane enforces: whatever `pi` is installed on the machine, and
// whatever survives in orb's historic npm roots, the host environment carries
// no real-SDK resolution machinery. ORB_PI_SDK_ROOT is dead — the embedded SDK
// root (ORB_EXTENSION_SDK_ROOT, set at host start) is the only SDK pointer.
func TestPrepareHostEnvironmentCarriesNoRealSDKMachinery(t *testing.T) {
	agentDir, binary := t.TempDir(), filepath.Join(t.TempDir(), "orb")
	writeExecutable(t, binary, "#!/bin/sh\n")

	// A complete-looking pi install on PATH and a leftover SDK copy in the
	// user npm root the deleted auto-provisioning used to target.
	leftover := filepath.Join(agentDir, "npm", "node_modules", "@earendil-works", "pi-coding-agent")
	writeFixtureFile(t, filepath.Join(leftover, "package.json"), `{"name":"@earendil-works/pi-coding-agent","version":"0.84.1"}`)
	writeFixtureFile(t, filepath.Join(leftover, "dist", "index.js"), "export const sdk = true;\n")
	prefix := t.TempDir()
	installed := filepath.Join(prefix, "lib", "node_modules", "@earendil-works", "pi-coding-agent")
	writeFixtureFile(t, filepath.Join(installed, "package.json"), `{"name":"@earendil-works/pi-coding-agent","version":"0.84.1"}`)
	writeFixtureFile(t, filepath.Join(installed, "dist", "cli.js"), "#!/usr/bin/env node\n")
	binDir := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(installed, "dist", "cli.js"), filepath.Join(binDir, "pi")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	environment, err := prepareHostEnvironment(
		Options{AgentDir: agentDir, OrbExecutable: binary},
		[]string{"PATH=" + binDir},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range environment {
		if strings.HasPrefix(entry, "ORB_PI_SDK_ROOT=") {
			t.Fatalf("host environment still carries the dead SDK root: %q", entry)
		}
	}
}

// End to end on the real host: an extension importing the legacy SDK names
// loads from the embedded SDK alone, and afterwards no @earendil-works (or
// historical-scope) package is installed anywhere in the environment the run
// produced — the npm auto-provisioning is gone, not merely dormant.
func TestProductEnvironmentHasNoRealSDKInstall(t *testing.T) {
	runtime := requireNamedRuntime(t, "node")
	extensionDir := t.TempDir()
	entry := filepath.Join(extensionDir, "extension.mjs")
	writeFixtureFile(t, entry, `import { modelsAreEqual } from "@earendil-works/pi-ai";

export default function (pi) {
	pi.registerTool({
		name: "no_install_probe",
		label: "No Install Probe",
		description: String(modelsAreEqual({ id: "m", provider: "p" }, { id: "m", provider: "p" })),
		parameters: { type: "object", properties: {} },
		async execute() {
			return { content: [{ type: "text", text: "ok" }] };
		},
	});
}
`)
	runner, result, agentDir := startRuntimeManager(t, runtime, entry)
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	definition := runner.ToolDefinition("no_install_probe")
	if definition == nil {
		t.Fatal("no_install_probe was not registered")
	}
	if definition.Description != "true" {
		t.Fatalf("description = %q, want %q", definition.Description, "true")
	}
	for _, root := range []string{agentDir, extensionDir} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "@earendil-works" || d.Name() == "@mariozechner") {
				t.Errorf("a real SDK scope directory exists at %q", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
