package host

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/orb/codingagent/extensions"
)

func TestEntryNeedsSDKOnlyForBareLooseImports(t *testing.T) {
	root := isolatedTempDir(t)

	bare := filepath.Join(root, "extensions", "probe.ts")
	writeFile(t, bare, `import { getModel } from "@earendil-works/pi-ai";`, 0o644)
	if !entryNeedsSDK(bare) {
		t.Error("a loose file with a bare SDK import needs provisioning")
	}

	historical := filepath.Join(root, "extensions", "old.ts")
	writeFile(t, historical, `import { Text } from '@mariozechner/pi-tui';`, 0o644)
	if !entryNeedsSDK(historical) {
		t.Error("the historical scope is part of the SDK surface")
	}

	plain := filepath.Join(root, "extensions", "plain.ts")
	writeFile(t, plain, `export default function (pi) { pi.registerCommand("x", {}); }`, 0o644)
	if entryNeedsSDK(plain) {
		t.Error("no SDK import, nothing to provision")
	}

	// A packaged extension with an unmaterialized SDK peerDependency needs
	// provisioning like any loose file...
	packaged := filepath.Join(root, "npm", "node_modules", "some-ext", "index.js")
	writeFile(t, packaged, `import "@earendil-works/pi-ai";`, 0o644)
	if !entryNeedsSDK(packaged) {
		t.Error("a packaged extension with no resolvable SDK needs provisioning")
	}
	// ...and one whose tree holds the SDK does not.
	writeFile(t, filepath.Join(root, "npm", "node_modules", "@earendil-works", "pi-coding-agent", "package.json"),
		`{"name":"@earendil-works/pi-coding-agent"}`, 0o644)
	if entryNeedsSDK(packaged) {
		t.Error("a resolvable SDK beside the package wins over provisioning")
	}

	// A loose file whose own tree already holds the SDK resolves without help.
	covered := filepath.Join(root, "project", "ext.ts")
	writeFile(t, covered, `import "@earendil-works/pi-ai";`, 0o644)
	writeFile(t, filepath.Join(root, "project", "node_modules", "@earendil-works", "pi-ai", "package.json"),
		`{"name":"@earendil-works/pi-ai"}`, 0o644)
	if entryNeedsSDK(covered) {
		t.Error("a resolvable copy in the file's own tree wins over provisioning")
	}
}

// provisionFixture returns a manager whose single entry needs the SDK, plus
// the install root provisioning would target.
func provisionFixture(t *testing.T) (*Manager, string) {
	t.Helper()
	agentDir := isolatedTempDir(t)
	entry := filepath.Join(agentDir, "extensions", "probe.ts")
	writeFile(t, entry, `import "@earendil-works/pi-ai";`, 0o644)
	manager := NewManager(Options{AgentDir: agentDir, SDKVersion: "0.81.1"})
	manager.entries = makeEntries([]string{entry})
	return manager, filepath.Join(agentDir, "npm")
}

func stubInstall(t *testing.T, fake func(ctx context.Context, npmPath, root, spec string) error) {
	t.Helper()
	previous := runSDKInstall
	runSDKInstall = fake
	t.Cleanup(func() { runSDKInstall = previous })
}

func TestEnsureSDKProvisionedInstallsThePinExactlyOnce(t *testing.T) {
	manager, installRoot := provisionFixture(t)
	var specs []string
	stubInstall(t, func(_ context.Context, _, root, spec string) error {
		specs = append(specs, spec)
		// A successful install makes the SDK resolvable, like npm would.
		writeFile(t, filepath.Join(root, "node_modules", "@earendil-works", "pi-coding-agent", "package.json"),
			`{"name":"@earendil-works/pi-coding-agent"}`, 0o644)
		return nil
	})
	manager.ensureSDKProvisioned(context.Background(), Runtime{})
	manager.ensureSDKProvisioned(context.Background(), Runtime{}) // second launch: already resolvable
	if len(specs) != 1 || specs[0] != "@earendil-works/pi-coding-agent@0.81.1" {
		t.Fatalf("installs = %v, want exactly one pinned install", specs)
	}
	if managedSDKRoot(manager.options) == "" {
		t.Fatalf("SDK not resolvable from %s after provisioning", installRoot)
	}
}

func TestEnsureSDKProvisionedRespectsOverrideAndOffline(t *testing.T) {
	stubInstall(t, func(context.Context, string, string, string) error {
		t.Fatal("install must not run")
		return nil
	})
	manager, _ := provisionFixture(t)
	t.Setenv(piSDKRootEnv, "/somewhere/explicit")
	manager.ensureSDKProvisioned(context.Background(), Runtime{})
	t.Setenv(piSDKRootEnv, "")
	t.Setenv("PI_OFFLINE", "1")
	manager.ensureSDKProvisioned(context.Background(), Runtime{})
}

func TestEnsureSDKProvisionedReportsInsteadOfFailingTheLaunch(t *testing.T) {
	manager, _ := provisionFixture(t)
	var diagnostics []string
	manager.options.OnDiagnostic = func(d extensions.Diagnostic) { diagnostics = append(diagnostics, d.Message) }
	stubInstall(t, func(context.Context, string, string, string) error {
		return context.DeadlineExceeded
	})
	manager.ensureSDKProvisioned(context.Background(), Runtime{})
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %v, want one warning naming the manual command", diagnostics)
	}
}
