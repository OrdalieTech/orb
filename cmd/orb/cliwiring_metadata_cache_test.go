package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/codingagent/config"
)

// metadataCacheModifiedExtension keeps the registrations of the fixture but
// changes the entry file bytes, which must invalidate the fingerprint.
const metadataCacheModifiedExtension = listModelsProviderExtension + `
export function fingerprintChanged() {}
`

func metadataCachePath(agentDir string) string {
	return filepath.Join(agentDir, "host", "metadata-cache.json")
}

func setupMetadataCacheFixture(t *testing.T) (cwd, agentDir, extension string) {
	t.Helper()
	requireExtensionHostRuntime(t)
	cwd = t.TempDir()
	agentDir = filepath.Join(t.TempDir(), "agent")
	closeExtensionHostOnCleanup(t)
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKE_KEY", "dummy")
	t.Chdir(cwd)
	extension = writeJSExtension(t, filepath.Join(cwd, "ext"), listModelsProviderExtension)
	return cwd, agentDir, extension
}

func runListModelsCLI(t *testing.T, extension string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"--list-models", "fake", "-e", extension}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{})
	return code, stdout.String(), stderr.String()
}

// A warm metadata cache serves --list-models without any JS runtime
// (ORB_NODE=none forbids spawning one) and the output is byte-identical to the
// cold, host-spawning run.
func TestListModelsMetadataCacheWarmMatchesCold(t *testing.T) {
	_, agentDir, extension := setupMetadataCacheFixture(t)

	code, cold, coldErr := runListModelsCLI(t, extension)
	if code != 0 {
		t.Fatalf("cold exit=%d stdout=%q stderr=%q", code, cold, coldErr)
	}
	if !strings.Contains(cold, "fakeprov") || !strings.Contains(cold, "fake-1") {
		t.Fatalf("cold run missing extension provider:\n%s", cold)
	}
	// The snapshot is written in the background; closing the host is the
	// documented wait point (runCLI closes it on the way out, this test drives
	// runCLIWithDependencies directly).
	replaceActiveExtensionHost(nil)
	if _, err := os.Stat(metadataCachePath(agentDir)); err != nil {
		t.Fatalf("cold run did not write the metadata cache: %v", err)
	}

	t.Setenv("ORB_NODE", "none")
	code, warm, warmErr := runListModelsCLI(t, extension)
	if code != 0 {
		t.Fatalf("warm exit=%d stdout=%q stderr=%q", code, warm, warmErr)
	}
	if warm != cold {
		t.Fatalf("warm-cache output diverged from cold run:\ncold:\n%s\nwarm:\n%s", cold, warm)
	}
}

// Modifying the extension entry file invalidates the fingerprint: with
// ORB_NODE=none the fallback spawn cannot run, so the extension provider must
// be absent — proof the stale cache was not consumed — while the command still
// completes without erroring out.
func TestListModelsMetadataCacheInvalidatedByEntryChange(t *testing.T) {
	_, agentDir, extension := setupMetadataCacheFixture(t)

	code, cold, coldErr := runListModelsCLI(t, extension)
	if code != 0 {
		t.Fatalf("cold exit=%d stdout=%q stderr=%q", code, cold, coldErr)
	}
	if !strings.Contains(cold, "fake-1") {
		t.Fatalf("cold run missing extension provider:\n%s", cold)
	}
	replaceActiveExtensionHost(nil)
	if _, err := os.Stat(metadataCachePath(agentDir)); err != nil {
		t.Fatalf("cold run did not write the metadata cache: %v", err)
	}

	// The rewrite changes the entry's size and bumps its mtime: the fingerprint
	// stamps entries (size, mtime) rather than hashing them.
	if err := os.WriteFile(extension, []byte(metadataCacheModifiedExtension), 0o644); err != nil {
		t.Fatal(err)
	}
	touched := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(extension, touched, touched); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORB_NODE", "none")
	code, warm, _ := runListModelsCLI(t, extension)
	if code != 0 {
		t.Fatalf("warm exit=%d stdout=%q", code, warm)
	}
	if strings.Contains(warm, "fake-1") {
		t.Fatalf("stale metadata cache was consumed after the entry file changed:\n%s", warm)
	}
}

// A corrupted cache file is a miss, never an error: the command falls back to
// the spawn path (here unavailable via ORB_NODE=none) and still exits 0.
func TestListModelsMetadataCacheCorruptFallsBackToSpawn(t *testing.T) {
	_, agentDir, extension := setupMetadataCacheFixture(t)

	code, cold, coldErr := runListModelsCLI(t, extension)
	if code != 0 {
		t.Fatalf("cold exit=%d stdout=%q stderr=%q", code, cold, coldErr)
	}
	replaceActiveExtensionHost(nil)

	if err := os.WriteFile(metadataCachePath(agentDir), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORB_NODE", "none")
	code, warm, _ := runListModelsCLI(t, extension)
	if code != 0 {
		t.Fatalf("corrupt-cache run errored out: exit=%d stdout=%q", code, warm)
	}
	if strings.Contains(warm, "fake-1") {
		t.Fatalf("corrupt metadata cache was consumed:\n%s", warm)
	}
}
