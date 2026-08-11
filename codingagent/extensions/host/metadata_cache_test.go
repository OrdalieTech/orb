package host

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFingerprintFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two entries of one npm package share a listing root, so the fingerprint walks
// that tree once instead of once per entry — the duplicate passes only burned
// the shared file budget, and tripping it disables the cache entirely.
func TestMetadataListingRootsDedupeSharedPackageRoot(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	packageDir := filepath.Join(cwd, "ext")
	writeFingerprintFile(t, filepath.Join(packageDir, "package.json"), `{"name":"ext"}`)
	first := filepath.Join(packageDir, "first.ts")
	second := filepath.Join(packageDir, "nested", "second.ts")
	writeFingerprintFile(t, first, "export default function () {}\n")
	writeFingerprintFile(t, second, "export default function () {}\n")

	roots := metadataListingRoots([]string{first, second}, cwd, agentDir)
	if len(roots) != 1 || roots[0] != packageDir {
		t.Fatalf("listing roots = %v, want [%s]", roots, packageDir)
	}
}

// A project extension in a JS repo resolves to the repo's own package.json;
// listing the checkout on every metadata command is exactly what the budget cap
// forbids, so the entry's own directory stands in (and nothing is listed when
// that directory is the cwd).
func TestMetadataListingRootNeverListsProjectCWD(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	writeFingerprintFile(t, filepath.Join(cwd, "package.json"), `{"name":"repo"}`)
	nested := filepath.Join(cwd, "ext", "index.ts")
	writeFingerprintFile(t, nested, "export default function () {}\n")
	top := filepath.Join(cwd, "index.ts")
	writeFingerprintFile(t, top, "export default function () {}\n")

	if root := metadataListingRoot(nested, cwd, agentDir); root != filepath.Dir(nested) {
		t.Fatalf("listing root = %q, want %q", root, filepath.Dir(nested))
	}
	if root := metadataListingRoot(top, cwd, agentDir); root != "" {
		t.Fatalf("listing root = %q, want no listing", root)
	}
}

// The fingerprint is stable for an unchanged extension set, ignores .git churn,
// and still changes when the entry or a package file does.
func TestMetadataFingerprintStabilityAndInvalidation(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	packageDir := filepath.Join(cwd, "ext")
	entry := filepath.Join(packageDir, "index.ts")
	dependency := filepath.Join(packageDir, "node_modules", "dep", "index.js")
	writeFingerprintFile(t, filepath.Join(packageDir, "package.json"), `{"name":"ext"}`)
	writeFingerprintFile(t, entry, "export default function () {}\n")
	writeFingerprintFile(t, dependency, "export const value = 1;\n")
	params := MetadataCacheParams{AgentDir: agentDir, CWD: cwd, Paths: []string{entry}}

	baseline, err := metadataFingerprint(params)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := metadataFingerprint(params)
	if err != nil {
		t.Fatal(err)
	}
	if repeat != baseline {
		t.Fatalf("fingerprint is not stable: %s != %s", repeat, baseline)
	}

	writeFingerprintFile(t, filepath.Join(packageDir, ".git", "HEAD"), "ref: refs/heads/main\n")
	withGit, err := metadataFingerprint(params)
	if err != nil {
		t.Fatal(err)
	}
	if withGit != baseline {
		t.Fatal("fingerprint changed after a .git write; the walk must skip .git")
	}

	writeFingerprintFile(t, dependency, "export const value = 2222;\n")
	withDependency, err := metadataFingerprint(params)
	if err != nil {
		t.Fatal(err)
	}
	if withDependency == baseline {
		t.Fatal("fingerprint survived a dependency edit")
	}

	writeFingerprintFile(t, entry, "export default function () { return 1; }\n")
	withEntry, err := metadataFingerprint(params)
	if err != nil {
		t.Fatal(err)
	}
	if withEntry == withDependency {
		t.Fatal("fingerprint survived an entry edit")
	}
}
